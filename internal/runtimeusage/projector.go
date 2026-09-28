// Package runtimeusage collects body-free usage independently of recording.
// It never invents missing token counts or model identities.
package runtimeusage

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/modelcatalog"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"golang.org/x/sync/singleflight"
)

const ReportSchema = "vibermate-runtime-usage-report-v1"

type Clock interface{ Now() time.Time }

type Options struct {
	Ledger Repository
	Clock  Clock
	Prices *modelcatalog.ReferencePrices
}

type Projector struct {
	options Options
	mu      sync.Mutex
	cache   map[string]Report
	flights singleflight.Group
}

func New(options Options) (*Projector, error) {
	if options.Ledger == nil || options.Clock == nil {
		return nil, errors.New("Runtime usage dependencies are incomplete")
	}
	return &Projector{options: options}, nil
}

// A report is either the complete summary (empty Dimension, Total and Days) or
// one bounded page (Dimension, Groups and NextCursor). No six eager trees, user
// enumeration, Capture scan, or legacy-attribution reconstruction is involved.
type Report struct {
	Schema      string           `json:"schema"`
	GeneratedAt time.Time        `json:"generatedAt"`
	Period      Period           `json:"period"`
	Snapshot    string           `json:"snapshot"`
	Collection  CollectionPolicy `json:"collection"`
	Pricing     PricingInfo      `json:"pricing"`
	Dimension   string           `json:"dimension"`
	Filters     []Filter         `json:"filters"`
	Total       *GroupUsage      `json:"total"`
	Days        []DayUsage       `json:"days"`
	Groups      []GroupUsage     `json:"groups"`
	NextCursor  string           `json:"nextCursor"`
}

type GroupUsage struct {
	Dimension     string       `json:"dimension"`
	Evidence      string       `json:"evidence"`
	ID            string       `json:"id"`
	Label         string       `json:"label"`
	AgentAPICalls int          `json:"agentApiCalls"`
	Succeeded     int          `json:"succeeded"`
	Failed        int          `json:"failed"`
	Canceled      int          `json:"canceled"`
	Tokens        TokenUsage   `json:"tokens"`
	Cost          CostEstimate `json:"cost"`
}

type TokenAggregate struct {
	Tokens       int64 `json:"tokens"`
	KnownCalls   int   `json:"knownCalls"`
	UnknownCalls int   `json:"unknownCalls"`
}

type TokenUsage struct {
	InputUncached TokenAggregate `json:"inputUncached"`
	CacheWrite    TokenAggregate `json:"cacheWrite"`
	CacheRead     TokenAggregate `json:"cacheRead"`
	Output        TokenAggregate `json:"output"`
	Reasoning     TokenAggregate `json:"reasoning"`
}

// Days are sparse, complete civil-day totals, never the current detail page.
type DayUsage struct {
	Date                  string       `json:"date"`
	AgentAPICalls         int          `json:"agentApiCalls"`
	Succeeded             int          `json:"succeeded"`
	Failed                int          `json:"failed"`
	Canceled              int          `json:"canceled"`
	ModelUnavailableCalls int          `json:"modelUnavailableCalls"`
	Tokens                TokenUsage   `json:"tokens"`
	Cost                  CostEstimate `json:"cost"`
}

func (projector *Projector) SetCollectionPolicy(ctx context.Context, policy CollectionPolicy) (CollectionPolicy, error) {
	return projector.options.Ledger.SetUsagePolicy(ctx, policy, projector.options.Clock.Now().UTC())
}

func (projector *Projector) Report(ctx context.Context, query AggregationQuery) (Report, error) {
	return projector.report(ctx, query, "")
}

func (projector *Projector) ReportForUser(ctx context.Context, query AggregationQuery, userID runtimeuser.UserID) (Report, error) {
	if !userID.Valid() {
		return Report{}, ErrInvalidQuery
	}
	return projector.report(ctx, query, userID)
}

