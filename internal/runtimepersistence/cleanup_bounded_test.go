package runtimepersistence

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

// Explicit cleanup shares the single writer with terminal Activity, usage and
// egress audit writes. It must yield between bounded batches like background
// maintenance does, or a user click can hold every other write for seconds.
func TestExplicitCleanupYieldsTheWriterBetweenBatches(t *testing.T) {
	if testing.Short() {
		t.Skip("50k-row writer contention check")
	}
	ctx := context.Background()
	store := openDisabledUsageTestStore(t, filepath.Join(t.TempDir(), "usage.db"))
	defer func() { shutdownTestStore(t, store) }()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	now := start.AddDate(0, 0, 8)
	policy, err := store.SetUsagePolicy(ctx, runtimeusage.CollectionPolicy{Enabled: true, RetentionDays: 90, Revision: 1}, start)
	if err != nil {
		t.Fatal(err)
	}
	seedBulkUsage(t, store, 50000, 1000, start)
	if _, err := store.database.Exec(`UPDATE runtime_usage_observations SET expires_at_unix_ms=?`, start.AddDate(0, 0, 90).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	policy.RetentionDays = 7
	if _, err := store.SetUsagePolicy(ctx, policy, now); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := store.CleanupExpired(ctx, now)
		done <- err
	}()
	var slowest time.Duration
	for n := range 10 {
		budget, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		began := time.Now()
		err := store.RecordUsage(budget, runtimeusage.Observation{
			ExchangeID: fmt.Sprintf("during-cleanup-%d", n), StartedAt: now, OccurredAt: now,
			Status: activity.StatusSucceeded,
		})
		cancel()
		slowest = max(slowest, time.Since(began))
		if err != nil {
			t.Fatalf("terminal usage waited behind explicit cleanup: %v (slowest %v)", err, slowest)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := store.database.QueryRow(`SELECT count(*) FROM runtime_usage_observations`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 10 {
		t.Fatalf("explicit cleanup left %d observations, want only the 10 fresh ones", remaining)
	}
	t.Logf("slowest concurrent terminal usage write: %v", slowest)
}
