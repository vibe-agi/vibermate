package runtimepersistence

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
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

func TestRecordingCleanupClaimPreservesNewerWantedWork(t *testing.T) {
	control := inspectionControlFixture(t)
	api, ok := any(control).(interface {
		RequestRecordingCleanup(context.Context, time.Time) error
		ClaimRecordingCleanup(context.Context) (uint64, uint64, int64, error)
		CompleteRecordingCleanup(context.Context, uint64, uint64) error
	})
	if !ok {
		t.Fatal("resumable coalesced cleanup claim unavailable")
	}
	now := time.Now().UTC()
	if err := api.RequestRecordingCleanup(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	target, generation, cutoff, err := api.ClaimRecordingCleanup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if target != 1 || generation != 2 || cutoff != now.UnixMilli() {
		t.Fatalf("claim coordinates: %d/%d/%d", target, generation, cutoff)
	}
	if err = api.RequestRecordingCleanup(t.Context(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	again, nextGeneration, nextCutoff, err := api.ClaimRecordingCleanup(t.Context())
	if err != nil || again != target || nextGeneration != generation || nextCutoff != cutoff {
		t.Fatal("unfinished owner was overtaken")
	}
	if err = api.CompleteRecordingCleanup(t.Context(), target, generation+1); err == nil {
		t.Fatal("wrong generation completed cleanup")
	}
	if err = api.CompleteRecordingCleanup(t.Context(), target, generation); err != nil {
		t.Fatal(err)
	}
	state, err := control.RecordingCleanupState(t.Context())
	if err != nil || state.CompletedRevision != 1 || state.RequestedRevision != 2 {
		t.Fatalf("new work falsely acknowledged: %+v %v", state, err)
	}
	target, generation, cutoff, err = api.ClaimRecordingCleanup(t.Context())
	if err != nil || target != 2 || generation != 3 || cutoff != now.Add(time.Second).UnixMilli() {
		t.Fatalf("next owner lost wanted work: %d/%d/%d %v", target, generation, cutoff, err)
	}
}

func TestRecordingMissingCleanupStateCannotAcknowledgeIntent(t *testing.T) {
	control := inspectionControlFixture(t)
	nativeExec(t, control.store.database, `DELETE FROM recording_cleanup_state`)
	if err := control.RequestRecordingCleanup(t.Context(), time.Now().UTC()); err == nil {
		t.Fatal("missing reliable obligation acknowledged as published")
	}
	policy, err := control.UsagePolicyRepository().UsagePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	policy.RetentionDays = 7
	if _, err = control.UsagePolicyRepository().SetUsagePolicy(t.Context(), policy, time.Now().UTC()); err == nil {
		t.Fatal("policy change lost its cleanup obligation")
	}
	retained, err := control.UsagePolicyRepository().UsagePolicy(t.Context())
	if err != nil || retained.Revision != policy.Revision {
		t.Fatal("failed intent partially committed policy")
	}
}

func TestRecordingRequiredClearCommitsWhileHistoryHeld(t *testing.T) {
	control := inspectionControlFixture(t)
	_, current := inspectionSourcesFixture(t)
	seedOwnedApprovalCaptures(t, control)
	if err := control.ToolApprovalRepository().Create(t.Context(), ownedApproval(t, "owned-clear", "doomed")); err != nil {
		t.Fatal(err)
	}
	_ = pendingNetworkAsk(t, control.store)
	before, err := control.UsagePolicyRepository().UsagePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := current.store.database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO capture_observations VALUES('manual_capture','doomed',1,7,1,1000,2000)`); err != nil {
		t.Fatal(err)
	}
	clearer, ok := any(control).(interface {
		ClearRecordingArchive(context.Context) (resourcedeletion.Released, uint64, error)
	})
	if !ok {
		t.Fatal("required split archive clear unavailable")
	}
	deadline, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	released, epoch, err := clearer.ClearRecordingArchive(deadline)
	if err != nil || epoch != 2 || released.Captures != 2 || released.Approvals != 2 {
		t.Fatalf("required clear: %+v epoch=%d %v", released, epoch, err)
	}
	state, err := control.RecordingCleanupState(deadline)
	if err != nil || state.RequestedRevision != 1 || state.CompletedRevision != 0 {
		t.Fatalf("clear omitted durable pending work: %+v %v", state, err)
	}
	after, err := control.UsagePolicyRepository().UsagePolicy(deadline)
	if err != nil || after.Enabled != before.Enabled || after.RetentionDays != before.RetentionDays || after.Revision != before.Revision+1 {
		t.Fatalf("clear changed config or missed original interval: %+v %v", after, err)
	}
	nativeExec(t, control.store.database, `INSERT INTO recording_capture_fences VALUES('manual_capture','new-after-clear',1,0)`)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = current.PublishInspectionMirror(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	var denied int
	if err = current.store.database.QueryRow(`SELECT deleted FROM inspection_mirror_fences WHERE mirror_generation=(SELECT generation FROM inspection_mirror_active) AND capture_id='doomed'`).Scan(&denied); err != nil || denied != 1 {
		t.Fatal("clear forgot captured tombstone", err)
	}
}

func TestRecordingRequiredClearRollsBackWithoutReliableIntent(t *testing.T) {
	control := inspectionControlFixture(t)
	seedOwnedApprovalCaptures(t, control)
	if err := control.ToolApprovalRepository().Create(t.Context(), ownedApproval(t, "not-cleared", "doomed")); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, control.store.database, `DELETE FROM recording_cleanup_state`)
	released, epoch, err := control.ClearRecordingArchive(t.Context())
	if err == nil || epoch != 0 || released != (resourcedeletion.Released{}) {
		t.Fatalf("missing reliable intent reported clear: %+v %d %v", released, epoch, err)
	}
	snapshot, err := control.ReadInspectionSnapshot(t.Context(), nil)
	if err != nil || snapshot.Stamp.ArchiveEpoch != 1 {
		t.Fatalf("failed clear advanced epoch: %+v %v", snapshot, err)
	}
	if _, err = control.ToolApprovalRepository().Get(t.Context(), "not-cleared"); err != nil {
		t.Fatal("failed clear removed approval", err)
	}
	var captures, deleted int
	if err = control.store.database.QueryRow(`SELECT (SELECT count(*) FROM manual_captures),(SELECT count(*) FROM recording_capture_fences WHERE deleted=1)`).Scan(&captures, &deleted); err != nil || captures != 2 || deleted != 0 {
		t.Fatalf("failed clear altered ownership: %d %d %v", captures, deleted, err)
	}
}

func TestRecordingRequiredClearRetainsNativeReplayAndConfiguration(t *testing.T) {
	control := inspectionControlFixture(t)
	_, id, revision := nativeFixtureOnStore(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, true, nil, control.store, egressprofile.Direct())
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, control.store.NativeToolRepository(), id, now)
	if _, err := control.NativeToolRepository().Start(t.Context(), id, "clear-native-once", revision, now); err != nil {
		t.Fatal(err)
	}
	if err := control.NativeToolRepository().Finish(t.Context(), id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	var beforeOnce, beforeConfig int
	if err := control.store.database.QueryRow(`SELECT (SELECT count(*) FROM native_tool_once),(SELECT count(*) FROM environment_revisions)`).Scan(&beforeOnce, &beforeConfig); err != nil || beforeOnce == 0 || beforeConfig == 0 {
		t.Fatal("fixture lacks required native/config facts", err)
	}
	if _, _, err := control.ClearRecordingArchive(t.Context()); err != nil {
		t.Fatal(err)
	}
	var afterOnce, afterConfig, closed int
	if err := control.store.database.QueryRow(`SELECT (SELECT count(*) FROM native_tool_once),(SELECT count(*) FROM environment_revisions),(SELECT count(*) FROM native_tool_scopes WHERE state='closed' AND replay_barrier=1)`).Scan(&afterOnce, &afterConfig, &closed); err != nil || afterOnce != beforeOnce || afterConfig != beforeConfig || closed == 0 {
		t.Fatalf("clear changed reliable safety/config: once=%d/%d config=%d/%d closed=%d %v", afterOnce, beforeOnce, afterConfig, beforeConfig, closed, err)
	}
}
