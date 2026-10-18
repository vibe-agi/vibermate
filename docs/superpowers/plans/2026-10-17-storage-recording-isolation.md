# Control and observation storage isolation implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Authorized Agent work uses reliable compact execution state independently of optional history volume, recording availability and inspection work, while existing data and deliberate deletion/backup behavior remain correct.

**Architecture:** `runtime.db` becomes control revision 3; `observations.db` has an independent observation identity/revision 1 and connection/operation owners. Verified owned cutover preserves original revision 1/2 artifacts as lazy archives. Native current facts, ACP registration, usage consent and deletion fences stay in control; ordinary and native inspection history is offered to the bounded recorder from the preceding plan.

**Tech Stack:** Go 1.26.9, SQLite, current instance guards, verified snapshots/receipts, existing domain repositories and UI/API contracts. No external broker or new process.

**Spec:** `docs/superpowers/specs/2026-10-17-recording-isolation-design.md`. Design evidence: `/Users/null/Downloads/vibermate-timeout-fix-20261017-a6aswkq0/storage-cutover-design.md` and `store-separation-audit.md` (all 57 tables classified). Predecessor: `docs/superpowers/plans/2026-10-17-bounded-recording.md`.

## Global Constraints

- Actual forwarded bytes are not droppable. Credentials, user configuration, grants, consent/deletion obligations, one-use native tool claims, and protocol delivery/replay facts remain execution state.
- No live control operation calls an observation writer, awaits an inspection read, or shares its SQLite connection/file writer lock. No control transaction or long read stays open across history work, including read-only ATTACH.
- Recording unavailability, corruption, locks and saturation affect recording status only. Missing/corrupt required control never silently creates fresh authority.
- Use the predecessor's one shared 2,048-item/64-MiB budget and two fixed recording workers. No log-derived execution authority or unbounded ordinary-record retry/outbox.
- Original v1/v2 frozen DDL and existing data remain preserved. One-time verified cutover may scale with the source snapshot; normal later startup must not open/hash/recover/scan legacy or observation history.
- Keep the existing runtime-data and host ownership guards throughout cutover and host lifetime. Unknown/interrupted mutation admits no normal writes until its receipt is reconciled.
- Preserve explicit deletion: durable tombstone before visibility acknowledgment, stale queued work rejected, all readers filter, eventual physical cleanup of current and legacy data. Hiding data alone does not certify erasure.
- Retention shortening is immediate and survives later lengthening. Collection consent and attribution come from trusted control snapshots, never a client label.
- Backup manifests describe every declared role and WAL; public secrets/keychain exclusions remain. Old backup/private snapshot readers remain supported. Never call omitted/unverified history a complete verified backup.
- Develop only in `/Users/null/.codex/worktrees/1c22/vibermate`; preserve running App and actual data. Behavioral TDD, one source implementer and one heavy test at a time. No real provider/network mutation or release until all tasks and final qualification pass.

---

### Task 1: Split storage roles and native execution facts atomically

**Files:**
- Modify: `internal/runtimepersistence/store.go`, `operations.go`, `schema.sql`, `schema_revision.go`, `schema_inspection.go`, `native_schema.sql`, native repository/execution/projection/content/session/retirement files, ordinary observation repository constructors.
- Create: `internal/runtimepersistence/observation_store.go`, `schema/v3.sql`, `schema/observations_v1.sql`, `native_delivery.go`, `native_authority_isolation_test.go`, `storage_role_test.go`.
- Create focused native history DTO/ownership/persistence files in `internal/nativetool`, `internal/productruntime` and `internal/runtimepersistence`; extend the existing general recording worker, never add another worker.
- Modify: `internal/nativetool/repository.go`, native composition in `internal/nativemcp`, pending approvals in `internal/desktopcontrol/native_tool_approvals.go` and their focused tests.
- Update the approval cursor consumers in Flutter `lib/core/api/native_tool_models.dart`, `lib/core/api/control_api.dart`, `lib/features/workbench/workbench_controller.dart`, `lib/preview/preview_control_api.dart` and the focused native approval/API fixtures.
- Use explicit role fixtures in persistence tests. Frozen `schema/v1.sql` and `schema/v2.sql` are immutable.

**Interfaces:**

```go
// runtimepersistence: role-specific owners; no observation open in OpenControl.
type ControlOptions struct {
    DatabasePath, LayoutID string
    BusyTimeout, CommitReconcileTimeout time.Duration
    NativeStreamLifetime time.Duration
    NativeHistory nativetool.HistoryRecorder // optional; nil never blocks control
    RecordingScopes recording.ScopeIssuer // pure current-token lookup
}
type ObservationOptions struct {
    DatabasePath, LayoutID, ArchiveID string
    BusyTimeout, CommitReconcileTimeout time.Duration
    ContentLimits *exchangecontent.SourceLimits
}
func OpenControl(context.Context, ControlOptions) (*ControlStore, error)
func OpenObservations(context.Context, ObservationOptions) (*ObservationStore, error)
```

Each role owns separate writer/read pools and operation gate. ControlStore exposes authority/configuration repositories and required administration facts only. ObservationStore exposes existing history repositories plus `egressaudit.AttemptStore` and native inspection projections. A compile-only transitional Store adapter is permitted while moving callers, but remove it from production composition before this plan completes; it must never make optional failure mandatory.

Control DDL keeps the classified identity/grant/configuration/provider/approval tables and `runtime_usage_policy`. This task adds `runtime_storage_state`, `recording_capture_fences` and the native schemas it consumes. Task2 owns ACP/usage consuming schema changes and current policy-revision caps; Task3 owns the archive-qualified legacy-cap projection; Task4 owns optional provisioning state; Task5 owns catalog/visibility/cleanup and final policy mirrors. Unused later-task tables need not be invented or frozen here. Remove ordinary activities, connection/egress history, Raw/semantic graphs, usage rows and native history/content from control. Observation DDL owns those atomic graphs with no cross-role FK. Native content uses immutable safe operation/capture display coordinates, not a foreign key to live control operations. Schema identity is exact and role-specific at every checkpoint.

Provide the initial reliable archive epoch and Capture-generation rows in this task, including fresh Capture creation, because native Receive immediately consumes those facts. Task5 completes deletion/clear/consent mutation, publication and cleanup semantics; it is not the first producer of the generation row. Keep initial facts in the same small control creation transaction and preserve existing ID/conflict rules. Required control repository reads must omit the old Activity/history joins as soon as they are exposed by ControlStore; optional display projection wiring follows in Task2.

`runtime_storage_state` has a singleton primary key constrained to1, nonempty `layout_id` bounded to128 UTF-8 bytes, and positive integer `archive_epoch` and `visibility_revision`; initialize both to1 using the required ControlOptions LayoutID. `recording_capture_fences` has primary key `(capture_kind, capture_id)`, positive integer `generation`, and `deleted` constrained to0/1. Kinds use existing `managed_run`/`manual_capture` identities. Initial Capture creation inserts generation1 in its control transaction. No foreign-key cascade may remove a tombstone when its live Capture row is deleted.

The predecessor stages direct scope/fence columns in an unreleased monolithic revision-3 candidate to make lossy connection/egress recording deletable. This task moves those observation tables/fields into the independent observation role and pins the current candidate schema. Later consuming tasks may amend unreleased control v3/observation v1 with their matching schema pins; final freeze is required before release, with no intermediate migration or release. Only released v1/v2 sources require migration, through Task3's direct owned cutover. Intermediate candidate test databases are disposable and must never be treated as a supported released layout. Use sequential coherent RED/GREEN commits within Task1; its complete role/native/approval behavior still requires independent task review.

Native operation columns add `last_event_kind`, `last_event_at_unix_ms`, `maximum_event_at_unix_ms`. `native_tool_deliveries` stores one physical once claim's immutable coordinates, frozen egress proof and latest physical outcome. Derive native-purpose constants; arbitrary audit rows never become receipts. Preserve all fields currently compared by `nativeAttemptFacts` and once/approval/session checks, nullable observed byte counts, uncertain commit behavior and logical-versus-physical completion distinction.

```go
// nativetool: existing signatures retained except inspection moved out.
type AuthorityRepository interface {
    LifecycleAuthority // old LifecycleRepository minus Page; Get is current facts only
    ExecutionAuthority // old ExecutionRepository minus PutContent/ReadContent
    ListPendingApprovals(context.Context, PendingApprovalRequest) (PendingApprovalPage, error)
}
type PendingApprovalRequest struct {
    Capture captureidentity.Reference
    AfterCreatedAt time.Time
    AfterID ID
    Limit int
}
type PendingApprovalItem struct { Approval Approval; Operation Operation }
type PendingApprovalPage struct { Items []PendingApprovalItem; NextCreatedAt time.Time; NextID ID }
```

Define the two embedded authority interfaces by copying the existing method sets minus the specified methods; do not rename their execution APIs. Inspection gets separate Page/Get/ReadContent interfaces and source-aware cursors in Task 3.

