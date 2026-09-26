package desktopcontrol_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	indexer := &readCostIndexer{
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	deeperIdentity := agentconversation.ClientIdentity{
		Client: "codex", SessionID: "session-read-cost", SessionResumable: true,
		ProviderResponseID: "response-read-cost", Source: agentconversation.ClientIdentitySourceLocalState,
		Confidence: "exact", ObservedAt: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC),
	}
	deeperConversation, err := agentconversation.Project(agentconversation.ProjectionInput{
		CaptureRunID: "run-read-cost", ExchangeID: "exchange-read-cost", SourceDisplayName: "Codex",
		ClientIdentity: &deeperIdentity,
	})
	if err != nil {
		t.Fatal(err)
	}
	indexer.onRelease = func() error {
		return runtime.ConversationProjectionWriter().ReprojectConversation(
			context.Background(), "exchange-read-cost", deeperConversation,
		)
	}
	newApplication := func(indexer *readCostIndexer) *desktopcontrol.Handler {
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
		return application
	}
	application := newApplication(indexer)
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
	if indexer.reindexes.Load() != 0 || indexer.identities.Load() != 2 {
		t.Fatalf("point reads: reindexes=%d identities=%d", indexer.reindexes.Load(), indexer.identities.Load())
	}
	response := httptest.NewRecorder()
	application.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/conversations?captureRunId=run-read-cost", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "exchange-read-cost") {
		t.Fatalf("directory must return durable evidence: status=%d body=%s", response.Code, response.Body)
	}
	select {
	case <-indexer.started:
	case <-time.After(time.Second):
		t.Fatal("directory did not schedule identity enrichment")
	}
	select {
	case <-indexer.done:
		t.Fatal("identity enrichment unexpectedly finished before release")
	default:
	}
	again := httptest.NewRecorder()
	application.ServeHTTP(again, httptest.NewRequest(http.MethodGet, "/api/v1/conversations?captureRunId=run-read-cost", nil))
	if again.Code != http.StatusOK || indexer.reindexes.Load() != 1 {
		t.Fatalf("read during enrichment: status=%d scans=%d", again.Code, indexer.reindexes.Load())
	}
	close(indexer.release)
	select {
	case <-indexer.done:
	case <-time.After(time.Second):
		t.Fatal("identity enrichment did not finish")
	}
	if indexer.reindexes.Load() != 1 {
		t.Fatalf("directory scheduled %d scans, want one", indexer.reindexes.Load())
	}
	updated := httptest.NewRecorder()
	application.ServeHTTP(updated, httptest.NewRequest(http.MethodGet, "/api/v1/conversations?captureRunId=run-read-cost", nil))
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), deeperConversation.ProjectionID) {
		t.Fatalf("directory did not expose completed enrichment: status=%d body=%s", updated.Code, updated.Body)
	}

	activityIndexer := &readCostIndexer{
		started: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{}),
	}
	activityApplication := newApplication(activityIndexer)
	activityResponse := httptest.NewRecorder()
	activityApplication.ServeHTTP(activityResponse, httptest.NewRequest(
		http.MethodGet, "/api/v1/activities?kind=exchange&captureRunId=run-read-cost", nil,
	))
	if activityResponse.Code != http.StatusOK || !strings.Contains(activityResponse.Body.String(), "exchange-read-cost") {
		t.Fatalf("Activity read waited for enrichment or lost evidence: status=%d body=%s", activityResponse.Code, activityResponse.Body)
	}
	select {
	case <-activityIndexer.started:
	case <-time.After(time.Second):
		t.Fatal("Activity read did not schedule identity enrichment")
	}
	close(activityIndexer.release)
	select {
	case <-activityIndexer.done:
	case <-time.After(time.Second):
		t.Fatal("Activity enrichment did not finish")
	}
}

type readCostIndexer struct {
	reindexes, identities  atomic.Int64
	started, release, done chan struct{}
	onRelease              func() error
}

func (indexer *readCostIndexer) Reindex(ctx context.Context, _ activity.ConversationIndexRequest) error {
	indexer.reindexes.Add(1)
	close(indexer.started)
	defer close(indexer.done)
	select {
	case <-indexer.release:
	case <-ctx.Done():
	}
	if indexer.onRelease != nil {
		return indexer.onRelease()
	}
	return nil
}

func (indexer *readCostIndexer) Identity(context.Context, string) (agentconversation.ClientIdentity, error) {
	indexer.identities.Add(1)
	return agentconversation.ClientIdentity{}, activity.ErrExchangeNotFound
}
