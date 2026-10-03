package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"hash"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
)

var (
	ErrMigrationRequired       = errors.New("runtime database requires a verified schema upgrade")
	ErrMigrationNotCommitted   = errors.New("schema upgrade was not committed")
	ErrMigrationOutcomeUnknown = errors.New("schema upgrade outcome is unknown")
)

// A receipt can only be minted after independent database verification. It
// contains no row values, credentials, or full-directory manifest authority.
type VerifiedMigrationDatabase struct {
	sourcePath, snapshotPath   string
	legacyDigest               [32]byte
	sourceOwner, snapshotOwner *instanceguard.Guard
}

// Open checks only presence; the full-directory owner parses this receipt.
const SchemaUpgradeReceiptName = "schema-upgrade-receipt.json"

func (v VerifiedMigrationDatabase) LegacyDigest() [32]byte { return v.legacyDigest }

type MigrationInput struct {
	Database         VerifiedMigrationDatabase
	ReconcileTimeout time.Duration
}
type MigrationResult struct {
	FromRevision, ToRevision int64
	Committed                bool
}

func migrationOwns(owner *instanceguard.Guard, path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && filepath.Base(path) == "runtime.db" && owner != nil && owner.Path() == filepath.Join(filepath.Dir(path), "runtime-data.lock") && owner.OwnsDirectory(filepath.Dir(path))
}
func migrationOwners(v VerifiedMigrationDatabase) bool {
	return v.sourcePath != v.snapshotPath && filepath.Dir(v.sourcePath) != filepath.Dir(v.snapshotPath) && migrationOwns(v.sourceOwner, v.sourcePath) && migrationOwns(v.snapshotOwner, v.snapshotPath)
}

func VerifyMigrationDatabase(ctx context.Context, source, snapshot string, owner, snapshotOwner *instanceguard.Guard, timeout time.Duration) (VerifiedMigrationDatabase, error) {
	v := VerifiedMigrationDatabase{sourcePath: source, snapshotPath: snapshot, sourceOwner: owner, snapshotOwner: snapshotOwner}
	if ctx == nil || timeout <= 0 || !migrationOwners(v) {
		return VerifiedMigrationDatabase{}, ErrMigrationNotCommitted
	}
	sourceState, digest, err := migrationFingerprintPath(ctx, source, timeout)
	if err != nil || sourceState.Revision != 1 {
		return VerifiedMigrationDatabase{}, errors.Join(ErrMigrationNotCommitted, ctx.Err())
	}
	snapshotState, snapshotDigest, err := migrationFingerprintPath(ctx, snapshot, timeout)
	if err != nil || snapshotState != sourceState || digest != snapshotDigest || !migrationOwners(v) {
		return VerifiedMigrationDatabase{}, errors.Join(ErrMigrationNotCommitted, ctx.Err())
	}
	v.legacyDigest = digest
	return v, nil
}

// Always read a private byte-verified copy, including retained WAL. SQLite may
// create sidecars or recover that copy, but never the stopped source.
func migrationFingerprintPath(ctx context.Context, path string, timeout time.Duration) (state SchemaState, digest [32]byte, result error) {
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
		digest, result = legacyFingerprint(ctx, database)
	}
	if result == nil && state.Revision == 2 {
		result = migrationAdditionsEmpty(ctx, database)
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

func migrationIntegrity(ctx context.Context, q schemaQueryer) error {
	var integrity string
	if err := q.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return ErrUnsupportedSchema
	}
	rows, err := q.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return ErrUnsupportedSchema
	}
	hasViolation := rows.Next()
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil || hasViolation {
		return ErrUnsupportedSchema
	}
	return nil
}

type legacyTable struct {
	name    string
	columns []string
}

func quoteMigrationIdentifier(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func legacyTables(ctx context.Context) ([]legacyTable, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schemaV1SQL); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var tables []legacyTable
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, legacyTable{name: name})
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for i := range tables {
		rows, err := db.QueryContext(ctx, `PRAGMA table_xinfo(`+quoteMigrationIdentifier(tables[i].name)+`)`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var cid, notnull, pk, hidden int
			var name, kind string
			var def any
			if err := rows.Scan(&cid, &name, &kind, &notnull, &def, &pk, &hidden); err != nil {
				rows.Close()
				return nil, err
			}
			if tables[i].name != "runtime_metadata" || name != "schema_revision" {
				tables[i].columns = append(tables[i].columns, quoteMigrationIdentifier(name))
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	return tables, nil
}
func fingerprintPart(h hash.Hash, tag byte, b []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(b)))
	h.Write([]byte{tag})
	h.Write(length[:])
	h.Write(b)
}

