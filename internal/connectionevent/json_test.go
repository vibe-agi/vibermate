package connectionevent_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
)

func TestEventJSONOmitsTerminalTimeUntilConnectionEnds(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	event := connectionevent.Event{
		ConnectionID:     "connection-live",
		SourceConfidence: connectionevent.SourceConfidenceUnknown,
		RequestedHost:    "api.example.com",
		Port:             443,
		Decryption:       connectionevent.DecryptionNone,
		Phase:            connectionevent.PhaseAttempted,
		StartedAt:        started,
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if _, exists := document["endedAt"]; exists {
		t.Fatalf("live event exposed endedAt: %s", encoded)
	}
	recordJSON, err := json.Marshal(connectionevent.Record{Sequence: 7, Event: event})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(recordJSON, &document); err != nil {
		t.Fatal(err)
	}
	if got := document["sequence"]; got != float64(7) {
		t.Fatalf("record sequence = %#v: %s", got, recordJSON)
	}
	if _, exists := document["endedAt"]; exists {
		t.Fatalf("live record exposed endedAt: %s", recordJSON)
	}

	event.Phase = connectionevent.PhaseFailed
	event.EndedAt = started.Add(time.Second)
	event.Outcome = connectionevent.OutcomeFailed
	event.ErrorClass = "connection_failed"
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if got := document["endedAt"]; got != "2026-08-05T10:00:01Z" {
		t.Fatalf("terminal endedAt = %#v", got)
	}
}

func TestMITMEventJSONCarriesEnvironmentDecisionEvidence(t *testing.T) {
	t.Parallel()
	event := connectionevent.Event{
		ConnectionID:           "connection-environment",
		IngressID:              "capture-run/run-one",
		SourceLabel:            "claude",
		SourceConfidence:       connectionevent.SourceConfidenceConfigured,
		EnvironmentID:          "work",
		EnvironmentName:        "Work",
		EnvironmentRevision:    3,
		ClientEndpointID:       "anthropic-messages",
		ClientEndpointRevision: 2,
		RequestedHost:          "api.example.com",
		RouteHost:              "gateway.example.com",
		Port:                   443,
		Decision:               connectionevent.DecisionAllow,
		RuleID:                 "client_endpoint_exact",
		EgressScope:            connectionevent.EgressScopeEnvironment,
		EgressSource:           connectionevent.EgressSourceEnvironmentRule,
		EgressPolicyRevision:   4,
		Decryption:             connectionevent.DecryptionMITM,
		Phase:                  connectionevent.PhaseDecided,
		StartedAt:              time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"environmentId":          "work",
		"environmentName":        "Work",
		"environmentRevision":    float64(3),
		"clientEndpointId":       "anthropic-messages",
		"clientEndpointRevision": float64(2),
		"egressScope":            "environment",
		"egressSource":           "environment_rule",
	} {
		if got := document[key]; got != want {
			t.Fatalf("%s = %#v, want %#v: %s", key, got, want, encoded)
		}
	}
}
