# Native Upstream Continuation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Execute the three cohesive units serially with behavioral TDD and independent review. Do not start while the acceptance-profile composition unit is still mutable or awaiting its review.

**Prerequisite update after335556f review:** The isolated profile checkpoint is approved, but its required historical decision-retry repair (`2026-10-05-native-decision-history.md`) and fresh-GET Start→Verify diagnostic/correction take priority. Complete those scoped reviews before starting continuation Task1; this plan does not mask or waive either instability.

History is independently closed at2e5a870. Actual diagnostics now prove the pre-transport false-uncertainty mechanism for both fresh GET and approved tools/call. `2026-10-05-native-unsent-disposition.md` owns the adopted two-unit repair and remaining lifecycle coverage; finish its reviews before continuation. No gate is waived by this sequencing change.

**Current prerequisite checkpoint:** Both unsent-disposition units are now independently closed (e83cfac/9587798). The subsequent quota alignment and MODEL SSE units are independently closed (e60dfce/e51330b), without implying an installed release or a fix for activity_start timeouts. `2026-10-05-native-claim-reclamation.md` is the active serial predecessor: Task1 is independently approved at8e078c4; Task2 still owns the sole source writer and must finish its scoped fault/race/exact-client diagnostics and independent review before this plan starts. Its new retirement return contract exposes only exact previously issued receipts verified physically terminal by an actual successful retirement transaction; empty/no-op/error results are not proof. Once that checkpoint is independently approved, carry its exact contract and cleanup-only preservation forward rather than reviving older error-only assumptions.

**Goal:** Keep an eligible native tools/call response alive across a broken upstream stream, including on pinned Codex 0.145, without a second tool execution or misleading request provenance.

**Architecture:** The original downstream response owns a bounded sequence of genuine upstream continuation GETs. Every GET has fresh request/admission/Offline/Store authority and its own physical audit; the original logical operation and exact callback parent remain unchanged. Persist immutable, body-free initiator evidence, never durable stream authority.

**Tech Stack:** Go 1.26.8, existing CaptureAssignments/Runtime/SQLite/native Store, strict Dart reader DTOs, pinned Codex 0.145.0/0.159.2/0.160.0.

**Spec:** `docs/superpowers/specs/2026-10-03-production-readiness-design.md`, `2026-10-04-native-mcp-transport.md` Task 4, and the unresolved Task 3 in `2026-10-04-native-stream-recovery.md`. This is a source-backed correction within the approved native support and safe data-upgrade goal, not expanded real-account access.

## Evidence and adopted scope

Root fully read the 92-line initial proposal and final 88-line interface refinement, then checked actual RequestLease/BeginRequest, Admission/Authorizer, native Receive/BindRecoveryGET/Start/audit, strict schema admission, retention and explicit history deletion. Source baseline is `7558ddfae2b93052f65b599ed7dd1543c3168ae1`; the intervening acceptance-profile unit changes composition/fixtures only and must be reviewed before execution here.

Actual clean-source145 diagnostic 10349 fails after a bounded15s: tools/call POST1, only the fresh listening GET1, no original-stream recovery GET. Its pinned rmcp1.8 POST stream lacks the recovery path present in rmcp3.2 used by159.2/160. Observe-only newer-client diagnostics do not prove Review/Full or all46 acceptance.

Adopt one general upstream-continuation path for all pins, not a self-reported-version branch. Keep downstream open; do not pretend145 can recover a broken downstream connection. Do not automatically reconnect unrelated fresh listening GETs. A real client triggering this path is not a fabricated client GET: reports must identify the actual proxy initiator.

## Global constraints

