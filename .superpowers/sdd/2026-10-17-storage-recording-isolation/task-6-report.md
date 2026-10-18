# Task6 role-aware backup implementation report

Status: DONE — implementation and bounded checks complete; root independent review pending. Base ad5f5cdd421c386ac0a589298d23e9130c7ab493, sole source/index/test owner in worktree1c22. No subagents, remote/release/user App/data or other-worktree operations. Explicit Go1.26.9 used for every Go invocation. No CodeGraph directory; used rg/source reads. Read final dispatch brief/inputs, Task5 final handoff, integration report, both consumer preflights, published fixture map and CONTEXT/ADR0013/0023. Behavioral TDD and verification-before-completion skills applied.

## Slice1 — source-immutable offline inspection

`ValidateOfflineDatabase` now always copies DB and any retained WAL/journal to an independently owned temporary directory, validates that copy, and rechecks original file identities/digests plus sidecar presence. It never opens SQLite on the source. Temporary cleanup errors fail validation. The explicit `ValidateOfflineRole` seam uses that same mechanism, exact identity/revision/layout/source schema, integrity/FK and required semantic checks. Its fixed control result contains cleanup and denial coordinates only; all SQL closes before the caller can inspect history. Observation validation streams the existing full denial chain and checks the required prefix, including any tail; no normal writer or initialization is used.

Causal proof: `offline-wal-red.log` reproduces old validation removing the stopped source WAL. `offline-wal-green.log` passes with the committed WAL state read correctly, source DB+WAL exact and no created SHM. Exact command: `/Users/null/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.9.darwin-arm64/bin/go test ./internal/runtimepersistence -run '^TestOfflineValidationRetainedWALLeavesSourceBytesUntouched$' -count=1 -timeout=30s`.

Logs under `task-6-logs/`; failures are retained and never relabeled green. Further role/format/eligibility/preservation/Task7 evidence will be appended before completion.

## Final implementation and format boundary

Implementation and bounded verification are DONE; root’s independent review remains pending. This report does not certify a release or the Task7 Runtime/acceptance matrix.

Source commits:

- `c63a0c8b11881d53ec4d48731f0636b3f2721e0f` — source-immutable offline DB/WAL validation and the explicit role-validation port.
- `019ec234eb01d15d36ac69e96c3ff2f9f760dc81` — `feat: preserve split runtime data in backups and restores`, including the producer/reader correction, exact-format fixtures, focused preservation/fault checks and English/Chinese operator documentation.

The review range starts at the original approved BASE `ad5f5cdd421c386ac0a589298d23e9130c7ab493`. No Tasks1–5 restart, private production adapter, remote action, release, real provider request, user App/data access, subagent, or other-worktree modification occurred. A final report-only commit follows these source commits.

### Manifest dispatch and explicit status

**Writer boundary:** an explicitly selected stopped published monolith still writes the existing public `vibermate-data-backup/v1` and private `runtime-schema-upgrade/v1` formats. No virtual layout is fabricated. Declared split layouts write public `vibermate-data-backup/v2` and private `runtime-schema-upgrade/v2`. Normal prepared Runtime data is split and therefore uses v2. This boundary and its compatibility cost are explicit, rather than silently changing old rollback inputs.

V2 retains `schema`, `createdAt`, one required-control `database`, full sorted `files` path/size/SHA256 inventory, and public `recoverable` scope. It adds:

- `layoutId` and `layout`, the bounded `vibermate-storage-layout/v1` document;
- ordered `roles`: control first, current observation second, then every declared archive in layout order; each has `path`, `identity`, `revision`, `required`, `status`, and an optional `archiveId` (current uses its deterministic source ID);
- required `historyComplete` boolean.

Only required control is `required=true`. Its status must be `verified`. Current observations are `verified` only after exact role/layout/source schema, integrity/FKs, complete denial-prefix/full-tail proof, and known internal stored-format decoders pass. Those reuse existing connection/egress/activity, Raw envelope/body/hash, semantic manifest and bounded graph/physical loader, ACP snapshot/policy/revision/final, Usage identity/JSON, native history/index and native-content identity/body decoders. Provider wire body meaning is not reinterpreted. Invalid present current data is refused, never silently omitted or called verified.

