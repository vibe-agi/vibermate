package runtimepersistence

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// A recording transaction must not hold Capture creation or lease renewal.
func TestStorageRoleControlCompletesBeforeObservationWriterRelease(t *testing.T) {
	ctx := context.Background()
	store, err := OpenControl(ctx, ControlOptions{DatabasePath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "test-layout", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	observationsOwner, err := OpenObservations(ctx, ObservationOptions{DatabasePath: filepath.Join(t.TempDir(), "observations.db"), LayoutID: "test-layout", ArchiveID: "test-archive", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := observationsOwner.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	runs, err := capturerun.NewManager(ctx, capturerun.DefaultOptions(store.CaptureRunRepository()))
	if err != nil {
		t.Fatal(err)
	}
	defer runs.Shutdown(ctx)
	// Hold a real writer in the independent observation file.
	observations := sql.OpenDB(newSQLiteConnector(observationsOwner.store.databasePath, time.Second))
	defer observations.Close()
	lock, err := observations.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err := lock.ExecContext(ctx, `UPDATE observation_storage_state SET archive_id=archive_id`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		grant, err := runs.Create(work, capturerun.CreateCommand{CWD: "/workspace", CanonicalExecutablePath: "/bin/test-agent", ExecutableLabel: "codex", CatalogRevision: 1, Lifetime: 10 * time.Minute})
		if err == nil {
			_, err = runs.Attach(work, grant.Run.ID, grant.ControlCapability, 42)
		}
		if err == nil {
			_, err = runs.Heartbeat(work, grant.Run.ID, grant.ControlCapability, 10*time.Minute)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("control failed while observation writer locked: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		cancel()
		_ = lock.Rollback()
		<-done
		t.Fatal(fmt.Errorf("control did not complete before observation writer release"))
	}
}

func TestStorageRoleNativeAuthorityNeedsNoObservationOwner(t *testing.T) {
	ctx := context.Background()
	control, err := OpenControl(ctx, ControlOptions{DatabasePath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "native-layout", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, id, rev := nativeFixtureOnStore(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, true, nil, control.store, egressprofile.Direct())
	authority := control.NativeToolRepository()
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, control.store.NativeToolRepository(), id, now)
	op, err := authority.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if op.RecordingScope.Capture != op.Capture || op.RecordingScope.Fence.ArchiveEpoch != 1 || op.RecordingScope.Fence.CaptureGeneration != 1 {
		t.Fatalf("missing original recording scope: %+v", op.RecordingScope)
	}
	if _, err := control.store.database.Exec(`UPDATE runtime_storage_state SET archive_epoch=2`); err != nil {
		t.Fatal(err)
	}
	receipt, err := authority.Start(ctx, id, "authority-without-observations", rev, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.VerifyStartReceipt(ctx, receipt, now); err != nil {
		t.Fatal(err)
	}
	if err := authority.Finish(ctx, id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", BytesIn: 1, BytesOut: 2, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	final, err := authority.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if final.RecordingScope != op.RecordingScope {
		t.Fatal("old operation recording scope upgraded after epoch change")
	}
}
