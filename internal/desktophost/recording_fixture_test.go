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
		if err == nil {
			return
		}
		if (!errors.Is(err, recording.ErrInspectionUnavailable) && !errors.Is(err, recording.ErrInspectionChanged) && !generationCanceled) || time.Now().After(deadline) {
			t.Fatal("optional mirror did not become readable", err)
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
