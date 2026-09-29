package runtimepersistence

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The schema evolves only through numbered, forward migrations. A new
// database applies every migration in order; an existing one applies the
// migrations after its recorded revision in one transaction at open. A
// database from a newer ViberMate (a higher revision) is refused unchanged.
//
// A migration file is never edited after it ships: add the next number.
// Migrations run inside a transaction with foreign keys enforced; a table
// rebuild must defer foreign-key checks for its own statement group.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// schemaIdentity marks a SQLite file as a ViberMate Runtime database.
const schemaIdentity = "vibermate-runtime"

var (
	// ErrUnsupportedSchema reports a file that is not a ViberMate Runtime
	// database, or one created by a newer ViberMate than this build.
	ErrUnsupportedSchema = errors.New("database is not a supported ViberMate Runtime schema")
)

type schemaMigration struct {
	revision int64
	name     string
	sql      string
}

var migrations = mustLoadMigrations(migrationFiles)

// latestSchemaRevision is the revision a database has after every migration.
func latestSchemaRevision() int64 { return int64(len(migrations)) }

func mustLoadMigrations(files fs.FS) []schemaMigration {
	loaded, err := loadMigrations(files)
	if err != nil {
		panic(err)
	}
	return loaded
}

// loadMigrations requires files named NNNN_name.sql numbered 1..n without gaps.
func loadMigrations(files fs.FS) ([]schemaMigration, error) {
	entries, err := fs.Glob(files, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(entries)
	loaded := make([]schemaMigration, 0, len(entries))
	for index, entry := range entries {
		base := path.Base(entry)
		number, name, found := strings.Cut(strings.TrimSuffix(base, ".sql"), "_")
		revision, err := strconv.ParseInt(number, 10, 64)
		if !found || name == "" || len(number) != 4 || err != nil || revision != int64(index+1) {
			return nil, fmt.Errorf("schema migration %q is not numbered %04d_<name>.sql", base, index+1)
		}
		content, err := fs.ReadFile(files, entry)
		if err != nil {
			return nil, err
		}
		loaded = append(loaded, schemaMigration{revision: revision, name: name, sql: string(content)})
	}
	if len(loaded) == 0 {
		return nil, errors.New("no schema migrations are embedded")
	}
	return loaded, nil
}

// migrateSchema brings database to the latest revision of chain in one
// transaction and returns that revision.
func migrateSchema(ctx context.Context, database *sql.DB, chain []schemaMigration, now time.Time) (int64, error) {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin SQLite schema migration: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	latest := int64(len(chain))
	current, err := readSchemaRevision(ctx, transaction)
	if err != nil {
		return 0, err
	}
	if current > latest {
		return 0, fmt.Errorf("%w: revision %d is newer than this build (%d)", ErrUnsupportedSchema, current, latest)
	}
	for _, step := range chain[current:] {
		if _, err := transaction.ExecContext(ctx, step.sql); err != nil {
			return 0, fmt.Errorf("apply schema migration %04d_%s: %w", step.revision, step.name, err)
		}
	}
	if current == 0 {
		_, err = transaction.ExecContext(ctx,
			`INSERT INTO runtime_metadata(singleton, schema_identity, schema_revision, initialized_at) VALUES (1, ?, ?, ?)`,
			schemaIdentity, latest, now.UTC().Format(time.RFC3339Nano))
	} else if current < latest {
		_, err = transaction.ExecContext(ctx, `UPDATE runtime_metadata SET schema_revision = ? WHERE singleton = 1`, latest)
	}
	if err != nil {
		return 0, fmt.Errorf("record SQLite schema revision: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return 0, fmt.Errorf("commit SQLite schema migration: %w", err)
	}
	return latest, nil
}

type schemaQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// readSchemaRevision returns 0 for an empty database, the recorded revision
// for a ViberMate database, and ErrUnsupportedSchema for anything else.
func readSchemaRevision(ctx context.Context, database schemaQuerier) (int64, error) {
	var hasMetadata bool
	if err := database.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = 'runtime_metadata')`,
	).Scan(&hasMetadata); err != nil {
		return 0, fmt.Errorf("inspect SQLite schema: %w", err)
	}
	if !hasMetadata {
		var objects int
		if err := database.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`,
		).Scan(&objects); err != nil {
			return 0, fmt.Errorf("inspect empty SQLite schema: %w", err)
		}
		if objects != 0 {
			return 0, fmt.Errorf("%w: database contains %d foreign schema objects", ErrUnsupportedSchema, objects)
		}
		return 0, nil
	}
	var identity string
	var revision int64
	if err := database.QueryRowContext(ctx,
		`SELECT schema_identity, schema_revision FROM runtime_metadata WHERE singleton = 1`,
	).Scan(&identity, &revision); err != nil {
		return 0, fmt.Errorf("%w: read schema revision: %v", ErrUnsupportedSchema, err)
	}
	if identity != schemaIdentity || revision < 1 {
		return 0, fmt.Errorf("%w: identity %q revision %d", ErrUnsupportedSchema, identity, revision)
	}
	return revision, nil
}
