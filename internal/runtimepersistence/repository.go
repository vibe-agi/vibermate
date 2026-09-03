package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var (
	ErrSchemaBaselineMismatch = errors.New("database was created from an unsupported development baseline")
)

// SchemaState is an immutable view of the durable schema authority.
type SchemaState struct {
	Revision      int64
	Identity      string
	SourceSHA256  string
	InitializedAt string
}

const (
	currentSchemaIdentity = "vibermate-runtime-clean-baseline"
	currentSchemaRevision = int64(1)
)

// DatabaseSettings records connection invariants that must hold on every
// SQLite connection used by the runtime.
type DatabaseSettings struct {
	JournalMode       string
	ForeignKeys       bool
	BusyTimeoutMillis int64
}

// SchemaStateReader reads schema state for runtime initialization. It is
// intentionally distinct from the Environment aggregate SnapshotResolver.
type SchemaStateReader interface {
	ReadSchemaState(context.Context) (SchemaState, error)
}

type Repository struct {
	database                   *sql.DB
	operations                 *operationGate
	expectedSchemaSourceSHA256 string
}

var _ SchemaStateReader = (*Repository)(nil)

func newRepository(
	database *sql.DB,
	operations *operationGate,
	expectedSchemaSourceSHA256 string,
) *Repository {
	return &Repository{
		database:                   database,
		operations:                 operations,
		expectedSchemaSourceSHA256: expectedSchemaSourceSHA256,
	}
}

func (r *Repository) ReadSchemaState(ctx context.Context) (SchemaState, error) {
	operationContext, finish, err := r.operations.begin(ctx)
	if err != nil {
		return SchemaState{}, err
	}
	defer finish()

	transaction, err := r.database.BeginTx(operationContext, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SchemaState{}, fmt.Errorf("begin schema state transaction: %w", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	var state SchemaState
	if err := transaction.QueryRowContext(
		operationContext,
		`SELECT schema_identity, schema_revision, schema_source_sha256, initialized_at
		 FROM runtime_metadata
		 WHERE singleton = 1`,
	).Scan(
		&state.Identity,
		&state.Revision,
		&state.SourceSHA256,
		&state.InitializedAt,
	); err != nil {
		return SchemaState{}, fmt.Errorf("%w: read runtime metadata: %v", ErrSchemaBaselineMismatch, err)
	}
	if state.Identity != currentSchemaIdentity {
		return SchemaState{}, fmt.Errorf(
			"%w: identity %q",
			ErrSchemaBaselineMismatch,
			state.Identity,
		)
	}
	if state.Revision != currentSchemaRevision {
		return SchemaState{}, fmt.Errorf(
			"%w: revision %d",
			ErrSchemaBaselineMismatch,
			state.Revision,
		)
	}
	if state.SourceSHA256 != r.expectedSchemaSourceSHA256 {
		return SchemaState{}, fmt.Errorf(
			"%w: source digest %q",
			ErrSchemaBaselineMismatch,
			state.SourceSHA256,
		)
	}
	if err := transaction.Commit(); err != nil {
		return SchemaState{}, fmt.Errorf("commit schema state transaction: %w", err)
	}
	return state, nil
}

func (r *Repository) Settings(ctx context.Context) (DatabaseSettings, error) {
	operationContext, finish, err := r.operations.begin(ctx)
	if err != nil {
		return DatabaseSettings{}, err
	}
	defer finish()

	var settings DatabaseSettings
	var foreignKeys int

	if err := r.database.QueryRowContext(
		operationContext,
		`PRAGMA journal_mode`,
	).Scan(&settings.JournalMode); err != nil {
		return DatabaseSettings{}, fmt.Errorf("read SQLite journal mode: %w", err)
	}
	if err := r.database.QueryRowContext(
		operationContext,
		`PRAGMA foreign_keys`,
	).Scan(&foreignKeys); err != nil {
		return DatabaseSettings{}, fmt.Errorf("read SQLite foreign key setting: %w", err)
	}
	if err := r.database.QueryRowContext(
		operationContext,
		`PRAGMA busy_timeout`,
	).Scan(&settings.BusyTimeoutMillis); err != nil {
		return DatabaseSettings{}, fmt.Errorf("read SQLite busy timeout: %w", err)
	}
	settings.ForeignKeys = foreignKeys == 1
	return settings, nil
}
