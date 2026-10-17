package runtimepersistence

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"path/filepath"
	"testing"
	"time"
)

func TestUsageControlCASRetainsPolicyRevisionCapsWithoutObservations(t *testing.T) {
	ctx := context.Background()
	c, err := OpenControl(ctx, ControlOptions{DatabasePath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "usage", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Shutdown(ctx)
	repo := c.UsagePolicyRepository()
	p, err := repo.UsagePolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	original := p.Revision
	p.RetentionDays = 7
	p, err = repo.SetUsagePolicy(ctx, p, time.Now())
	if err != nil {
		t.Fatalf("control CAS consulted observation storage: %v", err)
	}
	var through int64
	if err := c.store.database.QueryRow(`SELECT through_policy_revision FROM runtime_usage_retention_rules WHERE retention_days=7`).Scan(&through); err != nil || through != original {
		t.Fatalf("original revision cap: %d %v", through, err)
	}
	p.RetentionDays = 30
	p, err = repo.SetUsagePolicy(ctx, p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.database.QueryRow(`SELECT through_policy_revision FROM runtime_usage_retention_rules WHERE retention_days=7`).Scan(&through); err != nil || through != original {
		t.Fatalf("lengthening relaxed old cap: %d %v", through, err)
	}
	stale := p
	stale.Revision--
	stale.RetentionDays = 1
	if _, err := repo.SetUsagePolicy(ctx, stale, time.Now()); !errors.Is(err, runtimeusage.ErrPolicyConflict) {
		t.Fatalf("stale policy committed: %v", err)
	}
	var count int
	if err := c.store.database.QueryRow(`SELECT count(*) FROM runtime_usage_retention_rules`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed CAS changed caps: %d %v", count, err)
	}
}
func TestUsageObservationWriterNeedsOnlyFrozenCollection(t *testing.T) {
	ctx := context.Background()
	s, err := OpenObservations(ctx, ObservationOptions{DatabasePath: filepath.Join(t.TempDir(), "observations.db"), LayoutID: "usage", ArchiveID: "archive", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(ctx)
	epoch, _ := recording.NewEpoch(5)
	stamp, _ := recording.NewStamp(epoch, captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "retired-run"}, recording.Fence{ArchiveEpoch: 5, CaptureGeneration: 7})
	now := time.Now().UTC().Truncate(time.Millisecond)
	value := runtimeusage.CollectedObservation{Value: runtimeusage.Observation{ExchangeID: "usage-frozen", CaptureRunID: "retired-run", Source: "local", StartedAt: now, OccurredAt: now, Status: activity.StatusSucceeded, EnvironmentID: "env", EnvironmentName: "Frozen environment", AccountName: "Frozen account"}, PolicyRevision: 9, ExpiresAt: now.Add(24 * time.Hour), Stamp: stamp}
	if err := s.PutCollectedUsage(ctx, value); err != nil {
		t.Fatalf("writer joined control authority: %v", err)
	}
	var policy, expiry, archive, generation int64
	var id, wire string
	err = s.store.database.QueryRow(`SELECT policy_revision,expires_at_unix_ms,recording_archive_epoch,recording_capture_generation,recording_scope_id,observation_json FROM runtime_usage_observations WHERE exchange_id=?`, value.Value.ExchangeID).Scan(&policy, &expiry, &archive, &generation, &id, &wire)
	if err != nil || policy != 9 || expiry != value.ExpiresAt.UnixMilli() || archive != 5 || generation != 7 || id != "retired-run" {
		t.Fatalf("frozen coordinates: %d %d %d %d %s %v", policy, expiry, archive, generation, id, err)
	}
	epoch.Advance(6)
	value.Value.ExchangeID = "late"
	if err := s.PutCollectedUsage(ctx, value); !errors.Is(err, runtimeusage.ErrObservationDiscarded) {
		t.Fatalf("stale usage written: %v", err)
	}
}
