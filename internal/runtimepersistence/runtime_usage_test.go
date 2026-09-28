package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

// Test-only evidence inspection. Reports use numeric, complete ScanUsage queries.
func (store *Store) ListUsage(ctx context.Context, query runtimeusage.Query, userID runtimeuser.UserID, now time.Time, limit int) ([]runtimeusage.Observation, bool, error) {
	from, until := query.Bounds()
	if from.IsZero() || !until.After(from) || now.IsZero() || limit < 1 || limit > 100000 || (userID != "" && !userID.Valid()) {
		return nil, false, errors.New("invalid usage query")
	}
	operation, finish, err := store.operations.begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer finish()
	statement := `SELECT observation_json FROM runtime_usage_observations WHERE occurred_at_unix_ms>=? AND occurred_at_unix_ms<? AND expires_at_unix_ms>?`
	args := []any{from.UnixMilli(), until.UnixMilli(), now.UnixMilli()}
	// Filter ownership before limiting, so members cannot lose their history to
	// another member's traffic or learn anything about that traffic's volume.
	if userID != "" {
		statement += ` AND runtime_user_id=?`
		args = append(args, userID)
	}
	statement += ` ORDER BY occurred_at_unix_ms DESC,exchange_id LIMIT ?`
	args = append(args, limit+1)
	rows, err := store.reads.QueryContext(operation, statement, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := []runtimeusage.Observation{}
	for rows.Next() {
		if len(result) == limit {
			return result, true, nil
		}
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, false, err
		}
		var value runtimeusage.Observation
		if err := json.Unmarshal([]byte(data), &value); err != nil {
			return nil, false, err
		}
		if err := value.Validate(); err != nil {
			return nil, false, err
		}
		result = append(result, value)
	}
	return result, false, rows.Err()
}

func TestUsageReadersDoNotBlockTheWriterAndCannotWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"))
	defer shutdownTestStore(t, store)
	var version string
	if err := store.reads.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("read/write isolation SQLite version: %s", version)
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	query, err := runtimeusage.NewQuery("2026-09-28", "2026-09-29", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	writer, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, _, err := store.ListUsage(ctx, query, "", now, 1); err != nil {
		t.Fatalf("usage reader borrowed the held writer connection: %v", err)
	}
	if err := writer.Rollback(); err != nil {
		t.Fatal(err)
	}
	reader, err := store.reads.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	before, err := scanUsagePolicy(reader.QueryRowContext(ctx, `SELECT enabled,retention_days,revision,collecting_since_unix_ms FROM runtime_usage_policy`))
	if err != nil {
		t.Fatal(err)
	}
	before.Enabled = true
	started := time.Now()
	updated, err := store.SetUsagePolicy(ctx, before, now)
	if err != nil {
		t.Fatalf("held read snapshot blocked writer: %v", err)
	}
	t.Logf("policy write while report snapshot remains open: %v", time.Since(started))
	var frozen int64
	if err := reader.QueryRowContext(ctx, `SELECT revision FROM runtime_usage_policy`).Scan(&frozen); err != nil || frozen != before.Revision || updated.Revision != before.Revision+1 {
		t.Fatalf("read snapshot was not isolated: frozen=%d updated=%d err=%v", frozen, updated.Revision, err)
	}
	if _, err := store.reads.ExecContext(ctx, `UPDATE runtime_usage_policy SET enabled=0`); err == nil {
		t.Fatal("report connection can mutate storage")
	}
	if current, err := store.UsagePolicy(ctx); err != nil || !current.Enabled || current.Revision != updated.Revision {
		t.Fatalf("fresh report did not see committed policy: %+v %v", current, err)
	}
}