Pending-approval HTTP pagination changes from observation `afterSequence`/`nextSequence` to `cursor`/`nextCursor`, encoding the control `(createdAtMillis, approvalID)` tuple in a bounded, strict, canonical, versioned opaque cursor bound to the authorized Capture. Reuse the activity-search cursor conventions with a distinct approval domain. Reject legacy `afterSequence` (including zero), duplicate/unknown query fields, malformed or foreign-scope cursors explicitly with 422; never reinterpret or silently ignore an old sequence. Keep the existing bounded page limit and preview/decision ownership checks. The Flutter discovery cursor is a traversal of the current pending set, not an append watermark: after exhaustion, start from the beginning on the next refresh cycle while directly refreshing known pending approvals. Cover equal timestamps and later backdated/lower-ID arrivals so approvals cannot be omitted permanently.

During the Task1-to-Task2 transition, an explicit adapter may expose authority plus separately injected optional legacy content/inspection methods to existing MCP composition. Authority Get always returns current control facts; absence of the optional dependency does not fail construction or open history. The new role/authority fixtures use the narrowed authority directly with no optional store. Task2 removes synchronous content publication and Task4 removes this transitional production composition.

The old tests/consumers that use authoritative Get as a descriptor-bearing inspection read are an explicit Task2/3 transition gate. Do not restore content reads to Authority.Get or add an interim legacy Get facade solely to keep that obsolete interface passing. Task1 report must enumerate each such test/file, separating executed failures from source-only findings; it cannot claim all native/inspection suites pass. Task2 converts content publication/preservation fixtures to the actual observation writer/read boundary, and Task3 supplies final separate inspection Get/Page/ReadContent and converts recorded-reader/router/UI consumers. Every preservation assertion remains required before final qualification; no intermediate release is permitted.

Native history uses a dedicated bounded preparation ticket so a rolled-back or uncertain control commit cannot publish a successful state:

```go
// nativetool: inspection data only; no executable receipt/binding material.
type HistoryObservation struct {
    Operation Operation // safe snapshot; Arguments and Result are nil
    EventKind string
    EventAt, MaximumEventAt, MetadataExpiresAt time.Time
}
type RecordedHistory struct {
    Scope recording.Scope
    Observation HistoryObservation
}
type HistoryDiscardReason uint8 // rolled back | control commit unknown | owner canceled
type PreparedHistory interface {
    Publish() recording.OfferResult
    Discard(HistoryDiscardReason)
}
type HistoryRecorder interface {
    PrepareNativeHistory(recording.Stamp, HistoryObservation) (PreparedHistory, recording.OfferResult)
}
type HistoryWriter interface {
    PutNativeHistory(context.Context, RecordedHistory) error
}
```

Preparation is non-waiting, reserves from the same global budget before copying every string/pointer, and ends the producer borrow before returning. A ticket owns at most 16 KiB including its envelope/reservation overhead. Use a fixed maximum of 32 tickets per outer control transaction (at most 512 KiB, within the shared budget); excess optional projections drop. No unbounded pending slice, history-only requery or pre-commit queue publication. The fixed holder bookkeeping must be accounted with the prepared allocations. Stage the final post-transition snapshot only after all fields of that revision (including bytes/approval/binding) are known from already-required facts. Publish transfers ownership to the existing general worker only after the outer commit succeeds; rollback/commit-then-error discards. Publish/Discard are mutually exclusive and idempotent, including races. Do not count an unknown control commit as an attempted/unconfirmed observation-store write. External control transaction owners that call native revocation must finalize their own tickets after commit or explicitly disable those optional projections; helpers must never publish before their caller commits. Recovery/deletion/close may run with recording disabled.

Persist original `recording_archive_epoch` and `recording_capture_generation` on each control native operation at Receive, using the durable small control facts projected through its already-required Capture/scope read. Carry the pair as `Operation.RecordingScope recording.Scope` through the existing current-operation SELECT. It is a recording/deletion coordinate, never send authority. Native preparation gets the pure Runtime current stamp and proceeds only when `stamp.Current()` and `stamp.Scope() == operation.RecordingScope`; unavailable/mismatched recording coordinates drop history without changing valid native execution. Never assign a fresh epoch to old work after clear, and never add a historical operation-ID token registry. Task5 completes persisted generation publication and token invalidation; Task3 cutover defines the initial legacy-source fence mapping.

The observation writer atomically inserts the immutable operation/revision event snapshot and advances the latest display snapshot only for a higher operation revision. Exact duplicates are idempotent, conflicting same-revision snapshots/scopes fail recording without overwrite, and older revisions never regress latest state. Each snapshot is self-contained after dropped predecessors or control retirement; no FK/read to live operations, no inference of missing transitions. Preserve independently unknown byte counts and original metadata expiry. Shape/recording scope validation belongs to this optional boundary, not a new native permission check. Full dependency/outer-commit-owner evidence is in `/Users/null/Downloads/vibermate-timeout-fix-20261017-a6aswkq0/native-role-task1-preflight.md`.

Provide a native inspection index with a source-local publication position, updated atomically by history publication and (in Task2) content publication. It retains safe immutable identity while any visible history or content survives, without requiring a live control operation. This permits discovery of content-only records when every lifecycle snapshot was dropped; the index is not a fabricated operation event or authority proof. Content publication may advance inspection discovery but must not invent or downgrade logical operation revision/state. Retention/deletion removes an index entry only when no visible optional projection remains.

- [x] **Step 1: Add role-lock and native safety regressions before source changes.** Hold an actual observation DB writer transaction, then create/attach/renew/authorize a Capture through the control DB before releasing the lock. With no observation event/egress/content rows, execute a native Start/Verify/Finish through the existing real local MCP fixture and assert exactly one send across repeat/replay. Corrupt a required once/approval/delivery proof and assert no send. Pending approvals must appear with zero history events. Native retirement must not retain control rows solely because full recording retention is long.
- [x] **Step 2: Record RED under the monolithic store and mandatory native-history loader.** Test completion-before-release is the isolation assertion; timeouts are guards. Do not use a same-file second pool as the implementation.
- [x] **Step 3: Implement role constructors and exact schemas.** Move repository ownership to the correct role; keep Raw and semantic graph publication/GC atomic within observations. Refactor shared helper dependencies so observation writes do not borrow a control transaction. New role tests initialize fresh synthetic data with matching layout identities. Existing offline v1/v2 verification remains available for migration/archive readers.
- [x] **Step 4: Convert native current state transactions.** Receive/nativeTransition update revision/state/event tuple and maximum event time together. Start atomically consumes approval, claims once, creates delivery and transitions before issuing its process-local receipt. Verify/Finish/recovery/cancellation use only control tuples. Post-commit event/history projection uses the typed recorder and cannot change the committed operation result. Get never loads optional descriptors. Keep inbound/once identities until protocol-safe scope/session retirement permits removal; body-free recent terminal facts use the existing 30-day metadata interval, independent of content retention.

Add behavioral RED/GREEN coverage for no history publication before commit, rollback and commit-then-error ticket disposal, oversized/bulk transaction staging, duplicate Publish/Discard races, terminal-only independent inspection, out-of-order/idempotent/conflicting snapshots, and original-scope rejection across clear. Control must complete while history remains blocked; all reservations remain owned until actual recording I/O returns.
- [x] **Step 5: Switch pending-approval discovery to its indexed control query.** Preserve exact approval preview ownership and the control-tuple cursor across HTTP and Flutter; no event Page scan. Test empty/exhausted traversal, equal timestamps, newly pending earlier tuples, foreign/malformed/legacy cursors, and decision/preview refresh. Run targeted native lifecycle/once/receipt/fault/recovery/retirement tests, role isolation normal/race tests and affected Flutter approval/API tests. Commit `refactor: separate control storage from native observation history`.

### Task 2: Separate Capture display facts, ACP snapshots and usage collection

**Files:**
- Modify: `internal/runtimepersistence/capture_run_repository.go`, `capture_control_authority.go`, `manual_capture_repository.go`, `capture_activity_test.go`, `acp_observation.go`, `runtime_usage.go`, `usage_query.go`.
- Modify: `internal/acpobservation/manager.go`, `observation.go`, `internal/capturecontrol/acp.go`, `internal/runlauncher/acp_unix.go`, native content recording in `internal/nativemcp`.
- Extend Activity's existing typed recording bundle and observation schema with frozen Capture search/display coordinates from admitted facts: managed workspace ID/label and executable label; manual display name. Update the relevant `activity`/Exchange Runtime adapter fields as needed, without a per-event control lookup or retained Admission/receipt.
- Create: `internal/runtimepersistence/acp_registration.go`, `capture_recording_isolation_test.go`; focused ACP/native content regressions at existing harnesses.

**Interfaces:**

```go
// acpobservation: control-only registration.
type Registration struct {
    RunID string
    Policy environment.ContentRecordingPolicy
    ExpiresAtMillis int64
    Fence recording.Fence
}
type RegistrationRepository interface {
    Register(context.Context, Registration, int64) (Registration, error)
    Registration(context.Context, string, int64) (Registration, error)
    Markers(context.Context, []string) (map[string]bool, error)
}
```

