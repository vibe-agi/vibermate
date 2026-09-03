package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSQLiteStoreReopensWithContinuousSchemaState(t *testing.T) {
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
		t.Fatalf("busy timeout = %d, want %d", settings.BusyTimeoutMillis, DefaultBusyTimeout.Milliseconds())
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
		t.Fatalf("schema state changed across reopen: first=%+v second=%+v", firstState, secondState)
	}
}

func TestSQLiteStoreRejectsUnsupportedDatabase(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	database := sql.OpenDB(newSQLiteConnector(databasePath, DefaultBusyTimeout))
	if _, err := database.Exec(`CREATE TABLE obsolete_development_schema(id INTEGER PRIMARY KEY)`); err != nil {
		_ = database.Close()
		t.Fatalf("create unsupported database: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close unsupported database: %v", err)
	}

	store, err := Open(context.Background(), Options{
		DatabasePath:           databasePath,
		BusyTimeout:            DefaultBusyTimeout,
		CommitReconcileTimeout: DefaultCommitReconcileTimeout,
	})
	if store != nil {
		_ = store.Shutdown(context.Background())
		t.Fatal("unsupported database returned a store")
	}
	if !errors.Is(err, ErrSchemaBaselineMismatch) {
		t.Fatalf("unsupported database error = %v", err)
	}
}

func TestSQLiteStoreRejectsChangedCurrentSchema(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, databasePath)
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("close store: %v", err)
	}

	database := sql.OpenDB(newSQLiteConnector(databasePath, DefaultBusyTimeout))
	if _, err := database.Exec(
		`UPDATE runtime_metadata SET schema_source_sha256 = ? WHERE singleton = 1`,
		"0000000000000000000000000000000000000000000000000000000000000000",
	); err != nil {
		_ = database.Close()
		t.Fatalf("change schema binding: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close changed database: %v", err)
	}

	store, err := Open(context.Background(), Options{
		DatabasePath:           databasePath,
		BusyTimeout:            DefaultBusyTimeout,
		CommitReconcileTimeout: DefaultCommitReconcileTimeout,
	})
	if store != nil {
		_ = store.Shutdown(context.Background())
		t.Fatal("changed database returned a store")
	}
	if !errors.Is(err, ErrSchemaBaselineMismatch) {
		t.Fatalf("changed database error = %v", err)
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

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 40*time.Millisecond)
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
	if _, readErr := store.SchemaStateReader().ReadSchemaState(context.Background()); !errors.Is(readErr, ErrStoreClosing) {
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

func openTestStore(t testing.TB, databasePath string) *Store {
	t.Helper()
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

func assertInitialSchemaState(t *testing.T, state SchemaState) {
	t.Helper()
	if state.Revision != currentSchemaRevision {
		t.Fatalf("schema revision = %d, want %d", state.Revision, currentSchemaRevision)
	}
	if state.InitializedAt == "" {
		t.Fatal("schema initialization timestamp is empty")
	}
	if state.Identity != currentSchemaIdentity {
		t.Fatalf("schema identity = %q, want %q", state.Identity, currentSchemaIdentity)
	}
	if len(state.SourceSHA256) != 64 {
		t.Fatalf("schema source digest length = %d, want 64", len(state.SourceSHA256))
	}
	if _, err := hex.DecodeString(state.SourceSHA256); err != nil {
		t.Fatalf("schema source digest = %q: %v", state.SourceSHA256, err)
	}
}
