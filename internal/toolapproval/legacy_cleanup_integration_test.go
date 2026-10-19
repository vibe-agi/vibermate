package toolapproval_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func TestLegacyApprovalCleanupPublishesOnlyDurableExactRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	control, err := runtimepersistence.OpenControl(t.Context(), runtimepersistence.ControlOptions{DatabasePath: path, LayoutID: "layout", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Shutdown(context.Background())
	authority, err := toolapproval.New(t.Context(), toolapproval.Options{Repository: control.ToolApprovalRepository(), Clock: toolapproval.SystemClock{}, Random: rand.Reader, Config: toolapproval.Config{DecisionTimeout: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownAuthority(t, authority)
	remover, ok := any(authority).(toolapproval.LegacyApprovalRemover)
	if !ok {
		t.Fatal("actual approval owner cannot settle proven legacy removal")
	}
	request, err := decisionRequest(t, `{"path":"fixture"}`).WithOriginalScope(recording.Scope{Fence: recording.Fence{ArchiveEpoch: 1}})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan exchange.ToolDecision, 1)
	failure := make(chan error, 1)
	go func() {
		decision, err := authority.Decide(context.Background(), request)
		result <- decision
		failure <- err
	}()
	pending := waitForPending(t, authority)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// An already-extracted unknown question is not creatable through the new
	// required API. Preserve the real admitted waiter while modeling that row.
	if _, err = db.Exec(`UPDATE tool_approvals SET owner_kind='unknown',owner_archive_epoch=0 WHERE approval_id=?`, pending.ID); err != nil {
		t.Fatal(err)
	}
	expected, err := control.ToolApprovalRepository().Get(t.Context(), pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope := recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "removed-owner"}, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}
	if _, err = remover.RemoveLegacyApproval(t.Context(), expected, scope); err == nil {
		t.Fatal("missing tombstone authorized removal")
	}
	select {
	case <-result:
		t.Fatal("failed required removal canceled waiter")
	default:
	}
	if _, err = db.Exec(`INSERT INTO recording_capture_fences VALUES('managed_run','removed-owner',1,1)`); err != nil {
		t.Fatal(err)
	}
	if removed, err := remover.RemoveLegacyApproval(t.Context(), expected, scope); err != nil || !removed {
		t.Fatalf("removal=%v err=%v", removed, err)
	}
	select {
	case decision := <-result:
		if decision.Outcome == exchange.ToolDecisionApproved {
			t.Fatal("removed approval published allow")
		}
	case <-time.After(time.Second):
		t.Fatal("removed approval waiter abandoned")
	}
	if err := <-failure; err != nil {
		t.Fatal(err)
	}
}
