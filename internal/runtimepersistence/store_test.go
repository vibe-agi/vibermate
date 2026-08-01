package runtimepersistence

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
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

func assertInitialSchemaState(t *testing.T, state SchemaState) {
	t.Helper()
	if state.Revision != 20 {
		t.Fatalf("schema revision = %d, want 20", state.Revision)
	}
	if state.InitializedAt == "" {
		t.Fatal("schema initialization timestamp is empty")
	}
}
