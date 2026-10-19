package runtimepersistence

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/recording"
	"path/filepath"
	"testing"
	"time"
)

func TestCurrentControlValidationUsesActualNativeClosure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	epoch, _ := recording.NewEpoch(1)
	control, err := OpenControl(ctx, ControlOptions{DatabasePath: path, LayoutID: "current-proof", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second, RecordingScopes: nativeTestScopes{epoch}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { control.Shutdown(ctx) })
	_, id, revision := nativeFixtureOnStore(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, true, nil, control.store, egressprofile.Direct())
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, control.store.NativeToolRepository(), id, now)
	if _, err = control.NativeToolRepository().Start(ctx, id, "actual-current-proof", revision, now); err != nil {
		t.Fatal(err)
	}
	if _, err = ValidateCurrentControl(ctx, path, "current-proof"); err != nil {
		t.Fatalf("valid current control requires optional history: %v", err)
	}
	// Current authority revisions may legitimately advance after a historical
	// Start. The immutable delivery connection must still match that Start.
	var connection string
	if err = control.store.database.QueryRow(`SELECT connection_id FROM native_tool_deliveries WHERE operation_id=?`, id).Scan(&connection); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, control.store.database, `UPDATE native_tool_deliveries SET connection_id='foreign' WHERE operation_id=?`, id)
	if _, err = ValidateCurrentControl(ctx, path, "current-proof"); err == nil {
		t.Fatal("broken current native delivery accepted")
	}
	nativeExec(t, control.store.database, `UPDATE native_tool_deliveries SET connection_id=? WHERE operation_id=?`, connection, id)
	if _, err = ValidateCurrentControl(ctx, path, "current-proof"); err != nil {
		t.Fatalf("restored current authority rejected: %v", err)
	}
	nativeExec(t, control.store.database, `UPDATE native_tool_once SET authority_revision=authority_revision+1 WHERE operation_id=?`, id)
	if _, err = ValidateCurrentControl(ctx, path, "current-proof"); err == nil {
		t.Fatal("future scope revision claim accepted")
	}
	nativeExec(t, control.store.database, `UPDATE native_tool_once SET authority_revision=authority_revision-1 WHERE operation_id=?`, id)
	nativeExec(t, control.store.database, `UPDATE native_tool_scopes SET revision=revision+1`)
	if _, err = ValidateCurrentControl(ctx, path, "current-proof"); err != nil {
		t.Fatalf("valid earlier claim rejected after current scope advanced: %v", err)
	}
}
