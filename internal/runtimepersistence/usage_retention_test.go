package runtimepersistence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

func TestUsagePolicyNonShorteningDoesNotRewriteHistory(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled bool
		days    int
	}{
		{"unchanged", true, 90},
		{"disable collection", false, 90},
		{"extend retention", true, 365},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := openDisabledUsageTestStore(t, filepath.Join(t.TempDir(), "usage.db"))
			defer shutdownTestStore(t, store)
			now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
			start := now.AddDate(0, 0, -91)
			policy, err := store.SetUsagePolicy(ctx, runtimeusage.CollectionPolicy{Enabled: true, RetentionDays: 90, Revision: 1}, start)
			if err != nil {
				t.Fatal(err)
			}
			for id, at := range map[string]time.Time{"expired": start, "live": now} {
				if err := store.RecordUsage(ctx, runtimeusage.Observation{ExchangeID: id, StartedAt: at, OccurredAt: at, Status: activity.StatusSucceeded}); err != nil {
					t.Fatal(err)
				}
			}
			// Fault injection at the database seam: saving settings must not need
			// to rewrite or purge existing observations. Maintenance owns deletion.
			if _, err := store.database.ExecContext(ctx, `
CREATE TRIGGER unavailable_usage_update BEFORE UPDATE ON runtime_usage_observations
BEGIN SELECT RAISE(ABORT,'synthetic history write failure'); END;
CREATE TRIGGER unavailable_usage_delete BEFORE DELETE ON runtime_usage_observations
BEGIN SELECT RAISE(ABORT,'synthetic history delete failure'); END;`); err != nil {
				t.Fatal(err)
			}
			policy.Enabled, policy.RetentionDays = test.enabled, test.days
			updated, err := store.SetUsagePolicy(ctx, policy, now)
			if err != nil {
				t.Fatalf("settings save rewrote history: %v", err)
			}
			current, err := store.UsagePolicy(ctx)
			if err != nil || current.Enabled != test.enabled || current.RetentionDays != test.days || current.Revision != updated.Revision {
				t.Fatalf("settings not published: %+v, %v", current, err)
			}
			period, _ := runtimeusage.NewQuery("2026-06-01", "2026-10-01", "UTC")
			calls := 0
			_, err = store.ScanUsage(ctx, runtimeusage.AggregationQuery{Period: period}, "", now, func(bucket runtimeusage.UsageBucket) error {
				calls += bucket.Calls
				return nil
			})
			if err != nil || calls != 1 {
				t.Fatalf("saving settings changed retained usage: calls=%d, %v", calls, err)
			}
		})
	}
}

