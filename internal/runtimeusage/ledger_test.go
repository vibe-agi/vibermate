package runtimeusage_test

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

// Persistence's SQLite integration tests exercise filtering, pagination, expiry
// and snapshots. This adapter isolates the report reducer from SQL and prices.
type observationLedger struct {
	items    []runtimeusage.Observation
	selected runtimeuser.UserID
	read     func(runtimeusage.AggregationQuery, func(runtimeusage.UsageBucket) error) (runtimeusage.AggregationResult, error)
}

func (*observationLedger) RecordUsage(context.Context, runtimeusage.Observation) error { return nil }
func (*observationLedger) UsagePolicy(context.Context) (runtimeusage.CollectionPolicy, error) {
	return runtimeusage.CollectionPolicy{Enabled: true, RetentionDays: 90, Revision: 1}, nil
}
func (*observationLedger) SetUsagePolicy(_ context.Context, p runtimeusage.CollectionPolicy, _ time.Time) (runtimeusage.CollectionPolicy, error) {
	return p, nil
}
func (ledger *observationLedger) ScanUsage(_ context.Context, query runtimeusage.AggregationQuery, user runtimeuser.UserID, _ time.Time, visit func(runtimeusage.UsageBucket) error) (runtimeusage.AggregationResult, error) {
	ledger.selected = user
	if ledger.read != nil {
		return ledger.read(query, visit)
	}
	policy, _ := ledger.UsagePolicy(context.Background())
	result := runtimeusage.AggregationResult{Collection: policy, Snapshot: strings.Repeat("a", 64), Groups: []runtimeusage.GroupUsage{}}
	groups := map[string]*runtimeusage.GroupUsage{}
	for _, record := range ledger.items {
		if user != "" && record.UserID != user {
			continue
		}
		day := ""
		for _, window := range query.Period.DayWindows() {
			if !record.OccurredAt.Before(window.From) && record.OccurredAt.Before(window.Until) {
				day = window.Date
				break
			}
		}
		if day == "" {
			continue
		}
		key := func(dimension string) string {
			switch dimension {
			case "source":
				return record.Source
			case "profile":
				return record.EnvironmentID
			case "account":
				return record.AccountID
			case "model":
				return record.UpstreamModel
			case "caller":
				if record.Attribution != nil {
					return record.Attribution.CallerID
				}
			}
			return ""
		}
		matches := true
		for _, filter := range query.Filters {
			matches = matches && filter.ID == key(filter.Dimension)
		}
		if !matches {
			continue
		}
		bucket := runtimeusage.UsageBucket{Day: day, Model: record.UpstreamModel, Usage: record.Usage, Calls: 1, Status: record.Status}
		if query.Dimension != "" {
			bucket.Day, bucket.GroupID = "", key(query.Dimension)
			group := groups[bucket.GroupID]
			if group == nil {
				group = &runtimeusage.GroupUsage{ID: bucket.GroupID, Label: bucket.GroupID, Dimension: query.Dimension}
				groups[bucket.GroupID] = group
			}
			group.AgentAPICalls++
		}
		if err := visit(bucket); err != nil {
			return result, err
		}
	}
	for _, group := range groups {
		result.Groups = append(result.Groups, *group)
	}
	sort.Slice(result.Groups, func(i, j int) bool {
		if result.Groups[i].AgentAPICalls != result.Groups[j].AgentAPICalls {
			return result.Groups[i].AgentAPICalls > result.Groups[j].AgentAPICalls
		}
		return result.Groups[i].ID < result.Groups[j].ID
	})
	return result, nil
}

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }
