package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

type unsentLifecycleCommitFault struct {
	target      nativetool.ID
	phase, mode string
	recovery    bool
	attempt     string
	armed, used atomic.Bool
	physical    egressaudit.Attempt
}

func (f *unsentLifecycleCommitFault) Commit(tx *sql.Tx) error {
	if !f.armed.Load() {
		return tx.Commit()
	}
	var state, session, scope string
	err := tx.QueryRow(`SELECT o.state,s.state,c.state FROM native_tool_operations o JOIN native_tool_bindings b USING(operation_id) JOIN native_tool_sessions s USING(session_id) JOIN native_tool_scopes c ON c.capture_kind=o.capture_kind AND c.capture_id=o.capture_id WHERE o.operation_id=?`, f.target).Scan(&state, &session, &scope)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	audit, err := scanEgressAttempt(tx.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, f.target))
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if audit.Attempt.ID() != f.attempt {
		return errors.New("fault target attempt mismatch")
	}
	first := state == "failed" && audit.Attempt.Terminal()
	if f.phase == "close" {
		first = first && session == "revoked" && scope == "active"
	}
	if f.phase == "sweep" {
		first = state == "uncertain" && session == "revoked" && scope == "active" && audit.Attempt.Terminal()
	}
	if first && f.used.CompareAndSwap(false, true) {
		f.physical = audit.Attempt
		if f.mode == "committed" {
			return (commitThenError{}).Commit(tx)
		}
		return (rollbackThenError{}).Commit(tx)
	}
	if f.used.Load() && f.mode == "persistent" && (state == "uncertain" || session == "revoked" || scope == "revoked" || f.recovery && state == "failed" && audit.Attempt.Terminal() && audit.Attempt.ErrorClass() == "transport_failed" && !audit.Attempt.ByteCountsKnown()) {
		return (rollbackThenError{}).Commit(tx)
	}
	return tx.Commit()
}

