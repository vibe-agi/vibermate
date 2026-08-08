package runtimepersistence

import (
	"bytes"
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
)

func TestConnectionEventTimelinePersistsAndPaginatesAcrossReopen(
	t *testing.T,
) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openTestStore(t, databasePath)
	manager, err := connectionevent.New(context.Background(), connectionevent.Options{
		Repository: store.ConnectionEventRepository(),
		Clock: &connectionClock{
			now: time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC),
		},
		Random: bytes.NewReader(bytes.Repeat([]byte{0x52}, 80)),
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := manager.Start(context.Background(), connectionevent.Attempt{
		Source: connectionevent.Source{
			Confidence: connectionevent.SourceConfidenceUnknown,
		},
		RequestedHost: "api.anthropic.com",
		Port:          443,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Decide(
		context.Background(),
		connectionevent.DecisionEvidence{
			Source: connectionevent.Source{
				IngressID:  "run-001",
				Label:      "claude",
				Confidence: connectionevent.SourceConfidenceConfigured,
			},
			Decision:               connectionevent.DecisionAllow,
			RuleID:                 "client_endpoint_exact",
			RouteHost:              "api.anthropic.com",
			EnvironmentID:          "environment-test",
			EnvironmentName:        "Test Environment",
			EnvironmentRevision:    1,
			ClientEndpointID:       "endpoint-test",
			ClientEndpointRevision: 1,
			EgressScope:            connectionevent.EgressScopeEnvironment,
			EgressSource:           connectionevent.EgressSourceEnvironmentDefault,
			EgressPolicyRevision:   1,
			Decryption:             connectionevent.DecryptionMITM,
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := connection.Connected(
		context.Background(),
		connectionevent.ConnectedEvidence{
			ObservedSNI: "api.anthropic.com",
			RouteHost:   "relay.example.test",
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := connection.Finish(
		context.Background(),
		connectionevent.TerminalEvidence{
			Outcome:   connectionevent.OutcomeCompleted,
			BytesUp:   100,
			BytesDown: 200,
		},
	); err != nil {
		t.Fatal(err)
	}
	connectionID := connection.ID()
	page, err := manager.List(context.Background(), connectionevent.PageRequest{
		Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.NextCursor == "" ||
		page.Items[0].Phase != connectionevent.PhaseClosed ||
		page.Items[1].Phase != connectionevent.PhaseConnected {
		t.Fatalf("first page = %+v", page)
	}
	before, err := connectionevent.ParseCursor(page.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	secondPage, err := manager.List(
		context.Background(),
		connectionevent.PageRequest{
			BeforeSequence: before,
			Limit:          2,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Items) != 2 ||
		secondPage.Items[0].Phase != connectionevent.PhaseDecided ||
		secondPage.Items[1].Phase != connectionevent.PhaseAttempted {
		t.Fatalf("second page = %+v", secondPage)
	}
	filtered, err := manager.List(
		context.Background(),
		connectionevent.PageRequest{Limit: 10, IngressID: "run-001"},
	)
	if err != nil || len(filtered.Items) != 3 {
		t.Fatalf("ingress-filtered page = %+v, %v", filtered, err)
	}
	empty, err := manager.List(
		context.Background(),
		connectionevent.PageRequest{Limit: 10, IngressID: "run-other"},
	)
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("mismatched ingress page = %+v, %v", empty, err)
	}
	latest, err := manager.List(
		context.Background(),
		connectionevent.PageRequest{
			Limit:               10,
			IngressID:           "run-001",
			LatestPerConnection: true,
		},
	)
	if err != nil || len(latest.Items) != 1 ||
		latest.Items[0].ConnectionID != connectionID ||
		latest.Items[0].Phase != connectionevent.PhaseClosed ||
		latest.Items[0].EnvironmentID != "environment-test" ||
		latest.Items[0].ClientEndpointID != "endpoint-test" {
		t.Fatalf("latest-per-connection page = %+v, %v", latest, err)
	}
	timeline, err := manager.Timeline(context.Background(), connectionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline.Events) != 4 ||
		timeline.Events[0].Phase != connectionevent.PhaseAttempted ||
		timeline.Events[3].BytesDown != 200 {
		t.Fatalf("timeline = %+v", timeline)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, databasePath)
	defer func() {
		if err := reopened.Shutdown(context.Background()); err != nil {
			t.Errorf("close reopened store: %v", err)
		}
	}()
	recovered, err := reopened.ConnectionEventRepository().Timeline(
		context.Background(),
		connectionID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Events) != 4 ||
		recovered.Events[3].Outcome != connectionevent.OutcomeCompleted ||
		recovered.Events[3].RouteHost != "relay.example.test" {
		t.Fatalf("recovered timeline = %+v", recovered)
	}
}

func TestBlindConnectionPersistsEnvironmentWithoutClientEndpoint(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openTestStore(t, databasePath)
	startedAt := time.Date(2026, 8, 7, 2, 0, 0, 0, time.UTC)
	event := connectionevent.Event{
		ConnectionID:         "connection-blind-environment",
		IngressID:            "run-transparent",
		SourceLabel:          "claude",
		SourceConfidence:     connectionevent.SourceConfidenceConfigured,
		EnvironmentID:        "system-transparent",
		EnvironmentName:      "Transparent",
		EnvironmentRevision:  1,
		RequestedHost:        "files.example.com",
		RouteHost:            "files.example.com",
		Port:                 443,
		Decision:             connectionevent.DecisionAllow,
		RuleID:               "network_default",
		EgressScope:          connectionevent.EgressScopeNetwork,
		EgressSource:         connectionevent.EgressSourceNetworkDefault,
		EgressPolicyRevision: 1,
		Decryption:           connectionevent.DecryptionBlind,
		Phase:                connectionevent.PhaseClosed,
		StartedAt:            startedAt,
		EndedAt:              startedAt.Add(time.Second),
		Outcome:              connectionevent.OutcomeCompleted,
	}
	if _, err := store.ConnectionEventRepository().Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, databasePath)
	defer func() {
		if err := reopened.Shutdown(context.Background()); err != nil {
			t.Errorf("close reopened store: %v", err)
		}
	}()
	timeline, err := reopened.ConnectionEventRepository().Timeline(
		context.Background(),
		event.ConnectionID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline.Events) != 1 ||
		timeline.Events[0].EnvironmentID != event.EnvironmentID ||
		timeline.Events[0].EnvironmentName != event.EnvironmentName ||
		timeline.Events[0].EnvironmentRevision != event.EnvironmentRevision ||
		timeline.Events[0].ClientEndpointID != "" ||
		timeline.Events[0].ClientEndpointRevision != 0 {
		t.Fatalf("blind Environment evidence changed across reopen: %+v", timeline)
	}
}

func TestConnectionEventRecoveryTerminatesInterruptedConnectionOnce(
	t *testing.T,
) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	firstStore := openTestStore(t, databasePath)
	first, err := connectionevent.New(
		context.Background(),
		connectionevent.Options{
			Repository: firstStore.ConnectionEventRepository(),
			Clock: &connectionClock{
				now: time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC),
			},
			Random: bytes.NewReader(bytes.Repeat([]byte{0x62}, 40)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := first.Start(
		context.Background(),
		connectionevent.Attempt{
			Source: connectionevent.Source{
				Confidence: connectionevent.SourceConfidenceUnknown,
			},
			RequestedHost: "api.anthropic.com",
			Port:          443,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Decide(
		context.Background(),
		connectionevent.DecisionEvidence{
			Source: connectionevent.Source{
				IngressID:  "run-interrupted",
				Label:      "claude",
				Confidence: connectionevent.SourceConfidenceConfigured,
			},
			Decision:               connectionevent.DecisionAllow,
			RuleID:                 "client_endpoint_exact",
			RouteHost:              "api.anthropic.com",
			EnvironmentID:          "environment-test",
			EnvironmentName:        "Test Environment",
			EnvironmentRevision:    1,
			ClientEndpointID:       "endpoint-test",
			ClientEndpointRevision: 1,
			EgressScope:            connectionevent.EgressScopeEnvironment,
			EgressSource:           connectionevent.EgressSourceEnvironmentDefault,
			EgressPolicyRevision:   1,
			Decryption:             connectionevent.DecryptionMITM,
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := connection.Connected(
		context.Background(),
		connectionevent.ConnectedEvidence{
			ObservedSNI: "api.anthropic.com",
		},
	); err != nil {
		t.Fatal(err)
	}
	connectionID := connection.ID()
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	secondStore := openTestStore(t, databasePath)
	second, err := connectionevent.New(
		context.Background(),
		connectionevent.Options{
			Repository: secondStore.ConnectionEventRepository(),
			Clock: &connectionClock{
				now: time.Date(2026, 7, 30, 1, 3, 0, 0, time.UTC),
			},
			Random: bytes.NewReader(bytes.Repeat([]byte{0x63}, 40)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	timeline, err := second.Timeline(context.Background(), connectionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline.Events) != 4 ||
		timeline.Events[3].Phase != connectionevent.PhaseFailed ||
		timeline.Events[3].Outcome != connectionevent.OutcomeFailed ||
		timeline.Events[3].ErrorClass != connectionevent.RecoveryErrorClass {
		t.Fatalf("recovered timeline = %+v", timeline)
	}
	if err := second.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := secondStore.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	thirdStore := openTestStore(t, databasePath)
	defer func() {
		if err := thirdStore.Shutdown(context.Background()); err != nil {
			t.Errorf("close third store: %v", err)
		}
	}()
	third, err := connectionevent.New(
		context.Background(),
		connectionevent.Options{
			Repository: thirdStore.ConnectionEventRepository(),
			Clock: &connectionClock{
				now: time.Date(2026, 7, 30, 1, 4, 0, 0, time.UTC),
			},
			Random: bytes.NewReader(bytes.Repeat([]byte{0x64}, 40)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := third.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown third ConnectionEvent manager: %v", err)
		}
	}()
	recovered, err := third.Timeline(
		context.Background(),
		connectionID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Events) != 4 {
		t.Fatalf("recovery appended another terminal event: %+v", recovered)
	}
}

func TestConnectionEventRecoveryTerminatesPendingAsk(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	firstStore := openTestStore(t, databasePath)
	first, err := connectionevent.New(
		context.Background(),
		connectionevent.Options{
			Repository: firstStore.ConnectionEventRepository(),
			Clock: &connectionClock{
				now: time.Date(2026, 7, 30, 2, 0, 0, 0, time.UTC),
			},
			Random: bytes.NewReader(bytes.Repeat([]byte{0x65}, 40)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := first.Start(
		context.Background(),
		connectionevent.Attempt{
			Source: connectionevent.Source{
				Confidence: connectionevent.SourceConfidenceUnknown,
			},
			RequestedHost: "review.example.test",
			Port:          443,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Decide(
		context.Background(),
		connectionevent.DecisionEvidence{
			Source: connectionevent.Source{
				IngressID:  "run-asked",
				Label:      "claude",
				Confidence: connectionevent.SourceConfidenceConfigured,
			},
			Decision:   connectionevent.DecisionAsk,
			RuleID:     "policy.ask",
			Decryption: connectionevent.DecryptionNone,
		},
	); err != nil {
		t.Fatal(err)
	}
	connectionID := connection.ID()
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	secondStore := openTestStore(t, databasePath)
	defer func() {
		if err := secondStore.Shutdown(context.Background()); err != nil {
			t.Errorf("close second store: %v", err)
		}
	}()
	second, err := connectionevent.New(
		context.Background(),
		connectionevent.Options{
			Repository: secondStore.ConnectionEventRepository(),
			Clock: &connectionClock{
				now: time.Date(2026, 7, 30, 2, 1, 0, 0, time.UTC),
			},
			Random: bytes.NewReader(bytes.Repeat([]byte{0x66}, 40)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := second.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown second manager: %v", err)
		}
	}()
	timeline, err := second.Timeline(context.Background(), connectionID)
	if err != nil {
		t.Fatal(err)
	}
	last := timeline.Events[len(timeline.Events)-1]
	if len(timeline.Events) != 3 ||
		last.Phase != connectionevent.PhaseFailed ||
		last.Decision != connectionevent.DecisionAsk ||
		last.Outcome != connectionevent.OutcomeFailed ||
		last.ErrorClass != connectionevent.RecoveryErrorClass {
		t.Fatalf("recovered pending ask = %+v", timeline)
	}
}

type connectionClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *connectionClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	current := clock.now
	clock.now = clock.now.Add(time.Millisecond)
	return current
}
