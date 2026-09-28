package runtimeusage

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

// Cache only compact reports, never raw observations or token buckets. Every
// hit revalidates the read-only database snapshot, including expiry and policy.
const reportCacheEntries = 32

func (projector *Projector) report(ctx context.Context, query AggregationQuery, userID runtimeuser.UserID) (Report, error) {
	if projector == nil || ctx == nil || query.Validate() != nil {
		return Report{}, ErrInvalidQuery
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	prices := projector.options.Prices.Snapshot(ctx)
	query.PricingRevision = prices.UpdatedAt.Format(time.RFC3339Nano) + ":" + strconv.FormatBool(prices.Stale)
	query.Filters = append([]Filter(nil), query.Filters...)
	sort.Slice(query.Filters, func(i, j int) bool { return query.Filters[i].Dimension < query.Filters[j].Dimension })
	keyData, _ := json.Marshal(struct {
		Period                   Period
		User                     runtimeuser.UserID
		Dimension, Cursor, Price string
		Limit                    int
		Filters                  []Filter
	}{query.Period.Period(), userID, query.Dimension, query.Cursor, query.PricingRevision, query.Limit, query.Filters})
	key := string(keyData)
	result := projector.flights.DoChan(key+"\n"+query.Snapshot, func() (any, error) {
		// One canceled browser must not cancel another viewer's identical query.
		// The storage lifecycle still cancels this work on shutdown.
		operation, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
		defer cancel()
		projector.mu.Lock()
		cached := projector.cache[key]
		projector.mu.Unlock()
		query.KnownSnapshot = cached.Snapshot
		report, err := projector.project(operation, query, userID, projector.options.Clock.Now().UTC(), prices, cached)
		if err != nil {
			return Report{}, err
		}
		projector.mu.Lock()
		if projector.cache == nil {
			projector.cache = make(map[string]Report)
		}
		if _, exists := projector.cache[key]; !exists && len(projector.cache) >= reportCacheEntries {
			for old := range projector.cache {
				delete(projector.cache, old)
				break
			}
		}
		projector.cache[key] = report
		projector.mu.Unlock()
		return report, nil
	})
	select {
	case <-ctx.Done():
		return Report{}, ctx.Err()
	case completed := <-result:
		if completed.Err != nil {
			return Report{}, completed.Err
		}
		return cloneReport(completed.Val.(Report)), nil
	}
}

func cloneReport(report Report) Report {
	report.Filters = append([]Filter{}, report.Filters...)
	report.Groups = append([]GroupUsage{}, report.Groups...)
	report.Days = append([]DayUsage{}, report.Days...)
	if report.Total != nil {
		value := *report.Total
		report.Total = &value
	}
	if report.Collection.CollectingSince != nil {
		value := *report.Collection.CollectingSince
		report.Collection.CollectingSince = &value
	}
	if report.Pricing.UpdatedAt != nil {
		value := *report.Pricing.UpdatedAt
		report.Pricing.UpdatedAt = &value
	}
	return report
}