Capture authentication display has its own small typed arm on the same general recorder/worker and global budget. Do not infer the exact admission fact solely from Exchange Activity: authenticated CONNECT/UDP/denied-route traffic may produce no Exchange, and Manual credential rotation must reset which credential revision is displayed.

```go
// recording: optional authentication-display fact; Stamp supplies original scope.
type CaptureObservation struct {
    ObservedAt time.Time
    CredentialRevision uint64 // zero for managed; authorized revision for Manual
}
type CaptureObserver interface {
    ObserveCapture(Stamp, CaptureObservation) OfferResult
}
```

Emit only after successful required proxy authorization from already-loaded facts. A failed/dropped offer never changes authorization. Persist observation-only first/last times keyed by original scope and Manual credential revision, with no control FK/read. Do not add a per-Capture queue, worker or registry. Preserve genuine expiry/revocation/rotation and report absent observations as unknown/no retained observation. Required Capture catalog, Authorize/Attach/Heartbeat and startup do not await display enrichment; old control display columns may remain compatibility placeholders but receive no live observation writes. Activity's separate search/display fields remain frozen per Activity record, avoiding mutable per-Capture rename semantics.

ACP Start returns the committed marker and known initial empty snapshot; it never reads observation storage to finish startup. Snapshot publication remains authenticated and shape-bounded, then accepted/dropped by the typed recorder. Actual rejection and temporary authority unavailability remain different typed errors/statuses.

Capture display must freeze its recording epoch/generation in the existing required authorization SELECT/transaction, then match the pure current stamp to that original pair before offer. Do not look up a fresh stamp after the authority read and relabel an old admission across clear/recreation. A scope mismatch drops the optional fact without changing otherwise valid authorization; add the interleaving regression, including Manual credential revision. No recording-only control query or token registry.

Usage policy remains reliable control state. Split current runtimeusage Repository into PolicyRepository (`UsagePolicy`, `SetUsagePolicy`), Reader (`ScanUsage`) and the already-defined typed recording producer. An offered usage snapshot carries original `Observation`, trusted frozen attribution, collection-policy revision, original expiry and Stamp. Collection policy is published in memory only after reliable CAS; no new control lookup per recorded event. Observation writer never reconstructs authority by joining Captures/accounts.

Current Activity search currently joins Capture workspace/labels before filtering and pagination. Preserve those predicates by freezing the safe display fields with the Activity record (keyed by Capture kind plus ID), not by querying control after a page is selected. Source display fallback, model/tool manifest fields and current terminal precedence remain. These are optional display/search copies, never authority; legacy reads can use the corresponding frozen tables inside the archived source.

This task creates control `runtime_usage_retention_rules(retention_days PRIMARY KEY constrained1..365, through_policy_revision positive)`. A shortening CAS caps through the old successful current policy revision; prune only dominated rules and never remove a still-relevant shorter cap when later lengthening. Observation usage rows retain original policy revision, expiry and scope. Define the split PolicyRepository/Reader ports here; final multi-source reader composition and versioned policy mirrors belong to Tasks3/5. Do not expose unchanged ObservationStore.ScanUsage that queries missing control tables or claim full usage report qualification. Any necessary interim composition obtains a bounded copied policy/cap snapshot outside observation I/O, with no control ATTACH or held permit; final revision recheck/visibility remains mandatory in Task5. Role writer/policy tests may verify the real stored values directly while those reader gates remain explicit.

Consume the predecessor's exact `runtimeusage.CollectedObservation` and `CollectedWriter.PutCollectedUsage(context.Context, CollectedObservation) error` interfaces; do not introduce a second usage snapshot DTO. Implement that write solely in the observation role and remove any predecessor worker-side legacy bridge.

- [x] **Step 1: Add blocked/failing snapshot regressions.** Valid Capture Run and Manual Capture AuthorizeProxy succeed even if first/last-observed recording fails. Temporary Manual Capture expiry and actual revocation still refuse authorization. Real ACP child starts and preserves its successful exit while optional Create/Read/Save storage is unavailable. True revoked capability still stops it; temporary authority-store failure is unavailable, never 403. Native JSON/SSE result bytes arrive before a blocked PutContent is released; arguments/result copies cannot hold execution leases.
- [x] **Step 2: Record RED, then remove optional writes from authority checks.** Capture first-observed and Manual Capture last-observed display data become observations after successful authority admission; capture activity/history queries move to inspection. Manual authorization currently uses the observed-field UPDATE's affected-row count as its admission result: replace that coupling with a reliable active/lifetime/expiry predicate, retaining actual expiry/revocation control changes. ACP registration rechecks the active run and created state in its control transaction; snapshots and final-save failures do not determine child exit. Do not weaken genuine process attachment, capability or native start receipt checks. Missing observations must display as no retained observation/unknown evidence, not proof that no traffic occurred.
- [x] **Step 3: Freeze usage consent/attribution and extend the typed worker.** Disabling collection may conservatively drop queued old-policy observations; re-enabling does not revive callbacks older than CollectingSince. Keep original retention/expiry. Native/ACP arms are finite owned payloads with the same shared budget, not new workers or queues. Missing optional display names remain missing.

```go
// nativetool: observation only, never authorization for execution.
type ContentPart string // tool_arguments | tool_result
type ContentIdentity struct {
    OperationID ID
    Scope recording.Scope // original operation coordinates, not a fresh current epoch
    EnvironmentID environment.EnvironmentID
    EnvironmentRevision environment.Revision
    EnvironmentDigest environment.CandidateDigest
    Kind RequestKind // tools_call
    ToolName, TargetOrigin, CredentialOwner string
    Recording environment.ContentRecordingPolicy
    CreatedAt, ExpiresAt time.Time // exact original operation creation + frozen retention
}
type ContentObservation struct {
    Identity ContentIdentity
    ObservedAt time.Time
    Part ContentPart
    Body []byte // sanitized owned bytes; synchronous worker borrow only
}
type ContentWriter interface {
    PutNativeContent(context.Context, ContentObservation) error
}

// nativemcp: closed synchronous producer borrow; only this package constructs it.
type ContentInput struct {
    identity nativetool.ContentIdentity
    observedAt time.Time
    part nativetool.ContentPart
    raw []byte
    profile ContentProfile
    limits Limits
}
type ContentRecorder interface {
    ObserveNativeContent(recording.Stamp, ContentInput) recording.OfferResult
}
type OwnedContent struct { /* private sanitized observation and measured cost */ }
func OwnContentWithin(recording.Stamp, ContentInput, *recording.Reservation) (*OwnedContent, error)
func (*OwnedContent) Observation() nativetool.ContentObservation
func (*OwnedContent) RetainedBytes() uint64

// acpobservation: snapshot copy only, no control marker authority.
type Recorder interface {
    ObserveACP(recording.Stamp, Record) recording.OfferResult
}
```

The successful executor path currently receives only `ReceiveResult{OperationID, Duplicate}`; its existing Get calls are cleanup-only. Extend ReceiveResult with `RecordingScope recording.Scope` and `CreatedAt time.Time`, populated for a fresh Receive from the already-required original scope/input and returned only after its known successful commit. Canonicalize CreatedAt to the exact stored millisecond instant. No new control/history lookup is permitted merely to recover these recording facts. Duplicate inbound results may leave these fields zero when the existing required path has no current operation row; suppress optional content in that case, including retired replay identities, without changing duplicate/replay behavior. Never substitute current epoch/time. Other ContentIdentity fields come from the original frozen admitted plan/binding. These additional result fields are observation coordinates and must not enter Start/Verify receipt or authorization equality. Inject the same pure recording.ScopeIssuer into native content composition when needed; no operation-ID registry.

Native producer order is closed borrowed input → shared reservation → pure synchronous OwnContentWithin → owned sanitized queue arm. The Body-only writer DTO is not a sufficient producer seam: retainedContent/retainedToolResult currently parse/maps/marshal before storage. Availability/stale/capacity checks must skip that optional preparation; reserve before every retained copy and simultaneous parsing/path/marshal/worker workspace allocation. Reuse native jsonAllocation only as a building block and prove the complete peak-owned bound, not an unexplained multiplier. No raw input, profile, session, catalog, callback or execution lease survives in OwnedContent. Caller retains the reservation on every helper return and transfers it only on accepted queue publication.

The original trusted frozen profile also supplies required sensitive-call binding and pending approval preview classification. Preserve those execution/decision semantics when recording is unavailable or saturated; only additional recording-only work belongs behind recording admission. Reuse the existing frozen profile for the synchronous input borrow, and never queue it. Host ACP validation remains bounded/authenticated, but optional snapshot deep-copy/encoding must reserve first and detach all strings, slices and ExitCode. Launcher-side publication is a separate process with its own existing bounds; do not claim it shares the Runtime budget.

