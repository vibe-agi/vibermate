package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
)

func memorySchemaObjects(t *testing.T, statements ...string) []schemaObject {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, statement := range statements {
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	objects, err := schemaObjects(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return objects
}

// The observation role keeps revision 1 while its shape grows, so the guard is
// here: every shipped shape and step is frozen, and each older shape plus its
// steps must be exactly the shape a new file gets.
func TestObservationShapesAreFrozenAndUpgradeToTheCurrentShape(t *testing.T) {
	t.Parallel()
	for name, pin := range map[string]struct{ sql, want string }{
		"shape 1":    {observationSchemaV1SQL, "ed9fc1e5f71ca1801789d84488810df23872c0de520dea6360ad5573267899bd"},
		"steps 1->2": {observationMigration001To002SQL, pinnedObservationMigration001To002},
		"shape 2":    {observationSchemaSQL, pinnedObservationShape2},
	} {
		digest := sha256.Sum256([]byte(pin.sql))
		if hex.EncodeToString(digest[:]) != pin.want {
			t.Errorf("%s changed: %x. A shipped shape is immutable; add a new shape and step instead.", name, digest)
		}
	}
	current := memorySchemaObjects(t, observationSchemaSQL)
	for index, upgrade := range observationUpgrades {
		if !slices.Equal(memorySchemaObjects(t, upgrade.from, upgrade.steps), current) {
			t.Errorf("upgrade %d does not produce the current shape", index)
		}
		if slices.Equal(memorySchemaObjects(t, upgrade.from), current) {
			t.Errorf("upgrade %d starts from the current shape", index)
		}
	}
}

const upgradeLayout, upgradeArchive = "layout", "current"

func upgradeScope() recording.Scope {
	return recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "audit-a"}, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}
}

func upgradeOptions(path string) ObservationOptions {
	return ObservationOptions{DatabasePath: path, LayoutID: upgradeLayout, ArchiveID: upgradeArchive, BusyTimeout: time.Second, CommitReconcileTimeout: time.Second}
}

// writeShape1Observations leaves a real file in the first shipped shape, with
// one completed request recorded the way that build recorded it.
func writeShape1Observations(t *testing.T, path string, control *ControlStore) {
	t.Helper()
	store, err := openStore(context.Background(), Options{DatabasePath: path, BusyTimeout: time.Second, CommitReconcileTimeout: time.Second},
		&roleSchema{identity: observationSchemaIdentity, revision: 1, ddl: observationSchemaV1SQL, layout: upgradeLayout, archive: upgradeArchive})
	if err != nil {
		t.Fatal(err)
	}
	store.activityRepo.scripts = false
	old := &ObservationStore{store: store}
	seedCompleteInspectionMirror(t, old, control)
	record := inspectionActivity("before-upgrade", "exchange-before", activity.KindExchangeCompleted, time.Now().UTC().Truncate(time.Millisecond))
	if _, err = store.activityRepo.AppendScoped(context.Background(), upgradeScope(), record); err != nil {
		t.Fatal(err)
	}
	if err = old.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func fileSchemaObjects(t *testing.T, path string) []schemaObject {
	t.Helper()
	db := sql.OpenDB(newSQLiteReadConnector(path, time.Second))
	defer db.Close()
	objects, err := schemaObjects(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return objects
}

func storedScriptOutcomes(t *testing.T, path string) map[string]bool {
	t.Helper()
	db := sql.OpenDB(newSQLiteReadConnector(path, time.Second))
	defer db.Close()
	rows, err := db.Query(`SELECT activity_id, script_outcomes_json IS NOT NULL FROM runtime_activities`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var id string
		var has bool
		if err = rows.Scan(&id, &has); err != nil {
			t.Fatal(err)
		}
		result[id] = has
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestOpenObservationsUpgradesAnOlderFileInPlaceAndKeepsItsRows(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "observations.db")
	control := inspectionControlFixture(t)
	nativeExec(t, control.store.database, `INSERT INTO recording_capture_fences VALUES('managed_run','audit-a',1,0)`)
	writeShape1Observations(t, path, control)
	if !slices.Equal(fileSchemaObjects(t, path), memorySchemaObjects(t, observationSchemaV1SQL)) {
		t.Fatal("fixture is not the first shipped shape")
	}
	// A stopped directory in the older shape is a valid backup source.
	if err = observationRole(upgradeLayout, upgradeArchive).validateExisting(context.Background(), path, time.Second); err != nil {
		t.Fatal("older shape refused before open:", err)
	}

	upgraded, err := OpenObservations(context.Background(), upgradeOptions(path))
	if err != nil {
		t.Fatal("open of an older file:", err)
	}
	seedCompleteInspectionMirror(t, upgraded, control)
	after := inspectionActivity("after-upgrade", "exchange-after", activity.KindExchangeCompleted, time.Now().UTC().Truncate(time.Millisecond))
	after.Scripts = []activity.ScriptOutcome{{ScriptID: "transform.mask", Revision: 2, Name: "Mask secrets", Stage: activity.ScriptStageRequest, Outcome: activity.ScriptBlocked, BlockReason: "secret-detected", Applications: 1, BytesBefore: 40}}
	if _, err = upgraded.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), upgradeScope(), after); err != nil {
		t.Fatal("write after upgrade:", err)
	}
	if err = upgraded.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	shape := fileSchemaObjects(t, path)
	if !slices.Equal(shape, memorySchemaObjects(t, observationSchemaSQL)) {
		t.Fatal("upgraded file is not the current shape")
	}
	if got := storedScriptOutcomes(t, path); !reflect.DeepEqual(got, map[string]bool{"before-upgrade": false, "after-upgrade": true}) {
		t.Fatalf("rows after upgrade: %v", got)
	}
	var revision int64
	db := sql.OpenDB(newSQLiteReadConnector(path, time.Second))
	if err = db.QueryRow(`SELECT schema_revision FROM runtime_metadata`).Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("declared revision moved: %d %v", revision, err)
	}
	db.Close()

	// A second open has nothing left to do.
	again, err := OpenObservations(context.Background(), upgradeOptions(path))
	if err != nil {
		t.Fatal("second open:", err)
	}
	if err = again.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fileSchemaObjects(t, path), shape) || len(storedScriptOutcomes(t, path)) != 2 {
		t.Fatal("second open changed the file")
	}
}

