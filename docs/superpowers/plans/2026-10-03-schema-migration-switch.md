# Runtime Schema-2 Activation Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development or superpowers:executing-plans for this single reviewed task. Controller-adopted task after inactive-DDL independent review. No migration or native acceptance result is claimed.

**Goal:** Safely activate the reviewed native revision-2 schema and matching audit contract through a verified owned snapshot, transactional migration and fail-closed startup.

**Architecture:** Consume the completed snapshot unit and reviewed inactive native DDL. Runtimedata owns full snapshot validation/orchestration; runtimepersistence owns database-only verification/migration. Active schema, version validation, new egress purpose and unknown-byte projection switch in one coherent unit.

**Tech Stack:** Existing Go 1.26.8/go.mod 1.26.0, modernc SQLite, instanceguard and current private snapshot APIs; no new dependency.

**Spec:** Approved safe-upgrade contract; `docs/superpowers/plans/2026-10-03-native-tool-store.md` Task 1 supplies inactive DDL after independent review. Snapshot unit at reviewed `d8a8a77` is a completed prerequisite, not work to repeat. No native repository/executor/UI or live data changes belong to this unit.

## Adopted scope

Execute this one task with TDD and independent review. The inactive DDL/domain at 8209f1b0ff4308123b50290da849d5d8b1c83348 passed independent spec/security/schema review with no findings; that is the exact reviewed prerequisite. Development uses only isolated synthetic data, never the user's running directory/accounts/App/Codex/OS trust. The user's upgrade authorization already covers preservation, verified snapshot and failure-safe activation; no additional generic permission gate is required.

## Existing interfaces and hard dependencies

Draft source inspection used `a5efcb4e56aa622d32faae57a7a4b23eec87b6bb`; execution baseline is the reviewed `8209f1b0ff4308123b50290da849d5d8b1c83348`, including reviewed snapshot implementation from d8a8a77. Existing concrete APIs are:

```go
// runtimedata/schema_snapshot.go, already implemented/reviewed:
func CreateSchemaUpgradeSnapshot(ctx context.Context, source,target string,owner,hostOwner *instanceguard.Guard,createdAt time.Time) error
func ValidateSchemaUpgradeSnapshot(context.Context,string) error
func RestoreSchemaUpgradeSnapshot(context.Context,string,string) error
// runtimepersistence/schema_inspection.go, already implemented/reviewed:
func InspectSchema(context.Context,string,time.Duration) (SchemaState,error)
```

InspectSchema does not create/change source files: immutable reads only when no WAL; retained WAL uses a private byte-verified temporary DB/WAL copy. inspectSupportedSchema compares every user-defined sqlite_schema object to a frozen SQLite-generated reference; currently only actual v1 is supported. ValidateOfflineDatabase may recover **copied/restored** WAL; source preflight must continue through InspectSchema. Keep frozen v1 hash `393b7d67f8ffd1952fc516a150e1a2a68707b67536fbc2e7f5a7f17208424cb1`, OFF JSON/digests, collection defaults, accounts/secret refs/private files and every legacy scalar/history value.

Reviewed native-store Task 1 must supply `schema/v2.sql`, `native_schema.sql`, `migrations/001_to_002.sql` and passing direct-SQL parity/legacy fingerprint/sequence tests. It preserves active schema.sql/revision 1/purpose catalogs. This switch consumes that **reviewed** delta unchanged; any required DDL correction returns to its owner/review before activation. Existing model tool_approvals remain unchanged. No native repository or transport starts here.

## Task 1: Active schema switch and guarded startup migration

**Files:** Create `internal/runtimepersistence/schema_migration.go`, `schema_migration_test.go`, `internal/runtimedata/schema_upgrade.go`, `schema_upgrade_test.go`, `internal/productruntime/schema_upgrade_test.go`. Modify runtimepersistence/{schema.sql,schema_revision.go,schema_inspection.go,offline_validation.go,store.go,connector.go,egress_attempt_repository.go,schema_revision_test.go}; egressaudit/{attempt.go,view.go,builtin_test.go}; offlinehold/purpose.go; productruntime/{runtime.go,builders.go,options.go}; serverhost/host.go; desktopdaemon/{daemon.go,daemon_test.go}; desktopbootstrap/{bootstrap.go,bootstrap_test.go}; Flutter core/bootstrap/desktop_runtime.dart, app/vibermate_app.dart, core/i18n/app_copy.dart and existing bootstrap tests; docs/deployment.md + .zh-CN.md. Flutter paths are under ui/flutter_app/lib/. Only bounded existing bootstrap framing/loading/failure copy changes; no native permission/reader UI or manual primary upgrade command.

