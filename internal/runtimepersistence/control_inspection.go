package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

// InspectControl verifies the mandatory role schema without initializing it or
// touching optional sources. The owned directory protocol excludes writers.
func InspectControl(ctx context.Context, path, layout string) (SchemaState, error) {
	return inspectControl(ctx, path, layout, false)
}

// ValidateCurrentControl certifies the existing required rows at an activated
// recovery boundary. It never compares with an old projection or opens history.
// The directory owner excludes writers throughout this read-only snapshot.
func ValidateCurrentControl(ctx context.Context, path, layout string) (SchemaState, error) {
	return inspectControl(ctx, path, layout, true)
}
func inspectControl(ctx context.Context, path, layout string, validateRows bool) (state SchemaState, result error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || !roleID(layout) {
		return state, ErrUnsupportedSchema
	}
	db := sql.OpenDB(newSQLiteReadConnector(path, DefaultBusyTimeout))
	db.SetMaxOpenConns(1)
	defer func() { result = errors.Join(result, db.Close()) }()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return state, err
	}
	defer tx.Rollback()
	role := roleSchema{identity: controlSchemaIdentity, revision: 3, ddl: controlSchemaSQL, layout: layout}
	if err = role.verify(ctx, tx); err != nil {
		return state, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT schema_identity,schema_revision,initialized_at FROM runtime_metadata WHERE singleton=1`).Scan(&state.Identity, &state.Revision, &state.InitializedAt); err != nil {
		return state, err
	}
	initialized, err := time.Parse(time.RFC3339Nano, state.InitializedAt)
	if err != nil || initialized.IsZero() || initialized.Location() != time.UTC {
		return state, ErrUnsupportedSchema
	}
	if validateRows {
		if err = validateRequiredControlRows(ctx, tx); err != nil {
			return state, err
		}
	}
	return state, tx.Commit()
}

// CreateStagedControl initializes only a fresh private control file. Callers own
// both directory guards and durable receipt/publication; no Host is started.
func CreateStagedControl(ctx context.Context, path, layout string, now time.Time) (result error) {
	if !roleID(layout) || now.IsZero() || now.Location() != time.UTC {
		return ErrInvalidDatabasePath
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	db := sql.OpenDB(newSQLiteUpgradeConnector(path, DefaultBusyTimeout))
	db.SetMaxOpenConns(1)
	defer func() { result = errors.Join(result, db.Close()) }()
	if _, err = db.ExecContext(ctx, `PRAGMA journal_mode=DELETE`); err != nil {
		return err
	}
	role := roleSchema{identity: controlSchemaIdentity, revision: 3, ddl: controlSchemaSQL, layout: layout}
	if err = role.initialize(ctx, db, now); err != nil {
		return err
	}
	if err = db.Close(); err != nil {
		return err
	}
	file, err = os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

// CertifyStagedControlProjection validates all required facts and independently
// reads the typed target projection. The directory owner compares this result
// to a newly derived source certificate, never just a serialized receipt hash.
func CertifyStagedControlProjection(ctx context.Context, path, layout string, sourceRevision int64) (digest [32]byte, count int64, result error) {
	if sourceRevision != 0 && sourceRevision != 1 && sourceRevision != 2 && sourceRevision != 3 {
		return digest, 0, ErrUnsupportedSchema
	}
	if _, err := InspectControl(ctx, path, layout); err != nil {
		return digest, 0, err
	}
	db := sql.OpenDB(newSQLiteReadConnector(path, DefaultBusyTimeout))
	db.SetMaxOpenConns(1)
	defer func() { result = errors.Join(result, db.Close()) }()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return digest, 0, err
	}
	defer tx.Rollback()
	if err = validateRequiredControlRows(ctx, tx); err != nil {
		return digest, 0, err
	}
	h := sha256.New()
	fingerprintPart(h, 'V', []byte(fmt.Sprintf("control-extraction/v1/source-%d", sourceRevision)))
	for _, p := range controlExtractionProjections() {
		n, err := streamControlProjection(ctx, tx, nil, p, `SELECT `+p.columns+` FROM `+p.table+` ORDER BY `+p.key, nil, h)
		if err != nil {
			return digest, 0, err
		}
		count += n
	}
	copy(digest[:], h.Sum(nil))
	return digest, count, tx.Commit()
}

func validateRequiredControlRows(ctx context.Context, tx *sql.Tx) error {
	var err error
	if err = extractionIntegrity(ctx, tx, `PRAGMA main.integrity_check`); err != nil {
		return err
	}
	if err = extractionNoRows(ctx, tx, `PRAGMA main.foreign_key_check`); err != nil {
		return err
	}
	if err = validateExtractedControl(ctx, tx); err != nil {
		return err
	}
	if err = validateExtractedNative(ctx, tx); err != nil {
		return err
	}
	return nil
}