Add behavioral RED/GREEN coverage proving unavailable/saturated/stale/closed recording skips the optional sanitizer, every refused preparation growth releases its reservation, accepted bodies/identity have no producer aliases, and required binding/approval preview stays unchanged. The full seam/accounting/redaction matrix is in `/Users/null/Downloads/vibermate-timeout-fix-20261017-a6aswkq0/native-content-preparation-seam.md`; it reconciles this task's producer contract without adding a worker or generic callback system.

Native content must survive dropped history snapshots and retired control metadata without a control lookup. Freeze the identity/expiry from the already-required admitted operation facts, require its original Scope to match the supplied Stamp, and never extend expiry at enqueue/write/retry time. Persist each part's immutable envelope and body graph atomically without a live operation/history FK. Exact duplicates succeed; conflicting body/identity/expiry fails recording only. Cover absent history/control, exact original expiry, duplicate retries and stale content after clear. Task3 inspection may expose retained content with explicitly unknown lifecycle when no operation snapshot survived; it must not fabricate terminal execution facts from a body. Negative body-suppression/privacy assertions must inspect the actual optional body store or owned observation, because a nil descriptor from body-free Authority.Get is now unconditional and proves nothing about retained sensitive bytes. Consume Task1’s exact positive and negative inspection-test inventory. This task must supply the actual retained-body counterpart to the obsolete `TestNativeLongFullRetentionSurvivesMetadataExpiry`: Task1 replaces its control-row retention requirement with real-role metadata/physical-delivery/once safety tests, and Task2 proves retained content remains independently readable with exact original identity/expiry after control retirement.
- [x] **Step 4: Run affected Capture/ACP/native-content/usage tests, focused race ownership checks and launcher regressions; commit.** Commit `fix: keep capture and tool results independent of saved logs`.

### Task 3: Verified owned role cutover and lazy legacy inspection

**Files:**
- Create: `internal/runtimedata/storage_layout.go`, `storage_cutover.go`, `storage_cutover_test.go`; `internal/runtimepersistence/control_extract.go`, `control_extract_test.go`, `legacy_observation.go`, `legacy_observation_test.go`.
- Modify: existing schema inspection/migration/snapshot helpers only where reuse is needed; independent storage preparation and history reader composition/cursor codecs. Task4 owns the production `buildStorage`/Runtime owner switch as one coherent change.
- Split immutable Raw source reads/salt from the current reveal-audit sink; update semantic nested cursors, native inspection DTO/API and Flutter native operation cursor/body-cache consumers together. Pending approvals remain on Task1's control-only interface.
- Preserve the existing desktop progress adapter in `internal/desktopdaemon/daemon.go` and test it with `internal/desktopbootstrap` / Flutter `desktop_bootstrap_budget_test.dart` if that adapter changes.
- Preserve frozen v1/v2 files and old receipt handling. Add exact v3 schema digest/revision checks.

**Interfaces:**

```go
// runtimedata
type OwnedStorage struct { Directory string; Runtime, Host *instanceguard.Guard }
type DatabaseRole struct { RelativePath, Identity string; Revision int64; Required bool }
type LegacyArchive struct { ID, RelativeDirectory string; Database runtimepersistence.SchemaState }
type StorageLayout struct { ID string; Control, Observation DatabaseRole; Archives []LegacyArchive }
func PrepareStorageOwned(context.Context, OwnedStorage, time.Time, func(StoragePhase) error) (StorageLayout, error)
```

Persist `storage-layout.json` (`vibermate-storage-layout/v1`) with matching control `layout_id`. Root `runtime.db` is control revision 3; `observations.db` is optional role revision 1. Originals move to `archives/<archive-id>/runtime.db` plus retained WAL and `archive-manifest.json`. Paths are bounded, clean, relative, regular and under the owned root; symlinks/conflicts fail closed. New cutover receipt uses existing `schema-upgrade-receipt.json` filename with schema `runtime-role-cutover/v1` and phases preparing/ready/archived/activated, making old binaries refuse incomplete cutover. Revision 3 guards completed downgrade. For automatic revision-0 initialization, require a scaffold-only root under both guards before minting a receipt: actual held lock files plus optional exact empty private server-secrets only. Residual identity/CA/admin/secrets/config/UI/unknown state refuses without mutation. Fully erased/lock-only prior roots are not forensically distinguishable; this is an explicit clean-root policy, and known caller must-exist expectations remain. Task4 must admit required storage before consuming a pending root reset, retaining generation/root ownership and preceding CA open.

Inspection adapters remain domain-specific and share only a lazy source catalog/lease and visibility guard. There is one writable current observation source; archives are immutable outside explicit generation-changing maintenance. A constant-size, domain/query-bound opaque cursor uses the source-set identity/generation plus deterministic `(source publication ordinal, local sequence/boundary)`; no vector of every source position is needed. Current source has the last ordinal. Advance over consumed rows even when filtering/dedup emits none, and preserve the current tail on empty native polling. Source-set/maintenance changes invalidate continuation; ordinary current inserts do not. Apply global winner selection before cursor filtering so a later current duplicate cannot expose an obsolete archived projection. Source publication order is traversal order, not an event-time claim. All nested semantic/content cursors must also retain source/generation and their existing immutable root/digest bindings.

Cross-source summaries, usage and search must deduplicate by domain identity before final filtering/grouping/ranking/limits. Existing per-source `ScanUsage` buckets and summary totals have already lost identities and cannot simply be added. Use narrow domain SQL source projections on a pinned inspection-owned connection attaching only read-only observation/archive sources, never control; select the canonical global winner and apply visibility/policy mirrors before producing bounded final results. Preserve authenticated Usage UserID filtering and exact original retention caps. Usage canonical display selection must retain every duplicate copy's effective retention ceiling: use the earliest stored/applicable-cap expiry, with source-qualified sequence ranges and policy revisions. Legacy expiry is mutable under published retention materialization, so differing stored expiry alone is not an immutable conflict; true observation/owner/scope/time conflicts refuse. Task5 must preserve the ceiling or remove/reject surviving and late duplicates before erasing its last cap-bearing source row. Use the actual pinned driver's per-connection limits (including `sqlite.Limit` for attachments), cancellation/deadlines and bounded inspection concurrency. If source inventory/query resources exceed supported limits, fail that inspection explicitly; never omit sources or claim partial totals are complete. No new scratch-database fallback is required without a demonstrated supported-layout need, and VM program-size/cache settings do not establish a CPU or temp-disk quota.

Task3 is the first consumer of global visibility SQL and therefore owns the minimal observation-local mirror read DDL and concrete short control snapshot/recheck ports. Task5 owns production bounded export, complete atomic publication and mutation/version coupling. The concrete contract is `/Users/null/Downloads/vibermate-timeout-fix-20261017-a6aswkq0/visibility-mirror-read-contract.md`: fixed control stamp includes layout/epoch/visibility/policy revisions; scoped copies and future export pages are bounded to256 references/rows; global queries pin a matching complete SQL mirror; source-set generations are independently leased/rechecked. No unbounded Capture map, control ATTACH or permissive missing-mirror fallback. Task3 chooses and reports the exact private relation/API names and updated pins for Task5. Test-only complete mirror fixture seeding is not production publisher qualification.

Current Activity, semantic/identity scope roots and Raw envelopes also need persisted original epoch/Capture generation for these readers. Task3 owns their minimal real writer plumbing and current-only columns, using the already-admitted stamp from the existing queue/live capture ownership path; Task5 cannot reconstruct those facts later. Do not substitute a fresh control/current stamp, guess generation1, or stop at trusted test-only seeds. Missing original coordinates drop the optional observation; any unknown sentinel is invisible to the final reader. Tag actual scoped roots/associations, never shared body/chunk dictionaries, and retain bounded owned buffers, graph atomicity and shared GC. Prove the actual recorder/store path plus an admission-to-write epoch/generation change before claiming these readers qualified. Task5 still owns mutation/publication/cleanup. Task3 also supplies a lazy scoped current/legacy Capture-observation time reader and a distinct launch-only supplement on existing optional Activity summary; request-count/time extrema are not overloaded. Nullable legacy facts and trusted manual credential revision remain explicit. Task4 wires/presents it in optional Capture overview only, never on control catalog/lifecycle/approval paths. A current scoped Exchange-to-conversation-identity association is independently admitted from its valid original scope even if Activity and semantic projections were lost; it is its own scoped observation root. Preserve same-Exchange scope conflict rejection and the old unscoped combined-store existence/no-resurrection/error contract, while rejecting all unscoped ObservationStore writes. Actual recorder/repository tests must cover Activity failure with no semantic root; readers and Task5 cleanup filter/discover the identity-only association by its own coordinates.

Raw Reveal/RevealHeader remain audit-before-disclosure inspection actions. Separate immutable source reads and existing-source salt access from `AppendRevealAudit` on the current observation audit sink. Carry trusted source identity/generation plus Capture scope and original envelope/exchange/actor/outcome/time; no disclosed payload in the audit DTO. New source-qualified audit storage cannot use a foreign key to a nonexistent local envelope and must participate in scoped cleanup; never copy a legacy body just to satisfy the old FK. An unavailable audit sink fails only that reveal. Legacy sources remain read-only and are never passed to a manager that creates salt/session/recovery state. Header fingerprint verification uses the selected source's original salt. Release source transactions before credential/audit I/O and recheck visibility/source generation after the audit, before returning any disclosure.

