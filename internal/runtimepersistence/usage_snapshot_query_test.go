package runtimepersistence

import (
	"strings"
	"testing"
)

// Polls that already hold a KnownSnapshot must not pay COUNT(*) over the
// filtered observation set. MAX(sequence) plus the soonest expiry fence are
// enough to detect inserts and time-based disappearance.
func TestUsageSnapshotQueryOmitsCount(t *testing.T) {
	t.Parallel()
	query := usageSnapshotAggregateSQL()
	if strings.Contains(strings.ToLower(query), "count(") {
		t.Fatalf("snapshot aggregate must not COUNT observations:\n%s", query)
	}
	for _, required := range []string{
		"coalesce(max(sequence),0)",
		"coalesce(min(expires_at_unix_ms),0)",
	} {
		if !strings.Contains(strings.ToLower(query), strings.ToLower(required)) {
			t.Fatalf("snapshot aggregate missing %s:\n%s", required, query)
		}
	}
}
