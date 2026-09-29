package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

func openRawDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	database := sql.OpenDB(newSQLiteConnector(path, DefaultBusyTimeout))
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestFreshDatabaseAppliesEveryMigration(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	state, err := store.SchemaStateReader().ReadSchemaState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Identity != schemaIdentity || state.Revision != latestSchemaRevision() || state.InitializedAt == "" {
		t.Fatalf("fresh schema state = %+v", state)
	}
}

// An older database is brought forward at open, in one transaction, keeping
// its data. This is how every later schema change ships.
func TestOlderRevisionMigratesForwardKeepingData(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openRawDatabase(t, filepath.Join(t.TempDir(), "runtime.db"))
	chain := append([]schemaMigration(nil), migrations...)
	if revision, err := migrateSchema(ctx, database, chain, time.Now()); err != nil || revision != int64(len(chain)) {
		t.Fatalf("baseline revision=%d err=%v", revision, err)
	}
	next := schemaMigration{revision: int64(len(chain) + 1), name: "probe",
		sql: `CREATE TABLE migration_probe(value INTEGER NOT NULL) STRICT; INSERT INTO migration_probe VALUES(7);`}
	revision, err := migrateSchema(ctx, database, append(chain, next), time.Now())
	if err != nil || revision != next.revision {
		t.Fatalf("forward migration revision=%d err=%v", revision, err)
	}
	var value, recorded int64
	if err := database.QueryRow(`SELECT value FROM migration_probe`).Scan(&value); err != nil || value != 7 {
		t.Fatalf("migration effect = %d, %v", value, err)
	}
	if err := database.QueryRow(`SELECT schema_revision FROM runtime_metadata`).Scan(&recorded); err != nil || recorded != next.revision {
		t.Fatalf("recorded revision = %d, %v", recorded, err)
	}
	// Idempotent: reopening at the latest revision applies nothing.
	if revision, err := migrateSchema(ctx, database, append(chain, next), time.Now()); err != nil || revision != next.revision {
		t.Fatalf("second open revision=%d err=%v", revision, err)
	}
}

func TestFailedMigrationLeavesTheDatabaseUnchanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openRawDatabase(t, filepath.Join(t.TempDir(), "runtime.db"))
	chain := append([]schemaMigration(nil), migrations...)
	if _, err := migrateSchema(ctx, database, chain, time.Now()); err != nil {
		t.Fatal(err)
	}
	broken := schemaMigration{revision: int64(len(chain) + 1), name: "broken",
		sql: `CREATE TABLE half_applied(value INTEGER) STRICT; INSERT INTO table_that_does_not_exist VALUES(1);`}
	if _, err := migrateSchema(ctx, database, append(chain, broken), time.Now()); err == nil {
		t.Fatal("a failing migration reported success")
	}
	var recorded int64
	var tables int
	if err := database.QueryRow(`SELECT schema_revision FROM runtime_metadata`).Scan(&recorded); err != nil || recorded != int64(len(chain)) {
		t.Fatalf("revision after failed migration = %d, %v", recorded, err)
	}
	if err := database.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='half_applied'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatal("a failed migration left part of its changes behind")
	}
}

// A database written by a newer ViberMate is refused before any write.
func TestNewerRevisionIsRefusedUnchanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.db")
	shutdownTestStore(t, openTestStore(t, path))
	database := openRawDatabase(t, path)
	newer := latestSchemaRevision() + 1
	if _, err := database.Exec(`UPDATE runtime_metadata SET schema_revision = ?`, newer); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, Options{DatabasePath: path, BusyTimeout: DefaultBusyTimeout, CommitReconcileTimeout: DefaultCommitReconcileTimeout})
	if store != nil {
		shutdownTestStore(t, store)
		t.Fatal("opened a database from a newer ViberMate")
	}
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("newer revision error = %v", err)
	}
	var recorded int64
	if err := database.QueryRow(`SELECT schema_revision FROM runtime_metadata`).Scan(&recorded); err != nil || recorded != newer {
		t.Fatalf("refused open changed the revision to %d (%v)", recorded, err)
	}
}

func TestMigrationFilesMustBeNumberedWithoutGaps(t *testing.T) {
	t.Parallel()
	for name, files := range map[string]fstest.MapFS{
		"gap":       {"migrations/0001_a.sql": {Data: []byte("SELECT 1;")}, "migrations/0003_c.sql": {Data: []byte("SELECT 1;")}},
		"unnamed":   {"migrations/0001.sql": {Data: []byte("SELECT 1;")}},
		"short":     {"migrations/1_a.sql": {Data: []byte("SELECT 1;")}},
		"not first": {"migrations/0002_b.sql": {Data: []byte("SELECT 1;")}},
		"empty":     {},
	} {
		if _, err := loadMigrations(files); err == nil {
			t.Errorf("%s: invalid migration set accepted", name)
		}
	}
	if loaded, err := loadMigrations(fstest.MapFS{
		"migrations/0001_a.sql": {Data: []byte("SELECT 1;")}, "migrations/0002_b.sql": {Data: []byte("SELECT 2;")},
	}); err != nil || len(loaded) != 2 || loaded[1].name != "b" {
		t.Fatalf("valid set = %+v, %v", loaded, err)
	}
}
