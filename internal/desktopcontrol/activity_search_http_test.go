package desktopcontrol_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

func TestEvidenceSearchHTTPReturnsOnlyRetainedMetadata(t *testing.T) {
	t.Parallel()
	fixture := newAuditFixture(t)
	_, err := fixture.runtime.Activities().Record(context.Background(), activity.Event{
		Kind:          activity.KindExchangeCompleted,
		EnvironmentID: "search-environment", EnvironmentRevision: 1,
		EnvironmentDigest: strings.Repeat("a", 64),
		ClientEndpointID:  "search-client", ClientEndpointRevision: 1,
		ProtocolPlanID: "search-plan", ProtocolPlanRevision: 1,
		RouteID: "search-route", RouteRevision: 1,
		AccountID: "search-account", AccountRevision: 1, CredentialEpoch: 1,
		SubjectID: "exchange-search", Status: activity.StatusFailed,
		ReasonCode: "provider_transport_failed",
		SourceKind: activity.SourceCaptureRun, SourceDisplayName: "codex",
		SourceRecognition: activity.SourceRecognitionVerified,
		CaptureRunID:      "run-search", ConnectionID: "connection-search",
		Conversation: agentconversation.Ref{
			ProjectionID: "capture_run:run-search:main",
			DisplayName:  "Search fixture",
			Kind:         agentconversation.KindMain,
			Evidence:     agentconversation.EvidenceCaptureRun,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := doRequest(
		t, fixture.router, fixture.authority, http.MethodGet,
		"/api/v1/evidence/search?q=provider_transport_failed&status=failed&limit=10",
		fixture.readToken, nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("search status=%d body=%s", response.Code, response.Body)
	}
	var page desktopcontrol.EvidenceSearchPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Activity.ID != "exchange-search" ||
		page.Items[0].Context.ContentAvailable ||
		!slices.Contains(page.Items[0].Matches, "error") ||
		!slices.Contains(page.Items[0].Matches, "status") {
		t.Fatalf("search page = %+v", page)
	}
	for _, forbidden := range []string{"Authorization", "Bearer", "headers", "body"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("search leaked %q: %s", forbidden, response.Body)
		}
	}
	invalid := doRequest(
		t, fixture.router, fixture.authority, http.MethodGet,
		"/api/v1/evidence/search?q=", fixture.readToken, nil,
	)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty search status=%d", invalid.Code)
	}
}
