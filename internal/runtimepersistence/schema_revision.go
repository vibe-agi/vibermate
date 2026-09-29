package runtimepersistence

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"
)

// The Runtime database has one schema, schema.sql, identified by an integer
// revision. There are no migrations: a file created with another revision is
// refused before any write, and the user starts a new data directory. Any
// change to schema.sql bumps schemaRevision; TestSchemaRevisionTracksSchemaFile
// fails until it does.
//
//go:embed schema.sql
var schemaSQL string

const (
	// schemaIdentity marks a SQLite file as a ViberMate Runtime database.
	schemaIdentity = "vibermate-runtime"
	schemaRevision = int64(1)
)

// ErrUnsupportedSchema reports a file that is not a ViberMate Runtime database
// or that another schema revision created.
var ErrUnsupportedSchema = errors.New("database was created by a different ViberMate schema revision; start a new data directory")

// initializeSchema creates the schema in an empty database, or confirms that
// an existing one has this build's revision.
func initializeSchema(ctx context.Context, database *sql.DB, now time.Time) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin SQLite schema initialization: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	revision, err := readSchemaRevision(ctx, transaction)
	if err != nil {
		return err
	}
	if revision == schemaRevision {
		return transaction.Commit()
	}
	if revision != 0 {
		return fmt.Errorf("%w: file revision %d, build revision %d", ErrUnsupportedSchema, revision, schemaRevision)
	}
	if _, err := transaction.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("initialize SQLite schema: %w", err)
	}
	if _, err := transaction.ExecContext(ctx,
		`INSERT INTO runtime_metadata(singleton, schema_identity, schema_revision, initialized_at) VALUES (1, ?, ?, ?)`,
		schemaIdentity, schemaRevision, now.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("record SQLite schema revision: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit SQLite schema initialization: %w", err)
	}
	return nil
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
