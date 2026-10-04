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
	"sync"
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
	child            nativetool.ID
	committed        bool
	used             atomic.Bool
	armed            atomic.Bool
	retries          atomic.Int32
	physical         egressaudit.Attempt
	existingTerminal egressaudit.Attempt
}

// Holds entry into the real Store FinishAttempt while a different real
// lifecycle path retires the parent. It never fakes a transaction result.
type heldRecoveryFinish struct {
	nativetool.Repository
	child            nativetool.ID
	entered, release chan struct{}
	once             sync.Once
	after            func(error)
}

func (h *heldRecoveryFinish) FinishAttempt(ctx context.Context, receipt nativetool.StartReceipt, in nativetool.PhysicalFinishInput) error {
	if receipt.OperationID() == h.child {
		h.once.Do(func() { close(h.entered) })
		<-h.release
	}
	err := h.Repository.FinishAttempt(ctx, receipt, in)
	if receipt.OperationID() == h.child && h.after != nil {
		h.after(err)
	}
	return err
}

func (f *recoveryFinishFault) Commit(tx *sql.Tx) error {
	if !f.armed.Load() {
		return tx.Commit()
	}
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
	// Read-only transactions after retirement must not masquerade as a late
	// physical write. An already proven identical terminal is immutable.
	if f.existingTerminal.Terminal() && audit.Attempt == f.existingTerminal {
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
	for _, ordering := range []string{"fault-first", "cancel-first", "timer-first"} {
		for _, edge := range []string{"before-headers", "attached", "known-terminal"} {
			for _, mode := range []string{"rollback", "committed", "cleanup-retry"} {
				if ordering == "cancel-first" && edge == "known-terminal" {
					continue
				}
				t.Run(ordering+"/"+edge+"/"+mode, func(t *testing.T) {
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
					held := &heldRecoveryFinish{Repository: f.store.NativeToolRepository()}
					executor, err := nativemcp.New(nativemcp.Dependencies{OwnerContext: ctx, Transport: transport, Store: held, ToolDecisions: gate, Offline: offline, Clock: time.Now, IDs: ids}, nativemcp.DefaultLimits())
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
					transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
						if strings.Contains(string(body), `"notifications/cancelled"`) {
							return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
						}
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
					var got nativemcp.Terminal
					var lateFinishErr error
					f.store.nativeTools.committer = fault
					if ordering == "fault-first" {
						fault.armed.Store(true)
						got = executor.Serve(ctx, get, httptest.NewRecorder())
					} else {
						held.child = child
						held.entered = make(chan struct{})
						held.release = make(chan struct{})
						var releaseOnce sync.Once
						release := func() { releaseOnce.Do(func() { close(held.release) }) }
						defer release()
						done := make(chan nativemcp.Terminal, 1)
						go func() { done <- executor.Serve(ctx, get, httptest.NewRecorder()) }()
						select {
						case <-held.entered:
						case <-time.After(2 * time.Second):
							t.Fatal("real GET FinishAttempt not reached")
						}
						if ordering == "cancel-first" {
							cancelRequest, _ := capture("POST", `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":40}}`, "session", "")
							if result := executor.Serve(ctx, cancelRequest, httptest.NewRecorder()); result.Outcome != nativemcp.OutcomeCompleted {
								t.Fatalf("MCP cancellation %+v", result)
							}
						} else {
							f.store.nativeTools.issuerMu.Lock()
							for _, s := range f.store.nativeTools.streams {
								if s.receipt.OperationID() == origin {
									s.timer.Reset(0)
								}
							}
							f.store.nativeTools.issuerMu.Unlock()
						}
						deadline := time.Now().Add(2 * time.Second)
						for {
							f.store.nativeTools.issuerMu.Lock()
							retired := true
							for _, s := range f.store.nativeTools.streams {
								if s.receipt.OperationID() == origin && !s.cleanupOnly {
									retired = false
								}
							}
							f.store.nativeTools.issuerMu.Unlock()
							if retired {
								break
							}
							if time.Now().After(deadline) {
								t.Fatal("parent retirement did not precede GET finish")
							}
							time.Sleep(time.Millisecond)
						}
						f.store.nativeTools.issuerMu.Lock()
						existing, err := scanEgressAttempt(f.store.database.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, child))
						if err != nil {
							f.store.nativeTools.issuerMu.Unlock()
							t.Fatal(err)
						}
						if existing.Attempt.Terminal() {
							fault.existingTerminal = existing.Attempt
						}
						fault.armed.Store(true)
						f.store.nativeTools.issuerMu.Unlock()
						held.after = func(err error) { lateFinishErr = err }
						release()
						got = <-done
					}
					if ordering == "fault-first" && (!fault.used.Load() || got.Outcome != nativemcp.OutcomeUncertain) {
						t.Fatalf("physical fault absent: %+v used=%v", got, fault.used.Load())
					}
					if ordering == "fault-first" && mode == "cleanup-retry" {
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
					pending := 0
					for _, s := range f.store.nativeTools.streams {
						if s.receipt.OperationID() == origin {
							pending++
						}
					}
					_, orphan := f.store.nativeTools.recovery[child]
					f.store.nativeTools.issuerMu.Unlock()
					if pending != 0 || orphan {
						t.Fatal("completed obligations retained timer")
					}
					if ordering != "fault-first" && (fault.used.Load() || !errors.Is(lateFinishErr, nativetool.ErrConflict)) {
						t.Fatalf("late finish attempted to rewrite retired GET: %v fault=%v", lateFinishErr, fault.used.Load())
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
					if ordering != "fault-first" && physical.Attempt != fault.existingTerminal {
						t.Fatal("late Finish changed retired child evidence")
					}
					var once int
					if err := f.store.database.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, child).Scan(&once); err != nil || once != 1 {
						t.Fatalf("child once=%d err=%v", once, err)
					}
					if ordering == "fault-first" && mode == "committed" {
						if physical.Attempt != fault.physical {
							t.Fatal("committed GET audit rewritten")
						}
					} else if physical.Attempt.ByteCountsKnown() || physical.Attempt.Outcome() != "failed" {
						t.Fatal("rollback GET counts/outcome invented")
					}
					if receipt, err := f.store.NativeToolRepository().Start(ctx, child, "delayed-recovery", scope.Revision, time.Now().UTC()); err == nil || receipt.OperationID() != "" {
						t.Fatal("child cleanup granted delayed send")
					}
					wantCalls := int64(4)
					if ordering == "cancel-first" {
						wantCalls++
					}
					if transport.calls.Load() != wantCalls {
						t.Fatalf("unexpected outbound count %d", transport.calls.Load())
					}
				})
			}
		}
	}
}
