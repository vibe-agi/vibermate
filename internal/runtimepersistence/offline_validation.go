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

	"github.com/vibe-agi/vibermate/internal/recording"
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
// restore. Recovery and integrity checks run on an independently owned DB/WAL
// copy; the input and its sidecars remain byte-for-byte unchanged.
func ValidateOfflineDatabase(ctx context.Context, path string) (state SchemaState, result error) {
	result = withOfflineDatabaseCopy(ctx, path, func(copyPath string) error {
		var err error
		state, err = validateOfflineMonolith(ctx, copyPath)
		return err
	})
	return state, result
}

// Always copy, including checkpointed files. No SQLite connection is opened on
// the input. Copy identities/digests are rechecked after the last private read.
func withOfflineDatabaseCopy(ctx context.Context, path string, work func(string) error) (result error) {
	if ctx == nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrInvalidDatabasePath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("", "vibermate-offline-")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, os.RemoveAll(temporary)) }()
	target := filepath.Join(temporary, "runtime.db")
	var originals []schemaInspectionFile
	for _, suffix := range []string{"", "-wal", "-journal"} {
		info, err := os.Lstat(path + suffix)
		if suffix != "" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || suffix == "" && info.Size() == 0 {
			return ErrInvalidDatabasePath
		}
		original, err := copySchemaInspectionFile(ctx, path+suffix, target+suffix)
		if err != nil {
			return err
		}
		originals = append(originals, original)
	}
	result = work(target)
	for _, original := range originals {
		if err := verifySchemaInspectionFile(ctx, original); err != nil {
			result = errors.Join(result, err)
		}
	}
	// A newly appearing sidecar is also a source change.
	for _, suffix := range []string{"-wal", "-journal"} {
		found := false
		for _, original := range originals {
			if original.path == path+suffix {
				found = true
			}
		}
		if !found {
			if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, ErrUnsupportedSchema)
			}
		}
	}
	return result
}

func validateOfflineMonolith(ctx context.Context, path string) (SchemaState, error) {
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

// OfflineRoleState contains only the fixed-size required coordinates needed by
// stopped-directory exports. No live Store or SQL owner escapes validation.
type OfflineRoleState struct {
	Database SchemaState
	Cleanup  RecordingCleanupState
	Stamp    recording.InspectionStamp
}

// ValidateOfflineRole validates an explicit existing role on a private copy.
// Optional history must carry the entire required denial prefix and valid tail.
func ValidateOfflineRole(ctx context.Context, path, identity string, revision int64, layout, archive string, expected recording.InspectionStamp) (state OfflineRoleState, result error) {
	result = withOfflineDatabaseCopy(ctx, path, func(copyPath string) (result error) {
		var role roleSchema
		switch {
		case identity == controlSchemaIdentity && revision == 3:
			role = roleSchema{identity: identity, revision: revision, ddl: controlSchemaSQL, layout: layout}
		case identity == observationSchemaIdentity && revision == 1:
			role = roleSchema{identity: identity, revision: revision, ddl: observationSchemaSQL, layout: layout, archive: archive}
		default:
			return ErrUnsupportedSchema
		}
		db := sql.OpenDB(newSQLiteReadConnector(copyPath, DefaultBusyTimeout))
		db.SetMaxOpenConns(1)
		defer func() { result = errors.Join(result, db.Close()) }()
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = role.verify(ctx, tx); err != nil {
			return err
		}
		if err = extractionIntegrity(ctx, tx, `PRAGMA integrity_check`); err != nil {
			return err
		}
		if err = extractionNoRows(ctx, tx, `PRAGMA foreign_key_check`); err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, `SELECT schema_identity,schema_revision,initialized_at FROM runtime_metadata WHERE singleton=1`).Scan(&state.Database.Identity, &state.Database.Revision, &state.Database.InitializedAt); err != nil {
			return err
		}
		initialized, err := time.Parse(time.RFC3339Nano, state.Database.InitializedAt)
		if err != nil || initialized.IsZero() || initialized.Location() != time.UTC {
			return ErrUnsupportedSchema
		}
		if identity == controlSchemaIdentity {
			if err = validateRequiredControlRows(ctx, tx); err != nil {
				return err
			}
			c := &state.Cleanup
			if err = tx.QueryRowContext(ctx, `SELECT requested_revision,completed_revision,source_generation,active_revision,cutoff_unix_ms,active_cutoff_unix_ms FROM recording_cleanup_state WHERE singleton=1`).Scan(&c.RequestedRevision, &c.CompletedRevision, &c.SourceGeneration, &c.ActiveRevision, &c.CutoffMillis, &c.ActiveCutoffMillis); err != nil {
				return err
			}
			var hash []byte
			if err = tx.QueryRowContext(ctx, `SELECT identity_count,chain_hash FROM recording_denial_coverage WHERE singleton=1`).Scan(&state.Stamp.DenialCount, &hash); err != nil || len(hash) != 32 {
				return errors.Join(ErrRecordingPrivacyUnavailable, err)
			}
			copy(state.Stamp.DenialHash[:], hash)
		} else if _, err = readRecordingDenialCoverage(ctx, tx, "main", expected); err != nil {
			return err
		}
		return tx.Commit()
	})
	return state, result
}
