package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
)

type streamFinishFault struct {
	operation       nativetool.ID
	fault           transactionCommitter
	used            atomic.Bool
	cleanupFailures atomic.Int32
	physical        egressaudit.Attempt
}

func (f *streamFinishFault) Commit(tx *sql.Tx) error {
	var state string
	if err := tx.QueryRow(`SELECT state FROM native_tool_operations WHERE operation_id=?`, f.operation).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return tx.Commit()
		}
		return err
	}
	if f.used.Load() {
		if state == "uncertain" && f.cleanupFailures.Add(-1) >= 0 {
			return (rollbackThenError{}).Commit(tx)
		}
		return tx.Commit()
	}
	record, err := scanEgressAttempt(tx.QueryRow(nativeDeliverySelect+` WHERE operation_id=?`, f.operation))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && record.Attempt.Terminal() && f.used.CompareAndSwap(false, true) {
		f.physical = record.Attempt
		return f.fault.Commit(tx)
	}
	return tx.Commit()
}

type eofHookBody struct {
	*strings.Reader
	hook func() error
	once bool
}

func (b *eofHookBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF && !b.once {
		b.once = true
		if hookErr := b.hook(); hookErr != nil {
			return n, hookErr
		}
	}
	return n, err
}
func (b *eofHookBody) Close() error { return nil }

