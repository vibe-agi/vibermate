package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

func TestSQLiteStoreReopensWithContinuousSchemaRevision(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	first := openTestStore(t, databasePath)

	firstState, err := first.SchemaStateReader().ReadSchemaState(context.Background())
	if err != nil {
		t.Fatalf("read first schema state: %v", err)
	}
	assertInitialSchemaState(t, firstState)

	settings, err := first.Settings(context.Background())
	if err != nil {
		t.Fatalf("read database settings: %v", err)
	}
	if settings.JournalMode != "wal" {
		t.Fatalf("journal mode = %q, want wal", settings.JournalMode)
	}
	if !settings.ForeignKeys {
		t.Fatal("foreign keys are disabled")
	}
	if settings.BusyTimeoutMillis != DefaultBusyTimeout.Milliseconds() {
		t.Fatalf(
			"busy timeout = %d, want %d",
			settings.BusyTimeoutMillis,
			DefaultBusyTimeout.Milliseconds(),
		)
	}

	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatalf("close first store: %v", err)
	}
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatalf("close first store again: %v", err)
	}

	second := openTestStore(t, databasePath)
	defer func() {
		if err := second.Shutdown(context.Background()); err != nil {
			t.Errorf("close reopened store: %v", err)
		}
	}()

	secondState, err := second.SchemaStateReader().ReadSchemaState(context.Background())
	if err != nil {
		t.Fatalf("read reopened schema state: %v", err)
	}
	if secondState != firstState {
		t.Fatalf(
			"schema state changed across reopen: first=%+v second=%+v",
			firstState,
			secondState,
		)
	}
}

func TestSQLiteStoreRejectsFutureGooseHistoryBeforeMigrations(t *testing.T) {
	t.Parallel()

	embeddedRevision := embeddedSchemaRevisionForTest(t)
	tests := []struct {
		name      string
		revision  int64
		isApplied bool
	}{
		{
			name:      "current plus one applied",
			revision:  embeddedRevision + 1,
			isApplied: true,
		},
		{
			name:      "distant rolled back history",
			revision:  embeddedRevision + 100,
			isApplied: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			databasePath := filepath.Join(t.TempDir(), "runtime.db")
			seedGooseHistory(t, databasePath, test.revision, test.isApplied)

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			store, err := Open(ctx, Options{
				DatabasePath:           databasePath,
				BusyTimeout:            DefaultBusyTimeout,
				CommitReconcileTimeout: DefaultCommitReconcileTimeout,
			})
			if store != nil {
				_ = store.Shutdown(context.Background())
				t.Fatal("future-schema open returned a store")
			}
			if !errors.Is(err, ErrSchemaNewerThanBinary) {
				t.Fatalf("future-schema open error = %v, want ErrSchemaNewerThanBinary", err)
			}
			wantDiagnostic := fmt.Sprintf(
				"database revision %d, embedded revision %d",
				test.revision,
				embeddedRevision,
			)
			if !strings.Contains(err.Error(), wantDiagnostic) {
				t.Fatalf("future-schema diagnostic = %q, want %q", err, wantDiagnostic)
			}
			assertSQLiteTableAbsent(t, databasePath, "runtime_metadata")
		})
	}
}

func TestSchemaRevisionOfSources(t *testing.T) {
	t.Parallel()

	revision, err := schemaRevisionOfSources([]*goose.Source{
		{Version: 1},
		{Version: 4},
		{Version: 26},
		{Version: 27},
	})
	if err != nil {
		t.Fatalf("derive schema revision: %v", err)
	}
	if revision != 27 {
		t.Fatalf("derived schema revision = %d, want 27", revision)
	}

	invalidSources := []struct {
		name    string
		sources []*goose.Source
	}{
		{name: "empty"},
		{name: "nil source", sources: []*goose.Source{nil}},
		{name: "zero version", sources: []*goose.Source{{Version: 0}}},
		{
			name: "duplicate version",
			sources: []*goose.Source{
				{Version: 1},
				{Version: 1},
			},
		},
		{
			name: "descending version",
			sources: []*goose.Source{
				{Version: 2},
				{Version: 1},
			},
		},
	}
	for _, test := range invalidSources {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := schemaRevisionOfSources(test.sources); err == nil {
				t.Fatal("invalid migration sources were accepted")
			}
		})
	}
}

func TestSQLiteShutdownCancelsAndDrainsHeldTransaction(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	operationContext, finish, err := store.operations.begin(context.Background())
	if err != nil {
		t.Fatalf("admit transaction: %v", err)
	}
	transaction, err := store.database.BeginTx(operationContext, nil)
	if err != nil {
		finish()
		t.Fatalf("begin held transaction: %v", err)
	}

	released := make(chan struct{})
	go func() {
		<-operationContext.Done()
		_ = transaction.Rollback()
		close(released)
		finish()
	}()

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := store.Shutdown(shutdownContext); err != nil {
		t.Fatalf("shutdown store with cancellable transaction: %v", err)
	}
	select {
	case <-released:
	default:
		t.Fatal("store shutdown returned before the held transaction released")
	}
	if !errors.Is(context.Cause(operationContext), ErrStoreClosing) {
		t.Fatalf("transaction cancellation cause = %v, want ErrStoreClosing", context.Cause(operationContext))
	}
}

