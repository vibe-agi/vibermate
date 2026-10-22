package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
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

func TestStorageRoleNativeMetadataRetiresIndependentOfFullRetention(t *testing.T) {
	ctx := context.Background()
	control, err := OpenControl(ctx, ControlOptions{DatabasePath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "retention-layout", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, id, rev := nativeFixtureOnStore(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 3650}, true, nil, control.store, egressprofile.Direct())
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, control.store.NativeToolRepository(), id, now)
	if _, err := control.NativeToolRepository().Start(ctx, id, "retention-delivery", rev, now); err != nil {
		t.Fatal(err)
	}
	if err := control.NativeToolRepository().Finish(ctx, id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.RetireNativeMetadata(ctx, now.Add(31*24*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := control.NativeToolRepository().Get(ctx, id); !errors.Is(err, nativetool.ErrNotFound) {
		t.Fatalf("full content policy retained control metadata: %v", err)
	}
	var claims int
	if err := control.store.database.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, id).Scan(&claims); err != nil || claims != 1 {
		t.Fatalf("protocol once identity lost: %d %v", claims, err)
	}
}

func TestStorageRoleRawStartupRetainsExpiryForMaintenance(t *testing.T) {
	ctx := context.Background()
	store, err := OpenObservations(ctx, ObservationOptions{DatabasePath: filepath.Join(t.TempDir(), "observations.db"), LayoutID: "raw-layout", ArchiveID: "archive", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	now := time.Now().UTC()
	record := rawEvidenceRecordForTest("writer-1.1", 1, rawevidence.LayerClientIngress, []byte("independent-observation-body"), []byte(`{"version":1,"headers":[]}`))
	record.RecordingFence = recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}
	record.ObservedAt = now.Add(-time.Hour)
	record.ExpiresAt = now.Add(time.Minute)
	repo := store.RawEvidenceRepository()
	if err := repo.AppendBatch(ctx, []rawevidence.StoredEnvelope{record}, now); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListExchange(ctx, record.ExchangeID)
	if err != nil || len(got) != 1 || got[0].RecordingScope() != record.RecordingScope() {
		t.Fatal("optional graph lost original scope or was not published", err)
	}
	recovery, err := repo.BeginWriterSession(ctx, rawevidence.WriterSession{WriterID: "next", StartedAt: now.Add(time.Hour), MaximumUnflushedTime: time.Millisecond})
	if err != nil || recovery.PurgedExpiredEnvelopes != 0 {
		t.Fatalf("scoped startup erased proof before durable maintenance: %+v %v", recovery, err)
	}
	got, err = repo.ListExchange(ctx, record.ExchangeID)
	if err != nil || len(got) != 1 {
		t.Fatal("scoped cleanup owner lost its expired graph root", err)
	}
}

func TestStorageRoleRetirementKeepsUnfinishedPhysicalDelivery(t *testing.T) {
	ctx := context.Background()
	control, err := OpenControl(ctx, ControlOptions{DatabasePath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "physical-retirement", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, id, rev := nativeFixtureOnStore(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, true, nil, control.store, egressprofile.Direct())
	repo := control.NativeToolRepository()
	now := time.Now().UTC().Truncate(time.Millisecond)
	base, _ := nativeDecisionFixture(t, control.store, id)
	nativeAllow(t, control.store.NativeToolRepository(), id, now)
	receipt, err := repo.Start(ctx, id, "pending-physical", rev, now)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repo.OpenObservedStream(ctx, receipt, base, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteStreamRPC(ctx, stream, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.RetireNativeMetadata(ctx, now.Add(31*24*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, id); err != nil {
		t.Fatalf("logical completion discarded unfinished physical proof: %v", err)
	}
	if err := repo.FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.RetireNativeMetadata(ctx, now.Add(31*24*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, id); !errors.Is(err, nativetool.ErrNotFound) {
		t.Fatal("finished physical proof failed to retire", err)
	}
}

func TestStorageRoleRejectsForeignRoleAndLayout(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	controlPath := filepath.Join(dir, "control.db")
	observationPath := filepath.Join(dir, "observations.db")
	controlOptions := ControlOptions{DatabasePath: controlPath, LayoutID: "layout", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second}
	observationOptions := ObservationOptions{DatabasePath: observationPath, LayoutID: "layout", ArchiveID: "archive", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second}
	control, err := OpenControl(ctx, controlOptions)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	observation, err := OpenObservations(ctx, observationOptions)
	if err != nil {
		t.Fatal(err)
	}
	if err := observation.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	controlOptions.DatabasePath = observationPath
	if got, err := OpenControl(ctx, controlOptions); got != nil || !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatal("observation file became control authority", err)
	}
	controlOptions.DatabasePath = controlPath
	controlOptions.LayoutID = "foreign"
	if got, err := OpenControl(ctx, controlOptions); got != nil || !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatal("foreign layout became control authority", err)
	}
	observationOptions.DatabasePath = controlPath
	if got, err := OpenObservations(ctx, observationOptions); got != nil || !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatal("control file became observation role", err)
	}
	observationOptions.DatabasePath = observationPath
	observationOptions.ArchiveID = "foreign"
	if got, err := OpenObservations(ctx, observationOptions); got != nil || !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatal("foreign archive identity accepted", err)
	}
}
