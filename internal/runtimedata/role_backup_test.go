package runtimedata

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func roleBackupFixture(t *testing.T) (string, StorageLayout) {
	t.Helper()
	owned := storageCutoverFixture(t, 0)
	layout, err := PrepareStorageOwned(context.Background(), owned, time.Now().UTC(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return provisionRoleBackupFixture(t, owned, layout)
}
func provisionRoleBackupFixture(t *testing.T, owned OwnedStorage, layout StorageLayout) (string, StorageLayout) {
	t.Helper()
	var err error
	digest := sha256.Sum256([]byte(layout.ID))
	id := "current-" + hex.EncodeToString(digest[:16])
	obs, err := runtimepersistence.OpenObservations(context.Background(), runtimepersistence.ObservationOptions{DatabasePath: filepath.Join(owned.Directory, "observations.db"), LayoutID: layout.ID, ArchiveID: id, BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
	if err != nil {
		t.Fatal(err)
	}
	if err = obs.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	receipt, _ := json.Marshal(map[string]any{"schema": "vibermate-recording-provision/v1", "layoutId": layout.ID, "sourceId": id, "provisioned": true})
	if err = os.WriteFile(filepath.Join(owned.Directory, "recording-state.json"), receipt, 0600); err != nil {
		t.Fatal(err)
	}
	if err = MarkObservationProvisioned(context.Background(), owned.Directory, layout.ID, owned.Runtime); err != nil {
		t.Fatal(err)
	}
	layout, err = ReadStorageLayout(owned.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(owned.Directory, "local-ca"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"root-certificate.pem", "root-key.pem", "root-manifest.json"} {
		if _, e := os.Stat(filepath.Join(owned.Directory, "local-ca", name)); e == nil {
			continue
		}
		if err = os.WriteFile(filepath.Join(owned.Directory, "local-ca", name), []byte("fixture-"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	owned.Host.Release()
	owned.Runtime.Release()
	return owned.Directory, layout
}

func TestRoleBackupRoundTripUsesExplicitLayoutAndRoleInventory(t *testing.T) {
	source, layout := roleBackupFixture(t)
	root := filepath.Dir(source)
	backup := filepath.Join(root, filepath.Base(source)+"-backup")
	restored := backup + "-restore"
	if err := Backup(context.Background(), source, backup, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(backup, backupManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema   string
		LayoutID string `json:"layoutId"`
		Roles    []struct {
			Path, Identity, Status string
			Required               bool
		}
	}
	if err = json.Unmarshal(manifest, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != "vibermate-data-backup/v2" || doc.LayoutID != layout.ID || len(doc.Roles) != 2 || doc.Roles[0].Path != "runtime.db" || !doc.Roles[0].Required || doc.Roles[1].Status != "verified" {
		t.Fatalf("role manifest %s", manifest)
	}
	if err = ValidateBackup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	if err = Restore(context.Background(), backup, restored); err != nil {
		t.Fatal(err)
	}
	if _, err = runtimepersistence.ValidateCurrentControl(context.Background(), filepath.Join(restored, "runtime.db"), layout.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRolePrivateSnapshotAndMovePreserveRolesAndPrivateFiles(t *testing.T) {
	source, layout := roleBackupFixture(t)
	if err := os.Mkdir(filepath.Join(source, serverSecretDirectory), 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(serverSecretDirectory, "private.json")
	if err := os.WriteFile(filepath.Join(source, secret), []byte("private-role-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := source + "-private"
	owner, host := upgradeGuards(t, source)
	if err := CreateSchemaUpgradeSnapshot(context.Background(), source, snapshot, owner, host, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	owner.Release()
	host.Release()
	if err := ValidateSchemaUpgradeSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	restored := snapshot + "-restore"
	if err := RestoreSchemaUpgradeSnapshot(context.Background(), snapshot, restored); err != nil {
		t.Fatal(err)
	}
	moved := snapshot + "-move"
	if err := Copy(context.Background(), source, moved); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{restored, moved} {
		got, err := os.ReadFile(filepath.Join(target, secret))
		if err != nil || string(got) != "private-role-credential" {
			t.Fatalf("private credential %s: %q %v", target, got, err)
		}
		if _, err = runtimepersistence.ValidateCurrentControl(context.Background(), filepath.Join(target, "runtime.db"), layout.ID); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(snapshot, schemaSnapshotManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Schema string }
	if err = json.Unmarshal(manifest, &doc); err != nil || doc.Schema != "runtime-schema-upgrade/v2" {
		t.Fatalf("private version %s %v", manifest, err)
	}
}

func roleFixtureSQL(t *testing.T, root, relative, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, relative))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(query, args...); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestRoleBackupRefusesPendingMissingAuthorityAndPrivacyLoss(t *testing.T) {
	for _, scenario := range []string{"requested-no-receipt", "claimed-no-receipt", "retired-receipt-before-cas", "cleanup-receipt", "cleanup-temp", "certificate-temp", "missing-control", "wrong-layout", "corrupt-observations", "denial-prefix-lost", "denial-tail-corrupt", "unknown-provision", "lost-provision", "downgraded-provision"} {
		t.Run(scenario, func(t *testing.T) {
			source, layout := roleBackupFixture(t)
			switch scenario {
			case "requested-no-receipt", "claimed-no-receipt", "retired-receipt-before-cas":
				control, err := runtimepersistence.OpenControl(context.Background(), runtimepersistence.ControlOptions{DatabasePath: filepath.Join(source, "runtime.db"), LayoutID: layout.ID, BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
				if err != nil {
					t.Fatal(err)
				}
				if err = control.RequestRecordingCleanup(context.Background(), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if scenario != "requested-no-receipt" {
					if _, _, _, err = control.ClaimRecordingCleanup(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				if err = control.Shutdown(context.Background()); err != nil {
					t.Fatal(err)
				}
				if scenario == "retired-receipt-before-cas" {
					// The retired operation document cannot stand in for its final control CAS.
					path := filepath.Join(source, RecordingCleanupReceiptName)
					if err = os.WriteFile(path, []byte("retired"), 0600); err != nil {
						t.Fatal(err)
					}
					if err = os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
			case "cleanup-receipt", "cleanup-temp", "certificate-temp":
				name := map[string]string{"cleanup-receipt": RecordingCleanupReceiptName, "cleanup-temp": ".recording-cleanup-123", "certificate-temp": ".recording-certificate-123"}[scenario]
				if err := os.WriteFile(filepath.Join(source, name), []byte("unresolved"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-control":
				if err := os.Remove(filepath.Join(source, "runtime.db")); err != nil {
					t.Fatal(err)
				}
			case "wrong-layout":
				roleFixtureSQL(t, source, "runtime.db", `UPDATE runtime_storage_state SET layout_id='foreign'`)
			case "corrupt-observations":
				if err := os.WriteFile(filepath.Join(source, "observations.db"), []byte("damaged"), 0600); err != nil {
					t.Fatal(err)
				}
			case "denial-prefix-lost":
				roleFixtureSQL(t, source, "runtime.db", `UPDATE recording_denial_coverage SET identity_count=1,chain_hash=zeroblob(32)`)
			case "denial-tail-corrupt":
				roleFixtureSQL(t, source, "observations.db", `INSERT INTO recording_denials VALUES(1,'exchange','lost-tail',zeroblob(32))`)
			case "unknown-provision":
				layout.ObservationProvisioning = "unknown"
				data, _ := json.Marshal(storageLayoutDocument{Schema: storageLayoutSchema, StorageLayout: layout})
				if err := os.WriteFile(filepath.Join(source, storageLayoutName), data, 0600); err != nil {
					t.Fatal(err)
				}
			case "lost-provision":
				if err := os.Remove(filepath.Join(source, "recording-state.json")); err != nil {
					t.Fatal(err)
				}
			case "downgraded-provision":
				data, _ := json.Marshal(map[string]any{"schema": "vibermate-recording-provision/v1", "layoutId": layout.ID, "sourceId": currentBackupSourceID(layout.ID), "provisioned": false})
				if err := os.WriteFile(filepath.Join(source, "recording-state.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotTestFiles(t, source)
			if err := Backup(context.Background(), source, source+"-refused", time.Now().UTC()); err == nil {
				t.Fatal("completed backup with unresolved required/privacy state")
			}
			if _, err := os.Stat(filepath.Join(source+"-refused", backupManifestName)); !os.IsNotExist(err) {
				t.Fatal("refused backup published manifest")
			}
			if err := Copy(context.Background(), source, source+"-move-refused"); err == nil {
				t.Fatal("move completed pending or uncertified state")
			}
			owner, host := upgradeGuards(t, source)
			if err := CreateSchemaUpgradeSnapshot(context.Background(), source, source+"-private-refused", owner, host, time.Now().UTC()); err == nil {
				t.Fatal("private snapshot completed pending or uncertified split state")
			}
			owner.Release()
			host.Release()
			assertSnapshotSourceBytes(t, source, before)
		})
	}
}
func assertSnapshotSourceBytes(t *testing.T, root string, before map[string][]byte) {
	t.Helper()
	after := snapshotTestFiles(t, root)
	if len(before) != len(after) {
		t.Fatal("source persistent inventory changed")
	}
	for name, want := range before {
		if !bytes.Equal(want, after[name]) {
			t.Fatalf("source bytes changed: %s", name)
		}
	}
}
func TestRoleBackupMissingOptionalIsExplicitAndCannotRecreateLostHistory(t *testing.T) {
	source, layout := roleBackupFixture(t)
	if err := os.Remove(filepath.Join(source, "observations.db")); err != nil {
		t.Fatal(err)
	}
	backup := source + "-absent"
	if err := Backup(context.Background(), source, backup, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	manifest, err := readBackupManifest(backup)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.HistoryComplete == nil || *manifest.HistoryComplete || manifest.Roles[1].Status != "absent" {
		t.Fatalf("missing history certified complete: %+v", manifest)
	}
	restored := backup + "-restore"
	if err = Restore(context.Background(), backup, restored); err != nil {
		t.Fatal(err)
	}
	// Required admission still works and does not scan or initialize history.
	control := openBackupControlReader(t, restored)
	control.Shutdown(context.Background())
	if _, err = runtimepersistence.ValidateCurrentControl(context.Background(), filepath.Join(restored, "runtime.db"), layout.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(filepath.Join(restored, "observations.db")); !os.IsNotExist(err) {
		t.Fatal("recreated lost provisioned history")
	}
	if _, err = os.Lstat(filepath.Join(restored, "recording-state.json")); err != nil {
		t.Fatal("lost provisioning proof")
	}
}

// Freeze committed WAL bytes before closing the independent builder. Every
// production operation receives a stopped file set without a builder handle.
func retainRoleWAL(t *testing.T, path, query string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`PRAGMA journal_mode=WAL;PRAGMA wal_autocheckpoint=0;` + query); err != nil {
		t.Fatal(err)
	}
	originals := map[string][]byte{}
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		originals[suffix] = data
	}
	if len(originals["-wal"]) <= 32 {
		t.Fatal("missing committed WAL")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	for suffix, data := range originals {
		if err = os.WriteFile(path+suffix, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func TestRoleBackupAllRoleWALAndQualifiedSHMPreserveExactBytes(t *testing.T) {
	source, layout := roleBackupFixture(t)
	retainRoleWAL(t, filepath.Join(source, "runtime.db"), `INSERT INTO code_library_collections VALUES('wal-authority','Control WAL authority')`)
	retainRoleWAL(t, filepath.Join(source, "observations.db"), `UPDATE runtime_metadata SET initialized_at='2026-10-18T01:02:03Z'`)
	for _, name := range []string{"runtime.db-shm", "observations.db-shm", "user-authored-shm"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("retained-"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshotTestFiles(t, source)
	backup := source + "-wal-backup"
	if err := Backup(context.Background(), source, backup, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	backupBefore := snapshotTestFiles(t, backup)
	if err := ValidateBackup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	restored := backup + "-restored"
	if err := Restore(context.Background(), backup, restored); err != nil {
		t.Fatal(err)
	}
	assertSnapshotSourceBytes(t, source, before)
	assertSnapshotSourceBytes(t, backup, backupBefore)
	owner, host := upgradeGuards(t, source)
	private := source + "-wal-private"
	if err := CreateSchemaUpgradeSnapshot(context.Background(), source, private, owner, host, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	owner.Release()
	host.Release()
	privateBefore := snapshotTestFiles(t, private)
	if err := ValidateSchemaUpgradeSnapshot(context.Background(), private); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSchemaUpgradeSnapshot(context.Background(), private, private+"-restored"); err != nil {
		t.Fatal(err)
	}
	moved := source + "-wal-moved"
	if err := Copy(context.Background(), source, moved); err != nil {
		t.Fatal(err)
	}
	assertSnapshotSourceBytes(t, source, before)
	assertSnapshotSourceBytes(t, private, privateBefore)
	for _, target := range []string{backup, restored, private, private + "-restored", moved} {
		for _, name := range []string{"runtime.db-shm", "observations.db-shm"} {
			if _, err := os.Lstat(filepath.Join(target, name)); !os.IsNotExist(err) {
				t.Fatalf("copied role SHM %s", name)
			}
		}
		if data, err := os.ReadFile(filepath.Join(target, "user-authored-shm")); err != nil || string(data) != "retained-user-authored-shm" {
			t.Fatal("suffix filter dropped user file", err)
		}
		for _, name := range []string{"runtime.db-wal", "observations.db-wal"} {
			data, err := os.ReadFile(filepath.Join(target, name))
			if err != nil || !bytes.Equal(data, before[name]) {
				t.Fatalf("lost committed role WAL %s: %v", name, err)
			}
		}
		state, err := runtimepersistence.ValidateOfflineRole(context.Background(), filepath.Join(target, "runtime.db"), layout.Control.Identity, 3, layout.ID, "", recording.InspectionStamp{})
		if err != nil || state.Database.Identity != "vibermate-control" {
			t.Fatal(err)
		}
	}
}
func TestRoleBackupIncludesAll129DeclaredInspectionSources(t *testing.T) {
	source, layout := roleBackupFixture(t)
	for i := 0; i < 128; i++ {
		id := fmt.Sprintf("retained-%03d", i)
		layout.Archives = append(layout.Archives, LegacyArchive{ID: id, RelativeDirectory: "archives/" + id, Database: runtimepersistence.SchemaState{Identity: "vibermate-runtime", Revision: 3, InitializedAt: "2026-10-03T00:00:00Z"}})
	}
	data, _ := json.Marshal(storageLayoutDocument{Schema: storageLayoutSchema, StorageLayout: layout})
	if err := os.WriteFile(filepath.Join(source, storageLayoutName), data, 0600); err != nil {
		t.Fatal(err)
	}
	backup := source + "-129"
	if err := Backup(context.Background(), source, backup, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	manifest, err := readBackupManifest(backup)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Roles) != 130 || *manifest.HistoryComplete {
		t.Fatal("dropped declared sources or hid absent history")
	}
	for i, role := range manifest.Roles[2:] {
		if role.ArchiveID != layout.Archives[i].ID || role.Status != "absent" {
			t.Fatalf("lost archive descriptor %d", i)
		}
	}
}

func TestRoleBackupRejectsMalformedOptionalACPSnapshot(t *testing.T) {
	source, _ := roleBackupFixture(t)
	roleFixtureSQL(t, source, "observations.db", `INSERT INTO acp_snapshots VALUES('legacy-run',1,1,'full',7,1900000000000,X'01',1,0)`)
	if err := Backup(context.Background(), source, source+"-malformed-acp", time.Now().UTC()); err == nil {
		t.Fatal("malformed optional semantic snapshot certified verified")
	}
}

func publishedRoleBackupFixture(t *testing.T) (string, StorageLayout) {
	t.Helper()
	source, _ := backupFixture(t)
	seedSnapshotAuthority(t, source)
	freezePublishedBackupFixture(t, source)
	owner, host := upgradeGuards(t, source)
	owned := OwnedStorage{Directory: source, Runtime: owner, Host: host}
	layout, err := PrepareStorageOwned(context.Background(), owned, time.Now().UTC(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return provisionRoleBackupFixture(t, owned, layout)
}
func maintainBackupFixture(t *testing.T, root string, layout StorageLayout) RecordingMaintenanceCertificate {
	t.Helper()
	ctx := context.Background()
	control, err := runtimepersistence.OpenControl(ctx, runtimepersistence.ControlOptions{DatabasePath: filepath.Join(root, "runtime.db"), LayoutID: layout.ID, BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
	if err != nil {
		t.Fatal(err)
	}
	if err = control.RequestRecordingCleanup(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	target, generation, cutoff, err := control.ClaimRecordingCleanup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sources := []runtimepersistence.InspectionSource{}
	for _, archive := range layout.Archives {
		sources = append(sources, runtimepersistence.InspectionSource{ID: archive.ID, RelativePath: archive.RelativeDirectory + "/runtime.db", Database: archive.Database})
	}
	sources = append(sources, runtimepersistence.InspectionSource{ID: currentBackupSourceID(layout.ID), RelativePath: "observations.db", Current: true, Database: runtimepersistence.SchemaState{Identity: "vibermate-observations", Revision: 1}})
	catalog, err := runtimepersistence.NewInspectionCatalog(runtimepersistence.InspectionCatalogOptions{Root: root, LayoutID: layout.ID, Sources: sources, Generation: generation})
	if err != nil {
		t.Fatal(err)
	}
	set := catalog.Version().SourceSet
	catalog.Close(ctx)
	owner, host := upgradeGuards(t, root)
	receipt := RecordingCleanupReceipt{Schema: "vibermate-recording-cleanup/v1", LayoutID: layout.ID, SourceSet: set, TargetRevision: target, SourceGeneration: generation, CutoffMillis: cutoff, Phase: "verify", CountsExact: false}
	receipt.Released.PrivacyMetadataRetained = true
	receipt.Released.ApprovalOwnershipUnknown = true
	if err = CertifyRecordingMaintenance(ctx, root, owner, layout, receipt); err != nil {
		t.Fatal(err)
	}
	if err = control.CompleteRecordingCleanup(ctx, target, generation); err != nil {
		t.Fatal(err)
	}
	if err = control.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	owner.Release()
	host.Release()
	certificate, exists, err := ReadRecordingMaintenanceCertificate(root)
	if err != nil || !exists {
		t.Fatalf("maintained certificate: %v %v", exists, err)
	}
	return certificate
}
func TestRoleBackupOriginalAndMaintainedRuntime3ArchiveCertificates(t *testing.T) {
	for _, scenario := range []string{"original-no-certificate", "maintained", "changed-current", "stale-archive", "wrong-provenance", "wrong-cutoff", "wrong-target", "wrong-generation", "wrong-source-set", "missing-certificate"} {
		t.Run(scenario, func(t *testing.T) {
			source, layout := publishedRoleBackupFixture(t)
			if scenario != "original-no-certificate" {
				// This fixture exercises the file/coordinate consumer, not physical-erasure
				// qualification. Preserve original provenance while changing archive bytes.
				roleFixtureSQL(t, source, layout.Archives[0].RelativeDirectory+"/runtime.db", `PRAGMA user_version=7`)
				certificate := maintainBackupFixture(t, source, layout)
				switch scenario {
				case "changed-current":
					roleFixtureSQL(t, source, "observations.db", `UPDATE runtime_metadata SET initialized_at='2026-10-18T01:02:03Z'`)
				case "stale-archive":
					roleFixtureSQL(t, source, layout.Archives[0].RelativeDirectory+"/runtime.db", `PRAGMA user_version=8`)
				case "wrong-provenance":
					name := filepath.Join(source, layout.Archives[0].RelativeDirectory, "archive-manifest.json")
					data, _ := os.ReadFile(name)
					if err := os.WriteFile(name, append(data, ' '), 0600); err != nil {
						t.Fatal(err)
					}
				case "wrong-cutoff":
					certificate.CutoffMillis++
				case "wrong-target":
					certificate.TargetRevision++
				case "wrong-generation":
					certificate.SourceGeneration++
				case "wrong-source-set":
					certificate.SourceSet = strings.Repeat("a", 64)
				case "missing-certificate":
					if err := os.Remove(filepath.Join(source, RecordingMaintenanceCertificateName)); err != nil {
						t.Fatal(err)
					}
				}
				if strings.HasPrefix(scenario, "wrong-") && scenario != "wrong-provenance" {
					data, _ := json.Marshal(certificate)
					if err := os.WriteFile(filepath.Join(source, RecordingMaintenanceCertificateName), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := snapshotTestFiles(t, source)
			err := Backup(context.Background(), source, source+"-certificate-backup", time.Now().UTC())
			success := scenario == "original-no-certificate" || scenario == "maintained" || scenario == "changed-current"
			if success && err != nil {
				t.Fatal(err)
			}
			if !success && err == nil {
				t.Fatal("certified stale maintenance/provenance coordinates")
			}
			assertSnapshotSourceBytes(t, source, before)
		})
	}
}

func TestBackupStrictVersionDispatchRejectsV2FieldsInV1(t *testing.T) {
	source, _ := backupFixture(t)
	freezePublishedBackupFixture(t, source)
	backup := source + "-v1"
	if err := Backup(context.Background(), source, backup, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(backup, backupManifestName)
	encoded, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	encoded = bytes.Replace(encoded, []byte(`"schema":`), []byte(`"layoutId":null,"schema":`), 1)
	if err = os.WriteFile(name, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if err = ValidateBackup(context.Background(), backup); err == nil {
		t.Fatal("v1 reader accepted a v2 field")
	}
}
func TestRoleBackupStrictManifestAndPrivateVersionRefusals(t *testing.T) {
	for _, scenario := range []string{"wrong-layout-id", "wrong-role-identity", "wrong-role-revision", "wrong-role-required", "wrong-role-status", "wrong-history-complete", "duplicate-schema", "private-downgrade", "private-strip-role-document"} {
		t.Run(scenario, func(t *testing.T) {
			source, _ := roleBackupFixture(t)
			private := strings.HasPrefix(scenario, "private-")
			target := source + "-strict"
			var name string
			if private {
				owner, host := upgradeGuards(t, source)
				if err := CreateSchemaUpgradeSnapshot(context.Background(), source, target, owner, host, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				owner.Release()
				host.Release()
				name = filepath.Join(target, schemaSnapshotManifestName)
			} else {
				if err := Backup(context.Background(), source, target, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				name = filepath.Join(target, backupManifestName)
			}
			encoded, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err = json.Unmarshal(encoded, &document); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "wrong-layout-id":
				document["layoutId"] = "other-layout"
			case "wrong-role-identity":
				document["roles"].([]any)[0].(map[string]any)["identity"] = "vibermate-runtime"
			case "wrong-role-revision":
				document["roles"].([]any)[0].(map[string]any)["revision"] = float64(2)
			case "wrong-role-required":
				document["roles"].([]any)[0].(map[string]any)["required"] = false
			case "wrong-role-status":
				document["roles"].([]any)[1].(map[string]any)["status"] = "absent"
			case "wrong-history-complete":
				document["historyComplete"] = false
			case "private-downgrade", "private-strip-role-document":
				var manifest schemaSnapshotManifest
				if err = json.Unmarshal(encoded, &manifest); err != nil {
					t.Fatal(err)
				}
				manifest.Schema = schemaSnapshotManifestSchema
				if scenario == "private-strip-role-document" {
					manifest.backupRoles = backupRoles{}
				}
				encoded, err = json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				encoded = append(encoded, '\n')
			}
			if !private {
				encoded, err = json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "duplicate-schema" {
					encoded = bytes.Replace(encoded, []byte(`"schema":"vibermate-data-backup/v2"`), []byte(`"schema":"foreign","schema":"vibermate-data-backup/v2"`), 1)
				}
			}
			if err = os.WriteFile(name, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			before := snapshotTestFiles(t, target)
			var validation, restoration error
			if private {
				validation = ValidateSchemaUpgradeSnapshot(context.Background(), target)
				restoration = RestoreSchemaUpgradeSnapshot(context.Background(), target, target+"-refused")
			} else {
				validation = ValidateBackup(context.Background(), target)
				restoration = Restore(context.Background(), target, target+"-refused")
			}
			if validation == nil || restoration == nil {
				t.Fatal("accepted cross-version/mismatched role declaration")
			}
			if _, err = os.Lstat(target + "-refused"); !os.IsNotExist(err) {
				t.Fatal("invalid manifest created target")
			}
			assertSnapshotSourceBytes(t, target, before)
		})
	}
}

func TestRoleBackupRejectsMalformedOptionalNativeContentIdentity(t *testing.T) {
	source, _ := roleBackupFixture(t)
	body := []byte("synthetic retained native content")
	digest := sha256.Sum256(body)
	roleFixtureSQL(t, source, "observations.db", `INSERT INTO runtime_evidence_chunks VALUES(?,?,'identity',?)`, digest[:], len(body), body)
	roleFixtureSQL(t, source, "observations.db", `INSERT INTO runtime_evidence_bodies VALUES(?,?,?)`, digest[:], len(body), digest[:])
	roleFixtureSQL(t, source, "observations.db", `INSERT INTO native_tool_contents VALUES('eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','tool_result','manual_capture','synthetic-capture',1,1,'synthetic-environment',1,randomblob(32),'tools_call','synthetic-tool','http://invalid-plaintext-origin.example','original','full',1,1000,86401000,1001,?)`, digest[:])
	if err := Backup(context.Background(), source, source+"-invalid-native", time.Now().UTC()); err == nil {
		t.Fatal("malformed known native content identity certified verified")
	}
}

func TestRoleBackupArchivedWALExcludesSHMWithoutRewritingOriginalProvenance(t *testing.T) {
	source, _ := backupFixture(t)
	seedSnapshotAuthority(t, source)
	freezePublishedBackupFixture(t, source)
	retainRoleWAL(t, filepath.Join(source, "runtime.db"), `UPDATE provider_accounts SET note='archive WAL retained note',note_revision=11`)
	if err := os.WriteFile(filepath.Join(source, "runtime.db-shm"), []byte("original disposable SHM"), 0600); err != nil {
		t.Fatal(err)
	}
	owner, host := upgradeGuards(t, source)
	owned := OwnedStorage{Directory: source, Runtime: owner, Host: host}
	layout, err := PrepareStorageOwned(context.Background(), owned, time.Now().UTC(), nil)
	if err != nil {
		t.Fatal(err)
	}
	source, layout = provisionRoleBackupFixture(t, owned, layout)
	archived := layout.Archives[0].RelativeDirectory
	provenance, err := os.ReadFile(filepath.Join(source, archived, "archive-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var original archiveManifest
	if err = json.Unmarshal(provenance, &original); err != nil {
		t.Fatal(err)
	}
	foundSHM, foundWAL := false, false
	for _, file := range original.Files {
		foundSHM = foundSHM || file.Name == "runtime.db-shm"
		foundWAL = foundWAL || file.Name == "runtime.db-wal"
	}
	if !foundSHM || !foundWAL {
		t.Fatal("fixture lacks original WAL/SHM provenance")
	}
	before := snapshotTestFiles(t, source)
	backup := source + "-archive-wal-backup"
	if err = Backup(context.Background(), source, backup, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err = ValidateBackup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	restored := backup + "-restored"
	if err = Restore(context.Background(), backup, restored); err != nil {
		t.Fatal(err)
	}
	owner, host = upgradeGuards(t, source)
	private := source + "-archive-wal-private"
	if err = CreateSchemaUpgradeSnapshot(context.Background(), source, private, owner, host, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	owner.Release()
	host.Release()
	if err = RestoreSchemaUpgradeSnapshot(context.Background(), private, private+"-restored"); err != nil {
		t.Fatal(err)
	}
	moved := source + "-archive-wal-moved"
	if err = Copy(context.Background(), source, moved); err != nil {
		t.Fatal(err)
	}
	assertSnapshotSourceBytes(t, source, before)
	for _, target := range []string{backup, restored, private, private + "-restored", moved} {
		for _, name := range []string{"runtime.db", "runtime.db-wal", "archive-manifest.json"} {
			rel := archived + "/" + name
			data, err := os.ReadFile(filepath.Join(target, rel))
			if err != nil || !bytes.Equal(data, before[rel]) {
				t.Fatalf("changed original archive %s: %v", name, err)
			}
		}
		if _, err = os.Lstat(filepath.Join(target, archived, "runtime.db-shm")); !os.IsNotExist(err) {
			t.Fatal("copied archived SHM")
		}
	}
}
