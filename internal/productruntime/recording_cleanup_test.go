package productruntime

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

type cleanupHeldConnection struct {
	delegate         connectionevent.ScopedStore
	entered, release chan struct{}
}

func (h *cleanupHeldConnection) PutConnection(ctx context.Context, scope recording.Scope, event connectionevent.Event) error {
	err := h.delegate.PutConnection(ctx, scope, event)
	close(h.entered)
	<-h.release
	return err
}
func TestRuntimeOwnedCleanupWaitsForActualReturnAndAcknowledgesPhysicalFiles(t *testing.T) {
	r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	waitRecording(t, r)
	held := &cleanupHeldConnection{delegate: r.recordingStorage, entered: make(chan struct{}), release: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(held.release)
		}
		shutdownRuntime(t, r)
	}()
	r.recorder.connections = held
	event := connectionevent.Event{ConnectionID: "cleanup-owned", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Now().UTC()}
	if r.recorder.ObserveConnection(r.recorder.ScopeStamp(captureidentity.Reference{}), event) != recording.Accepted {
		t.Fatal("observation refused")
	}
	select {
	case <-held.entered:
	case <-time.After(time.Second):
		t.Fatal("actual observation did not commit")
	}
	receipt, err := r.recordingStorage.ClearEvidence(t.Context())
	if err != nil || !receipt.CleanupPending {
		t.Fatalf("required clear: %+v %v", receipt, err)
	}
	if _, err := r.ManualCaptures().Create(t.Context(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "control during cleanup", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked}); err != nil {
		t.Fatal(err)
	}
	state, err := r.storage.RecordingCleanupState(t.Context())
	if err != nil || state.CompletedRevision == state.RequestedRevision {
		t.Fatalf("held actual owner called complete: %+v %v", state, err)
	}
	close(held.release)
	released = true
	deadline := time.Now().Add(60 * time.Second)
	for {
		state, err = r.storage.RecordingCleanupState(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if state.ActiveRevision == 0 && state.RequestedRevision == state.CompletedRevision {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("durable work was never physically completed: %+v status=%+v", state, r.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if state.SourceGeneration < 2 {
		t.Fatal("cleanup did not advance source generation")
	}
	if _, exists, err := runtimedata.ReadRecordingCleanupReceipt(r.paths.DataDirectory()); err != nil || exists {
		t.Fatalf("receipt not retired: %v %v", exists, err)
	}
	db, err := sql.Open("sqlite", filepath.Join(r.paths.DataDirectory(), "observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM runtime_connection_events WHERE connection_id='cleanup-owned'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted bytes still indexed: %d %v", count, err)
	}
	if info, err := os.Stat(filepath.Join(r.paths.DataDirectory(), "observations.db-wal")); err == nil && info.Size() != 0 {
		t.Fatalf("retained WAL at completion: %d", info.Size())
	}
}

func TestRuntimeCleanupRestartBlocksOptionalAdmissionAndPreservesActiveClaim(t *testing.T) {
	for _, mode := range []string{"receipt", "no-receipt", "mismatch"} {
		t.Run(mode, func(t *testing.T) {
			options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
			first := startTestRuntime(t, options)
			waitRecording(t, first)
			root := first.paths.DataDirectory()
			layout := first.recordingStorage.layout
			sourceSet := first.recordingStorage.catalog.Version().SourceSet
			shutdownRuntime(t, first)
			control, err := runtimepersistence.OpenControl(t.Context(), runtimepersistence.ControlOptions{DatabasePath: filepath.Join(root, "runtime.db"), LayoutID: layout.ID, BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = control.ClearRecordingArchive(t.Context()); err != nil {
				t.Fatal(err)
			}
			target, generation, cutoff, err := control.ClaimRecordingCleanup(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = control.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			if mode != "no-receipt" {
				guard, err := instanceguard.Acquire(filepath.Join(root, "runtime-data.lock"))
				if err != nil {
					t.Fatal(err)
				}
				receipt := runtimedata.RecordingCleanupReceipt{Schema: "vibermate-recording-cleanup/v1", LayoutID: layout.ID, SourceSet: sourceSet, TargetRevision: target, SourceGeneration: generation, CutoffMillis: cutoff, Phase: "delete", CountsExact: true}
				if mode == "mismatch" {
					receipt.TargetRevision++
				}
				if err = runtimedata.WriteRecordingCleanupReceipt(t.Context(), root, guard, receipt); err != nil {
					t.Fatal(err)
				}
				if err = guard.Release(); err != nil {
					t.Fatal(err)
				}
			}
			entered, release := make(chan struct{}), make(chan struct{})
			released := false
			second, err := startWithBuilders(t.Context(), options, runtimeBuildOverrides{openObservations: func(ctx context.Context, o runtimepersistence.ObservationOptions) (*runtimepersistence.ObservationStore, error) {
				close(entered)
				<-release
				return runtimepersistence.OpenObservations(ctx, o)
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if !released {
					close(release)
				}
				shutdownRuntime(t, second)
			}()
			if mode != "mismatch" {
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("recovery open not reached")
				}
			}
			if second.recordingStorage.available() {
				t.Fatal("startup recovery admitted writer")
			}
			if token, err := second.recordingStorage.catalog.BeginMaintenance(t.Context(), generation+1); err == nil {
				token.Close()
				t.Fatal("startup recovery failed to reserve catalog before exposure")
			}
			if _, err := second.ManualCaptures().Create(t.Context(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "independent control", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked}); err != nil {
				t.Fatal(err)
			}
			close(release)
			released = true
			if mode == "mismatch" {
				select {
				case <-entered:
					t.Fatal("mismatched receipt opened observation writer")
				case <-time.After(50 * time.Millisecond):
				}
				state, err := second.storage.RecordingCleanupState(t.Context())
				if err != nil || state.ActiveRevision != target {
					t.Fatalf("mismatched claim acknowledged: %+v %v", state, err)
				}
				return
			}
			waitOwnedRecordingCleanup(t, second)
			waitRecording(t, second)
			state, err := second.storage.RecordingCleanupState(t.Context())
			if err != nil || state.ActiveRevision != 0 || state.CompletedRevision != target || state.SourceGeneration != generation {
				t.Fatalf("recovery changed active coordinates: %+v %v", state, err)
			}
			certificate, exists, err := runtimedata.ReadRecordingMaintenanceCertificate(root)
			if err != nil || !exists || certificate.CountsExact || !certificate.Released.CountsIncomplete {
				t.Fatalf("crash-gap count certificate: %+v %v %v", certificate, exists, err)
			}
		})
	}
}

func TestRuntimeCleanupRetainedWALFailureRecoversWithoutPrematureCompletion(t *testing.T) {
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	first, err := startWithBuilders(t.Context(), options, runtimeBuildOverrides{openObservations: func(ctx context.Context, o runtimepersistence.ObservationOptions) (*runtimepersistence.ObservationStore, error) {
		o.BusyTimeout = 50 * time.Millisecond
		return runtimepersistence.OpenObservations(ctx, o)
	}})
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			shutdownRuntime(t, first)
		}
	}()
	waitRecording(t, first)
	root := first.paths.DataDirectory()
	event := connectionevent.Event{ConnectionID: "retained-wal", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Now().UTC()}
	if err = first.recordingStorage.PutConnection(t.Context(), recording.Scope{Fence: recording.Fence{ArchiveEpoch: 1}}, event); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	held, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	var count int
	if err = held.QueryRow(`SELECT count(*) FROM runtime_connection_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if _, err = first.recordingStorage.ClearEvidence(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for !first.recordingStorage.unsafeMaintenance.Load() {
		if time.Now().After(deadline) {
			t.Fatal("retained WAL did not withhold physical completion")
		}
		time.Sleep(10 * time.Millisecond)
	}
	state, err := first.storage.RecordingCleanupState(t.Context())
	if err != nil || state.ActiveRevision == 0 || state.CompletedRevision == state.RequestedRevision {
		t.Fatalf("retained WAL falsely completed: %+v %v", state, err)
	}
	if _, exists, err := runtimedata.ReadRecordingCleanupReceipt(root); err != nil || !exists {
		t.Fatal("failed physical pass lost recovery receipt", err)
	}
	if _, err := first.ManualCaptures().Create(t.Context(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "control after optional failure", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked}); err != nil {
		t.Fatal(err)
	}
	if err = held.Rollback(); err != nil {
		t.Fatal(err)
	}
	shutdownRuntime(t, first)
	stopped = true
	second := startTestRuntime(t, options)
	defer shutdownRuntime(t, second)
	waitOwnedRecordingCleanup(t, second)
	waitRecording(t, second)
	recovered, err := second.storage.RecordingCleanupState(t.Context())
	if err != nil || recovered.ActiveRevision != 0 || recovered.CompletedRevision != state.ActiveRevision || recovered.SourceGeneration != state.SourceGeneration {
		t.Fatalf("failed pass did not recover same claim: %+v %v", recovered, err)
	}
	certificate, exists, err := runtimedata.ReadRecordingMaintenanceCertificate(root)
	if err != nil || !exists || certificate.CountsExact || !certificate.Released.CountsIncomplete {
		t.Fatalf("unknown crash counts certified exact: %+v %v", certificate, err)
	}
}

func TestRuntimeCleanupMaintainsFrozenArchivesAndPreservesProvenance(t *testing.T) {
	for _, revision := range []int{1, 2} {
		t.Run(fmt.Sprint(revision), func(t *testing.T) {
			options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
			root := options.Paths.DataDirectory()
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			ddl, err := os.ReadFile(fmt.Sprintf("../runtimepersistence/schema/v%d.sql", revision))
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", filepath.Join(root, "runtime.db"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(string(ddl)); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO runtime_metadata VALUES(1,'vibermate-runtime',?,'2026-10-03T00:00:00Z');INSERT INTO code_library_collections VALUES('preserved','Original');INSERT INTO capture_runs(run_id,proxy_capability_hash,control_capability_hash,cwd,canonical_executable_path,executable_label,client_catalog_revision,state,created_at_unix_ms,expires_at_unix_ms,updated_at_unix_ms) VALUES('legacy-owned',randomblob(32),randomblob(32),'/unique-private-legacy-cleanup-marker','/bin/tool','tool',1,'finished',10,20,20);INSERT INTO acp_observations(run_id,policy,revision,snapshot,final,expires_at_unix_ms) VALUES('legacy-owned','{"mode":"full","retentionDays":7}',1,X'010203',1,9999999999999)`, revision); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			runtime := startTestRuntime(t, options)
			defer shutdownRuntime(t, runtime)
			waitRecording(t, runtime)
			root = runtime.paths.DataDirectory()
			archive := runtime.recordingStorage.layout.Archives[0]
			provenance := filepath.Join(root, archive.RelativeDirectory, "archive-manifest.json")
			original, err := os.ReadFile(provenance)
			if err != nil {
				t.Fatal(err)
			}
			if receipt, err := runtime.recordingStorage.DeleteCapture(t.Context(), "managed_run", "legacy-owned"); err != nil || !receipt.CleanupPending {
				t.Fatalf("legacy required deletion: %+v %v", receipt, err)
			}
			deadline := time.Now().Add(inspectionFixtureBudget(10*time.Second, time.Minute))
			for {
				state, err := runtime.storage.RecordingCleanupState(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if state.RequestedRevision == state.CompletedRevision && state.ActiveRevision == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("archive cleanup pending: %+v %+v", state, runtime.Status())
				}
				time.Sleep(10 * time.Millisecond)
			}
			after, err := os.ReadFile(provenance)
			if err != nil || string(after) != string(original) {
				t.Fatal("archive provenance changed")
			}
			bytes, err := os.ReadFile(filepath.Join(root, archive.RelativeDirectory, "runtime.db"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(bytes), "unique-private-legacy-cleanup-marker") {
				t.Fatal("deleted archive bytes retained after compaction")
			}
			certificate, exists, err := runtimedata.ReadRecordingMaintenanceCertificate(root)
			if err != nil || !exists || len(certificate.Sources) != 2 || certificate.Sources[0].Database.Identity != "vibermate-runtime" || certificate.Sources[0].Database.Revision != int64(revision) || certificate.Sources[0].ProvenanceSHA256 == "" {
				t.Fatalf("archive/current certification: %+v %v", certificate, err)
			}
			db, err = sql.Open("sqlite", filepath.Join(root, archive.RelativeDirectory, "runtime.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var label string
			if err = db.QueryRow(`SELECT display_name FROM code_library_collections WHERE collection_id='preserved'`).Scan(&label); err != nil || label != "Original" {
				t.Fatal("shared configuration lost", err)
			}
		})
	}
}

func TestRuntimeRoutineExpiryPublishesCoalescedIntentWhileRecoveryHeld(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	released := false
	r, err := startWithBuilders(t.Context(), testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}), runtimeBuildOverrides{recoverEgress: func(context.Context, egressaudit.Repository, time.Time) error { close(entered); <-release; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if !released {
			close(release)
		}
		shutdownRuntime(t, r)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("actual optional recovery not held")
	}
	scheduler, ok := any(r.recordingStorage).(interface {
		requestRoutineRecordingCleanup(context.Context, time.Time) error
	})
	if !ok {
		t.Fatal("fixed-owner routine expiry publication unavailable")
	}
	due := r.clock.Now().UTC().Add(2 * time.Minute)
	if err = scheduler.requestRoutineRecordingCleanup(t.Context(), due); err != nil {
		t.Fatal(err)
	}
	first, err := r.storage.RecordingCleanupState(t.Context())
	if err != nil || first.RequestedRevision == 0 {
		t.Fatalf("routine expiry not durable: %+v %v", first, err)
	}
	if err = scheduler.requestRoutineRecordingCleanup(t.Context(), due); err != nil {
		t.Fatal(err)
	}
	second, err := r.storage.RecordingCleanupState(t.Context())
	if err != nil || second.RequestedRevision != first.RequestedRevision {
		t.Fatalf("same tick duplicated routine intent: %+v %v", second, err)
	}
	close(release)
	released = true
}

// Optional physical work has an eventual completion allowance under the race
// instrumented SQLite VM. Required callers above still execute while real I/O
// is held and cannot rely on this wait to release their blockers.
func waitOwnedRecordingCleanup(t *testing.T, r *Runtime) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		state, err := r.storage.RecordingCleanupState(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if state.ActiveRevision == 0 && state.CompletedRevision == state.RequestedRevision {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("owned cleanup did not settle: %+v status=%+v", state, r.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