- One source implementer in the existing isolated production-readiness worktree. No original dirty workspace, real App/daemon/login/trust/DNS/database mutations. Use private synthetic data and existing contained exact-client harness.
- `DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly`; normal/race/structural checks, actual failed assertions and same-source evidence are required. No compile error substituted for behavioral RED.
- Per-profile default OFF, original login for native tools, managed model credential separation, own ToolDecision and one original tools/call send remain unchanged. No original approval/receipt reuse and no automatic POST retry or401 account fallback.
- Recovery never extends the original absolute stream deadline, restores capabilities from SQL, or grants authority from an operation ID, initiator field, connection string or current-looking DTO.
- Source provenance uses an independent private clean checkout with a real `.git` directory; verify actual VCS revision/modified and artifact hash. The complete46-case matrix, real model cancellation, production classification, CA/soak/final-source/release gates remain required.
- Close the remaining live-registration drop audit before approving Task2/Task3. Claim-reclamation diagnostic14231 proves only the two CloseSession/Revoke drop decisions; it does not prove the adjacent `OpenObservedStream` expired-logical-terminal pruning or `BindRecoveryGET` ErrUnavailable drops safe. Fixed8e source inspection shows those candidates may discard an original receipt/child cleanup owner without physical-terminal verification. For the former, use a genuine logically completed/open-physical original, advance the supplied expiry boundary and make a distinct real Open; for the latter, exercise both authority-read and durable-Bind rollback/commit-error with actual original/child receipts. Require existing cleanup ownership until an actual verified physical closing event, immutable prior audits, old receipt/send denial and no repeated POST. Record each first outcome and either the concrete safe predicate or a reviewed minimal TDD correction within the existing I3 lifecycle. These are unexecuted candidates, not claimed defects or permission for blanket drop/prune rewrites; do not silently omit them from the whole-goal review.

## Task 1: Fresh native continuation leases and read-only current admission

**Files:** `internal/captureassignment/manager.go`, `manager_test.go`; new `internal/captureadmission/current.go`; new `internal/runtimepersistence/capture_admission_current.go` / `_test.go`; narrow real-handler tests if needed. Runtime/module composition is deferred to Task3, so this unit grants no automatic continuation behavior.

**Interfaces:**

```go
func (lease *RequestLease) ConnectionID() string
func (lease *RequestLease) BeginNativeContinuation(ctx context.Context) (*RequestLease, error)
func (lease *RequestLease) RecheckNativeContinuation(ctx context.Context) error

// In captureadmission. This is current-state validation, not authentication.
type CurrentChecker interface {
    CheckCurrent(context.Context, Admission) error
}
// On the real runtimepersistence.Store.
func (store *Store) CaptureAdmissionCurrent() captureadmission.CurrentChecker
```

- [ ] **RED:** Exercise real manager/Store behavior while an original native POST lease remains active. Prove a distinct fresh GET lease is needed, and that frozen Admission/activity alone does not notice a manual credential revision rotated during an Offline wait. New fail-closed declarations may be scaffolding; only the observed missing lease/revision denial is RED evidence. No real network is needed for this authority unit.
- [ ] **Implement the narrow lease:** Store private manager/capture/connection-record identity, original request facts and released state when BeginRequest mints a lease. BeginNativeContinuation accepts only an unreleased original native POST lease on that exact registered record, derives body-free GET facts from its frozen route/transport, and obtains a distinct active lease through the existing manager path. Reject same-ID re-registration, shutdown/poison/transition, changed assignment/environment/route or original release; release any newly acquired lease on mismatch. No connection registration or caller-selected target. The executor/Store separately restrict the originating RPC to tools/call.
- [ ] **Recheck without renewing:** A child retains private continuation-only identity and original reference. Recheck rejects ordinary, foreign or released leases and lost original/connection authority. Do not call BeginRequest again just to recheck and do not let child leases mint further children. ConnectionID exposes attribution only. Serialize release/identity checks with existing capture state; prevent lock inversion or active-request leaks during close/apply races.
- [ ] **Current admission:** The Store adapter validates the actual Admission structure and existing capture/owner/expiry predicates. Manual admission requires the current exact credential revision; managed admission requires revision1 and active capture-run identity. No AuthorizeProxy call, raw capability/hash access, timestamp mutation or credential refresh. This read-only check does not replace the same transaction-level checks in Task2.
- [ ] **GREEN and review:** Test valid distinct leases, original remains active, child release counts, rotate/apply/OFF/close/expiry/owner/shutdown, same-ID replacement, parent release racing Begin/Recheck, and unchanged observation/updated_at fields. Run affected normal/race tests and repository checks; self-review, owned commit, drained handles and independent authority review.

## Task 2: Atomic request-origin evidence with positive send predicates

