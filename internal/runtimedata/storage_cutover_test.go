package runtimedata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func storageCutoverFixture(t *testing.T, revision int) OwnedStorage {
	t.Helper()
	root, canonicalErr := filepath.EvalSymlinks(t.TempDir())
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	owner, host := upgradeGuards(t, root)
	if revision != 0 {
		ddl, err := os.ReadFile("../runtimepersistence/schema/v" + string(rune('0'+revision)) + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", filepath.Join(root, "runtime.db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(ddl)); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO runtime_metadata VALUES(1,'vibermate-runtime',?,'2026-10-03T00:00:00Z');INSERT INTO code_library_collections VALUES('preserved','Original')`, revision); err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return OwnedStorage{Directory: root, Runtime: owner, Host: host}
}

func TestOwnedStorageCutoverBarrierResume(t *testing.T) {
	for _, revision := range []int{0, 1, 2} {
		barriers := []string{"receipt-preparing", "source-copy", "control-certified", "receipt-ready", "archive-runtime.db", "archive-manifest", "receipt-archived", "control-published", "layout-published", "receipt-activated", "receipt-removed"}
		if revision == 0 {
			barriers = []string{"receipt-preparing", "control-certified", "receipt-ready", "receipt-archived", "control-published", "layout-published", "receipt-activated", "receipt-removed"}
		}
		for _, barrier := range barriers {
			t.Run(string(rune('0'+revision))+"/"+barrier, func(t *testing.T) {
				owned := storageCutoverFixture(t, revision)
				var original []byte
				if revision != 0 {
					original, _ = os.ReadFile(filepath.Join(owned.Directory, "runtime.db"))
				}
				runner := newStorageCutoverRunner()
				interrupted := false
				runner.barrier = func(ctx context.Context, name string) error {
					if name == barrier && !interrupted {
						interrupted = true
						return errors.New("synthetic interruption")
					}
					return ctx.Err()
				}
				_, err := runner.prepare(context.Background(), owned, time.Date(2026, 10, 17, 1, 2, 3, 0, time.UTC), nil)
				if !interrupted || !errors.Is(err, ErrSchemaUpgradeIncomplete) {
					t.Fatalf("barrier not incomplete: %v %v", interrupted, err)
				}
				layout, err := PrepareStorageOwned(context.Background(), owned, time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), nil)
				if err != nil {
					t.Fatal(err)
				}
				control, err := runtimepersistence.OpenControl(context.Background(), runtimepersistence.ControlOptions{DatabasePath: filepath.Join(owned.Directory, layout.Control.RelativePath), LayoutID: layout.ID, BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
				if err != nil {
					t.Fatal(err)
				}
				state, err := control.SchemaStateReader().ReadSchemaState(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if err = control.Shutdown(context.Background()); err != nil {
					t.Fatal(err)
				}
				expected := "2026-10-03T00:00:00Z"
				if revision == 0 {
					expected = "2026-10-17T01:02:03Z"
				}
				if state.InitializedAt != expected {
					t.Fatalf("installation replaced: %s", state.InitializedAt)
				}
				if revision != 0 {
					archived, err := os.ReadFile(filepath.Join(owned.Directory, layout.Archives[0].RelativeDirectory, "runtime.db"))
					if err != nil || string(archived) != string(original) {
						t.Fatal("original archive bytes changed")
					}
				}
				if _, err = os.Stat(filepath.Join(owned.Directory, schemaUpgradeReceiptName)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("receipt not consumed")
				}
			})
		}
	}
}

func TestOwnedStorageNormalStartDoesNotOpenHistory(t *testing.T) {
	owned := storageCutoverFixture(t, 2)
	first, err := PrepareStorageOwned(context.Background(), owned, time.Now().UTC(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(owned.Directory, first.Archives[0].RelativeDirectory, "runtime.db")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(owned.Directory, "observations.db"), []byte("unavailable optional source"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := newStorageCutoverRunner()
	runner.extract = func(context.Context, runtimepersistence.ControlExtraction) (runtimepersistence.ControlExtractionProof, error) {
		t.Fatal("normal start extracted history")
		return runtimepersistence.ControlExtractionProof{}, nil
	}
	runner.barrier = func(ctx context.Context, name string) error { t.Fatalf("normal start mutated %s", name); return nil }
	second, err := runner.prepare(context.Background(), owned, time.Now().UTC(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("layout changed on normal start")
	}
	if err = os.Remove(filepath.Join(owned.Directory, "runtime.db")); err != nil {
		t.Fatal(err)
	}
	if _, err = PrepareStorageOwned(context.Background(), owned, time.Now().UTC(), nil); err == nil {
		t.Fatal("missing declared control created fresh authority")
	}
}

func TestOwnedStorageProgressAndSingleDeadline(t *testing.T) {
	owned := storageCutoverFixture(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runner := newStorageCutoverRunner()
	var phases []StoragePhase
	var deadline time.Time
	runner.barrier = func(work context.Context, name string) error {
		if d, ok := work.Deadline(); !ok || (!deadline.IsZero() && !d.Equal(deadline)) {
			t.Fatal("deadline reset")
		} else {
			deadline = d
		}

		return work.Err()
	}
	reachedCopy := false
	runner.copyFile = func(work context.Context, _, _ string, _ os.FileInfo) error {
		reachedCopy = true
		if !reflect.DeepEqual(phases, []StoragePhase{SchemaUpgradePreparing, SchemaUpgradeRunning}) {
			t.Fatalf("copy precedes upgrade phase: %v", phases)
		}
		actual, _ := work.Deadline()
		parent, _ := ctx.Deadline()
		if !actual.Equal(parent) {
			t.Fatal("explicit shorter deadline replaced")
		}
		<-work.Done()
		return work.Err()
	}

	_, err := runner.prepare(ctx, owned, time.Now().UTC(), func(p StoragePhase) error { phases = append(phases, p); return nil })
	if !reachedCopy {
		t.Fatal("controlled copy was not reached")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("short deadline not honored: %v", err)
	}
	if _, err = os.Stat(filepath.Join(owned.Directory, "storage-layout.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("background activation escaped timeout")
	}
}

func TestOwnedStorageResumeRecertifiesProjectionAndManifest(t *testing.T) {
	for _, damage := range []string{"projection", "manifest"} {
		t.Run(damage, func(t *testing.T) {
			owned := storageCutoverFixture(t, 2)
			runner := newStorageCutoverRunner()
			stop := "receipt-ready"
			if damage == "manifest" {
				stop = "archive-manifest"
			}
			runner.barrier = func(ctx context.Context, name string) error {
				if name == stop {
					return errors.New("stop before publication")
				}
				return ctx.Err()
			}
			if _, err := runner.prepare(context.Background(), owned, time.Now().UTC(), nil); err == nil {
				t.Fatal("fixture did not interrupt")
			}
			encoded, _, err := readStorageDocument(filepath.Join(owned.Directory, schemaUpgradeReceiptName))
			if err != nil {
				t.Fatal(err)
			}
			var receipt roleCutoverReceipt
			if err = decodeStorageDocument(encoded, &receipt); err != nil {
				t.Fatal(err)
			}
			if damage == "projection" {
				staged := filepath.Join(owned.Directory, ".storage-cutover-"+receipt.Layout.ID, "control.db")
				db, err := sql.Open("sqlite", staged)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(`UPDATE code_library_collections SET display_name='Not original'`); err != nil {
					t.Fatal(err)
				}
				if err = db.Close(); err != nil {
					t.Fatal(err)
				}
				artifact, err := hashCutoverArtifact(context.Background(), staged, "control.db")
				if err != nil {
					t.Fatal(err)
				}
				receipt.ControlSHA256 = artifact.SHA256
				encoded, err = json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(owned.Directory, schemaUpgradeReceiptName), encoded, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = os.WriteFile(filepath.Join(owned.Directory, receipt.Layout.Archives[0].RelativeDirectory, "archive-manifest.json"), []byte(`{"schema":"conflicting"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = PrepareStorageOwned(context.Background(), owned, time.Now().UTC(), nil); err == nil {
				t.Fatal("unverified resumed artifacts activated")
			}
		})
	}
}

