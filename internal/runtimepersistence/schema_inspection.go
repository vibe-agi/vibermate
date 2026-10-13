package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// InspectSchema reads a supported stopped database without initializing it,
// changing its journal mode, or creating any source files. The caller owns
// exclusion of all source writers. Retained WAL is read from a private verified
// temporary copy because ordinary read-only SQLite can create source sidecars;
// immutable SQLite ignores WAL and is only safe for a checkpointed database.
// Revision zero means an absent or empty database; foreign/malformed schemas
// fail closed. Temporary copies are deleted before the operation returns.
func InspectSchema(ctx context.Context, path string, timeout time.Duration) (SchemaState, error) {
	return newSchemaInspectionRunner().inspect(ctx, path, timeout)
}

type schemaInspectionRunner struct {
	copyFile           func(context.Context, string, string) (schemaInspectionFile, error)
	temporaryDirectory func() (string, error)
	readSchema         func(context.Context, string, time.Duration, bool) (SchemaState, error)
}

func newSchemaInspectionRunner() schemaInspectionRunner {
	return schemaInspectionRunner{
		copyFile:           copySchemaInspectionFile,
		temporaryDirectory: func() (string, error) { return os.MkdirTemp("", "vibermate-schema-inspection-") },
		readSchema:         readInspectedSchema,
	}
}

func (runner schemaInspectionRunner) inspect(ctx context.Context, path string, timeout time.Duration) (state SchemaState, result error) {
	if ctx == nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || timeout < 0 {
		return SchemaState{}, ErrInvalidDatabasePath
	}
	if err := ctx.Err(); err != nil {
		return SchemaState{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return SchemaState{}, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return SchemaState{}, ErrInvalidDatabasePath
	}
	if info.Size() == 0 {
		return SchemaState{}, nil
	}
	wal, err := os.Lstat(path + "-wal")
	if err != nil && !errors.Is(err, os.ErrNotExist) || err == nil && !wal.Mode().IsRegular() {
		return SchemaState{}, ErrInvalidDatabasePath
	}
	if wal == nil || wal.Size() == 0 {
		state, err := runner.readSchema(ctx, path, timeout, true)
		// Immutable skips SQLite change detection. In particular, a WAL that
		// appears while we read must not let a stale base revision succeed.
		after, statErr := os.Lstat(path)
		walAfter, walErr := os.Lstat(path + "-wal")
		if statErr != nil || !sameInspectionFile(info, after) || (wal == nil && (!errors.Is(walErr, os.ErrNotExist))) || (wal != nil && (walErr != nil || !sameInspectionFile(wal, walAfter))) {
			return SchemaState{}, errors.Join(ErrUnsupportedSchema, ctx.Err())
		}
		return state, err
	}
	temporary, err := runner.temporaryDirectory()
	if err != nil {
		return SchemaState{}, errors.Join(ErrUnsupportedSchema, ctx.Err())
	}
	defer func() {
		if err := os.RemoveAll(temporary); err != nil {
			state = SchemaState{}
			result = errors.Join(ErrUnsupportedSchema, result)
		}
	}()
	copyPath := filepath.Join(temporary, "runtime.db")
	var originals []schemaInspectionFile
	initial := []os.FileInfo{info, wal}
	for index, suffix := range []string{"", "-wal"} {
		file, err := runner.copyFile(ctx, path+suffix, copyPath+suffix)
		if err != nil {
			return SchemaState{}, errors.Join(ErrUnsupportedSchema, ctx.Err())
		}
		if !sameInspectionFile(initial[index], file.info) {
			return SchemaState{}, ErrUnsupportedSchema
		}
		originals = append(originals, file)
	}
	state, result = runner.readSchema(ctx, copyPath, timeout, false)
	for _, original := range originals {
		if err := verifySchemaInspectionFile(ctx, original); err != nil {
			return SchemaState{}, errors.Join(ErrUnsupportedSchema, ctx.Err())
		}
	}
	return state, result
}

func readInspectedSchema(ctx context.Context, path string, timeout time.Duration, immutable bool) (SchemaState, error) {
	connector := newSQLiteReadConnector(path, timeout)
	if immutable {
		connector.(*sqliteConnector).dsn += "&immutable=1"
	}
	database := sql.OpenDB(connector)
	defer database.Close()
	database.SetMaxOpenConns(1)
	transaction, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SchemaState{}, errors.Join(ErrUnsupportedSchema, ctx.Err())
	}
	defer transaction.Rollback()
	return inspectSupportedSchema(ctx, transaction)
}

type schemaInspectionFile struct {
	path   string
	info   os.FileInfo
	digest [sha256.Size]byte
}

func sameInspectionFile(before, after os.FileInfo) bool {
	return before != nil && after != nil && after.Mode().IsRegular() && os.SameFile(before, after) && before.Size() == after.Size() && before.Mode() == after.Mode() && before.ModTime().Equal(after.ModTime())
}