**Files:** `internal/nativetool/repository.go` (ReceiveInput/Operation), `stream_authority.go`; `internal/runtimepersistence/{native_tool_repository,native_tool_projection,native_tool_execution,native_stream}.go`; new `native_proxy_continuation.go`; all four SQL owners `schema.sql`, `schema/v2.sql`, `native_schema.sql`, `migrations/001_to_002.sql`; existing schema/stream/migration/retention/Capture deletion tests; `internal/nativemcp/executor.go` for actual client-origin producer; `internal/desktopcontrol/native_tool_views.go` and tests; `ui/flutter_app/lib/core/api/native_tool_models.dart`, `ui/flutter_app/test/native_tool_reader_test.dart` for strict model tests.

**Interfaces:**

```go
type RequestInitiator string
const (
    RequestInitiatorClient RequestInitiator = "client"
    RequestInitiatorProxyStreamContinuation RequestInitiator = "proxy_stream_continuation"
)
type RequestOrigin struct {
    Initiator RequestInitiator
    IngressConnectionID string
    CredentialRevision uint64
    OriginalOperationID *ID
}
// Add Origin *RequestOrigin to ReceiveInput. Ordinary Receive requires client origin.
// Add Origin *RequestOrigin to Operation; nil is diagnostic unknown only.
// Add to StreamRepository; revision is from the original authenticated Admission.
ReceiveProxyContinuationGET(context.Context, ReceiveInput, Binding, StreamAuthority, uint64, time.Time) (ReceiveResult, error)
```

- [ ] **RED:** Use real Store/once/audit paths to show that an originless validated GET currently can enter ordinary Start. Desired behavior denies without once/audit/send. Test insertion/known commit/rollback behavior, not merely absence of a new table or method.
- [ ] **Schema:** Add STRICT `native_tool_request_origins`: operation_id primary-key ownership FK to native_tool_operations ON DELETE CASCADE; closed initiator; nonempty ingress ID at most512bytes; credential revision1..MaxInt64; nullable checked32hex original ID, nonself and present iff proxy. The original ID is immutable historical evidence, with NO parent FK. Proxy insert guard requires a present client-origin POST/tools_call original and GET/get_stream child with equal Capture/Environment/actual ingress/credential revision. Client rows have no original. Forbid updates and direct origin deletion while its owning operation survives; permit its operation's cascade. No credentials, body, cursor or wire session ID.
- [ ] **Real producer and atomic receive:** Replace synthetic `native:<sessionID>` connection attribution for new operations with the actual minted RequestLease.ConnectionID; populate client origin from the actual Admission. Ordinary Receive rejects proxy origin. New ReceiveProxyContinuationGET requires input.Origin=nil, resolves the original only from live same-Store StreamAuthority, checks exact scope/session/owner/assignment/route, new GET ID/no RPC digests/no callback parent and current revision. Extract narrow transaction helpers from Receive/Bind: insert received event, origin, binding and validated event in one transaction. Never accept a caller's original ID as authority. Reserve bounded private recovery state before binding; publish only on known commit. Unknown commit fences exact child/stream cleanup-only and retains reconciliation; no orphan ordinary-GET fallback.
- [ ] **Positive authority at every send:** Start and VerifyStartReceipt require a valid existing origin for EVERY kind plus current admission revision and matching actual ingress. Missing/unknown/corrupt evidence denies. Explicit client-origin ordinary GET has nil original; client recovery keeps its existing association checks. Proxy GET additionally requires its live private recovery association/StreamAuthority and CURRENT original operation/origin with the exact relation. Restart, original deletion, rotation and absent private state deny even when historical rows remain. Existing Store issuer/once/approval/Offline/send gates remain; no extra original ToolDecision consumption.
- [ ] **Callback compatibility:** Ordinary callback replies can arrive on another genuine connection within the same session. Their client origin uses their own lease/Admission; do not require callback connection or credential revision to equal the parent. Existing sealed observed-parent scope/session/owner/assignment/route checks remain. Only proxy continuation requires original ingress equality. Test original on connectionA and callback on connectionB with distinct truthful audit IDs.
- [ ] **Read projection:** Operation.Origin carries a copied RequestOrigin (or nil). NativeOperationDTO adds `Initiator string`, `OriginalOperationID *string`, `IngressConnectionID *string` with JSON names `initiator`, `originalOperationId`, `ingressConnectionId`; credential revision stays internal. Missing origin projects `unknown`/nil/nil, never inferred client or send authority; malformed present origin fails. A valid historical proxy origin still exposes its immutable original ID after the original expires. Scope-filtered original lookup may return404; never extend retention or reconstruct authority. Dart accepts older-server absent fields as unknown and validates closed values/nonself32hex/proxy-original pairing. Keep technical provenance out of default user copy.
- [ ] **Preserve supported data upgrades:** Extend only this unpublished v2 and its v1→v2 delta, not frozen v1 or the version number. Root verified frozen v1 SHA256 `393b7d67f8ffd1952fc516a150e1a2a68707b67536fbc2e7f5a7f17208424cb1`; current pre-change v2/bootstrap SHA256 `2a915c10425b0a848813d4e8d7fd3f03a0614d4cfd69fecbab3985b074fb2590`, delta `d874261cdd9731c8dea84407ebe0d4a85bc0cebe30ac7d63bf78302804431b52`. Retain a test-only frozen7558ddf v2 fixture. Inspect/Open/Runtime must refuse this older private-v2 layout unchanged (DB/WAL/SHM/receipt, no activation), not reset, relabel or silently import originless rows. No legitimate originless native data exists on fresh-final-v2/frozen-v1-upgrade paths. Non-test earlier-v2 data would require a separate exact additive migration; do not touch it here.
- [ ] **Migration and retention tests:** Keep snapshot/guards/BEGIN IMMEDIATE/integrity/typed legacy fingerprint/reconciliation; prove complete populated-v1 preservation, secrets references, opt-outs, sequences, initialized_at and full restore. Test fresh/delta SQL parity, constraints, corruption, DDL/bind/commit/sync/reopen faults and new table empty after upgrade. Origins cascade only from their own operation: existing bounded retention and explicit Capture/archive deletion keep their semantics and need no ordering or new locking framework. Test LIMIT1 original expiry with a later-expiring terminal child retaining historical ID, child deletion only removing its own origin, unrelated Capture untouched, original lookup404, rollback/crash outcomes, and original loss/restart causing zero sends. Harmless terminal replay registrations must not become new history-clearing blockers.
- [ ] **GREEN and independent review:** Run affected domain/Store/module/control normal and focused race tests, schema/migration/fault suites, strict Dart model tests and repository checks. Missing origin before Start yields zero new once/audit; deleting/corrupting origin after Start denies Verify. Record exact fingerprints, source, behavioral RED/GREEN and remaining limits; owned commit and independent data/authority/API review before Task3.

