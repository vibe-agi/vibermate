package runtimepersistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/modelcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

func insertUsageFixture(t testing.TB, store *Store, value runtimeusage.Observation, expires time.Time) {
	t.Helper()
	value.StartedAt = value.OccurredAt
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.database.Exec(`INSERT INTO runtime_usage_observations(exchange_id,capture_run_id,manual_capture_id,runtime_user_id,occurred_at_unix_ms,expires_at_unix_ms,observation_json) VALUES(?,?,?,?,?,?,?)`,
		value.ExchangeID, value.CaptureRunID, value.ManualCaptureID, value.UserID, value.OccurredAt.UnixMilli(), expires.UnixMilli(), string(data))
	if err != nil {
		t.Fatal(err)
	}
}

func TestUsageAggregationScopesWindowsMissingValuesAndFrozenLabels(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"))
	defer shutdownTestStore(t, store)
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	user := runtimeuser.UserID("user.AAAAAAAAAAAAAAAAAAAAAAAAAAA")
	known := func(tokens int64) protocolcore.UsageValue {
		return protocolcore.UsageValue{Known: true, Tokens: tokens, Source: "fixture"}
	}
	base := runtimeusage.Observation{Source: "member", UserID: user, OccurredAt: now, Status: activity.StatusSucceeded,
		EnvironmentID: "p", EnvironmentName: "frozen profile", UpstreamModel: "m", Client: "codex", SessionID: "same",
		Usage: protocolcore.Usage{InputUncached: known(10), Output: known(0)}, Attribution: &runtimeusage.Attribution{
			CallerID: string(user), CallerLabel: "Alice", CallerKind: "member", ProjectID: "remote:project",
			GitAtLaunch: &capturerun.GitSnapshot{RepositorySource: "remote", RepositoryKey: strings.Repeat("a", 64), RepositoryName: "github.com/org/project", Branch: "main"},
		}}
	for _, edit := range []func(*runtimeusage.Observation){
		func(*runtimeusage.Observation) {},
		func(v *runtimeusage.Observation) {
			v.Status = activity.StatusFailed
			v.Usage = protocolcore.Usage{}
			v.UpstreamModel = ""
		},
		func(v *runtimeusage.Observation) { v.Client = "claude"; v.UpstreamModel = "unknown" },
		func(v *runtimeusage.Observation) { v.OccurredAt = now.Add(-time.Hour) },
		func(v *runtimeusage.Observation) { v.UserID = ""; v.Source = "local" },
	} {
		v := base
		edit(&v)
		v.ExchangeID = fmt.Sprint(v.Client, v.UpstreamModel, v.OccurredAt.UnixMilli(), v.UserID)
		insertUsageFixture(t, store, v, now.Add(48*time.Hour))
	}
	query, _ := runtimeusage.NewQuery("2026-09-28", "2026-09-29", "Asia/Kathmandu")
	request := runtimeusage.AggregationQuery{Period: query}
	var buckets []runtimeusage.UsageBucket
	result, err := store.ScanUsage(ctx, request, user, now, func(b runtimeusage.UsageBucket) error { buckets = append(buckets, b); return nil })
	if err != nil {
		t.Fatal(err)
	}
	calls, knownZero, unknown := 0, 0, 0
	for _, b := range buckets {
		calls += b.Calls
		if b.Day != "2026-09-28" {
			t.Fatalf("wrong local day: %+v", b)
		}
		if b.Usage.Output.Known && b.Usage.Output.Tokens == 0 {
			knownZero += b.Calls
		}
		if !b.Usage.Output.Known {
			unknown += b.Calls
		}
	}
	if calls != 4 || knownZero != 3 || unknown != 1 {
		t.Fatalf("calls=%d zero=%d unknown=%d", calls, knownZero, unknown)
	}
	request.KnownSnapshot = result.Snapshot
	conditional, err := store.ScanUsage(ctx, request, user, now, func(runtimeusage.UsageBucket) error { t.Fatal("unchanged report rescanned buckets"); return nil })
	if err != nil || !conditional.Unchanged {
		t.Fatalf("conditional snapshot: %v", err)
	}
	request.KnownSnapshot = ""
	request.Dimension, request.Limit, request.Snapshot = "model", 50, result.Snapshot
	page, err := store.ScanUsage(ctx, request, user, now, func(runtimeusage.UsageBucket) error { return nil })
	if err != nil || len(page.Groups) != 3 || page.Groups[0].ID != "m" || page.Groups[1].ID != "" || page.Groups[2].ID != "unknown" {
		t.Fatalf("model identities: %+v %v", page, err)
	}
	request.Dimension = "project"
	page, err = store.ScanUsage(ctx, request, user, now, func(runtimeusage.UsageBucket) error { return nil })
	if err != nil || len(page.Groups) != 1 || page.Groups[0].Label != "github.com/org/project" || page.Groups[0].Evidence != "remote" {
		t.Fatalf("frozen project: %+v %v", page, err)
	}
	request.Dimension = "profile"
	request.Filters = []runtimeusage.Filter{{Dimension: "profile", ID: "p"}}
	page, err = store.ScanUsage(ctx, request, user, now, func(runtimeusage.UsageBucket) error { return nil })
	if err != nil || len(page.Groups) != 1 || page.Groups[0].ID != "p" || page.Groups[0].Label != "frozen profile" || page.Groups[0].AgentAPICalls != 4 {
		t.Fatalf("environment group and filter: %+v %v", page, err)
	}
	request.Dimension = "caller"
	request.Filters = []runtimeusage.Filter{{Dimension: "project", ID: "remote:project"}, {Dimension: "client", ID: "codex"}, {Dimension: "session", ID: "same"}}
	page, err = store.ScanUsage(ctx, request, user, now, func(runtimeusage.UsageBucket) error { return nil })
	if err != nil || len(page.Groups) != 1 || page.Groups[0].AgentAPICalls != 3 || page.Groups[0].Label != "Alice" {
		t.Fatalf("project to caller / client-scoped session: %+v %v", page, err)
	}
	request.Filters = []runtimeusage.Filter{{Dimension: "model", ID: "' OR 1=1 --"}}
	page, err = store.ScanUsage(ctx, request, user, now, func(runtimeusage.UsageBucket) error { t.Fatal("filter was injected"); return nil })
	if err != nil || len(page.Groups) != 0 {
		t.Fatalf("bound filter: %+v %v", page, err)
	}
}

