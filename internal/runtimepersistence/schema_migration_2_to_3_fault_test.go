package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func (f migrationFixture) input2(t *testing.T) Revision2MigrationInput {
	t.Helper()
	v, err := VerifyRevision2MigrationDatabase(context.Background(), filepath.Join(f.source, "runtime.db"), filepath.Join(f.snapshot, "runtime.db"), f.owner, f.snapshotOwner, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return Revision2MigrationInput{Database: v, ReconcileTimeout: 30 * time.Second}
}
func TestRevision2SwitchRequiresFreshVerifiedSnapshot(t *testing.T) {
	f := populatedV2MigrationFixture(t, false)
	db := openSQLiteForTest(t, filepath.Join(f.snapshot, "runtime.db"))
	nativeExec(t, db, `UPDATE runtime_usage_policy SET retention_days=91`)
	db.Close()
	if _, err := VerifyRevision2MigrationDatabase(context.Background(), filepath.Join(f.source, "runtime.db"), filepath.Join(f.snapshot, "runtime.db"), f.owner, f.snapshotOwner, time.Second); err == nil {
		t.Fatal("different snapshot certified")
	}
	if _, err := UpgradeRevision2(context.Background(), Revision2MigrationInput{}); err == nil {
		t.Fatal("unverified upgrade accepted")
	}
}
func TestRevision2SwitchCommitThenErrorProvesV3(t *testing.T) {
	f := populatedV2MigrationFixture(t, false)
	runner := revision2MigrationRunner{commit: func(ctx context.Context, c *sql.Conn) error {
		if _, err := c.ExecContext(ctx, "COMMIT"); err != nil {
			return err
		}
		return errors.New("injected commit result")
	}}
	result, err := runner.upgrade(context.Background(), f.input2(t))
	if err != nil || !result.Committed {
		t.Fatalf("commit proof=%+v %v", result, err)
	}
	f.assertLegacyPreserved(t)
}

func TestRevision2SwitchDDLFailureAndCancelPreserveV2(t *testing.T) {
	for _, scenario := range []string{"ddl", "cancel", "rollback_commit_error"} {
		t.Run(scenario, func(t *testing.T) {
			f := populatedV2MigrationFixture(t, false)
			input := f.input2(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := revision2MigrationRunner{afterDDL: func(context.Context, *sql.Conn) error {
				if scenario == "cancel" {
					cancel()
				}
				return errors.New("injected DDL failure")
			}}
			if scenario == "rollback_commit_error" {
				runner.afterDDL = nil
				runner.commit = func(ctx context.Context, c *sql.Conn) error {
					_, err := c.ExecContext(ctx, "ROLLBACK")
					if err != nil {
						return err
					}
					return errors.New("injected uncommitted result")
				}
			}
			result, err := runner.upgrade(ctx, input)
			if result.Committed || !errors.Is(err, ErrMigrationNotCommitted) {
				t.Fatalf("rollback=%+v %v", result, err)
			}
			f.assertLegacyPreserved(t)
			state, err := InspectSchema(context.Background(), filepath.Join(f.source, "runtime.db"), time.Second)
			if err != nil || state.Revision != 2 {
				t.Fatal("failure changed old schema")
			}
		})
	}
}
func TestRevision2SwitchReconcileUnavailablePreventsOpen(t *testing.T) {
	f := populatedV2MigrationFixture(t, false)
	input := f.input2(t)
	if err := os.WriteFile(filepath.Join(f.source, SchemaUpgradeReceiptName), []byte("synthetic-pending-before-commit"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := revision2MigrationRunner{commit: func(ctx context.Context, c *sql.Conn) error {
		if _, err := c.ExecContext(ctx, "COMMIT"); err != nil {
			return err
		}
		return os.Rename(filepath.Join(f.snapshot, "runtime.db"), filepath.Join(f.snapshot, "unavailable.db"))
	}}
	result, err := runner.upgrade(context.Background(), input)
	if result.Committed || !errors.Is(err, ErrMigrationOutcomeUnknown) {
		t.Fatalf("unavailable proof=%+v %v", result, err)
	}
	state, err := InspectSchema(context.Background(), filepath.Join(f.source, "runtime.db"), time.Second)
	if err != nil || state.Revision != 3 {
		t.Fatal("unknown outcome blindly restored data")
	}
	f.assertLegacyPreserved(t)
	store, openErr := Open(context.Background(), Options{DatabasePath: filepath.Join(f.source, "runtime.db"), BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if store != nil {
		shutdownTestStore(t, store)
		t.Fatal("unknown result opened Store")
	}
	if !errors.Is(openErr, ErrMigrationRequired) {
		t.Fatal(openErr)
	}
}
func TestRevision2SwitchReceiptRejectsChangesAndReleasedGuards(t *testing.T) {
	for _, scenario := range []string{"snapshot", "source", "released"} {
		t.Run(scenario, func(t *testing.T) {
			f := populatedV2MigrationFixture(t, false)
			input := f.input2(t)
			if scenario == "released" {
				f.snapshotOwner.Release()
			} else {
				dir := f.source
				if scenario == "snapshot" {
					dir = f.snapshot
				}
				db := openSQLiteForTest(t, filepath.Join(dir, "runtime.db"))
				nativeExec(t, db, `UPDATE runtime_usage_policy SET retention_days=91`)
				db.Close()
			}
			if result, err := UpgradeRevision2(context.Background(), input); !errors.Is(err, ErrMigrationOutcomeUnknown) || result.Committed {
				t.Fatalf("changed verified receipt falsely classified intact rollback: %v", err)
			}
		})
	}
}
func TestRevision2SwitchReconcileBudgetExpiryIsUnknown(t *testing.T) {
	f := populatedV2MigrationFixture(t, false)
	input := f.input2(t)
	input.ReconcileTimeout = time.Nanosecond
	if err := os.WriteFile(filepath.Join(f.source, SchemaUpgradeReceiptName), []byte("synthetic-pending-before-commit"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := UpgradeRevision2(context.Background(), input)
	if result.Committed || !errors.Is(err, ErrMigrationOutcomeUnknown) {
		t.Fatalf("expired proof=%+v %v", result, err)
	}
	state, err := InspectSchema(context.Background(), filepath.Join(f.source, "runtime.db"), time.Second)
	if err != nil || state.Revision != 3 {
		t.Fatal("expired proof blindly restored known commit")
	}
	f.assertLegacyPreserved(t)
	store, err := Open(context.Background(), Options{DatabasePath: filepath.Join(f.source, "runtime.db"), BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if store != nil {
		shutdownTestStore(t, store)
		t.Fatal("expired proof activated Store")
	}
	if !errors.Is(err, ErrMigrationRequired) {
		t.Fatal(err)
	}
}

func TestRevision2DurabilityFailureKeepsReadableV3Pending(t *testing.T) {
	for _, fault := range []string{"connection_close", "pool_close", "database_sync", "directory_sync"} {
		t.Run(fault, func(t *testing.T) {
			f := populatedV2MigrationFixture(t, false)
			input := f.input2(t)
			if err := os.WriteFile(filepath.Join(f.source, SchemaUpgradeReceiptName), []byte("synthetic-pending"), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			runner := revision2MigrationRunner{}
			switch fault {
			case "connection_close":
				runner.closeConn = func(conn *sql.Conn) error {
					calls++
					if err := conn.Close(); err != nil {
						return err
					}
					return errors.New("injected connection close failure")
				}
			case "pool_close":
				runner.closePool = func(pool *sql.DB) error {
					calls++
					if err := pool.Close(); err != nil {
						return err
					}
					return errors.New("injected pool close failure")
				}
			case "database_sync":
				runner.syncSourceFile = func(ctx context.Context, path string) error {
					calls++
					return errors.New("injected source file sync failure")
				}
			case "directory_sync":
				runner.syncSourceDirectory = func(ctx context.Context, path string) error {
					calls++
					return errors.New("injected source directory sync failure")
				}
			}
			result, err := runner.upgrade(context.Background(), input)
			if !errors.Is(err, ErrMigrationOutcomeUnknown) || result.Committed || calls != 1 {
				t.Fatalf("readable v3 promoted despite %s: result=%+v err=%v calls=%d", fault, result, err, calls)
			}
			state, err := InspectSchema(context.Background(), filepath.Join(f.source, "runtime.db"), time.Second)
			if err != nil || state.Revision != 3 {
				t.Fatal("fault fixture did not leave actual readable v3")
			}
			f.assertLegacyPreserved(t)
			if _, err := os.Stat(filepath.Join(f.source, SchemaUpgradeReceiptName)); err != nil {
				t.Fatal("durability uncertainty lost marker")
			}
			store, err := Open(context.Background(), Options{DatabasePath: filepath.Join(f.source, "runtime.db"), BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if store != nil {
				shutdownTestStore(t, store)
				t.Fatal("uncertain durability opened Store")
			}
			if !errors.Is(err, ErrMigrationRequired) {
				t.Fatal(err)
			}
			result, err = ReconcileRevision2MigrationDatabase(context.Background(), filepath.Join(f.source, "runtime.db"), filepath.Join(f.snapshot, "runtime.db"), f.owner, f.snapshotOwner, input.Database.SourceDigest(), 30*time.Second)
			if err != nil || !result.Committed {
				t.Fatalf("fresh durability recertification did not complete: %+v %v", result, err)
			}
		})
	}
}

func retainedV3MigrationWAL(t *testing.T, f migrationFixture, input Revision2MigrationInput) {
	t.Helper()
	if _, err := UpgradeRevision2(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "builder.db")
	encoded, err := os.ReadFile(filepath.Join(f.source, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	db := openSQLiteForTest(t, path)
	nativeExec(t, db, `PRAGMA wal_autocheckpoint=0; UPDATE runtime_metadata SET schema_revision=2; UPDATE runtime_metadata SET schema_revision=3`)
	for _, suffix := range []string{"", "-wal"} {
		encoded, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.source, "runtime.db")+suffix, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
}
func TestRevision2DurabilityRetainedWALRequiresSuccessfulSync(t *testing.T) {
	f := populatedV2MigrationFixture(t, false)
	input := f.input2(t)
	retainedV3MigrationWAL(t, f, input)
	var files []string
	directorySynced := false
	runner := revision2MigrationRunner{syncSourceFile: func(ctx context.Context, path string) error {
		files = append(files, filepath.Base(path))
		if filepath.Ext(path) == ".db" {
			return syncMigrationSourceFile(ctx, path)
		}
		return errors.New("injected retained WAL sync failure")
	}, syncSourceDirectory: func(context.Context, string) error { directorySynced = true; return nil }}
	result, err := runner.reconcile(context.Background(), input.Database, 30*time.Second)
	if result.Committed || !errors.Is(err, ErrMigrationOutcomeUnknown) || directorySynced {
		t.Fatalf("unsynced WAL promoted: %+v %v", result, err)
	}
	if !reflect.DeepEqual(files, []string{"runtime.db", "runtime.db-wal"}) {
		t.Fatalf("wrong source sync paths: %v", files)
	}
	runner.syncSourceFile = func(ctx context.Context, path string) error { return syncMigrationSourceFile(ctx, path) }
	runner.syncSourceDirectory = func(ctx context.Context, path string) error {
		directorySynced = true
		return syncMigrationSourceDirectory(ctx, path)
	}
	result, err = runner.reconcile(context.Background(), input.Database, 30*time.Second)
	if err != nil || !result.Committed || !directorySynced {
		t.Fatalf("retained WAL did not recertify: %+v %v", result, err)
	}
	f.assertLegacyPreserved(t)
}
func TestRevision2DurabilityJournalGuardAndDeadlineRemainUnknown(t *testing.T) {
	for _, fault := range []string{"rollback_journal", "released_guard", "deadline", "post_barrier_changed_rows", "file_close"} {
		t.Run(fault, func(t *testing.T) {
			f := populatedV2MigrationFixture(t, false)
			input := f.input2(t)
			if _, err := UpgradeRevision2(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			runner := revision2MigrationRunner{}
			timeout := 30 * time.Second
			switch fault {
			case "rollback_journal":
				if err := os.WriteFile(filepath.Join(f.source, "runtime.db-journal"), []byte("unresolved synthetic journal"), 0600); err != nil {
					t.Fatal(err)
				}
			case "released_guard":
				runner.syncSourceFile = func(ctx context.Context, path string) error {
					if err := syncMigrationSourceFile(ctx, path); err != nil {
						return err
					}
					f.owner.Release()
					return nil
				}
			case "deadline":
				timeout = 50 * time.Millisecond
				runner.syncSourceFile = func(ctx context.Context, path string) error { <-ctx.Done(); return ctx.Err() }
			case "post_barrier_changed_rows":
				runner.syncSourceDirectory = func(ctx context.Context, path string) error {
					if err := syncMigrationSourceDirectory(ctx, path); err != nil {
						return err
					}
					file := filepath.Join(f.source, "runtime.db")
					before, err := os.Stat(file)
					if err != nil {
						t.Fatal(err)
					}
					db := openSQLiteForTest(t, file)
					nativeExec(t, db, `UPDATE runtime_usage_policy SET retention_days=91`)
					db.Close()
					// Keep metadata stable so only the independent typed-row proof
					// can catch this injected post-barrier change.
					if err := os.Chtimes(file, before.ModTime(), before.ModTime()); err != nil {
						t.Fatal(err)
					}
					return nil
				}
			case "file_close":
				runner.syncSourceFile = func(ctx context.Context, path string) error {
					if err := syncMigrationSourceFile(ctx, path); err != nil {
						return err
					}
					return errors.New("injected synchronized source file close failure")
				}
			}
			result, err := runner.reconcile(context.Background(), input.Database, timeout)
			if result.Committed || !errors.Is(err, ErrMigrationOutcomeUnknown) {
				t.Fatalf("%s promoted readable v3: %+v %v", fault, result, err)
			}
		})
	}
}
