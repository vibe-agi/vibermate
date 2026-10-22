package productruntime

import (
	"context"
	"errors"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

// RecordingMaintenanceFailure is an opt-in, closed diagnostic. Raw storage
// errors remain internal; they may contain paths, SQL and retained identities.
type RecordingMaintenanceFailure struct {
	Phase    string    `json:"phase"`
	Reason   string    `json:"reason"`
	Recovery string    `json:"recovery"`
	At       time.Time `json:"at"`
}

func (f RecordingMaintenanceFailure) Valid() bool {
	switch f.Phase {
	case "drain", "select", "delete", "verify", "compact", "certify", "complete", "mirror":
	default:
		return false
	}
	switch f.Reason {
	case "busy", "timeout", "canceled", "failed":
	default:
		return false
	}
	return f.Recovery == "restart_required" && !f.At.IsZero()
}

type recordingMaintenanceError struct {
	phase string
	cause error
}

func (e *recordingMaintenanceError) Error() string {
	return "recording maintenance failed during " + e.phase
}
func (e *recordingMaintenanceError) Unwrap() error { return e.cause }

func (t *statusTracker) failRecordingMaintenance(err error) {
	f := &RecordingMaintenanceFailure{Phase: "drain", Reason: "failed", Recovery: "restart_required", At: t.clock.Now().UTC()}
	var phase *recordingMaintenanceError
	if errors.As(err, &phase) {
		f.Phase = phase.phase
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		f.Reason = "timeout"
	case errors.Is(err, context.Canceled):
		f.Reason = "canceled"
	case errors.Is(err, runtimepersistence.ErrRecordingCheckpointBusy):
		f.Reason = "busy"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status.Recording.State = RecordingDegraded
	if t.status.RecordingMaintenanceFailure == nil {
		t.status.RecordingMaintenanceFailure = f
	}
}
