package desktophost_test

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/recording"
	"testing"
	"time"
)

// A successful inspection validates the complete optional mirror against the
// current required visibility stamp, including the just-created child Capture.
func waitHostInspection(t *testing.T, r *productruntime.Runtime) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		token, err := r.Activities().Begin(t.Context())
		if err == nil {
			_, err = r.Activities().ListExchanges(t.Context(), activity.PageRequest{Limit: 1})
		}
		generationCanceled := false
		if errors.Is(err, context.Canceled) && t.Context().Err() == nil {
			changed := r.Activities().Check(t.Context(), token)
			generationCanceled = errors.Is(changed, recording.ErrInspectionChanged) || errors.Is(changed, recording.ErrInspectionUnavailable)
		}
		// Initialization publishes the mirror before the actual producer sinks.
		// A positive history fixture must witness both before releasing its
		// child; Statistics acquires the current owner and refuses a pause.
		if err == nil && r.RawEvidenceStatistics().WriterID != "" {
			return
		}
		retry := err == nil || errors.Is(err, recording.ErrInspectionUnavailable) || errors.Is(err, recording.ErrInspectionChanged) || generationCanceled
		if !retry || time.Now().After(deadline) {
			t.Fatal("optional mirror and producer did not become ready", err, r.Status().Recording)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Called only after the child and its supervisor have finished offering all
// observations; zero owned slots therefore proves actual worker settlement.
func waitHostRecordingSettled(t *testing.T, r *productruntime.Runtime) {
	t.Helper()
	waitHostRecordingSlots(t, r)
	waitHostInspection(t, r)
}
func waitHostRecordingSlots(t *testing.T, r *productruntime.Runtime) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for r.Status().Recording.ReservedRecords != 0 {
		if time.Now().After(deadline) {
			t.Fatal("optional recording did not settle", r.Status().Recording)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Read the actual target page: success of another inspector is not a promise
// that the catalog cannot change before this read. This retries only explicit
// optional availability/generation outcomes, never commands or outbound work.
func readHostInspection[T any](t *testing.T, boundary recording.InspectionBoundary, read func(context.Context) (T, error)) T {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		token, err := boundary.Begin(t.Context())
		var value T
		if err == nil {
			value, err = read(t.Context())
		}
		if err == nil {
			return value
		}
		changed := false
		if errors.Is(err, context.Canceled) && t.Context().Err() == nil {
			check := boundary.Check(t.Context(), token)
			changed = errors.Is(check, recording.ErrInspectionChanged) || errors.Is(check, recording.ErrInspectionUnavailable)
		}
		if (!errors.Is(err, recording.ErrInspectionUnavailable) && !errors.Is(err, recording.ErrInspectionChanged) && !changed) || time.Now().After(deadline) {
			t.Fatal("target inspection did not become readable", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
