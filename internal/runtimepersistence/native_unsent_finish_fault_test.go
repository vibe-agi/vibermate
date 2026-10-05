package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
)

type unsentCommitFault struct {
	target      nativetool.ID
	mode        string
	armed, used atomic.Bool
	physical    egressaudit.Attempt
}

func (f *unsentCommitFault) Commit(tx *sql.Tx) error {
	if !f.armed.Load() {
		return tx.Commit()
	}
	var state, scope string
	err := tx.QueryRow(`SELECT o.state,s.state FROM native_tool_operations o JOIN native_tool_scopes s USING(capture_kind,capture_id) WHERE o.operation_id=?`, f.target).Scan(&state, &scope)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if f.mode == "start_committed" {
		if state == "started" && f.used.CompareAndSwap(false, true) {
			return (commitThenError{}).Commit(tx)
		}
		return tx.Commit()
	}
	audit, err := scanEgressAttempt(tx.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, f.target))
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if state == "failed" && audit.Attempt.Terminal() && f.used.CompareAndSwap(false, true) {
		f.physical = audit.Attempt
		if f.mode == "committed" {
			return (commitThenError{}).Commit(tx)
		}
		return (rollbackThenError{}).Commit(tx)
	}
	if f.used.Load() && strings.HasPrefix(f.mode, "persistent_") && (scope == "revoked" || state == "uncertain") {
		return (rollbackThenError{}).Commit(tx)
	}
	return tx.Commit()
}

type unsentStartObservation struct {
	receipt nativetool.StartReceipt
	err     error
}
type heldUnsentAttempt struct {
	nativetool.Repository
	target        nativetool.ID
	started       chan unsentStartObservation
	release       chan struct{}
	timeoutFinish bool
	verified      atomic.Bool
	afterFinish   func(error)
	beforeVerify  func()
}

func (h *heldUnsentAttempt) Start(ctx context.Context, id nativetool.ID, attempt string, revision uint64, now time.Time) (nativetool.StartReceipt, error) {
	receipt, err := h.Repository.Start(ctx, id, attempt, revision, now)
	if id == h.target {
		h.started <- unsentStartObservation{receipt, err}
		if err == nil {
			select {
			case <-h.release:
			case <-ctx.Done():
				return receipt, ctx.Err()
			}
		}
	}
	return receipt, err
}
func (h *heldUnsentAttempt) VerifyStartReceipt(ctx context.Context, r nativetool.StartReceipt, now time.Time) error {
	if r.OperationID() == h.target && h.beforeVerify != nil {
		h.beforeVerify()
	}
	err := h.Repository.VerifyStartReceipt(ctx, r, now)
	if r.OperationID() == h.target {
		h.verified.Store(true)
		if !errors.Is(err, nativetool.ErrConflict) {
			return err
		}
	}
	return err
}
func (h *heldUnsentAttempt) Finish(ctx context.Context, id nativetool.ID, in nativetool.TerminalInput) error {
	if h.timeoutFinish && id == h.target && in.Outcome == "failed" {
		<-ctx.Done()
	}
	err := h.Repository.Finish(ctx, id, in)
	if id == h.target && h.afterFinish != nil {
		h.afterFinish(err)
	}
	return err
}

