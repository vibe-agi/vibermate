package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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
	// Open is the retained combined repository test adapter. Role startup and
	// production offline/source inspection never use this private layout reader.
	runner := newSchemaInspectionRunner()
	runner.readSchema = func(ctx context.Context, path string, timeout time.Duration, immutable bool) (SchemaState, error) {
		connector := newSQLiteReadConnector(path, timeout)
		if immutable {
			connector.(*sqliteConnector).dsn += "&immutable=1"
		}
		db := sql.OpenDB(connector)
		defer db.Close()
		db.SetMaxOpenConns(1)
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return SchemaState{}, err
		}
		defer tx.Rollback()
		revision, err := readSchemaRevision(ctx, tx)
		if err != nil {
			return SchemaState{}, err
		}
		if revision == 1 || revision == 2 {
			if _, err = inspectSupportedSchema(ctx, tx); err != nil {
				return SchemaState{}, err
			}
			return SchemaState{}, ErrMigrationRequired
		}
		if revision != schemaRevision {
			return SchemaState{}, ErrUnsupportedSchema
		}
		observed, err := schemaObjects(ctx, tx)
		if err != nil {
			return SchemaState{}, err
		}
		reference, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			return SchemaState{}, err
		}
		defer reference.Close()
		if _, err = reference.ExecContext(ctx, schemaSQL); err != nil {
			return SchemaState{}, err
		}
		expected, err := schemaObjects(ctx, reference)
		if err != nil || !slices.Equal(observed, expected) {
			return SchemaState{}, ErrUnsupportedSchema
		}
		var state SchemaState
		if err = tx.QueryRowContext(ctx, `SELECT schema_identity,schema_revision,initialized_at FROM runtime_metadata WHERE singleton=1`).Scan(&state.Identity, &state.Revision, &state.InitializedAt); err != nil {
			return SchemaState{}, ErrUnsupportedSchema
		}
		initialized, err := time.Parse(time.RFC3339Nano, state.InitializedAt)
		if err != nil || initialized.IsZero() || initialized.Location() != time.UTC {
			return SchemaState{}, ErrUnsupportedSchema
		}
		return state, nil
	}
	_, err = runner.inspect(ctx, path, timeout)
	return err
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
	state, err := inspectSupportedSchema(ctx, database)
	if err != nil || state.Revision == 0 {
		return SchemaState{}, errors.Join(ErrUnsupportedSchema, err)
	}
	return state, nil
}