**Consumes:** Exact existing snapshot APIs/guards, private schemaSnapshotRunner.certifySnapshotLocked, reviewed inactive native delta, current Store.Open/options, current instanceguard lifetime. **Produces:** These proposed exact interfaces; no full-manifest parser belongs to runtimepersistence.

```go
// runtimepersistence: private fields, database-only preservation receipt.
type VerifiedMigrationDatabase struct { sourcePath,snapshotPath string; legacyDigest [32]byte; sourceOwner,snapshotOwner *instanceguard.Guard }
func VerifyMigrationDatabase(context.Context,string,string,*instanceguard.Guard,*instanceguard.Guard,time.Duration) (VerifiedMigrationDatabase,error)
type MigrationInput struct { Database VerifiedMigrationDatabase; ReconcileTimeout time.Duration }
type MigrationResult struct { FromRevision,ToRevision int64; Committed bool }
func UpgradeRevision1(context.Context,MigrationInput) (MigrationResult,error)
var ErrMigrationRequired,ErrMigrationNotCommitted,ErrMigrationOutcomeUnknown error
// runtimedata: source/NEW snapshot directory, held source guards, UTC clock and work budget.
type SchemaUpgradePhase string // closed: "storage_preparing" or "storage_upgrade"
func UpgradeSchemaOwned(context.Context,string,string,*instanceguard.Guard,*instanceguard.Guard,time.Time,time.Duration,func(SchemaUpgradePhase) error) (runtimepersistence.MigrationResult,error)
var ErrSchemaUpgradeIncomplete error // closed startup classification, no secret/error/path values
// productruntime.Options adds SchemaUpgradeHostGuard *instanceguard.Guard, SchemaUpgradeTimeout time.Duration,
// and SchemaUpgradeProgress func(runtimedata.SchemaUpgradePhase) error. Zero timeout =>15min; nil outside bootstrap.
// storageBuildRequest adds directory, data/host guards, UTC now, upgrade timeout and progress callback.
```

