package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"os"
	"path/filepath"
	"time"
)

// VerifiedRevision2Database certifies every frozen-v2 field and sequence.
// It is intentionally distinct from the published v1 legacy projection.
type VerifiedRevision2Database struct {
	sourcePath, snapshotPath   string
	sourceDigest               [32]byte
	sourceOwner, snapshotOwner *instanceguard.Guard
}

func (v VerifiedRevision2Database) SourceDigest() [32]byte { return v.sourceDigest }
func (v VerifiedRevision2Database) ownership() VerifiedMigrationDatabase {
	return VerifiedMigrationDatabase{sourcePath: v.sourcePath, snapshotPath: v.snapshotPath, sourceOwner: v.sourceOwner, snapshotOwner: v.snapshotOwner}
}
func revision2MigrationOwners(v VerifiedRevision2Database) bool {
	return migrationOwners(v.ownership())
}

type Revision2MigrationInput struct {
	Database         VerifiedRevision2Database
	ReconcileTimeout time.Duration
}
type revision2MigrationRunner schemaMigrationRunner

func UpgradeRevision2(ctx context.Context, input Revision2MigrationInput) (MigrationResult, error) {
	return (revision2MigrationRunner{}).upgrade(ctx, input)
}
func revision2Fingerprint(ctx context.Context, q schemaQueryer) ([32]byte, error) {
	return frozenFingerprint(ctx, q, schemaV2SQL)
}
func revision2AdditionsStandard(ctx context.Context, q schemaQueryer) error {
	var wrong int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM provider_accounts WHERE cyber_access_program<>'standard'`).Scan(&wrong); err != nil || wrong != 0 {
		return ErrUnsupportedSchema
	}
	return nil
}
func VerifyRevision2MigrationDatabase(ctx context.Context, source, snapshot string, owner, snapshotOwner *instanceguard.Guard, timeout time.Duration) (VerifiedRevision2Database, error) {
	v := VerifiedRevision2Database{sourcePath: source, snapshotPath: snapshot, sourceOwner: owner, snapshotOwner: snapshotOwner}
	if ctx == nil || timeout <= 0 || !revision2MigrationOwners(v) {
		return VerifiedRevision2Database{}, ErrMigrationNotCommitted
	}
	sourceState, digest, err := revision2FingerprintPath(ctx, source, timeout)
	if err != nil || sourceState.Revision != 2 {
		return VerifiedRevision2Database{}, errors.Join(ErrMigrationNotCommitted, ctx.Err())
	}
	snapshotState, snapshotDigest, err := revision2FingerprintPath(ctx, snapshot, timeout)
	if err != nil || snapshotState != sourceState || digest != snapshotDigest || !revision2MigrationOwners(v) {
		return VerifiedRevision2Database{}, errors.Join(ErrMigrationNotCommitted, ctx.Err())
	}
	v.sourceDigest = digest
	return v, nil
}

// Always read a private byte-verified copy, including retained WAL. SQLite may
// create sidecars or recover that copy, but never the stopped source.
func revision2FingerprintPath(ctx context.Context, path string, timeout time.Duration) (state SchemaState, digest [32]byte, result error) {
	if ctx == nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return state, digest, ErrInvalidDatabasePath
	}
	temporary, err := os.MkdirTemp("", "vibermate-migration-inspection-")
	if err != nil {
		return state, digest, ErrUnsupportedSchema
	}
	defer func() {
		if err := os.RemoveAll(temporary); err != nil {
			result = errors.Join(result, ErrUnsupportedSchema)
		}
	}()
	copyPath := filepath.Join(temporary, "runtime.db")
	var originals []schemaInspectionFile
	for _, suffix := range []string{"", "-wal"} {
		info, err := os.Lstat(path + suffix)
		if suffix != "" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return state, digest, ErrUnsupportedSchema
		}
		original, err := copySchemaInspectionFile(ctx, path+suffix, copyPath+suffix)
		if err != nil {
			return state, digest, err
		}
		originals = append(originals, original)
	}
	database := sql.OpenDB(newSQLiteReadConnector(copyPath, timeout))
	database.SetMaxOpenConns(1)
	if _, err := database.ExecContext(ctx, "PRAGMA temp_store=FILE; PRAGMA cache_size=-2048"); err != nil {
		database.Close()
		return state, digest, ErrUnsupportedSchema
	}
	state, result = inspectSupportedSchema(ctx, database)
	if result == nil {
		result = migrationIntegrity(ctx, database)
	}
	if result == nil {
		digest, result = revision2Fingerprint(ctx, database)
	}
	if result == nil && state.Revision == 3 {
		result = revision2AdditionsStandard(ctx, database)
	}
	result = errors.Join(result, database.Close())
	for _, original := range originals {
		result = errors.Join(result, verifySchemaInspectionFile(ctx, original))
	}
	// A new WAL cannot appear between base-copy creation and completion.
	if len(originals) == 1 {
		if _, err := os.Lstat(path + "-wal"); !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, ErrUnsupportedSchema)
		}
	}
	return
}

func (runner revision2MigrationRunner) upgrade(ctx context.Context, input Revision2MigrationInput) (MigrationResult, error) {
	v := input.Database
	if ctx == nil || !revision2MigrationOwners(v) || v.sourceDigest == ([32]byte{}) {
		if v.sourceDigest != ([32]byte{}) {
			return MigrationResult{}, ErrMigrationOutcomeUnknown
		}
		return MigrationResult{}, ErrMigrationNotCommitted
	}
	timeout := input.ReconcileTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	fresh, err := VerifyRevision2MigrationDatabase(ctx, v.sourcePath, v.snapshotPath, v.sourceOwner, v.snapshotOwner, DefaultBusyTimeout)
	if err != nil || fresh.sourceDigest != v.sourceDigest {
		return runner.reconcileVerified(ctx, v, timeout)
	}
	// No journal_mode pragma: opening the writer cannot precede certification.
	db := sql.OpenDB(newSQLiteUpgradeConnector(v.sourcePath, DefaultBusyTimeout))
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		if err := runner.closeDatabasePool(db); err != nil {
			return MigrationResult{FromRevision: 2}, ErrMigrationOutcomeUnknown
		}
		return runner.reconcileVerified(ctx, v, timeout)
	}
	closed := false
	var closeErr error
	closeDatabase := func() error {
		if !closed {
			closeConnection := runner.closeConn
			if closeConnection == nil {
				closeConnection = (*sql.Conn).Close
			}
			closeErr = errors.Join(closeConnection(conn), runner.closeDatabasePool(db))
			closed = true
		}
		return closeErr
	}
	defer func() { _ = closeDatabase() }()
	begun := false
	reconcile := func(root error) (MigrationResult, error) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		if begun {
			_, _ = conn.ExecContext(cleanup, "ROLLBACK")
		}
		// Both closes must positively succeed before source visibility can be
		// considered. A failed close retains uncertainty even if v2 is readable.
		if err := closeDatabase(); err != nil {
			return MigrationResult{FromRevision: 2}, errors.Join(ErrMigrationOutcomeUnknown, ctx.Err())
		}
		result, err := runner.reconcile(cleanup, v, timeout)
		if err != nil {
			return result, errors.Join(err, ctx.Err())
		}
		_ = root
		return result, nil
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return reconcile(err)
	}
	begun = true
	state, err := inspectSupportedSchema(ctx, conn)
	if err == nil && state.Revision == 2 {
		err = migrationIntegrity(ctx, conn)
	} else {
		err = ErrUnsupportedSchema
	}
	var digest [32]byte
	if err == nil {
		digest, err = revision2Fingerprint(ctx, conn)
	}
	if err != nil || digest != v.sourceDigest || !revision2MigrationOwners(v) {
		return reconcile(ErrMigrationNotCommitted)
	}
	// Recheck the independently owned snapshot immediately inside the transaction.
	snapshotState, snapshotDigest, err := revision2FingerprintPath(ctx, v.snapshotPath, DefaultBusyTimeout)
	if err != nil || snapshotState.Revision != 2 || snapshotDigest != v.sourceDigest {
		return reconcile(ErrMigrationNotCommitted)
	}
	if _, err := conn.ExecContext(ctx, migration002To003SQL); err != nil {
		return reconcile(err)
	}
	if runner.afterDDL != nil {
		if err := runner.afterDDL(ctx, conn); err != nil {
			return reconcile(err)
		}
	}
	if err := inspectSchemaLayout(ctx, conn, 3); err != nil {
		return reconcile(err)
	}
	if err := migrationIntegrity(ctx, conn); err != nil {
		return reconcile(err)
	}
	digest, err = revision2Fingerprint(ctx, conn)
	if err != nil || digest != v.sourceDigest || !revision2MigrationOwners(v) {
		return reconcile(ErrMigrationNotCommitted)
	}
	if err := revision2AdditionsStandard(ctx, conn); err != nil {
		return reconcile(err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE runtime_metadata SET schema_revision=3 WHERE singleton=1 AND schema_revision=2`); err != nil {
		return reconcile(err)
	}
	commit := runner.commit
	if commit == nil {
		commit = commitSchemaMigration
	}
	if err := commit(ctx, conn); err != nil {
		return reconcile(err)
	}
	begun = false
	// Even ordinary successful COMMIT gets an independent durable proof.
	return reconcile(nil)
}