func TestUsageReadFiltersExpiryWithoutDeleting(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"))
	defer shutdownTestStore(t, store)
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	if _, err := store.SetUsagePolicy(ctx, runtimeusage.CollectionPolicy{Enabled: true, RetentionDays: 1, Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUsage(ctx, runtimeusage.Observation{ExchangeID: "expired", StartedAt: now, OccurredAt: now, Status: activity.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	query, _ := runtimeusage.NewQuery("2026-09-28", "2026-09-29", "UTC")
	if values, _, err := store.ListUsage(ctx, query, "", now.Add(24*time.Hour), 10); err != nil || len(values) != 0 {
		t.Fatalf("expired data became visible: %+v %v", values, err)
	}
	var count int
	if err := store.database.QueryRowContext(ctx, `SELECT count(*) FROM runtime_usage_observations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("read mutated the ledger: count=%d err=%v", count, err)
	}
	if _, err := store.CleanupExpired(ctx, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.database.QueryRowContext(ctx, `SELECT count(*) FROM runtime_usage_observations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("maintenance did not remove expired usage: count=%d err=%v", count, err)
	}
}

func TestExpiredMaintenanceCommitsBoundedProgressAndPreservesLiveUsage(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"))
	defer shutdownTestStore(t, store)
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"a", "b", "c", "d", "e", "live"} {
		expires := now
		if id == "live" {
			expires = now.Add(time.Hour)
		}
		insertUsageFixture(t, store, runtimeusage.Observation{
			ExchangeID: id, OccurredAt: now.Add(-time.Hour), Status: activity.StatusSucceeded,
		}, expires)
	}
	if _, more, err := store.cleanupExpired(ctx, now, 2); err != nil || !more {
		t.Fatalf("bounded cleanup: more=%v error=%v", more, err)
	}
	count := func(want int) {
		t.Helper()
		var got int
		if err := store.database.QueryRow(`SELECT COUNT(*) FROM runtime_usage_observations`).Scan(&got); err != nil || got != want {
			t.Fatalf("remaining=%d want=%d error=%v", got, want, err)
		}
	}
	count(4)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.MaintainExpired(canceled, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled maintenance: %v", err)
	}
	count(4) // The earlier batch stays committed; cancellation cannot restart the backlog.
	if err := store.MaintainExpired(ctx, now); err != nil {
		t.Fatal(err)
	}
	count(1)
	var remaining string
	if err := store.database.QueryRow(`SELECT exchange_id FROM runtime_usage_observations`).Scan(&remaining); err != nil || remaining != "live" {
		t.Fatalf("live usage was changed: id=%q error=%v", remaining, err)
	}
}

func TestUsageLedgerConsentDeduplicationOwnershipAndRetention(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"))
	t.Cleanup(func() { _ = store.Shutdown(ctx) })
	seedCaptureGraph(t, store, "managed_run", "local", 1)
	seedCaptureGraph(t, store, "managed_run", "member", 1)
	seedCaptureGraph(t, store, "manual_capture", "manual", 1)
	git, err := json.Marshal(capturerun.GitSnapshot{RepositorySource: "local", RepositoryKey: strings.Repeat("a", 64), RepositoryName: "project", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(ctx, `UPDATE capture_runs SET git_json=?,local_user_label='local-alice' WHERE run_id='local'`, string(git)); err != nil {
		t.Fatal(err)
	}
	user := runtimeuser.UserID("user.AAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if _, err := store.database.ExecContext(ctx, `INSERT INTO runtime_users VALUES(?, 'alice', ?, 'active', 1, 2)`, user, strings.Repeat("x", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(ctx, `INSERT INTO runtime_user_login_sessions VALUES('login.test',?,randomblob(32),'machine','device',1,2,NULL)`, user); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(ctx, `UPDATE capture_runs SET runtime_user_id=?,runtime_username='alice',login_session_id='login.test',device_name='device' WHERE run_id='member'`, user); err != nil {
		t.Fatal(err)
	}
	query, err := runtimeusage.NewQuery("2026-09-01", "2026-11-01", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	value := runtimeusage.Observation{ExchangeID: "one", CaptureRunID: "local", StartedAt: now, OccurredAt: now, Status: activity.StatusSucceeded, RequestedModel: "model", UpstreamModel: "model", Usage: protocolcore.Usage{Output: protocolcore.UsageValue{Tokens: 0, Known: true, Source: "provider"}}}
	if err := store.RecordUsage(ctx, value); err != nil {
		t.Fatal(err)
	}
	rows, _, err := store.ListUsage(ctx, query, "", now, 100)
	if err != nil || len(rows) != 0 {
		t.Fatalf("disabled: %v %v", rows, err)
	}
	policy, err := store.SetUsagePolicy(ctx, runtimeusage.CollectionPolicy{Enabled: true, RetentionDays: 30, Revision: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetUsagePolicy(ctx, runtimeusage.CollectionPolicy{Enabled: true, RetentionDays: 30, Revision: 1}, now); !errors.Is(err, runtimeusage.ErrPolicyConflict) {
		t.Fatalf("stale policy: %v", err)
	}
	// Supplying a user ID never attributes an unowned Capture to that user.
	value.UserID = user
	for range 2 {
		if err := store.RecordUsage(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.database.ExecContext(ctx, `UPDATE capture_runs SET git_json=json_set(git_json,'$.branch','later'),local_user_label='later-user' WHERE run_id='local'`); err != nil {
		t.Fatal(err)
	}
	value.ExchangeID, value.CaptureRunID = "two", "member"
	value.UserID = ""
	value.Usage = protocolcore.Usage{}
	if err := store.RecordUsage(ctx, value); err != nil {
		t.Fatal(err)
	}
	value.ExchangeID, value.CaptureRunID, value.ManualCaptureID = "three", "", "manual"
	if err := store.RecordUsage(ctx, value); err != nil {
		t.Fatal(err)
	}
	rows, _, err = store.ListUsage(ctx, query, "", now, 100)
	if err != nil || len(rows) != 3 {
		t.Fatalf("one observation per Exchange: %d %v", len(rows), err)
	}
	bySource := map[string]runtimeusage.Observation{}
	for _, row := range rows {
		bySource[row.Source] = row
	}
	if bySource["local"].UserID != "" || bySource["manual"].UserID != "" || bySource["member"].UserID != user {
		t.Fatalf("ownership: %#v", bySource)
	}
	if got := bySource["local"].Attribution; got == nil || got.CallerLabel != "local-alice" || got.CallerKind != "local" || got.GitAtLaunch == nil || got.GitAtLaunch.Branch != "main" {
		t.Fatalf("immutable usage attribution = %#v", got)
	}
	if got := bySource["member"].Attribution; got == nil || got.CallerLabel != "alice" || got.CallerKind != "member" || got.CallerID != string(user) {
		t.Fatalf("authenticated caller = %#v", got)
	}
	if !bySource["local"].Usage.Output.Known || bySource["member"].Usage.Output.Known {
		t.Fatal("unknown token usage became zero")
	}
	rows, truncated, err := store.ListUsage(ctx, query, user, now, 1)
	if err != nil || truncated || len(rows) != 1 || rows[0].Source != "member" {
		t.Fatalf("isolation before limit: %#v %v %v", rows, truncated, err)
	}
	// Expiring or purging body evidence does not clear the independent ledger.
	if _, err := store.CleanupExpired(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows, _, err = store.ListUsage(ctx, query, "", now.Add(time.Hour), 100)
	if err != nil || len(rows) != 3 {
		t.Fatalf("body cleanup changed usage: %d %v", len(rows), err)
	}
	policy.Enabled = false
	policy, err = store.SetUsagePolicy(ctx, policy, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	value.ExchangeID = "disabled"
	if err := store.RecordUsage(ctx, value); err != nil {
		t.Fatal(err)
	}
	policy.Enabled = true
	policy, err = store.SetUsagePolicy(ctx, policy, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUsage(ctx, value); err != nil {
		t.Fatal(err)
	} // began before re-enabling
	rows, _, err = store.ListUsage(ctx, query, "", now.Add(3*time.Hour), 100)
	if err != nil || len(rows) != 3 {
		t.Fatalf("consent interval: %d %v", len(rows), err)
	}
	// Deleting the Capture removes its usage, and a late terminal callback cannot recreate it.
	if _, err := store.DeleteCapture(ctx, "manual_capture", "manual"); err != nil {
		t.Fatal(err)
	}
	value.StartedAt = now.Add(3 * time.Hour)
	value.OccurredAt = value.StartedAt
	if err := store.RecordUsage(ctx, value); err != nil {
		t.Fatal(err)
	}
	rows, _, err = store.ListUsage(ctx, query, "", now.Add(3*time.Hour), 100)
	if err != nil || len(rows) != 2 {
		t.Fatalf("capture deletion: %d %v", len(rows), err)
	}
	policy.RetentionDays = 7
	if _, err := store.SetUsagePolicy(ctx, policy, now.AddDate(0, 0, 8)); err != nil {
		t.Fatal(err)
	}
	rows, _, err = store.ListUsage(ctx, query, "", now.AddDate(0, 0, 8), 100)
	if err != nil || len(rows) != 0 {
		t.Fatalf("shorter retention: %d %v", len(rows), err)
	}
}

func TestUsageArchiveClearAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")
	store := openTestStore(t, path)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	if _, err := store.SetUsagePolicy(ctx, runtimeusage.CollectionPolicy{Enabled: true, RetentionDays: 7, Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	value := runtimeusage.Observation{ExchangeID: "proxy", StartedAt: now, OccurredAt: now, Status: activity.StatusFailed}
	if err := store.RecordUsage(ctx, value); err != nil {
		t.Fatal(err)
	}
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	store = openTestStore(t, path)
	t.Cleanup(func() { _ = store.Shutdown(ctx) })
	query, _ := runtimeusage.NewQuery("2026-09-27", "2026-10-10", "UTC")
	rows, _, err := store.ListUsage(ctx, query, "", now, 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("reopen: %d %v", len(rows), err)
	}
	if _, err := store.ClearEvidence(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUsage(ctx, value); err != nil {
		t.Fatal(err)
	}
	rows, _, err = store.ListUsage(ctx, query, "", now, 100)
	if err != nil || len(rows) != 0 {
		t.Fatalf("clear: %d %v", len(rows), err)
	}
	policy, err := store.UsagePolicy(ctx)
	if err != nil || !policy.Enabled {
		t.Fatalf("clearing usage changed consent: %#v %v", policy, err)
	}
}
