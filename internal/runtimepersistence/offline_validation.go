package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// validateExistingSchema refuses, before any write (journal_mode itself can
// change the file header), a file that is not a ViberMate database of this
// build's schema revision.
func validateExistingSchema(ctx context.Context, path string, timeout time.Duration) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) || err == nil && info.Size() == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	database := sql.OpenDB(newSQLiteReadConnector(path, timeout))
	defer database.Close()
	revision, err := readSchemaRevision(ctx, database)
	if err != nil {
		return err
	}
	if revision != 0 && revision != schemaRevision {
		return fmt.Errorf("%w: file revision %d, build revision %d", ErrUnsupportedSchema, revision, schemaRevision)
	}
	return nil
}

// ValidateOfflineDatabase verifies a stopped Runtime database before backup or
// restore. Opening read-write is intentional: SQLite must recover a retained
// WAL before integrity and foreign-key checks can describe the durable state.
func ValidateOfflineDatabase(ctx context.Context, path string) (SchemaState, error) {
	if ctx == nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return SchemaState{}, ErrInvalidDatabasePath
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return SchemaState{}, ErrInvalidDatabasePath
	}
	databaseURL := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}
	database, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return SchemaState{}, fmt.Errorf("open offline SQLite database: %w", err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	var integrity string
	if err := database.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return SchemaState{}, errors.Join(ErrUnsupportedSchema, err)
	}
	rows, err := database.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return SchemaState{}, fmt.Errorf("check offline SQLite foreign keys: %w", err)
	}
	if rows.Next() {
		_ = rows.Close()
		return SchemaState{}, ErrUnsupportedSchema
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return SchemaState{}, fmt.Errorf("finish offline SQLite foreign-key check: %w", err)
	}
	var state SchemaState
	if err := database.QueryRowContext(
		ctx,
		`SELECT schema_identity, schema_revision, initialized_at
		 FROM runtime_metadata WHERE singleton = 1`,
	).Scan(&state.Identity, &state.Revision, &state.InitializedAt); err != nil {
		return SchemaState{}, fmt.Errorf("%w: read offline Runtime metadata: %v", ErrUnsupportedSchema, err)
	}
	if state.Identity != schemaIdentity || state.Revision != schemaRevision {
		return SchemaState{}, fmt.Errorf("%w: identity %q revision %d", ErrUnsupportedSchema, state.Identity, state.Revision)
	}
	return state, nil
}
