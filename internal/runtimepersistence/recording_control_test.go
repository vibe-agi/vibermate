package runtimepersistence

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

func TestRecordingDeleteFencesRealLateCommitWhileHistoryHeld(t *testing.T) {
	control := inspectionControlFixture(t)
	options, current := inspectionSourcesFixture(t)
	nativeExec(t, control.store.database, `INSERT INTO manual_captures(capture_id,owner_kind,owner_id,display_name,client_class,lifetime,state,credential_revision,proxy_credential_hash,observation,created_at_unix_ms,updated_at_unix_ms) VALUES('doomed','local_installation','','Desktop','desktop_app','until_revoked','revoked',1,randomblob(32),'waiting_for_traffic',1,2); INSERT INTO recording_capture_fences VALUES('manual_capture','doomed',7,0)`)
	if err := current.PublishInspectionMirror(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	tx, err := current.store.database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO capture_observations VALUES('manual_capture','doomed',1,7,1,1000,2000)`); err != nil {
		t.Fatal(err)
	}
	deletion, ok := any(control).(interface {
		DeleteCapture(context.Context, string, string) (resourcedeletion.Released, error)
	})
	if !ok {
		t.Fatal("split control deletion unavailable")
	}
	deadline, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	released, err := deletion.DeleteCapture(deadline, "manual_capture", "doomed")
	if err != nil {
		t.Fatal("history transaction blocked reliable deletion:", err)
	}
	if released.Captures != 1 {
		t.Fatalf("authority removal not verified: %+v", released)
	}
	// Real independent control work still commits before history returns.
	if _, err = control.store.database.ExecContext(deadline, `INSERT INTO recording_capture_fences VALUES('manual_capture','new',1,0)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(context.Background())
	reader := NewCaptureInspectionReader(catalog, control)
	view, err := reader.ReadCaptureObservation(t.Context(), captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: "doomed"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Selection.Visible || view.FirstObservedAt != nil || view.LastObservedAt != nil {
		t.Fatal("late observation disclosed after durable deletion")
	}
	var pending int
	if err = control.store.reads.QueryRow(`SELECT requested_revision>completed_revision FROM recording_cleanup_state WHERE singleton=1`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatal("late-write cleanup obligation missing")
	}
	if err = current.PublishInspectionMirror(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	var deleted, generation int
	if err = current.store.reads.QueryRow(`SELECT deleted,capture_generation FROM inspection_mirror_fences WHERE mirror_generation=(SELECT generation FROM inspection_mirror_active) AND capture_id='doomed'`).Scan(&deleted, &generation); err != nil {
		t.Fatal(err)
	}
	if deleted != 1 || generation != 7 {
		t.Fatal("original scope was relabeled instead of fenced")
	}
}

func TestRecordingDeletedScopeRemainsHiddenAfterBothRolesRestart(t *testing.T) {
	control := inspectionControlFixture(t)
	options, current := inspectionSourcesFixture(t)
	nativeExec(t, control.store.database, `INSERT INTO manual_captures(capture_id,owner_kind,owner_id,display_name,client_class,lifetime,state,credential_revision,proxy_credential_hash,observation,created_at_unix_ms,updated_at_unix_ms) VALUES('doomed','local_installation','','Desktop','desktop_app','until_revoked','revoked',1,randomblob(32),'waiting_for_traffic',1,2); INSERT INTO recording_capture_fences VALUES('manual_capture','doomed',7,0)`)
	nativeExec(t, current.store.database, `INSERT INTO capture_observations VALUES('manual_capture','doomed',1,7,1,1000,2000)`)
	if _, err := control.DeleteCapture(t.Context(), "manual_capture", "doomed"); err != nil {
		t.Fatal(err)
	}
	controlPath := control.store.databasePath
	if err := control.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := current.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	control, err := OpenControl(t.Context(), ControlOptions{DatabasePath: controlPath, LayoutID: "layout", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Shutdown(context.Background())
	current, err = OpenObservations(t.Context(), ObservationOptions{DatabasePath: filepath.Join(options.Root, "observations.db"), LayoutID: "layout", ArchiveID: "current", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer current.Shutdown(context.Background())
	if err = current.PublishInspectionMirror(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(context.Background())
	view, err := NewCaptureInspectionReader(catalog, control).ReadCaptureObservation(t.Context(), captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: "doomed"})
	if err != nil || view.Selection.Visible || view.FirstObservedAt != nil {
		t.Fatalf("deleted scope revived on restart: %+v %v", view, err)
	}
	state, err := control.RecordingCleanupState(t.Context())
	if err != nil || state.RequestedRevision != 1 || state.CompletedRevision != 0 {
		t.Fatalf("lost pending cleanup: %+v %v", state, err)
	}
}
