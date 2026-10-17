package runtimepersistence

import (
	"context"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/nativemcp"
)

func TestNativeUnsentCallbackCleanupReadFailureFencesKnownFinish(t *testing.T) {
	for _, kind := range []string{"form", "url"} {
		t.Run(kind, func(t *testing.T) {
			h := newUnsentLifecycleHarness(t, kind)
			// A private real admission gate isolates the native repository outage
			// from fixture-manager teardown. It is installed before any native work.
			gate := newOperationGate()
			h.f.store.nativeTools.operations = gate
			request, id := h.prepare(t)
			h.held.target = id
			var once sync.Once
			release := func() { once.Do(func() { close(h.held.release) }) }
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			done := make(chan struct{})
			var terminal nativemcp.Terminal
			defer func() {
				release()
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("callback outage call did not drain")
				}
			}()
			h.held.afterFinish = func(err error) {
				if err == nil {
					gate.closeAdmission()
				}
			}
			go func() { terminal = h.module.Serve(ctx, request, httptest.NewRecorder()); close(done) }()
			approveUnsentLifecycle(t, h.f.store.NativeToolRepository(), id)
			var started unsentStartObservation
			select {
			case started = <-h.held.started:
			case <-ctx.Done():
				t.Fatal("actual callback Start missing")
			}
			if started.err != nil || started.receipt.OperationID() != id {
				t.Fatal("actual callback Start failed")
			}
			op, err := h.f.store.NativeToolRepository().Get(context.Background(), id)
			if err != nil || op.ApprovalID == nil {
				t.Fatal("consumed approval missing")
			}
			approval, err := h.f.store.NativeToolRepository().GetApproval(context.Background(), *op.ApprovalID)
			if err != nil || approval.ConsumedAt == nil {
				t.Fatal("actual Allow was not consumed")
			}
			parent, err := scanEgressAttempt(h.f.store.database.QueryRow(nativeDeliverySelect+` WHERE operation_id=?`, h.parent))
			if err != nil {
				t.Fatal(err)
			}
			h.invalidate(t, id)
			sends := h.transport.calls.Load()
			release()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("callback cleanup outage did not finish")
			}
			if terminal.Outcome != nativemcp.OutcomeUncertain || terminal.Reason != nativemcp.ReasonStorageUnavailable || h.transport.calls.Load() != sends {
				t.Fatal("failed explicit cleanup was silently reported successful")
			}
			if _, err := h.f.store.NativeToolRepository().Get(context.Background(), id); err == nil {
				t.Fatal("actual repository outage did not fire")
			}
			physical, err := scanEgressAttempt(h.f.store.database.QueryRow(nativeDeliverySelect+` WHERE operation_id=?`, id))
			if err != nil || !physical.Attempt.Terminal() || !physical.Attempt.ByteCountsKnown() || physical.Attempt.BytesIn() != 0 || physical.Attempt.BytesOut() != 0 {
				t.Fatal("successful prior Finish was not preserved")
			}
			probe, _ := h.capture(t, "POST", unsentInit, "", "")
			if result := h.module.Serve(context.Background(), probe, httptest.NewRecorder()); result.Outcome == nativemcp.OutcomeCompleted || h.transport.calls.Load() != sends {
				t.Fatal("cleanup failure reactivated owner")
			}
			if err := h.module.Close(context.Background()); err == nil {
				t.Fatal("Close hid native repository outage")
			}
			// Fresh Store/issuer, not an OS process restart; old native admission
			// remains closed and its executor remains closed throughout recovery.
			reopened := openTestStore(t, h.f.store.databasePath)
			defer shutdownTestStore(t, reopened)
			if err := reopened.NativeToolRepository().Recover(context.Background(), h.now()); err != nil {
				t.Fatal(err)
			}
			final, err := reopened.NativeToolRepository().Get(context.Background(), id)
			if err != nil || final.State != "failed" || final.BytesIn == nil || final.BytesOut == nil || *final.BytesIn != 0 || *final.BytesOut != 0 {
				t.Fatal("Recover rewrote known failed child")
			}
			after, err := scanEgressAttempt(reopened.database.QueryRow(nativeDeliverySelect+` WHERE operation_id=?`, id))
			if err != nil || after.Attempt != physical.Attempt {
				t.Fatal("Recover rewrote immutable child audit")
			}
			parentAfter, err := scanEgressAttempt(reopened.database.QueryRow(nativeDeliverySelect+` WHERE operation_id=?`, h.parent))
			if err != nil || parentAfter.Attempt != parent.Attempt {
				t.Fatal("Recover rewrote parent audit")
			}
			retained, err := reopened.NativeToolRepository().GetApproval(context.Background(), approval.ID)
			if err != nil || !reflect.DeepEqual(approval, retained) {
				t.Fatal("Recover changed consumed approval")
			}
			if reopened.NativeToolRepository().VerifyStartReceipt(context.Background(), started.receipt, h.now()) == nil {
				t.Fatal("new Store revived old receipt")
			}
		})
	}
}
