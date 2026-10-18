package runtimedata

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func frozenRevision2UpgradeFixture(t *testing.T) string {
	t.Helper()
	source := frozenUpgradeFixture(t)
	owner, host := upgradeGuards(t, source)
	if _, err := UpgradeSchemaOwned(context.Background(), source, "", owner, host, time.Now().UTC(), time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(source, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO native_tool_scopes VALUES('managed_run','snapshot-native',1,1,'active',0,100);
 INSERT INTO native_tool_once VALUES('managed_run','snapshot-native',zeroblob(32),'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',zeroblob(32),'retained-once',3,1);`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	host.Release()
	owner.Release()
	return source
}
func TestRevision2OwnedBackupFailureNoDDL(t *testing.T) {
	source := frozenRevision2UpgradeFixture(t)
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
	runner := newRevision2UpgradeRunner()
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
func TestRevision2OwnedPendingReceiptSurvivesCrash(t *testing.T) {
	source := frozenRevision2UpgradeFixture(t)
	owner, host := upgradeGuards(t, source)
	// Compare every frozen-v1 account field; the new preference is checked
	// independently below rather than comparing arrays of different columns.
	legacyColumns := map[string][]string{"provider_accounts": {
		"account_id", "display_name", "credential_origin", "endpoint_associations", "association_revision", "note", "note_revision",
		"settings_revision", "egress_profile_json", "automatic_refresh", "realm_id", "driver_ref", "secret_reference", "state", "revision", "created_at_unix_ms", "updated_at_unix_ms",
	}}
	before := snapshotTestRowsWithColumns(t, source, legacyColumns)
	private := snapshotTestFiles(t, source)
	target := filepath.Join(filepath.Dir(source), "private-snapshot")
	runner := newRevision2UpgradeRunner()
	runner.upgradeDatabase = func(ctx context.Context, input runtimepersistence.Revision2MigrationInput) (runtimepersistence.MigrationResult, error) {
		result, err := runtimepersistence.UpgradeRevision2(ctx, input)
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
	result, err := upgradeRevision2Owned(context.Background(), source, filepath.Join(filepath.Dir(source), "unused-new"), owner, host, time.Now().UTC(), time.Minute, nil)
	if err != nil || !result.Committed {
		t.Fatalf("restart reconciliation=%+v %v", result, err)
	}
	if _, exists, err := readSchemaUpgradeReceipt(source); err != nil || exists {
		t.Fatal("committed receipt not consumed")
	}
	after := snapshotTestRowsWithColumns(t, source, legacyColumns)
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
	// The old API remains a frozen-v2 boundary. Inspect it offline; current
	// Store admission requires the separately owned second stage.
	state, err := runtimepersistence.InspectSchema(context.Background(), filepath.Join(source, "runtime.db"), time.Second)
	if err != nil || state.Revision != 3 {
		t.Fatalf("legacy receipt result %+v %v", state, err)
	}

	if _, err := upgradeRevision2Owned(context.Background(), source, filepath.Join(filepath.Dir(source), "still-unused"), owner, host, time.Now().UTC(), time.Minute, func(SchemaUpgradePhase) error { t.Fatal("normal2 emitted upgrade"); return nil }); err != nil {
		t.Fatal(err)
	}
}
func TestRevision2OwnedRestoreRetainsAccountSecretReferences(t *testing.T) {
	source := frozenRevision2UpgradeFixture(t)
	owner, host := upgradeGuards(t, source)
	snapshot := filepath.Join(filepath.Dir(source), "snapshot")
	if _, err := upgradeRevision2Owned(context.Background(), source, snapshot, owner, host, time.Now().UTC(), time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(filepath.Dir(source), "restored")
	if err := RestoreSchemaUpgradeSnapshot(context.Background(), snapshot, restored); err != nil {
		t.Fatal(err)
	}
	state, err := runtimepersistence.InspectSchema(context.Background(), filepath.Join(restored, "runtime.db"), time.Second)
	if err != nil || state.Revision != 2 {
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

func TestRevision2OwnedReceiptFailureAndCancelNeverAdmitWrites(t *testing.T) {
	for _, fault := range []string{"pending_write", "certification", "cleanup", "cancel_after_commit"} {
		t.Run(fault, func(t *testing.T) {
			source := frozenRevision2UpgradeFixture(t)
			owner, host := upgradeGuards(t, source)
			runner := newRevision2UpgradeRunner()
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
				runner.upgradeDatabase = func(ctx context.Context, input runtimepersistence.Revision2MigrationInput) (runtimepersistence.MigrationResult, error) {
					result, err := runtimepersistence.UpgradeRevision2(ctx, input)
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
				if state.Revision != 2 {
					t.Fatal("precommit failure executed DDL")
				}
			} else {
				if state.Revision != 3 {
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
				if _, err := upgradeRevision2Owned(context.Background(), source, filepath.Join(filepath.Dir(source), "unused"), owner, host, time.Now().UTC(), time.Minute, nil); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestRevision2OwnedPendingPublicBackupAndMoveProduceNoOutput(t *testing.T) {
	for _, revision := range []int64{1, 2} {
		t.Run(fmt.Sprint(revision), func(t *testing.T) {
			source := frozenRevision2UpgradeFixture(t)
			owner, host := upgradeGuards(t, source)
			runner := newRevision2UpgradeRunner()
			runner.upgradeDatabase = func(ctx context.Context, input runtimepersistence.Revision2MigrationInput) (runtimepersistence.MigrationResult, error) {
				if revision == 2 {
					if _, err := runtimepersistence.UpgradeRevision2(ctx, input); err != nil {
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
func TestRevision2OwnedOrdinaryManifestRejectsOperationalReceipt(t *testing.T) {
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
