package runtimepersistence

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

func TestScopedRawStartupPreservesCleanupProofAndPhysicalPurge(t *testing.T) {
	f := newEgressInspectionFixture(t, 3)
	now := time.Now().UTC().Truncate(time.Millisecond)
	marker := []byte("scoped-startup-physical-cleanup-payload")
	root := rawEvidenceRecordForTest("startup-expired", 1, rawevidence.LayerProviderEgress, marker, []byte(`{"version":1,"headers":[]}`))
	root.ScopeID = f.scope.Capture.ID
	root.RecordingFence = f.scope.Fence
	root.ObservedAt = now.Add(-2 * time.Hour)
	root.ExpiresAt = now.Add(-time.Hour)
	if err := f.current.RawEvidenceRepository().AppendBatch(t.Context(), []rawevidence.StoredEnvelope{root}, root.ObservedAt); err != nil {
		t.Fatal(err)
	}
	recovery, err := f.current.RawEvidenceRepository().BeginWriterSession(t.Context(), rawevidence.WriterSession{WriterID: "scoped-startup", StartedAt: now, MaximumUnflushedTime: time.Second})
	if err != nil || recovery.PurgedExpiredEnvelopes != 0 {
		t.Fatalf("startup erased optional proof: %+v %v", recovery, err)
	}
	if needed, err := RecordingCleanupNeeded(t.Context(), f.control, f.reader.catalog, now); err != nil || !needed {
		t.Fatalf("expired startup graph hidden from cleanup: %v %v", needed, err)
	}
	token, err := f.reader.catalog.BeginMaintenance(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	released, err := f.current.PurgeRecordingEvidence(t.Context(), f.control, token, now)
	if err != nil || released.Envelopes != 1 {
		t.Fatalf("owned physical purge: %+v %v", released, err)
	}
	if changed, err := token.RecordingSourceMutated(1); err != nil || !changed {
		t.Fatalf("scoped raw purge did not mark actual current work: %v %v", changed, err)
	}
	if err = f.current.VerifyRecordingCleanup(t.Context(), token, now); err != nil {
		t.Fatal(err)
	}
	if err = f.current.CompactRecordingSource(t.Context(), token, 1); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(f.current.store.databasePath)
	if err != nil || bytes.Contains(data, marker) {
		t.Fatalf("physical expired startup payload survived: %v", err)
	}
}
