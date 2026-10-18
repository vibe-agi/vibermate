package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type replayAdmissionFixture struct {
	current *ObservationStore
	control *ControlStore
	scope   recording.Scope
	write   func() error
}

func newReplayAdmissionFixture(t *testing.T, domain string) replayAdmissionFixture {
	t.Helper()
	var f replayAdmissionFixture
	switch domain {
	case "native-history":
		current, value := nativeIndependentHistoryFixture(t)
		f.current = current
		f.scope = value.Scope
		f.write = func() error { return current.PutNativeHistory(t.Context(), value) }
		var err error
		f.control, err = OpenControl(t.Context(), ControlOptions{DatabasePath: filepath.Join(filepath.Dir(current.store.databasePath), "control.db"), LayoutID: "history-layout", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
	case "native-content":
		current, value := nativeRetiredContentFixture(t)
		f.current = current
		f.scope = value.Identity.Scope
		f.write = func() error { return current.PutNativeContent(t.Context(), value) }
		var err error
		f.control, err = OpenControl(t.Context(), ControlOptions{DatabasePath: filepath.Join(filepath.Dir(current.store.databasePath), "control.db"), LayoutID: "content-layout", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
	default:
		f.control = inspectionControlFixture(t)
		_, f.current = inspectionSourcesFixture(t)
		f.scope = recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "replay-owner"}, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 7}}
		nativeExec(t, f.control.store.database, `INSERT INTO recording_capture_fences VALUES('managed_run','replay-owner',7,0)`)
		if domain == "egress" {
			start := providerAttempt(t, "recorded-terminal")
			terminal, err := start.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, BytesIn: 1, BytesOut: 2, CompletedAt: start.StartedAt().Add(time.Second)})
			if err != nil {
				t.Fatal(err)
			}
			f.write = func() error {
				return f.current.EgressAttemptStore().(egressaudit.ScopedAttemptStore).PutScopedAttempt(t.Context(), f.scope, terminal)
			}
		} else {
			value := acpobservation.Record{RunID: f.scope.Capture.ID, Scope: f.scope, Policy: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingMetadataOnly, RetentionDays: 30}, ExpiresAtMillis: time.Now().Add(time.Hour).UnixMilli(), Snapshot: acpobservation.Snapshot{Revision: 1}}
			f.write = func() error { return f.current.PutACP(t.Context(), value) }
		}
	}
	t.Cleanup(func() { f.control.Shutdown(context.Background()) })
	if err := f.current.DenyRecordingIdentities(t.Context(), f.control, "usage", []string{"proof-first", "proof-last"}); err != nil {
		t.Fatal(err)
	}
	if err := f.current.PublishInspectionMirror(t.Context(), f.control); err != nil {
		t.Fatal(err)
	}
	if err := f.write(); err != nil {
		t.Fatal("initial admitted write:", err)
	}
	if err := f.write(); err != nil {
		t.Fatal("healthy exact retry:", err)
	}
	return f
}
func TestRecordingExactReplayChecksCurrentAdmission(t *testing.T) {
	for _, domain := range []string{"egress", "native-history", "native-content", "acp"} {
		for _, fault := range []string{"tombstone", "foreign-commit"} {
			t.Run(domain+"/"+fault, func(t *testing.T) {
				f := newReplayAdmissionFixture(t, domain)
				if fault == "tombstone" {
					nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_kind=? AND capture_id=?`, f.scope.Capture.Kind, f.scope.Capture.ID)
					nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
					if err := f.current.PublishInspectionMirror(t.Context(), f.control); err != nil {
						t.Fatal(err)
					}
				} else {
					external, err := sql.Open("sqlite", f.current.store.databasePath)
					if err != nil {
						t.Fatal(err)
					}
					defer external.Close()
					nativeExec(t, external, `UPDATE recording_denials SET identity='corrupt' WHERE sequence=1`)
				}
				if err := f.write(); !errors.Is(err, recording.ErrObservationDiscarded) {
					t.Fatalf("inadmissible exact retry reported persisted success: %v", err)
				}
			})
		}
	}
}