func TestNativeUnsentLifecycleCleanupFaults(t *testing.T) {
	for _, kind := range []string{"initialize", "initialized", "form", "url", "recovery"} {
		phases := []string{"finish"}
		if kind == "initialize" || kind == "initialized" {
			phases = append(phases, "close")
		}
		for _, phase := range phases {
			for _, mode := range []string{"rollback", "committed", "persistent", "persistent_recover"} {
				t.Run(kind+"/"+phase+"/"+mode, func(t *testing.T) {
					h := newUnsentLifecycleHarness(t, kind)
					ctx := context.Background()
					request, id := h.prepare(t)
					h.held.target = id
					persistent := strings.HasPrefix(mode, "persistent")
					faultMode := mode
					if persistent {
						faultMode = "persistent"
					}
					fault := &unsentLifecycleCommitFault{target: id, phase: phase, mode: faultMode, recovery: kind == "recovery"}
					originalCommitter := h.f.store.nativeTools.committer
					h.f.store.nativeTools.committer = fault
					var releaseOnce sync.Once
					release := func() { releaseOnce.Do(func() { close(h.held.release) }) }
					callCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
					done := make(chan struct{})
					var terminal nativemcp.Terminal
					defer func() {
						fault.armed.Store(false)
						release()
						cancel()
						select {
						case <-done:
						case <-time.After(3 * time.Second):
							t.Error("lifecycle fault call did not drain")
						}
						if err := h.module.Close(ctx); err != nil {
							t.Error(err)
						}
						h.f.store.nativeTools.committer = originalCommitter
					}()
					go func() { terminal = h.module.Serve(callCtx, request, httptest.NewRecorder()); close(done) }()
					if kind == "form" || kind == "url" {
						approveUnsentLifecycle(t, h.f.store.NativeToolRepository(), id)
					}
					var started unsentStartObservation
					select {
					case started = <-h.held.started:
					case <-callCtx.Done():
						t.Fatal("actual Start never returned")
					}
					if started.err != nil || started.receipt.OperationID() != id {
						t.Fatal("real own Start failed")
					}
					fault.attempt = started.receipt.AttemptID()
					var approval nativetool.Approval
					if kind == "form" || kind == "url" {
						op, err := h.f.store.NativeToolRepository().Get(ctx, id)
						if err != nil || op.ApprovalID == nil {
							t.Fatal("real decision missing")
						}
						approval, err = h.f.store.NativeToolRepository().GetApproval(ctx, *op.ApprovalID)
						if err != nil || approval.ConsumedAt == nil {
							t.Fatal("real decision not consumed")
						}
					}
					var original egressaudit.Attempt
					var parentAuthority nativetool.StreamAuthority
					if h.parent != "" {
						row, err := scanEgressAttempt(h.f.store.database.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, h.parent))
						if err != nil {
							t.Fatal(err)
						}
						original = row.Attempt
						h.f.store.nativeTools.issuerMu.Lock()
						for authority, stream := range h.f.store.nativeTools.streams {
							if stream.receipt.OperationID() == h.parent {
								parentAuthority = authority
							}
						}
						h.f.store.nativeTools.issuerMu.Unlock()
					}
					h.invalidate(t, id)
					sends := h.transport.calls.Load()
					fault.armed.Store(true)
					release()
					select {
					case <-done:
					case <-callCtx.Done():
						t.Fatal("unsent cleanup did not finish")
					}
					if !fault.used.Load() || !h.held.verified.Load() || h.transport.calls.Load() != sends || terminal.Outcome != nativemcp.OutcomeUncertain || terminal.Reason != nativemcp.ReasonStorageUnavailable {
						t.Fatalf("real cleanup fault missed or claimed success: used=%t verified=%t result=%+v", fault.used.Load(), h.held.verified.Load(), terminal)
					}
					op, err := h.f.store.NativeToolRepository().Get(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					audit, err := scanEgressAttempt(h.f.store.database.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, id))
					if err != nil {
						t.Fatal(err)
					}
					known := phase == "close" || mode == "committed"
					unknownState := "uncertain"
					if kind == "recovery" {
						unknownState = "failed"
					}
					t.Logf("op=%s/%s auditTerminal=%t byte_counts_known=%t countsValid=%t/%t", op.State, op.Reason, audit.Attempt.Terminal(), audit.Attempt.ByteCountsKnown(), op.BytesIn != nil, op.BytesOut != nil)
					if known {
						if op.State != "failed" || op.BytesIn == nil || op.BytesOut == nil || *op.BytesIn != 0 || *op.BytesOut != 0 || !audit.Attempt.ByteCountsKnown() || audit.Attempt != fault.physical {
							t.Fatal("cleanup rewrote known-zero commit")
						}
					} else if persistent {
						if op.State != "started" || audit.Attempt.Terminal() {
							t.Fatal("persistent rollback lost explicit open obligation")
						}
					} else if op.State != unknownState || op.BytesIn != nil || op.BytesOut != nil || audit.Attempt.ByteCountsKnown() {
						t.Fatal("rollback manufactured known counts")
					}
					if persistent {
						probe, _ := h.capture(t, "POST", strings.Replace(unsentInit, `"id":1`, `"id":92`, 1), "", "")
						if result := h.module.Serve(ctx, probe, httptest.NewRecorder()); result.Outcome == nativemcp.OutcomeCompleted || h.transport.calls.Load() != sends {
							t.Fatal("persistent cleanup reactivated owner")
						}
						if err := h.module.Close(ctx); err == nil {
							t.Fatal("Close hid persistent cleanup failure")
						}
					}
					if mode == "persistent_recover" {
						reopened := openTestStore(t, h.f.store.databasePath)
						defer shutdownTestStore(t, reopened)
						if err := reopened.NativeToolRepository().Recover(ctx, h.now()); err != nil {
							t.Fatal(err)
						}
						if reopened.NativeToolRepository().VerifyStartReceipt(ctx, started.receipt, h.now()) == nil {
							t.Fatal("fresh Store revived receipt")
						}
						unknownState = "uncertain"
					}
					fault.armed.Store(false)
					if kind == "recovery" {
						// Exercise exact I3 settlement before general Close, retaining
						// its documented failed/unknown child outcome deterministically.
						if _, err := h.f.store.NativeToolRepository().RetireObservedStream(ctx, parentAuthority, h.now()); err != nil {
							t.Fatal(err)
						}
					}
					if err := h.module.Close(ctx); err != nil {
						t.Fatal(err)
					}
					final, err := h.f.store.NativeToolRepository().Get(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					finalAudit, err := scanEgressAttempt(h.f.store.database.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, id))
					if err != nil {
						t.Fatal(err)
					}
					if !finalAudit.Attempt.Terminal() || known && finalAudit.Attempt != audit.Attempt || !known && (final.State != unknownState || finalAudit.Attempt.ByteCountsKnown()) {
						t.Fatal("later Close failed to reconcile/preserve terminal")
					}
					if h.parent != "" {
						after, err := scanEgressAttempt(h.f.store.database.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, h.parent))
						if err != nil || after.Attempt != original {
							t.Fatal("child cleanup changed original physical audit")
						}
					}
					if approval.ID != "" {
						after, err := h.f.store.NativeToolRepository().GetApproval(ctx, approval.ID)
						if err != nil || !reflect.DeepEqual(approval, after) {
							t.Fatal("cleanup changed spent approval")
						}
					}
					var once, audits int
					if err := h.f.store.database.QueryRow(`SELECT (SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?),(SELECT COUNT(*) FROM runtime_egress_attempts WHERE parent_id=?)`, id, id).Scan(&once, &audits); err != nil || once != 1 || audits != 1 {
						t.Fatal("cleanup changed exactly-once provenance")
					}
					if h.f.store.NativeToolRepository().VerifyStartReceipt(ctx, started.receipt, h.now()) == nil || h.transport.calls.Load() != sends {
						t.Fatal("cleanup revived receipt or resent")
					}
				})
			}
		}
	}
}
