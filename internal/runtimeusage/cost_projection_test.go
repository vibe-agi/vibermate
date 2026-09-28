package runtimeusage_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/modelcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

func TestCostProjectionSharesOneSnapshotAndScopesAllGroups(t *testing.T) {
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	user := runtimeuser.User{ID: "user.AAAAAAAAAAAAAAAAAAAAAAAAAAA", Username: "alice", State: runtimeuser.StateActive, CreatedAt: now, UpdatedAt: now}
	usage := protocolcore.Usage{InputUncached: protocolcore.UsageValue{Known: true, Tokens: 1000}, Output: protocolcore.UsageValue{Known: true, Tokens: 100}}
	ledger := &observationLedger{items: []runtimeusage.Observation{
		{ExchangeID: "local", OccurredAt: now, Source: "local", Status: activity.StatusFailed, EnvironmentID: "p", AccountID: "a", UpstreamModel: "m", Usage: usage},
		{ExchangeID: "member", OccurredAt: now, Source: "member", UserID: user.ID, Status: activity.StatusSucceeded, EnvironmentID: "p", AccountID: "a", UpstreamModel: "m", Usage: usage},
		{ExchangeID: "unpriced", OccurredAt: now, Source: "member", UserID: user.ID, Status: activity.StatusFailed, EnvironmentID: "p", AccountID: "b", UpstreamModel: "alias", Usage: usage},
	}}
	fetches := 0
	prices := &modelcatalog.ReferencePrices{Clock: fixedClock{now}, Fetch: func(context.Context) (*http.Response, error) {
		fetches++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"openai":{"models":{"m":{"cost":{"input":2,"output":10}}}}}`))}, nil
	}}
	projector, err := runtimeusage.New(runtimeusage.Options{Ledger: ledger, Clock: fixedClock{now}, Prices: prices})
	if err != nil {
		t.Fatal(err)
	}
	all, err := projector.Report(context.Background(), queryAround(t, now))
	if err != nil {
		t.Fatal(err)
	}
	if all.Total.Cost.NanoUSD != 6_000_000 || all.Total.Cost.PricedCalls != 2 || all.Total.Cost.UnpricedCalls != 1 || all.Total.Failed != 2 {
		t.Fatalf("cost = %+v", all.Total)
	}
	if all.Days[0].Cost != all.Total.Cost || all.Pricing.State != "ready" {
		t.Fatal("inconsistent totals")
	}
	for _, dimension := range []string{"profile", "account", "model", "source"} {
		query := queryAround(t, now)
		query.Dimension, query.Limit = dimension, 50
		page, err := projector.Report(context.Background(), query)
		if err != nil {
			t.Fatal(err)
		}
		nanos := int64(0)
		for _, group := range page.Groups {
			nanos += group.Cost.NanoUSD
		}
		if nanos != all.Total.Cost.NanoUSD {
			t.Fatal("group sum differs")
		}
	}
	self, err := projector.ReportForUser(context.Background(), queryAround(t, now), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if self.Total.Cost.NanoUSD != 3_000_000 || self.Total.AgentAPICalls != 2 || self.Days[0].Cost != self.Total.Cost || fetches != 1 {
		t.Fatalf("self report leaked or re-fetched: %+v", self.Total)
	}
}
