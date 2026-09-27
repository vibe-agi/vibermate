package runtimeusage

import (
	"math"
	"math/big"
	"time"

	"github.com/vibe-agi/vibermate/internal/modelcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// The report is an API-equivalent estimate at one current price snapshot,
// not a bill. Integers remain exactly representable by Web clients.
const maxCostNanoUSD int64 = 1<<53 - 1

type CostEstimate struct {
	NanoUSD       int64 `json:"nanoUsd"`
	PricedCalls   int   `json:"pricedCalls"`
	PartialCalls  int   `json:"partialCalls"`
	UnpricedCalls int   `json:"unpricedCalls"`
}

type PricingInfo struct {
	Source    string     `json:"source"`
	Currency  string     `json:"currency"`
	Basis     string     `json:"basis"`
	State     string     `json:"state"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

func pricingInfo(snapshot modelcatalog.PriceSnapshot) PricingInfo {
	info := PricingInfo{Source: "models.dev", Currency: "USD", Basis: "current_standard_api", State: "unavailable"}
	if !snapshot.UpdatedAt.IsZero() {
		info.UpdatedAt = &snapshot.UpdatedAt
		info.State = "ready"
		if snapshot.Stale {
			info.State = "stale"
		}
	}
	return info
}

func (cost *CostEstimate) add(value CostEstimate) {
	if value.NanoUSD > maxCostNanoUSD-cost.NanoUSD {
		// Preserve a truthful lower bound rather than overflowing/rounding a bill.
		cost.UnpricedCalls += value.PricedCalls + value.UnpricedCalls
		return
	}
	cost.NanoUSD += value.NanoUSD
	cost.PricedCalls += value.PricedCalls
	cost.PartialCalls += value.PartialCalls
	cost.UnpricedCalls += value.UnpricedCalls
}

func estimateCost(snapshot modelcatalog.PriceSnapshot, record Observation) CostEstimate {
	unknown := CostEstimate{UnpricedCalls: 1}
	price, found := snapshot.Lookup(record.UpstreamModel)
	if !found {
		return unknown
	}
	u := record.Usage
	rates := price.Rates
	partial := false
	if len(price.Tiers) > 0 {
		input, complete := int64(0), true
		for _, item := range []struct {
			value protocolcore.UsageValue
			rate  *big.Rat
		}{
			{u.InputUncached, rates.Input}, {u.CacheRead, rates.CacheRead}, {u.CacheWrite, rates.CacheWrite},
		} {
			if !item.value.Known {
				if item.rate != nil {
					complete = false
				}
				continue
			}
			if item.value.Tokens > math.MaxInt64-input {
				return unknown
			}
			input += item.value.Tokens
		}
		for _, tier := range price.Tiers {
			if input > tier.Above {
				rates = tier.Rates
			}
		}
		if !complete {
			partial = true
			// Missing input can cross another threshold. Use only the known
			// lower bound, including providers whose higher tier is cheaper.
			for _, tier := range price.Tiers {
				if tier.Above >= input {
					rates = minimumRates(rates, tier.Rates)
				}
			}
		}
	}
	amount := new(big.Rat)
	known := false
	add := func(value protocolcore.UsageValue, rate *big.Rat, required bool) {
		if !value.Known {
			partial = partial || required || rate != nil
			return
		}
		if value.Tokens == 0 {
			known = true
			return
		}
		if value.Tokens < 0 || rate == nil {
			partial = true
			return
		}
		known = true
		amount.Add(amount, new(big.Rat).Mul(new(big.Rat).SetInt64(value.Tokens), rate))
	}
	add(u.InputUncached, rates.Input, true)
	add(u.CacheRead, rates.CacheRead, false)
	add(u.CacheWrite, rates.CacheWrite, false)
	if rates.Reasoning == nil || rates.Reasoning.Cmp(rates.Output) == 0 {
		add(u.Output, rates.Output, true) // Reasoning is already included.
	} else if u.Output.Known && u.Reasoning.Known && u.Output.Tokens >= u.Reasoning.Tokens {
		ordinary := u.Output
		ordinary.Tokens -= u.Reasoning.Tokens
		add(ordinary, rates.Output, true)
		add(u.Reasoning, rates.Reasoning, true)
	} else if !u.Output.Known && u.Reasoning.Known {
		partial = true
		add(u.Reasoning, rates.Reasoning, true)
	} else {
		partial = true
		add(u.Output, minimumRate(rates.Output, rates.Reasoning), true)
	}
	if !known {
		return unknown
	}
	// USD per million tokens * tokens * 1000 = nano-USD. Round down only
	// once per request, never each component, to retain lower-bound semantics.
	amount.Mul(amount, big.NewRat(1000, 1))
	nanos := new(big.Int).Quo(amount.Num(), amount.Denom())
	if !nanos.IsInt64() || nanos.Sign() < 0 || nanos.Int64() > maxCostNanoUSD {
		return unknown
	}
	result := CostEstimate{NanoUSD: nanos.Int64(), PricedCalls: 1}
	if partial {
		result.PartialCalls = 1
	}
	return result
}

func minimumRate(a, b *big.Rat) *big.Rat {
	if a == nil || b == nil {
		return nil
	}
	if a.Cmp(b) < 0 {
		return a
	}
	return b
}

func minimumRates(a, b modelcatalog.PriceRates) modelcatalog.PriceRates {
	// An absent separate reasoning rate means the output rate, not free tokens.
	if a.Reasoning == nil {
		a.Reasoning = a.Output
	}
	if b.Reasoning == nil {
		b.Reasoning = b.Output
	}
	return modelcatalog.PriceRates{
		Input: minimumRate(a.Input, b.Input), Output: minimumRate(a.Output, b.Output),
		CacheRead: minimumRate(a.CacheRead, b.CacheRead), CacheWrite: minimumRate(a.CacheWrite, b.CacheWrite),
		Reasoning: minimumRate(a.Reasoning, b.Reasoning),
	}
}
