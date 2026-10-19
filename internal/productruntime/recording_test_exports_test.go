package productruntime

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"testing"
)

// External launcher integration tests cannot import runlauncher into this
// package: launcher already imports Runtime. These test-only exports compose
// the real general recorder/compiler without adding a production constructor.
type ACPRecordingTestRecorder interface {
	acpobservation.Recorder
	recording.ScopeIssuer
	Flush(context.Context) error
	Shutdown(context.Context) error
}

func StartACPRecordingTestRuntime(t *testing.T, writer acpobservation.Writer) (*Runtime, ACPRecordingTestRecorder, *runtimepersistence.ObservationStore) {
	t.Helper()
	r, err := startWithBuilders(t.Context(), testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}), runtimeBuildOverrides{recorder: func(ctx context.Context, b *recording.Budget, _ connectionevent.ScopedStore, _ egressaudit.ScopedAttemptStore) *observationRecorder {
		q := newObservationRecorder(ctx, b, nil, nil)
		q.acp = writer
		return q
	}})
	if err != nil {
		t.Fatal(err)
	}
	waitRecording(t, r)
	return r, r.recorder, r.recordingStorage.current.Load().store
}