- [ ] **Write RED** `TestSchemaSwitchPreservesPopulatedV1`, `TestSchemaSwitchRequiresFreshVerifiedSnapshot`, `TestSchemaSwitchStartupUsesHeldGuards`, `TestNativePurposeCatalogUsesValidNativeAttempt`. Use a synthetic v1 DB built from frozen schemaV1SQL, not current Open once it becomes v2. Seed all legacy tables using the reviewed Task 1 typed-value fingerprint fixture, including real account/config/history/approval/audit/content/Raw/usage opt-out and nonempty WAL; preserve original canonical JSON, secret references, private secret/CA/config bytes and a deleted high AUTOINCREMENT sequence. Direct active Open(v1) must report ErrMigrationRequired without WAL/header/sidecar writes. Running current source should fail missing switch/migration/purpose interfaces.
- [ ] Run RED: `DEVELOPER_DIR=/Library/Developer/CommandLineTools go test ./internal/runtimepersistence ./internal/runtimedata ./internal/productruntime ./internal/egressaudit -run 'Test(SchemaSwitch|NativePurposeCatalog)' -count=1`. Save exact failure in execution report; compile failures must identify the missing interface, not broken toolchain.
- [ ] **Activate the reviewed schema coherently:** apply_patch active schema.sql to reviewed schema/v2.sql bytes; schemaRevision=2; embed frozen schemaV2SQL and migration001To002SQL; extend inspectSupportedSchema switch case 2 with exact frozen schemaV2SQL. Update schemaRevision/hash test with the reviewed `shasum -a 256 schema/v2.sql` digest; old v1 literal remains untouched. Empty databases bootstrap v2/defaults once; v2 validates without default reseeding; v1 direct Store.Open returns ErrMigrationRequired **before its connector enables WAL**. Foreign/unknown 3+/malformed 2 still reject before any write. Offline validator/ordinary backup/private snapshot accept actual frozen 1/2; a metadata-only version=2 forgery does not establish support. Change prior unknown-2 fixtures to unknown99, while adding separate real-v1 fixtures and genuine-v2 cases; do not delete old tests/hash.
- [ ] **Switch audit with SQL:** add PurposeClientToolService to Purposes, AuthorityForPurpose=Environment, client_data/none payload, ClientOperation/no Exchange parent, Core caller and EgressAuxiliary hold mapping; preserve original-origin/control restrictions. Add NewClientToolAttempt(environment.ClientToolPlan,NewInput) (Attempt,error) with exact frozen native origin/route/profile, no model account; RuleID is actual native route ID. Adopt reviewed audit.byte_counts_known default1, TerminalInput.UnobservedByteCounts defaultfalse, immutable attempt known-count getter and View.byteCountsKnown/nativeOperationId. Append/scan/Complete/Recover round-trip the column; legacy rows retain default1. Native unknown-count recovery uses failure execution_uncertain and known=false, not success/zero-effects assertion. TestSQLiteAcceptsEveryDeclaredEgressPurpose constructs a valid dedicated native fixture instead of rewriting provider parent/account fields into an invalid native row; its unknown-purpose denial stays.
- [ ] **Own the entire sequence:** UpgradeSchemaOwned validates exact live source/runtime-data.lock/server.lock; nil host guard is acquired once, Server passes already-held guard. No Store/writer/admitted operation. Before full InspectSchema, regular source runtime.db>=128 MiB and regular WAL>32 bytes invokes storage_preparing once under both guards. This is a controller-chosen preparation threshold, not a performance measurement or schema-support claim. Valid pending receipt or proved1 invokes storage_upgrade before backup/reconciliation **only if no preparatory phase was emitted**. Absent/small/current2 uses ordinary budget; a prepared large current2 still must validate exact schema before activation. Callback error/cancel aborts. Work budget begins at first preparatory callback; full inspection/snapshot/migration share it. Fresh NEW sibling snapshot mandatory; no old backup reuse/public Backup(source). Lock snapshot twice, privately certifySnapshotLocked, retain locks through DB verification/upgrade. No reentrant public Validate. Snapshot uses0700 sibling MkdirTemp container with nonexistent child target. Every version/preservation/FK proof remains mandatory after the capability-free phase.
- [ ] **Implement DB-only verification:** VerifyMigrationDatabase requires exact source/snapshot DB paths and both live data guards; use source-inspection's private verified copy strategy for pre-write logical fingerprints so retained WAL inspection never mutates source. Compare exact v1 objects, integrity/FKs and every old typed column/row plus sqlite_sequence between source and snapshot (file hashes differ after checkpoint). The receipt stores aggregate legacy digest, no secret bytes or per-secret diagnostics. Recheck source/guard/receipt immediately after BEGIN IMMEDIATE; reject changed source/snapshot. Close all cursors before another query on the one connection; bounded streaming fingerprinting must not load the whole DB into memory.
- [ ] **Execute one transaction:** dedicated upgrade connector avoids journal_mode changes before snapshot/receipt verification. Use one *sql.Conn, synchronous=FULL, BEGIN IMMEDIATE; verify v1, run exact reviewed delta, verify actual v2 sqlite_schema/constraints/FKs/integrity and preservation of all original columns/default semantics/sequence counters, then UPDATE runtime_metadata schema_revision=2 last in that transaction. initialized_at, collection settings and old records stay unchanged. COMMIT once. No repository recovery/listener/transport or bootstrap readiness exists before validated commit. The reviewed delta already handles audit reconstruction/indexes/deleted-high sequence; do not duplicate it in Go or write another DDL interpretation.

```go
// Private per-instance failure seam; never mutable package globals.
type schemaMigrationRunner struct {
 afterDDL func(context.Context,*sql.Conn) error
 commit func(context.Context,*sql.Conn) error
}
func commitSchemaMigration(ctx context.Context,conn *sql.Conn) error {
 _,err:=conn.ExecContext(ctx,"COMMIT"); return err
}
// Default afterDDL nil; production commit=commitSchemaMigration.
// Tests inject ROLLBACK-then-error, COMMIT-then-error, cancellation and read unavailability.
```

