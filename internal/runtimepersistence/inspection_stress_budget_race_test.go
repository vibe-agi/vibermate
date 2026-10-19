//go:build race

package runtimepersistence

import "time"

// Only large result/ownership-graph stress fixtures use this finite budget.
// It does not scale product defaults, expiry lifetimes or cancellation stimuli.
func inspectionStressBudget(time.Duration) time.Duration { return 5 * time.Minute }