Native inspection is distinct from authority: archived event references only prove the latest operation snapshot actually retained in that archive, not invented per-revision historical states. New terminal-only snapshots and content-only identity entries remain inspectable after control retirement; content-only lifecycle is explicitly unknown. Legacy native content has no persisted observation timestamp, so its separate inspection projection exposes unknown time; never fabricate it, copy old bodies into the current table, or run the projection through live `PutNativeContent` validation. Current-only content keeps its required recorded timestamp. Use opaque `cursor`/`nextCursor` in the native operations HTTP/Flutter API, reject legacy sequence-only continuation explicitly, preserve the inspection index's publication position, and invalidate stale body/page caches on generation/visibility changes. Never route pending approvals through this optional reader. Concrete reader/consumer evidence is in `/Users/null/Downloads/vibermate-timeout-fix-20261017-a6aswkq0/legacy-reader-task3-preflight.md`.

`StoragePhase` reuses the existing desktop wire vocabulary (`storage_preparing`, then `storage_upgrade`) through a type alias or explicit adapter; receipt checkpoints are internal and must not be sent as invented bootstrap phases. Emit preparation before potentially large inspection, and upgrade before the verified history copy so the existing 5-second first-record / 120-second preparation / 15-minute upgrade budgets behave correctly. Preserve a caller's explicit shorter `SchemaUpgradeTimeout` and cancellation; default owned upgrade work is bounded to 15 minutes. Add a controlled slow-copy startup regression proving it uses the upgrade budget without repeatedly extending its deadline.

Keep the planned function signature: the caller validates/encodes an explicit shorter timeout in its context; preparation derives one 15-minute child deadline, taking the earlier bound. Do not reuse the old progress helper's single `emitted` flag, which suppresses `storage_upgrade` after `storage_preparing`. The new preparation API emits progress before expensive work; Task4 replaces the candidate builder's production pre-inspection/rejection path when it switches Runtime owners.

Production owner integration is Task4. The current `storageBuildResult.store` and Runtime constructor use the same combined Store for both domains; a control-only result cannot be installed without changing required and optional consumers together. Task3 delivers and tests the real owned preparation API, control extraction, lazy readers and final inspection consumer interfaces without wiring `PrepareStorageOwned` into that combined production opener. Do not add a public combined-Store bridge or temporary legacy inspection facade. Test second-host exclusion, cancellation/no background activation, both progress phases, old-receipt→cutover→OpenControl and normal-start history independence directly through the real new owned boundary. The existing productruntime `TestSchemaSwitchSecondHostCannotEnter` and `TestSchemaSwitchDeadlineCannotActivateBackground` move explicitly to Task4's real Runtime integration gate; they remain unwaived before Task4 approval/release. Native inspection HTTP/router/UI consumers may adopt their final reader ports here with actual independently injected readers in fixtures; automatic production owner installation follows in Task4.

Use a new narrow role-extraction certificate, not the old `VerifiedMigrationDatabase`/whole-directory snapshot orchestration. Those old APIs hash/sort every optional table and intentionally reject populated normal v2 additions; they remain unchanged for strict reconciliation of an already pending old receipt. New extraction uses explicit released-v1/v2 source adapters: directly project v1's control fields plus reviewed control defaults into final v3, without normalizing the entire monolith. Preserve original DB/WAL bytes, exact source schema, complete typed required projections and independent target counts/digests. Optional Raw/semantic/ACP/usage payloads are preserved as archive bytes, not certified as readable or admitted as authority.

On the private pinned source connection only, the pinned SQLite supports `ignore_check_constraints=ON` for a full structural `main.integrity_check`: it skips CHECK-expression evaluation while preserving btree/freelist/page-reference, index, type and NOT NULL checks. Require the exact sole `ok` result, restore OFF with readback (or close on failure), then validate all required projected row semantics and required FK closure explicitly; final control gets full ordinary integrity/FK/semantic checks with no exemption. This permits supported optional CHECK/FK defects without filtering diagnostic strings. Any structural/index/type/NOT NULL/expression-evaluation failure or malformed required authority still refuses extraction. Generated/indexed expression damage is not an arbitrary salvage promise. Add synthetic fixtures distinguishing these boundaries; the original/archive connection is never opened with mutating validation.

The actual Task1 role schema uses `vibermate-control`/revision3, distinct from the released monolith’s schema discriminator. Transform runtime_metadata’s schema discriminator/revision to that exact target role while preserving initialized_at and every actual durable installation/user/account/grant identity. The older preflight phrase “transform only revision” is superseded by the implemented role identity; schema_identity is a format discriminator, not an installation identity. Do not manufacture fresh accounts, capability hashes, secret references or initialization time.

Capture rows are mixed projections. The optional source columns are exactly `capture_runs.observation/first_observed_at_unix_ms` and `manual_captures.observation/last_observed_at_unix_ms`. Project every target row to `observation='waiting_for_traffic'` and a NULL observed timestamp; this compatibility placeholder means unknown/no retained evidence, never proof of no traffic. Preserve all original display bytes in the archive. Required projection digests include the fixed target constants, not source optional values. Do not populate optional aggregates or fabricate authentication events during extraction. Retain all original source checks except the managed observation enum and the manual observation enum, observation/last-observed NULL pairing, and last-observed bounds against created/updated. Independently enforce required timestamp/lifetime/owner/state/credential/configuration checks and FK closure. No restored-CHECK whole-table integrity pass on either mixed Capture table, and no untransformed `scanManualCapture.Validate()` that reinstates display checks. Use explicit fixed required-column validation after the private structural pass. Type/NOT NULL/UNIQUE/index/structural failures still refuse; this exception does not extend to labels, git/configuration, recognition, or identity fields.

Preserve old usage consent obligations in this small control table, added by Task3 with its exact schema pin:

```sql
CREATE TABLE runtime_usage_legacy_retention_caps(
  archive_id TEXT NOT NULL CHECK(length(CAST(archive_id AS BLOB)) BETWEEN 1 AND 128),
  retention_days INTEGER NOT NULL CHECK(retention_days BETWEEN 1 AND 365),
  after_sequence INTEGER NOT NULL CHECK(after_sequence>=0),
  through_sequence INTEGER NOT NULL CHECK(through_sequence>0 AND after_sequence<through_sequence),
  PRIMARY KEY(archive_id,retention_days)
) STRICT;
```

Use the receipt/layout's canonical archive ID, never a path or current-source identity. Copy every source `(retention_days,after_sequence,through_sequence)` exactly and independently compare typed counts/digests; damage to these required facts refuses extraction. Never infer caps from optional usage rows/MAX/expiry, reset materialization progress, or rewrite original expiry. Initialize current `runtime_usage_retention_rules` empty; preserve source `runtime_usage_policy` consent/revision/collecting-since exactly. A legacy cap applies only to its archive and sequence range `(after_sequence,through_sequence]`, additionally retaining the row only while `occurred_at + retention_days*86400000 > now`. Released usage rows have logical source-projection policy revision 0, without archive rewriting; later positive policy-revision caps include them. Current collected writes still require positive revision. Copy bounded policy/cap tuples before inspection I/O, without holding/attaching control. Only Task5's verified maintenance may discharge an obligation.

Map old ACP markers to the actual six target columns `run_id,recording,retention_days,expires_at_unix_ms,archive_epoch,capture_generation`. Select only old `run_id,policy,expires_at_unix_ms`; validate bounded unique text ID, required managed Capture parent, text policy JSON decoded with `ContentRecordingPolicy.Validate`, and exact positive integer expiry. Full/metadata require 1..3650 days and off requires 0. Preserve ID/expiry and use the same explicit original epoch1/generation1 mapping as native/managed fences. Import expired markers and sound finished/revoked/expired parents without replaying live `Register` active/current-time checks. Optional snapshot/revision/final data and application checks do not become control proof; required marker/type/policy/parent damage still refuses. Do not use restored-CHECK whole-table validation for this mixed source table or infer defaults from payloads.

The concrete delta evidence is `/Users/null/Downloads/vibermate-timeout-fix-20261017-a6aswkq0/cutover-role-projection-reconciliation.md`. It is pinned to partial Task2 checkpoint9e8246fc, not final Task2 qualification. Before Task3 dispatch, reconcile final Task2 control/observation DDL, API names and schema pins against these mappings; optional content additions do not automatically join the authority allowlist.

