package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Published runtime1/2/3 are immutable. Current role and combined test adapter
// definitions are separately pinned.
func TestSchemaRevisionTracksSchemaFile(t *testing.T) {
	t.Parallel()
	const revision1 = "393b7d67f8ffd1952fc516a150e1a2a68707b67536fbc2e7f5a7f17208424cb1"
	const revision2 = "d8ef36c8b49439cd82625b2e780bbf5cff9d1be9c7593c94d33ca816cac1ec42"
	if frozen := sha256.Sum256([]byte(schemaV1SQL)); hex.EncodeToString(frozen[:]) != revision1 {
		t.Fatal("frozen v1 changed")
	}
	if frozen := sha256.Sum256([]byte(schemaV2SQL)); hex.EncodeToString(frozen[:]) != revision2 {
		t.Fatal("frozen v2 changed")
	}
	for _, role := range []struct{ sql, pin string }{{controlSchemaSQL, "19c6eda3cd83a8f7836446835164367db49f60573c88aca561e6c381b92cc10a"}, {observationSchemaSQL, "ed9fc1e5f71ca1801789d84488810df23872c0de520dea6360ad5573267899bd"}} {
		digest := sha256.Sum256([]byte(role.sql))
		if hex.EncodeToString(digest[:]) != role.pin {
			t.Fatalf("unreleased role schema pin differs: %x", digest)
		}
	}
	digest := sha256.Sum256([]byte(schemaSQL))
	if schemaRevision != 3 || hex.EncodeToString(digest[:]) != "65fcdece1879048a88f19105fef70e45c354c1d827b5ec70ab7e5a2de4c11313" {
		t.Fatalf("schema.sql changed (sha256 %x) without a schemaRevision bump", digest)
	}
	for _, pin := range []struct{ sql, want string }{{schemaRuntimeV3SQL, "377f9b8af48bd097be77be685de56d5a1af7bb55bb781c20c4bd1bdfefff71a7"}, {migration001To002SQL, "a6bf36f011aa987c2862e45b22d23c6f48d75a3d6f408b50ac9c440c5bd5d90c"}, {migration002To003SQL, "ab85f5c3228ae203d69a2a095b8b9c15d2a051434f1e70c6b42c1c10f3aaf7ff"}} {
		actual := sha256.Sum256([]byte(pin.sql))
		if hex.EncodeToString(actual[:]) != pin.want {
			t.Fatalf("published migration changed: %x", actual)
		}
	}
}

func TestUsageDefaultsApplyOnlyToNewDatabases(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "runtime.db")
	before := time.Now().UTC().Truncate(time.Millisecond)
	store := openTestStore(t, path)
	policy, err := store.UsagePolicy(context.Background())
	if err != nil || !policy.Enabled || policy.RetentionDays != 365 || policy.Revision != 1 ||
		policy.CollectingSince == nil || policy.CollectingSince.Before(before) {
		t.Fatalf("new usage defaults = %+v, %v", policy, err)
	}
	// An old revision-one default is still an existing user's choice.
	if _, err := store.database.Exec(`UPDATE runtime_usage_policy SET enabled=0,retention_days=90,collecting_since_unix_ms=NULL`); err != nil {
		t.Fatal(err)
	}
	shutdownTestStore(t, store)
	store = openTestStore(t, path)
	defer shutdownTestStore(t, store)
	policy, err = store.UsagePolicy(context.Background())
	if err != nil || policy.Enabled || policy.RetentionDays != 90 || policy.Revision != 1 || policy.CollectingSince != nil {
		t.Fatalf("reopening overwrote an existing choice = %+v, %v", policy, err)
	}
}

// A file created by another schema revision is refused before any write and
// left exactly as it was; there is no migration.
func TestOtherSchemaRevisionIsRefusedUnchanged(t *testing.T) {
	t.Parallel()
	for _, other := range []int64{schemaRevision + 100, schemaRevision + 1} {
		ctx := context.Background()
		path := filepath.Join(t.TempDir(), "runtime.db")
		shutdownTestStore(t, openTestStore(t, path))
		database := openSQLiteForTest(t, path)
		if _, err := database.Exec(`UPDATE runtime_metadata SET schema_revision = ?`, other); err != nil {
			t.Fatal(err)
		}
		store, err := Open(ctx, Options{DatabasePath: path, BusyTimeout: DefaultBusyTimeout, CommitReconcileTimeout: DefaultCommitReconcileTimeout})
		if store != nil {
			shutdownTestStore(t, store)
			t.Fatalf("opened a database of revision %d", other)
		}
		if !errors.Is(err, ErrUnsupportedSchema) {
			t.Fatalf("revision %d error = %v", other, err)
		}
		var recorded int64
		if err := database.QueryRow(`SELECT schema_revision FROM runtime_metadata`).Scan(&recorded); err != nil || recorded != other {
			t.Fatalf("refused open changed revision %d to %d (%v)", other, recorded, err)
		}
	}
}

func openSQLiteForTest(t *testing.T, path string) *sql.DB {
	t.Helper()
	database := sql.OpenDB(newSQLiteConnector(path, DefaultBusyTimeout))
	t.Cleanup(func() { _ = database.Close() })
	return database
}