func TestUsageShorterRetentionIsImmediateAndNeverResurrected(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")
	store := openDisabledUsageTestStore(t, path)
	defer func() { shutdownTestStore(t, store) }()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	now := start.AddDate(0, 0, 8)
	policy, err := store.SetUsagePolicy(ctx, runtimeusage.CollectionPolicy{Enabled: true, RetentionDays: 90, Revision: 1}, start)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUsage(ctx, runtimeusage.Observation{ExchangeID: "old", StartedAt: start, OccurredAt: start, Status: activity.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(ctx, `
CREATE TRIGGER unavailable_usage_update BEFORE UPDATE ON runtime_usage_observations
BEGIN SELECT RAISE(ABORT,'synthetic history write failure'); END;
CREATE TRIGGER unavailable_usage_delete BEFORE DELETE ON runtime_usage_observations
BEGIN SELECT RAISE(ABORT,'synthetic history delete failure'); END;`); err != nil {
		t.Fatal(err)
	}
	period, _ := runtimeusage.NewQuery("2026-09-01", "2026-10-01", "UTC")
	check := func(want int) {
		t.Helper()
		for _, dimension := range []string{"", "model"} {
			calls := 0
			query := runtimeusage.AggregationQuery{Period: period, Dimension: dimension}
			if dimension != "" {
				query.Limit = 50
			}
			_, err := store.ScanUsage(ctx, query, "", now, func(bucket runtimeusage.UsageBucket) error {
				calls += bucket.Calls
				return nil
			})
			if err != nil || calls != want {
				t.Fatalf("retained usage (%q): calls=%d want=%d, %v", dimension, calls, want, err)
			}
		}
	}
	check(1)
	policy.RetentionDays = 7
	policy, err = store.SetUsagePolicy(ctx, policy, now)
	if err != nil {
		t.Fatalf("shortening retention waited for history rewrites: %v", err)
	}
	check(0)
	policy.RetentionDays = 90
	policy, err = store.SetUsagePolicy(ctx, policy, now)
	if err != nil {
		t.Fatal(err)
	}
	check(0) // Extending before maintenance must not undo the earlier restriction.
	if err := store.RecordUsage(ctx, runtimeusage.Observation{ExchangeID: "new", StartedAt: now, OccurredAt: now, Status: activity.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	check(1) // New observations use the new policy, not an older pending restriction.
	shutdownTestStore(t, store)
	store = openDisabledUsageTestStore(t, path)
	check(1)
	if err := store.MaintainExpired(ctx, now); err == nil {
		t.Fatal("maintenance did not attempt to apply the shortened retention")
	}
	check(1) // A failed maintenance transaction cannot make expired usage visible.
	if _, err := store.database.ExecContext(ctx, `DROP TRIGGER unavailable_usage_update; DROP TRIGGER unavailable_usage_delete;`); err != nil {
		t.Fatal(err)
	}
	if err := store.MaintainExpired(ctx, now); err != nil {
		t.Fatal(err)
	}
	now = now.AddDate(0, 0, 10)
	check(1)
	shutdownTestStore(t, store)
	store = openDisabledUsageTestStore(t, path)
	check(1)
}

func TestUsageRetentionAtScaleDoesNotHoldWriter(t *testing.T) {
	if os.Getenv("VIBERMATE_USAGE_SCALE") != "1" {
		t.Skip("set VIBERMATE_USAGE_SCALE=1 for the isolated 200k-row retention check")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")
	store := openDisabledUsageTestStore(t, path)
	defer func() { shutdownTestStore(t, store) }()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	now := start.AddDate(0, 0, 8)
	policy, err := store.SetUsagePolicy(ctx, runtimeusage.CollectionPolicy{Enabled: true, RetentionDays: 90, Revision: 1}, start)
	if err != nil {
		t.Fatal(err)
	}
	seedBulkUsage(t, store, 200000, 1000, start)
	// Bulk fixture setup is outside the measured public operations.
	if _, err := store.database.Exec(`UPDATE runtime_usage_observations SET expires_at_unix_ms=?`, start.AddDate(0, 0, 90).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	for _, days := range []int{90, 365, 7, 365} {
		policy.RetentionDays = days
		budget, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		began := time.Now()
		policy, err = store.SetUsagePolicy(budget, policy, now)
		cancel()
		if err != nil {
			t.Fatalf("200k-row policy save (%d days) exceeded budget: %v", days, err)
		}
		t.Logf("200k-row policy save (%d days): %v", days, time.Since(began))
	}
	period, _ := runtimeusage.NewQuery("2026-09-01", "2026-10-01", "UTC")
	check := func(want int) {
		t.Helper()
		calls := 0
		_, err := store.ScanUsage(ctx, runtimeusage.AggregationQuery{Period: period}, "", now, func(bucket runtimeusage.UsageBucket) error {
			calls += bucket.Calls
			return nil
		})
		if err != nil || calls != want {
			t.Fatalf("retained calls=%d want=%d: %v", calls, want, err)
		}
	}
	check(0)
	maintenance, stop := context.WithTimeout(ctx, 2*time.Second)
	done := make(chan error, 1)
	go func() { done <- store.MaintainExpired(maintenance, now) }()
	var slowest, slowestAudit time.Duration
	for n := range 20 {
		budget, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		began := time.Now()
		err := store.RecordUsage(budget, runtimeusage.Observation{ExchangeID: fmt.Sprintf("fresh-%d", n), StartedAt: now, OccurredAt: now, Status: activity.StatusSucceeded})
		cancel()
		slowest = max(slowest, time.Since(began))
		if err != nil {
			t.Errorf("new terminal usage blocked by retention maintenance: %v", err)
			break
		}
		attempt := providerAttempt(t, fmt.Sprintf("concurrent-egress-%d", n))
		terminal, err := attempt.Finish(egressaudit.TerminalInput{
			Outcome: egressaudit.OutcomeCompleted, BytesOut: 128, BytesIn: 4096, CompletedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		auditBudget, auditCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		began = time.Now()
		if _, err = store.EgressAttemptRepository().Append(auditBudget, attempt); err == nil {
			_, err = store.EgressAttemptRepository().Complete(auditBudget, terminal)
		}
		auditCancel()
		slowestAudit = max(slowestAudit, time.Since(began))
		if err != nil {
			t.Errorf("core audit blocked by retention maintenance: %v", err)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	t.Logf("slowest terminal usage write during retention cleanup: %v", slowest)
	t.Logf("slowest core audit append + complete during retention cleanup: %v", slowestAudit)
	checkAudit := func() {
		t.Helper()
		page, err := store.EgressAttemptRepository().List(ctx, egressaudit.PageRequest{Limit: 50})
		if err != nil || len(page.Items) != 20 {
			t.Fatalf("durable core attempts = %d, want 20: %v", len(page.Items), err)
		}
		for _, record := range page.Items {
			if !record.Attempt.Terminal() || record.Attempt.Outcome() != egressaudit.OutcomeCompleted ||
				record.Attempt.BytesOut() != 128 || record.Attempt.BytesIn() != 4096 {
				t.Fatalf("core audit lost its completion: %+v", record.Attempt)
			}
		}
	}
	checkAudit()
	check(20)
	shutdownTestStore(t, store)
	store = openDisabledUsageTestStore(t, path)
	check(20)
	checkAudit()
	if err := store.MaintainExpired(ctx, now); err != nil {
		t.Fatal(err)
	}
	check(20)
	checkAudit()
}

func TestUsageRetentionChangesPreserveEachObservationDeadline(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")
	store := openDisabledUsageTestStore(t, path)
	defer func() { shutdownTestStore(t, store) }()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	policy, err := store.UsagePolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		day, days int
		model     string
	}{
		{0, 30, "long"},   // Later shortened to day 7.
		{2, 7, "short"},   // Expires on day 9 even after a later extension.
		{4, 30, "middle"}, // Later shortened to day 18, not day 11.
		{6, 14, ""},
		{6, 90, "new"}, // Not covered by older restrictions; expires on day 96.
	} {
		at := start.AddDate(0, 0, step.day)
		policy.Enabled, policy.RetentionDays = true, step.days
		policy, err = store.SetUsagePolicy(ctx, policy, at)
		if err != nil {
			t.Fatal(err)
		}
		if step.model != "" {
			if err := store.RecordUsage(ctx, runtimeusage.Observation{ExchangeID: step.model, UpstreamModel: step.model, StartedAt: at, OccurredAt: at, Status: activity.StatusSucceeded}); err != nil {
				t.Fatal(err)
			}
		}
	}
	period, _ := runtimeusage.NewQuery("2026-09-01", "2026-10-01", "UTC")
	check := func(day int, want []string) {
		t.Helper()
		result, err := store.ScanUsage(ctx, runtimeusage.AggregationQuery{Period: period, Dimension: "model", Limit: 50}, "", start.AddDate(0, 0, day), func(runtimeusage.UsageBucket) error { return nil })
		var got []string
		for _, group := range result.Groups {
			got = append(got, group.ID)
		}
		slices.Sort(got)
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("retained models on day %d: %v want %v, %v", day, got, want, err)
		}
	}
	check(7, []string{"middle", "new", "short"})
	check(9, []string{"middle", "new"})
	check(18, []string{"new"})
	shutdownTestStore(t, store)
	store = openDisabledUsageTestStore(t, path)
	if err := store.MaintainExpired(ctx, start.AddDate(0, 0, 7)); err != nil {
		t.Fatal(err)
	}
	check(7, []string{"middle", "new", "short"})
	check(9, []string{"middle", "new"})
	check(18, []string{"new"})
	if _, err := store.CleanupExpired(ctx, start.AddDate(0, 0, 18)); err != nil {
		t.Fatal(err)
	}
	check(18, []string{"new"})
}
