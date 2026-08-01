package connectionevent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
)

// The window renders these shapes, so they are generated from the runtime
// rather than typed by hand into the window's tests.
func TestConnectionSamplesDescribeWhatTheRuntimeSends(t *testing.T) {
	started := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	records := []connectionevent.Record{
		{
			Sequence: 1,
			Event: connectionevent.Event{
				ConnectionID:     "connection-blind-sample",
				IngressID:        "run-sample",
				SourceLabel:      "claude",
				SourceConfidence: connectionevent.SourceConfidenceVerified,
				RequestedHost:    "files.example.com",
				RouteHost:        "files.example.com",
				Port:             443,
				Decision:         connectionevent.DecisionAllow,
				RuleID:           "allow.files",
				Decryption:       connectionevent.DecryptionBlind,
				Phase:            connectionevent.PhaseClosed,
				BytesUp:          2048,
				BytesDown:        16384,
				StartedAt:        started,
				EndedAt:          started.Add(3 * time.Second),
				Outcome:          connectionevent.OutcomeCompleted,
			},
		},
		{
			Sequence: 2,
			Event: connectionevent.Event{
				ConnectionID:     "connection-refused-sample",
				IngressID:        "run-sample",
				SourceLabel:      "claude",
				SourceConfidence: connectionevent.SourceConfidenceConfigured,
				RequestedHost:    "unknown.example.com",
				Port:             443,
				Decision:         connectionevent.DecisionDeny,
				RuleID:           "default.ask",
				Decryption:       connectionevent.DecryptionNone,
				Phase:            connectionevent.PhaseDecided,
				StartedAt:        started.Add(time.Minute),
				EndedAt:          started.Add(time.Minute),
				Outcome:          connectionevent.OutcomeDenied,
				ErrorClass:       "connection_denied",
			},
		},
	}
	for _, record := range records {
		if err := record.Event.Validate(); err != nil {
			t.Fatalf("sample %q is not a record the runtime would write: %v",
				record.ConnectionID, err)
		}
	}
	encoded, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("..", "..", "api", "samples", "connections.json")
	if os.Getenv("VIBERMATE_UPDATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read connection samples: %v", err)
	}
	if string(current) != string(encoded) {
		t.Fatalf(
			"the connection samples the window renders are stale; "+
				"rerun with VIBERMATE_UPDATE=1\n--- stored\n%s\n--- current\n%s",
			current,
			encoded,
		)
	}
}