The no-parent-FK choice deliberately follows existing callback history: original details may disappear before child metadata. Atomic insertion and immutable facts prove historical origin; live authority gates, not retention coupling, authorize sends. Reject the FK RESTRICT alternative because it needlessly changes history-clearing/lifecycle semantics and requires broad child-ordering machinery.

## Task 3: Bounded upstream attempt loop and exact-client evidence

**Files:** `internal/nativemcp/{executor,stream,response,transport}.go`, new private `continuation.go` and tests; add `AdmissionCurrent captureadmission.CurrentChecker` to existing Dependencies and wire the real Store adapter in `internal/productruntime/runtime.go`; real handler/Runtime tests; `cmd/vibermate-acceptance/native_mcp*.go` fixture/counter/audit consumers. Keep NewCapturedRequest's public interface and one-shot rule; do not recursively call Serve.

**Consumes:** Task1 minted leases/current checker; Task2 ReceiveProxyContinuationGET; existing BindRecoveryGET, Offline action/lease, Start/Verify receipts, FinishAttempt/CompleteStreamRPC and reviewed cleanup-only lifecycle.

- [ ] **RED:** Real handler/Runtime/Store, original POST emits complete e1 then upstream breaks while downstream stays open. Require a genuine upstream GET with exact cursor and one original tools/call; current code closes the response instead. Preserve the recorded official145 failure as client-level evidence, not a substitute for this controlled behavioral test.
- [ ] **Minimal loop:** Extract a small physical-attempt helper in the current stream owner, not another Serve or generic retry engine. Keep one logical slot/stream/exclusive attachment/original lease/downstream response; separate the current physical body/receipt/counts. Close and FinishAttempt on the old physical request before delay/new GET. Only unresolved tools/call plus a complete accepted identifiable event boundary qualifies. No partial/no-ID/unidentifiable/protocol/budget-error fallback and no fresh-listener reconnect.
- [ ] **Bounded authority and credentials:** At most3 continuation GETs per original RPC; delays100ms/400ms/1s; at most10s total continuation window, all clamped to original effective expiry. Offline waiting/backoff counts; no reset on attempts. Before Begin, after Offline wait and immediately before Verify/send, check current Admission and the fresh continuation lease. SQL Start/Verify also checks exact revision. These checks are point-in-time, not a claim of atomic network/SQL revocation.
- [ ] **Exact request and retention:** Retain only a budget-charged allowlist of original Authorization/Cookie and required MCP protocol/session headers for the active response lifetime, clear on exit/owner/deadline. Construct exact frozen HTTPS GET, Accept:text/event-stream, last fully accepted same-stream cursor, Body nil/NoBody and ContentLength0; no POST/content/transfer/hop/proxy headers. Each GET gets its own ID/origin/once/receipt/audit and actual bytes. Original byte fields remain initial-attempt-only (including NULL), correlated result/content/callback parent stays original; no account-pool/SQL/environment credential reconstruction.
- [ ] **Stop and race boundaries:** Terminal result stops;404/401/403/redirect/wrong MIME/session/ID/correlation/expiry/OFF/stop/apply/owner loss/cancel/unknown commit never trigger another GET. Budget exhaustion produces no fake result; retain only existing valid detached recovery state. Downstream Write/Flush failure immediately stops proxy attempts and releases to existing client-owned recovery. Preserve modern-client exclusive attachment, terminal local replay, exact event order/dedup and I3 joint child-receipt cleanup in both orders. Release headers/timers/leases/ledger charges on every exit.
- [ ] **GREEN with real artifacts:** After unit/handler/Store/race/structural checks, run exact verified145/159.2/160 through the contained launcher/runtime. For each upstream-break case separately count genuine fresh listening GET, client recovery GET and proxy continuation GET; require original tools/call POST1 and each approved reply once with actual SQL/API approval/once/provenance/outcome/byte evidence.145 expected client-recovery GET0/proxy GET1; do not fabricate a client GET or claim downstream-break support.159/160 also need a separate genuine downstream-break regression to preserve their client-driven recovery. Use known synthetic classification for ordinary Review and existing declared form/URL previews for approved callbacks; Observe/ping-only diagnostics cannot stand in for Review. No new ping preview family is approved.
- [ ] **Independent checkpoint review:** Self-review, clean owned commit, exact artifact/report hashes, all handles drained, then independent complete-unit review. Task3 completion still depends on its actual approved-reply/client requirements; no full46/production-ready claim follows automatically. Return to every remaining matrix and final gate.