A retained legacy role is `unverified`: exact supported schema, integrity/FKs, original or maintained inventory/provenance are verified, while its optional internal payload semantics are not exhaustively reinterpreted. Every byte is retained. This is the narrow R73 refinement; it does not permit known structural, provenance or required privacy damage. A missing declared optional role is `absent`. Either status makes `historyComplete=false`. Even true only certifies the declared-role backup contract, never that best-effort recording captured every activity. Restore→rebackup retains this qualification through the actual layout; no Runtime status registry, control table, or new incomplete-recovery format was introduced.

The strict public v1 decoder retains its original closed field set and rejects even null/empty v2 fields. Public v2 requires the emitted canonical JSON apart from surrounding whitespace, rejecting duplicate members/unknown fields. Private v1’s exact canonical encoding/newline and duplicate/unknown/path/inventory/permissions rules remain intact; private v2 uses that same strict boundary with the role extension. V1 cannot claim a split role database merely by stripping/downgrading the manifest. Old monolithic 1→2/2→3 runners cannot consume split v2 as their rollback state: certified role identity/revision/layout do not match their strict source boundary.

### Eligibility and provenance

Every complete new-role backup, move and private snapshot checks the actual copied required singleton, not fixture-only mirrors or absence of a receipt: requested revision equals completed revision, active revision is zero, and source generation is valid. Any schema/cutover receipt, `recording-cleanup.json`, or reserved `.recording-cleanup-*` / `.recording-certificate-*` artifact refuses completion; no source reconciliation/deletion runs. This covers request/claim-before-receipt and retired-receipt-before-completion-CAS gaps.

Layout plus private regular0600/bounded4096-byte `recording-state.json` must bind the exact layout/current source and compatible provisioning state. Unknown, lost or downgraded proof is refused. A provisioned but missing optional DB can be exported explicitly absent only when required denial coverage does not depend on it; its receipt/layout remain provisioned after restore, withholding authorization to recreate lost history. A nonzero or otherwise mismatched required denial prefix without the observation proof refuses completion. The full optional chain/tail is verified on the private copy, not replaced by a fresh mirror.

For generation1/completed0, no maintenance certificate is legitimately required. Otherwise the bounded existing maintenance document must match the exact layout ID, ordered source-set hash/descriptors, completed target revision, source generation and cutoff. The consumer compares every coordinate, including cutoff; it does not rely on the Task5 helper’s incomplete replacement guard. Archive DB bytes must match the latest maintained hash and size, retain no unresolved WAL/journal, and link the unchanged original `archive-manifest.json` hash. Never-maintained archives verify their original DB/WAL descriptors. Original SHM descriptors remain historical provenance while export SHM omission is independently represented by the new inventory. Current observation bytes legitimately change after maintenance and are independently recertified as a stopped snapshot rather than compared with their historical certificate hash.

The concrete integration gap was `ReadRecordingMaintenanceCertificate`’s archive predicate in `recording_certificate.go`: the producer/layout already supported runtime3, but the reader admitted only runtime1/2. It now admits exactly runtime1/2/3 with `vibermate-runtime` identity; there is no numeric `>=` fallback. Actual Request→Claim→file-half producer→Complete→reader→Backup fixtures qualify this consumer, including legitimate changed current bytes, stale archive/provenance and all coordinate refusals. These tests exercise the byte/coordinate contract, not a new claim of physical cleanup qualification.

`countsExact=false`, `ApprovalOwnershipUnknown` and `PrivacyMetadataRetained` survive in their copied maintenance metadata. They disclose retained identifiers/unknown totals; they are neither unfinished-work proof nor full Capture-approval/forensic erasure claims. No free-page/WAL/media/snapshot erasure promise was added.

### Ownership, immutability, restore and secrets

Public Backup/Copy/Restore/ValidateBackup retain both source runtime and host guards; target copy/restore also owns both guards. Private creation consumes the caller’s exact runtime guard and validates/acquires host ownership; validation/restore own both source/target guard sets as before. Existing durability/unknown-target/symlink/cancellation/error-category contracts remain. Required control is inspected without initialization: no validation path uses normal `OpenControl` or creates missing authority.