func (projector *Projector) project(ctx context.Context, query AggregationQuery, userID runtimeuser.UserID, now time.Time, prices modelcatalog.PriceSnapshot, cached Report) (Report, error) {
	report := Report{
		Schema: ReportSchema, GeneratedAt: now, Period: query.Period.Period(), Pricing: pricingInfo(prices),
		Dimension: query.Dimension, Filters: append([]Filter{}, query.Filters...),
		Days: []DayUsage{}, Groups: []GroupUsage{},
	}
	days := map[string]*DayUsage{}
	groups := map[string]*GroupUsage{}
	if query.Dimension == "" {
		report.Total = &GroupUsage{ID: "all", Label: "all"}
	}
	result, err := projector.options.Ledger.ScanUsage(ctx, query, userID, now, func(bucket UsageBucket) error {
		totals := bucket.Totals(prices)
		if query.Dimension != "" {
			group := groups[bucket.GroupID]
			if group == nil {
				if len(groups) == query.Limit {
					return errors.New("usage page exceeded its row limit")
				}
				group = &GroupUsage{}
				groups[bucket.GroupID] = group
			}
			group.merge(totals)
			return nil
		}
		report.Total.merge(totals)
		day := days[bucket.Day]
		if day == nil {
			if len(days) == MaxQueryDays {
				return errors.New("usage summary exceeded its day limit")
			}
			day = &DayUsage{Date: bucket.Day}
			days[bucket.Day] = day
		}
		day.AgentAPICalls += totals.AgentAPICalls
		day.Succeeded += totals.Succeeded
		day.Failed += totals.Failed
		day.Canceled += totals.Canceled
		day.Tokens.addAggregate(totals.Tokens)
		day.Cost.add(totals.Cost)
		if bucket.Model == "" {
			day.ModelUnavailableCalls += bucket.Calls
		}
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	if result.Unchanged {
		cached.GeneratedAt = now
		return cached, nil
	}
	report.Collection, report.Snapshot, report.NextCursor = result.Collection, result.Snapshot, result.NextCursor
	for _, day := range days {
		report.Days = append(report.Days, *day)
	}
	sort.Slice(report.Days, func(i, j int) bool { return report.Days[i].Date < report.Days[j].Date })
	for _, descriptor := range result.Groups {
		totals := groups[descriptor.ID]
		if totals == nil || totals.AgentAPICalls != descriptor.AgentAPICalls {
			return Report{}, errors.New("usage page and aggregate snapshot disagree")
		}
		totals.ID, totals.Label, totals.Dimension, totals.Evidence = descriptor.ID, descriptor.Label, descriptor.Dimension, descriptor.Evidence
		report.Groups = append(report.Groups, *totals)
	}
	return report, nil
}

func (group *GroupUsage) merge(value GroupUsage) {
	group.AgentAPICalls += value.AgentAPICalls
	group.Succeeded += value.Succeeded
	group.Failed += value.Failed
	group.Canceled += value.Canceled
	group.Tokens.addAggregate(value.Tokens)
	group.Cost.add(value.Cost)
}

func (usage *TokenUsage) addAggregate(value TokenUsage) {
	usage.InputUncached.addAggregate(value.InputUncached)
	usage.CacheWrite.addAggregate(value.CacheWrite)
	usage.CacheRead.addAggregate(value.CacheRead)
	usage.Output.addAggregate(value.Output)
	usage.Reasoning.addAggregate(value.Reasoning)
}

func (aggregate *TokenAggregate) addAggregate(value TokenAggregate) {
	if value.Tokens < 0 || value.Tokens > math.MaxInt64-aggregate.Tokens {
		aggregate.UnknownCalls += value.KnownCalls + value.UnknownCalls
		return
	}
	aggregate.Tokens += value.Tokens
	aggregate.KnownCalls += value.KnownCalls
	aggregate.UnknownCalls += value.UnknownCalls
}