## Verification commands and handoff

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly go test -count=1 ./internal/captureassignment ./internal/captureadmission ./internal/nativetool ./internal/runtimepersistence ./internal/nativemcp ./internal/loopbackproxy ./internal/productruntime ./internal/desktopcontrol
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly go run ./cmd/repositorycheck
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly go test -count=1 ./internal/repositorycheck
git diff --check
```

Run each unit's named RED alone, then GREEN and relevant race selections; record timeouts/failures, do not convert zero-selected tests or missing-client skips into passes. Tagged native synthetic evidence and untagged packaged V7 remain separate. No source implementation is implied by adoption of this plan. The current claim-reclamation Task2 retains the sole writer until its scoped gates and independent review complete; no concurrent continuation implementation is authorized by this prerequisite update.

Self-review: three independently rejectable units; exact interfaces and consumers match current authority ownership; historical provenance cannot issue capabilities; all existing deletion/data-retention semantics remain; migration admits only exact supported layouts; live credential checks do not retain capabilities; all46 and downstream gates remain intact. Quota layout is separately fixed/reviewed but unpublished; activity_start timeout remains an unresolved investigation and is not claimed fixed here. Remaining live-drop candidates have explicit real-fault coverage obligations, without treating source hypotheses as execution results or reopening unrelated permanent-disposal semantics.
