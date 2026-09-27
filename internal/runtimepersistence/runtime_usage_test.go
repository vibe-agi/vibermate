package runtimepersistence

import (
	"context"
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

func TestUsageLedgerConsentDeduplicationOwnershipAndRetention(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"))
	t.Cleanup(func() { _ = store.Shutdown(ctx) })
	seedCaptureGraph(t, store, "managed_run", "local", 1)
	seedCaptureGraph(t, store, "managed_run", "member", 1)
	seedCaptureGraph(t, store, "manual_capture", "manual", 1)
	git, err := json.Marshal(capturerun.GitSnapshot{RepositoryKey: strings.Repeat("a", 64), RepositoryName: "project", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(ctx, `INSERT INTO capture_run_projects VALUES('local',?); UPDATE capture_runs SET local_user_label='local-alice' WHERE run_id='local'`, string(git)); err != nil {
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
	if _, err := store.database.ExecContext(ctx, `UPDATE capture_run_projects SET git_json=json_set(git_json,'$.branch','later') WHERE run_id='local'; UPDATE capture_runs SET local_user_label='later-user' WHERE run_id='local'`); err != nil {
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
