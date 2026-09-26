package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

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
		return SchemaState{}, errors.Join(ErrSchemaBaselineMismatch, err)
	}
	rows, err := database.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return SchemaState{}, fmt.Errorf("check offline SQLite foreign keys: %w", err)
	}
	if rows.Next() {
		_ = rows.Close()
		return SchemaState{}, ErrSchemaBaselineMismatch
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return SchemaState{}, fmt.Errorf("finish offline SQLite foreign-key check: %w", err)
	}
	var state SchemaState
	if err := database.QueryRowContext(
		ctx,
		`SELECT schema_identity, schema_revision, schema_source_sha256, initialized_at
		 FROM runtime_metadata WHERE singleton = 1`,
	).Scan(&state.Identity, &state.Revision, &state.SourceSHA256, &state.InitializedAt); err != nil {
		return SchemaState{}, fmt.Errorf("%w: read offline Runtime metadata: %v", ErrSchemaBaselineMismatch, err)
	}
	sum := sha256.Sum256([]byte(schemaSQL))
	if err := validateSchemaState(state, hex.EncodeToString(sum[:])); err != nil {
		return SchemaState{}, err
	}
	return state, nil
}
