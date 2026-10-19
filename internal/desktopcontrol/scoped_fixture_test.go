package desktopcontrol_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/recording"
)

// scopedFixtureScope is deliberately explicit: test observations exercise the
// same original Capture coordinates required by the production observation
// role instead of relying on an unscoped legacy write.
func scopedFixtureScope(captureRunID string) recording.Scope {
	return recording.Scope{
		Capture: captureidentity.Reference{
			Kind: captureidentity.KindManagedRun,
			ID:   captureRunID,
		},
		Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1},
	}
}

func globalFixtureScope() recording.Scope {
	return recording.Scope{Fence: recording.Fence{ArchiveEpoch: 1}}
}

// Successful inspection proves the current required visibility revision is
// completely mirrored; a degraded startup label alone is not readiness.
func waitRecording(t *testing.T, runtime *productruntime.Runtime) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		token, err := runtime.Activities().Begin(t.Context())
		if err == nil {
			_, err = runtime.Activities().ListExchanges(t.Context(), activity.PageRequest{Limit: 1})
		}
		generationCanceled := false
		if errors.Is(err, context.Canceled) && t.Context().Err() == nil {
			changed := runtime.Activities().Check(t.Context(), token)
			generationCanceled = errors.Is(changed, recording.ErrInspectionChanged) || errors.Is(changed, recording.ErrInspectionUnavailable)
		}
		if err == nil && runtime.RawEvidenceStatistics().WriterID != "" {
			return
		}
		retry := err == nil || errors.Is(err, recording.ErrInspectionUnavailable) || errors.Is(err, recording.ErrInspectionChanged) || generationCanceled
		if !retry || time.Now().After(deadline) {
			t.Fatal("optional mirror did not become readable", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func createScopedFixtureCapture(t *testing.T, runtime *productruntime.Runtime) (string, recording.Scope) {
	t.Helper()
	dir := t.TempDir()
	grant, err := runtime.CaptureRuns().Create(context.Background(), capturerun.CreateCommand{
		CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "fixture-agent"),
		ExecutableLabel: "fixture-agent", Lifetime: 5 * time.Minute, CatalogRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Creating the scope advances required visibility. Prime that exact mirror
	// before the first positive fixture observation.
	waitRecording(t, runtime)
	return grant.Run.ID, scopedFixtureScope(grant.Run.ID)
}

func appendScopedFixtureActivity(
	t *testing.T,
	runtime *productruntime.Runtime,
	scope recording.Scope,
	event activity.Event,
) activity.Record {
	t.Helper()
	recorder, ok := runtime.ActivityRecorder().(activity.ScopedPreparedRecorder)
	if !ok {
		t.Fatal("runtime activity recorder has no scoped fixture seam")
	}
	// Prepare has no write side effect. The catalog can become readable just
	// before the optional facade publishes its initialized writer, or during
	// a brief mirror pause; retry only those explicit refusal outcomes.
	deadline := time.Now().Add(5 * time.Second)
	var prepared activity.Record
	var err error
	for {
		prepared, err = recorder.Prepare(event)
		if err == nil {
			break
		}
		if (!errors.Is(err, recording.ErrObservationDiscarded) && !errors.Is(err, recording.ErrInspectionUnavailable)) || time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A scoped write is offered once. Any privacy rejection or write error
	// fails this fixture; never reinterpret it as startup readiness.
	stored, err := recorder.RecordScopedPrepared(t.Context(), scope, prepared)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func appendScopedFixtureContent(
	t *testing.T,
	runtime *productruntime.Runtime,
	scope recording.Scope,
	source *exchangecontent.Source,
) {
	t.Helper()
	recorder, ok := runtime.ExchangeContents().(exchangecontent.ScopedSourceRecorder)
	if !ok {
		t.Fatal("runtime content recorder has no scoped fixture seam")
	}
	if err := recorder.RecordScopedSource(t.Context(), scope, source); err != nil {
		t.Fatal(err)
	}
}

func appendScopedFixtureRecord(
	t *testing.T,
	runtime *productruntime.Runtime,
	scope recording.Scope,
	record exchangecontent.Record,
) {
	t.Helper()
	source, err := exchangecontent.SourceFromRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	appendScopedFixtureContent(t, runtime, scope, source)
}