`ValidateOfflineDatabase` and `ValidateOfflineRole` open SQLite only on independently owned verified DB/WAL/journal copies; original identities/digests and newly appearing sidecars are rechecked. Required role validation returns a fixed-size snapshot and closes all control SQL before any history copying/validation. There is no live control Store/operation/transaction spanning history I/O. The source full persistent inventory and guard ownership are rechecked through finalization. Public failure removes its completion manifest; existing private failure/durability recertification behavior is preserved.

Public v1 restore validates source and **copied old inventory before any destination recovery/cutover**, then invokes `PrepareStorageOwned` only on the new guarded destination. The backup source remains unchanged. Split public restore validates/reproduces existing roles without an unnecessary second preparation open (the initial WAL test caught that open creating target SHM). Old private v1 restore remains faithful original runtime1/2/3 content/schema/credentials; only a later Runtime admission may upgrade/cut over the restored destination. Split private v2 restore reproduces the recorded layout/facts, without implicit cutover.

Only declared role SHM paths and existing lock files are ephemeral. Every role’s retained WAL survives, including nested archives. A user-authored unrelated `*-shm` file remains persistent. Public policy still excludes only the root `server-secrets/` and `development-secrets/` stores; Keychain/external TLS remain external. Public copies retain account/SecretRef/config/identity rows, Raw salt and local CA certificate/private key/manifest; no SQL row scrubber was added to monoliths. Private snapshots and moves retain all persistent credentials and unknown files with their existing permissions policy. Encryption remains false.

### Fixture qualification and preserved assertions

No production reader accepts the private combined test adapter. Existing repository construction remains explicit test-only staging. Before production operations, the closed frozen runtime3 target is built from `schema/runtime_v3.sql`: every published non-generated column is inserted, every emitted column (including generated values) and every `sqlite_sequence` high-water mark is independently compared, nonempty staging-only tables are refused except the exact adapter-default layout singleton, and nondefault staging-only columns are refused. There is no common-column intersection or hidden conversion in open/shutdown. Restored outputs are never rewritten on read/close. Actual public control reads use role repositories; faithful private monolith account/config/Environment assertions extract only a disposable DB/WAL copy, while actual original Raw bytes and file/row inventories remain independently asserted.

The four actual integration failures are closed by the final affected run: `TestPublishedRuntimeFormatsPublicBackupAndMove/daybreak_blue`, `/daybreak_red`, `TestSchemaUpgradeSnapshotUsesHeldOwnershipAndPreservesPrivateFiles`, and `TestSchemaUpgradeSnapshotCommittedUncheckpointedWAL`. The additional `TestRevision2OwnedOrdinaryManifestRejectsOperationalReceipt` preserves its exact `errors.Is(ErrBackupInvalid)` and no-target-creation assertion. Original1/2 cases are included in the final affected run because public destination-only restore behavior changed; they preserve the archived old source and values. No unrelated broad campaign was repeated.

The populated split fixture uses exported real control/native authority and observation seams: nondefault SOCKS egress binding, real session/handshake/catalog/tool binding, allowed then consumed approval, once/inbound/delivery rows, uncertain NULL byte counts, native history/index/content, Raw envelope/body/salt, semantic graph and full ACP snapshot. Backup→public restore, move and private snapshot→restore compare every control/current/archive SQL row plus exact typed content and native facts; source DB/WAL/provenance bytes remain unchanged. Actual persisted source-reader baselines handle nil/empty canonicalization while retaining exact authored body/text assertions. Old private tests still resolve the actual restored ServerFile credential and compare account/SecretRef, canonical Environment JSON/digest, configuration, large profile and Raw body. Original native-purpose/empty-endpoint and exact global/transport default2/terminal tuple assertions remain, with direct read-only field comparisons where appropriate.

### Causal evidence and focused final commands

All command logs remain in the requested ignored `task-6-logs/` directory. `test-inventory.json` records exact matched test names/results for final JSON invocations; `causal-probes.json` records exact restored-source mutation commands, statuses and durations. Package passes below are bounded evidence, not full Runtime qualification.

