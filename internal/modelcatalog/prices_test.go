package modelcatalog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type priceClock struct{ now time.Time }

func (clock *priceClock) Now() time.Time { return clock.now }

func TestReferencePricesCacheAndFailure(t *testing.T) {
	clock := &priceClock{now: time.Now().UTC()}
	calls, fail := 0, false
	directory := &ReferencePrices{Clock: clock, Fetch: func(ctx context.Context) (*http.Response, error) {
		calls++
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
			t.Fatal("missing bounded fetch")
		}
		if fail {
			return nil, errors.New("offline")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"openai":{"models":{"model":{"id":"model","cost":{"input":2,"output":10,"cache_read":0.2}}}},"opencode":{"models":{"model":{"cost":{"input":0,"output":0}}}}}`))}, nil
	}}
	first := directory.Snapshot(context.Background())
	price, found := first.Lookup("model")
	if !found || price.Rates.Input.RatString() != "2" || first.Stale {
		t.Fatalf("snapshot = %+v", first)
	}
	if _, ok := first.Lookup("openai/model"); !ok {
		t.Fatal("missing explicit directory identity")
	}
	for _, id := range []string{"Model", "relay:model", "model-2026", "opencode/model"} {
		if _, ok := first.Lookup(id); ok {
			t.Fatalf("guessed %s", id)
		}
	}
	directory.Snapshot(context.Background())
	if calls != 1 {
		t.Fatal("did not cache")
	}
	clock.now = clock.now.Add(7 * time.Hour)
	fail = true
	stale := directory.Snapshot(context.Background())
	if !stale.Stale || stale.UpdatedAt != first.UpdatedAt {
		t.Fatal("lost last good snapshot")
	}
	directory.Snapshot(context.Background())
	if calls != 2 {
		t.Fatal("retry storm")
	}
	clock.now = clock.now.Add(time.Minute)
	fail = false
	if directory.Snapshot(context.Background()).Stale || calls != 3 {
		t.Fatal("did not recover")
	}
}

func TestReferencePricesRejectInvalidOrAmbiguousRates(t *testing.T) {
	for _, cost := range []string{
		`{"input":-1,"output":2}`, `{"input":1e999,"output":2}`,
		`{"input":1}`, `{"input":null,"output":2}`,
		`{"input":1,"output":2,"tiers":[{"input":3,"tier":{"type":"duration","size":100}}]}`,
	} {
		if _, err := parseReferencePrices([]byte(`{"openai":{"models":{"m":{"cost":` + cost + `}}}}`)); err == nil {
			t.Fatalf("accepted %s", cost)
		}
	}
	snapshot, err := parseReferencePrices([]byte(`{"openai":{"models":{"same":{"cost":{"input":0,"output":0}}}},"anthropic":{"models":{"same":{"cost":{"input":3,"output":15}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.Lookup("same"); ok {
		t.Fatal("ambiguous model was priced")
	}
	if price, ok := snapshot.Lookup("openai/same"); !ok || price.Rates.Input.Sign() != 0 {
		t.Fatal("known zero lost")
	}
}
