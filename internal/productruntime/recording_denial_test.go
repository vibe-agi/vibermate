package productruntime

import (
	"context"
	"database/sql"
	"errors"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
)

func TestRuntimeProviderTerminalAndRawDescendantKeepDeniedAnchors(t *testing.T) {
	f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "semantic", choice: "builtin"})
	waitRecording(t, f.r)
	waitRecordingMirror(t, f.r)
	reply := f.send(t.Context(), "POST", semanticOriginalPath, semanticOriginalBody)
	if reply.Err != nil || reply.Status != 200 {
		t.Fatalf("actual forwarding: %+v", reply)
	}
	if err := f.r.recorder.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.r.rawEvidence.Flush(t.Context(), rawevidence.Watermark{}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	frozen := f.semanticRequests[0]
	f.mu.Unlock()
	raw, ok := frozen.RawEvidenceContext()
	if !ok {
		t.Fatal("actual producer omitted Raw context")
	}
	if raw.ExchangeID != frozen.ExchangeID() || raw.ConnectionID != frozen.ConnectionID() || raw.AttemptID != frozen.EgressAttemptID() {
		t.Fatal("producer lost independent child anchors")
	}
	store := f.r.recordingStorage.current.Load().store
	page, err := store.EgressAttemptRepository().List(t.Context(), egressaudit.PageRequest{ExchangeID: frozen.ExchangeID()})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("actual terminal missing: %+v %v", page, err)
	}
	terminal := page.Items[0].Attempt
	if !terminal.Terminal() || terminal.ConnectionID() != raw.ConnectionID || terminal.Parent().ExchangeID != raw.ExchangeID || terminal.ID() != raw.AttemptID {
		t.Fatal("terminal changed immutable producer lineage")
	}
	lease, err := f.r.recordingStorage.BeginMaintenance(t.Context(), f.r.recordingStorage.catalog.Version().Generation+1)
	if err != nil {
		t.Fatal(err)
	}
	usable := false
	defer func() { lease.Complete(usable) }()
	if err = lease.Store().DenyRecordingIdentities(t.Context(), f.r.storage, "exchange", []string{raw.ExchangeID}); err != nil {
		t.Fatal(err)
	}
	if err = lease.Store().PublishInspectionMirror(t.Context(), f.r.storage); err != nil {
		t.Fatal(err)
	}
	usable = true
	lease.Complete(true)
	waitRecording(t, f.r)
	// Replay the real terminal value. No invented missing-parent/partial update.
	scope := raw.ObservationStamp.Scope()
	if err = store.EgressAttemptStore().(egressaudit.ScopedAttemptStore).PutScopedAttempt(t.Context(), scope, terminal); !errors.Is(err, recording.ErrObservationDiscarded) {
		t.Fatalf("late terminal escaped denied Exchange anchor: %v", err)
	}
	db, err := sql.Open("sqlite", f.r.recordingStorage.options.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var before, after, denials int
	if err = db.QueryRow(`SELECT count(*) FROM runtime_raw_evidence_envelopes`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	capture := f.r.rawEvidence.(rawevidence.CaptureObserver).Capture(rawevidence.CaptureMetadata{Context: raw, Layer: rawevidence.LayerProviderResponse, StatusCode: 200}, 1024)
	if capture == nil {
		t.Fatal("valid original producer context was not admitted to bounded queue")
	}
	capture.ObserveBytes([]byte("late provider response"), rawevidence.FrameData)
	if result := capture.Finish(rawevidence.TerminalMetadata{Complete: true}); result != recording.Accepted {
		t.Fatalf("late producer offer=%v", result)
	}
	if err = f.r.rawEvidence.Flush(context.Background(), rawevidence.Watermark{}); !errors.Is(err, recording.ErrObservationDiscarded) {
		t.Fatalf("Raw descendant escaped independent anchors: %v", err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM runtime_raw_evidence_envelopes`).Scan(&after); err != nil || after != before {
		t.Fatalf("denied Raw row retained: %d -> %d %v", before, after, err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM recording_denials`).Scan(&denials); err != nil || denials != 1 {
		t.Fatalf("ordinary rejected descendants created identity registry: %d %v", denials, err)
	}
}

func TestRuntimeLostDenialCoverageLatchesOnlyRecordingUnavailable(t *testing.T) {
	r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	lease, err := r.recordingStorage.BeginMaintenance(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	usable := false
	defer func() { lease.Complete(usable) }()
	if err = lease.Store().DenyRecordingIdentities(t.Context(), r.storage, "usage", []string{"erased"}); err != nil {
		t.Fatal(err)
	}
	if err = lease.Store().PublishInspectionMirror(t.Context(), r.storage); err != nil {
		t.Fatal(err)
	}
	usable = true
	lease.Complete(true)
	waitRecording(t, r)
	db, err := sql.Open("sqlite", r.recordingStorage.options.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`DELETE FROM recording_denials`); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for r.recordingStorage.available() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if r.recordingStorage.available() {
		t.Fatal("lost required privacy state remained available")
	}
	if _, err = r.UsageRepository().UsagePolicy(t.Context()); err != nil {
		t.Fatalf("optional proof loss disabled required control: %v", err)
	}
	dir := t.TempDir()
	if _, err = r.CaptureRuns().Create(t.Context(), capturerun.CreateCommand{CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "agent"), ExecutableLabel: "agent", CatalogRevision: 1, Lifetime: time.Minute}); err != nil {
		t.Fatalf("optional proof loss gated Agent admission: %v", err)
	}
	if _, err = r.ConnectionEvents().List(t.Context(), connectionevent.PageRequest{Limit: 1}); !errors.Is(err, recording.ErrInspectionUnavailable) {
		t.Fatalf("lost proof remained inspectable: %v", err)
	}
}

func TestRuntimeMaintenanceCannotResumeCorruptCertifiedPrivacy(t *testing.T) {
	r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	lease, err := r.recordingStorage.BeginMaintenance(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Complete(false)
	if err = lease.Store().DenyRecordingIdentities(t.Context(), r.storage, "usage", []string{"first", "second"}); err != nil {
		t.Fatal(err)
	}
	if err = lease.Store().PublishInspectionMirror(t.Context(), r.storage); err != nil {
		t.Fatal(err)
	}
	external, err := sql.Open("sqlite", r.recordingStorage.options.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	if _, err = external.Exec(`UPDATE recording_denials SET identity='corrupt' WHERE sequence=1`); err != nil {
		t.Fatal(err)
	}
	lease.Complete(true)
	deadline := time.Now().Add(time.Second)
	for !r.recordingStorage.unsafeMaintenance.Load() && !r.recordingStorage.available() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if r.recordingStorage.available() || !r.recordingStorage.unsafeMaintenance.Load() {
		t.Fatal("maintenance reopened writer before validating its privacy certificate")
	}
}
