package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

func TestRecordingUsageDenialRejectsLateIdentityAndDetectsLoss(t *testing.T) {
	f := newUsageInspectionFixture(t)
	value := f.value("purged-identity", "profile")
	f.put(t, true, value, f.now.Add(30*24*time.Hour), 3)
	f.put(t, false, value, f.now.Add(30*24*time.Hour), 0)
	f.put(t, true, f.value("unrelated", "profile"), f.now.Add(30*24*time.Hour), 3)
	deny, ok := any(f.current).(interface {
		DenyRecordingIdentities(context.Context, *ControlStore, string, []string) error
	})
	if !ok {
		t.Fatal("durable whole-identity denial unavailable")
	}
	if err := deny.DenyRecordingIdentities(t.Context(), f.control, "usage", []string{value.ExchangeID}); err != nil {
		t.Fatal(err)
	}
	if err := f.current.PublishInspectionMirror(t.Context(), f.control); err != nil {
		t.Fatal(err)
	}
	if _, calls, err := f.scan(t); err != nil || calls != 1 {
		t.Fatalf("denied logical duplicate disclosed: calls=%d %v", calls, err)
	}
	// Physical copies can disappear only after the independent durable denial.
	maintenance, err := f.reader.catalog.BeginMaintenance(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	nativeExec(t, f.current.store.database, `DELETE FROM runtime_usage_observations WHERE exchange_id='purged-identity'`)
	nativeExec(t, f.legacy, `DELETE FROM runtime_usage_observations WHERE exchange_id='purged-identity'`)
	if err := maintenance.Close(); err != nil {
		t.Fatal(err)
	}
	epoch, _ := recording.NewEpoch(1)
	stamp, _ := recording.NewStamp(epoch, captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "usage-owner"}, recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1})
	late := runtimeusage.CollectedObservation{Value: value, Stamp: stamp, PolicyRevision: 3, ExpiresAt: f.now.Add(365 * 24 * time.Hour)}
	if err := f.current.PutCollectedUsage(t.Context(), late); !errors.Is(err, runtimeusage.ErrObservationDiscarded) {
		t.Fatalf("erased identity revived: %v", err)
	}
	// Loss of required membership cannot be repaired by a new permissive mirror.
	nativeExec(t, f.current.store.database, `DELETE FROM recording_denials`)
	if _, _, err := f.scan(t); !errors.Is(err, ErrInspectionUnavailable) {
		t.Fatalf("lost privacy coverage remained readable: %v", err)
	}
	if _, err := f.control.UsagePolicyRepository().UsagePolicy(t.Context()); err != nil {
		t.Fatalf("optional loss gated required control: %v", err)
	}
	if err := f.current.PublishInspectionMirror(t.Context(), f.control); !errors.Is(err, ErrInspectionUnavailable) {
		t.Fatalf("mirror rebuild concealed denial loss: %v", err)
	}
}

func TestRecordingDenialCorruptPrefixRejectsSinkBeforePublication(t *testing.T) {
	f := newUsageInspectionFixture(t)
	if err := f.current.DenyRecordingIdentities(t.Context(), f.control, "usage", []string{"erased", "second"}); err != nil {
		t.Fatal(err)
	}
	if err := f.current.PublishInspectionMirror(t.Context(), f.control); err != nil {
		t.Fatal(err)
	}
	// Keep count and the last certified digest unchanged: checking just those
	// scalars would silently forget the first denied identity.
	external, err := sql.Open("sqlite", f.current.store.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	nativeExec(t, external, `UPDATE recording_denials SET identity='corrupt' WHERE sequence=1`)
	epoch, _ := recording.NewEpoch(1)
	stamp, _ := recording.NewStamp(epoch, captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "usage-owner"}, recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1})
	late := runtimeusage.CollectedObservation{Value: f.value("erased", "profile"), Stamp: stamp, PolicyRevision: 3, ExpiresAt: f.now.Add(24 * time.Hour)}
	if err := f.current.PutCollectedUsage(t.Context(), late); !errors.Is(err, runtimeusage.ErrObservationDiscarded) {
		t.Fatalf("corrupt required prefix admitted a write: %v", err)
	}
}

func TestRecordingCertifiedWriterCannotSilentlyReplaceConnection(t *testing.T) {
	control := inspectionControlFixture(t)
	_, current := inspectionSourcesFixture(t)
	if err := current.PublishInspectionMirror(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	current.store.database.SetMaxIdleConns(0)
	if err := current.store.database.PingContext(t.Context()); err == nil {
		t.Fatal("certified writer silently reconnected")
	}
}

func TestRecordingDenialMetadataDowngradeCannotDisarmOwnedWriter(t *testing.T) {
	f := newUsageInspectionFixture(t)
	if err := f.current.DenyRecordingIdentities(t.Context(), f.control, "usage", []string{"erased", "second"}); err != nil {
		t.Fatal(err)
	}
	if err := f.current.PublishInspectionMirror(t.Context(), f.control); err != nil {
		t.Fatal(err)
	}
	external, err := sql.Open("sqlite", f.current.store.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	nativeExec(t, external, `UPDATE recording_admission_state SET enforced=0,required_denial_count=0,required_denial_hash=zeroblob(32);UPDATE recording_denials SET identity='corrupt' WHERE sequence=1`)
	epoch, _ := recording.NewEpoch(1)
	stamp, _ := recording.NewStamp(epoch, captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "usage-owner"}, recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1})
	late := runtimeusage.CollectedObservation{Value: f.value("erased", "profile"), Stamp: stamp, PolicyRevision: 3, ExpiresAt: f.now.Add(24 * time.Hour)}
	if err = f.current.PutCollectedUsage(t.Context(), late); !errors.Is(err, runtimeusage.ErrObservationDiscarded) {
		t.Fatalf("mutable certificate disarmed privacy admission: %v", err)
	}
}