Import final native rows into a private staged schema with only a fixed reviewed allowlist of lifecycle insert/update triggers deferred. Keep type/CHECK/unique/FK correctness and restore the exact final trigger DDL before certification/publication; no public OpenControl option can disable it. Never replay lifecycle methods or fabricate received/events/receipts to import terminal rows. Preserve all once/inbound identities independently of retired operations: valid historical replay barriers can have no extant operation/delivery, while retained started operations require their exact once/delivery/current proofs. Initial recording fence/source mappings are explicit and never enumerate history to invent live Capture authority. Full allowlists, source-proof boundaries, filesystem barrier ordering and tests are in `/Users/null/Downloads/vibermate-timeout-fix-20261017-a6aswkq0/control-cutover-task3-preflight.md`.

The unmatched physically-open native-attempt classification is now fixed by `/Users/null/Downloads/vibermate-timeout-fix-20261017-a6aswkq0/legacy-open-delivery-classification.md`. With both guards excluding the old process, validate/copy every required once/inbound barrier independently, and project only the actual attempts reached by sound retained-operation Start closures. A retired operation's open audit remains unchanged in the archive even if a valid once row links it; the released code has no surviving required consumer for that orphan. Keep the target delivery FK. No cleanup-only control table, synthetic operation/claim, fabricated terminal or receipt is needed. The old generic audit Recover must not be run as an extraction repair/normalization step.

This classification cannot hide a broken retained closure: a retained started operation or retained actual Start claim with missing/inconsistent once/attempt/provenance refuses extraction. Resolve a required attempt by exact once attempt ID before validating its purpose/parent/constants; prefiltering by native label must not silently omit a malformed required row. Additional unclaimed audits, including native-labelled rows naming an otherwise sound retained operation, stay archive-only. An absent operation alone does not invalidate a sound denying barrier, and optional orphan-audit semantic damage must not become its new proof requirement. Preserve required barrier constraints/owning FK closure and all structural refusal rules. Decisive fixtures cover real logical-completion→metadata-purge before physical close, prior generic recovery, valid standalone barriers with absent/malformed optional audits, required retained open/terminal deliveries and each broken closure, unclaimed native-labelled rows, exact replay denial after extraction, and unavailable archives with unchanged control denial. Physical-open state for retained logical-terminal operations remains exact; Runtime recovery belongs to Task4, never cutover.

- [x] **Step 1: Add failure-at-every-barrier tests before implementation.** Fixtures include fresh root, v1, v2, retained WAL, pending old migration, bad schemas, missing control with existing layout, path substitution and conflicting destination. Interrupt after receipt, each archive rename, control publication, layout publication and receipt removal; resume with no fresh identity, lost original history or stale-authority fallback. Assert a second normal startup never opens/hashes legacy/observation files. Old binary inspection rejects pending and final layout.
- [x] **Step 2: Record RED; implement the certified state machine.** Hold both guards. Before changing originals, durably publish receipt. Verified-copy DB/WAL into owned private staging; use the explicit version-specific role projection adapters and narrower source certificate above, without whole-monolith v1 normalization. Stream explicit control projections into fresh staging v3 (DELETE journal/FULL sync); reduce native event history to current tuple/max time and native attempts to required delivery facts. Copy IDs/revisions/digests/secret references/config choices, not replacement defaults or payloads. Test populated v2, optional row/payload defects versus required/structural refusal, retired once/inbound preservation, exact final triggers and fresh-install interruption.
- [x] **Step 3: Certify and publish.** Validate exact schema, integrity, FK closure, required native/control invariants and projection logical digests; verify original artifact digests still match. Close/sync staged control. Record ready, idempotently move originals under certified identities, sync directories, publish archive manifest, publish control+layout, record activated, and remove receipt only after final guards/barriers. Cancellation or uncertain durability remains incomplete. Never automatically roll back authority after v3 might have admitted writes.
- [x] **Step 4: Implement lazy archive readers and composite inspection.** Open v1/v2 archived sources only on an inspection/admin request with independent read owner/budgets; retained WAL uses normal read-only recovery semantics, not immutable mode. Preserve IDs and salt meaning. Source-aware cursors include archive identity/generation/local sequence and reject stale generations. Merge/deduplicate current+legacy by actual domain identity. Connection timelines have no stable per-event identity: select one canonical whole source timeline per ConnectionID, retain every local repeated occurrence, and bind continuation to the selected source. Legacy connection/egress visibility comes only from retained source-local ownership associations with every known fence visible; unmatched non-runtime rows are unattributed/invisible, while global runtime_action classification requires the full closed taxonomy and no contradictory Capture proof. Task5 must preserve/purge audit-ID closure before deleting its last ownership-proof roots. Native historical snapshots/ACP/Activity/Usage reads do not consult control from inside a long history transaction. Control visibility/policy snapshots are released before history work and rechecked after; Task 5 supplies mirrors/fences.

Convert every descriptor-bearing authoritative-Get consumer/test inventoried by Task1 to the separate optional inspection interface, including the native recorded reader/router fixtures. Preserve body digest/expiry, sanitization, authorization/scope, recorded DTO/content rendering and deletion-race assertions; Authority.Get remains body-free. Task2 already owns actual current-body publication/retention fixtures. Do not leave these intermediate inspection failures to an unspecified final cleanup or add control lookups to recover missing optional descriptors.

Add tests with overlapping local sequences and cross-source duplicate Exchange/operation IDs; global usage top groups that local top-N merging would miss; terminal-winner filtering before paging; native current-tail append and content-only discovery; nested source-qualified semantic cursors; original-salt archived reveal with blocked audit/deletion race; retained-WAL-only history; and stale generation/body-cache reset. Outer HTTP enrichment must perform its final visibility check after all constituent history reads, before publishing results or visitor output.
- [x] **Step 5: Run cutover crash/recovery/legacy/WAL and actual query regression fixtures; commit.** Keep raw payloads out of test logs. Commit `feat: cut over runtime storage while preserving legacy evidence`.

### Task 4: Wire optional recording startup, recovery, finish and health

**Files:**
- Modify: `internal/productruntime/runtime.go`, `builders.go`, `status.go`, maintenance and shutdown owners; `internal/capturerun` and `internal/manualcapture` terminal coordination; Raw facade construction; CLI doctor inspection and control status DTOs.
- Create: `internal/productruntime/recording_storage.go`, `recording_lifecycle_test.go`.
- Update recording status presentation in Flutter API/status models and localized AppCopy without adding prompts to Agent work.

**Interfaces:**
- `storageBuildResult` contains only required `*runtimepersistence.ControlStore`, schema state and prepared layout. Recording owner receives `ObservationOptions` and lazy archive descriptors separately.
- This task switches production `buildStorage` to Task3's `PrepareStorageOwned` plus `OpenControl` and changes all Runtime required/optional consumers coherently. Remove the candidate pre-inspection/rejection path so preparation progress precedes expensive work; honor the caller's explicit shorter upgrade budget. Restore the two real Runtime startup gates `TestSchemaSwitchSecondHostCannotEnter` and `TestSchemaSwitchDeadlineCannotActivateBackground` through the new owned path, retaining their ownership/deadline assertions. Task3's independent owned-boundary tests are prerequisite evidence, not substitutes for these production tests.
- Existing Runtime readiness tracks execution/control availability. Recording snapshot has closed starting/available/degraded/disabled states, reserved records/bytes, drops and unconfirmed writes; no raw errors/secrets.
- Use one fixed lifecycle owner for optional open/reopen/maintenance, and the two existing recording workers. While unavailable, producers drop cheaply and readers return explicit recording-unavailable. Install a writer only after its own schema/session/salt initialization succeeds; no raw fallback.

Required native recovery remains control-owned. Reconcile physically open `native_tool_deliveries` after exclusive ownership excludes the old process, including retained operations that already have a logical terminal state; never depend on generic observation/audit Recover for this. Preserve immutable provenance and once barriers, use honest interrupted/unknown physical outcome semantics, issue no new send receipt, and retain bounded control cleanup. Task3 preserves these open facts exactly. Retired-operation audit orphans remain archive history as classified above and do not create control cleanup work.
- Distinguish authorized first-time observation provisioning from a previously provisioned role whose file is now missing. Use owned layout/control provisioning state, not bare file absence, and publish recording readiness only after that state is durable. Never silently replace a lost/corrupt declared history role with an empty healthy store.

The split-role Archive, StorageStatistics and CleanupExpired administration implementations belong to Task5. During this unreleased Task4 checkpoint, expose explicit unavailable/no-mutation results for those absent ports instead of retaining a combined Store bridge or claiming deletion, cleanup or zero statistics. This must not gate ordinary auth/configuration, control readiness, doctor environment checks, dashboard or approvals. Preserve genuine holder/revocation/I/O obligations and inventory the temporary refusals. Task5 must restore the actual administration semantics and caller tests before final acceptance/release; no intermediate release is permitted.

Carry the durable original recording Scope already selected by required capture admission through existing admission/retained-receipt and connection Attribution values. Native/ACP use their already-loaded originals. A narrow optional OriginalScopeIssuer companion may issue a stamp from that Scope after matching its epoch to required runtime state; never guess captured generation1, query control solely for recording on forwarding, or add a per-Capture recorder registry. Ref-only production issuance is for genuinely global work; unknown Capture coordinates drop optional copies. Qualify actual restored non1 epoch/generation propagation and DTO/buffer ownership. Normal Finish sealing and Task5 durable delete/clear fencing remain distinct; a freshly constructed stamp alone is not per-Capture revocation proof.

