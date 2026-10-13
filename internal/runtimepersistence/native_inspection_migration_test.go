package runtimepersistence

import (
	"context"
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestNativeInspectionMigrationPreservesAllLegacyRows(t *testing.T) {
	f := populatedV1MigrationFixture(t)
	result, err := UpgradeRevision1(context.Background(), f.input(t))
	if err != nil || !result.Committed {
		t.Fatalf("migration %+v %v", result, err)
	}
	f.assertLegacyPreserved(t)
	db := openSQLiteForTest(t, filepath.Join(f.source, "runtime.db"))
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM runtime_connection_events WHERE inspection_purpose<>''`).Scan(&count); err != nil {
		t.Fatalf("migrated audit cannot preserve explicit native inspection purpose: %v", err)
	}
	if count != 0 {
		t.Fatal("migration fabricated native inspection purpose for legacy rows")
	}
	page, err := newConnectionEventRepository(db, newOperationGate()).List(context.Background(), connectionevent.PageRequest{Limit: 20})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("migrated legacy audit reader %+v %v", page, err)
	}
	for _, record := range page.Items {
		if record.InspectionPurpose != "" {
			t.Fatal("legacy audit inferred native purpose")
		}
	}
}

func TestNativeInspectionAuditReloadRetirementAndJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, path)
	event := connectionevent.Event{InspectionPurpose: connectionevent.InspectionPurposeClientTools, ConnectionID: "native-inspection", IngressID: "capture-run/native-synthetic", SourceLabel: "Synthetic", SourceConfidence: connectionevent.SourceConfidenceConfigured, EnvironmentID: "native.synthetic", EnvironmentName: "Native", EnvironmentRevision: 1, RequestedHost: "native.example", RouteHost: "native.example", Port: 443, Decision: connectionevent.DecisionAllow, RuleID: "native.synthetic", EgressScope: connectionevent.EgressScopeEnvironment, EgressSource: connectionevent.EgressSourceEnvironmentDefault, EgressPolicyRevision: 1, Decryption: connectionevent.DecryptionMITM, Phase: connectionevent.PhaseDecided, StartedAt: time.Now().UTC().Truncate(time.Millisecond)}
	inserted, err := store.ConnectionEventRepository().Append(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	shutdownTestStore(t, store)
	store = openTestStore(t, path)
	defer shutdownTestStore(t, store)
	page, err := store.ConnectionEventRepository().List(context.Background(), connectionevent.PageRequest{Limit: 20})
	if err != nil || len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0], inserted) {
		t.Fatalf("reloaded purpose %+v %v", page, err)
	}
	encoded, err := json.Marshal(page.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	var decoded connectionevent.Record
	if err = json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(decoded, page.Items[0]) {
		t.Fatal("native inspection JSON roundtrip failed")
	}
	count, err := store.ConnectionEventRepository().Recover(context.Background(), event.StartedAt.Add(time.Second))
	if err != nil || count != 1 {
		t.Fatalf("native audit retirement %d %v", count, err)
	}
	page, err = store.ConnectionEventRepository().List(context.Background(), connectionevent.PageRequest{Limit: 20, LatestPerConnection: true})
	if err != nil || len(page.Items) != 1 || page.Items[0].InspectionPurpose != "client_tools" || page.Items[0].Phase != connectionevent.PhaseFailed || page.Items[0].ClientEndpointID != "" || page.Items[0].ClientEndpointRevision != 0 {
		t.Fatalf("retirement lost native purpose %+v %v", page, err)
	}
}