- [ ] **Classify commit precisely:** pre-commit failure rolls back with bounded WithoutCancel and independently verifies intact v1 before ErrMigrationNotCommitted. COMMIT error/cancellation is ambiguous: close dedicated conn **and pool**, retain both ownerships, reconcile on new connection with bounded WithoutCancel/ReconcileTimeout (migration default30s, not ordinary repository2s). Valid v2+preserved fingerprints+schema/integrity/FKs => committed success; intact v1 => not committed; unavailable/inconsistent => ErrMigrationOutcomeUnknown/no Store. Never restore blindly. If large verification exceeds budget, retain pending receipt; next synchronous owned startup reconciliation has the same bounded upgrade budget. Even known committed success cannot activate after caller cancellation; retain committed data/receipt and fail startup.
- [ ] **Make unknown discoverable across restart:** runtimedata owns private schema-upgrade-receipt.json with source initialized_at, from/to revisions, verified snapshot absolute path, legacy aggregate digest and status pending/committed. Fresh snapshot success precedes atomic pending write/file+directory sync; then source migration. Committed status only follows proof. Every startup resolves a remaining receipt under source/snapshot guards before normal2 activation; unknown stays pending/no Store. Known rollback removes/syncs marker. Once committed preservation/durability is proved, consume/remove+sync marker **before permitting ordinary runtime writes**; future legitimate history/settings changes must not be compared to the old snapshot as if migration were still pending. Crash before consumption re-proves unchanged migration data; crash after consumption is normal validated2 startup. Receipt-cleanup uncertainty denies current activation; it cannot trigger a blind restore. Validate source identity/roots/path/version/digest, retain snapshots, never export private manifest/secret data. Operational receipt is not native state or another DB.
- [ ] **Integrate actual startup:** ProductRuntime retains dataGuard, supplies directory/guards/clock/budget to buildStorage before Open. Serverhost passes its exact live server.lock guard. For Desktop nil, ProductRuntime.Start itself acquires a temporary source/server.lock **before** owned upgrade and retains it through Open/all core startup or failed-start cleanup, releasing only as Start returns; later existing serverhost.Attach can acquire it without reentrant lock. Thus production passes nonnil held host guard to UpgradeSchemaOwned; direct nil-helper use never activates a Store. Existing attached Server blocks. Source ownership remains through connection/fingerprint/receipt cleanup; failure has no Runtime/listener/descriptor. Prove preservation before ordinary startup recovery changes pending rows. No release/reacquire gap permits a second upgrader.
- [ ] **Bound automatic startup without manual primary step:** first runtime_starting5s, ordinary terminal120s; lifecycle close27s/5s TERM/5s KILL. Add closed ProgressStoragePreparing="storage_preparing" and ProgressStorageUpgrade="storage_upgrade" to existing capability-free schema. Typed owned callback emits at most ONE of these; no upgrade frame follows an already-emitted preparation. Dart accepts one preparatory frame between starting/terminal, extends hard deadline to launch+20min once, otherwise120s. No repeated resets/capabilities before proof/network progress API. Work15min/min(parent deadline) includes expensive inspection when preparation emitted; reconcile30s/cleanup bounded. Timeout/parent-close cannot leave activating background worker/second host. Preparing grants no format/permission/credential authority.
- [ ] Add FailureStorageUpgradeIncomplete="storage_upgrade_incomplete" to closed Go failure validation/classification and Flutter failure mapping. EN “Data upgrade incomplete; original data retained”; zh-CN “数据升级未完成，原数据保留”. Actual API is DesktopRuntime.start (not bootstrap); existing RuntimeConnector has no progress callback. Use the permitted minimal loading-copy path: update existing bootstrap.live to “Preparing local data; backups and upgrades may take a few minutes” / “正在准备本地数据，备份和升级可能需要几分钟”. No connector-signature redesign is needed. A normal2 startup emits no upgrade phase; pending reconciliation may. Never print snapshot/secret/hash values through bootstrap.
- [ ] In DesktopRuntime.start's existing previous-storage fallback, rethrow storage_upgrade_incomplete/all failures after either preparation phase rather than select previous directory/save rolled-back settings/start another daemon. _launch maps timeout/EOF/malformed post-preparation frame to closed incomplete after existing lifecycle close, but preserves an explicit validated terminal failure reason such as storage_incompatible. Add exception preserveDataDirectory flag(defaultfalse) for this internal no-fallback fact; it conveys no authority. Existing move fallback only for proved uncommitted move. Retry waits for child exit/lock release; active generation rejects another updater.
- [ ] **Write fault/restore proof:** named tests `TestSchemaSwitchBackupFailureNoDDL`, `TestSchemaSwitchDDLFailureAndCancelPreserveV1`, `TestSchemaSwitchCommitThenErrorProvesV2`, `TestSchemaSwitchReconcileUnavailablePreventsOpen`, `TestSchemaSwitchPendingReceiptSurvivesCrash`, `TestSchemaSwitchSecondHostCannotEnter`, `TestSchemaSwitchDeadlineCannotActivateBackground`, `TestSchemaSwitchOwnedLargeWALPreparingBeforeInspection`, `TestSchemaSwitchRestoreRetainsAccountSecretReferences`. Fake slow private inspection verifies preparing emitted under real guards before copy,20min hard bound/cancel/no source write/activation; small2 stays ordinary120s. Dart rejects preparing+upgrade double extension, duplicates/unknown phases, capabilities before proof. Reopen every failure/compare actual old rows/sequence/private bytes/WAL. Unknown retains observed source/no activation, no false rollback. Restore old snapshot to NEW sibling and verify local account refs/credentials/config/CA/history; external secrets original/nonportable.

