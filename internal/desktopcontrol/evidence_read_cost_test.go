package desktopcontrol_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

// Opening a Turn, or polling its exact Conversation, must not run the optional
// client-log indexer. In particular, an empty Capture filter must never cause
// an archive-wide scan on the critical path of one visible Conversation.
func TestEvidencePointReadsDoNotReindexTheArchive(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	t.Cleanup(func() { shutdownRuntime(t, runtime) })
	conversation, err := agentconversation.Project(agentconversation.ProjectionInput{
		CaptureRunID: "run-read-cost", ExchangeID: "exchange-read-cost", SourceDisplayName: "Codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Activities().Record(context.Background(), activity.Event{
		Kind: activity.KindExchangeCompleted, SubjectID: "exchange-read-cost",
		EnvironmentID: "policy-read-cost", EnvironmentRevision: 1, EnvironmentDigest: strings.Repeat("a", 64),
		ClientEndpointID: "client", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1,
		Status: activity.StatusSucceeded, SourceKind: activity.SourceCaptureRun,
		SourceDisplayName: "Codex", SourceRecognition: activity.SourceRecognitionVerified,
		CaptureRunID: "run-read-cost", ConnectionID: "connection-read-cost", Conversation: conversation,
	})
	if err != nil {
		t.Fatal(err)
	}
	indexer := &readCostIndexer{}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(),
		Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(),
		Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(),
		Offline: runtime, Clock: desktopcontrol.SystemClock{}, ConversationIndexer: indexer,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/exchanges/exchange-read-cost",
		"/api/v1/activities?conversationId=" + url.QueryEscape(conversation.ProjectionID),
	} {
		response := httptest.NewRecorder()
		application.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", path, response.Code, response.Body)
		}
	}
	if indexer.reindexes != 0 || indexer.identities != 2 {
		t.Fatalf("point reads: reindexes=%d identities=%d", indexer.reindexes, indexer.identities)
	}
	response := httptest.NewRecorder()
	application.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/conversations?captureRunId=run-read-cost", nil))
	if response.Code != http.StatusOK || indexer.reindexes != 1 {
		t.Fatalf("directory must retain enrichment: status=%d reindexes=%d", response.Code, indexer.reindexes)
	}
}

type readCostIndexer struct{ reindexes, identities int }

func (indexer *readCostIndexer) Reindex(context.Context, activity.ConversationIndexRequest) error {
	indexer.reindexes++
	return nil
}

func (indexer *readCostIndexer) Identity(context.Context, string) (agentconversation.ClientIdentity, error) {
	indexer.identities++
	return agentconversation.ClientIdentity{}, activity.ErrExchangeNotFound
}
