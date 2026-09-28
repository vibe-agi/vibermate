package runtimeusage_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

func queryAround(t *testing.T, now time.Time) runtimeusage.AggregationQuery {
	t.Helper()
	period, err := runtimeusage.NewQuery(now.AddDate(0, 0, -30).Format(time.DateOnly), now.AddDate(0, 0, 1).Format(time.DateOnly), "UTC")
	if err != nil {
		t.Fatal(err)
	}
	return runtimeusage.AggregationQuery{Period: period}
}

func TestSummaryIncludesLocalManualAndUnknownWithoutEnumeratingUsersOrRuns(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	ledger := &observationLedger{items: []runtimeusage.Observation{
		{ExchangeID: "local", Source: "local", OccurredAt: now, Status: activity.StatusSucceeded, UpstreamModel: "unknown", Usage: protocolcore.Usage{Output: protocolcore.UsageValue{Known: true, Tokens: 0}}},
		{ExchangeID: "manual", Source: "manual", OccurredAt: now, Status: activity.StatusFailed},
	}}
	projector, err := runtimeusage.New(runtimeusage.Options{Ledger: ledger, Clock: fixedClock{now}})
	if err != nil {
		t.Fatal(err)
	}
	query := queryAround(t, now)
	report, err := projector.Report(context.Background(), query)
	if err != nil || report.Total == nil || report.Total.AgentAPICalls != 2 || report.Total.Succeeded != 1 || report.Total.Failed != 1 || len(report.Groups) != 0 || len(report.Days) != 1 || report.Days[0].ModelUnavailableCalls != 1 {
		t.Fatalf("summary: %+v %v", report, err)
	}
	if report.Total.Tokens.Output.KnownCalls != 1 || report.Total.Tokens.Output.UnknownCalls != 1 || report.Total.Tokens.Output.Tokens != 0 {
		t.Fatal("unknown became a zero")
	}
	query.Dimension, query.Limit = "model", 50
	page, err := projector.Report(context.Background(), query)
	if err != nil || page.Total != nil || len(page.Days) != 0 || len(page.Groups) != 2 || page.Groups[0].ID != "" || page.Groups[1].ID != "unknown" {
		t.Fatalf("bounded page: %+v %v", page, err)
	}
}

func TestSummaryDoesNotTruncateWeightedBucketsAndScopesPersonalReads(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	ledger := &observationLedger{read: func(_ runtimeusage.AggregationQuery, visit func(runtimeusage.UsageBucket) error) (runtimeusage.AggregationResult, error) {
		err := visit(runtimeusage.UsageBucket{Day: "2026-09-28", Status: activity.StatusSucceeded, Calls: 100001,
			Usage: protocolcore.Usage{Output: protocolcore.UsageValue{Known: true, Tokens: 3}}})
		return runtimeusage.AggregationResult{Snapshot: strings.Repeat("a", 64)}, err
	}}
	projector, err := runtimeusage.New(runtimeusage.Options{Ledger: ledger, Clock: fixedClock{now}})
	if err != nil {
		t.Fatal(err)
	}
	user := runtimeuser.UserID("user.AAAAAAAAAAAAAAAAAAAAAAAAAAA")
	report, err := projector.ReportForUser(context.Background(), queryAround(t, now), user)
	if err != nil || ledger.selected != user || report.Total.AgentAPICalls != 100001 || report.Total.Tokens.Output.Tokens != 300003 || report.Days[0].AgentAPICalls != 100001 {
		t.Fatalf("weighted scoped report: %+v %v", report, err)
	}
	if _, err := projector.ReportForUser(context.Background(), queryAround(t, now), ""); !errors.Is(err, runtimeusage.ErrInvalidQuery) {
		t.Fatalf("empty personal identity: %v", err)
	}
	if _, err := projector.Report(context.Background(), runtimeusage.AggregationQuery{}); !errors.Is(err, runtimeusage.ErrInvalidQuery) {
		t.Fatalf("invalid query: %v", err)
	}
	want := errors.New("read unavailable")
	ledger.read = func(runtimeusage.AggregationQuery, func(runtimeusage.UsageBucket) error) (runtimeusage.AggregationResult, error) {
		return runtimeusage.AggregationResult{}, want
	}
	if _, err := projector.Report(context.Background(), queryAround(t, now)); !errors.Is(err, want) {
		t.Fatal(err)
	}
}