- [ ] **Step 1: Add Runtime startup and finish fault regressions.** An opener that blocks/signals entered, a corrupt observation file, failed ordinary history recovery, and a locked observation maintenance transaction must each leave real control startup/Create/Attach/Heartbeat usable before the fault is released. Finish must revoke/join actual transport work but not await Raw/semantic durability. A required control open/commit fault still correctly fails authority work. Doctor reports the distinction. Cover fresh optional provisioning versus missing previously provisioned observations. Control dashboard/bootstrap and live pending-approval previews must remain usable when optional history reads fail; a dropped log must not indirectly block Agent work by preventing the Owner from seeing or deciding an actual approval.
- [ ] **Step 2: Record RED; then move optional work behind the lifecycle owner.** Remove ordinary egress/connection recovery from mandatory startup. Raw salt/session work happens only in optional initialization. Scope terminal seals new recording admission without waiting for durability; it does not invalidate already-admitted observations merely because an otherwise normal Capture finished. Deletion/clear/consent fences still reject stale queued work. Add a short completed-Capture test proving its previously accepted final records can persist after Finish, without making Finish await them. Do not remove joins for real network I/O, retained revocation or live body ownership.
- [ ] **Step 3: Implement cancellation and truthful shutdown ownership.** Close offers; cancel reads/workers; release queued copies; keep in-flight reservations/resources until their actual return. Bound caller shutdown without starting per-record cleanup goroutines or releasing directory ownership while any owned I/O can still touch files. If workers outlive the budget, retain the lifecycle owner and report recording cleanup pending; do not falsely report all handles closed. Execution readiness is not canceled merely because optional cleanup failed.
- [ ] **Step 4: Expose actionable loss state.** Doctor reports control readiness and recording degradation separately. UI distinguishes unavailable/incomplete evidence from failed Agent execution and never infers zero traffic from missing rows. Counters survive only in-process; do not fabricate exact crash-loss counts. Run lifecycle/status/doctor normal and race checks; commit `refactor: isolate recording lifecycle and health from runtime readiness`.

Restore the asynchronous warning-stage/readiness/clean-shutdown regression lost when the predecessor changed `productruntime/usage_failure_test.go`: use the actual queued failure reporting path and settle before checking status. Intentional consent/expiry discards are distinct from failed writes. The final whole-branch review must triage this predecessor Minor explicitly.

UI availability proof must hold optional detail/history reads pending while initial dashboard and pending approval discovery/decision still complete. In `ui/flutter_app/lib/features/workbench/workbench_controller.dart`, `_refreshDashboard` currently awaits `_loadCaptureDetail` before `refreshPendingApprovals`; do not preserve that dependency after storage isolation. Keep refresh ownership/generation checks and bounded requests. `loadDashboard` also requires the Capture catalog, whose ACP markers must use Task2's control registration repository; no optional marker/snapshot read may turn it into DashboardUnavailable.

Separate Conversation identity reads from optional local-client reindex writes. Task3 supplies the source-aware reader/Indexer continuation and original scope/source/row provenance. This task supplies the actual lifecycle-owned conditional repair writer for an existing current Activity root; ordinary Ruling34 producer identities remain independent roots. Skip new local-client repairs of archive-only rows while keeping retained identity/protocol evidence readable. Do not copy archived Activities, introduce an enrichment overlay, call rejected unscoped writes, or obtain a fresh scope to relabel an old record. Writer readiness cannot gate construction/control. Count and exclude these administrative writes during maintenance/shutdown alongside queued records and synchronous Raw audits; preserve actual delete/clear/late-callback tests and exact identity conflicts.

### Task 5: Durable visibility, retention and physical current/legacy cleanup

**Files:**
- Create: `internal/runtimepersistence/recording_control.go`, `recording_cleanup.go`, and focused mutation/publication tests; extend Task3's actual inspection-state/mirror read modules rather than duplicating them; `internal/runtimedata/archive_cleanup.go`, `archive_cleanup_test.go`.
- Modify: `capture_deletion.go`, clear/retention/storage statistics queries, usage policy/query code, `internal/evidencearchive`, `internal/resourcedeletion`, deletion control DTOs/UI result copy.
- Add schema tables through the explicit new role schemas created in Task 1; all changes remain inside the unreleased revision 3/observation revision 1 candidate.

**Interfaces:**

```go
type CleanupTask struct { ID int64; Capture captureidentity.Reference; Fence recording.Fence }
type DeletionResult struct { Deleted, CleanupPending bool; Released resourcedeletion.Released }
type RecordingCleanupControl interface {
    PendingCleanup(context.Context, int) ([]CleanupTask, error)
    CompleteCleanup(context.Context, int64) error
}
```

Read-side snapshot/recheck types and the minimal mirror relation are supplied by Task3's final report under the shared visibility-mirror-read-contract; the former unbounded-looking map/interface sketch is superseded. Reuse that exact contract. This task implements its bounded256-row exporter and complete atomic publisher, including version change between exported pages, no partial publication, restart and stale-mirror refusal. The cleanup task shape above still needs its concrete per-archive/generation/phase fields when this task consumes the actual Task3 catalog/lease API.

Control owns indexed `recording_capture_fences`, global archive epoch, visibility revision and coalesced cleanup obligations; these are required deletion state, not a durable ordinary-log queue. All queued observations carry admitted generation/epoch. Scoped history reads use bounded copied filters. Global reports require an observation-local versioned visibility/policy mirror exported in bounded control pages; no long control attachment. Recheck revision before publishing results, otherwise return snapshot-changed/unavailable rather than leak erased data.

Retain every still-relevant live Usage copy and its applicable cap semantics; do not remove a noncanonical copy or its archive merely to deduplicate it. When the logical Exchange expires or is deleted under the all-copy rule, durably deny that whole identity before purging all copies. Before erasing an audit association root, durably deny and purge the affected typed Attempt/Connection closure even if currently visible, preserving unrelated identities and shared bodies. This conservative optional-record loss avoids live inherited-cap/association machinery. Negative membership belongs to the optional store, with atomic sink/read enforcement and bounded reliable completeness/loss detection; no per-record control ledger or extra worker. Missing required coverage degrades inspection/recording only. Bounded batches do not bound total retained denial metadata; absent a proven non-reintroduction boundary, its identifiers may remain indefinitely, and physical-completion wording must say so. Every retention/clear/scoped-cleanup path must preserve this invariant. Exact ports, recovery and later-link handling consume the final Task3/4 interfaces; reasoning is in `cleanup-duplicate-provenance-preflight.md` under the evidence directory.

- [ ] **Step 1: Add late-commit/restart deletion and consent tests.** Pause an observation transaction before commit; delete scope in control, then let the old write commit. Current and legacy queries/aggregates must stay invisible across restart; new Capture control remains responsive while physical cleanup is blocked. Shorten usage retention, enqueue delayed old-policy usage, then lengthen: old data remains capped. Disable/re-enable collection does not revive old callbacks.
- [ ] **Step 2: Record RED; implement reliable fence publication and mirrors.** Capture creation assigns generation; deletion commits tombstone/revocation and cleanup obligations together, then publishes revocation in memory. Clear advances global epoch with its existing explicit authority-clear semantics. Existing live-holder refusal remains. Long inspection releases all control operations before I/O and verifies its visibility version again before returning. Stale jobs drop/release reservations.
- [ ] **Step 3: Implement consent caps without history I/O on setting changes.** Control `runtime_usage_retention_rules(retention_days, through_policy_revision)` caps through previous policy revision on shortening; prune only dominated rules. Preserve legacy sequence caps by archive ID. Current rows retain original observation time/policy/expiry. Reader mirrors apply caps before aggregation. Lengthening cannot remove old shorter caps.
- [ ] **Step 4: Implement resumable physical cleanup.** Current observation GC runs independently and acknowledges reliable obligations only after actual completion. Legacy cleanup uses an archive-only exclusive lease and maintenance receipt, bounded compatible v1/v2 scoped deletion plus shared graph GC, then separately cancelable WAL/compaction work. No control connection/transaction/mutex is held during history I/O. Record archive generation changes and invalidate old cursors. If replacement is necessary, verify/publish under an archive receipt and remove superseded DB/WAL/work copies before claiming physical completion. Never delete unrelated shared bodies or identities. A tombstone is not an erasure receipt.
- [ ] **Step 5: Report only verified Released counts and explicit cleanupPending; run delete/read race, retained-WAL, interrupted cleanup and usage consent tests; commit.** Commit `feat: preserve deletion and retention across lossy recording`.

### Task 6: Role-aware backup, restore, private snapshots and move

