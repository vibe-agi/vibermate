//go:build race

package productruntime

import (
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchange"
)

func concurrentContentFixtureMode() contentBudgetFixtureMode {
	// Finite containment for instrumented concurrency, not a product latency SLO.
	return contentBudgetFixtureMode{"race_concurrency_coverage", 2 * time.Minute, 5 * time.Minute, 5 * time.Minute}
}

func buildConcurrentContentFixtureExchange(request exchangeBuildRequest) (exchangeRuntime, error) {
	defaults, err := buildExchangeOptions(request)
	if err != nil {
		return nil, err
	}
	options := defaults
	options.ContentObservationTimeout = concurrentContentFixtureMode().content
	return exchange.New(options)
}

func TestRuntimeContentBudgetFixtureConstruction(t *testing.T) {
	testContentBudgetFixtureConstruction(t, "race_concurrency_coverage", 2*time.Minute, 5*time.Minute, 5*time.Minute)
}
