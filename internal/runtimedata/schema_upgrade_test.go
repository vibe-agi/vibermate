package runtimedata

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestSchemaSwitchBackupFailureNoDDL(t *testing.T) {
	source := frozenUpgradeFixture(t)
	owner, err := Acquire(source)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	host, err := instanceguard.Acquire(filepath.Join(source, "server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Release()
	before := snapshotTestFiles(t, source)
	runner := newSchemaUpgradeRunner()
	runner.create = func(context.Context, string, string, *instanceguard.Guard, *instanceguard.Guard, time.Time) error {
		return ErrCopy
	}
	_, err = runner.upgrade(context.Background(), source, filepath.Join(filepath.Dir(source), "new-snapshot"), owner, host, time.Now().UTC(), time.Second, nil)
	if !errors.Is(err, ErrSchemaUpgradeIncomplete) {
		t.Fatalf("backup failure: %v", err)
	}
	after := snapshotTestFiles(t, source)
	for name, encoded := range before {
		if string(after[name]) != string(encoded) {
			t.Fatal("backup failure changed source")
		}
	}
}

// Build stopped genuine v1 with all old typed columns from independently
// populated current fixtures. No active Store is ever allowed to open v1.
func frozenUpgradeFixture(t *testing.T) string {
	t.Helper()
	source, _ := backupFixture(t)
	seedSnapshotAuthority(t, source)
	frozen, err := os.ReadFile("../runtimepersistence/schema/v1.sql")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(source), "frozen.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(string(frozen)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ATTACH DATABASE ? AS old`, filepath.Join(source, "runtime.db")); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT name FROM main.sqlite_schema WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	rows.Close()
	for _, table := range tables {
		rows, err := db.Query(fmt.Sprintf(`PRAGMA main.table_info(%q)`, table))
		if err != nil {
			t.Fatal(err)
		}
		var columns []string
		for rows.Next() {
			var cid, notnull, pk int
			var name, kind string
			var def any
			if err := rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
				t.Fatal(err)
			}
			columns = append(columns, fmt.Sprintf("%q", name))
		}
		rows.Close()
		cols := strings.Join(columns, ",")
		// Seed defaults and AUTOINCREMENT rows must be replaced with the actual old fixture.
		if _, err := db.Exec(fmt.Sprintf(`DELETE FROM main.%q; INSERT INTO main.%q(%s) SELECT %s FROM old.%q`, table, table, cols, cols, table)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE runtime_metadata SET schema_revision=1; DETACH DATABASE old`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(source, "runtime.db")); err != nil {
		t.Fatal(err)
	}
	state, err := runtimepersistence.InspectSchema(context.Background(), filepath.Join(source, "runtime.db"), time.Second)
	if err != nil || state.Revision != 1 {
		t.Fatalf("frozen fixture=%+v %v", state, err)
	}
	return source
}
func upgradeGuards(t *testing.T, source string) (*instanceguard.Guard, *instanceguard.Guard) {
	t.Helper()
	owner, err := Acquire(source)
	if err != nil {
		t.Fatal(err)
	}
	host, err := instanceguard.Acquire(filepath.Join(source, "server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Release(); owner.Release() })
	return owner, host
}
func TestSchemaSwitchPendingReceiptSurvivesCrash(t *testing.T) {
	source := frozenUpgradeFixture(t)
	owner, host := upgradeGuards(t, source)
	before := snapshotTestRows(t, source)
	private := snapshotTestFiles(t, source)
	target := filepath.Join(filepath.Dir(source), "private-snapshot")
	runner := newSchemaUpgradeRunner()
	runner.upgradeDatabase = func(ctx context.Context, input runtimepersistence.MigrationInput) (runtimepersistence.MigrationResult, error) {
		result, err := runtimepersistence.UpgradeRevision1(ctx, input)
		if err != nil {
			return result, err
		}
		return result, runtimepersistence.ErrMigrationOutcomeUnknown
	}
	var phases []SchemaUpgradePhase
	_, err := runner.upgrade(context.Background(), source, target, owner, host, time.Now().UTC(), time.Minute, func(p SchemaUpgradePhase) error { phases = append(phases, p); return nil })
	if !errors.Is(err, ErrSchemaUpgradeIncomplete) || len(phases) != 1 || phases[0] != SchemaUpgradeRunning {
		t.Fatalf("interrupted receipt=%v", err)
	}
	receipt, exists, err := readSchemaUpgradeReceipt(source)
	if err != nil || !exists || receipt.Status != "pending" {
		t.Fatal("unknown commit receipt lost")
	}
	owner.Release()
	host.Release()
	owner, host = upgradeGuards(t, source)
	result, err := UpgradeSchemaOwned(context.Background(), source, filepath.Join(filepath.Dir(source), "unused-new"), owner, host, time.Now().UTC(), time.Minute, nil)
	if err != nil || !result.Committed {
		t.Fatalf("restart reconciliation=%+v %v", result, err)
	}
	if _, exists, err := readSchemaUpgradeReceipt(source); err != nil || exists {
		t.Fatal("committed receipt not consumed")
	}
	after := snapshotTestRows(t, source)
	filter := func(rows []string) []string {
		var result []string
		for _, row := range rows {
			if strings.HasPrefix(row, "runtime_metadata:") || strings.HasPrefix(row, "runtime_egress_attempts:") || strings.HasPrefix(row, "runtime_egress_default:") {
				continue
			}
			result = append(result, row)
		}
		return result
	}
	// The only new nonempty table is independently certified as revision1/unset.
	db, openErr := sql.Open("sqlite", "file:"+filepath.Join(source, "runtime.db")+"?mode=ro")
	if openErr != nil {
		t.Fatal(openErr)
	}
	var defaults int
	defaultErr := db.QueryRow(`SELECT count(*) FROM runtime_egress_default WHERE singleton=1 AND revision=1 AND egress_id IS NULL AND egress_revision IS NULL`).Scan(&defaults)
	db.Close()
	if defaultErr != nil || defaults != 1 {
		t.Fatalf("upgrade default seed %d %v", defaults, defaultErr)
	}
	if fmt.Sprint(filter(before)) != fmt.Sprint(filter(after)) {
		t.Fatal("old logical rows changed")
	}
	for name, value := range private {
		if name == "runtime.db" || name == "runtime.db-wal" {
			continue
		}
		actual, err := os.ReadFile(filepath.Join(source, name))
		if err != nil || string(actual) != string(value) {
			t.Fatalf("private file changed %s", name)
		}
	}
	// Marker consumption precedes ordinary changes, which must not be compared
	// to the historical snapshot on later normal2 startups.
	// The current role-aware binary intentionally refuses ordinary Open on v2.
	// Exercise the old receipt protocol with an explicit released-schema write;
	// PrepareStorageOwned separately owns subsequent role cutover.
	changed, err := sql.Open("sqlite", "file:"+filepath.Join(source, "runtime.db")+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	writeResult, writeErr := changed.Exec(`UPDATE connection_rule_sets SET revision=revision+1 WHERE id=1`)
	if writeErr == nil {
		count, countErr := writeResult.RowsAffected()
		if countErr != nil || count != 1 {
			t.Fatal("frozen v2 authored mutation did not change its singleton")
		}
	}
	if err = errors.Join(writeErr, changed.Close()); err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeSchemaOwned(context.Background(), source, filepath.Join(filepath.Dir(source), "still-unused"), owner, host, time.Now().UTC(), time.Minute, func(SchemaUpgradePhase) error { t.Fatal("normal2 emitted upgrade"); return nil }); err != nil {
		t.Fatal(err)
	}
}
func TestSchemaSwitchRestoreRetainsAccountSecretReferences(t *testing.T) {
	source := frozenUpgradeFixture(t)
	owner, host := upgradeGuards(t, source)
	snapshot := filepath.Join(filepath.Dir(source), "snapshot")
	if _, err := UpgradeSchemaOwned(context.Background(), source, snapshot, owner, host, time.Now().UTC(), time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(filepath.Dir(source), "restored")
	if err := RestoreSchemaUpgradeSnapshot(context.Background(), snapshot, restored); err != nil {
		t.Fatal(err)
	}
	state, err := runtimepersistence.InspectSchema(context.Background(), filepath.Join(restored, "runtime.db"), time.Second)
	if err != nil || state.Revision != 1 {
		t.Fatal("snapshot restore lost frozen1")
	}
	originalRows, restoredRows := snapshotTestRows(t, snapshot), snapshotTestRows(t, restored)
	if fmt.Sprint(originalRows) != fmt.Sprint(restoredRows) {
		t.Fatal("restored account/history rows changed")
	}
	assertRestoredSecretReference(t, restored)
	originals, restoredFiles := snapshotTestFiles(t, snapshot), snapshotTestFiles(t, restored)
	for name, value := range originals {
		if name == schemaSnapshotManifestName || name == "runtime.db-wal" {
			continue
		}
		if string(restoredFiles[name]) != string(value) {
			t.Fatalf("restored private bytes changed %s", name)
		}
	}
}

func assertRestoredSecretReference(t *testing.T, restored string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(restored, "runtime.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var reference string
	if err := db.QueryRow(`SELECT secret_reference FROM provider_accounts WHERE account_id='snapshot-account'`).Scan(&reference); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ref, err := secretstore.ParseReference(reference)
	if err != nil {
		t.Fatal(err)
	}
	factory, err := hostsecret.NewServerFileFactory(restored)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	value, err := secrets.ReadAtRevision(context.Background(), ref, 1)
	if err != nil {
		t.Fatal("restored old account reference cannot resolve")
	}
	defer value.Destroy()
	encoded, err := value.CopyBytes()
	defer clear(encoded)
	if err != nil || string(encoded) != "snapshot-synthetic-provider-credential" {
		t.Fatal("restored old credential changed")
	}
}
func TestSchemaSwitchOwnedLargeWALPreparingBeforeInspection(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := os.Create(filepath.Join(source, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Truncate(128 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.WriteFile(filepath.Join(source, "runtime.db-wal"), make([]byte, 33), 0600); err != nil {
		t.Fatal(err)
	}
	owner, err := Acquire(source)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	host, err := instanceguard.Acquire(filepath.Join(source, "server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Release()
	var phases []SchemaUpgradePhase
	runner := newSchemaUpgradeRunner()
	runner.inspect = func(ctx context.Context, path string, timeout time.Duration) (runtimepersistence.SchemaState, error) {
		if len(phases) != 1 || phases[0] != SchemaUpgradePreparing || !owner.OwnsDirectory(source) || !host.OwnsDirectory(source) {
			t.Fatal("inspection preceded owned preparation")
		}
		<-ctx.Done()
		return runtimepersistence.SchemaState{}, ctx.Err()
	}
	_, err = runner.upgrade(context.Background(), source, filepath.Join(filepath.Dir(source), "snapshot"), owner, host, time.Now().UTC(), time.Millisecond, func(p SchemaUpgradePhase) error { phases = append(phases, p); return nil })
	if !errors.Is(err, ErrSchemaUpgradeIncomplete) {
		t.Fatalf("preparation deadline: %v", err)
	}
}

func TestSchemaSwitchReceiptFailureAndCancelNeverAdmitWrites(t *testing.T) {
	for _, fault := range []string{"pending_write", "certification", "cleanup", "cancel_after_commit"} {
		t.Run(fault, func(t *testing.T) {
			source := frozenUpgradeFixture(t)
			owner, host := upgradeGuards(t, source)
			runner := newSchemaUpgradeRunner()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch fault {
			case "pending_write":
				runner.writeReceipt = func(context.Context, string, schemaUpgradeReceipt) error { return ErrCopy }
			case "certification":
				runner.snapshot.syncPath = func(string) error { return ErrCopy }
			case "cleanup":
				runner.removeReceipt = func(context.Context, string) error { return ErrCopy }
			case "cancel_after_commit":
				runner.upgradeDatabase = func(ctx context.Context, input runtimepersistence.MigrationInput) (runtimepersistence.MigrationResult, error) {
					result, err := runtimepersistence.UpgradeRevision1(ctx, input)
					cancel()
					return result, err
				}
			}
			target := filepath.Join(filepath.Dir(source), "snapshot")
			_, err := runner.upgrade(ctx, source, target, owner, host, time.Now().UTC(), time.Minute, nil)
			if !errors.Is(err, ErrSchemaUpgradeIncomplete) {
				t.Fatalf("fault startup=%v", err)
			}
			state, err := runtimepersistence.InspectSchema(context.Background(), filepath.Join(source, "runtime.db"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if fault == "pending_write" || fault == "certification" {
				if state.Revision != 1 {
					t.Fatal("precommit failure executed DDL")
				}
			} else {
				if state.Revision != 2 {
					t.Fatal("known commit falsely restored")
				}
				if _, exists, err := readSchemaUpgradeReceipt(source); err != nil || !exists {
					t.Fatal("uncertain activation lost receipt")
				}
				store, err := runtimepersistence.Open(context.Background(), runtimepersistence.Options{DatabasePath: filepath.Join(source, "runtime.db"), BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
				if store != nil {
					store.Shutdown(context.Background())
					t.Fatal("failed cleanup admitted Store")
				}
				if !errors.Is(err, runtimepersistence.ErrMigrationRequired) {
					t.Fatal(err)
				}
				if _, err := UpgradeSchemaOwned(context.Background(), source, filepath.Join(filepath.Dir(source), "unused"), owner, host, time.Now().UTC(), time.Minute, nil); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestSchemaSwitchPreparationThresholdAndCurrent2(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source, "runtime.db")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, test := range []struct {
		db, wal int64
		want    bool
	}{{128*1024*1024 - 1, 33, false}, {128 * 1024 * 1024, 32, false}, {128 * 1024 * 1024, 33, true}} {
		if err := file.Truncate(test.db); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+"-wal", make([]byte, test.wal), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := schemaUpgradeNeedsPreparation(source)
		if err != nil || got != test.want {
			t.Fatalf("threshold=%v %v", got, err)
		}
	}
	current, _ := backupFixture(t)
	owner, host := upgradeGuards(t, current)
	if _, err := UpgradeSchemaOwned(context.Background(), current, filepath.Join(filepath.Dir(current), "unused"), owner, host, time.Now().UTC(), time.Minute, func(SchemaUpgradePhase) error { t.Fatal("small current2 prepared"); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaSwitchCurrent2RequiresOwnershipThroughInspection(t *testing.T) {
	source, _ := backupFixture(t)
	owner, host := upgradeGuards(t, source)
	runner := newSchemaUpgradeRunner()
	inspect := runner.inspect
	runner.inspect = func(ctx context.Context, path string, timeout time.Duration) (runtimepersistence.SchemaState, error) {
		state, err := inspect(ctx, path, timeout)
		host.Release()
		return state, err
	}
	if _, err := runner.upgrade(context.Background(), source, "", owner, host, time.Now().UTC(), time.Minute, nil); !errors.Is(err, ErrSchemaUpgradeIncomplete) {
		t.Fatalf("released current2 host admitted: %v", err)
	}
}

func TestSchemaSwitchPendingPublicBackupAndMoveProduceNoOutput(t *testing.T) {
	for _, revision := range []int64{1, 2} {
		t.Run(fmt.Sprint(revision), func(t *testing.T) {
			source := frozenUpgradeFixture(t)
			owner, host := upgradeGuards(t, source)
			runner := newSchemaUpgradeRunner()
			runner.upgradeDatabase = func(ctx context.Context, input runtimepersistence.MigrationInput) (runtimepersistence.MigrationResult, error) {
				if revision == 2 {
					if _, err := runtimepersistence.UpgradeRevision1(ctx, input); err != nil {
						t.Fatal(err)
					}
				}
				return runtimepersistence.MigrationResult{}, runtimepersistence.ErrMigrationOutcomeUnknown
			}
			if _, err := runner.upgrade(context.Background(), source, filepath.Join(filepath.Dir(source), "snapshot"), owner, host, time.Now().UTC(), time.Minute, nil); !errors.Is(err, ErrSchemaUpgradeIncomplete) {
				t.Fatal(err)
			}
			owner.Release()
			host.Release()
			before := snapshotTestFiles(t, source)
			for _, operation := range []string{"backup", "move"} {
				target := filepath.Join(filepath.Dir(source), operation)
				var err error
				if operation == "backup" {
					err = Backup(context.Background(), source, target, time.Now().UTC())
				} else {
					err = Copy(context.Background(), source, target)
				}
				if !errors.Is(err, ErrSchemaUpgradeIncomplete) {
					t.Errorf("pending %s admitted: %v", operation, err)
				}
				if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
					t.Error("pending operation created output")
				}
				after := snapshotTestFiles(t, source)
				if fmt.Sprint(before) != fmt.Sprint(after) {
					t.Fatal("pending operation changed source")
				}
			}
		})
	}
}
func TestSchemaSwitchOrdinaryManifestRejectsOperationalReceipt(t *testing.T) {
	source, _ := backupFixture(t)
	backup := filepath.Join(filepath.Dir(source), "backup")
	if err := Backup(context.Background(), source, backup, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	manifest, err := readBackupManifest(backup)
	if err != nil {
		t.Fatal(err)
	}
	encoded := []byte(`{"status":"pending","privateSnapshot":"synthetic-private-only"}`)
	if err := os.WriteFile(filepath.Join(backup, schemaUpgradeReceiptName), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	manifest.Files = append(manifest.Files, backupFile{Path: schemaUpgradeReceiptName, Bytes: int64(len(encoded)), SHA256: hex.EncodeToString(digest[:])})
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, backupManifestName), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBackup(context.Background(), backup); !errors.Is(err, ErrBackupInvalid) {
		t.Fatalf("private operational receipt imported: %v", err)
	}
	target := filepath.Join(filepath.Dir(source), "restored")
	if err := Restore(context.Background(), backup, target); !errors.Is(err, ErrBackupInvalid) {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid ordinary restore created target")
	}
}