```go
func TestSchemaSwitchCommitThenErrorProvesV2(t *testing.T) {
 fixture:=populatedV1MigrationFixture(t) // frozen v1 SQL + every legacy table + private files
 result,err:=fixture.upgradeWith(t,schemaMigrationRunner{commit:func(ctx context.Context,c *sql.Conn) error {
  if _,err:=c.ExecContext(ctx,"COMMIT"); err!=nil { return err }; return errInjectedCommitResult
 }})
 if err!=nil || !result.Committed || result.ToRevision!=2 { t.Fatal("committed upgrade not reconciled") }
 fixture.assertLegacyPreserved(t); fixture.assertKnownSchema(t,2)
}
```

Define test-only populatedV1MigrationFixture in schema_migration_test.go with actual stopped temp paths/guards/snapshot and reviewed typed-table fixture helper; methods upgradeWith/assertLegacyPreserved/assertKnownSchema call the real runner/InspectSchema/repositories. No mock Store or wrapper can stand in for preservation/activation. Snapshot tests that previously used current Open as “v1” must explicitly build frozen v1, and additionally test current v2 snapshot/restore; existing reviewed snapshot fault/recertification tests stay intact.
- [ ] **GREEN and independent review** against the one final SHA:

```sh
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test ./internal/runtimepersistence ./internal/runtimedata ./internal/productruntime ./internal/serverhost ./internal/desktophost ./internal/desktopdaemon ./internal/desktopbootstrap ./internal/egressaudit ./internal/offlinehold -count=1
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test -race ./internal/runtimedata ./internal/productruntime ./internal/instanceguard ./internal/runtimepersistence -run 'Test(SchemaSwitch|SchemaUpgradeSnapshot|FrozenRevision1)' -count=1
DEVELOPER_DIR=/Library/Developer/CommandLineTools go run ./cmd/repositorycheck
DEVELOPER_DIR=/Library/Developer/CommandLineTools GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags vibermate_native_secrets ./...
```

Create `ui/flutter_app/lib/core/bootstrap/desktop_bootstrap_budget.dart` and `ui/flutter_app/test/desktop_bootstrap_budget_test.dart` for the pure bounded frame/deadline value consumed by _launch; verify with fake elapsed time/streams rather than launching a real user daemon. Pinned Flutter checks from ui/flutter_app:

```sh
dart format --output=none --set-exit-if-changed lib test
flutter analyze --no-pub
flutter test --no-pub test/desktop_bootstrap_budget_test.dart test/desktop_daemon_lifecycle_test.dart test/desktop_storage_test.dart test/app_copy_test.dart --reporter expanded
```

Update deployment EN/zh-CN with automatic bounded upgrade, private snapshot containing secrets, non-destructive restore, failure classification and source/receipt retention. Reviewer checks actual preservation, schema/WAL, full snapshot certification, locks, receipt/reconciliation, single-progress deadline and audit parity. Windows compile is not crash durability. Only then native-store Tasks2/3 proceed; migration success is no MCP acceptance.

## Bounded bootstrap interface and assertions

The pure value has the following exact proposed public shape; application/server networking interfaces are unchanged:

```dart
final class DesktopBootstrapBudget {
  DesktopBootstrapBudget({required Duration Function() elapsed}) : _elapsed=elapsed;
  final Duration Function() _elapsed;
  Duration? _deadline;
  Duration _last=Duration.zero;
  bool _upgrade=false;
  bool get upgradeSeen=>_upgrade;
  Duration _now() {
    final next=_elapsed();
    if(next<_last) throw StateError('Bootstrap clock moved backwards');
    _last=next; return next;
  }
  void acceptRuntimeStarting() {
    if(_deadline!=null) throw const FormatException('Duplicate starting phase');
    _deadline=_now()+const Duration(seconds:120);
  }
  void acceptStoragePreparation() {
    if(_deadline==null || _upgrade) throw const FormatException('Unexpected upgrade phase');
    _upgrade=true; _deadline=const Duration(minutes:20);
  }
  Duration get remaining {
    final deadline=_deadline;
    if(deadline==null) throw const FormatException('Missing starting phase');
    final left=deadline-_now();
    if(left<=Duration.zero) throw TimeoutException('Bootstrap deadline exceeded');
    return left;
  }
}
```

_launch starts Stopwatch before Process.start; frame1 stays5s. acceptRuntimeStarting sets elapsed+120s. The caller validates exact ProgressSchema and either storage_preparing/storage_upgrade, then acceptStoragePreparation changes deadline once to launch+20min, never now+20min. _nextFrame uses budget.remaining; terminal schema/PID checks remain. Capabilities only after verified preparation/migration completion; duplicate starting/either prep/unknown frames fail. No frame resets the clock. upgradeSeen means either preparatory phase in this internal budget, not proof that DDL executed.

Test immediate/delayed starting, either preparation at119s, preparing→upgrade rejection, terminal boundary, elapsed advance during pending frame, malformed capabilities, cancellation/child close. Ordinary small2 expires120s; large-WAL owned preparation/true upgrade hard20min expires incomplete and closes parent lifetime. Server nil callback writes no desktop frame.

The private preflight predicate inspects only Lstat regular-file sizes under exact guards: DB>=128*1024*1024 and WAL>32; links/invalid paths fail. It may emit preparation before supported-version knowledge because it reports bounded owned work, not schema acceptance. Pending receipt reconciliation uses upgrade directly. Full current/old/unknown schema proofs still run; an unsupported large DB may legitimately emit preparation then storage_incompatible. Threshold is a routing ruling; test both boundaries rather than presenting it as throughput evidence. No manual primary workflow or unsupported metadata-based activation substitutes for validation.

## Draft self-review and API gaps

Current APIs deliberately lack migration, database-only receipt and owned full-snapshot **migration** orchestration; all new names/shapes are defined above. Existing Create/Validate/Restore are reused, not replaced. Runtimedata's private certifySnapshotLocked is the required owned recertification seam; public Validate reacquisition would deadlock. Existing InspectSchema temporary-copy cleanup and frozen-v1 comparison remain authoritative. Exact inactive DDL at8209f1b has now passed independent review. Controller read the full draft and inspected actual Go/Dart bootstrap frames, source guard ownership and snapshot interfaces before adopting. No migration or native-client acceptance has yet run; this adopted plan is executable preparation, not a gate result.

Fingerprint compares original v1 columns/rows except permitted metadata revision; additive audit.byte_counts_known=1 for every old row. New native tables empty; old sqlite_sequence unchanged. Snapshot unit reused; receipt consumption precedes normal recovery/history/settings writes. Bootstrap uses actual start/move fallback and one typed owned preparation/upgrade phase, never authority inferred from a phase. No tests executed or large-data/platform acceptance claimed while drafting.

## Controller execution notes

The prior native review passed and its complete report was read. Keep the reviewed inactive SQL unchanged unless a specific defect requires its owner/review loop. Active schema/purpose switch is coherent in this unit. Root-owned future native issuer/catalog proof remain later native-store tasks, not unimplemented prerequisites of the migration transaction. Named old-data fixture helpers are package-private test reuse; production fingerprinting must be streaming/bounded, not a copy of test whole-table materialization. Additional old-table nonempty cases must exercise accounts/config/history/approvals/evidence/usage and sequence semantics, not only empty bootstrap.

Implementation uses parent routing gpt-6.1-sol/high, fresh fork none; independent durability/security review gpt-6-astra/high. One mutable source worker, no subagents. Go/git use env DEVELOPER_DIR=/Library/Developer/CommandLineTools. Source edits via apply_patch. Record real RED/GREEN, final source SHA, file set and every concern; never migrate real data or emit credentials/private-file hashes. Development snapshots and pending markers are synthetic only. Any substantial API/decomposition issue is reported promptly for a bounded controller decision.

