package desktopcontrol_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// Directory polls must not re-arm a 10k identity pass when the Capture's
// activity watermark has not advanced since the last successful enrichment.
func TestConversationDirectoryDoesNotReindexWhenWatermarkIsIdle(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	t.Cleanup(func() { shutdownRuntime(t, runtime) })
	clock := &scheduleClock{now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	indexer := &countingIndexer{caughtUp: true}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(),
		Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(),
		Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(),
		Offline: runtime, Clock: clock, ConversationIndexer: indexer,
	})
	if err != nil {
		t.Fatal(err)
	}
	recordExchange(t, runtime, "exchange-idle-1", "run-idle")

	getDirectory := func() {
		t.Helper()
		response := httptest.NewRecorder()
		application.ServeHTTP(response, httptest.NewRequest(
			http.MethodGet, "/api/v1/conversations?captureRunId=run-idle", nil,
		))
		if response.Code != http.StatusOK {
			t.Fatalf("directory status=%d body=%s", response.Code, response.Body)
		}
	}

	getDirectory()
	waitFor(t, func() bool { return indexer.reindexes.Load() == 1 }, "first enrichment")
	if got := indexer.reindexes.Load(); got != 1 {
		t.Fatalf("first directory scheduled %d scans", got)
	}

	// Past the 5s debounce: without a watermark short-circuit this would arm again.
	clock.advance(6 * time.Second)
	getDirectory()
	time.Sleep(50 * time.Millisecond)
	if got := indexer.reindexes.Load(); got != 1 {
		t.Fatalf("idle watermark re-armed enrichment: scans=%d", got)
	}

	recordExchange(t, runtime, "exchange-idle-2", "run-idle")
	clock.advance(6 * time.Second)
	getDirectory()
	waitFor(t, func() bool { return indexer.reindexes.Load() == 2 }, "enrichment after new activity")
	if got := indexer.reindexes.Load(); got != 2 {
		t.Fatalf("new activity did not re-arm enrichment: scans=%d", got)
	}
}

func TestConversationDirectoryKeepsSchedulingPartialIndexPasses(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	t.Cleanup(func() { shutdownRuntime(t, runtime) })
	clock := &scheduleClock{now: time.Date(2026, 10, 14, 13, 0, 0, 0, time.UTC)}
	indexer := &countingIndexer{caughtUp: false}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(),
		Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(),
		Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(),
		Offline: runtime, Clock: clock, ConversationIndexer: indexer,
	})
	if err != nil {
		t.Fatal(err)
	}
	recordExchange(t, runtime, "exchange-partial-1", "run-partial")
	response := httptest.NewRecorder()
	application.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/conversations?captureRunId=run-partial", nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("directory status=%d", response.Code)
	}
	waitFor(t, func() bool { return indexer.reindexes.Load() == 1 }, "first partial pass")
	clock.advance(6 * time.Second)
	again := httptest.NewRecorder()
	application.ServeHTTP(again, httptest.NewRequest(
		http.MethodGet, "/api/v1/conversations?captureRunId=run-partial", nil,
	))
	if again.Code != http.StatusOK {
		t.Fatalf("directory status=%d", again.Code)
	}
	waitFor(t, func() bool { return indexer.reindexes.Load() == 2 }, "continued partial pass")
}

func recordExchange(t *testing.T, runtime *productruntime.Runtime, exchangeID, captureRunID string) {
	t.Helper()
	conversation, err := agentconversation.Project(agentconversation.ProjectionInput{
		CaptureRunID: captureRunID, ExchangeID: exchangeID, SourceDisplayName: "Codex",
		Request: &protocolcore.Request{Messages: []protocolcore.Message{{
			Agent: &protocolcore.AgentMessageContext{Author: "/root/reviewer"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Activities().Record(context.Background(), activity.Event{
		Kind: activity.KindExchangeCompleted, SubjectID: exchangeID,
		EnvironmentID: "policy-idle", EnvironmentRevision: 1, EnvironmentDigest: strings.Repeat("b", 64),
		ClientEndpointID: "client", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1,
		Status: activity.StatusSucceeded, SourceKind: activity.SourceCaptureRun,
		SourceDisplayName: "Codex", SourceRecognition: activity.SourceRecognitionVerified,
		CaptureRunID: captureRunID, ConnectionID: "connection-" + exchangeID, Conversation: conversation,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, ready func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}

type countingIndexer struct {
	reindexes atomic.Int64
	caughtUp  bool
}

func (indexer *countingIndexer) Reindex(context.Context, activity.ConversationIndexRequest) error {
	indexer.reindexes.Add(1)
	return nil
}

func (indexer *countingIndexer) CaptureRunCaughtUp(string) bool {
	return indexer.caughtUp
}

func (indexer *countingIndexer) Identity(context.Context, string) (agentconversation.ClientIdentity, error) {
	return agentconversation.ClientIdentity{}, activity.ErrExchangeNotFound
}

type scheduleClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *scheduleClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *scheduleClock) advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
}
