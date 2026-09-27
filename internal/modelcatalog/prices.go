package modelcatalog

import (
	"context"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ReferencePrices is descriptive, never an authority for routing or billing.
// Fetch is supplied by the Runtime's existing Hold/audit/transport boundary.
type ReferencePrices struct {
	Fetch     func(context.Context) (*http.Response, error)
	Clock     Clock
	mu        sync.Mutex
	snapshot  PriceSnapshot
	nextCheck time.Time
}

type PriceSnapshot struct {
	UpdatedAt time.Time
	Stale     bool
	models    map[string]ReferencePrice
}

type ReferencePrice struct {
	Rates PriceRates
	Tiers []PriceTier
}

// Rates remain exact decimals until one request is rounded to nano-USD.
type PriceRates struct {
	Input, Output, CacheRead, CacheWrite, Reasoning *big.Rat
}

type PriceTier struct {
	Above int64
	Rates PriceRates
}

func (snapshot PriceSnapshot) Lookup(model string) (ReferencePrice, bool) {
	price, ok := snapshot.models[model]
	return price, ok
}

func (directory *ReferencePrices) Snapshot(ctx context.Context) PriceSnapshot {
	if directory == nil || directory.Fetch == nil || directory.Clock == nil {
		return PriceSnapshot{}
	}
	directory.mu.Lock()
	defer directory.mu.Unlock()
	now := directory.Clock.Now().UTC()
	if now.Before(directory.nextCheck) {
		return directory.snapshot
	}
	// A failed refresh keeps the last good catalog. Bound initial/report latency
	// and retry no more than once a minute, even with several visible dashboards.
	directory.nextCheck = now.Add(time.Minute)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response, err := directory.Fetch(ctx)
	if err == nil {
		defer response.Body.Close()
		if response.StatusCode == http.StatusOK {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, MaxMetadataBodyBytes+1))
			if readErr == nil && len(body) <= MaxMetadataBodyBytes {
				if snapshot, parseErr := parseReferencePrices(body); parseErr == nil {
					snapshot.UpdatedAt = now
					directory.snapshot = snapshot
					directory.nextCheck = now.Add(defaultMetadataTTL)
					return snapshot
				}
			}
		}
	}
	directory.snapshot.Stale = !directory.snapshot.UpdatedAt.IsZero()
	return directory.snapshot
}

type priceWire struct {
	Input      json.Number `json:"input"`
	Output     json.Number `json:"output"`
	CacheRead  json.Number `json:"cache_read"`
	CacheWrite json.Number `json:"cache_write"`
	Reasoning  json.Number `json:"reasoning"`
}

func (wire priceWire) rates() (PriceRates, bool) {
	result := PriceRates{}
	for _, item := range []struct {
		raw    json.Number
		target **big.Rat
	}{
		{wire.Input, &result.Input}, {wire.Output, &result.Output},
		{wire.CacheRead, &result.CacheRead}, {wire.CacheWrite, &result.CacheWrite},
		{wire.Reasoning, &result.Reasoning},
	} {
		if item.raw == "" {
			continue
		}
		// Bound remote decimal/exponent complexity before big.Rat allocation.
		if len(item.raw) > 24 || strings.ContainsAny(string(item.raw), "eE/") {
			return PriceRates{}, false
		}
		value, ok := new(big.Rat).SetString(string(item.raw))
		if !ok || value.Sign() < 0 || value.Cmp(big.NewRat(1_000_000, 1)) > 0 {
			return PriceRates{}, false
		}
		*item.target = value
	}
	return result, true
}

func parseReferencePrices(body []byte) (PriceSnapshot, error) {
	var providers map[string]struct {
		Models map[string]struct {
			ID   string `json:"id"`
			Cost struct {
				priceWire
				Tiers []struct {
					priceWire
					Tier struct {
						Type string `json:"type"`
						Size int64  `json:"size"`
					} `json:"tier"`
				} `json:"tiers"`
				Legacy *priceWire `json:"context_over_200k"`
			} `json:"cost"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &providers); err != nil || len(providers) == 0 || len(providers) > 2048 {
		return PriceSnapshot{}, ErrInvalidCatalog
	}
	snapshot := PriceSnapshot{models: map[string]ReferencePrice{}}
	ambiguous := map[string]bool{}
	// ponytail: exact first-party IDs cover the supported agent protocols.
	// Add explicit providers when needed; never guess a private alias or use a
	// subscription/relay's zero-price entry as the API reference rate.
	for _, provider := range []string{"openai", "anthropic", "google"} {
		models := providers[provider].Models
		if len(models) > MaxCatalogModels {
			return PriceSnapshot{}, ErrInvalidCatalog
		}
		for id, model := range models {
			if !validModelID(id) || (model.ID != "" && model.ID != id) {
				continue
			}
			rates, valid := model.Cost.priceWire.rates()
			if !valid || rates.Input == nil || rates.Output == nil || len(model.Cost.Tiers) > 32 {
				continue
			}
			price := ReferencePrice{Rates: rates}
			lastThreshold := int64(0)
			for _, tier := range model.Cost.Tiers {
				rates, ok := tier.priceWire.rates()
				if !ok || tier.Tier.Type != "context" || tier.Tier.Size <= lastThreshold {
					valid = false
					break
				}
				lastThreshold = tier.Tier.Size
				price.Tiers = append(price.Tiers, PriceTier{Above: tier.Tier.Size, Rates: rates.WithDefaults(price.Rates)})
			}
			// New tier metadata is authoritative; the legacy key may name 200k
			// even when the actual threshold is 272k.
			if len(model.Cost.Tiers) == 0 && model.Cost.Legacy != nil {
				rates, ok := model.Cost.Legacy.rates()
				valid = valid && ok
				price.Tiers = append(price.Tiers, PriceTier{Above: 200_000, Rates: rates.WithDefaults(price.Rates)})
			}
			if !valid {
				continue
			}
			snapshot.models[provider+"/"+id] = price
			if _, exists := snapshot.models[id]; exists {
				ambiguous[id] = true
			}
			snapshot.models[id] = price
		}
	}
	for id := range ambiguous {
		delete(snapshot.models, id)
	}
	if len(snapshot.models) == 0 {
		return PriceSnapshot{}, ErrInvalidCatalog
	}
	return snapshot, nil
}

func (rates PriceRates) WithDefaults(base PriceRates) PriceRates {
	if rates.Input == nil {
		rates.Input = base.Input
	}
	if rates.Output == nil {
		rates.Output = base.Output
	}
	if rates.CacheRead == nil {
		rates.CacheRead = base.CacheRead
	}
	if rates.CacheWrite == nil {
		rates.CacheWrite = base.CacheWrite
	}
	if rates.Reasoning == nil {
		rates.Reasoning = base.Reasoning
	}
	return rates
}