func copySchemaInspectionFile(ctx context.Context, source, target string) (schemaInspectionFile, error) {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return schemaInspectionFile{}, ErrUnsupportedSchema
	}
	in, err := os.Open(source)
	if err != nil {
		return schemaInspectionFile{}, ErrUnsupportedSchema
	}
	defer in.Close()
	opened, err := in.Stat()
	if err != nil || !sameInspectionFile(info, opened) {
		return schemaInspectionFile{}, ErrUnsupportedSchema
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return schemaInspectionFile{}, ErrUnsupportedSchema
	}
	hash := sha256.New()
	count, copyErr := io.Copy(io.MultiWriter(out, hash), &schemaInspectionReader{ctx, in})
	if err := errors.Join(copyErr, out.Close()); err != nil || count != info.Size() {
		return schemaInspectionFile{}, ErrUnsupportedSchema
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	file := schemaInspectionFile{path: source, info: info, digest: digest}
	if err := verifySchemaInspectionFile(ctx, file); err != nil {
		return schemaInspectionFile{}, err
	}
	copyInfo, err := os.Lstat(target)
	if err != nil {
		return schemaInspectionFile{}, ErrUnsupportedSchema
	}
	if err := verifySchemaInspectionFile(ctx, schemaInspectionFile{path: target, info: copyInfo, digest: digest}); err != nil {
		return schemaInspectionFile{}, err
	}
	return file, nil
}

func verifySchemaInspectionFile(ctx context.Context, file schemaInspectionFile) error {
	info, err := os.Lstat(file.path)
	if err != nil || !sameInspectionFile(file.info, info) {
		return ErrUnsupportedSchema
	}
	in, err := os.Open(file.path)
	if err != nil {
		return ErrUnsupportedSchema
	}
	defer in.Close()
	opened, err := in.Stat()
	if err != nil || !sameInspectionFile(file.info, opened) {
		return ErrUnsupportedSchema
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, &schemaInspectionReader{ctx, in}); err != nil {
		return err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	after, err := os.Lstat(file.path)
	if err != nil || !sameInspectionFile(file.info, after) || digest != file.digest {
		return ErrUnsupportedSchema
	}
	return nil
}

type schemaInspectionReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *schemaInspectionReader) Read(encoded []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(encoded)
}

type schemaQueryer interface {
	schemaQuerier
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Only concretely frozen versions belong here. A metadata row never proves
// support; all user-defined SQLite objects must match the frozen bootstrap.
func inspectSupportedSchema(ctx context.Context, database schemaQueryer) (SchemaState, error) {
	revision, err := readSchemaRevision(ctx, database)
	if err != nil {
		return SchemaState{}, errors.Join(ErrUnsupportedSchema, ctx.Err())
	}
	if revision == 0 {
		return SchemaState{}, nil
	}
	if err := inspectSchemaLayout(ctx, database, revision); err != nil {
		return SchemaState{}, err
	}
	var state SchemaState
	if err := database.QueryRowContext(ctx, `SELECT schema_identity, schema_revision, initialized_at FROM runtime_metadata WHERE singleton=1`).Scan(&state.Identity, &state.Revision, &state.InitializedAt); err != nil {
		return SchemaState{}, errors.Join(ErrUnsupportedSchema, ctx.Err())
	}
	initialized, err := time.Parse(time.RFC3339Nano, state.InitializedAt)
	if err != nil || initialized.IsZero() || initialized.Location() != time.UTC {
		return SchemaState{}, ErrUnsupportedSchema
	}
	return state, nil
}

// SQLite's ALTER TABLE produces a second exact layout. Admit only a complete
// layout generated from one of the two reviewed SQL paths, never normalized SQL.
func inspectSchemaLayout(ctx context.Context, database schemaQueryer, revision int64) error {
	var bootstraps []string
	switch revision {
	case 1:
		bootstraps = []string{schemaV1SQL}
	case 2:
		bootstraps = []string{schemaV2SQL, schemaV1SQL + "\n" + migration001To002SQL}
	default:
		return ErrUnsupportedSchema
	}
	observed, err := schemaObjects(ctx, database)
	if err != nil {
		return errors.Join(ErrUnsupportedSchema, ctx.Err())
	}
	// Build the reference in memory, never in the inspected directory. SQLite
	// supplies its own canonical sqlite_schema form for the exact frozen DDL.
	for _, frozen := range bootstraps {
		reference, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			return ErrUnsupportedSchema
		}
		reference.SetMaxOpenConns(1)
		if _, err := reference.ExecContext(ctx, frozen); err != nil {
			_ = reference.Close()
			return errors.Join(ErrUnsupportedSchema, ctx.Err())
		}
		expected, err := schemaObjects(ctx, reference)
		_ = reference.Close()
		if err != nil {
			return errors.Join(ErrUnsupportedSchema, ctx.Err())
		}
		if slices.Equal(observed, expected) {
			return nil
		}
	}
	return ErrUnsupportedSchema
}

type schemaObject struct{ kind, name, table, statement string }

func schemaObjects(ctx context.Context, database schemaQueryer) ([]schemaObject, error) {
	rows, err := database.QueryContext(ctx, `SELECT type, name, tbl_name, coalesce(sql,'') FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*' ORDER BY type,name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var objects []schemaObject
	for rows.Next() {
		var object schemaObject
		if err := rows.Scan(&object.kind, &object.name, &object.table, &object.statement); err != nil {
			return nil, err
		}
		objects = append(objects, object)
	}
	return objects, errors.Join(rows.Err(), rows.Close())
}
