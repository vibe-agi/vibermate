package runtimepersistence

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"os"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/recording"
)

const controlSchemaIdentity = "vibermate-control"
const observationSchemaIdentity = "vibermate-observations"

//go:embed schema/v3.sql
var controlSchemaSQL string

//go:embed schema/observations_v1.sql
var observationSchemaSQL string

type ControlOptions struct {
	DatabasePath, LayoutID              string
	BusyTimeout, CommitReconcileTimeout time.Duration
	NativeStreamLifetime                time.Duration
	NativeHistory                       nativetool.HistoryRecorder
	CaptureObserver                     recording.CaptureObserver
	RecordingScopes                     recording.ScopeIssuer
}

// Owners intentionally do not embed Store: authority callers cannot reach an
// observation repository through the control owner. The private implementation
// shares constructor plumbing, not pools, paths, gates, or role dependencies.
type ControlStore struct{ store *Store }

type roleSchema struct {
	identity             string
	revision             int64
	ddl, layout, archive string
}

func roleID(s string) bool { return len(s) > 0 && len(s) <= 128 && utf8.ValidString(s) }
func OpenControl(ctx context.Context, o ControlOptions) (*ControlStore, error) {
	if !roleID(o.LayoutID) {
		return nil, ErrInvalidDatabasePath
	}
	role := &roleSchema{identity: controlSchemaIdentity, revision: 3, ddl: controlSchemaSQL, layout: o.LayoutID}
	s, err := openStore(ctx, Options{DatabasePath: o.DatabasePath, BusyTimeout: o.BusyTimeout, CommitReconcileTimeout: o.CommitReconcileTimeout, NativeStreamLifetime: o.NativeStreamLifetime}, role)
	if err != nil {
		return nil, err
	}
	s.nativeTools.history = o.NativeHistory
	s.nativeTools.recordingScopes = o.RecordingScopes
	s.captureRepo.captureObserver, s.captureRepo.recordingScopes = o.CaptureObserver, o.RecordingScopes
	s.manualCapture.captureObserver, s.manualCapture.recordingScopes = o.CaptureObserver, o.RecordingScopes
	return &ControlStore{store: s}, nil
}

func (r *roleSchema) validateExisting(ctx context.Context, path string, timeout time.Duration) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return nil
	}
	db := sql.OpenDB(newSQLiteReadConnector(path, timeout))
	defer db.Close()
	return r.verify(ctx, db)
}
func (r *roleSchema) verify(ctx context.Context, db schemaQueryer) error {
	var identity string
	var revision int64
	if err := db.QueryRowContext(ctx, `SELECT schema_identity,schema_revision FROM runtime_metadata WHERE singleton=1`).Scan(&identity, &revision); err != nil {
		return errors.Join(ErrUnsupportedSchema, err)
	}
	if identity != r.identity || revision != r.revision {
		return ErrUnsupportedSchema
	}
	observed, err := schemaObjects(ctx, db)
	if err != nil {
		return err
	}
	reference, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer reference.Close()
	reference.SetMaxOpenConns(1)
	if _, err := reference.ExecContext(ctx, r.ddl); err != nil {
		return err
	}
	expected, err := schemaObjects(ctx, reference)
	if err != nil {
		return err
	}
	if !slices.Equal(observed, expected) {
		return ErrUnsupportedSchema
	}
	var layout string
	if r.identity == controlSchemaIdentity {
		var epoch, visibility int64
		if err := db.QueryRowContext(ctx, `SELECT layout_id,archive_epoch,visibility_revision FROM runtime_storage_state WHERE singleton=1`).Scan(&layout, &epoch, &visibility); err != nil || epoch <= 0 || visibility <= 0 {
			return ErrUnsupportedSchema
		}
	} else {
		var archive string
		if err := db.QueryRowContext(ctx, `SELECT layout_id,archive_id FROM observation_storage_state WHERE singleton=1`).Scan(&layout, &archive); err != nil || archive != r.archive {
			return ErrUnsupportedSchema
		}
	}
	if layout != r.layout {
		return ErrUnsupportedSchema
	}
	return nil
}
func (r *roleSchema) initialize(ctx context.Context, db *sql.DB, now time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*'`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		if err := r.verify(ctx, tx); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, r.ddl); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_metadata VALUES(1,?,?,?)`, r.identity, r.revision, now.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if r.identity == controlSchemaIdentity {
		if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_storage_state VALUES(1,?,1,1)`, r.layout); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runtime_usage_policy SET enabled=1,retention_days=365,collecting_since_unix_ms=? WHERE singleton=1`, now.UTC().UnixMilli()); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `INSERT INTO observation_storage_state VALUES(1,?,?)`, r.layout, r.archive); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *ControlStore) Shutdown(ctx context.Context) error { return s.store.Shutdown(ctx) }

func (s *ControlStore) NativeToolRepository() nativetool.AuthorityRepository {
	return s.store.nativeTools
}

func (s *ControlStore) RetireNativeMetadata(ctx context.Context, now time.Time, limit int) (int64, error) {
	if !nativeTime(now) || limit < 1 || limit > 1000 {
		return 0, nativetool.ErrInvalid
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer finish()
	tx, err := s.store.database.BeginTx(operation, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(operation, `DELETE FROM native_tool_operations WHERE operation_id IN(SELECT operation_id FROM native_tool_operations WHERE completed_at_unix_ms IS NOT NULL AND metadata_expires_at_unix_ms<=? AND NOT EXISTS(SELECT 1 FROM native_tool_deliveries d WHERE d.operation_id=native_tool_operations.operation_id AND d.completed_at_unix_ms IS NULL) ORDER BY metadata_expires_at_unix_ms,operation_id LIMIT ?)`, now.UnixMilli(), limit)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *ControlStore) SchemaStateReader() SchemaStateReader { return s.store.repo }

func (s *ControlStore) Settings(ctx context.Context) (DatabaseSettings, error) {
	return s.store.Settings(ctx)
}
