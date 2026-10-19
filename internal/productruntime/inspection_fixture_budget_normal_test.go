//go:build !race

package productruntime

import "time"

// inspectionFixtureBudget bounds test callers around real commands and reads.
// It never changes a production operation's own deadline.
func inspectionFixtureBudget(normal, instrumented time.Duration) time.Duration { return normal }
