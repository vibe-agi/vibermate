package runtimeusage

import (
	"context"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/modelcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

type costClock struct{}

func (costClock) Now() time.Time { return time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC) }

func costSnapshot(t *testing.T, cost string) modelcatalog.PriceSnapshot {
	t.Helper()
	directory := &modelcatalog.ReferencePrices{Clock: costClock{}, Fetch: func(context.Context) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"openai":{"models":{"m":{"cost":` + cost + `}}}}`))}, nil
	}}
	snapshot := directory.Snapshot(context.Background())
	if snapshot.UpdatedAt.IsZero() {
		t.Fatal("invalid test catalog")
	}
	return snapshot
}

func TestEstimateCostExactCategoriesAndUnknowns(t *testing.T) {
	known := func(n int64) protocolcore.UsageValue {
		return protocolcore.UsageValue{Known: true, Tokens: n, Source: "test"}
	}
	base := protocolcore.Usage{InputUncached: known(1000), CacheRead: known(500), CacheWrite: known(0), Output: known(200), Reasoning: known(50)}
	for _, test := range []struct {
		name, rates, model string
		edit               func(*protocolcore.Usage)
		want               CostEstimate
	}{
		{"reasoning is output subset", `{"input":2,"output":10,"cache_read":0.2}`, "m", nil, CostEstimate{NanoUSD: 4_100_000, PricedCalls: 1}},
		{"separate reasoning rate", `{"input":2,"output":10,"cache_read":0.2,"reasoning":20}`, "m", nil, CostEstimate{NanoUSD: 4_600_000, PricedCalls: 1}},
		{"unknown input", `{"input":2,"output":10,"cache_read":0.2}`, "m", func(u *protocolcore.Usage) { u.InputUncached = protocolcore.UsageValue{} }, CostEstimate{NanoUSD: 2_100_000, PricedCalls: 1, PartialCalls: 1}},
		{"missing cache price", `{"input":2,"output":10}`, "m", nil, CostEstimate{NanoUSD: 4_000_000, PricedCalls: 1, PartialCalls: 1}},
		{"unknown model", `{"input":2,"output":10}`, "private:m", nil, CostEstimate{UnpricedCalls: 1}},
		{"known zero", `{"input":2,"output":10}`, "m", func(u *protocolcore.Usage) {
			*u = protocolcore.Usage{InputUncached: known(0), CacheRead: known(0), CacheWrite: known(0), Output: known(0)}
		}, CostEstimate{PricedCalls: 1}},
		{"all unknown", `{"input":2,"output":10}`, "m", func(u *protocolcore.Usage) { *u = protocolcore.Usage{} }, CostEstimate{UnpricedCalls: 1}},
		{"overflow", `{"input":2,"output":10}`, "m", func(u *protocolcore.Usage) { u.Output = known(math.MaxInt64) }, CostEstimate{UnpricedCalls: 1}},
		{"cache writes", `{"input":2,"output":10,"cache_read":0.2,"cache_write":3}`, "m", func(u *protocolcore.Usage) { u.CacheWrite = known(100) }, CostEstimate{NanoUSD: 4_400_000, PricedCalls: 1}},
		{"tier uses cached context", `{"input":2,"output":10,"cache_read":0.2,"tiers":[{"input":4,"output":20,"cache_read":0.4,"tier":{"type":"context","size":1000}}],"context_over_200k":{"input":99,"output":99}}`, "m", nil, CostEstimate{NanoUSD: 8_200_000, PricedCalls: 1}},
		{"unknown tier uses lower bound", `{"input":2,"output":10,"cache_read":0.2,"tiers":[{"input":1,"output":5,"cache_read":0.1,"tier":{"type":"context","size":1000}}]}`, "m", func(u *protocolcore.Usage) { u.InputUncached = protocolcore.UsageValue{} }, CostEstimate{NanoUSD: 1_050_000, PricedCalls: 1, PartialCalls: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			usage := base
			if test.edit != nil {
				test.edit(&usage)
			}
			got := estimateCost(costSnapshot(t, test.rates), Observation{UpstreamModel: test.model, Usage: usage})
			if got != test.want {
				t.Fatalf("got %+v want %+v", got, test.want)
			}
		})
	}
	total := CostEstimate{NanoUSD: maxCostNanoUSD, PricedCalls: 1}
	total.add(CostEstimate{NanoUSD: 1, PricedCalls: 1})
	if total.NanoUSD != maxCostNanoUSD || total.UnpricedCalls != 1 {
		t.Fatalf("aggregate overflow: %+v", total)
	}
}
