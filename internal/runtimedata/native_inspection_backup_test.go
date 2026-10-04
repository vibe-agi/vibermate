package runtimedata

import (
	"context"
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
	backup := filepath.Join(filepath.Dir(source), "native-backup")
	restored := filepath.Join(filepath.Dir(source), "native-restored")
	if err := Backup(ctx, source, backup, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := Restore(ctx, backup, restored); err != nil {
		t.Fatal(err)
	}
	store = openBackupStore(t, filepath.Join(restored, "runtime.db"))
	defer shutdownBackupStore(t, store)
	page, err := store.ConnectionEventRepository().List(ctx, connectionevent.PageRequest{Limit: 20})
	if err != nil || len(page.Items) != 1 || page.Items[0].InspectionPurpose != "client_tools" || page.Items[0].ClientEndpointID != "" || page.Items[0].ClientEndpointRevision != 0 {
		t.Fatalf("restored native purpose %+v %v", page, err)
	}
}
