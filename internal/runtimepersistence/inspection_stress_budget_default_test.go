//go:build !race

package runtimepersistence

import "time"

func inspectionStressBudget(normal time.Duration) time.Duration { return normal }