**Files:**
- Modify: `internal/runtimedata/backup.go`, `schema_snapshot.go`, `move.go`, corresponding tests; schema offline verification for explicit roles.
- Extend current native-inspection/egress-default backup fixtures rather than replacing required identity/config preservation assertions.
- Update deployment/operator documentation for layout, incomplete recording, upgrade and rollback compatibility.

**Interfaces:**
- Public manifest `vibermate-data-backup/v2` retains file/hash inventory and secret exclusions, adds layout ID/document plus role list `{path, identity, revision, required, status, archiveId}`.
- v1 public backups and existing private snapshots remain readable. v1 restore cuts over only the restored destination under its guards, never the backup source.
- Old private rollback restore retains its existing faithful original-schema/content completion boundary. Verify its old inventory before any subsequent upgrade; current Runtime admission may then run owned cutover/reconciliation only on that restored destination. Never implicitly upgrade the original private snapshot or change old protocol semantics inside its restore operation.
- Preserve actual public exclusions (root server-secrets/development-secrets directories, external Keychain) and inclusion of account references/config/CA; no new SQL row redaction. Validate retained-WAL sources on independent copies, never through a source-mutating read-write validator. Existing stopped-runtime/host exclusion remains; no new live-backup mode. Concrete source/fixture evidence and Task5-dependent contracts are in `/Users/null/Downloads/vibermate-timeout-fix-20261017-a6aswkq0/backup-role-compatibility-preflight.md`.
- Control + its WAL are mandatory. Existing declared observation/legacy files are included by default; omissions or unverified history are explicit, never certified as complete. Every role's SHM is excluded and required WAL retained. Pending mutation receipts cannot be exported as completed backups.

- [ ] **Step 1: Add multi-role/WAL/legacy round-trip tests.** Preserve required identity/config/native safety plus exact sample Raw/semantic/ACP/native history across backup/restore/move. Cover missing optional, corrupt optional, missing required control, mismatched layout, v1 backup and old private rollback. Public exports still exclude provider credential/keychain material. Reopening the restored control must not scan optional history.
- [ ] **Step 2: Record RED; implement role manifest and validation dispatch.** Reuse stopped-runtime/host guards, verified file copying and existing inclusion/exclusion rules. Verify control schema/invariants/layout before success. For optional semantic validation failure either fail backup clearly or preserve with explicit unverified status; never silently drop existing history. Do not recreate lost control identity during restore.
- [ ] **Step 3: Run bounded backup/restore/move/private snapshot affected tests and formatting; commit.** Commit `feat: preserve split runtime data in backups and restores`.

### Task 7: Real Runtime acceptance, whole-branch review and release qualification

**Files:**
- Create: `internal/productruntime/recording_isolation_acceptance_test.go` and focused reusable test fixture in that package.
- Update obsolete mandatory-log assertions and relevant architecture/deployment/status documentation.
- Version/release changes only after the behavioral matrix and independent review pass; use next unused version, never modify published 0.1.29 assets.

- [ ] **Step 1: Add the final actual-composition matrix first.** Temporary real control/observation DBs, actual loopback control/proxy, deterministic local upstream and native/ACP harnesses. Cases: optional open failure/corruption; blocked writer plus filled shared budget/live buffers; dropped start plus terminal; blocked content/native/ACP; real revoked/duplicate authority with recording unavailable; delete racing commit/restart. Hold actual observation writer and inspection read/maintenance work independently; control operations finish before release. Add large unrelated history and assert normal startup never touches it. Verify exact JSON/SSE/CONNECT/UDP bytes, real child attach/heartbeat/finish, once/replay, loss status and owned shutdown resources.

```go
// Each fixture proves the fault is active before exercising Agent work.
<-observationBlocked
result := exerciseAuthorizedAgent(ctx, fixture)
if result.Err != nil { t.Fatal(result.Err) }
if !bytes.Equal(result.Body, expectedBody) { t.Fatal("delivery changed") }
// Only now release optional storage, then join every owned fixture goroutine.
close(releaseObservations)
```

- [ ] **Step 2: Record any RED gaps and fix them within their actual ownership module.** Preserve test strength for auth, frozen routing, pending approvals, unknown commits and delivery replay. Verify no remaining live call edge from Capture control/forwarding to history persistence/flush. Do not treat a passing query benchmark as the architectural proof.
- [ ] **Step 3: Run affected integration/race checks once, then final repository checks on the qualified candidate.** Consult current repo CI/release scripts. Avoid duplicate local hour-long persistence campaigns; use exact-source CI once and rerun only failures or changed scope. Independent whole-branch review gets all three plan ranges, spec, ledger rulings, deferred findings and test evidence.

Before qualification, resolve the full Runtime failure list retained in bounded-recording `task-4-report.md`/`runtime-complete.log`: migrate old immediate-audit and repository-hook fixtures to nonwaiting observation probes plus explicit post-producer inspection settlement, while preserving actual frozen route/auth/transport-close assertions. Root's baseline sample proves several predecessor failures, not that the entire list is harmless. Recheck the broader-run SOCKS shutdown timeout (focused current-head triage passed) and finish the two legacy startup cases through the real direct cutover. No full-Runtime red result is waived by the task-scoped queue reviews.

The complete40-failure inventory and shared fixture seams are in `/Users/null/Downloads/vibermate-timeout-fix-20261017-a6aswkq0/runtime-fixture-qualification-preflight.md`. In the managed rotation-tail test, the pre-loss assertion occurs while actual model dispatch is deliberately held: use producer observations plus actual persisted credential epoch/revision there, then settle only after real I/O completion. Shutdown may discard accepted optional rows, so never add a shutdown Flush to satisfy former terminal-row assertions. Retained revocation/expiry tests must hold actual socket/body/inner-handler cleanup independently of a blocked optional sink; the existing repository override now intercepts only a reader and cannot prove the execution cleanup join.
- [ ] **Step 4: Complete user-authorized normal release only after review/checks pass.** Use normal source review/merge, exact merged-source CI, protected macOS signing/notarization/install verification, Linux distribution, independently verified public assets and Homebrew update. Every host gh/remote git child removes only inherited GH_TOKEN/GITHUB_TOKEN overrides; use stored valid login. No force tag, bypass or stale artifact. Published release does not claim a Runtime Server deployment or local App restart.

## Acceptance and progress

This plan completes the architecture only when every task plus the predecessor queue and reliability plans pass review and the final matrix proves the isolation. Track each task in its own SDD ledger with commit bounds and exact verification. Keep the user's existing App/data unchanged during development; any later install/restart is a concrete separate action. Do not end the task at a supporting timeout/query fix or a design document.

## Ruling44: supported layout versus inspection capacity

Ruling: Align the bounded inspection inventory with the supported StorageLayout: accept up to128 validated legacy descriptors plus the current observation source (129 total), preserving every descriptor and the full source-set fingerprint; acquisition still enforces the actual pinned SQLite attachment/resource limit and fails that inspection explicitly. Do not add a current-only fallback or reject required Runtime startup solely for this catalog off-by-one — validStorageLayout already accepts128 archives, while inspectionInventory incorrectly caps the total including current at128, and the approved plan assigns resource overflow to inspection rather than execution — cost if wrong: catalog bookkeeping grows by one bounded descriptor and oversized history remains unavailable to inspect; malformed or truly unsupported inventories must still refuse, and an actual128-archive Runtime control/startup plus explicit no-omission inspection-refusal test must qualify the correction.

## Ruling45: failed attachment retains host ownership

Ruling: When Server attachment fails after Runtime construction, preserve bounded failed-start return and transfer its one owned host guard to the existing Runtime shutdown owner through a narrow typed handoff. Release that guard only after actual cleanup completion, with atomic idempotent handling of handoff versus completion; keep normal Host/Attach ownership unchanged and add no extra goroutine, recording lane or generic cleanup callback registry — serverhost.Start currently releases server.lock unconditionally after a timed-out Shutdown, while calling Shutdown again with Background still returns the existing shutdown budget and an unbounded join would hide the real startup error behind optional I/O — cost if wrong: the additional single-slot lifecycle API can leak or release a guard early; actual attachment-failure, bounded return, second-host exclusion and eventual real-return release tests must prove ownership.

## Ruling46: Desktop owns failed-start cleanup

Ruling: Keep failed Desktop startup cleanup owned by Desktop: run its existing rollback on one fixed failed-start cleanup goroutine while the caller waits only its existing cleanup budget. Initiate Runtime shutdown before an unbounded attached-host join when that handler may depend on Runtime cancellation, and join actual Runtime plus attached-host completion before final generation release. Preserve the normal public bounded shutdown API; add no recording worker/lane or generic cleanup registry — the Desktop generation guard lives in cache outside Runtime data, and the attached Server has independent listener ownership, so extending Runtime with those foreign lifetimes would couple modules unnecessarily — cost if wrong: one additional fixed lifecycle owner can remain alive and hold the failed Desktop generation until real I/O ends; incorrect initiation/join ordering can deadlock or release early, so actual post-Runtime failure, attached-enabled cancellation dependency, bounded caller/original cause and eventual-release tests must qualify it.
