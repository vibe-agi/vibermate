package runtimeusage

import (
	"reflect"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestUsageBucketPreservesPerRequestTiersAndRounding(t *testing.T) {
	known := func(tokens int64) protocolcore.UsageValue {
		return protocolcore.UsageValue{Known: true, Tokens: tokens}
	}
	for _, rates := range []string{
		`{"input":0.000001,"output":0.000001}`,
		`{"input":2,"output":10,"cache_read":0.2,"tiers":[{"input":4,"output":20,"cache_read":0.4,"tier":{"type":"context","size":1000}}]}`,
		`{"input":2,"output":10,"cache_read":0.2,"reasoning":12,"tiers":[{"input":1,"output":5,"cache_read":0.1,"tier":{"type":"context","size":1000}}]}`,
	} {
		prices := costSnapshot(t, rates)
		for _, usage := range []protocolcore.Usage{
			{InputUncached: known(1), CacheWrite: known(0), CacheRead: known(0), Output: known(1)},
			{InputUncached: known(800), CacheWrite: known(0), CacheRead: known(0), Output: known(5)},
			{InputUncached: known(800), CacheRead: known(800), Output: known(10), Reasoning: known(3)},
			{CacheRead: known(800), Output: known(5)}, {},
		} {
			bucket := UsageBucket{Model: "m", Status: activity.StatusSucceeded, Usage: usage, Calls: 3}
			got := bucket.Totals(prices)
			want := GroupUsage{}
			for range 3 {
				want.merge((UsageBucket{Model: "m", Status: bucket.Status, Usage: usage, Calls: 1}).Totals(prices))
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("rates=%s usage=%+v\nbucket=%+v\nper request=%+v", rates, usage, got, want)
			}
		}
	}
}

func TestAggregationWindowsAndValidation(t *testing.T) {
	for _, test := range []struct {
		from, until, zone string
		hours             int
	}{
		{"2026-03-08", "2026-03-09", "America/New_York", 23},
		{"2026-11-01", "2026-11-02", "America/New_York", 25},
		{"2026-09-28", "2026-09-29", "Asia/Kathmandu", 24},
	} {
		query, err := NewQuery(test.from, test.until, test.zone)
		if err != nil {
			t.Fatal(err)
		}
		days := query.DayWindows()
		if len(days) != 1 || days[0].Date != test.from || days[0].Until.Sub(days[0].From) != time.Duration(test.hours)*time.Hour {
			t.Fatalf("civil day changed: %+v", days)
		}
	}
	period, _ := NewQuery("2026-09-28", "2026-09-29", "UTC")
	for _, query := range []AggregationQuery{
		{}, {Period: period, Limit: 1}, {Period: period, Dimension: "sql injection", Limit: 1},
		{Period: period, Dimension: "model", Limit: 51}, {Period: period, Snapshot: "unknown"},
		{Period: period, Filters: []Filter{{"caller", "a"}, {"caller", "b"}}},
		{Period: period, Filters: []Filter{{"model", "\x00"}}},
		{Period: period, Filters: []Filter{{"session", "same-id"}}},
	} {
		if query.Validate() == nil {
			t.Fatalf("accepted invalid query: %+v", query)
		}
	}
	if err := (AggregationQuery{Period: period, Filters: []Filter{{"model", ""}, {"session", "same-id"}, {"client", "codex"}}}).Validate(); err != nil {
		t.Fatal(err)
	}
}
