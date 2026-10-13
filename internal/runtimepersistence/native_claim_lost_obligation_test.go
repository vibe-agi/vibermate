package runtimepersistence

import (
	"context"
	"database/sql"
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
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

type heldClaimTerminalWriter struct {
	*httptest.ResponseRecorder
	writes           int
	entered, release chan struct{}
}

// A timer retirement writes unknown failed counts, never this completed known
// response. Block its actual transactions until the intended Serve Finish has
// faulted and drained; no test assumes a 100ms scheduling window.
type claimPhysicalFinishFault struct {
	operation              nativetool.ID
	mode                   string
	boundary, boundaryMode string
	boundaryUsed           atomic.Bool
	used, allowCleanup     atomic.Bool
	blocked                atomic.Int32
	physical               egressaudit.Attempt
}

func (f *claimPhysicalFinishFault) Commit(tx *sql.Tx) error {
	record, err := scanEgressAttempt(tx.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, f.operation))
	if err != nil {
		return err
	}
	audit := record.Attempt
	if !audit.Terminal() {
		if f.boundaryMode != "none" && !f.boundaryUsed.Load() {
			var session, scope string
			if err := tx.QueryRow(`SELECT s.state,c.state FROM native_tool_bindings b JOIN native_tool_sessions s USING(session_id) JOIN native_tool_scopes c ON c.capture_kind=s.capture_kind AND c.capture_id=s.capture_id WHERE b.operation_id=?`, f.operation).Scan(&session, &scope); err != nil {
				return err
			}
			changed := f.boundary == "revoke" && scope == "revoked" || f.boundary == "session" && session == "revoked" && scope == "active"
			if changed && f.boundaryUsed.CompareAndSwap(false, true) {
				if f.boundaryMode == "committed" {
					return (commitThenError{}).Commit(tx)
				}
				return (rollbackThenError{}).Commit(tx)
			}
		}
		return tx.Commit()
	}
	if audit.Outcome() == egressaudit.OutcomeCompleted && audit.ByteCountsKnown() && audit.BytesIn() > 0 && audit.BytesOut() > 0 && f.used.CompareAndSwap(false, true) {
		f.physical = audit
		if f.mode == "committed" {
			return (commitThenError{}).Commit(tx)
		}
		return (rollbackThenError{}).Commit(tx)
	}
	if !f.allowCleanup.Load() {
		f.blocked.Add(1)
		return (rollbackThenError{}).Commit(tx)
	}
	return tx.Commit()
}

func (w *heldClaimTerminalWriter) Write(body []byte) (int, error) {
	w.writes++
	if w.writes == 2 {
		close(w.entered)
		<-w.release
	}
	return w.ResponseRecorder.Write(body)
}

