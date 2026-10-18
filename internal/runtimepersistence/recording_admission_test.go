package runtimepersistence

import (
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

func TestPublishedMirrorRejectsOriginalDeletedScopeAtSink(t *testing.T) {
	control := inspectionControlFixture(t)
	_, current := inspectionSourcesFixture(t)
	nativeExec(t, control.store.database, `INSERT INTO recording_capture_fences VALUES('manual_capture','doomed',7,0),('manual_capture','other',9,0)`)
	if err := current.PublishInspectionMirror(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	scope := recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: "doomed"}, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 7}}
	observation := recording.CaptureObservation{ObservedAt: time.Now().UTC(), CredentialRevision: 1}
	if err := current.PutCaptureObservation(t.Context(), scope, observation); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_id='doomed';UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
	if err := current.PublishInspectionMirror(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	observation.ObservedAt = observation.ObservedAt.Add(time.Second)
	if err := current.PutCaptureObservation(t.Context(), scope, observation); !errors.Is(err, recording.ErrObservationDiscarded) {
		t.Fatalf("deleted original callback accepted: %v", err)
	}
	event := connectionevent.Event{ConnectionID: "late-doomed", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Now().UTC()}
	writer := current.ConnectionEventRepository().(connectionevent.ScopedStore)
	if err := writer.PutConnection(t.Context(), scope, event); !errors.Is(err, recording.ErrObservationDiscarded) {
		t.Fatalf("late scoped connection accepted: %v", err)
	}
	scope.Capture.ID = "other"
	scope.Fence.CaptureGeneration = 9
	event.ConnectionID = "retained"
	if err := writer.PutConnection(t.Context(), scope, event); err != nil {
		t.Fatal("unrelated original scope rejected:", err)
	}
	var count int
	if err := current.store.reads.QueryRow(`SELECT count(*) FROM runtime_connection_events`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rejected transaction left an unscoped row: %d %v", count, err)
	}
	nativeExec(t, current.store.database, `DELETE FROM inspection_mirror_active`)
	if err := current.PutCaptureObservation(t.Context(), scope, observation); !errors.Is(err, recording.ErrObservationDiscarded) {
		t.Fatalf("missing published visibility became permissive: %v", err)
	}
}

func TestPublishedPolicyRejectsDisabledAndReenabledOldCallbacks(t *testing.T) {
	control := inspectionControlFixture(t)
	_, current := inspectionSourcesFixture(t)
	now := time.Now().UTC().Add(time.Second)
	epoch, _ := recording.NewEpoch(1)
	stamp, _ := recording.NewStamp(epoch, captureidentity.Reference{}, recording.Fence{ArchiveEpoch: 1})
	value := runtimeusage.CollectedObservation{Value: runtimeusage.Observation{ExchangeID: "before-disable", StartedAt: now, OccurredAt: now, Status: activity.StatusSucceeded}, PolicyRevision: 1, ExpiresAt: now.Add(365 * 24 * time.Hour), Stamp: stamp}
	if err := current.PublishInspectionMirror(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	if err := current.PutCollectedUsage(t.Context(), value); err != nil {
		t.Fatal(err)
	}
	policy, err := control.UsagePolicyRepository().UsagePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled = false
	policy, err = control.UsagePolicyRepository().SetUsagePolicy(t.Context(), policy, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err = current.PublishInspectionMirror(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	value.Value.ExchangeID = "late-disabled"
	if err = current.PutCollectedUsage(t.Context(), value); !errors.Is(err, runtimeusage.ErrObservationDiscarded) {
		t.Fatalf("disabled callback retained: %v", err)
	}
	policy.Enabled = true
	policy, err = control.UsagePolicyRepository().SetUsagePolicy(t.Context(), policy, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err = current.PublishInspectionMirror(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	value.Value.ExchangeID = "late-reenabled"
	if err = current.PutCollectedUsage(t.Context(), value); !errors.Is(err, runtimeusage.ErrObservationDiscarded) {
		t.Fatalf("old interval revived: %v", err)
	}
	value.Value.ExchangeID = "new-interval"
	value.Value.StartedAt = now.Add(3 * time.Second)
	value.Value.OccurredAt = value.Value.StartedAt
	value.PolicyRevision = policy.Revision
	if err = current.PutCollectedUsage(t.Context(), value); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = current.store.reads.QueryRow(`SELECT count(*) FROM runtime_usage_observations`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("consent history/interval count: %d %v", count, err)
	}
}