func TestUsagePaginationSnapshotInvalidationAndConcurrentWrite(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"))
	defer shutdownTestStore(t, store)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	user := runtimeuser.UserID("user.AAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if _, err := store.SetUsagePolicy(ctx, runtimeusage.CollectionPolicy{Enabled: true, RetentionDays: 90, Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	for index := range 7 {
		insertUsageFixture(t, store, runtimeusage.Observation{ExchangeID: fmt.Sprint(index), UserID: user, OccurredAt: now, Status: activity.StatusSucceeded, UpstreamModel: fmt.Sprintf("model-%d", index/2)}, now.Add(time.Hour))
	}
	period, _ := runtimeusage.NewQuery("2026-09-28", "2026-09-29", "UTC")
	query := runtimeusage.AggregationQuery{Period: period, Dimension: "model", Limit: 2, PricingRevision: "prices-a"}
	noop := func(runtimeusage.UsageBucket) error { return nil }
	first, err := store.ScanUsage(ctx, query, user, now, noop)
	if err != nil || first.NextCursor == "" || len(first.Groups) != 2 {
		t.Fatalf("first page: %+v %v", first, err)
	}
	query.Cursor = first.NextCursor
	second, err := store.ScanUsage(ctx, query, user, now, noop)
	if err != nil || second.NextCursor != "" || len(second.Groups) != 2 || second.Groups[0].ID != "model-2" || second.Groups[1].AgentAPICalls != 1 {
		t.Fatalf("second page: %+v %v", second, err)
	}
	wrongFilter := query
	wrongFilter.Filters = []runtimeusage.Filter{{Dimension: "source", ID: "member"}}
	if _, err := store.ScanUsage(ctx, wrongFilter, user, now, noop); !errors.Is(err, runtimeusage.ErrInvalidQuery) {
		t.Fatalf("reused cursor for other filter: %v", err)
	}
	wrongPrice := query
	wrongPrice.PricingRevision = "prices-b"
	if _, err := store.ScanUsage(ctx, wrongPrice, user, now, noop); !errors.Is(err, runtimeusage.ErrSnapshotChanged) {
		t.Fatalf("stale pricing cursor: %v", err)
	}
	// Write a different user's observation while the report read snapshot stays
	// open. It neither blocks the writer nor changes this member's cursor.
	written := false
	_, err = store.ScanUsage(ctx, query, user, now, func(runtimeusage.UsageBucket) error {
		if !written {
			written = true
			return store.RecordUsage(ctx, runtimeusage.Observation{ExchangeID: "other-user", StartedAt: now, OccurredAt: now, Status: activity.StatusSucceeded})
		}
		return nil
	})
	if err != nil || !written {
		t.Fatalf("concurrent terminal write: %v", err)
	}
	unchanged, err := store.ScanUsage(ctx, query, user, now, noop)
	if err != nil || !reflect.DeepEqual(second, unchanged) {
		t.Fatalf("unrelated activity leaked into member snapshot: %v", err)
	}
	if _, err := store.database.Exec(`DELETE FROM runtime_usage_observations WHERE exchange_id='0'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ScanUsage(ctx, query, user, now, noop); !errors.Is(err, runtimeusage.ErrSnapshotChanged) {
		t.Fatalf("deletion did not invalidate: %v", err)
	}
	query.Cursor, query.Snapshot = "", ""
	fresh, err := store.ScanUsage(ctx, query, user, now, noop)
	if err != nil {
		t.Fatal(err)
	}
	query.Snapshot = fresh.Snapshot
	if _, err := store.ScanUsage(ctx, query, user, now.Add(time.Hour), noop); !errors.Is(err, runtimeusage.ErrSnapshotChanged) {
		t.Fatalf("expiry did not invalidate: %v", err)
	}
	query.Snapshot = ""
	wantErr := errors.New("visitor stopped")
	if _, err := store.ScanUsage(ctx, query, user, now, func(runtimeusage.UsageBucket) error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("lost visitor error: %v", err)
	}
	if _, err := store.ScanUsage(ctx, query, user, now, noop); err != nil {
		t.Fatalf("read connection leaked on error: %v", err)
	}
	cancel()
	if _, err := store.ScanUsage(ctx, query, user, now, noop); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func seedBulkUsage(t testing.TB, store *Store, count, groups int, now time.Time) {
	t.Helper()
	value := runtimeusage.Observation{ExchangeID: "bulk", Source: "local", StartedAt: now, OccurredAt: now, Status: activity.StatusSucceeded,
		UpstreamModel: "m", EnvironmentID: "profile", Usage: protocolcore.Usage{Output: protocolcore.UsageValue{Known: true, Tokens: 3, Source: "fixture"}},
		Attribution: &runtimeusage.Attribution{CallerID: "alice", CallerLabel: "Alice", CallerKind: "local"}}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.database.Exec(`WITH RECURSIVE series(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM series WHERE n<?)
 INSERT INTO runtime_usage_observations(exchange_id,capture_run_id,manual_capture_id,runtime_user_id,occurred_at_unix_ms,expires_at_unix_ms,observation_json)
 SELECT 'bulk-'||n,'','','',?,?,json_set(?,'$.exchangeId','bulk-'||n,'$.accountId','account-'||(n%?),'$.accountName',?||(n%?)) FROM series`,
		count, now.UnixMilli(), now.Add(24*time.Hour).UnixMilli(), string(data), groups, strings.Repeat("long label ", 90), groups)
	if err != nil {
		t.Fatal(err)
	}
}

func TestUsageAggregationBeyond100000IsCompleteAndPageSizeIsBounded(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"))
	defer shutdownTestStore(t, store)
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	seedBulkUsage(t, store, 100001, 30000, now)
	period, _ := runtimeusage.NewQuery("2026-09-28", "2026-09-29", "UTC")
	ctx := context.Background()
	request := runtimeusage.AggregationQuery{Period: period}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	calls, tokens, buckets := 0, int64(0), 0
	_, err := store.ScanUsage(ctx, request, "", now, func(b runtimeusage.UsageBucket) error {
		calls += b.Calls
		tokens += b.Usage.Output.Tokens * int64(b.Calls)
		buckets++
		return nil
	})
	runtime.ReadMemStats(&after)
	t.Logf("100001 observations: %v, %.2f MiB allocations, %d numeric buckets", time.Since(started), float64(after.TotalAlloc-before.TotalAlloc)/(1<<20), buckets)
	if err != nil || calls != 100001 || tokens != 300003 || buckets != 1 {
		t.Fatalf("truncated summary: calls=%d tokens=%d buckets=%d err=%v", calls, tokens, buckets, err)
	}
	request.Dimension, request.Limit = "account", 50
	pageCalls := 0
	started = time.Now()
	page, err := store.ScanUsage(ctx, request, "", now, func(b runtimeusage.UsageBucket) error { pageCalls += b.Calls; return nil })
	if err != nil || len(page.Groups) != 50 || page.NextCursor == "" || pageCalls != 200 {
		t.Fatalf("page: count=%d calls=%d error=%v", len(page.Groups), pageCalls, err)
	}
	data, err := json.Marshal(page)
	if err != nil || len(data) > 100000 {
		t.Fatalf("unbounded page bytes=%d error=%v", len(data), err)
	}
	t.Logf("30000 long-label groups: first 50 in %v, %d response bytes", time.Since(started), len(data))
}

// Explicit scale gate: the regular regression above stays small; this fixture
// exercises a million observations with 100003 distinct token combinations.
func TestUsageMillionObservationPricedProjection(t *testing.T) {
	if os.Getenv("VIBERMATE_USAGE_SCALE") != "1" {
		t.Skip("set VIBERMATE_USAGE_SCALE=1 for the isolated million-row check")
	}
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"))
	defer shutdownTestStore(t, store)
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()
	const count, variants = 1_000_001, 100003
	value := runtimeusage.Observation{ExchangeID: "bulk", Source: "local", StartedAt: now, OccurredAt: now,
		Status: activity.StatusSucceeded, UpstreamModel: "m", Usage: protocolcore.Usage{
			InputUncached: protocolcore.UsageValue{Known: true, Tokens: 0, Source: "fixture"},
			Output:        protocolcore.UsageValue{Known: true, Tokens: 3, Source: "fixture"}}}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.database.Exec(`WITH RECURSIVE series(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM series WHERE n<?)
 INSERT INTO runtime_usage_observations(exchange_id,capture_run_id,manual_capture_id,runtime_user_id,occurred_at_unix_ms,expires_at_unix_ms,observation_json)
 SELECT 'bulk-'||n,'','','',?,?,json_set(?,'$.exchangeId','bulk-'||n,'$.usage.Output.Tokens',3+n%?,'$.accountId','account-'||(n%30000)) FROM series`,
		count, now.UnixMilli(), now.Add(24*time.Hour).UnixMilli(), string(data), variants)
	if err != nil {
		t.Fatal(err)
	}
	clock := runtimeUserTestClock{now: now}
	prices := &modelcatalog.ReferencePrices{Clock: clock, Fetch: func(context.Context) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"openai":{"models":{"m":{"cost":{"input":1,"output":0.000013}}}}}`))}, nil
	}}
	projector, err := runtimeusage.New(runtimeusage.Options{Ledger: store, Clock: clock, Prices: prices})
	if err != nil {
		t.Fatal(err)
	}
	period, _ := runtimeusage.NewQuery("2026-09-28", "2026-09-29", "UTC")
	var wantTokens, wantCost int64
	for n := 1; n <= count; n++ {
		tokens := int64(3 + n%variants)
		wantTokens += tokens
		wantCost += tokens * 13 / 1000 // one request floor, not floor of the grand total
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	report, err := projector.Report(ctx, runtimeusage.AggregationQuery{Period: period})
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total.AgentAPICalls != count || report.Total.Tokens.Output.Tokens != wantTokens || report.Total.Cost.NanoUSD != wantCost {
		t.Fatalf("inexact complete projection: %+v want tokens=%d cost=%d", report.Total, wantTokens, wantCost)
	}
	wire, err := json.Marshal(report)
	if err != nil || len(wire) > 10000 {
		t.Fatalf("summary bytes=%d error=%v", len(wire), err)
	}
	t.Logf("million-row priced summary: %v, %.2f MiB Go allocation, %d bytes, exact per-request cost=%d nano-USD", time.Since(started), float64(after.TotalAlloc-before.TotalAlloc)/(1<<20), len(wire), wantCost)
	started = time.Now()
	runtime.ReadMemStats(&before)
	warm, err := projector.Report(ctx, runtimeusage.AggregationQuery{Period: period})
	runtime.ReadMemStats(&after)
	if err != nil || !reflect.DeepEqual(warm, report) {
		t.Fatalf("warm summary changed: %v", err)
	}
	t.Logf("million-row revalidated summary: %v, %.2f MiB Go allocation", time.Since(started), float64(after.TotalAlloc-before.TotalAlloc)/(1<<20))
	started = time.Now()
	page, err := projector.Report(ctx, runtimeusage.AggregationQuery{Period: period, Dimension: "account", Limit: 50, Snapshot: report.Snapshot})
	if err != nil || len(page.Groups) != 50 || page.NextCursor == "" {
		t.Fatalf("page rows=%d error=%v", len(page.Groups), err)
	}
	t.Logf("million-row account page: %v", time.Since(started))
	started = time.Now()
	if _, more, err := store.cleanupExpired(ctx, now.Add(48*time.Hour), expiredCleanupBatchSize); err != nil || !more {
		t.Fatalf("first maintenance batch: more=%v error=%v", more, err)
	}
	t.Logf("million-row expired usage first %d-row batch: %v", expiredCleanupBatchSize, time.Since(started))
	var remaining int
	if err := store.database.QueryRow(`SELECT COUNT(*) FROM runtime_usage_observations`).Scan(&remaining); err != nil || remaining != count-expiredCleanupBatchSize {
		t.Fatalf("unbounded maintenance remaining=%d error=%v", remaining, err)
	}
	passes := 1
	for remaining > 0 {
		budget, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		err := store.MaintainExpired(budget, now.Add(48*time.Hour))
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		var next int
		if err := store.database.QueryRow(`SELECT COUNT(*) FROM runtime_usage_observations`).Scan(&next); err != nil || next >= remaining {
			t.Fatalf("maintenance lost committed progress: before=%d after=%d error=%v", remaining, next, err)
		}
		remaining = next
		passes++
	}
	if err := store.MaintainExpired(ctx, now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	t.Logf("million-row expired usage cleanup: %v across %d resumable budgets", time.Since(started), passes)
}
