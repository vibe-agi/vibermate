package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

// This stopped fixture is frozen v1, independent of the active bootstrap.
type migrationFixture struct {
	source, snapshot     string
	owner, snapshotOwner *instanceguard.Guard
	before               map[string][]string
	columns              map[string][]string
}

// Frozen fixture access is test-only; production Open must always refuse v1.
func openFrozenV1Store(t *testing.T, path string) *Store {
	t.Helper()
	db := openSQLiteForTest(t, path)
	nativeExec(t, db, schemaV1SQL)
	nativeExec(t, db, `INSERT INTO runtime_metadata VALUES(1,'vibermate-runtime',1,'2026-10-03T00:00:00Z')`)
	operations := newOperationGate()
	reads := sql.OpenDB(newSQLiteReadConnector(path, time.Second))
	return &Store{databasePath: path, database: db, reads: reads, operations: operations, closeDone: make(chan struct{}), repo: newRepository(db, operations), activityRepo: newActivityRepository(db, reads, operations), providerAccounts: newProviderAccountRepository(db, operations, time.Second, sqlTransactionCommitter{}), upstreamEndpoints: newUpstreamEndpointRepository(db, operations, time.Second, sqlTransactionCommitter{}), environments: newEnvironmentRepository(db, operations, time.Second, sqlTransactionCommitter{}), exchangeContents: newExchangeContentRepository(db, operations), egressAttempts: newEgressAttemptRepository(db, operations), rawEvidence: newRawEvidenceRepository(db, operations)}
}
func seedPopulatedV1(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	writeAnthropicEndpoint(t, store)
	ref, _ := secretstore.ParseReference("secret://provider-account/native-legacy")
	account := provideraccount.Account{ID: "native-legacy", DisplayName: "Legacy", Origin: providerTestOrigin(t), AssociationRevision: 1, RealmID: "anthropic.official", Driver: providerauth.AnthropicAPIKeyDriverRef(), SecretRef: ref, State: provideraccount.StateActive, Revision: 1, SettingsRevision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := store.ProviderAccountRepository().Write(ctx, 0, account); err != nil {
		t.Fatal(err)
	}
	manager := newEnvironmentManager(t, store)
	candidate := environmentFixture(t, "native-legacy", 1)
	candidate.ContentRecording = environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}
	draft, err := manager.SaveDraft(ctx, environment.DraftCommand{Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := manager.Preview(ctx, candidate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(ctx, preview); err != nil {
		t.Fatal(err)
	}
	if err := store.ExchangeContentRepository().Put(ctx, contentRecordFixture(t, "legacy-history", now)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EgressAttemptRepository().Append(ctx, providerAttempt(t, "legacy-audit")); err != nil {
		t.Fatal(err)
	}
	raw := rawEvidenceRecordForTest("legacy-writer.1", 1, rawevidence.LayerClientIngress, []byte(`{"legacy":"raw"}`), []byte(`{"version":1,"headers":[]}`))
	if err := store.RawEvidenceRepository().AppendBatch(ctx, []rawevidence.StoredEnvelope{raw}, now); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, store.database, `INSERT INTO tool_approvals(approval_id,revision,kind,subject_refs_json,subject_labels_json,aggregate_key,state,created_at_unix_ms,expires_at_unix_ms) VALUES('legacy-approval',1,'client_root_ask',X'5b2261225d',X'5b2262225d','legacy-root','pending',1,2)`)
	nativeExec(t, store.database, `INSERT INTO code_library_collections VALUES('legacy','Legacy'); UPDATE runtime_usage_policy SET enabled=0,retention_days=90,collecting_since_unix_ms=NULL`)
	record := activity.Record{ID: "legacy-activity", OccurredAt: now, Kind: activity.KindExchangeStarted, SubjectID: "legacy-exchange", Status: activity.StatusPending, SourceKind: activity.SourceCaptureRun, SourceDisplayName: "claude", SourceRecognition: activity.SourceRecognitionConfigured, CaptureRunID: "legacy-run", ConnectionID: "legacy-connection"}
	setFrozenExecutionEvidence(&record, "legacy")
	record.AccountID = ""
	record.AccountRevision = 0
	record.CredentialEpoch = 0
	if _, err := store.ActivityRepository().Append(ctx, record); err != nil {
		t.Fatal(err)
	}
	insertUsageFixture(t, store, runtimeusage.Observation{ExchangeID: "legacy-usage", CaptureRunID: "legacy-run", Source: "member", OccurredAt: now, Status: activity.StatusSucceeded, EnvironmentID: "native-legacy", EnvironmentName: "Frozen legacy", UpstreamModel: "legacy-model", Client: "codex", Usage: protocolcore.Usage{InputUncached: protocolcore.UsageValue{Known: true, Tokens: 11, Source: "fixture"}}}, now.Add(24*time.Hour))
}

func populatedV1MigrationFixture(t *testing.T) migrationFixture {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	snapshot := filepath.Join(root, "snapshot")
	for _, dir := range []string{source, snapshot} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	store := openFrozenV1Store(t, filepath.Join(source, "runtime.db"))
	seedPopulatedV1(t, store)
	db := store.database
	nativeExec(t, db, `INSERT INTO runtime_egress_attempts(sequence,attempt_id,purpose,payload_class,parent_kind,parent_id,caller_kind,target_origin,policy_id,policy_revision,policy_authority,rule_id,proxy_id,proxy_revision,account_id,account_settings_revision,started_at_unix_ms) VALUES(90000,'deleted-high','update','runtime','runtime_action','legacy','core','https://legacy.example','legacy',1,'runtime','legacy','direct',0,'',0,1); DELETE FROM runtime_egress_attempts WHERE sequence=90000`)
	columns := nativeLegacyColumns(t, db)
	before := nativeFingerprint(t, db, columns)
	// Copy committed WAL from the synthetic builder before its close checkpoints
	// it. The stopped source has no SHM and no running SQLite connection.
	stopped := filepath.Join(root, "stopped")
	if err := os.Mkdir(stopped, 0700); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal"} {
		encoded, err := os.ReadFile(filepath.Join(source, "runtime.db") + suffix)
		if err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{stopped, snapshot} {
			if err := os.WriteFile(filepath.Join(dir, "runtime.db")+suffix, encoded, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	source = stopped
	for _, dir := range []string{source, snapshot} {
		if err := os.WriteFile(filepath.Join(dir, "synthetic-private-config"), []byte("private-test-only"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := instanceguard.Acquire(filepath.Join(source, "runtime-data.lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Release() })
	snapshotOwner, err := instanceguard.Acquire(filepath.Join(snapshot, "runtime-data.lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { snapshotOwner.Release() })
	return migrationFixture{source, snapshot, owner, snapshotOwner, before, columns}
}
func (f migrationFixture) input(t *testing.T) MigrationInput {
	t.Helper()
	verified, err := VerifyMigrationDatabase(context.Background(), filepath.Join(f.source, "runtime.db"), filepath.Join(f.snapshot, "runtime.db"), f.owner, f.snapshotOwner, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return MigrationInput{Database: verified, ReconcileTimeout: 30 * time.Second}
}
func (f migrationFixture) assertLegacyPreserved(t *testing.T) {
	t.Helper()
	db := openSQLiteForTest(t, filepath.Join(f.source, "runtime.db"))
	after := nativeFingerprint(t, db, f.columns)
	// The one permitted metadata change is checked separately.
	delete(after, "runtime_metadata")
	want := make(map[string][]string)
	for table, rows := range f.before {
		if table != "runtime_metadata" {
			want[table] = rows
		}
	}
	if !reflect.DeepEqual(want, after) {
		t.Fatal("legacy rows or sequences changed")
	}
}
func TestSchemaSwitchPreservesPopulatedV1(t *testing.T) {
	f := populatedV1MigrationFixture(t)
	before := map[string]string{}
	for _, suffix := range []string{"", "-wal"} {
		encoded, err := os.ReadFile(filepath.Join(f.source, "runtime.db") + suffix)
		if err != nil {
			t.Fatal(err)
		}
		before[suffix] = string(encoded)
	}
	store, err := Open(context.Background(), Options{DatabasePath: filepath.Join(f.source, "runtime.db"), BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if store != nil {
		shutdownTestStore(t, store)
		t.Fatal("direct Open accepted v1")
	}
	if !errors.Is(err, ErrMigrationRequired) {
		t.Fatalf("old open: %v", err)
	}
	for suffix, want := range before {
		encoded, err := os.ReadFile(filepath.Join(f.source, "runtime.db") + suffix)
		if err != nil || string(encoded) != want {
			t.Fatal("direct Open changed old DB/WAL")
		}
	}
	if _, err := os.Lstat(filepath.Join(f.source, "runtime.db-shm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("direct Open created source sidecar")
	}
	result, err := UpgradeRevision1(context.Background(), f.input(t))
	if err != nil || !result.Committed || result.ToRevision != 2 {
		t.Fatalf("upgrade=%+v %v", result, err)
	}
	f.assertLegacyPreserved(t)
	state, err := InspectSchema(context.Background(), filepath.Join(f.source, "runtime.db"), time.Second)
	if err != nil || state.Revision != 2 {
		t.Fatalf("schema=%+v %v", state, err)
	}
}
func TestSchemaSwitchRequiresFreshVerifiedSnapshot(t *testing.T) {
	f := populatedV1MigrationFixture(t)
	db := openSQLiteForTest(t, filepath.Join(f.snapshot, "runtime.db"))
	nativeExec(t, db, `UPDATE runtime_usage_policy SET retention_days=91`)
	db.Close()
	if _, err := VerifyMigrationDatabase(context.Background(), filepath.Join(f.source, "runtime.db"), filepath.Join(f.snapshot, "runtime.db"), f.owner, f.snapshotOwner, time.Second); err == nil {
		t.Fatal("different snapshot certified")
	}
	if _, err := UpgradeRevision1(context.Background(), MigrationInput{}); err == nil {
		t.Fatal("unverified upgrade accepted")
	}
}
func TestSchemaSwitchCommitThenErrorProvesV2(t *testing.T) {
	f := populatedV1MigrationFixture(t)
	runner := schemaMigrationRunner{commit: func(ctx context.Context, c *sql.Conn) error {
		if _, err := c.ExecContext(ctx, "COMMIT"); err != nil {
			return err
		}
		return errors.New("injected commit result")
	}}
	result, err := runner.upgrade(context.Background(), f.input(t))
	if err != nil || !result.Committed {
		t.Fatalf("commit proof=%+v %v", result, err)
	}
	f.assertLegacyPreserved(t)
}

func TestSchemaSwitchDDLFailureAndCancelPreserveV1(t *testing.T) {
	for _, scenario := range []string{"ddl", "cancel", "rollback_commit_error"} {
		t.Run(scenario, func(t *testing.T) {
			f := populatedV1MigrationFixture(t)
			input := f.input(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := schemaMigrationRunner{afterDDL: func(context.Context, *sql.Conn) error {
				if scenario == "cancel" {
					cancel()
				}
				return errors.New("injected DDL failure")
			}}
			if scenario == "rollback_commit_error" {
				runner.afterDDL = nil
				runner.commit = func(ctx context.Context, c *sql.Conn) error {
					_, err := c.ExecContext(ctx, "ROLLBACK")
					if err != nil {
						return err
					}
					return errors.New("injected uncommitted result")
				}
			}
			result, err := runner.upgrade(ctx, input)
			if result.Committed || !errors.Is(err, ErrMigrationNotCommitted) {
				t.Fatalf("rollback=%+v %v", result, err)
			}
			f.assertLegacyPreserved(t)
			state, err := InspectSchema(context.Background(), filepath.Join(f.source, "runtime.db"), time.Second)
			if err != nil || state.Revision != 1 {
				t.Fatal("failure changed old schema")
			}
		})
	}
}
func TestSchemaSwitchReconcileUnavailablePreventsOpen(t *testing.T) {
	f := populatedV1MigrationFixture(t)
	input := f.input(t)
	if err := os.WriteFile(filepath.Join(f.source, SchemaUpgradeReceiptName), []byte("synthetic-pending-before-commit"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := schemaMigrationRunner{commit: func(ctx context.Context, c *sql.Conn) error {
		if _, err := c.ExecContext(ctx, "COMMIT"); err != nil {
			return err
		}
		return os.Rename(filepath.Join(f.snapshot, "runtime.db"), filepath.Join(f.snapshot, "unavailable.db"))
	}}
	result, err := runner.upgrade(context.Background(), input)
	if result.Committed || !errors.Is(err, ErrMigrationOutcomeUnknown) {
		t.Fatalf("unavailable proof=%+v %v", result, err)
	}
	state, err := InspectSchema(context.Background(), filepath.Join(f.source, "runtime.db"), time.Second)
	if err != nil || state.Revision != 2 {
		t.Fatal("unknown outcome blindly restored data")
	}
	f.assertLegacyPreserved(t)
	store, openErr := Open(context.Background(), Options{DatabasePath: filepath.Join(f.source, "runtime.db"), BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if store != nil {
		shutdownTestStore(t, store)
		t.Fatal("unknown result opened Store")
	}
	if !errors.Is(openErr, ErrMigrationRequired) {
		t.Fatal(openErr)
	}
}
func TestSchemaSwitchReceiptRejectsChangesAndReleasedGuards(t *testing.T) {
	for _, scenario := range []string{"snapshot", "source", "released"} {
		t.Run(scenario, func(t *testing.T) {
			f := populatedV1MigrationFixture(t)
			input := f.input(t)
			if scenario == "released" {
				f.snapshotOwner.Release()
			} else {
				dir := f.source
				if scenario == "snapshot" {
					dir = f.snapshot
				}
				db := openSQLiteForTest(t, filepath.Join(dir, "runtime.db"))
				nativeExec(t, db, `UPDATE runtime_usage_policy SET retention_days=91`)
				db.Close()
			}
			if result, err := UpgradeRevision1(context.Background(), input); !errors.Is(err, ErrMigrationOutcomeUnknown) || result.Committed {
				t.Fatalf("changed verified receipt falsely classified intact rollback: %v", err)
			}
		})
	}
}
func TestSchemaSwitchNativeUnknownByteRecovery(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	attempt := nativeAuditFixture(t, "native-recovery")
	if _, err := store.EgressAttemptRepository().Append(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EgressAttemptRepository().Recover(context.Background(), attempt.StartedAt().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	page, err := store.EgressAttemptRepository().List(context.Background(), egressaudit.PageRequest{Limit: 20})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(err)
	}
	loaded := page.Items[0].Attempt
	if loaded.ByteCountsKnown() || loaded.ErrorClass() != "execution_uncertain" || loaded.Outcome() != egressaudit.OutcomeFailed {
		t.Fatal("native recovery claimed observed effects")
	}
	view := egressaudit.ViewOf(page.Items[0])
	if view.ByteCountsKnown || view.NativeOperationID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatal("native audit view lost operation/unknown counts")
	}
}

func TestNativePurposeCatalogUsesValidNativeAttempt(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	attempt := nativeAuditFixture(t, "native-valid-catalog")
	if _, err := store.EgressAttemptRepository().Append(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	page, err := store.EgressAttemptRepository().List(context.Background(), egressaudit.PageRequest{Limit: 1})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(err)
	}
	loaded := page.Items[0].Attempt
	if loaded.Purpose() != egressaudit.PurposeClientToolService || loaded.Decision().RuleID != "native-route" || loaded.Decision().ProxyID != "profile.direct" || loaded.Decision().AccountID != "" || loaded.Parent().ExchangeID != "" || !loaded.ByteCountsKnown() {
		t.Fatal("native route evidence changed")
	}
}

func TestSchemaSwitchOpenBlocksEveryPendingReceiptShape(t *testing.T) {
	for _, shape := range []string{"file", "directory", "symlink"} {
		t.Run(shape, func(t *testing.T) {
			f := populatedV1MigrationFixture(t)
			if _, err := UpgradeRevision1(context.Background(), f.input(t)); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(f.source, "schema-upgrade-receipt.json")
			switch shape {
			case "file":
				if err := os.WriteFile(marker, []byte("unknown pending result"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(marker, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("missing", marker); err != nil {
					t.Fatal(err)
				}
			}
			store, err := Open(context.Background(), Options{DatabasePath: filepath.Join(f.source, "runtime.db"), BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if store != nil {
				shutdownTestStore(t, store)
				t.Fatal("pending receipt opened a Store")
			}
			if !errors.Is(err, ErrMigrationRequired) {
				t.Fatalf("marker denial: %v", err)
			}
		})
	}
}

func TestSchemaSwitchReconcileBudgetExpiryIsUnknown(t *testing.T) {
	f := populatedV1MigrationFixture(t)
	input := f.input(t)
	input.ReconcileTimeout = time.Nanosecond
	if err := os.WriteFile(filepath.Join(f.source, SchemaUpgradeReceiptName), []byte("synthetic-pending-before-commit"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := UpgradeRevision1(context.Background(), input)
	if result.Committed || !errors.Is(err, ErrMigrationOutcomeUnknown) {
		t.Fatalf("expired proof=%+v %v", result, err)
	}
	state, err := InspectSchema(context.Background(), filepath.Join(f.source, "runtime.db"), time.Second)
	if err != nil || state.Revision != 2 {
		t.Fatal("expired proof blindly restored known commit")
	}
	f.assertLegacyPreserved(t)
	store, err := Open(context.Background(), Options{DatabasePath: filepath.Join(f.source, "runtime.db"), BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if store != nil {
		shutdownTestStore(t, store)
		t.Fatal("expired proof activated Store")
	}
	if !errors.Is(err, ErrMigrationRequired) {
		t.Fatal(err)
	}
}

func TestSchemaMigrationDurabilityFailureKeepsReadableV2Pending(t *testing.T) {
	for _, fault := range []string{"connection_close", "pool_close", "database_sync", "directory_sync"} {
		t.Run(fault, func(t *testing.T) {
			f := populatedV1MigrationFixture(t)
			input := f.input(t)
			if err := os.WriteFile(filepath.Join(f.source, SchemaUpgradeReceiptName), []byte("synthetic-pending"), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			runner := schemaMigrationRunner{}
			switch fault {
			case "connection_close":
				runner.closeConn = func(conn *sql.Conn) error {
					calls++
					if err := conn.Close(); err != nil {
						return err
					}
					return errors.New("injected connection close failure")
				}
			case "pool_close":
				runner.closePool = func(pool *sql.DB) error {
					calls++
					if err := pool.Close(); err != nil {
						return err
					}
					return errors.New("injected pool close failure")
				}
			case "database_sync":
				runner.syncSourceFile = func(ctx context.Context, path string) error {
					calls++
					return errors.New("injected source file sync failure")
				}
			case "directory_sync":
				runner.syncSourceDirectory = func(ctx context.Context, path string) error {
					calls++
					return errors.New("injected source directory sync failure")
				}
			}
			result, err := runner.upgrade(context.Background(), input)
			if !errors.Is(err, ErrMigrationOutcomeUnknown) || result.Committed || calls != 1 {
				t.Fatalf("readable v2 promoted despite %s: result=%+v err=%v calls=%d", fault, result, err, calls)
			}
			state, err := InspectSchema(context.Background(), filepath.Join(f.source, "runtime.db"), time.Second)
			if err != nil || state.Revision != 2 {
				t.Fatal("fault fixture did not leave actual readable v2")
			}
			f.assertLegacyPreserved(t)
			if _, err := os.Stat(filepath.Join(f.source, SchemaUpgradeReceiptName)); err != nil {
				t.Fatal("durability uncertainty lost marker")
			}
			store, err := Open(context.Background(), Options{DatabasePath: filepath.Join(f.source, "runtime.db"), BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if store != nil {
				shutdownTestStore(t, store)
				t.Fatal("uncertain durability opened Store")
			}
			if !errors.Is(err, ErrMigrationRequired) {
				t.Fatal(err)
			}
			result, err = ReconcileMigrationDatabase(context.Background(), filepath.Join(f.source, "runtime.db"), filepath.Join(f.snapshot, "runtime.db"), f.owner, f.snapshotOwner, input.Database.LegacyDigest(), 30*time.Second)
			if err != nil || !result.Committed {
				t.Fatalf("fresh durability recertification did not complete: %+v %v", result, err)
			}
		})
	}
}

func retainedV2MigrationWAL(t *testing.T, f migrationFixture, input MigrationInput) {
	t.Helper()
	if _, err := UpgradeRevision1(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "builder.db")
	encoded, err := os.ReadFile(filepath.Join(f.source, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	db := openSQLiteForTest(t, path)
	nativeExec(t, db, `PRAGMA wal_autocheckpoint=0; UPDATE runtime_metadata SET schema_revision=1; UPDATE runtime_metadata SET schema_revision=2`)
	for _, suffix := range []string{"", "-wal"} {
		encoded, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.source, "runtime.db")+suffix, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
}
func TestSchemaMigrationDurabilityRetainedWALRequiresSuccessfulSync(t *testing.T) {
	f := populatedV1MigrationFixture(t)
	input := f.input(t)
	retainedV2MigrationWAL(t, f, input)
	var files []string
	directorySynced := false
	runner := schemaMigrationRunner{syncSourceFile: func(ctx context.Context, path string) error {
		files = append(files, filepath.Base(path))
		if filepath.Ext(path) == ".db" {
			return syncMigrationSourceFile(ctx, path)
		}
		return errors.New("injected retained WAL sync failure")
	}, syncSourceDirectory: func(context.Context, string) error { directorySynced = true; return nil }}
	result, err := runner.reconcile(context.Background(), input.Database, 30*time.Second)
	if result.Committed || !errors.Is(err, ErrMigrationOutcomeUnknown) || directorySynced {
		t.Fatalf("unsynced WAL promoted: %+v %v", result, err)
	}
	if !reflect.DeepEqual(files, []string{"runtime.db", "runtime.db-wal"}) {
		t.Fatalf("wrong source sync paths: %v", files)
	}
	runner.syncSourceFile = func(ctx context.Context, path string) error { return syncMigrationSourceFile(ctx, path) }
	runner.syncSourceDirectory = func(ctx context.Context, path string) error {
		directorySynced = true
		return syncMigrationSourceDirectory(ctx, path)
	}
	result, err = runner.reconcile(context.Background(), input.Database, 30*time.Second)
	if err != nil || !result.Committed || !directorySynced {
		t.Fatalf("retained WAL did not recertify: %+v %v", result, err)
	}
	f.assertLegacyPreserved(t)
}
func TestSchemaMigrationDurabilityJournalGuardAndDeadlineRemainUnknown(t *testing.T) {
	for _, fault := range []string{"rollback_journal", "released_guard", "deadline", "post_barrier_changed_rows", "file_close"} {
		t.Run(fault, func(t *testing.T) {
			f := populatedV1MigrationFixture(t)
			input := f.input(t)
			if _, err := UpgradeRevision1(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			runner := schemaMigrationRunner{}
			timeout := 30 * time.Second
			switch fault {
			case "rollback_journal":
				if err := os.WriteFile(filepath.Join(f.source, "runtime.db-journal"), []byte("unresolved synthetic journal"), 0600); err != nil {
					t.Fatal(err)
				}
			case "released_guard":
				runner.syncSourceFile = func(ctx context.Context, path string) error {
					if err := syncMigrationSourceFile(ctx, path); err != nil {
						return err
					}
					f.owner.Release()
					return nil
				}
			case "deadline":
				timeout = 50 * time.Millisecond
				runner.syncSourceFile = func(ctx context.Context, path string) error { <-ctx.Done(); return ctx.Err() }
			case "post_barrier_changed_rows":
				runner.syncSourceDirectory = func(ctx context.Context, path string) error {
					if err := syncMigrationSourceDirectory(ctx, path); err != nil {
						return err
					}
					file := filepath.Join(f.source, "runtime.db")
					before, err := os.Stat(file)
					if err != nil {
						t.Fatal(err)
					}
					db := openSQLiteForTest(t, file)
					nativeExec(t, db, `UPDATE runtime_usage_policy SET retention_days=91`)
					db.Close()
					// Keep metadata stable so only the independent typed-row proof
					// can catch this injected post-barrier change.
					if err := os.Chtimes(file, before.ModTime(), before.ModTime()); err != nil {
						t.Fatal(err)
					}
					return nil
				}
			case "file_close":
				runner.syncSourceFile = func(ctx context.Context, path string) error {
					if err := syncMigrationSourceFile(ctx, path); err != nil {
						return err
					}
					return errors.New("injected synchronized source file close failure")
				}
			}
			result, err := runner.reconcile(context.Background(), input.Database, timeout)
			if result.Committed || !errors.Is(err, ErrMigrationOutcomeUnknown) {
				t.Fatalf("%s promoted readable v2: %+v %v", fault, result, err)
			}
		})
	}
}