func TestNativeUnsentFinishFaultsFenceAndReconcile(t *testing.T) {
	for _, kind := range []string{"get", "tools_call"} {
		for _, mode := range []string{"rollback", "committed", "persistent_close", "persistent_recover", "finish_timeout", "start_committed"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newNativeLeaseFixture(t, true, false, environment.ToolPolicyReview)
				ctx := context.Background()
				ids := &mcpFaultIDs{}
				transport := &recoveryFinishTransport{}
				offline, _ := offlinehold.New(offlinehold.DefaultConfig())
				if err := offline.Start(ctx, offlinehold.RuntimeBinding{InstanceID: "unsent-finish-fault"}); err != nil {
					t.Fatal(err)
				}
				defer offline.BeginShutdown()
				options := toolapproval.DefaultOptions(f.store.ToolApprovalRepository())
				options.NativeRepository = f.store.NativeToolRepository()
				authority, err := toolapproval.New(ctx, options)
				if err != nil {
					t.Fatal(err)
				}
				defer authority.Shutdown(ctx)
				gate, _ := toolpolicy.New(authority)
				held := &heldUnsentAttempt{Repository: f.store.NativeToolRepository(), started: make(chan unsentStartObservation, 1), release: make(chan struct{})}
				module, err := nativemcp.New(nativemcp.Dependencies{OwnerContext: ctx, Transport: transport, Store: held, ToolDecisions: gate, Offline: offline, Clock: time.Now, IDs: ids}, nativemcp.DefaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				fault := &unsentCommitFault{mode: mode}
				originalCommitter := f.store.nativeTools.committer
				f.store.nativeTools.committer = fault
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(held.release) }) }
				defer func() {
					fault.armed.Store(false)
					release()
					if err := module.Close(ctx); err != nil {
						t.Error(err)
					}
					f.store.nativeTools.committer = originalCommitter
				}()
				capture := func(method, body, sid string) (nativemcp.CapturedRequest, nativetool.ID) {
					t.Helper()
					facts := f.facts
					facts.Target.Method = method
					lease, err := f.assignments.BeginRequest(ctx, f.capture, f.connectionID, facts)
					if err != nil {
						t.Fatal(err)
					}
					id, _ := ids.NewNativeOperationID(ctx)
					req := httptest.NewRequest(method, "https://native.example/backend-api/ps/mcp", strings.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Accept", "application/json, text/event-stream")
					req.Header.Set("Authorization", "Bearer synthetic-original")
					if sid != "" {
						req.Header.Set("Mcp-Session-Id", sid)
						req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
					}
					request, err := nativemcp.NewCapturedRequest(lease, f.admission, req, id)
					if err != nil {
						lease.Release()
						t.Fatal(err)
					}
					return request, id
				}
				transport.handler = func(_ *http.Request, body []byte) (*http.Response, error) {
					var request struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if json.Unmarshal(body, &request) != nil {
						return nil, errors.New("unexpected empty native send")
					}
					header := http.Header{"Content-Type": []string{"application/json"}}
					result := `{}`
					switch request.Method {
					case "initialize":
						header.Set("Mcp-Session-Id", "session")
						result = `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"fixture","version":"1"}}`
					case "notifications/initialized":
						return &http.Response{StatusCode: 202, Header: header, Body: http.NoBody}, nil
					case "tools/list":
						result = `{"tools":[{"name":"read_file","inputSchema":{"type":"object"}}]}`
					}
					return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":` + result + `}`))}, nil
				}
				serve := func(body, sid string) {
					t.Helper()
					request, _ := capture("POST", body, sid)
					if result := module.Serve(ctx, request, httptest.NewRecorder()); result.Outcome != nativemcp.OutcomeCompleted {
						t.Fatal(result)
					}
				}
				serve(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, "")
				serve(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, "session")
				method, body := "GET", ""
				if kind == "tools_call" {
					serve(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "session")
					method = "POST"
					body = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{}}}`
				}
				request, id := capture(method, body, "session")
				held.target = id
				fault.target = id
				held.timeoutFinish = mode == "finish_timeout"
				if mode == "start_committed" {
					fault.armed.Store(true)
				}
				done := make(chan struct{})
				var terminal nativemcp.Terminal
				callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
				defer cancel()
				go func() { terminal = module.Serve(callCtx, request, httptest.NewRecorder()); close(done) }()
				defer func() {
					release()
					cancel()
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("unsent fault call did not drain")
					}
				}()
				if kind == "tools_call" {
					deadline := time.After(2 * time.Second)
					for {
						op, err := f.store.NativeToolRepository().Get(ctx, id)
						if err == nil && op.ApprovalID != nil {
							nativeAllow(t, f.store.NativeToolRepository(), id, time.Now().UTC())
							break
						}
						select {
						case <-deadline:
							t.Fatal("real approval missing")
						case <-time.After(time.Millisecond):
						}
					}
				}
				var observed unsentStartObservation
				select {
				case observed = <-held.started:
				case <-callCtx.Done():
					t.Fatal("real Start observation missing")
				}
				var approval nativetool.Approval
				if kind == "tools_call" {
					op, err := f.store.NativeToolRepository().Get(ctx, id)
					if err != nil || op.ApprovalID == nil {
						t.Fatal("real consumed decision missing")
					}
					approval, err = f.store.NativeToolRepository().GetApproval(ctx, *op.ApprovalID)
					if err != nil || approval.ConsumedAt == nil {
						t.Fatal("Start did not consume Allow")
					}
				}
				if mode == "start_committed" {
					if observed.err == nil || observed.receipt.OperationID() != "" {
						t.Fatal("ambiguous Start returned a receipt")
					}
				} else {
					if observed.err != nil || observed.receipt.OperationID() != id {
						t.Fatal("real successful Start missing")
					}
					serve(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`, "session")
					if mode != "finish_timeout" {
						fault.armed.Store(true)
					}
					release()
				}
				select {
				case <-done:
				case <-callCtx.Done():
					t.Fatal("real failure closure did not finish")
				}
				if mode != "start_committed" && (terminal.Outcome != nativemcp.OutcomeUncertain || terminal.Reason != nativemcp.ReasonStorageUnavailable || !held.verified.Load()) {
					t.Fatalf("cleanup error claimed known closure: %+v", terminal)
				}
				if mode == "start_committed" && held.verified.Load() {
					t.Fatal("ambiguous Start reached send gate")
				}
				if mode != "finish_timeout" && !fault.used.Load() {
					t.Fatal("real transaction fault was not exercised")
				}
				// All Serve consumers have returned. Observe the private registry
				// read-only from this Store fault package, without a production
				// inspection hook or changing the real committer/receipt path.
				claims := reflect.ValueOf(module).Elem().FieldByName("claimed")
				token := claims.MapIndex(reflect.ValueOf(id))
				if mode == "start_committed" {
					if token.IsValid() || claims.Len() != 0 {
						t.Fatal("ambiguous Start manufactured a local claim")
					}
				} else if !token.IsValid() || claims.Len() != 1 || token.Elem().FieldByName("physicalTerminal").Bool() || !token.Elem().FieldByName("consumerDone").Bool() {
					t.Fatal("Finish error/terminal history manufactured physical claim proof")
				}
				beforeSends := transport.calls.Load()
				wantSends := int64(3)
				if kind == "tools_call" {
					wantSends = 4
				}
				if mode == "start_committed" {
					wantSends--
				}
				if beforeSends != wantSends {
					t.Fatalf("target entered transport: sends=%d want%d", beforeSends, wantSends)
				}
				op, err := f.store.NativeToolRepository().Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				audit, err := scanEgressAttempt(f.store.database.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, id))
				if err != nil {
					t.Fatal(err)
				}
				scope, err := f.store.NativeToolRepository().GetScope(ctx, f.capture)
				if err != nil {
					t.Fatal(err)
				}
				persistent := strings.HasPrefix(mode, "persistent_")
				if persistent {
					if op.State != "started" || audit.Attempt.Terminal() || scope.State != "active" {
						t.Fatal("rollback/open obligation was lost")
					}
				} else if mode == "committed" {
					if op.State != "failed" || op.BytesIn == nil || op.BytesOut == nil || *op.BytesIn != 0 || *op.BytesOut != 0 || audit.Attempt != fault.physical || !audit.Attempt.ByteCountsKnown() {
						t.Fatal("committed known-zero terminal was overwritten")
					}
				} else if op.State != "uncertain" || op.BytesIn != nil || op.BytesOut != nil || audit.Attempt.ByteCountsKnown() {
					t.Fatal("rollback/no-receipt cleanup manufactured known counts")
				}
				if !persistent && (scope.State != "revoked" || !scope.ReplayBarrier) {
					t.Fatal("durable cleanup did not fence scope")
				}
				probe, _ := capture("POST", `{"jsonrpc":"2.0","id":88,"method":"ping"}`, "session")
				if result := module.Serve(ctx, probe, httptest.NewRecorder()); result.Outcome == nativemcp.OutcomeCompleted || transport.calls.Load() != beforeSends {
					t.Fatal("fenced in-memory owner admitted new send")
				}
				if persistent {
					initialize, _ := capture("POST", `{"jsonrpc":"2.0","id":90,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, "")
					if result := module.Serve(ctx, initialize, httptest.NewRecorder()); result.Outcome == nativemcp.OutcomeCompleted || transport.calls.Load() != beforeSends {
						t.Fatal("unsettled cleanup reactivated owner")
					}
					after, err := f.store.NativeToolRepository().GetScope(ctx, f.capture)
					if err != nil || after.OwnerEpoch != scope.OwnerEpoch || after.Revision != scope.Revision {
						t.Fatal("failed new initialization replaced fenced owner")
					}
				}
				if kind == "tools_call" {
					after, err := f.store.NativeToolRepository().GetApproval(ctx, approval.ID)
					if err != nil || !reflect.DeepEqual(approval, after) {
						t.Fatal("cleanup changed consumed approval history")
					}
				}
				var once, audits int
				if err = f.store.database.QueryRow(`SELECT (SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?),(SELECT COUNT(*) FROM runtime_egress_attempts WHERE parent_id=?)`, id, id).Scan(&once, &audits); err != nil || once != 1 || audits != 1 {
					t.Fatal("cleanup changed one-attempt provenance")
				}
				if mode == "persistent_recover" {
					if err := module.Close(ctx); err == nil {
						t.Fatal("Close falsely succeeded while cleanup remained unavailable")
					}
					// A fresh Store/issuer opens the durable file; the old Store remains
					// solely for fixture-manager teardown, with its executor closed.
					reopened := openTestStore(t, f.store.databasePath)
					defer shutdownTestStore(t, reopened)
					if err := reopened.NativeToolRepository().Recover(ctx, time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
					recovered, err := reopened.NativeToolRepository().Get(ctx, id)
					if err != nil || recovered.State != "uncertain" {
						t.Fatal("fresh Store Recover lost open work")
					}
					if reopened.NativeToolRepository().VerifyStartReceipt(ctx, observed.receipt, time.Now().UTC()) == nil {
						t.Fatal("restart restored receipt")
					}
				}
				fault.armed.Store(false)
				if err := module.Close(ctx); err != nil {
					t.Fatal(err)
				}
				final, err := f.store.NativeToolRepository().Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				finalAudit, err := scanEgressAttempt(f.store.database.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, id))
				if err != nil {
					t.Fatal(err)
				}
				if mode == "committed" {
					if finalAudit.Attempt != audit.Attempt {
						t.Fatal("Close rewrote known terminal")
					}
				} else if final.State != "uncertain" || !finalAudit.Attempt.Terminal() || finalAudit.Attempt.ByteCountsKnown() {
					t.Fatal("Close/Recover failed to settle unknown work")
				}
				if observed.receipt.OperationID() != "" && f.store.NativeToolRepository().VerifyStartReceipt(ctx, observed.receipt, time.Now().UTC()) == nil {
					t.Fatal("cleanup restored old receipt")
				}
				if transport.calls.Load() != beforeSends {
					t.Fatal("cleanup resent a request")
				}
			})
		}
	}
}