func TestOwnedStorageRemovesDerivedHistoryCopies(t *testing.T) {
	owned := storageCutoverFixture(t, 2)
	layout, err := PrepareStorageOwned(context.Background(), owned, time.Now().UTC(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(owned.Directory, ".storage-cutover-"+layout.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("undeclared derived history remains after activation")
	}
}

func TestOwnedStorageReconcilesOldReceiptBeforeRoleCutover(t *testing.T) {
	owned := storageCutoverFixture(t, 1)
	old := newSchemaUpgradeRunner()
	old.upgradeDatabase = func(ctx context.Context, in runtimepersistence.MigrationInput) (runtimepersistence.MigrationResult, error) {
		result, err := runtimepersistence.UpgradeRevision1(ctx, in)
		if err != nil {
			return result, err
		}
		return result, errors.New("interrupted old commit acknowledgment")
	}
	if _, err := old.upgrade(context.Background(), owned.Directory, "", owned.Runtime, owned.Host, time.Now().UTC(), time.Minute, nil); !errors.Is(err, ErrSchemaUpgradeIncomplete) {
		t.Fatal("old receipt fixture did not interrupt")
	}
	layout, err := PrepareStorageOwned(context.Background(), owned, time.Now().UTC(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(layout.Archives) != 1 || layout.Archives[0].Database.Revision != 2 {
		t.Fatal("old protocol was not reconciled first")
	}
	control, err := runtimepersistence.OpenControl(context.Background(), runtimepersistence.ControlOptions{DatabasePath: filepath.Join(owned.Directory, "runtime.db"), LayoutID: layout.ID, BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
	if err != nil {
		t.Fatal(err)
	}
	if err = control.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func retainOwnedStorageWAL(t *testing.T, owned OwnedStorage) {
	t.Helper()
	path := filepath.Join(owned.Directory, "runtime.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL;PRAGMA wal_autocheckpoint=0;INSERT INTO code_library_collections VALUES('wal-only','Retained WAL authority')`); err != nil {
		t.Fatal(err)
	}
	artifacts := map[string][]byte{}
	for _, name := range []string{"runtime.db", "runtime.db-wal", "runtime.db-shm"} {
		encoded, err := os.ReadFile(filepath.Join(owned.Directory, name))
		if err != nil {
			t.Fatal(err)
		}
		artifacts[name] = encoded
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	for name, encoded := range artifacts {
		if err = os.WriteFile(filepath.Join(owned.Directory, name), encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOwnedStorageEveryWALDurabilityBarrierResumes(t *testing.T) {
	baseline := storageCutoverFixture(t, 2)
	retainOwnedStorageWAL(t, baseline)
	runner := newStorageCutoverRunner()
	var names []string
	seen := map[string]bool{}
	runner.barrier = func(ctx context.Context, name string) error {
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
		return ctx.Err()
	}
	if _, err := runner.prepare(context.Background(), baseline, time.Now().UTC(), nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			owned := storageCutoverFixture(t, 2)
			retainOwnedStorageWAL(t, owned)
			original, err := cutoverInventory(context.Background(), owned.Directory)
			if err != nil {
				t.Fatal(err)
			}
			interrupted := false
			runner := newStorageCutoverRunner()
			runner.barrier = func(ctx context.Context, current string) error {
				if name == current && !interrupted {
					interrupted = true
					return errors.New("interrupted durability barrier")
				}
				return ctx.Err()
			}
			if _, err = runner.prepare(context.Background(), owned, time.Now().UTC(), nil); err == nil || !interrupted {
				t.Fatalf("barrier not reached: %s %v", name, err)
			}
			layout, err := PrepareStorageOwned(context.Background(), owned, time.Now().UTC(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = verifyCutoverInventory(context.Background(), filepath.Join(owned.Directory, layout.Archives[0].RelativeDirectory), original); err != nil {
				t.Fatal("original DB/WAL/SHM changed")
			}
			db, err := sql.Open("sqlite", "file:"+filepath.Join(owned.Directory, "runtime.db")+"?mode=ro")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var label string
			if err = db.QueryRow(`SELECT display_name FROM code_library_collections WHERE collection_id='wal-only'`).Scan(&label); err != nil || label != "Retained WAL authority" {
				t.Fatal("WAL-only control lost")
			}
		})
	}
}

func TestOwnedStorageResurrectedActivatedReceiptPreservesNewAuthority(t *testing.T) {
	owned := storageCutoverFixture(t, 2)
	runner := newStorageCutoverRunner()
	var activated []byte
	runner.barrier = func(ctx context.Context, name string) error {
		if name == "before-receipt-removal" {
			var err error
			activated, err = os.ReadFile(filepath.Join(owned.Directory, schemaUpgradeReceiptName))
			if err != nil {
				t.Fatal(err)
			}
		}
		if name == "before-receipt-removal-sync" {
			return errors.New("unlink visible; directory durability unknown")
		}
		return ctx.Err()
	}
	if _, err := runner.prepare(context.Background(), owned, time.Now().UTC(), nil); err == nil {
		t.Fatal("fixture did not interrupt unlink durability")
	}
	layout, err := PrepareStorageOwned(context.Background(), owned, time.Now().UTC(), nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(owned.Directory, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE code_library_collections SET display_name='New admitted authority' WHERE collection_id='preserved'`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(owned.Directory, layout.Archives[0].RelativeDirectory, "runtime.db")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(owned.Directory, schemaUpgradeReceiptName), activated, 0600); err != nil {
		t.Fatal(err)
	}
	resumed := newStorageCutoverRunner()
	resumed.extract = func(context.Context, runtimepersistence.ControlExtraction) (runtimepersistence.ControlExtractionProof, error) {
		t.Fatal("activated receipt re-extracted old authority")
		return runtimepersistence.ControlExtractionProof{}, nil
	}
	if _, err = resumed.prepare(context.Background(), owned, time.Now().UTC(), nil); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", filepath.Join(owned.Directory, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var label string
	if err = db.QueryRow(`SELECT display_name FROM code_library_collections WHERE collection_id='preserved'`).Scan(&label); err != nil || label != "New admitted authority" {
		t.Fatal("new authority rolled back")
	}
}

func TestOwnedStorageRefusesGuardAndPathAmbiguity(t *testing.T) {
	for _, damage := range []string{"released-host", "replaced-root", "archive-symlink", "duplicate-original", "unexpected-wal"} {
		t.Run(damage, func(t *testing.T) {
			owned := storageCutoverFixture(t, 2)
			before, err := os.ReadFile(filepath.Join(owned.Directory, "runtime.db"))
			if err != nil {
				t.Fatal(err)
			}
			runner := newStorageCutoverRunner()
			injected := false
			originalRoot := owned.Directory
			runner.barrier = func(ctx context.Context, name string) error {
				if name != "receipt-ready" || injected {
					return ctx.Err()
				}
				injected = true
				encoded, _, err := readStorageDocument(filepath.Join(owned.Directory, schemaUpgradeReceiptName))
				if err != nil {
					t.Fatal(err)
				}
				var receipt roleCutoverReceipt
				if err = decodeStorageDocument(encoded, &receipt); err != nil {
					t.Fatal(err)
				}
				switch damage {
				case "released-host":
					if err = owned.Host.Release(); err != nil {
						t.Fatal(err)
					}
				case "replaced-root":
					originalRoot = owned.Directory + "-original"
					if err = os.Rename(owned.Directory, originalRoot); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { os.RemoveAll(originalRoot) })
					if err = os.Mkdir(owned.Directory, 0700); err != nil {
						t.Fatal(err)
					}
					for _, lock := range []string{"runtime-data.lock", "server.lock"} {
						if err = os.Link(filepath.Join(originalRoot, lock), filepath.Join(owned.Directory, lock)); err != nil {
							t.Fatal(err)
						}
					}
				case "archive-symlink":
					if err = os.Symlink(t.TempDir(), filepath.Join(owned.Directory, "archives")); err != nil {
						t.Fatal(err)
					}
				case "duplicate-original":
					archive := filepath.Join(owned.Directory, receipt.Layout.Archives[0].RelativeDirectory)
					if err = os.MkdirAll(archive, 0700); err != nil {
						t.Fatal(err)
					}
					if err = os.Link(filepath.Join(owned.Directory, "runtime.db"), filepath.Join(archive, "runtime.db")); err != nil {
						t.Fatal(err)
					}
				case "unexpected-wal":
					if err = os.WriteFile(filepath.Join(owned.Directory, "runtime.db-wal"), []byte("unexpected WAL"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}
			if _, err = runner.prepare(context.Background(), owned, time.Now().UTC(), nil); err == nil || !injected {
				t.Fatalf("ambiguous ownership/path admitted: %v", err)
			}
			after, err := os.ReadFile(filepath.Join(originalRoot, "runtime.db"))
			if err != nil || string(after) != string(before) {
				t.Fatal("ambiguous original was moved or replaced")
			}
		})
	}
}

func TestOwnedStorageNormalAdmissionRequiresDirectoryDurability(t *testing.T) {
	owned := storageCutoverFixture(t, 0)
	if _, err := PrepareStorageOwned(context.Background(), owned, time.Now().UTC(), nil); err != nil {
		t.Fatal(err)
	}
	runner := newStorageCutoverRunner()
	called := false
	runner.syncPath = func(path string) error {
		if path != owned.Directory {
			t.Fatal("normal admission touched nonroot path")
		}
		called = true
		return errors.New("directory sync unavailable")
	}
	if _, err := runner.prepare(context.Background(), owned, time.Now().UTC(), nil); err == nil || !called {
		t.Fatal("uncertain directory admitted normal writes")
	}
}

func TestOwnedStorageSlowCopyUsesUpgradeDeadlineAndBothGuards(t *testing.T) {
	owned := storageCutoverFixture(t, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := newStorageCutoverRunner()
	entered := make(chan struct{})
	done := make(chan error, 1)
	var phases []StoragePhase
	var deadline time.Time
	runner.barrier = func(work context.Context, _ string) error {
		d, ok := work.Deadline()
		if !ok || !deadline.IsZero() && !deadline.Equal(d) {
			return errors.New("upgrade deadline reset")
		}
		deadline = d
		return work.Err()
	}
	runner.copyFile = func(work context.Context, _, _ string, _ os.FileInfo) error {
		if !reflect.DeepEqual(phases, []StoragePhase{SchemaUpgradePreparing, SchemaUpgradeRunning}) {
			return errors.New("upgrade phase did not precede slow copy")
		}
		d, ok := work.Deadline()
		remaining := time.Until(d)
		if !ok || remaining <= 2*time.Minute || remaining > 15*time.Minute {
			return errors.New("copy uses preparation budget instead of upgrade budget")
		}
		close(entered)
		<-work.Done()
		return work.Err()
	}
	go func() {
		_, err := runner.prepare(ctx, owned, time.Now().UTC(), func(p StoragePhase) error { phases = append(phases, p); return nil })
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("copy not reached: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("copy never started")
	}
	if guard, err := instanceguard.Acquire(filepath.Join(owned.Directory, "server.lock")); err == nil {
		guard.Release()
		t.Fatal("second host entered during copy")
	} else if !errors.Is(err, instanceguard.ErrAlreadyOwned) {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("copy cancellation lost: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled preparation remained active")
	}
	if _, err := os.Stat(filepath.Join(owned.Directory, storageLayoutName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled copy activated in background")
	}
}

func TestOwnedStorageDowngradeAndMissingGuardRefuse(t *testing.T) {
	owned := storageCutoverFixture(t, 2)
	if _, err := PrepareStorageOwned(context.Background(), OwnedStorage{Directory: owned.Directory, Runtime: owned.Runtime}, time.Now().UTC(), nil); err == nil {
		t.Fatal("missing host guard admitted")
	}
	runner := newStorageCutoverRunner()
	runner.barrier = func(ctx context.Context, name string) error {
		if name == "receipt-ready" {
			return errors.New("stop at pending role receipt")
		}
		return ctx.Err()
	}
	if _, err := runner.prepare(context.Background(), owned, time.Now().UTC(), nil); err == nil {
		t.Fatal("fixture did not interrupt")
	}
	if _, exists, err := readSchemaUpgradeReceipt(owned.Directory); err == nil || !exists {
		t.Fatal("old receipt parser accepted role cutover")
	}
	if _, err := runtimepersistence.Open(context.Background(), runtimepersistence.Options{DatabasePath: filepath.Join(owned.Directory, "runtime.db"), BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout}); err == nil {
		t.Fatal("ordinary writer bypassed pending receipt")
	}
	if _, err := PrepareStorageOwned(context.Background(), owned, time.Now().UTC(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimepersistence.InspectSchema(context.Background(), filepath.Join(owned.Directory, "runtime.db"), time.Second); !errors.Is(err, runtimepersistence.ErrUnsupportedSchema) {
		t.Fatal("released schema predicate accepted completed control role")
	}
}

func TestOwnedStorageStageOwnershipCannotBeSubstituted(t *testing.T) {
	owned := storageCutoverFixture(t, 2)
	runner := newStorageCutoverRunner()
	extracted := false
	runner.extract = func(context.Context, runtimepersistence.ControlExtraction) (runtimepersistence.ControlExtractionProof, error) {
		extracted = true
		return runtimepersistence.ControlExtractionProof{}, errors.New("substituted stage reached extractor")
	}
	runner.barrier = func(ctx context.Context, name string) error {
		if name != "source-copy" {
			return ctx.Err()
		}
		encoded, _, err := readStorageDocument(filepath.Join(owned.Directory, schemaUpgradeReceiptName))
		if err != nil {
			t.Fatal(err)
		}
		var receipt roleCutoverReceipt
		if err = decodeStorageDocument(encoded, &receipt); err != nil {
			t.Fatal(err)
		}
		stage := filepath.Join(owned.Directory, ".storage-cutover-"+receipt.Layout.ID)
		moved := stage + "-original"
		if err = os.Rename(stage, moved); err != nil {
			t.Fatal(err)
		}
		if err = os.Mkdir(stage, 0700); err != nil {
			t.Fatal(err)
		}
		for _, lock := range []string{"runtime-data.lock", "server.lock"} {
			if err = os.Link(filepath.Join(moved, lock), filepath.Join(stage, lock)); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	if _, err := runner.prepare(context.Background(), owned, time.Now().UTC(), nil); err == nil {
		t.Fatal("substituted stage admitted")
	}
	if extracted {
		t.Fatal("extractor entered a directory not held by its private guards")
	}
}
