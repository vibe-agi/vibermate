package runtimeusage

import (
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/modelcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

const MaxGroupPage = 50

var ErrSnapshotChanged = errors.New("usage snapshot changed; reload the report")

// Filter IDs are exact identities, not display names. An explicit empty ID
// selects missing evidence; absence of a filter means all values.
type Filter struct {
	Dimension string `json:"dimension"`
	ID        string `json:"id"`
}

// AggregationQuery selects either complete daily totals (Dimension is empty)
// or one bounded dimension page. Ownership is passed separately by the server,
// never accepted from this display/filter contract.
type AggregationQuery struct {
	Period          Query
	Filters         []Filter
	Dimension       string
	Limit           int
	Cursor          string
	Snapshot        string
	PricingRevision string
	// Internal conditional read; never accepted from HTTP query parameters.
	KnownSnapshot string
}

func (query AggregationQuery) Validate() error {
	if !query.Period.valid() || len(query.Filters) > 12 || len(query.Cursor) > 4096 ||
		len(query.PricingRevision) > 128 || (query.Snapshot != "" && !validSnapshot(query.Snapshot)) ||
		(query.KnownSnapshot != "" && !validSnapshot(query.KnownSnapshot)) {
		return ErrInvalidQuery
	}
	if query.Dimension == "" {
		if query.Limit != 0 || query.Cursor != "" {
			return ErrInvalidQuery
		}
	} else if !groupDimension(query.Dimension) || query.Limit < 1 || query.Limit > MaxGroupPage {
		return ErrInvalidQuery
	}
	seen := map[string]bool{}
	client := ""
	for _, filter := range query.Filters {
		if seen[filter.Dimension] || !filterDimension(filter.Dimension) || len(filter.ID) > 1024 ||
			(filter.Dimension == "exchange" && filter.ID == "") ||
			!utf8.ValidString(filter.ID) || strings.IndexFunc(filter.ID, unicode.IsControl) >= 0 {
			return ErrInvalidQuery
		}
		seen[filter.Dimension] = true
		if filter.Dimension == "client" {
			client = filter.ID
		}
	}
	if seen["session"] && client == "" {
		return ErrInvalidQuery
	}
	return nil
}

func groupDimension(value string) bool {
	switch value {
	case "source", "profile", "account", "model", "caller", "project", "branch", "client":
		return true
	}
	return false
}

func filterDimension(value string) bool {
	return groupDimension(value) || value == "capture" || value == "manualCapture" || value == "session" || value == "exchange"
}

func validSnapshot(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

type DayWindow struct {
	Date        string
	From, Until time.Time
}

// DayWindows preserves the requested IANA calendar, including 23/25-hour days
// and non-hour offsets. SQL must not substitute UTC date() or fixed 24h bins.
func (query Query) DayWindows() []DayWindow {
	if !query.valid() {
		return nil
	}
	result := make([]DayWindow, 0, MaxQueryDays)
	for from := query.from; from.Before(query.until); {
		until := from.AddDate(0, 0, 1)
		result = append(result, DayWindow{Date: query.day(from), From: from, Until: until})
		from = until
	}
	return result
}

// UsageBucket groups only observations with identical per-request pricing
// inputs. Multiplicity is applied AFTER tier selection and per-request rounding.
// Neither a request body nor Observation JSON crosses the query seam.
type UsageBucket struct {
	Day, GroupID, Model string
	Status              activity.Status
	Usage               protocolcore.Usage
	Calls               int
}

type AggregationResult struct {
	Collection CollectionPolicy
	Snapshot   string
	Groups     []GroupUsage
	NextCursor string
	Unchanged  bool
}

func (bucket UsageBucket) Totals(prices modelcatalog.PriceSnapshot) GroupUsage {
	result := GroupUsage{AgentAPICalls: bucket.Calls}
	switch bucket.Status {
	case activity.StatusSucceeded:
		result.Succeeded = bucket.Calls
	case activity.StatusFailed:
		result.Failed = bucket.Calls
	case activity.StatusCanceled:
		result.Canceled = bucket.Calls
	}
	for _, pair := range []struct {
		target *TokenAggregate
		value  protocolcore.UsageValue
	}{
		{&result.Tokens.InputUncached, bucket.Usage.InputUncached},
		{&result.Tokens.CacheRead, bucket.Usage.CacheRead},
		{&result.Tokens.CacheWrite, bucket.Usage.CacheWrite},
		{&result.Tokens.Output, bucket.Usage.Output},
		{&result.Tokens.Reasoning, bucket.Usage.Reasoning},
	} {
		if !pair.value.Known || pair.value.Tokens < 0 ||
			(bucket.Calls > 0 && pair.value.Tokens > math.MaxInt64/int64(bucket.Calls)) {
			pair.target.UnknownCalls = bucket.Calls
		} else {
			pair.target.Tokens = pair.value.Tokens * int64(bucket.Calls)
			pair.target.KnownCalls = bucket.Calls
		}
	}
	cost := estimateCost(prices, Observation{UpstreamModel: bucket.Model, Usage: bucket.Usage})
	if bucket.Calls > 0 && cost.NanoUSD > maxCostNanoUSD/int64(bucket.Calls) {
		result.Cost.UnpricedCalls = bucket.Calls
	} else {
		result.Cost = CostEstimate{
			NanoUSD: cost.NanoUSD * int64(bucket.Calls), PricedCalls: cost.PricedCalls * bucket.Calls,
			PartialCalls: cost.PartialCalls * bucket.Calls, UnpricedCalls: cost.UnpricedCalls * bucket.Calls,
		}
	}
	return result
}
