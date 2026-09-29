package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
)

// schema.sql and schemaRevision change together. When this fails after an
// edit to schema.sql, bump schemaRevision and record the new digest here.
func TestSchemaRevisionTracksSchemaFile(t *testing.T) {
	t.Parallel()
	const revision1 = "393b7d67f8ffd1952fc516a150e1a2a68707b67536fbc2e7f5a7f17208424cb1"
	digest := sha256.Sum256([]byte(schemaSQL))
	if schemaRevision != 1 || hex.EncodeToString(digest[:]) != revision1 {
		t.Fatalf("schema.sql changed (sha256 %x) without a schemaRevision bump", digest)
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
