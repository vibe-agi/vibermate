package productruntime

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type fakeVolume struct {
	free atomic.Uint64
	fail atomic.Bool
}

func (v *fakeVolume) available(string) (uint64, error) {
	if v.fail.Load() {
		return 0, errors.New("capacity unreadable")
	}
	return v.free.Load(), nil
}

func acceptedObservations(r *Runtime) (accepted uint64) {
	for _, kind := range r.recordingBudget.Snapshot().ByKind {
		accepted += kind.Offers[recording.Accepted].Records
	}
	return accepted
}

// The owner's constraint: a storage problem must never stop the agent. A
// nearly full volume pauses recording, and a request still completes.
func TestRuntimeForwardsWhileRecordingPausedForLowSpace(t *testing.T) {
	volume := &fakeVolume{}
	volume.free.Store(100 << 20)
	f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "raw", choice: "builtin", edge: "send_edit", filesystemAvailable: volume.available})
	if !f.r.recordingStorage.lowSpace.Load() {
		t.Fatal("a Runtime started on a nearly full volume accepted recording")
	}
	waitRecording(t, f.r)
	before := acceptedObservations(f.r)
	run := newOriginalForwardRun(t, f)
	first := run.start(t)
	first.waitEntry(t, f.entered)
	f.unblock()
	assertOriginalForwardReply(t, f, first.join(t), false)
	if after := acceptedObservations(f.r); after != before {
		t.Fatalf("paused recording accepted %d observations", after-before)
	}
	if f.r.recordingBudget.Snapshot().ByKind[recording.Egress].Offers[recording.DroppedUnavailable].Records == 0 {
		t.Fatal("the paused request left no counted drop")
	}
	status := f.r.Status()
	if !status.Recording.LowSpacePause || status.Recording.State != RecordingDegraded || status.RecordingFailure != nil || status.StorageFailure != nil {
		t.Fatalf("low-space pause not reported as a recording-only condition: %+v", status)
	}
	exerciseAcceptanceCaptureControls(t, f.r)
}

func TestRecordingLowSpacePauseGatesOffersOnly(t *testing.T) {
	volume := &fakeVolume{}
	volume.free.Store(8 << 30)
	r, err := startWithBuilders(t.Context(), testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}), runtimeBuildOverrides{filesystemAvailable: volume.available})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	s := r.recordingStorage
	set := func(free uint64, want bool) {
		t.Helper()
		volume.free.Store(free)
		s.probeSpace()
		if s.lowSpace.Load() != want {
			t.Fatalf("free=%d MiB: paused=%v, want %v", free>>20, !want, want)
		}
	}
	// Between the two limits the previous decision holds.
	set(600<<20, false)
	set(100<<20, true)
	set(600<<20, true)
	set(1<<30, false)
	set(100<<20, true)
	volume.fail.Store(true)
	s.probeSpace()
	if !s.lowSpace.Load() {
		t.Fatal("an unreadable capacity resumed recording")
	}
	volume.fail.Store(false)

	stamp := r.recorder.ScopeStamp(captureidentity.Reference{})
	raw := r.rawEvidence.(*recordingRaw)
	observation := rawevidence.Observation{Context: rawevidence.Context{ObservationStamp: stamp, ScopeKind: rawevidence.ScopeRuntime, ExchangeID: "low-space", AccountID: "synthetic-account", AccountRevision: 1, CredentialEpoch: 1, UpstreamEndpointID: "synthetic-endpoint", UpstreamEndpointRevision: 1, Recording: rawevidence.RecordingFull, RetentionDays: 1}, Layer: rawevidence.LayerProviderEgress, Method: "GET", Scheme: "https", Authority: "example.test", Path: "/", Complete: true, Representation: "http_message"}
	if got := raw.TryObserve(stamp, observation); got != recording.DroppedUnavailable {
		t.Fatal("raw evidence offered while paused:", got)
	}
	if buffer := raw.Capture(rawevidence.CaptureMetadata{Context: observation.Context}, 1024); buffer != nil {
		t.Fatal("raw capture buffer handed out while paused")
	}
	if got := r.recorder.ObserveConnection(stamp, connectionevent.Event{}); got != recording.DroppedUnavailable {
		t.Fatal("metadata offered while paused:", got)
	}
	// Reads and the cleanup that frees space keep working, and the maintenance
	// that cleanup runs must not lift the pause.
	if !s.available() {
		t.Fatal("low-space pause made recorded history unavailable")
	}
	if _, err := r.StorageLocation(t.Context()); err != nil {
		t.Fatal("storage inspection while paused:", err)
	}
	if _, err := r.connectionInspection.List(t.Context(), connectionevent.PageRequest{Limit: 1}); err != nil {
		t.Fatal("history read while paused:", err)
	}
	if _, err := r.CleanupExpiredStorage(t.Context()); err != nil {
		t.Fatal("cleanup while paused:", err)
	}
	if !s.lowSpace.Load() || !r.Status().Recording.LowSpacePause {
		t.Fatal("maintenance lifted the low-space pause")
	}

	// The Runtime's own one-second probe resumes recording once space returns.
	volume.free.Store(8 << 30)
	deadline := time.Now().Add(10 * time.Second)
	for s.lowSpace.Load() {
		if time.Now().After(deadline) {
			t.Fatal("recording did not resume after space returned")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r.Status().Recording.LowSpacePause {
		t.Fatal("status still reports a pause")
	}
	if got := raw.TryObserve(stamp, observation); got != recording.Accepted {
		t.Fatal("raw evidence not accepted after resume:", got)
	}
}
