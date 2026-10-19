package runtimepersistence

import (
	"context"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/nativemcp"
)

func TestNativeUnsentProvisionalSweepFailureCannotReactivateOwner(t *testing.T) {
	testNativeUnsentSweepFault(t, "initialize", "new_init", "persistent", false)
}
func TestNativeUnsentSweepFaultVariants(t *testing.T) {
	for _, entry := range []struct{ kind, path string }{{"initialize", "new_init"}, {"initialized", "new_init"}, {"initialized", "old_wire"}} {
		for _, mode := range []string{"rollback", "committed", "persistent"} {
			t.Run(entry.kind+"/"+entry.path+"/"+mode, func(t *testing.T) { testNativeUnsentSweepFault(t, entry.kind, entry.path, mode, false) })
		}
	}
}
func TestNativeUnsentLateOriginalPreservesReconciledOwner(t *testing.T) {
	for _, mode := range []string{"rollback", "committed", "persistent"} {
		t.Run(mode, func(t *testing.T) { testNativeUnsentSweepFault(t, "initialize", "new_init", mode, true) })
	}
}
func testNativeUnsentSweepFault(t *testing.T, kind, entry, mode string, replaceOwner bool) {
	t.Helper()
	h := newUnsentLifecycleHarness(t, kind)
	ctx := context.Background()
	request, id := h.prepare(t)
	h.held.target = id
	entered, releaseVerify := make(chan struct{}), make(chan struct{})
	var startOnce, verifyOnce sync.Once
	releaseStart := func() { startOnce.Do(func() { close(h.held.release) }) }
	finishVerify := func() { verifyOnce.Do(func() { close(releaseVerify) }) }
	h.held.beforeVerify = func() { close(entered); <-releaseVerify }
	fault := &unsentLifecycleCommitFault{target: id, phase: "sweep", mode: mode}
	originalCommitter := h.f.store.nativeTools.committer
	h.f.store.nativeTools.committer = fault
	callCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
	done := make(chan struct{})
	var terminal nativemcp.Terminal
	defer func() {
		fault.armed.Store(false)
		releaseStart()
		finishVerify()
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("held initializer did not drain")
		}
		if err := h.module.Close(ctx); err != nil {
			t.Error(err)
		}
		h.f.store.nativeTools.committer = originalCommitter
	}()
	go func() { terminal = h.module.Serve(callCtx, request, httptest.NewRecorder()); close(done) }()
	select {
	case started := <-h.held.started:
		if started.err != nil || started.receipt.OperationID() != id {
			t.Fatal("real own Start absent")
		}
		fault.attempt = started.receipt.AttemptID()
	case <-callCtx.Done():
		t.Fatal("Start not reached")
	}
	h.invalidate(t, id)
	releaseStart()
	select {
	case <-entered:
	case <-callCtx.Done():
		t.Fatal("first-claim Verify boundary missing")
	}
	sends := h.transport.calls.Load()
	fault.armed.Store(true)
	body, sid := strings.Replace(unsentInit, `"id":1`, `"id":93`, 1), ""
	if entry == "old_wire" {
		body, sid = `{"jsonrpc":"2.0","id":93,"method":"ping"}`, "session"
	}
	probe, _ := h.capture(t, "POST", body, sid, "")
	result := h.module.Serve(ctx, probe, httptest.NewRecorder())
	if !fault.used.Load() {
		t.Fatal("actual expiry sweep close fault never fired")
	}
	if result.Outcome == nativemcp.OutcomeCompleted || h.transport.calls.Load() != sends {
		t.Error("new initialization sent after provisional CloseSession rollback")
	}
	if replaceOwner {
		oldScope, err := h.f.store.NativeToolRepository().GetScope(ctx, h.f.capture)
		if err != nil {
			t.Fatal(err)
		}
		fault.armed.Store(false)
		renew, _ := h.capture(t, "POST", strings.Replace(unsentInit, `"id":1`, `"id":94`, 1), "", "")
		if next := h.module.Serve(ctx, renew, httptest.NewRecorder()); next.Outcome != nativemcp.OutcomeCompleted {
			t.Fatal("real EnsureScope reconciliation could not initialize replacement")
		}
		h.serve(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "session")
		renewed, err := h.f.store.NativeToolRepository().GetScope(ctx, h.f.capture)
		if err != nil || renewed.OwnerEpoch == oldScope.OwnerEpoch || renewed.State != "active" {
			t.Fatal("replacement owner not actually reconciled")
		}
		before, err := scanEgressAttempt(h.f.store.database.QueryRow(nativeDeliverySelect+` WHERE operation_id=?`, id))
		if err != nil || !before.Attempt.Terminal() {
			t.Fatal("EnsureScope did not settle original attempt")
		}
		finishVerify()
		select {
		case <-done:
		case <-callCtx.Done():
			t.Fatal("late original did not finish")
		}
		afterScope, err := h.f.store.NativeToolRepository().GetScope(ctx, h.f.capture)
		after, physicalErr := scanEgressAttempt(h.f.store.database.QueryRow(nativeDeliverySelect+` WHERE operation_id=?`, id))
		if err != nil || physicalErr != nil || !reflect.DeepEqual(renewed, afterScope) || before.Attempt != after.Attempt {
			t.Fatal("late failed old handle revoked replacement owner or rewrote settled audit")
		}
		if terminal.Outcome != nativemcp.OutcomeUncertain || terminal.Reason != nativemcp.ReasonStorageUnavailable || h.transport.calls.Load() != sends+2 {
			t.Fatal("late old closure lost failure or sent")
		}
		h.serve(t, `{"jsonrpc":"2.0","id":95,"method":"ping"}`, "session")
		return
	}
	finishVerify()
	select {
	case <-done:
	case <-callCtx.Done():
		t.Fatal("first-claim refusal did not finish")
	}
	if terminal.Outcome != nativemcp.OutcomeUncertain || terminal.Reason != nativemcp.ReasonStorageUnavailable {
		t.Errorf("repeat close hid unresolved durable cleanup: %s/%s", terminal.Outcome, terminal.Reason)
	}
	t.Logf("sweep fault fired=%t extraSends=%d newInit=%s target=%s/%s", fault.used.Load(), h.transport.calls.Load()-sends, result.Outcome, terminal.Outcome, terminal.Reason)
}
