package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
)

type recoveryFinishTransport struct {
	calls   atomic.Int64
	handler func(*http.Request, []byte) (*http.Response, error)
}

func (t *recoveryFinishTransport) RoundTrip(r *http.Request, _ egressnetwork.Policy) (*http.Response, error) {
	t.calls.Add(1)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	return t.handler(r, body)
}

type recoveryFinishFault struct {
	child     nativetool.ID
	committed bool
	used      atomic.Bool
	retries   atomic.Int32
	physical  egressaudit.Attempt
}

func (f *recoveryFinishFault) Commit(tx *sql.Tx) error {
	audit, err := scanEgressAttempt(tx.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, f.child))
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if !audit.Attempt.Terminal() {
		return tx.Commit()
	}
	if f.used.CompareAndSwap(false, true) {
		f.physical = audit.Attempt
		if f.committed {
			return (commitThenError{}).Commit(tx)
		}
		return (rollbackThenError{}).Commit(tx)
	}
	if f.retries.Add(-1) >= 0 {
		return (rollbackThenError{}).Commit(tx)
	}
	return tx.Commit()
}

func TestNativeRecoveryGETAmbiguousFinishCleansBothAttempts(t *testing.T) {
	for _, edge := range []string{"before-headers", "attached", "known-terminal"} {
		for _, mode := range []string{"rollback", "committed", "cleanup-retry"} {
			t.Run(edge+"/"+mode, func(t *testing.T) {
				f := newNativeLeaseFixture(t, true, false)
				ctx := context.Background()
				ids := &mcpFaultIDs{}
				transport := &recoveryFinishTransport{}
				offline, _ := offlinehold.New(offlinehold.DefaultConfig())
				if err := offline.Start(ctx, offlinehold.RuntimeBinding{InstanceID: "recovery-fault"}); err != nil {
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
				capture := func(method, raw, session, cursor string) (nativemcp.CapturedRequest, nativetool.ID) {
					facts := f.facts
					facts.Target.Method = method
					lease, err := f.assignments.BeginRequest(ctx, f.capture, f.connectionID, facts)
					if err != nil {
						t.Fatal(err)
					}
					id, _ := ids.NewNativeOperationID(ctx)
					req := httptest.NewRequest(method, "https://native.example/backend-api/ps/mcp", strings.NewReader(raw))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Authorization", "Bearer synthetic-original")
					if session != "" {
						req.Header.Set("Mcp-Session-Id", session)
					}
					if cursor != "" {
						req.Header.Set("Last-Event-Id", cursor)
					}
					c, err := nativemcp.NewCapturedRequest(lease, f.admission, req, id)
					if err != nil {
						t.Fatal(err)
					}
					return c, id
				}
				transport.handler = func(_ *http.Request, body []byte) (*http.Response, error) {
					var m map[string]json.RawMessage
					_ = json.Unmarshal(body, &m)
					if string(m["method"]) == `"notifications/initialized"` {
						return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}, "Mcp-Session-Id": []string{"session"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"fixture","version":"1"}}}`))}, nil
				}
				for i, raw := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`} {
					sid := "session"
					if i == 0 {
						sid = ""
					}
					c, _ := capture("POST", raw, sid, "")
					if got := executor.Serve(ctx, c, httptest.NewRecorder()); got.Outcome != nativemcp.OutcomeCompleted {
						t.Fatal(got)
					}
				}
				progress := func(id string) string {
					return "id: " + id + "\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":1}}\n\n"
				}
				transport.handler = func(*http.Request, []byte) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(progress("e1")))}, nil
				}
				call, origin := capture("POST", `{"jsonrpc":"2.0","id":40,"method":"ping","params":{"_meta":{"progressToken":9}}}`, "session", "")
				executor.Serve(ctx, call, httptest.NewRecorder())
				original, err := scanEgressAttempt(f.store.database.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, origin))
				if err != nil {
					t.Fatal(err)
				}
				get, child := capture("GET", "", "session", "e1")
				fault := &recoveryFinishFault{child: child, committed: mode == "committed"}
				if mode == "cleanup-retry" {
					fault.retries.Store(4)
				}
				transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
					if r.Method != "GET" || r.Header.Get("Last-Event-Id") != "e1" {
						t.Error("not exact recovery GET")
					}
					if edge == "before-headers" {
						return nil, io.ErrUnexpectedEOF
					}
					wire := progress("e2")
					if edge == "known-terminal" {
						wire = "id: e2\ndata: {\"jsonrpc\":\"2.0\",\"id\":40,\"result\":{}}\n\n"
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}, nil
				}
				f.store.nativeTools.committer = fault
				got := executor.Serve(ctx, get, httptest.NewRecorder())
				if !fault.used.Load() || got.Outcome != nativemcp.OutcomeUncertain {
					t.Fatalf("physical fault absent: %+v used=%v", got, fault.used.Load())
				}
				if mode == "cleanup-retry" {
					before := transport.calls.Load()
					probe, _ := capture("POST", `{"jsonrpc":"2.0","id":50,"method":"ping"}`, "session", "")
					if result := executor.Serve(ctx, probe, httptest.NewRecorder()); result.Outcome != nativemcp.OutcomeRejected || transport.calls.Load() != before {
						t.Fatal("cleanup failure owner remained sendable")
					}
					deadline := time.Now().Add(2 * time.Second)
					for fault.retries.Load() > 2 {
						if time.Now().After(deadline) {
							t.Fatal("child cleanup retry missing")
						}
						time.Sleep(10 * time.Millisecond)
					}
					fault.retries.Store(0)
				}
				deadline := time.Now().Add(3 * time.Second)
				for {
					op, err := f.store.NativeToolRepository().Get(ctx, child)
					if err != nil {
						t.Fatal(err)
					}
					if op.CompletedAt != nil {
						break
					}
					if time.Now().After(deadline) {
						parent, _ := f.store.NativeToolRepository().Get(ctx, origin)
						t.Fatalf("recovery GET cleanup stranded B=%s while A=%s", op.State, parent.State)
					}
					time.Sleep(10 * time.Millisecond)
				}
				f.store.nativeTools.issuerMu.Lock()
				f.store.nativeTools.committer = sqlTransactionCommitter{}
				pending := 0
				for _, s := range f.store.nativeTools.streams {
					if s.receipt.OperationID() == origin {
						pending++
					}
				}
				f.store.nativeTools.issuerMu.Unlock()
				if pending != 0 {
					t.Fatal("completed obligations retained timer")
				}
				parent, err := f.store.NativeToolRepository().Get(ctx, origin)
				if err != nil {
					t.Fatal(err)
				}
				want := "uncertain"
				if edge == "known-terminal" {
					want = "completed"
				}
				if parent.State != want {
					t.Fatalf("original logical result %s want %s", parent.State, want)
				}
				scope, err := f.store.NativeToolRepository().GetScope(ctx, f.capture)
				if err != nil || scope.ReplayBarrier != (edge != "known-terminal") {
					t.Fatalf("barrier %+v %v", scope, err)
				}
				after, err := scanEgressAttempt(f.store.database.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, origin))
				if err != nil || after.Attempt != original.Attempt {
					t.Fatal("original physical audit changed")
				}
				physical, err := scanEgressAttempt(f.store.database.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, child))
				if err != nil || !physical.Attempt.Terminal() {
					t.Fatal("GET audit not closed")
				}
				if mode == "committed" {
					if physical.Attempt != fault.physical {
						t.Fatal("committed GET audit rewritten")
					}
				} else if physical.Attempt.ByteCountsKnown() || physical.Attempt.Outcome() != "failed" {
					t.Fatal("rollback GET counts/outcome invented")
				}
				if receipt, err := f.store.NativeToolRepository().Start(ctx, child, "delayed-recovery", scope.Revision, time.Now().UTC()); err == nil || receipt.OperationID() != "" {
					t.Fatal("child cleanup granted delayed send")
				}
				if transport.calls.Load() != 4 {
					t.Fatalf("unexpected outbound count %d", transport.calls.Load())
				}
			})
		}
	}
}
