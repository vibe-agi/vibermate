package productruntime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

// The fault is at the real default startup recovery call, not a parallel fake
// Runtime. Removing optional lifecycle isolation must fail before release.
func TestRuntimeControlAvailableDuringOrdinaryRecovery(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "blocked"}[blocked], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var enteredOnce sync.Once
			var runtime *Runtime
			defer func() {
				close(release)
				if runtime != nil {
					shutdownRuntime(t, runtime)
				}
			}()
			options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
			type result struct {
				r   *Runtime
				err error
			}
			started := make(chan result, 1)
			go func() {
				r, err := startWithBuilders(context.Background(), options, runtimeBuildOverrides{recoverEgress: func(context.Context, egressaudit.Repository, time.Time) error {
					enteredOnce.Do(func() { close(entered) })
					if blocked {
						<-release
					}
					return errors.New("synthetic ordinary history fault")
				}})
				started <- result{r, err}
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("recovery was not entered")
			}
			select {
			case got := <-started:
				if got.err != nil {
					t.Fatalf("optional recovery failed required startup: %v", got.err)
				}
				runtime = got.r
				if got.r.Status().State != RuntimeStateInitialized {
					t.Fatalf("control not ready: %+v", got.r.Status())
				}
				dir := t.TempDir()
				grant, err := got.r.CaptureRuns().Create(context.Background(), capturerun.CreateCommand{CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "agent"), ExecutableLabel: "agent", CatalogRevision: 1, Lifetime: time.Minute})
				if err != nil {
					t.Fatalf("Create during history fault: %v", err)
				}
				if _, err := got.r.CaptureRuns().Attach(context.Background(), grant.Run.ID, grant.ControlCapability, 123); err != nil {
					t.Fatal("Attach during history fault", err)
				}
				if _, err := got.r.CaptureRuns().Heartbeat(context.Background(), grant.Run.ID, grant.ControlCapability, time.Minute); err != nil {
					t.Fatal("Heartbeat during history fault", err)
				}
				if grant.Run.ID == "" {
					t.Fatal("missing capture")
				}
			case <-time.After(time.Second):
				t.Fatal("required startup waited for optional recovery")
			}
		})
	}
}

func waitRecording(t *testing.T, r *Runtime) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if r.recordingStorage.available() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("recording initialization: %+v", r.Status())
		case <-tick.C:
		}
	}
}
func TestRuntimeFinishDoesNotAwaitAcceptedRecording(t *testing.T) {
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	r := startTestRuntime(t, options)
	waitRecording(t, r)
	held := &heldFinalConnection{delegate: r.recorder.connections, entered: make(chan struct{}, 1), release: make(chan struct{})}
	defer func() { held.unblock(); shutdownRuntime(t, r) }()
	// Replaces only the actual optional sink; the real default Runtime owns the
	// queue, Capture, native revocation, authority and final transition.
	r.recorder.connections = held
	dir := t.TempDir()
	grant, err := r.CaptureRuns().Create(context.Background(), capturerun.CreateCommand{CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "agent"), ExecutableLabel: "agent", CatalogRevision: 1, Lifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ref := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: grant.Run.ID}
	waitRecordingMirror(t, r)
	evidence, e := r.captureRuns.AuthorizeProxy(context.Background(), grant.ProxyCapability)
	if e != nil {
		t.Fatal(e)
	}
	stamp := r.recorder.StampScope(evidence.RecordingScope)
	if stamp.Capture() != ref {
		t.Fatal("authority scope mismatch")
	}
	event := connectionevent.Event{ConnectionID: "finish-held", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Now().UTC()}
	if result := r.recorder.ObserveConnection(stamp, event); result != recording.Accepted {
		t.Fatalf("offer=%v", result)
	}
	<-held.entered
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := r.CaptureRuns().Finish(ctx, grant.Run.ID, grant.ControlCapability); err != nil {
		t.Fatalf("Finish waited for recording: %v", err)
	}
	if !stamp.Current() {
		t.Fatal("normal Finish invalidated accepted original stamp")
	}
	if r.recordingBudget.Snapshot().ReservedRecords != 1 {
		t.Fatal("Finish released in-flight writer ownership")
	}
	held.unblock()
	if err := r.recorder.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := r.recordingStorage.current.Load().store.ConnectionEventRepository().Timeline(context.Background(), "finish-held")
	if err != nil || len(rows.Events) != 1 {
		t.Fatalf("accepted original record did not persist after Finish: %v %v", rows, err)
	}
}

