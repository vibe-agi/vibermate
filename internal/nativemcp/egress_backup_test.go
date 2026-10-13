package nativemcp

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func TestNativeEffectiveBindingAndAuditSurviveActualBackupRestore(t *testing.T) {
	f := newNativeFixtureWithChoice(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, 0, egressprofile.Inherit())
	profiles, err := egressprofile.NewManager(f.store.EgressProfileRepository(), effectiveBudgetClock{})
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := egressprofile.NewDefaultManager(f.store.EgressDefaultRepository(), profiles)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := defaults.SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: egressprofile.DirectID, Revision: 1}}); err != nil {
		t.Fatal(err)
	}
	f.executor.(*executor).dependencies.EgressDefaults = defaults
	f.handshake(t)
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
		return wireResponse(`{"jsonrpc":"2.0","id":3,"result":{}}`, false), nil
	}
	terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":3,"method":"ping"}`, "synthetic-session")
	if terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
	if err := f.executor.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	op, err := f.store.NativeToolRepository().Get(t.Context(), terminal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.store.EgressAttemptRepository().List(t.Context(), egressaudit.PageRequest{Limit: 20})
	if err != nil || len(page.Items) != 4 {
		t.Fatalf("real audit fixture %+v %v", page, err)
	}
	var bindingDigest, profileJSON, source string
	var version, revision uint64
	if err := f.db.QueryRow(`SELECT hex(binding_digest),profile_json,selection_source,binding_version,default_revision FROM native_tool_binding_egress WHERE operation_id=?`, terminal.OperationID).Scan(&bindingDigest, &profileJSON, &source, &version, &revision); err != nil {
		t.Fatal(err)
	}
	var sequence int
	var databaseName, databasePath string
	if err := f.db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &databaseName, &databasePath); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	sourceDir, err := filepath.EvalSymlinks(filepath.Dir(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	// Existing backup admission requires the local-CA file family. These are
	// inert private test markers, not a generated/installed/trusted identity.
	caDir := filepath.Join(sourceDir, "local-ca")
	if err := os.MkdirAll(caDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"root-certificate.pem", "root-key.pem", "root-manifest.json"} {
		if err := os.WriteFile(filepath.Join(caDir, name), []byte("synthetic-backup-marker-only"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backup, restored := filepath.Join(root, "backup"), filepath.Join(root, "restored")
	if err := runtimedata.Backup(t.Context(), sourceDir, backup, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := runtimedata.Restore(t.Context(), backup, restored); err != nil {
		t.Fatal(err)
	}
	store, err := runtimepersistence.Open(t.Context(), runtimepersistence.Options{DatabasePath: filepath.Join(restored, "runtime.db"), BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	actual, err := store.NativeToolRepository().Get(t.Context(), terminal.OperationID)
	if err != nil || actual.BindingDigest == nil || op.BindingDigest == nil || *actual.BindingDigest != *op.BindingDigest || actual.State != op.State {
		t.Fatalf("backup changed actual native projection %+v %v", actual, err)
	}
	restoredPage, err := store.EgressAttemptRepository().List(t.Context(), egressaudit.PageRequest{Limit: 20})
	if err != nil || len(restoredPage.Items) != len(page.Items) {
		t.Fatal("backup lost native audits", err)
	}
	for i := range page.Items {
		if restoredPage.Items[i].Attempt != page.Items[i].Attempt {
			t.Fatal("backup changed physical audit frozen source/bytes/state")
		}
	}
	// Actual Get above re-verifies the restored V2 digest against the profile
	// row; additionally prove the selection is the exact prior global revision.
	if bindingDigest == "" || profileJSON == "" || version != 2 || source != "global" || revision != 2 {
		t.Fatal("fixture lacked positive effective provenance")
	}
	setting, err := store.EgressDefaultRepository().LoadDefault(t.Context())
	if err != nil || setting.Revision != 2 {
		t.Fatal("backup changed authored default setting", err)
	}
}
