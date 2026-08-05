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
