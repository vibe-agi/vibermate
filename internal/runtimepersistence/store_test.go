package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
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

func TestSQLiteSharedOriginMigrationPreservesEndpointAccountBindings(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	seedRuntimeSchemaBeforeSharedOrigins(t, databasePath)

	store := openTestStore(t, databasePath)
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Errorf("close migrated store: %v", err)
		}
	}()

	var endpointID string
	if err := store.database.QueryRowContext(
		context.Background(),
		`SELECT upstream_endpoint_id
		 FROM provider_accounts
		 WHERE account_id = 'account.shared.openai'`,
	).Scan(&endpointID); err != nil {
		t.Fatalf("read migrated Account binding: %v", err)
	}
	if endpointID != "target.shared.openai" {
		t.Fatalf("migrated Account endpoint = %q", endpointID)
	}

	if _, err := store.database.ExecContext(
		context.Background(),
		`INSERT INTO upstream_endpoints(
		   endpoint_id, display_name, origin, realm_id,
		   backend_protocols_json, capabilities_json, drivers_json,
		   state, revision, created_at_unix_ms, updated_at_unix_ms
		 ) VALUES (
		   'target.shared.anthropic', 'Shared Anthropic',
		   'http://127.0.0.1:23333', 'anthropic.official',
		   CAST('["anthropic_messages"]' AS BLOB),
		   CAST('["messages","streaming","tool_calls"]' AS BLOB),
		   CAST('["anthropic_api_key","static_header"]' AS BLOB),
		   'active', 1, 1787410329000, 1787410329000
		 )`,
	); err != nil {
		t.Fatalf("insert second explicit protocol at shared origin: %v", err)
	}

	rows, err := store.database.QueryContext(context.Background(), `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("check migrated foreign keys: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		var table, parent string
		var rowID, foreignKeyID int64
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			t.Fatalf("read foreign-key violation: %v", err)
		}
		t.Fatalf(
			"foreign-key violation table=%s row=%d parent=%s key=%d",
			table,
			rowID,
			parent,
			foreignKeyID,
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

func TestSQLiteStoreRejectsThePreBaselineDevelopmentSchema(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	seedGooseHistory(t, databasePath, 1, true)
	database := sql.OpenDB(newSQLiteConnector(databasePath, DefaultBusyTimeout))
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if _, err := database.ExecContext(
		context.Background(),
		`CREATE TABLE runtime_metadata (
			singleton INTEGER PRIMARY KEY NOT NULL CHECK (singleton = 1),
			initialized_at TEXT NOT NULL
		) STRICT;
		INSERT INTO runtime_metadata (singleton, initialized_at)
		VALUES (1, '2026-08-04T00:00:00Z')`,
	); err != nil {
		_ = database.Close()
		t.Fatalf("create pre-baseline metadata: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close pre-baseline fixture: %v", err)
	}

	store, err := Open(context.Background(), Options{
		DatabasePath:           databasePath,
		BusyTimeout:            DefaultBusyTimeout,
		CommitReconcileTimeout: DefaultCommitReconcileTimeout,
	})
	if store != nil {
		_ = store.Shutdown(context.Background())
		t.Fatal("pre-baseline database returned a store")
	}
	if !errors.Is(err, ErrSchemaBaselineMismatch) {
		t.Fatalf("pre-baseline open error = %v", err)
	}
}

func TestSQLiteStoreRejectsDevelopmentDatabaseWithoutSourceBinding(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	seedGooseHistory(t, databasePath, 1, true)
	database := sql.OpenDB(newSQLiteConnector(databasePath, DefaultBusyTimeout))
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if _, err := database.ExecContext(
		context.Background(),
		`CREATE TABLE runtime_metadata (
			singleton INTEGER PRIMARY KEY NOT NULL CHECK (singleton = 1),
			schema_identity TEXT NOT NULL,
			initialized_at TEXT NOT NULL
		) STRICT;
		INSERT INTO runtime_metadata (singleton, schema_identity, initialized_at)
		VALUES (1, ?, '2026-08-04T00:00:00Z')`,
		currentSchemaIdentity,
	); err != nil {
		_ = database.Close()
		t.Fatalf("create unbound development metadata: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close unbound development fixture: %v", err)
	}

	store, err := Open(context.Background(), Options{
		DatabasePath:           databasePath,
		BusyTimeout:            DefaultBusyTimeout,
		CommitReconcileTimeout: DefaultCommitReconcileTimeout,
	})
	if store != nil {
		_ = store.Shutdown(context.Background())
		t.Fatal("unbound development database returned a store")
	}
	if !errors.Is(err, ErrSchemaBaselineMismatch) {
		t.Fatalf("unbound development database error = %v", err)
	}
}

func TestSQLiteStoreRejectsEveryEarlierDevelopmentBaselineIdentity(t *testing.T) {
	t.Parallel()
	for _, identity := range []string{
		"vibermate-runtime",
		"obsolete-development-baseline",
	} {
		identity := identity
		t.Run(identity, func(t *testing.T) {
			t.Parallel()
			databasePath := filepath.Join(t.TempDir(), "runtime.db")
			seedGooseHistory(t, databasePath, 1, true)
			database := sql.OpenDB(newSQLiteConnector(databasePath, DefaultBusyTimeout))
			database.SetMaxOpenConns(1)
			database.SetMaxIdleConns(1)
			if _, err := database.ExecContext(
				context.Background(),
				`CREATE TABLE runtime_metadata (
			singleton INTEGER PRIMARY KEY NOT NULL CHECK (singleton = 1),
			schema_identity TEXT NOT NULL,
			initialized_at TEXT NOT NULL
		) STRICT;
		INSERT INTO runtime_metadata (singleton, schema_identity, initialized_at)
		VALUES (1, ?, '2026-08-04T00:00:00Z')`,
				identity,
			); err != nil {
				_ = database.Close()
				t.Fatalf("create obsolete baseline metadata: %v", err)
			}
			if err := database.Close(); err != nil {
				t.Fatalf("close obsolete baseline fixture: %v", err)
			}

			store, err := Open(context.Background(), Options{
				DatabasePath:           databasePath,
				BusyTimeout:            DefaultBusyTimeout,
				CommitReconcileTimeout: DefaultCommitReconcileTimeout,
			})
			if store != nil {
				_ = store.Shutdown(context.Background())
				t.Fatal("obsolete baseline database returned a store")
			}
			if !errors.Is(err, ErrSchemaBaselineMismatch) {
				t.Fatalf("obsolete baseline open error = %v", err)
			}
		})
	}
}

func TestSchemaRevisionOfSources(t *testing.T) {
	t.Parallel()

	revision, err := schemaRevisionOfSources([]*goose.Source{
		{Version: 1},
		{Version: 2},
		{Version: 3},
	})
	if err != nil {
		t.Fatalf("derive schema revision: %v", err)
	}
	if revision != 3 {
		t.Fatalf("derived schema revision = %d, want 3", revision)
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

func TestMigrationSourceDigestBindsNamesAndContents(t *testing.T) {
	t.Parallel()
	first := fstest.MapFS{
		"00001.sql": {Data: []byte("SELECT 1;")},
		"00002.sql": {Data: []byte("SELECT 2;")},
	}
	digest, err := migrationSourceDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != 64 {
		t.Fatalf("digest length = %d, want 64", len(digest))
	}
	if _, err := hex.DecodeString(digest); err != nil {
		t.Fatalf("digest is not lowercase hexadecimal: %q", digest)
	}

	contentChanged := fstest.MapFS{
		"00001.sql": {Data: []byte("SELECT 1;")},
		"00002.sql": {Data: []byte("SELECT 3;")},
	}
	changedDigest, err := migrationSourceDigest(contentChanged)
	if err != nil {
		t.Fatal(err)
	}
	if changedDigest == digest {
		t.Fatal("migration content change preserved the source digest")
	}

	nameChanged := fstest.MapFS{
		"00001.sql": {Data: []byte("SELECT 1;")},
		"00003.sql": {Data: []byte("SELECT 2;")},
	}
	renamedDigest, err := migrationSourceDigest(nameChanged)
	if err != nil {
		t.Fatal(err)
	}
	if renamedDigest == digest {
		t.Fatal("migration name change preserved the source digest")
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

func openTestStore(t testing.TB, databasePath string) *Store {
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

func seedRuntimeSchemaBeforeSharedOrigins(t *testing.T, databasePath string) {
	t.Helper()
	const previousRevision = 3
	previous := fstest.MapFS{}
	for _, name := range []string{
		"00001_runtime_schema.sql",
		"00002_exchange_agent_identity.sql",
		"00003_protocol_agent_identity.sql",
	} {
		payload, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read previous migration %s: %v", name, err)
		}
		previous[name] = &fstest.MapFile{Data: payload}
	}
	digest, err := migrationSourceDigest(previous)
	if err != nil {
		t.Fatalf("digest previous migration set: %v", err)
	}
	database := sql.OpenDB(newSQLiteConnector(databasePath, DefaultBusyTimeout))
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		database,
		previous,
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		_ = database.Close()
		t.Fatalf("construct previous migration provider: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := provider.Up(ctx); err != nil {
		_ = database.Close()
		t.Fatalf("apply previous migrations: %v", err)
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil || version != previousRevision {
		_ = database.Close()
		t.Fatalf("previous schema revision = %d, err=%v", version, err)
	}
	if _, err := database.ExecContext(
		ctx,
		`UPDATE runtime_metadata SET schema_source_sha256 = ? WHERE singleton = 1;
		 INSERT INTO upstream_endpoints(
		   endpoint_id, display_name, origin, realm_id,
		   backend_protocols_json, capabilities_json, drivers_json,
		   state, revision, created_at_unix_ms, updated_at_unix_ms
		 ) VALUES (
		   'target.shared.openai', 'Shared OpenAI',
		   'http://127.0.0.1:23333', 'openai.platform',
		   CAST('["openai_responses","openai_chat"]' AS BLOB),
		   CAST('["messages","streaming","tool_calls"]' AS BLOB),
		   CAST('["static_header"]' AS BLOB),
		   'active', 1, 1787410329000, 1787410329000
		 );
		 INSERT INTO provider_accounts(
		   account_id, display_name, upstream_endpoint_id, realm_id, driver_ref,
		   secret_reference, state, revision, created_at_unix_ms, updated_at_unix_ms
		 ) VALUES (
		   'account.shared.openai', 'Shared Account', 'target.shared.openai',
		   'openai.platform', 'static_header', 'secret://migration/shared-account',
		   'active', 1, 1787410329000, 1787410329000
		 )`,
		digest,
	); err != nil {
		_ = database.Close()
		t.Fatalf("seed previous Endpoint and Account: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close previous migration fixture: %v", err)
	}
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
	wantRevision := embeddedSchemaRevisionForTest(t)
	if state.Revision != wantRevision {
		t.Fatalf("schema revision = %d, want %d", state.Revision, wantRevision)
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