func (runner revision2MigrationRunner) reconcileVerified(ctx context.Context, v VerifiedRevision2Database, timeout time.Duration) (MigrationResult, error) {
	proof, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	result, err := runner.reconcile(proof, v, timeout)
	if err != nil {
		return result, errors.Join(err, ctx.Err())
	}
	return result, nil
}

func ReconcileRevision2MigrationDatabase(ctx context.Context, source, snapshot string, owner, snapshotOwner *instanceguard.Guard, expected [32]byte, timeout time.Duration) (MigrationResult, error) {
	v := VerifiedRevision2Database{sourcePath: source, snapshotPath: snapshot, sourceOwner: owner, snapshotOwner: snapshotOwner}
	v.sourceDigest = expected
	return (revision2MigrationRunner{}).reconcile(ctx, v, timeout)
}

func (runner revision2MigrationRunner) reconcile(ctx context.Context, v VerifiedRevision2Database, timeout time.Duration) (MigrationResult, error) {
	result := MigrationResult{FromRevision: 2}
	if ctx == nil || timeout <= 0 || v.sourceDigest == ([32]byte{}) || !revision2MigrationOwners(v) {
		return result, ErrMigrationOutcomeUnknown
	}
	proof, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	snapshotState, snapshotDigest, err := revision2FingerprintPath(proof, v.snapshotPath, timeout)
	if err != nil || snapshotState.Revision != 2 || snapshotDigest != v.sourceDigest {
		return result, ErrMigrationOutcomeUnknown
	}
	// Readable cache bytes are insufficient after commit/close uncertainty.
	// Durably flush the exact owned source, then independently inspect it anew.
	// Pending-startup row proof may use its owned work budget, but the
	// durability segment itself is always at most 30s and caller remaining.
	barrier, cancelBarrier := context.WithTimeout(proof, 30*time.Second)
	barrierErr := runner.syncOwnedSource(barrier, v.ownership())
	cancelBarrier()
	if barrierErr != nil {
		return result, ErrMigrationOutcomeUnknown
	}
	sourceState, sourceDigest, err := revision2FingerprintPath(proof, v.sourcePath, timeout)
	if err != nil || sourceDigest != v.sourceDigest || sourceState.Identity != snapshotState.Identity || sourceState.InitializedAt != snapshotState.InitializedAt || proof.Err() != nil || !revision2MigrationOwners(v) {
		return result, ErrMigrationOutcomeUnknown
	}
	if sourceState.Revision == 2 {
		result.ToRevision = 2
		return result, ErrMigrationNotCommitted
	}
	if sourceState.Revision != 3 {
		return result, ErrMigrationOutcomeUnknown
	}
	if proof.Err() != nil || !revision2MigrationOwners(v) {
		return result, ErrMigrationOutcomeUnknown
	}
	result.ToRevision = 3
	result.Committed = true
	return result, nil
}

func (runner revision2MigrationRunner) closeDatabasePool(db *sql.DB) error {
	return schemaMigrationRunner(runner).closeDatabasePool(db)
}
func (runner revision2MigrationRunner) syncOwnedSource(ctx context.Context, v VerifiedMigrationDatabase) error {
	return schemaMigrationRunner(runner).syncOwnedSource(ctx, v)
}
