package desktopcontrol_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// "Is my client actually going through vibermate" had no answer through the
// control API. The read carries no capability in either direction: it says
// what a run is and whether anything was seen through it, and never how to
// act on one.
func TestCaptureRunsAreReadableWithoutCarryingACapability(t *testing.T) {
	t.Parallel()

	fixture := newAuditFixture(t)
	recorded := doRequest(
		t,
		fixture.router,
		fixture.authority,
		http.MethodGet,
		"/api/v1/capture-runs?limit=20",
		fixture.readToken,
		nil,
	)
	if recorded.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorded.Code, recorded.Body)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(recorded.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	body := recorded.Body.String()
	for _, forbidden := range []string{
		"proxyCapability",
		"controlCapability",
		"capabilityHash",
	} {
		if bodyContains(body, forbidden) {
			t.Fatalf("the read carried %q: %s", forbidden, body)
		}
	}
	for _, item := range page.Items {
		for _, field := range []string{
			"id",
			"executableLabel",
			"state",
			"observation",
			"recognition",
		} {
			if _, found := item[field]; !found {
				t.Fatalf("a run is missing %q: %+v", field, item)
			}
		}
	}
}

func bodyContains(body string, needle string) bool {
	return len(needle) > 0 && len(body) >= len(needle) &&
		indexOf(body, needle) >= 0
}

func indexOf(haystack string, needle string) int {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return index
		}
	}
	return -1
}