func TestSQLiteShutdownDeadlineBoundsUnreleasedTransaction(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	operationContext, finish, err := store.operations.begin(context.Background())
	if err != nil {
		t.Fatalf("admit transaction: %v", err)
	}
	transaction, err := store.database.BeginTx(operationContext, nil)
	if err != nil {
		finish()
		t.Fatalf("begin held transaction: %v", err)
	}

	shutdownContext, cancelShutdown := context.WithTimeout(
		context.Background(),
		40*time.Millisecond,
	)
	started := time.Now()
	shutdownErr := store.Shutdown(shutdownContext)
	cancelShutdown()
	if !errors.Is(shutdownErr, context.DeadlineExceeded) {
		t.Fatalf("expected shutdown deadline, got %v", shutdownErr)
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("bounded SQLite shutdown took too long: %v", elapsed)
	}
	if !errors.Is(context.Cause(operationContext), ErrStoreClosing) {
		t.Fatalf("transaction cancellation cause = %v, want ErrStoreClosing", context.Cause(operationContext))
	}
	if _, readErr := store.SchemaStateReader().ReadSchemaState(context.Background()); !errors.Is(
		readErr,
		ErrStoreClosing,
	) {
		t.Fatalf("closed admission accepted a new schema read: %v", readErr)
	}

	_ = transaction.Rollback()
	finish()
	retryContext, cancelRetry := context.WithTimeout(context.Background(), time.Second)
	defer cancelRetry()
	if err := store.Shutdown(retryContext); err != nil {
		t.Fatalf("retry SQLite shutdown after transaction release: %v", err)
	}
}

func TestSQLiteStoreProtectsPersistentArtifacts(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows permissions require a native DACL adapter")
	}

	dataDirectory := filepath.Join(t.TempDir(), "data")
	databasePath := filepath.Join(dataDirectory, "runtime.db")
	store := openTestStore(t, databasePath)
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()

	directoryInfo, err := os.Stat(dataDirectory)
	if err != nil {
		t.Fatalf("stat data directory: %v", err)
	}
	if permissions := directoryInfo.Mode().Perm(); permissions != 0o700 {
		t.Fatalf("data directory permissions = %o, want 700", permissions)
	}
	databaseInfo, err := os.Stat(databasePath)
	if err != nil {
		t.Fatalf("stat database: %v", err)
	}
	if permissions := databaseInfo.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("database permissions = %o, want 600", permissions)
	}
}

func openTestStore(t *testing.T, databasePath string) *Store {
	t.Helper()
	// The bound is a harness guard against a hung open, not a product startup
	// contract. Applying every migration on a pure-Go SQLite driver under the
	// race detector is slow, and trimming a migration to fit a test clock
	// would be shaping the schema around the harness.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store, err := Open(ctx, Options{
		DatabasePath:           databasePath,
		BusyTimeout:            DefaultBusyTimeout,
		CommitReconcileTimeout: DefaultCommitReconcileTimeout,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return store
}

func embeddedSchemaRevisionForTest(t *testing.T) int64 {
	t.Helper()
	migrations, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatalf("open embedded migrations: %v", err)
	}
	database := sql.OpenDB(newSQLiteConnector(
		filepath.Join(t.TempDir(), "embedded-revision.db"),
		DefaultBusyTimeout,
	))
	defer func() {
		if err := database.Close(); err != nil {
			t.Errorf("close embedded-revision database: %v", err)
		}
	}()
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		database,
		migrations,
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		t.Fatalf("construct migration provider: %v", err)
	}
	revision, err := schemaRevisionOfSources(provider.ListSources())
	if err != nil {
		t.Fatalf("derive embedded schema revision: %v", err)
	}
	return revision
}

func seedGooseHistory(
	t *testing.T,
	databasePath string,
	revision int64,
	isApplied bool,
) {
	t.Helper()
	database := sql.OpenDB(newSQLiteConnector(databasePath, DefaultBusyTimeout))
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.PingContext(context.Background()); err != nil {
		_ = database.Close()
		t.Fatalf("open future-schema fixture: %v", err)
	}
	if _, err := database.ExecContext(
		context.Background(),
		`CREATE TABLE goose_db_version (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version_id INTEGER NOT NULL,
			is_applied INTEGER NOT NULL,
			tstamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	); err != nil {
		_ = database.Close()
		t.Fatalf("create Goose history fixture: %v", err)
	}
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (?, ?)`,
		revision,
		isApplied,
	); err != nil {
		_ = database.Close()
		t.Fatalf("seed Goose history fixture: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close future-schema fixture: %v", err)
	}
}

func assertSQLiteTableAbsent(t *testing.T, databasePath string, tableName string) {
	t.Helper()
	database := sql.OpenDB(newSQLiteConnector(databasePath, DefaultBusyTimeout))
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer func() {
		if err := database.Close(); err != nil {
			t.Errorf("close inspected database: %v", err)
		}
	}()
	var count int
	if err := database.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		tableName,
	).Scan(&count); err != nil {
		t.Fatalf("inspect SQLite table %q: %v", tableName, err)
	}
	if count != 0 {
		t.Fatalf("SQLite table %q exists after rejected migration", tableName)
	}
}

func assertInitialSchemaState(t *testing.T, state SchemaState) {
	t.Helper()
	if state.Revision != 27 {
		t.Fatalf("schema revision = %d, want 27", state.Revision)
	}
	if state.InitializedAt == "" {
		t.Fatal("schema initialization timestamp is empty")
	}
}
