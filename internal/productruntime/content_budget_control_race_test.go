//go:build race

package productruntime_test

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/productruntime"
)

func TestRuntimeContentBudgetRaceTwoExchangesAndRealControls(t *testing.T) {
	productruntime.RunContentBudgetConcurrentControlFixture(t, runtimeDefaultControls(t))
}
