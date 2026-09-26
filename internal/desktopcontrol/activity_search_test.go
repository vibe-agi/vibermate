package desktopcontrol

import (
	"net/url"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
)

func TestActivitySearchQueryBindsCursorToFilters(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	until := from.AddDate(0, 1, 0)
	query := activity.SearchQuery{
		Limit: 25, Text: "workspace", AccountID: "account.team",
		Model: "gpt-6", Tool: "inspect", Status: activity.StatusFailed,
		Reason: "transport", OccurredAtOrAfter: from, OccurredBefore: until,
	}
	cursor, err := activitySearchCursor(42, query)
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{
		"q": {query.Text}, "accountId": {query.AccountID}, "model": {query.Model},
		"tool": {query.Tool}, "status": {string(query.Status)}, "reason": {query.Reason},
		"from": {from.Format(time.RFC3339Nano)}, "until": {until.Format(time.RFC3339Nano)},
		"limit": {"25"}, "cursor": {cursor},
	}
	parsed, err := parseActivitySearchQuery(values.Encode())
	if err != nil || parsed.BeforeSequence != 42 || parsed.Text != query.Text ||
		parsed.AccountID != query.AccountID || parsed.Model != query.Model ||
		parsed.Tool != query.Tool || parsed.Status != query.Status ||
		parsed.Reason != query.Reason || !parsed.OccurredAtOrAfter.Equal(from) ||
		!parsed.OccurredBefore.Equal(until) {
		t.Fatalf("parseActivitySearchQuery() = %+v, %v", parsed, err)
	}
	values.Set("model", "different")
	if _, err := parseActivitySearchQuery(values.Encode()); err == nil {
		t.Fatal("cursor was accepted for different filters")
	}
}

func TestActivitySearchQueryRejectsEmptyAndMalformedFilters(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"", "q=", "unknown=value", "status=unknown", "from=2026-09-01T00:00:00Z",
		"q=ok&from=bad&until=2026-10-01T00:00:00Z", "q=ok&limit=0",
		"q=ok&q=again", "q=%0Asecret",
	} {
		if _, err := parseActivitySearchQuery(raw); err == nil {
			t.Fatalf("parseActivitySearchQuery(%q) succeeded", raw)
		}
	}
}