func TestNativeScopeClosurePreservesKnownLogicalPhysicalObligation(t *testing.T) {
	for _, boundary := range []string{"revoke", "session"} {
		for _, boundaryMode := range []string{"none", "rollback", "committed"} {
			for _, mode := range []string{"rollback", "committed"} {
				t.Run(boundary+"/"+boundaryMode+"/"+mode, func(t *testing.T) {
					h := newUnsentLifecycleHarness(t, "claim-physical")
					ctx := context.Background()
					h.serve(t, unsentInit, "")
					h.serve(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "session")
					h.serve(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "session")
					wire := "id: claim-progress\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"p\",\"progress\":1}}\n\nid: claim-result\ndata: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[]}}\n\n"
					h.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}, nil
					}
					request, id := h.capture(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{},"_meta":{"progressToken":"p"}}}`, "session", "")
					h.held.target = id
					close(h.held.release)
					writer := &heldClaimTerminalWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
					var releaseOnce sync.Once
					release := func() { releaseOnce.Do(func() { close(writer.release) }) }
					done := make(chan nativemcp.Terminal, 1)
					go func() { done <- h.module.Serve(ctx, request, writer) }()
					var receipt nativetool.StartReceipt
					defer func() {
						release()
						select {
						case <-done:
						case <-time.After(2 * time.Second):
							t.Error("physical consumer did not join")
						}
						h.f.store.nativeTools.issuerMu.Lock()
						h.f.store.nativeTools.committer = sqlTransactionCommitter{}
						h.f.store.nativeTools.issuerMu.Unlock()
						if receipt.OperationID() != "" {
							audit, err := scanEgressAttempt(h.f.store.database.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, receipt.OperationID()))
							if err == nil && !audit.Attempt.Terminal() {
								if err := h.f.store.NativeToolRepository().FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "failed", Reason: "transport_failed", ObservedAt: h.now()}); err != nil {
									t.Error("fixture final physical cleanup", err)
								}
							}
						}
					}()
					approveUnsentLifecycle(t, h.f.store.NativeToolRepository(), id)
					select {
					case started := <-h.held.started:
						if started.err != nil {
							t.Fatal(started.err)
						}
						receipt = started.receipt
					case <-time.After(2 * time.Second):
						t.Fatal("real Start missing")
					}
					select {
					case <-writer.entered:
					case <-time.After(2 * time.Second):
						t.Fatal("logical terminal/held writer missing")
					}
					repo := h.f.store.NativeToolRepository()
					op, err := repo.Get(ctx, id)
					before, readErr := scanEgressAttempt(h.f.store.database.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, id))
					if err != nil || readErr != nil || op.State != "completed" || before.Attempt.Terminal() {
						t.Fatal("not at actual logical-before-physical boundary")
					}
					var authority nativetool.StreamAuthority
					h.f.store.nativeTools.issuerMu.Lock()
					for a, s := range h.f.store.nativeTools.streams {
						if s.receipt == receipt {
							authority = a
						}
					}
					h.f.store.nativeTools.issuerMu.Unlock()
					if authority.ExpiresAt().IsZero() {
						t.Fatal("real original registration missing")
					}
					fault := &claimPhysicalFinishFault{operation: id, mode: mode, boundary: boundary, boundaryMode: boundaryMode}
					h.f.store.nativeTools.issuerMu.Lock()
					h.f.store.nativeTools.committer = fault
					h.f.store.nativeTools.issuerMu.Unlock()
					var boundaryErr error
					if boundary == "revoke" {
						scope, err := repo.GetScope(ctx, h.f.capture)
						if err != nil {
							t.Fatal(err)
						}
						boundaryErr = repo.Revoke(ctx, h.f.capture, scope.Revision, "revoked", h.now())
					} else {
						var sid string
						var revision uint64
						if err := h.f.store.database.QueryRow(`SELECT s.session_id,s.revision FROM native_tool_sessions s JOIN native_tool_bindings b USING(session_id) WHERE b.operation_id=?`, id).Scan(&sid, &revision); err != nil {
							t.Fatal(err)
						}
						_, boundaryErr = repo.CloseSession(ctx, nativetool.ID(sid), revision, h.now())
					}
					if boundaryMode == "none" && boundaryErr != nil || boundaryMode != "none" && (!fault.boundaryUsed.Load() || !errors.Is(boundaryErr, nativetool.ErrUnavailable)) {
						t.Fatal("actual closure fault missing or returned wrong result", boundaryErr)
					}
					h.f.store.nativeTools.issuerMu.Lock()
					registration := h.f.store.nativeTools.streams[authority]
					retained := registration != nil && registration.cleanupOnly && registration.timer != nil
					h.f.store.nativeTools.issuerMu.Unlock()
					release()
					select {
					case terminal := <-done:
						done <- terminal
					case <-time.After(3 * time.Second):
						t.Fatal("real physical Finish fault did not return")
					}
					if !fault.used.Load() || fault.physical.Outcome() != egressaudit.OutcomeCompleted || !fault.physical.ByteCountsKnown() || fault.physical.BytesIn() <= 0 || fault.physical.BytesOut() <= 0 || fault.blocked.Load() == 0 {
						t.Fatal("actual physical transaction fault did not fire")
					}
					after, err := scanEgressAttempt(h.f.store.database.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, id))
					if err != nil {
						t.Fatal(err)
					}
					if mode == "rollback" && after.Attempt.Terminal() {
						t.Fatal("blocked cleanup/rollback unexpectedly committed physical terminal")
					}
					if mode == "committed" && after.Attempt != fault.physical {
						t.Fatal("commit error changed exact committed physical audit")
					}
					h.f.store.nativeTools.issuerMu.Lock()
					remaining := h.f.store.nativeTools.streams[authority] != nil
					fault.allowCleanup.Store(true)
					h.f.store.nativeTools.committer = sqlTransactionCommitter{}
					h.f.store.nativeTools.issuerMu.Unlock()
					t.Logf("boundary=%s boundaryFault=%s physicalFault=%s retainedAfterBoundary=%t registrationAfterFinish=%t physicalTerminal=%t byte_counts_known=%t sends=%d", boundary, boundaryMode, mode, retained, remaining, after.Attempt.Terminal(), after.Attempt.ByteCountsKnown(), h.transport.calls.Load())
					if !retained || !remaining {
						t.Error("scope/session drop lost existing physical cleanup receipt/timer before actual Finish fault")
					}
					if h.transport.calls.Load() != 4 {
						t.Error("cleanup repeated or omitted an actual send")
					}
					proof, err := repo.RetireObservedStream(ctx, authority, h.now())
					if err != nil {
						t.Fatal(err)
					}
					settled, err := scanEgressAttempt(h.f.store.database.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, id))
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("laterRetirementProof=%d auditTerminal=%t byte_counts_known=%t", len(proof), settled.Attempt.Terminal(), settled.Attempt.ByteCountsKnown())
					if !settled.Attempt.Terminal() {
						t.Error("actual Finish rollback left audit open without an I3 cleanup owner")
					}
					if mode == "rollback" && (settled.Attempt.Outcome() != egressaudit.OutcomeFailed || settled.Attempt.ErrorClass() != "transport_failed" || settled.Attempt.ByteCountsKnown()) {
						t.Error("retirement of rolled-back attempt did not preserve unknown effects")
					}
					final, err := repo.Get(ctx, id)
					if err != nil || final.State != "completed" {
						t.Error("cleanup rewrote known logical result", err)
					}
					if mode == "committed" && settled.Attempt != fault.physical {
						t.Error("later cleanup rewrote committed physical counts")
					}
				})
			}
		}
	}
}
