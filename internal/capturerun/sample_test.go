package capturerun_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

// The window renders these shapes, so they come from the runtime rather than
// from a hand-typed idea of what it sends.
func TestCaptureRunSamplesDescribeWhatTheRuntimeSends(t *testing.T) {
	created := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	views := []capturerun.View{
		{
			ID:              "run-verified-sample",
			ExecutableLabel: "claude",
			CWD:             "/Users/example/project",
			ProcessID:       4242,
			State:           capturerun.StateAttached,
			Observation:     capturerun.ObservationObserved,
			Recognition:     clientadapter.RecognitionVerified,
			CreatedAt:       created,
			ExpiresAt:       created.Add(time.Hour),
		},
		{
			ID:              "run-unverified-sample",
			ExecutableLabel: "codex",
			CWD:             "/Users/example/project",
			State:           capturerun.StateCreated,
			Observation:     capturerun.ObservationWaitingForTraffic,
			Recognition:     clientadapter.RecognitionUnverified,
			CreatedAt:       created.Add(time.Minute),
			ExpiresAt:       created.Add(time.Hour),
		},
	}
	encoded, err := json.MarshalIndent(views, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("..", "..", "api", "samples", "capture-runs.json")
	if os.Getenv("VIBERMATE_UPDATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture run samples: %v", err)
	}
	if string(current) != string(encoded) {
		t.Fatalf(
			"the capture run samples the window renders are stale; "+
				"rerun with VIBERMATE_UPDATE=1\n--- stored\n%s\n--- current\n%s",
			current,
			encoded,
		)
	}
}