func TestNativeExecutorAmbiguousFinishRetainsCleanupObligation(t *testing.T) {
	for _, mode := range []string{"committed", "rollback", "cleanup-retry"} {
		t.Run(mode, func(t *testing.T) {
			f := newNativeLeaseFixture(t, true, false, environment.ToolPolicyReview)
			ctx := context.Background()
			ids := &mcpFaultIDs{}
			transport := &renewalTransport{}
			offline, _ := offlinehold.New(offlinehold.DefaultConfig())
			if err := offline.Start(ctx, offlinehold.RuntimeBinding{InstanceID: "stream-finish-fault"}); err != nil {
				t.Fatal(err)
			}
			options := toolapproval.DefaultOptions(f.store.ToolApprovalRepository())
			options.NativeRepository = f.store.NativeToolRepository()
			authority, err := toolapproval.New(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			gate, _ := toolpolicy.New(authority)
			executor, err := nativemcp.New(nativemcp.Dependencies{OwnerContext: ctx, Transport: transport, Store: f.store.NativeToolRepository(), ToolDecisions: gate, Offline: offline, Clock: time.Now, IDs: ids}, nativemcp.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer executor.Close(ctx)
			var plan environment.ClientToolPlan
			capture := func(raw, session string) (nativemcp.CapturedRequest, nativetool.ID) {
				lease, err := f.assignments.BeginRequest(ctx, f.capture, f.connectionID, f.facts)
				if err != nil {
					t.Fatal(err)
				}
				plan, _ = lease.Plan().ClientToolPlan()
				plan = frozenExplicitNativePlan(t, plan)
				id, _ := ids.NewNativeOperationID(ctx)
				req := httptest.NewRequest("POST", "https://native.example/backend-api/ps/mcp", strings.NewReader(raw))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer synthetic-original")
				if session != "" {
					req.Header.Set("Mcp-Session-Id", session)
				}
				c, err := nativemcp.NewCapturedRequest(lease, f.admission, req, id)
				if err != nil {
					t.Fatal(err)
				}
				return c, id
			}
			transport.handler = func(_ *http.Request, body []byte) *http.Response {
				var m map[string]json.RawMessage
				_ = json.Unmarshal(body, &m)
				method := string(m["method"])
				if method == `"notifications/initialized"` {
					return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}
				}
				result := `{"tools":[{"name":"read_file","inputSchema":{"type":"object"}}]}`
				header := http.Header{"Content-Type": []string{"application/json"}}
				if method == `"initialize"` {
					result = `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"fixture","version":"1"}}`
					header.Set("Mcp-Session-Id", "session")
				}
				return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":` + string(m["id"]) + `,"result":` + result + `}`))}
			}
			for i, raw := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`} {
				sid := "session"
				if i == 0 {
					sid = ""
				}
				c, _ := capture(raw, sid)
				if got := executor.Serve(ctx, c, httptest.NewRecorder()); got.Outcome != nativemcp.OutcomeCompleted {
					t.Fatal(got)
				}
			}
			call, id := capture(`{"jsonrpc":"2.0","id":40,"method":"tools/call","params":{"name":"read_file","arguments":{}}}`, "session")
			fault := &streamFinishFault{operation: id, fault: commitThenError{}}
			if mode == "rollback" {
				fault.fault = rollbackThenError{}
			}
			if mode == "cleanup-retry" {
				fault.cleanupFailures.Store(3)
			}
			question := nativetool.ID("96969696969696969696969696969696")
			child := nativetool.ID("95959595959595959595959595959595")
			hookResult := make(chan error, 1)
			transport.handler = func(_ *http.Request, _ []byte) *http.Response {
				wire := "id: callback\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"server\",\"method\":\"ping\"}\n\n"
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &eofHookBody{Reader: strings.NewReader(wire), hook: func() (hookErr error) {
					defer func() { hookResult <- hookErr }()
					var sid string
					var rev, owner, assignment, generation uint64
					var catalog []byte
					if err := f.store.database.QueryRow(`SELECT session_id,session_revision,owner_epoch,assignment_revision,catalog_generation,catalog_digest FROM native_tool_bindings WHERE operation_id=?`, id).Scan(&sid, &rev, &owner, &assignment, &generation, &catalog); err != nil {
						return fmt.Errorf("callback binding scope: %w", err)
					}
					var d nativetool.Digest
					copy(d[:], catalog)
					server := nativetool.Digest(sha256.Sum256([]byte(`{"jsonrpc":"2.0","id":"server","method":"ping"}`)))
					b, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestServerReply, Plan: plan, Capture: f.capture, AssignmentRevision: assignment, OwnerEpoch: owner, SessionID: nativetool.ID(sid), SessionRevision: rev, CatalogGeneration: generation, CatalogDigest: &d, ParentOperationID: &id, ServerRequestDigest: &server, ExactRPC: []byte(`{"jsonrpc":"2.0","id":"server","result":{}}`)})
					if err != nil {
						return fmt.Errorf("callback binding: %w", err)
					}
					now := time.Now().UTC()
					rpc, _ := b.RPCIDDigest()
					requestDigest := b.RequestDigest()
					sessionID := b.SessionID()
					if result, err := f.store.NativeToolRepository().Receive(ctx, nativetool.ReceiveInput{ID: child, Capture: b.Capture(), EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "actual-native-fixture", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "POST", Kind: b.Kind(), SessionID: &sessionID, RPCIDDigest: &rpc, RequestDigest: &requestDigest, Recording: plan.ContentRecording(), ObservedAt: now}); err != nil || result.Duplicate {
						return fmt.Errorf("callback Receive duplicate=%v: %v", result.Duplicate, err)
					}
					// The real authenticated parser has already issued this exact
					// callback. Consume its registration, never reconstruct one from SQL.
					f.store.nativeTools.issuerMu.Lock()
					var callback nativetool.CallbackAuthority
					for a, c := range f.store.nativeTools.callbacks {
						if stream := f.store.nativeTools.streams[c.stream]; stream != nil && stream.receipt.OperationID() == id {
							callback = a
						}
					}
					f.store.nativeTools.issuerMu.Unlock()
					args, _ := protocolcore.NewJSONObject([]byte(`{}`), 1024)
					key, _ := protocolcore.NewCallKey("native", "40")
					catalogObserver, err := nativetool.NewCatalogObserver(nativetool.CatalogScope{Capture: f.capture, Plan: plan, AssignmentRevision: assignment, OwnerEpoch: owner, SessionID: nativetool.ID(sid), SessionRevision: rev, CatalogGeneration: generation})
					if err != nil {
						return fmt.Errorf("catalog observer: %w", err)
					}
					catalogEvidence, err := catalogObserver.Observe([]nativetool.CatalogPageObservation{{ExactRequest: []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`), ExactResponse: []byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"read_file","inputSchema":{"type":"object"}}]}}`)}})
					if err != nil {
						return fmt.Errorf("catalog observation: %w", err)
					}
					parent, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestToolsCall, Plan: plan, Capture: f.capture, AssignmentRevision: assignment, OwnerEpoch: owner, SessionID: nativetool.ID(sid), SessionRevision: rev, CatalogGeneration: generation, CatalogDigest: &d, CatalogEvidence: &catalogEvidence, ExactRPC: []byte(`{"jsonrpc":"2.0","id":40,"method":"tools/call","params":{"name":"read_file","arguments":{}}}`), Intents: []protocolcore.ToolIntent{{ResponseID: "rpc-40", Call: protocolcore.ToolCall{Key: key, Name: "read_file", Arguments: args}}}})
					if err != nil {
						return fmt.Errorf("parent binding: %w", err)
					}
					observer, err := nativetool.NewParentObserver(id, parent)
					if err != nil {
						return fmt.Errorf("parent observer: %w", err)
					}
					witness, err := observer.ObserveReply(child, []byte(`{"jsonrpc":"2.0","id":"server","method":"ping"}`), []byte(`{"jsonrpc":"2.0","id":"server","result":{}}`))
					if err != nil {
						return fmt.Errorf("reply witness: %w", err)
					}
					if err := f.store.NativeToolRepository().BindObservedReply(ctx, child, b, callback, witness, now); err != nil {
						return fmt.Errorf("observed reply: %w", err)
					}
					if _, err := f.store.NativeToolRepository().RequestApproval(ctx, child, question, now, now.Add(time.Minute)); err != nil {
						return fmt.Errorf("callback approval: %w", err)
					}
					return nil
				}}}
			}
			f.store.nativeTools.committer = fault
			done := make(chan nativemcp.Terminal, 1)
			go func() { done <- executor.Serve(ctx, call, httptest.NewRecorder()) }()
			deadline := time.Now().Add(2 * time.Second)
			for {
				op, err := f.store.NativeToolRepository().Get(ctx, id)
				if err == nil && op.State == "awaiting_decision" {
					nativeAllow(t, f.store.NativeToolRepository(), id, time.Now().UTC())
					break
				}
				select {
				case result := <-done:
					t.Fatalf("call exited before decision: %+v", result)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("approval absent")
				}
				time.Sleep(time.Millisecond)
			}
			var got nativemcp.Terminal
			select {
			case got = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("actual ambiguous-finish fixture did not return after real approval")
			}
			select {
			case err := <-hookResult:
				if err != nil {
					t.Fatalf("actual EOF fixture: %v", err)
				}
			default:
				t.Fatal("actual EOF fixture did not run")
			}
			if !fault.used.Load() || got.Outcome != nativemcp.OutcomeUncertain {
				t.Fatalf("fault not exercised: %+v used=%v", got, fault.used.Load())
			}
			if mode == "cleanup-retry" {
				before := transport.calls.Load()
				probe, _ := capture(`{"jsonrpc":"2.0","id":50,"method":"ping"}`, "session")
				blocked := executor.Serve(ctx, probe, httptest.NewRecorder())
				if blocked.Outcome != nativemcp.OutcomeRejected || transport.calls.Load() != before {
					t.Fatal("failed cleanup left owner sendable")
				}
				deadline := time.Now().Add(time.Second)
				for fault.cleanupFailures.Load() > 1 {
					if time.Now().After(deadline) {
						t.Fatal("cleanup retry never ran")
					}
					time.Sleep(10 * time.Millisecond)
				}
				f.store.nativeTools.issuerMu.Lock()
				var pending nativetool.StreamAuthority
				for a, s := range f.store.nativeTools.streams {
					if s.receipt.OperationID() == id {
						pending = a
						if !s.cleanupOnly {
							t.Error("pending cleanup retained authority")
						}
					}
				}
				f.store.nativeTools.issuerMu.Unlock()
				if f.store.NativeToolRepository().CompleteStreamRPC(ctx, pending, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: time.Now().UTC()}) == nil {
					t.Fatal("cleanup-only handle completed RPC")
				}
				fault.cleanupFailures.Store(0)
			}
			deadline = time.Now().Add(8 * time.Second)
			for {
				op, err := f.store.NativeToolRepository().Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if op.State == "uncertain" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("cleanup stranded original: %s", op.State)
				}
				time.Sleep(10 * time.Millisecond)
			}
			f.store.nativeTools.issuerMu.Lock()
			f.store.nativeTools.committer = sqlTransactionCommitter{}
			f.store.nativeTools.issuerMu.Unlock()
			scope, err := f.store.NativeToolRepository().GetScope(ctx, f.capture)
			if err != nil || !scope.ReplayBarrier {
				t.Fatalf("missing barrier %+v %v", scope, err)
			}
			var state string
			if err := f.store.database.QueryRow(`SELECT state FROM native_tool_approvals WHERE approval_id=?`, question).Scan(&state); err != nil || state != "canceled" {
				t.Fatalf("question %s %v", state, err)
			}
			after, err := scanEgressAttempt(f.store.database.QueryRow(nativeDeliverySelect+` WHERE operation_id=?`, id))
			if err != nil {
				t.Fatal(err)
			}
			if mode != "rollback" && after.Attempt != fault.physical {
				t.Fatal("committed physical evidence rewritten")
			}
			if after.Attempt.ByteCountsKnown() {
				t.Fatal("invented physical counts")
			}
			if _, err := f.store.NativeToolRepository().Start(ctx, id, "forbidden-reissue", scope.Revision, time.Now().UTC()); err == nil {
				t.Fatal("cleanup reissued send")
			}
		})
	}
}