| Evidence | Actual outcome and causal meaning |
|---|---|
| `offline-wal-red.log` → green/final | Old validation removed source WAL; independent-copy validation reads committed state with DB/WAL exact and no created SHM. |
| `role-manifest-qualified-red.log` → `role-manifest-green.log` | Real split source was backup_incompatible; v2 public Backup→Validate→Restore/control reopen passes. The earlier `role-manifest-red.log` is a fixture timeout-option failure, not product RED. |
| `private-role-red.log` → green | Split private snapshot was backup_incompatible; separate v2 snapshot/validation/restore and private move pass. |
| `role-wal-inventory-initial.log` → green | Unnecessary split restore preparation created target control SHM; only old monolithic public restore now invokes cutover. Required/current WAL, exact sources and unrelated user suffix file survive; all129 declared inspection sources are retained. |
| `optional-semantic-red.log` → `optional-semantic-qualified-green.log` | Malformed ACP internal snapshot was labeled verified; actual stored-policy decoder now refuses it. `optional-semantic-green.log` is a compile error (invalid fixture type-field assumption), not GREEN. |
| `maintained-runtime3-red.log` → green | Actual runtime3 certificate producer output was refused by old reader; strict1/2/3 reader plus all-byte/coordinate/provenance checks pass. |
| `strict-dispatch-red.log` → affected/final checks | V1 accepted a null v2 extension and v2 accepted duplicate schema; strict version/canonical dispatch now refuses both. |
| `published-fixtures-first.log` | After qualifying the fixture, ordinary receipt restore exposed wrong ErrSchemaUpgradeIncomplete category. Fixed Restore’s source refusal to ErrBackupInvalid before target creation; original assertion passes. |
| `copy-affected-initial.log` | Existing corrupt-source and canceled-copy error categories regressed; source copy now retains ErrValidation and context cancellation without weakening assertions. |
| `populated-role-reader-diagnostic.log` → reader/qualified/final evidence | Normal source semantic Get succeeded but offline projection refused. Root cause was missing `loadPhysical=physicalContentRow` in the offline bounded reader; the established loader fixes validation. A later typed assertion exposed only constructor empty slices versus persisted nil slices; exact source-reader baseline and all-column comparison preserve real content. |
| `optional-native-identity-behavioral-red.log` → `final-refinements.jsonl` | Known malformed native content identity passed structural schema checks and was labeled verified; actual native-content identity/body decoder now refuses it. Earlier native-identity logs are fixture multi-statement binding/trigger duplicate errors, not product RED. |
| `cleanup-request-causal-{red,green}.log` | Temporarily omitting only required cleanup eligibility makes requested/no-receipt Backup complete incorrectly. Restoring exact source makes Backup/Move/new-private refusal pass. |
| `privacy-prefix-tail-causal-{red,green}.log` | Temporarily omitting only denial-chain verification makes both missing required prefix and corrupt tail complete incorrectly. Exact source is restored in `finally`; both refusal cases pass again. |

Final covering command (explicit Go executable prefix below for every command):

```text
/Users/null/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.9.darwin-arm64/bin/go test -json ./internal/runtimedata -run '^(TestBackup.*|TestCopy.*|TestNativeInspectionPurposeSurvivesActualBackupRestore|TestPublishedRuntimeFormatsPublicBackupAndMove|TestSchemaUpgradeSnapshot.*|TestRevision2OwnedOrdinaryManifestRejectsOperationalReceipt|TestRole.*)$' -count=1 -timeout=120s
```

`affected-final.jsonl`: PASS106.015s,138 test/subtest entries,32 top-level tests,0 failures. Includes the original private target/ownership/tamper/copy/sync/manifest/cancellation/restore/uncertain-durability/final-barrier cases. This complete affected group was not repeated after it passed.

Only final changed behavior/additional assertions were then covered:

```text
go test -json ./internal/runtimedata -run '^TestRoleBackup(RejectsMalformedOptionalNativeContentIdentity|RejectsMalformedOptionalACPSnapshot|PopulatedNativeRawSemanticACPAndAuthorityRoundTrip|ArchivedWALExcludesSHMWithoutRewritingOriginalProvenance)$' -count=1 -timeout=60s
go test -race -json ./internal/runtimedata -run '^TestSchemaUpgradeSnapshot(HoldsDestinationHostOwnership|WaitsForFinalDurabilityBarrier)$' -count=1 -timeout=60s
go test -json ./internal/runtimepersistence -run '^(TestSchemaRevisionTracksSchemaFile|TestOfflineValidationRetainedWALLeavesSourceBytesUntouched)$' -count=1 -timeout=30s
go vet ./internal/runtimedata ./internal/runtimepersistence
```

The `go` shorthand here denotes the same explicit Go1.26.9 executable, not ambient Go. Final refinements PASS4/5.764s include legacy `unverified`/false surviving restore→rebackup and a stopped archive with original retained DB/WAL/SHM provenance: every export preserves original DB/WAL/manifest bytes, deliberately omits archive SHM and leaves all source bytes unchanged. The narrow race is justified by the retained private guard/durability barrier lifetime after inspection changes: PASS2/8.213s. Offline/schema pins PASS2/0.855s. Vet exit0; gofmt reports no unformatted files and `git diff --check` passes.

Compile/setup/path failures remain separate: populated fixture initially had a string/byte type compile error, incomplete codec registry, and missing ACP final ExitCode; those were construction defects, not product behavior. One gofmt command misspelled the toolchain directory `v0.01-...` and did not run; the corrected command passes. Initial checksum command guessed nonexistent migration paths and exited1 (`schema-pins-initial-path-error.log`); corrected actual migration paths pass (`final-schema-pins.log`). None is relabeled behavioral proof or hidden behind a passing aggregate.

### Final pins, self-review and Task7 handoff

`final-schema-pins.log` and `TestSchemaRevisionTracksSchemaFile` retain exact control3 `19c6eda3cd83a8f7836446835164367db49f60573c88aca561e6c381b92cc10a`, observations1 `ed9fc1e5f71ca1801789d84488810df23872c0de520dea6360ad5573267899bd`, and all frozen runtime1/2/3/001/002 hashes from the approved brief. No DDL/migration/pin changed.

Source/report self-review checked strict version dispatch, no source SQLite open/bootstrap, required/privacy failure paths, both guard sets, original/copied inventory ordering, private v1 completion/credential compatibility, retained WAL and role-qualified SHM, canonical statuses and restore/rebackup revalidation, source-generation/cutoff/certificate matching, source immutability and coherent fixture boundaries. Remaining cost is explicit offline whole-file copying/hash/known-current-payload verification and conservative unverified legacy semantics; no cache optimization or live-backup support was added. Root owns independent source/report review at the original BASE range.

Task7 inputs remain mandatory and unchanged:

1. Integration’s reproduced `TestRuntimeLongSessionCompleteTail/original_invalid_tail` still fails shutdown with `close raw evidence writer session: inspection is unavailable`. Its required no-send/no-credential/complete-input rejection assertions passed, and actual `ShutdownDone` plus both reacquirable guards were independently proved before this task. Task6 consumes that ownership boundary; it neither reruns nor suppresses the failed shutdown result. Cancellation/denial/TEMP-certificate interaction remains a hypothesis to qualify, not a confirmed fix.
2. `TestNativeObservedReviewDenyReusesSamePOSTCapacity` retains its prior1025-workload55.01s context-budget failure (`begin database operation: context deadline exceeded`) from integration’s `native-approval-publication-adapter.jsonl`. This task does not alter/repeat that workload or native capacity policy.
3. Actual acceptance/SQL/optional-history tests, four acceptance NativeInspection adapters, the full Runtime/failure-injection matrix (including the prior40 failures), exact final-source CI/load rebalance/R67, real startup and end-to-end acceptance remain Task7. No source-only or compile-only result here certifies those gates.
4. Task5 Minor B2 (receipt replacement helper omits cutoff though execute checks it) and redundant reference-DDL work remain final-triage inputs. Task6 explicitly compares cutoff and all immutable certificate coordinates; it does not assume the helper alone proves completion. Privacy retained metadata, old strict operational receipts and account-mode/native source facts remain intact.

First0.1.30 was already published and was not requalified, rebuilt or republished. This is second-release work only. No release readiness, deployment, or full-branch green claim is made.