func TestOpenObservationsRefusesAShapeItCannotUpgrade(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "observations.db")
	control := inspectionControlFixture(t)
	nativeExec(t, control.store.database, `INSERT INTO recording_capture_fences VALUES('managed_run','audit-a',1,0)`)
	writeShape1Observations(t, path, control)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE unexpected(value TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = OpenObservations(context.Background(), upgradeOptions(path)); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("unknown shape opened: %v", err)
	}
	if got := fileSchemaObjects(t, path); slices.Equal(got, memorySchemaObjects(t, observationSchemaSQL)) {
		t.Fatal("unknown shape was altered")
	}
	after, err := os.ReadFile(path)
	if err != nil || len(after) != len(before) {
		t.Fatalf("refused file changed size: %d -> %d %v", len(before), len(after), err)
	}
}

func TestActivityInspectionReturnsScriptOutcomes(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, inspectionActivity("legacy", "legacy-only", activity.KindExchangeCompleted, now))
	record := inspectionActivity("current", "current-only", activity.KindExchangeCompleted, now)
	record.Scripts = []activity.ScriptOutcome{
		{ScriptID: "transform.mask", Revision: 3, Name: "Mask secrets", Stage: activity.ScriptStageRequest, Outcome: activity.ScriptChanged, Applications: 1, Changed: 1, BodyChanged: true, BytesBefore: 120, BytesAfter: 96},
		{ScriptID: "transform.guard", Revision: 1, Name: "Guard", Stage: activity.ScriptStageResponse, Outcome: activity.ScriptUnchanged, Applications: 14, BytesBefore: 900, BytesAfter: 900},
	}
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, record); err != nil {
		t.Fatal(err)
	}
	reader := newActivityInspectionTestReader(t, f)
	got, err := reader.GetExchange(context.Background(), "current-only")
	if err != nil || !reflect.DeepEqual(got.Scripts, record.Scripts) {
		t.Fatalf("current record: %+v %v", got.Scripts, err)
	}
	// An archive from before script outcomes existed simply has none.
	old, err := reader.GetExchange(context.Background(), "legacy-only")
	if err != nil || old.Scripts != nil {
		t.Fatalf("archived record: %+v %v", old.Scripts, err)
	}
}

const (
	pinnedObservationMigration001To002 = "007e50604de0f85d06ec063fd6312dee7de6fc53270e08de84f080efb32b24b4"
	pinnedObservationShape2            = "b34702b0bb35afe34977005c2ed053f66c86dbc3a5f7deb3423d751b10093094"
)