func TestSummaryUsesRequestedCivilDays(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	ledger := &observationLedger{items: []runtimeusage.Observation{
		{OccurredAt: now.Add(-8*time.Hour - time.Millisecond), Status: activity.StatusSucceeded},
		{OccurredAt: now.Add(-8 * time.Hour), Status: activity.StatusFailed},
		{OccurredAt: now.Add(16*time.Hour - time.Millisecond), Status: activity.StatusCanceled},
		{OccurredAt: now.Add(16 * time.Hour), Status: activity.StatusSucceeded},
	}}
	projector, _ := runtimeusage.New(runtimeusage.Options{Ledger: ledger, Clock: fixedClock{now}})
	period, _ := runtimeusage.NewQuery("2026-09-28", "2026-09-29", "Asia/Singapore")
	report, err := projector.Report(context.Background(), runtimeusage.AggregationQuery{Period: period})
	if err != nil || report.Total.AgentAPICalls != 2 || report.Total.Failed != 1 || report.Total.Canceled != 1 || len(report.Days) != 1 || report.Days[0].Date != "2026-09-28" {
		t.Fatalf("window: %+v %v", report, err)
	}
}

func TestCompactReportCacheRevalidatesAndDoesNotExposeMutableResults(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	aggregations := 0
	version := strings.Repeat("a", 64)
	ledger := &observationLedger{read: func(query runtimeusage.AggregationQuery, visit func(runtimeusage.UsageBucket) error) (runtimeusage.AggregationResult, error) {
		result := runtimeusage.AggregationResult{Snapshot: version}
		if query.Snapshot != "" && query.Snapshot != version {
			return result, runtimeusage.ErrSnapshotChanged
		}
		if query.KnownSnapshot == version {
			result.Unchanged = true
			return result, nil
		}
		aggregations++
		return result, visit(runtimeusage.UsageBucket{Day: "2026-09-28", Status: activity.StatusSucceeded, Calls: 2})
	}}
	projector, _ := runtimeusage.New(runtimeusage.Options{Ledger: ledger, Clock: fixedClock{now}})
	query := queryAround(t, now)
	first, err := projector.Report(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	first.Total.AgentAPICalls = 999
	first.Days[0].AgentAPICalls = 999
	second, err := projector.Report(context.Background(), query)
	if err != nil || aggregations != 1 || second.Total.AgentAPICalls != 2 || second.Days[0].AgentAPICalls != 2 {
		t.Fatalf("cache alias or missed conditional: %+v %v count=%d", second.Total, err, aggregations)
	}
	// A different scope never reuses the first scope's numbers, even if a
	// repository happens to have equal version tokens in this test adapter.
	other := query
	other.Filters = []runtimeusage.Filter{{Dimension: "account", ID: "different"}}
	if _, err := projector.Report(context.Background(), other); err != nil || aggregations != 2 {
		t.Fatalf("scope cache leak: %v", err)
	}
	version = strings.Repeat("b", 64)
	query.Snapshot = second.Snapshot
	if _, err := projector.Report(context.Background(), query); !errors.Is(err, runtimeusage.ErrSnapshotChanged) {
		t.Fatal(err)
	}
	query.Snapshot = ""
	third, err := projector.Report(context.Background(), query)
	if err != nil || aggregations != 3 || third.Snapshot != version {
		t.Fatalf("stale cache: %v %d", err, aggregations)
	}
}

func TestCancelingViewerDoesNotCancelSharedUsageWork(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	started, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	ledger := &observationLedger{read: func(query runtimeusage.AggregationQuery, visit func(runtimeusage.UsageBucket) error) (runtimeusage.AggregationResult, error) {
		if reads.Add(1) == 1 {
			close(started)
			<-release
		}
		result := runtimeusage.AggregationResult{Snapshot: strings.Repeat("a", 64)}
		if query.KnownSnapshot == result.Snapshot {
			result.Unchanged = true
			return result, nil
		}
		return result, visit(runtimeusage.UsageBucket{Day: "2026-09-28", Status: activity.StatusSucceeded, Calls: 2})
	}}
	projector, _ := runtimeusage.New(runtimeusage.Options{Ledger: ledger, Clock: fixedClock{now}})
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := projector.Report(ctx, queryAround(t, now)); first <- err }()
	<-started
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	second := make(chan error, 1)
	go func() {
		report, err := projector.Report(context.Background(), queryAround(t, now))
		if err == nil && report.Total.AgentAPICalls != 2 {
			err = errors.New("incomplete shared report")
		}
		second <- err
	}()
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}
