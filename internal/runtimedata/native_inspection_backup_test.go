package runtimedata

import (
	"context"
	"database/sql"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeInspectionPurposeSurvivesActualBackupRestore(t *testing.T) {
	ctx := context.Background()
	source, _ := backupFixture(t)
	store := openBackupStore(t, filepath.Join(source, "runtime.db"))
	event := connectionevent.Event{InspectionPurpose: connectionevent.InspectionPurposeClientTools, ConnectionID: "native-backup", IngressID: "capture-run/native-synthetic", SourceLabel: "Synthetic", SourceConfidence: connectionevent.SourceConfidenceConfigured, EnvironmentID: "native", EnvironmentName: "Native", EnvironmentRevision: 1, RequestedHost: "native.example", RouteHost: "native.example", Port: 443, Decision: connectionevent.DecisionAllow, RuleID: "native", EgressScope: connectionevent.EgressScopeEnvironment, EgressSource: connectionevent.EgressSourceEnvironmentDefault, EgressPolicyRevision: 1, Decryption: connectionevent.DecryptionMITM, Phase: connectionevent.PhaseDecided, StartedAt: time.Now().UTC()}
	if _, err := store.ConnectionEventRepository().Append(ctx, event); err != nil {
		t.Fatal(err)
	}
	shutdownBackupStore(t, store)
	freezePublishedBackupFixture(t, source)
	backup := filepath.Join(filepath.Dir(source), "native-backup")
	restored := filepath.Join(filepath.Dir(source), "native-restored")
	if err := Backup(ctx, source, backup, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := Restore(ctx, backup, restored); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+backupHistoryPath(t, restored)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var purpose, endpoint string
	var revision int64
	if err = db.QueryRow(`SELECT inspection_purpose,client_endpoint_id,client_endpoint_revision FROM runtime_connection_events WHERE connection_id='native-backup'`).Scan(&purpose, &endpoint, &revision); err != nil || purpose != "client_tools" || endpoint != "" || revision != 0 {
		t.Fatalf("restored native purpose %s %s %d %v", purpose, endpoint, revision, err)
	}
}