// Memory is bounded by one row (the frozen schema bounds each body). The SQL
// sorter is file-backed and capped; no whole-table or whole-DB materialization.
func legacyFingerprint(ctx context.Context, q schemaQueryer) ([32]byte, error) {
	tables, err := legacyTables(ctx)
	if err != nil {
		return [32]byte{}, err
	}
	h := sha256.New()
	for _, table := range tables {
		fingerprintPart(h, 'T', []byte(table.name))
		cols := strings.Join(table.columns, ",")
		rows, err := q.QueryContext(ctx, `SELECT `+cols+` FROM `+quoteMigrationIdentifier(table.name)+` ORDER BY `+cols)
		if err != nil {
			return [32]byte{}, err
		}
		values := make([]any, len(table.columns))
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		for rows.Next() {
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return [32]byte{}, err
			}
			h.Write([]byte{'R'})
			for _, value := range values {
				var bits [8]byte
				switch v := value.(type) {
				case nil:
					fingerprintPart(h, 'N', nil)
				case int64:
					binary.BigEndian.PutUint64(bits[:], uint64(v))
					fingerprintPart(h, 'I', bits[:])
				case float64:
					binary.BigEndian.PutUint64(bits[:], math.Float64bits(v))
					fingerprintPart(h, 'F', bits[:])
				case string:
					fingerprintPart(h, 'S', []byte(v))
				case []byte:
					fingerprintPart(h, 'B', v)
				default:
					rows.Close()
					return [32]byte{}, ErrUnsupportedSchema
				}
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return [32]byte{}, err
		}
		h.Write([]byte{'E'})
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest, nil
}

func UpgradeRevision1(ctx context.Context, input MigrationInput) (MigrationResult, error) {
	return schemaMigrationRunner{commit: commitSchemaMigration}.upgrade(ctx, input)
}

type schemaMigrationRunner struct {
	afterDDL            func(context.Context, *sql.Conn) error
	commit              func(context.Context, *sql.Conn) error
	closeConn           func(*sql.Conn) error
	closePool           func(*sql.DB) error
	syncSourceFile      func(context.Context, string) error
	syncSourceDirectory func(context.Context, string) error
}

func commitSchemaMigration(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, "COMMIT")
	return err
}
func (runner schemaMigrationRunner) upgrade(ctx context.Context, input MigrationInput) (MigrationResult, error) {
	v := input.Database
	if ctx == nil || !migrationOwners(v) || v.legacyDigest == ([32]byte{}) {
		if v.legacyDigest != ([32]byte{}) {
			return MigrationResult{}, ErrMigrationOutcomeUnknown
		}
		return MigrationResult{}, ErrMigrationNotCommitted
	}
	timeout := input.ReconcileTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	fresh, err := VerifyMigrationDatabase(ctx, v.sourcePath, v.snapshotPath, v.sourceOwner, v.snapshotOwner, DefaultBusyTimeout)
	if err != nil || fresh.legacyDigest != v.legacyDigest {
		return runner.reconcileVerified(ctx, v, timeout)
	}
	// No journal_mode pragma: opening the writer cannot precede certification.
	db := sql.OpenDB(newSQLiteUpgradeConnector(v.sourcePath, DefaultBusyTimeout))
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		if err := runner.closeDatabasePool(db); err != nil {
			return MigrationResult{FromRevision: 1}, ErrMigrationOutcomeUnknown
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
			return MigrationResult{FromRevision: 1}, errors.Join(ErrMigrationOutcomeUnknown, ctx.Err())
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
	if err == nil && state.Revision == 1 {
		err = migrationIntegrity(ctx, conn)
	} else {
		err = ErrUnsupportedSchema
	}
	var digest [32]byte
	if err == nil {
		digest, err = legacyFingerprint(ctx, conn)
	}
	if err != nil || digest != v.legacyDigest || !migrationOwners(v) {
		return reconcile(ErrMigrationNotCommitted)
	}
	// Recheck the independently owned snapshot immediately inside the transaction.
	_, snapshotDigest, err := migrationFingerprintPath(ctx, v.snapshotPath, DefaultBusyTimeout)
	if err != nil || snapshotDigest != v.legacyDigest {
		return reconcile(ErrMigrationNotCommitted)
	}
	if _, err := conn.ExecContext(ctx, migration001To002SQL); err != nil {
		return reconcile(err)
	}
	if runner.afterDDL != nil {
		if err := runner.afterDDL(ctx, conn); err != nil {
			return reconcile(err)
		}
	}
	if err := inspectSchemaLayout(ctx, conn, 2); err != nil {
		return reconcile(err)
	}
	if err := migrationIntegrity(ctx, conn); err != nil {
		return reconcile(err)
	}
	digest, err = legacyFingerprint(ctx, conn)
	if err != nil || digest != v.legacyDigest || !migrationOwners(v) {
		return reconcile(ErrMigrationNotCommitted)
	}
	if err := migrationAdditionsEmpty(ctx, conn); err != nil {
		return reconcile(err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE runtime_metadata SET schema_revision=2 WHERE singleton=1 AND schema_revision=1`); err != nil {
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

func (runner schemaMigrationRunner) closeDatabasePool(database *sql.DB) error {
	closePool := runner.closePool
	if closePool == nil {
		closePool = (*sql.DB).Close
	}
	return closePool(database)
}

func (runner schemaMigrationRunner) reconcileVerified(ctx context.Context, v VerifiedMigrationDatabase, timeout time.Duration) (MigrationResult, error) {
	proof, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	result, err := runner.reconcile(proof, v, timeout)
	if err != nil {
		return result, errors.Join(err, ctx.Err())
	}
	return result, nil
}

func migrationAdditionsEmpty(ctx context.Context, q schemaQueryer) error {
	objects, err := schemaObjects(ctx, q)
	if err != nil {
		return err
	}
	for _, object := range objects {
		if object.kind == "table" && strings.HasPrefix(object.name, "native_tool_") {
			var count int64
			if err := q.QueryRowContext(ctx, `SELECT count(*) FROM `+quoteMigrationIdentifier(object.name)).Scan(&count); err != nil || count != 0 {
				return ErrUnsupportedSchema
			}
		}
	}
	var wrong int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM runtime_egress_attempts WHERE byte_counts_known<>1`).Scan(&wrong); err != nil || wrong != 0 {
		return ErrUnsupportedSchema
	}
	return nil
}

// Used only before normal writers are admitted. A persisted digest is never
// authority by itself: the exact frozen snapshot is independently re-proved.
func ReconcileMigrationDatabase(ctx context.Context, source, snapshot string, owner, snapshotOwner *instanceguard.Guard, expected [32]byte, timeout time.Duration) (MigrationResult, error) {
	v := VerifiedMigrationDatabase{sourcePath: source, snapshotPath: snapshot, sourceOwner: owner, snapshotOwner: snapshotOwner}
	v.legacyDigest = expected
	return (schemaMigrationRunner{}).reconcile(ctx, v, timeout)
}

func (runner schemaMigrationRunner) reconcile(ctx context.Context, v VerifiedMigrationDatabase, timeout time.Duration) (MigrationResult, error) {
	result := MigrationResult{FromRevision: 1}
	if ctx == nil || timeout <= 0 || v.legacyDigest == ([32]byte{}) || !migrationOwners(v) {
		return result, ErrMigrationOutcomeUnknown
	}
	proof, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	snapshotState, snapshotDigest, err := migrationFingerprintPath(proof, v.snapshotPath, timeout)
	if err != nil || snapshotState.Revision != 1 || snapshotDigest != v.legacyDigest {
		return result, ErrMigrationOutcomeUnknown
	}
	// Readable cache bytes are insufficient after commit/close uncertainty.
	// Durably flush the exact owned source, then independently inspect it anew.
	// Pending-startup row proof may use its owned work budget, but the
	// durability segment itself is always at most 30s and caller remaining.
	barrier, cancelBarrier := context.WithTimeout(proof, 30*time.Second)
	barrierErr := runner.syncOwnedSource(barrier, v)
	cancelBarrier()
	if barrierErr != nil {
		return result, ErrMigrationOutcomeUnknown
	}
	sourceState, sourceDigest, err := migrationFingerprintPath(proof, v.sourcePath, timeout)
	if err != nil || sourceDigest != v.legacyDigest || sourceState.Identity != snapshotState.Identity || sourceState.InitializedAt != snapshotState.InitializedAt || proof.Err() != nil || !migrationOwners(v) {
		return result, ErrMigrationOutcomeUnknown
	}
	if sourceState.Revision == 1 {
		result.ToRevision = 1
		return result, ErrMigrationNotCommitted
	}
	if sourceState.Revision != 2 {
		return result, ErrMigrationOutcomeUnknown
	}
	if proof.Err() != nil || !migrationOwners(v) {
		return result, ErrMigrationOutcomeUnknown
	}
	result.ToRevision = 2
	result.Committed = true
	return result, nil
}
