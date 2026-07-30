package connectionevent

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestConnectionTimelineRecordsAttemptDecisionConnectionAndClose(
	t *testing.T,
) {
	t.Parallel()

	repository := &recordingRepository{}
	manager, err := New(context.Background(), Options{
		Repository: repository,
		Clock: &stepClock{
			now: time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC),
		},
		Random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 80)),
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := manager.Start(context.Background(), Attempt{
		Source: Source{
			Confidence: SourceConfidenceUnknown,
		},
		RequestedHost: "api.anthropic.com:443",
		Port:          443,
	})
	if err != nil {
		t.Fatal(err)
	}
	if connection.ID() == "" {
		t.Fatal("connection ID is empty")
	}
	if err := connection.Decide(
		context.Background(),
		DecisionEvidence{
			Source: Source{
				IngressID:  "run-001",
				Label:      "claude",
				Confidence: SourceConfidenceConfigured,
			},
			Decision:             DecisionAllow,
			RuleID:               "m0.agent_endpoint_exact",
			RouteHost:            "api.anthropic.com",
			EgressScope:          EgressScopeAccess,
			EgressSource:         EgressSourceAccessDefault,
			EgressPolicyRevision: 4,
			Decryption:           DecryptionMITM,
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := connection.Connected(
		context.Background(),
		ConnectedEvidence{
			ObservedSNI: "api.anthropic.com",
			RouteHost:   "api.example.com",
			IP:          "203.0.113.8",
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := connection.Finish(
		context.Background(),
		TerminalEvidence{
			Outcome:   OutcomeCompleted,
			BytesUp:   123,
			BytesDown: 456,
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := connection.Finish(
		context.Background(),
		TerminalEvidence{Outcome: OutcomeFailed, ErrorClass: "late"},
	); err != nil {
		t.Fatalf("idempotent Finish() error = %v", err)
	}

	records := repository.records()
	if len(records) != 4 {
		t.Fatalf("record count = %d", len(records))
	}
	for index, phase := range []Phase{
		PhaseAttempted,
		PhaseDecided,
		PhaseConnected,
		PhaseClosed,
	} {
		if records[index].Phase != phase {
			t.Fatalf("record %d phase = %q", index, records[index].Phase)
		}
	}
	terminal := records[len(records)-1]
	if terminal.RouteHost != "api.example.com" ||
		terminal.BytesUp != 123 ||
		terminal.BytesDown != 456 ||
		terminal.Outcome != OutcomeCompleted ||
		terminal.SourceConfidence != SourceConfidenceConfigured {
		t.Fatalf("terminal record = %+v", terminal)
	}
}

func TestDeniedConnectionTerminatesAtDecision(t *testing.T) {
	t.Parallel()

	repository := &recordingRepository{}
	manager := newTestManager(t, repository)
	connection, err := manager.Start(context.Background(), Attempt{
		Source:        Source{Confidence: SourceConfidenceUnknown},
		RequestedHost: "unknown.invalid:443",
		Port:          443,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Decide(context.Background(), DecisionEvidence{
		Source:     Source{Confidence: SourceConfidenceUnknown},
		Decision:   DecisionDeny,
		RuleID:     "capture_run_rejected",
		Decryption: DecryptionNone,
		ErrorClass: "capture_run_rejected",
	}); err != nil {
		t.Fatal(err)
	}
	if err := connection.Connected(
		context.Background(),
		ConnectedEvidence{},
	); !errors.Is(err, ErrInvalidPhase) {
		t.Fatalf("Connected() error = %v", err)
	}
	records := repository.records()
	if len(records) != 2 ||
		records[1].Phase != PhaseDecided ||
		records[1].Outcome != OutcomeDenied {
		t.Fatalf("records = %+v", records)
	}
}

func TestAskedConnectionCanTerminateWhileAwaitingDecision(t *testing.T) {
	t.Parallel()

	repository := &recordingRepository{}
	manager := newTestManager(t, repository)
	connection, err := manager.Start(context.Background(), Attempt{
		Source:        Source{Confidence: SourceConfidenceUnknown},
		RequestedHost: "review.example.test:443",
		Port:          443,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Decide(context.Background(), DecisionEvidence{
		Source: Source{
			IngressID:  "run-asked",
			Label:      "claude",
			Confidence: SourceConfidenceConfigured,
		},
		Decision:   DecisionAsk,
		RuleID:     "policy.ask",
		Decryption: DecryptionNone,
	}); err != nil {
		t.Fatal(err)
	}
	if err := connection.Finish(
		context.Background(),
		TerminalEvidence{
			Outcome:    OutcomeFailed,
			ErrorClass: RecoveryErrorClass,
		},
	); err != nil {
		t.Fatal(err)
	}
	records := repository.records()
	if len(records) != 3 ||
		records[1].Phase != PhaseAsked ||
		records[2].Phase != PhaseFailed ||
		records[2].Decision != DecisionAsk ||
		records[2].ErrorClass != RecoveryErrorClass {
		t.Fatalf("records = %+v", records)
	}
}

func TestAppendFailureDoesNotAdvanceConnectionPhase(t *testing.T) {
	t.Parallel()

	repository := &recordingRepository{}
	manager := newTestManager(t, repository)
	connection, err := manager.Start(context.Background(), Attempt{
		Source:        Source{Confidence: SourceConfidenceUnknown},
		RequestedHost: "api.anthropic.com:443",
		Port:          443,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository.failNext = errors.New("injected append failure")
	decision := DecisionEvidence{
		Source: Source{
			IngressID:  "run-001",
			Label:      "claude",
			Confidence: SourceConfidenceConfigured,
		},
		Decision:             DecisionAllow,
		RuleID:               "m0.agent_endpoint_exact",
		RouteHost:            "api.anthropic.com",
		EgressScope:          EgressScopeAccess,
		EgressSource:         EgressSourceAccessDefault,
		EgressPolicyRevision: 1,
		Decryption:           DecryptionMITM,
	}
	if err := connection.Decide(
		context.Background(),
		decision,
	); err == nil {
		t.Fatal("Decide() unexpectedly succeeded")
	}
	if err := connection.Decide(context.Background(), decision); err != nil {
		t.Fatalf("Decide() retry error = %v", err)
	}
	if len(repository.records()) != 2 {
		t.Fatalf("record count = %d", len(repository.records()))
	}
}

func TestCursorIsCanonicalAndOpaque(t *testing.T) {
	t.Parallel()

	cursor, err := Cursor(42)
	if err != nil {
		t.Fatal(err)
	}
	if cursor == "42" {
		t.Fatal("cursor exposed the sequence directly")
	}
	sequence, err := ParseCursor(cursor)
	if err != nil || sequence != 42 {
		t.Fatalf("ParseCursor() = %d, %v", sequence, err)
	}
	for _, invalid := range []string{"", "42", cursor + "=", "djE6MDI"} {
		if _, err := ParseCursor(invalid); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("ParseCursor(%q) error = %v", invalid, err)
		}
	}
}

func TestManagerShutdownRejectsNewRecording(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, &recordingRepository{})
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := manager.Start(context.Background(), Attempt{
		Source:        Source{Confidence: SourceConfidenceUnknown},
		RequestedHost: "api.anthropic.com:443",
		Port:          443,
	})
	if !errors.Is(err, ErrRuntimeStopping) {
		t.Fatalf("Start() error = %v", err)
	}
}

func newTestManager(
	t *testing.T,
	repository Repository,
) *Manager {
	t.Helper()
	manager, err := New(context.Background(), Options{
		Repository: repository,
		Clock: &stepClock{
			now: time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC),
		},
		Random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 80)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *stepClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	current := clock.now
	clock.now = clock.now.Add(time.Millisecond)
	return current
}

type recordingRepository struct {
	mu       sync.Mutex
	events   []Record
	failNext error
}

func (repository *recordingRepository) Append(
	_ context.Context,
	event Event,
) (Record, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.failNext != nil {
		err := repository.failNext
		repository.failNext = nil
		return Record{}, err
	}
	record := Record{
		Sequence: int64(len(repository.events) + 1),
		Event:    event,
	}
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	repository.events = append(repository.events, record)
	return record, nil
}

func (repository *recordingRepository) List(
	context.Context,
	PageRequest,
) (Page, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return Page{Items: append([]Record(nil), repository.events...)}, nil
}

func (repository *recordingRepository) Timeline(
	_ context.Context,
	connectionID string,
) (Timeline, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	timeline := Timeline{ConnectionID: connectionID}
	for _, record := range repository.events {
		if record.ConnectionID == connectionID {
			timeline.Events = append(timeline.Events, record)
		}
	}
	return timeline, nil
}

func (repository *recordingRepository) Recover(
	context.Context,
	time.Time,
) (int, error) {
	return 0, nil
}

func (repository *recordingRepository) records() []Event {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	events := make([]Event, len(repository.events))
	for index := range repository.events {
		events[index] = repository.events[index].Event
	}
	return events
}
