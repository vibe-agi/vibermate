package productruntime

import (
	"context"
	"database/sql"
	"errors"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/recording"
	"path/filepath"
	"testing"
	"time"
)

func maintenancePublication(t *testing.T, r *Runtime, ctx context.Context) (recordingMaintenanceRequest, recordingMaintenanceResult) {
	t.Helper()
	request := recordingMaintenanceRequest{ctx: ctx, generation: 2, ready: make(chan recordingMaintenanceResult, 1), handoff: &recordingMaintenanceHandoff{claimed: make(chan struct{})}}
	select {
	case r.recordingStorage.maintenanceRequests <- request:
	case <-time.After(time.Second):
		t.Fatal("owner did not receive maintenance")
	}
	select {
	case result := <-request.ready:
		if result.err != nil || result.lease == nil {
			t.Fatal("owner did not publish real lease", result.err)
		}
		return request, result
	case <-time.After(time.Second):
		t.Fatal("owner did not publish maintenance")
	}
	return request, recordingMaintenanceResult{}
}
func awaitMaintenanceCondition(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("maintenance ownership transition did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestMaintenanceAbandonBeforeClaimCannotReturnBufferedLease(t *testing.T) {
	for _, source := range []string{"request", "lifecycle"} {
		t.Run(source, func(t *testing.T) {
			r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
			waitRecording(t, r)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request, result := maintenancePublication(t, r, ctx)
			defer func() { result.lease.Complete(false); _ = r.Shutdown(context.Background()) }()
			if source == "request" {
				cancel()
				awaitMaintenanceCondition(t, r.recordingStorage.available)
			} else {
				r.recordingStorage.cancel()
				select {
				case <-r.recordingStorage.done:
				case <-time.After(3 * time.Second):
					t.Fatal("abandoned lifecycle did not close")
				}
			}
			lease, err := claimRecordingMaintenance(request, result)
			if lease != nil || err == nil {
				t.Fatal("abandoned buffered publication returned a live maintenance lease")
			}
		})
	}
}
func TestMaintenanceClaimBeforeCancellationOwnsActualIOUntilComplete(t *testing.T) {
	for _, source := range []string{"request", "lifecycle"} {
		t.Run(source, func(t *testing.T) {
			options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
			options.Lifecycle.ShutdownTimeout = 40 * time.Millisecond
			r := startTestRuntime(t, options)
			waitRecording(t, r)
			guard, err := instanceguard.Acquire(filepath.Join(r.paths.DataDirectory(), "server.lock"))
			if err != nil {
				t.Fatal(err)
			}
			if err = r.HandoffFailedStartHostGuard(guard); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request, result := maintenancePublication(t, r, ctx)
			lease, err := claimRecordingMaintenance(request, result)
			if err != nil || lease == nil {
				t.Fatal(err)
			}
			// The maintenance caller owns an actual write transaction in the protected
			// role until Commit returns. No ordinary worker/queue stands in for it.
			db, err := sql.Open("sqlite", r.recordingStorage.options.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = tx.Rollback()
				lease.Complete(false)
				_ = r.Shutdown(context.Background())
				select {
				case <-r.ShutdownDone():
				case <-time.After(3 * time.Second):
					t.Error("cleanup retained completed maintenance")
				}
			}()
			if _, err = tx.Exec(`UPDATE observation_storage_state SET archive_id=archive_id`); err != nil {
				t.Fatal(err)
			}
			// This is the owner's cancellation transition, deliberately ordered AFTER
			// the successful caller claim. It must lose regardless of select ordering.
			if request.handoff.abandon() {
				t.Error("cancellation abandoned an already-claimed maintenance owner")
			}
			if source == "request" {
				cancel()
			} else {
				if err = r.Shutdown(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("shutdown did not remain bounded", err)
				}
			}
			if !r.recordingStorage.paused.Load() {
				t.Error("recording resumed while maintenance still owns SQL")
			}
			if err = r.recordingStorage.catalog.Check(r.recordingStorage.catalog.Version()); !errors.Is(err, recording.ErrInspectionChanged) && !errors.Is(err, recording.ErrInspectionUnavailable) {
				t.Error("catalog exclusion released before actual I/O", err)
			}
			for _, name := range []string{"runtime-data.lock", "server.lock"} {
				probe, e := instanceguard.Acquire(filepath.Join(r.paths.DataDirectory(), name))
				if e == nil {
					probe.Release()
					t.Error("guard released during claimed maintenance", name)
				}
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			lease.Complete(false)
			awaitMaintenanceCondition(t, r.recordingStorage.unsafeMaintenance.Load)
			if source == "request" {
				if r.Status().State != RuntimeStateInitialized {
					t.Fatal("optional cancellation disabled control")
				}
				if r.recordingStorage.available() {
					t.Fatal("late Complete(false) lost unavailable latch")
				}
			} else {
				select {
				case <-r.ShutdownDone():
				case <-time.After(3 * time.Second):
					t.Fatal("actual return did not release shutdown")
				}
			}
		})
	}
}
