//go:build !race

package productruntime

import (
	"testing"
	"time"
)

func concurrentContentFixtureMode() contentBudgetFixtureMode {
	return contentBudgetFixtureMode{"production_default_acceptance", 30 * time.Second, 2 * time.Minute, time.Minute}
}

func buildConcurrentContentFixtureExchange(request exchangeBuildRequest) (exchangeRuntime, error) {
	return buildExchange(request)
}

func TestRuntimeContentBudgetFixtureConstruction(t *testing.T) {
	testContentBudgetFixtureConstruction(t, "production_default_acceptance", 30*time.Second, 2*time.Minute, time.Minute)
}