func TestRuntimeCaptureObservationKeepsRestoredControlScope(t *testing.T) {
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	first := startTestRuntime(t, options)
	waitRecording(t, first)
	grant, err := first.ManualCaptures().Create(context.Background(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Original scope", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if err != nil {
		t.Fatal(err)
	}
	shutdownRuntime(t, first)
	db, err := sql.Open("sqlite", options.Paths.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE runtime_storage_state SET archive_epoch=7;UPDATE recording_capture_fences SET generation=9`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	options.OfflineHold = &coordinatorDouble{}
	r := startTestRuntime(t, options)
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	if _, err = r.manualCaptures.AuthorizeProxy(context.Background(), grant.Credential); err != nil {
		t.Fatalf("recording coordinates affected authority: %v", err)
	}
	if err = r.recorder.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	scope := recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: grant.Capture.ID}, Fence: recording.Fence{ArchiveEpoch: 7, CaptureGeneration: 9}}
	current := r.recordingStorage.current.Load()
	times, err := current.store.CaptureObservation(context.Background(), scope, uint64(grant.Capture.CredentialRevision))
	if err != nil || times.First.IsZero() {
		t.Fatalf("original admitted capture coordinates were lost: %+v %v", times, err)
	}
	authorizer, err := captureadmission.NewAuthorizer(r.captureRuns, r.manualCaptures)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := captureadmission.NewProxyCredential(grant.Credential.Value())
	if err != nil {
		t.Fatal(err)
	}
	admission, err := authorizer.Authorize(context.Background(), credential)
	if err != nil || admission.RecordingScope() != scope {
		t.Fatalf("authority admission lost original scope: %v", err)
	}
	connection, err := r.connections.Start(context.Background(), connectionevent.Attempt{Source: connectionevent.Source{IngressID: admission.AdmissionRef(), Label: admission.SourceLabel(), Confidence: connectionevent.SourceConfidenceConfigured}, RequestedHost: "original.test", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	if err = connection.BindObservationScope(connection.OriginalObservationScope(admission.RecordingScope())); err != nil {
		t.Fatal(err)
	}
	if connection.ObservationStamp().Scope() != scope {
		t.Fatal("connection restamped original admission")
	}
	if r.recorder.ScopeStamp(scope.Capture).Current() {
		t.Fatal("reference-only captured scope was guessed")
	}
	stale := scope
	stale.Fence.ArchiveEpoch = 6
	if r.recorder.StampScope(stale).Current() {
		t.Fatal("stale original epoch relabeled")
	}
	if err = r.recorder.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	observations, err := sql.Open("sqlite", r.recordingStorage.options.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer observations.Close()
	var epoch, generation uint64
	if err = observations.QueryRow(`SELECT recording_archive_epoch,recording_capture_generation FROM runtime_connection_events WHERE connection_id=?`, connection.ID()).Scan(&epoch, &generation); err != nil || epoch != 7 || generation != 9 {
		t.Fatalf("connection durable coordinates=%d/%d %v", epoch, generation, err)
	}

}

func TestRuntimeBlockedObservationOpenRetainsShutdownOwnership(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	options.Lifecycle.ShutdownTimeout = 50 * time.Millisecond
	r, err := startWithBuilders(context.Background(), options, runtimeBuildOverrides{openObservations: func(ctx context.Context, o runtimepersistence.ObservationOptions) (*runtimepersistence.ObservationStore, error) {
		close(entered)
		<-release
		return runtimepersistence.OpenObservations(ctx, o)
	}})
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	<-entered
	if r.Status().State != RuntimeStateInitialized {
		t.Fatal("blocked optional opener disabled control")
	}
	if err = r.Shutdown(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("shutdown=%v", err)
	}
	if !r.Status().Recording.CleanupPending {
		close(release)
		t.Fatal("still-owned optional opener was reported closed")
	}
	guard, e := runtimedata.Acquire(options.Paths.DataDirectory())
	if e == nil {
		guard.Release()
		close(release)
		t.Fatal("directory ownership released while opener still active")
	}
	close(release)
	select {
	case <-r.shutdownDone:
	case <-time.After(10 * time.Second):
		t.Fatal("released opener did not settle")
	}
	guard, e = runtimedata.Acquire(options.Paths.DataDirectory())
	if e != nil {
		t.Fatal(e)
	}
	guard.Release()
}

func TestRuntimeFreshProvisionAndLostDeclaredObservations(t *testing.T) {
	for _, fault := range []string{"missing", "corrupt", "lost_receipt"} {
		t.Run(fault, func(t *testing.T) {
			options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
			first := startTestRuntime(t, options)
			waitRecording(t, first)
			statePath := filepath.Join(options.Paths.DataDirectory(), "recording-state.json")
			before, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			var state recordingProvision
			if json.Unmarshal(before, &state) != nil || !state.Provisioned || state.SourceID != first.recordingStorage.options.ArchiveID {
				t.Fatal("writer ready before durable source provisioning")
			}
			shutdownRuntime(t, first)
			path := filepath.Join(options.Paths.DataDirectory(), "observations.db")
			if fault != "corrupt" {
				err = os.Remove(path)
			} else {
				err = os.WriteFile(path, []byte("corrupt optional history"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if fault == "lost_receipt" {
				if err = os.Remove(statePath); err != nil {
					t.Fatal(err)
				}
			}
			options.OfflineHold = &coordinatorDouble{}
			second := startTestRuntime(t, options)
			defer shutdownRuntime(t, second)
			deadline := time.Now().Add(5 * time.Second)
			for second.Status().Recording.State == RecordingStarting && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			status := second.Status()
			if status.State != RuntimeStateInitialized || status.Recording.State != RecordingDegraded {
				t.Fatalf("wrong authority/recording health: %+v", status)
			}
			if second.recordingStorage.available() {
				t.Fatal("replaced lost declared history with fresh healthy writer")
			}
			if fault != "corrupt" {
				if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing history silently recreated: %v", err)
				}
			}
			after, err := os.ReadFile(statePath)
			if fault == "lost_receipt" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatal("lost provisioning receipt silently replaced", err)
				}
			} else if err != nil || string(after) != string(before) {
				t.Fatal("provisioning identity changed")
			}
			if _, err = second.UsageRepository().UsagePolicy(context.Background()); err != nil {
				t.Fatal("optional loss disabled control policy", err)
			}
			if _, err = second.EvidenceArchivePreview(context.Background()); !errors.Is(err, recording.ErrInspectionUnavailable) {
				t.Fatal("unavailable cleanup advertised success", err)
			}
		})
	}
}

func TestRuntimeMaintenanceExcludesActualOptionalWriter(t *testing.T) {
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	r := startTestRuntime(t, options)
	waitRecording(t, r)
	held := &blockedObservationStore{make(chan struct{}, 1), make(chan struct{})}
	var released bool
	defer func() {
		if !released {
			close(held.release)
		}
		shutdownRuntime(t, r)
	}()
	r.recorder.connections = held
	event := connectionevent.Event{ConnectionID: "maintenance-held", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Now().UTC()}
	if r.recorder.ObserveConnection(r.recorder.ScopeStamp(captureidentity.Reference{}), event) != recording.Accepted {
		t.Fatal("writer admission failed")
	}
	<-held.entered
	owner, ok := any(r.recordingStorage).(interface {
		BeginMaintenance(context.Context, uint64) (recordingMaintenanceLease, error)
	})
	if !ok {
		t.Fatal("lifecycle lacks actual writer exclusion for maintenance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if lease, err := owner.BeginMaintenance(ctx, 2); !errors.Is(err, context.DeadlineExceeded) || lease != nil {
		t.Fatalf("maintenance passed blocked I/O: %v %v", lease, err)
	}
	if r.recordingBudget.Snapshot().ReservedRecords != 1 {
		t.Fatal("maintenance lost in-flight reservation")
	}
	if _, err := r.UsageRepository().UsagePolicy(context.Background()); err != nil {
		t.Fatal("maintenance blocked required policy", err)
	}
	close(held.release)
	released = true
	lease, err := owner.BeginMaintenance(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Store() == nil || r.recordingStorage.available() {
		t.Fatal("exclusive maintenance did not own writer")
	}
	lease.Complete(true)
	waitRecording(t, r)
}

type heldFinalConnection struct {
	delegate connectionevent.ScopedStore
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (h *heldFinalConnection) unblock() { h.once.Do(func() { close(h.release) }) }
func (h *heldFinalConnection) PutConnection(ctx context.Context, scope recording.Scope, event connectionevent.Event) error {
	h.entered <- struct{}{}
	<-h.release
	return h.delegate.PutConnection(ctx, scope, event)
}

func TestRuntimeMaintenanceOwnsSynchronousRevealAudit(t *testing.T) {
	r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	db, err := sql.Open("sqlite", r.recordingStorage.options.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE observation_storage_state SET archive_id=archive_id`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.storage.ReadInspectionSnapshot(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	audit := rawevidence.LocatedRevealAudit{RevealAudit: rawevidence.RevealAudit{EnvelopeID: "synthetic-envelope", ExchangeID: "synthetic-exchange", ActorID: "owner", Outcome: rawevidence.RevealUnavailable, OccurredAt: time.Now().UTC()}, SourceID: r.recordingStorage.options.ArchiveID, Token: recording.InspectionToken{Control: snapshot.Stamp, Sources: r.recordingStorage.catalog.Version()}, Scope: r.recorder.ScopeStamp(captureidentity.Reference{}).Scope()}
	done := make(chan error, 1)
	go func() { done <- r.recordingStorage.AppendLocatedRevealAudit(context.Background(), audit) }()
	deadline := time.Now().Add(time.Second)
	for {
		r.recordingStorage.mu.Lock()
		active := r.recordingStorage.active
		r.recordingStorage.mu.Unlock()
		if active > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("audit did not enter real owner")
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err = r.recordingStorage.BeginMaintenance(ctx, 2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("maintenance passed active audit: %v", err)
	}
	if _, err = r.UsageRepository().UsagePolicy(context.Background()); err != nil {
		t.Fatal("locked optional audit blocked control", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	lease, err := r.recordingStorage.BeginMaintenance(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	lease.Complete(false)
	deadline = time.Now().Add(time.Second)
	for !r.recordingStorage.unsafeMaintenance.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if r.recordingStorage.available() {
		t.Fatal("post-mutation failure reopened writer")
	}
	if err = r.recordingStorage.catalog.Check(r.recordingStorage.catalog.Version()); !errors.Is(err, recording.ErrInspectionUnavailable) {
		t.Fatalf("ambiguous source advertised readable: %v", err)
	}
	if _, err = r.recordingStorage.BeginMaintenance(context.Background(), 4); !errors.Is(err, recording.ErrInspectionUnavailable) {
		t.Fatal("unsafe maintenance resumed", err)
	}
	if r.Status().State != RuntimeStateInitialized {
		t.Fatal("optional maintenance failure disabled control")
	}
}

func TestRuntimeRawFacadeRetainsBoundedCapturePort(t *testing.T) {
	r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	if _, ok := r.rawEvidence.(rawevidence.CaptureObserver); !ok {
		t.Fatal("production Raw facade erased bounded capture producer port")
	}
}

func TestRuntimeActualHTTPUsesBoundedRawFacade(t *testing.T) {
	f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "semantic", choice: "builtin"})
	waitRecording(t, f.r)
	waitRecordingMirror(t, f.r)
	reply := f.send(context.Background(), "POST", semanticOriginalPath, semanticOriginalBody)
	if reply.Err != nil || reply.Status != 200 {
		t.Fatalf("real forwarded response: %d %v", reply.Status, reply.Err)
	}
	if err := f.r.rawEvidence.Flush(context.Background(), rawevidence.Watermark{}); err != nil {
		t.Fatal(err)
	}
	if f.r.recordingBudget.Snapshot().ByKind[recording.Raw].Settlements[recording.Persisted].Records < 4 {
		t.Fatalf("actual producer dispatch bypassed Raw facade: %+v", f.r.recordingBudget.Snapshot().ByKind[recording.Raw])
	}
	db, err := sql.Open("sqlite", f.r.recordingStorage.options.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rows, original int
	if err = db.QueryRow(`SELECT COUNT(*),SUM(CASE WHEN recording_archive_epoch=1 AND recording_capture_generation=1 THEN 1 ELSE 0 END) FROM runtime_raw_evidence_envelopes WHERE scope_id=?`, f.capture.ID).Scan(&rows, &original); err != nil || rows < 4 || original != rows {
		t.Fatalf("original Raw scope=%d/%d %v", original, rows, err)
	}
	recorder := f.r.rawEvidence.(rawevidence.CaptureObserver)
	meta := rawevidence.CaptureMetadata{Context: rawevidence.Context{ScopeKind: rawevidence.ScopeManualCapture, ScopeID: f.capture.ID, ExchangeID: "unknown-admission", Recording: rawevidence.RecordingFull, RetentionDays: 1}, Layer: rawevidence.LayerClientIngress, Method: "POST", Scheme: "https", Authority: "example.test", Path: "/"}
	before := f.r.recordingBudget.Snapshot().ReservedRecords
	if recorder.Capture(meta, 1024) != nil {
		t.Fatal("Raw facade fabricated missing admission stamp")
	}
	if f.r.recordingBudget.Snapshot().ReservedRecords != before {
		t.Fatal("unknown scope reserved Raw body ownership")
	}
}

func TestArchiveBarrierAcquisitionDoesNotAdvanceRecordingEpoch(t *testing.T) {
	r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	before := r.recorder.epoch.Value()
	release, err := r.EvidenceArchiveBarrier().BeginClear(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if r.recorder.epoch.Value() != before {
		t.Fatal("mere barrier release advanced recording epoch")
	}
	state, err := r.storage.RecordingCleanupState(t.Context())
	if err != nil || state.RequestedRevision != state.CompletedRevision || state.ActiveRevision != 0 {
		t.Fatalf("mere barrier acquisition requested cleanup: %+v %v", state, err)
	}
}

func TestRuntimeFullDeclaredArchiveInventoryDoesNotGateControl(t *testing.T) {
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	first := startTestRuntime(t, options)
	waitRecording(t, first)
	shutdownRuntime(t, first)
	path := filepath.Join(options.Paths.DataDirectory(), "storage-layout.json")
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	archives := make([]runtimedata.LegacyArchive, 128)
	for i := range archives {
		id := fmt.Sprintf("declared-%03d", i)
		archives[i] = runtimedata.LegacyArchive{ID: id, RelativeDirectory: "archives/" + id, Database: runtimepersistence.SchemaState{Identity: "vibermate-runtime", Revision: 1, InitializedAt: "2026-10-03T00:00:00Z"}}
	}
	// The first declared archive contains eight GiB of unrelated sparse history.
	// Ordinary admission must not extract/copy/scan it; optional owner startup is
	// observed separately below and the source remains untouched.
	largePath := filepath.Join(options.Paths.DataDirectory(), archives[0].RelativeDirectory, "runtime.db")
	if err = os.MkdirAll(filepath.Dir(largePath), 0700); err != nil {
		t.Fatal(err)
	}
	large, err := os.OpenFile(largePath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = large.Truncate(8 << 30); err != nil {
		t.Fatal(err)
	}
	if _, err = large.WriteAt([]byte("unrelated retained history"), (8<<30)-64); err != nil {
		t.Fatal(err)
	}
	if err = large.Close(); err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(largePath)
	if err != nil {
		t.Fatal(err)
	}
	document["archives"] = archives
	encoded, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	options.OfflineHold = &coordinatorDouble{}
	startedAt := time.Now()
	r, err := Start(context.Background(), options)
	if err != nil {
		t.Fatalf("bounded optional source limit rejected control: %v", err)
	}
	defer shutdownRuntime(t, r)
	if time.Since(startedAt) > 2*time.Second {
		t.Fatal("normal admission depended on unrelated history volume")
	}
	afterInfo, err := os.Stat(largePath)
	if err != nil || !os.SameFile(originalInfo, afterInfo) || originalInfo.Size() != afterInfo.Size() || !originalInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("normal startup changed unrelated archive", err)
	}
	waitRecording(t, r)

	if _, err = r.UsageRepository().UsagePolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Activities().ListExchanges(context.Background(), activity.PageRequest{Limit: 1}); !errors.Is(err, runtimepersistence.ErrInspectionSourceLimit) {
		t.Fatalf("unsupported inspection was not explicit: %v", err)
	}
	layout, err := runtimedata.ReadStorageLayout(options.Paths.DataDirectory())
	if err != nil {
		t.Fatal(err)
	}
	sources := make([]runtimepersistence.InspectionSource, 0, 129)
	for _, archive := range archives {
		sources = append(sources, runtimepersistence.InspectionSource{ID: archive.ID, RelativePath: archive.RelativeDirectory + "/runtime.db", Database: archive.Database})
	}
	sources = append(sources, runtimepersistence.InspectionSource{ID: recordingSourceID(layout), RelativePath: "observations.db", Database: runtimepersistence.SchemaState{Identity: "vibermate-observations", Revision: 1}, Current: true})
	fingerprint, _ := json.Marshal(struct {
		Layout  string
		Sources []runtimepersistence.InspectionSource
	}{layout.ID, sources})
	expected := sha256.Sum256(fingerprint)
	if r.recordingStorage.catalog.Version().SourceSet != hex.EncodeToString(expected[:]) {
		t.Fatal("catalog silently omitted declared sources")
	}
}

func TestRuntimeRecordingAdmissionWaitsForDurableProvisionPublication(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	r, err := startWithBuilders(context.Background(), options, runtimeBuildOverrides{publishObservations: func(ctx context.Context, root, id string, guard *instanceguard.Guard) error {
		if first.CompareAndSwap(false, true) {
			close(entered)
			<-release
			if err := runtimedata.MarkObservationProvisioned(ctx, root, id, guard); err != nil {
				return err
			}
			return errors.New("synthetic outcome unknown after durable publication")
		}
		return runtimedata.MarkObservationProvisioned(ctx, root, id, guard)
	}})
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	<-entered
	if r.recordingStorage.available() {
		close(release)
		shutdownRuntime(t, r)
		t.Fatal("producer published before durable provisioning")
	}
	layout, err := runtimedata.ReadStorageLayout(options.Paths.DataDirectory())
	if err != nil || layout.ObservationProvisioning != "unprovisioned" {
		close(release)
		shutdownRuntime(t, r)
		t.Fatal("wrong prepublication layout", err)
	}
	if r.Status().State != RuntimeStateInitialized {
		close(release)
		shutdownRuntime(t, r)
		t.Fatal("optional publication gated execution")
	}
	if result := r.recorder.ObserveEgress(r.recorder.ScopeStamp(captureidentity.Reference{}), runtimeProviderAttempt(t, "before-publication")); result != recording.DroppedUnavailable {
		close(release)
		shutdownRuntime(t, r)
		t.Fatal("optional writer admitted before publication", result)
	}
	close(release)
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	layout, err = runtimedata.ReadStorageLayout(options.Paths.DataDirectory())
	if err != nil || layout.ObservationProvisioning != "provisioned" {
		t.Fatal("ready preceded durable marker", err)
	}
	if r.recordingBudget.Snapshot().ByKind[recording.Egress].Offers[recording.Accepted].Records != 0 {
		t.Fatal("prepublication offer replayed as new work")
	}
}

func TestRuntimeProvisionedLayoutRefusesReceiptDowngrade(t *testing.T) {
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	first := startTestRuntime(t, options)
	waitRecording(t, first)
	shutdownRuntime(t, first)
	path := filepath.Join(options.Paths.DataDirectory(), "recording-state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var receipt recordingProvision
	if err = json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt.Provisioned = false
	data, _ = json.Marshal(receipt)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	observationPath := filepath.Join(options.Paths.DataDirectory(), "observations.db")
	if err = os.Remove(observationPath); err != nil {
		t.Fatal(err)
	}
	options.OfflineHold = &coordinatorDouble{}
	r := startTestRuntime(t, options)
	defer shutdownRuntime(t, r)
	deadline := time.Now().Add(time.Second)
	for r.Status().Recording.State == RecordingStarting && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if r.Status().Recording.State != RecordingDegraded || r.Status().State != RuntimeStateInitialized {
		t.Fatalf("receipt downgrade recreated declared history: %+v", r.Status())
	}
	if _, err = os.Stat(observationPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("declared lost history was replaced", err)
	}
}

func TestRuntimeUndeclaredObservationProvisioningStaysUnavailable(t *testing.T) {
	for _, state := range []string{"", "unknown"} {
		t.Run(state, func(t *testing.T) {
			options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
			first := startTestRuntime(t, options)
			waitRecording(t, first)
			shutdownRuntime(t, first)
			path := filepath.Join(options.Paths.DataDirectory(), "storage-layout.json")
			encoded, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err = json.Unmarshal(encoded, &document); err != nil {
				t.Fatal(err)
			}
			if state == "" {
				delete(document, "observationProvisioning")
			} else {
				document["observationProvisioning"] = state
			}
			encoded, _ = json.Marshal(document)
			if err = os.WriteFile(path, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(filepath.Join(options.Paths.DataDirectory(), "observations.db")); err != nil {
				t.Fatal(err)
			}
			options.OfflineHold = &coordinatorDouble{}
			r := startTestRuntime(t, options)
			defer shutdownRuntime(t, r)
			deadline := time.Now().Add(time.Second)
			for r.Status().Recording.State == RecordingStarting && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if r.Status().State != RuntimeStateInitialized || r.Status().Recording.State != RecordingDegraded {
				t.Fatalf("undeclared optional role gated authority or fabricated recording: %+v", r.Status())
			}
			if _, err = os.Stat(filepath.Join(options.Paths.DataDirectory(), "observations.db")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("undeclared role recreated")
			}
		})
	}
}

func TestRuntimeRequiredControlWriteFaultDoesNotGrantAuthority(t *testing.T) {
	r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	db, err := sql.Open("sqlite", r.paths.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE runtime_storage_state SET archive_epoch=archive_epoch`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	dir := t.TempDir()
	grant, err := r.CaptureRuns().Create(ctx, capturerun.CreateCommand{CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "agent"), ExecutableLabel: "agent", CatalogRevision: 1, Lifetime: time.Minute})
	if err == nil || grant.Run.ID != "" {
		t.Fatalf("required control fault granted Capture: %+v %v", grant.Run, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	page, err := r.CaptureRunReader().ListRuns(context.Background(), capturerun.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("failed control write left authority: %+v %v", page, err)
	}
}

func TestFailedStartGuardHandoffIsAtomicWithActualCompletion(t *testing.T) {
	r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	waitRecording(t, r)
	guard, err := instanceguard.Acquire(filepath.Join(r.paths.DataDirectory(), "server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err = r.HandoffFailedStartHostGuard(guard); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	errorsSeen := make(chan error, 32)
	for range 32 {
		group.Add(1)
		go func() { defer group.Done(); errorsSeen <- r.HandoffFailedStartHostGuard(guard) }()
	}
	err = r.Shutdown(context.Background())
	group.Wait()
	close(errorsSeen)
	if err != nil {
		t.Fatal(err)
	}
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if !r.ShutdownCompleted() {
		t.Fatal("Shutdown returned before actual completion")
	}
	if err = r.HandoffFailedStartHostGuard(guard); err != nil {
		t.Fatal("completed duplicate handoff", err)
	}
	probe, err := instanceguard.Acquire(filepath.Join(r.paths.DataDirectory(), "server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Release()
	if err = r.HandoffFailedStartHostGuard(probe); !errors.Is(err, ErrInvalidOptions) {
		t.Fatal("second distinct owner accepted", err)
	}
	if !probe.OwnsDirectory(r.paths.DataDirectory()) {
		t.Fatal("duplicate handoff released a later owner")
	}
}

// Invalidation closes admission/catalog but must not dispose an idle certified
// physical writer. This distinguishes it from canceled in-flight Raw SQL.
func TestRuntimeIdleRecordingInvalidationKeepsCleanShutdown(t *testing.T) {
	r, err := Start(t.Context(), testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	if err != nil {
		t.Fatal(err)
	}
	waitRecording(t, r)
	r.recordingStorage.invalidateRecordingSource()
	if err := r.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.ShutdownDone():
	default:
		t.Fatal("shutdown ownership did not join")
	}
}
