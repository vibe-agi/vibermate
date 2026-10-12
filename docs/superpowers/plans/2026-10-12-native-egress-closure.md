# Native Egress Closure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Root assigns one source writer and owns independent review, staging and commits. This document authorizes planning only; each new production edit requires a contained behavioral RED and root's narrow ruling.

**Goal:** Close the remaining Phase B3 native-egress evidence and actual Runtime Hold gap for existing approved native calls, parent-linked replies and client-driven recovery GETs.

**Architecture:** Keep native capture admission, per-operation V2 binding, current Store authority, ToolDecision, once/receipt and the real sender intact. Reproduce the native probe producer/Runtime consumer contradiction with a genuine approved operation before proposing an origin-only reachability projection of that same frozen binding. Exercise missing physical selection and failure cells using private strict TLS/SOCKS and actual Store evidence, with independent operations separated from linked children.

**Tech Stack:** Existing Go/SQLite/HTTP/SOCKS/TLS only. Execution toolchain `/tmp/vibermate-go-security.JcRyfL/go/bin/go` (Go 1.26.9), `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly`; do not change go.mod/go.sum.

**Spec:** `docs/superpowers/specs/2026-10-05-global-default-egress-design.md`; parent `docs/superpowers/plans/2026-10-05-global-default-egress.md` Tasks 3 and 6. Scope clarification: `.superpowers/sdd/2026-10-05-global-default-egress/phase-b-remaining-audit.md` native row and latest closure notes, and `.superpowers/sdd/2026-10-11-stable-production-integration/phase-b-separation-audit.md`.

## Global constraints

- Planning product-source pin: `fc82acf0a81d21ecb0abac402ce7315a3ae8eaa9`. All planning source/test reads use `git show`/`git grep` at that pin. No `.codegraph/` exists in this linked worktree. Ignored SDD reports are retained disk evidence with their own source ranges, not Git objects at this pin.
- Execution order remains B1 captured/original → B2 managed tail → B3 this plan → B4 blind/cleartext → B5 candidate/release. Reconcile this plan against the then-approved execution HEAD before starting; do not read or modify a concurrent writer's WIP as source evidence.
- “原生工具权限仍每 profile 默认关闭”; use explicit authorization only in temporary fixtures. No real profile permission toggles, user Store, App/9666, system trust/proxy, accounts, public network or provider activity.
- “网络选择只决定怎样连接已有合法目标，不能授予目标访问、工具执行、账号使用或证书信任权限。” Original-client credentials remain original-client credentials; native has FP/FD/G/B, no artificial managed-account layer.
- “代理不可用、DNS/TLS 失败、取消、配置读取失败，都不能触发直连或换代理重试。” No automatic POST replay or fallback through another proxy. Client-driven recovery GET remains the existing mechanism, with its own authority and physical audit.
- “全局默认不是运行中连接的动态变量。” Freeze before Hold, probe, Start/audit and send. A new independent operation selects anew; a linked child takes its actual parent's frozen plan.
- No generic prober relaxation, public/production fixture hook, dependency, trust or permission expansion is preselected. Do not copy a captured/owned producer branch without proving the native binding/authority chain below.
- One test process at a time. Preserve initial failures; compile/setup failures are not behavioral RED. All fixture barriers, contexts, sockets, consumers and cleanup must be bounded and joined before a result is reported.
- Task3 G proof is carried from `41b76df9eb4f1b1f4fd07f1374137309e75a7706`; its I1 active/retained SSE charge defect was fixed at `543b1b4395d15d3f82d8d15e098a56ee64cf9985` and independently approved in `task-3-fix-1-review.md`. Do not reopen I1 or repeat broad budgets/migrations/backups/full46 to prepare or execute B3.
- Phase C upstream-continuation/client-matrix/CA/soak work and Phase B5 candidate data/install/release gates remain separate. This plan proves neither full MCP compatibility nor production fingerprint transport/installed-client behavior.

## Pinned facts and the bounded outcomes

`internal/nativemcp/executor.go:766` chooses an independent exit with `Resolve(ctx, Inherit(), plan.Route().EgressProfile)`; actual callback/stream children take the retained binding without consulting defaults. `:406` replaces the working plan with `binding.Plan()`. `:494–545` performs the genuine Bind/BindObservedReply/BindRecoveryGET and ToolDecision. `:556` queues the probe, then `:569` calls Store.Start. Thus an allowed decision waiting in Hold is **not yet consumed**.

`internal/runtimepersistence/native_tool_execution.go:176–253` checks current native authority/recovery/approval, atomically consumes required Allow, inserts once and the actual audit, and issues the sealed receipt. `internal/nativemcp/transport.go:120–169` claims once, checks exact target, calls the real `VerifyStartReceipt`, then performs one `RoundTrip` using `plan.ResolvedEgress().Profile().Policy`. This authority chain must survive the correction unchanged.

The contradiction is local: `nativeProbe` at `transport.go:92–99` emits Auxiliary/strictTLS with route ID and Environment revision/digest. Runtime `offline.go:60` sends Auxiliary to original TLS. `originaltransport/probe.go:117–135` requires zero plan and `TargetRef == canonical NetworkOrigin`. No native behavioral RED has been run as part of planning.

Two review units are sufficient: (1) actual Runtime native Hold with its demonstrated producer correction; (2) remaining native physical selection and consumed failure evidence. Task 2 may pass existing production unchanged. No task per matrix cell.

## File and interface map

| File | Responsibility / ownership |
| --- | --- |
| Create `internal/productruntime/native_egress_fixture_test.go` | Private Runtime/native fixture: genuine Review, Store, admission/assignment, narrow native TLS/SOCKS wire, barriers, expected audits and cleanup. No shared captured/managed fixture edits. |
| Create `internal/productruntime/native_egress_recovery_test.go` | Task 1 actual Hold/default-edit/prober, cancellation, failed-probe and authority-negative tests. |
| Create `internal/nativemcp/native_probe_test.go` | Task 1 narrow producer projection/invalid-plan checks using genuine frozen plans from existing native fixtures. |
| Conditional modify `internal/nativemcp/transport.go:nativeProbe` | Only after contained RED and root ruling; project the already-authorized binding's reachability target. |
| Create `internal/productruntime/native_egress_matrix_test.go` | Task 2 FP/FD/B call/reply/recovery and post-Start tool/child failures. Extend only the new native fixture as needed. |

Read-only reference fixtures: `internal/nativemcp/{executor_test,egress_network_test,egress_freeze_test,native_send_test}.go`, and `internal/productruntime/native_mcp_test.go`. The existing `nativeClosedNetwork` denies all Direct dials and therefore cannot prove FD/B success unchanged. The existing native SOCKS fixture is the exact native protocol reference; add an explicit closed Direct mapping in the new fixture, never an ambient `net.Dial` fallback.

Existing interfaces, verified at the pin:

```go
// productruntime private composition; existing fields, not proposed additions.
startWithBuilders(context.Context, Options, runtimeBuildOverrides) (*Runtime, error)
// Options.NetworkOptions egressnetwork.BuilderOptions
// Options.OriginalTransport originaltransport.Transport
// runtimeBuildOverrides.originalProber func() (*originaltransport.TLSProber, error)
originaltransport.NewTLSProberWithBuilder(*egressnetwork.Builder, *x509.CertPool) (*originaltransport.TLSProber, error)
// originaltransport.Transport:
RoundTrip(*http.Request, egressnetwork.Policy) (*http.Response, error)

// Runtime's real native executor is r.nativeTools in package productruntime.
nativemcp.NewCapturedRequest(*captureassignment.RequestLease, captureadmission.Admission, *http.Request, nativetool.ID) (nativemcp.CapturedRequest, error)
// Executor:
Serve(context.Context, nativemcp.CapturedRequest, http.ResponseWriter) nativemcp.Terminal
Revoke(context.Context, captureidentity.Reference, nativemcp.RevocationReason) error

// Repository:
DecideApproval(context.Context, nativetool.DecisionInput) (nativetool.Approval, error)
Start(context.Context, nativetool.ID, string, uint64, time.Time) (nativetool.StartReceipt, error)
VerifyStartReceipt(context.Context, nativetool.StartReceipt, time.Time) error
// A receipt exposes OperationID()/AttemptID(), not a public issuer or reconstruction API.
```

## Task 1: Native Runtime Hold closes through the existing concrete TLS prober

**Files:** create the fixture, recovery tests and native producer tests above; conditional edit only `nativeProbe`. `productruntime/{runtime,offline}.go`, Store, executor, schema and generic prober are reference-only.

**Consumes:** Actual Runtime construction and native fixture sequences in `native_mcp_test.go`; actual protocol/Review/recovery frames from `nativemcp/egress_network_test.go`; real `OfflineHoldSnapshot`, `EnterOfflineHold`, `ResumeOfflineHold`, `EgressDefaults`, `NativeToolRepository`, capture assignment and native executor.

**Produces:** A private fixture reused by Task 2 and source-bound proof that approved native Hold, default edit and current concrete probe preserve the actual frozen exit and authority.

- [ ] **Step 1: Add the contained native Runtime fixture, without changing production.** Define these test-only types/methods in `native_egress_fixture_test.go`:

```go
type runtimeNativeEgressConfig struct { choice, edge string }
type runtimeNativeExpected struct {
    ProfileID string
    ProfileRevision uint64
    Source egressprofile.SelectionSource
    DefaultRevision uint64
}
type runtimeNativeCall struct {
    ID nativetool.ID
    Captured nativemcp.CapturedRequest
    Done <-chan nativemcp.Terminal
    Cancel context.CancelFunc
}
// runtimeNativeEgressFixture owns r *Runtime, gate *offlinehold.Gate,
// admission, capture, registered connection, private roots/network/listeners,
// per-operation expected/start snapshots, request records and barriers.
func newRuntimeNativeEgressFixture(t *testing.T, cfg runtimeNativeEgressConfig) *runtimeNativeEgressFixture
func (f *runtimeNativeEgressFixture) handshake(t *testing.T)
func (f *runtimeNativeEgressFixture) begin(t *testing.T, method, body, cursor string) runtimeNativeCall
func (f *runtimeNativeEgressFixture) allow(t *testing.T, id nativetool.ID)
func (f *runtimeNativeEgressFixture) waitQueued(t *testing.T) offlinehold.ProbeTarget
func (f *runtimeNativeEgressFixture) setDefault(t *testing.T, profile *egressprofile.ProfileRef) uint64
func (f *runtimeNativeEgressFixture) join(t *testing.T, call runtimeNativeCall) nativemcp.Terminal
func (f *runtimeNativeEgressFixture) assertAudit(t *testing.T, id nativetool.ID, want runtimeNativeExpected, outcome, errorClass string)
func (f *runtimeNativeEgressFixture) assertNoStart(t *testing.T, id nativetool.ID)
func (f *runtimeNativeEgressFixture) assertDrained(t *testing.T)
```

Implement construction in the same order as `TestNativeRuntimeContentProfileComposition`: real Gate + `testOptions`, real `startWithBuilders`, SaveDraft/Preview/Publish a native Environment with `ToolPolicyReview` and original credentials, real manual capture/authorizer/credential, assignment Create and registered `https://native.example` connection. Use generated operation IDs distinct from handshake IDs; `begin` performs real BeginRequest and NewCapturedRequest and launches `r.nativeTools.Serve` with a cancelable context and buffered result channel. If captured construction fails, release the real lease. Never create binding/receipt/Allow rows by SQL.

Publish profiles A/B through `r.EgressProfiles().Publish` before publishing the chosen Environment. Use exact native HTTPS origin and `/backend-api/ps/mcp` (`environment.ClientToolMCPPath`), catalog `read_file`, session `synthetic-session`, original Authorization `Bearer synthetic-original-login`, and a synthetic Cookie. Strictly reject unknown method/path/host/query/SNI/destination; check original headers and exact RPC bytes at the TLS endpoint, and absence of proxy credentials/account substitution. `handshake` sends initialize, notifications/initialized and tools/list over the actual chosen wire; these three sends are explicit baseline counters.

The fixture's transport must call the existing `egressnetwork.Builder.Dialer(policy)` with its **received** frozen policy, then an `http.Transport` with `Proxy:nil`, `DisableKeepAlives:true`, TLS >= 1.2 and only the private root pool; never `InsecureSkipVerify`. Set existing trusted `Options.NetworkOptions` to the same closed dialer/resolver so every Runtime prober is contained. Supply the existing `OriginalTransport` seam and `originalProber` factory; the latter returns the concrete `NewTLSProberWithBuilder(builder, roots)`. No replacement of Runtime's dispatcher, Gate or successful probe double.

Permit only A/B listener endpoints and the exact native target, whose synthetic `127.0.0.1:443` resolution maps to one owned TLS listener. Track Direct attempts separately from successful Direct connections, A/B SOCKS attempts separately from accepted CONNECTs, TLS handshakes separately from HTTP requests, unexpected destinations/DNS and credential violations. SOCKS CONNECT accepts only the native target mapping and joins both copy directions. No public DNS; no provider operation in this fixture. An unexpected destination fails immediately before dialing.

`allow` reads the real pending Operation and Approval, then calls `DecideApproval` with exact approval revision, `DecisionAllowOnce`, unique idempotency key and current UTC time. Wait for a real pending question, using a bounded observation context; never sleep to select a race winner. Record immutable expected selection **before** beginning each operation, independent of the eventual probe/audit. In a transport-entry channel barrier, query the actual initial audit before releasing network I/O; this point is after real Start/Verify, not fabricated audit insertion.

- [ ] **Step 2: Write the actual authority/Runtime RED first.** Add `TestRuntimeNativeEgressHoldUsesFrozenConcreteProbe` with the following causal sequence:

```go
f := newRuntimeNativeEgressFixture(t, runtimeNativeEgressConfig{choice: "global", edge: "hold"})
f.handshake(t) // A, setting revision 2; three physical requests.
if _, err := f.r.EnterOfflineHold(t.Context(), f.r.OfflineHoldSnapshot().Revision); err != nil { t.Fatal(err) }
call := f.begin(t, "POST", `{"jsonrpc":"2.0","id":44,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "")
f.allow(t, call.ID)
target := f.waitQueued(t)
f.assertNoStart(t, call.ID) // approval exists/allowed, ConsumedAt nil; once/audit 0.
// Set B through the real default manager, expected revision 2 -> 3.
// Use B's actual published ProfileRef, retained by the fixture.
```

Observe the actual queued target; do not substitute a hand-constructed target or call TLSProber directly as the principal proof. Invoke `f.r.ResumeOfflineHold(ctx, f.r.OfflineHoldSnapshot().Revision)` after the real queue observation and default edit: enqueue changes the Gate revision, so the earlier Enter snapshot is stale and would fail CAS before reaching the prober. Expected at the planning pin: Resume rejects the routed-plan native target before any probe socket; call remains queued, tool HTTP/once/consumed-Allow/audit remain zero. Cancel and join the call and all resources **before asserting the expected-success test failure**. Require the failure to come from actual prober rejection, not setup, malformed native route, missing approval, wrong session or deadline. Preserve the actual error and target fields and exact zero-send evidence. Do not weaken this into a unit test of `nativeProbe`.

- [ ] **Step 3: Root evaluates the RED and bounds the producer correction.** Present the real RED/drain evidence and the existing binding → Bind/current authority → Review → outbound exact target → Hold → Start/receipt → send chain. The candidate change is limited to reachability projection in `nativeProbe`:

```go
// The caller supplies binding.Plan(), already frozen for this exact operation.
// Probe identity describes reachability; native authority stays on the binding.
route := plan.Route()
return offlinehold.ProbeTarget{
    Kind: offlinehold.EgressAuxiliary,
    Transport: offlinehold.ProbeTransportStrictTLS,
    TargetRef: route.TargetOrigin.String(),
    NetworkOrigin: route.TargetOrigin.String(),
    HTTPAuthority: route.TargetOrigin.HTTPAuthority(),
    TLSServerName: route.TargetOrigin.Host(),
    EgressPolicy: resolved.Profile().Policy,
    // PlanRevision 0, PlanDigest empty, TargetAccess 0.
}
```

Retain the existing unresolved-plan failure. Add `plan.Validate()` fail-closed checking only if the actual producer tests show that it is needed; do not expand production scope speculatively. Do not alter binding digest/version, Environment ID/revision/digest, native route ID, audit purpose/authority/RuleID, original request credentials/body, caller authority, Store Start/Verify, once/claim lifetime or parent association. This is not a declaration that native has no semantic Environment: only its TLS reachability target has no provider wire-plan identity. If the native authority proof does not support this exact projection, report the concrete conflict to root; do not relax the generic prober or introduce a second authority API.

- [ ] **Step 4: Prove the correction and narrow negative controls.** The same RED test must now see exactly one strict TLS-only probe via A and one approved tools/call HTTP via A after editing to B; no HTTP from probe, no B/Direct attempt. At wire entry assert one genuine once and one consumed Allow and a nonterminal audit with expected A/global/revision 2. After completion assert the same attempt's immutable identity/decision and terminal; then issue a separate approved tools/call with a new RPC ID and prove B/global/revision 3 and its own once/Allow/audit. Do not use next-lease inspection or a ping as this new approved-operation proof.

Add `TestRuntimeNativeEgressHoldFailuresBeforeStart` with only `probe_proxy_down`, `probe_wrong_name`, `cancel_queued` and `authority_revoked` cases, each in a fresh fixture. Queue/approve first; close selected proxy or select a wrong-name certificate at a barrier before actual Resume; caller cancellation and `r.nativeTools.Revoke(ctx, capture, nativemcp.RevocationOwnerChanged)` use their real paths. For wrong-name, the private TLS server's test-only `GetCertificate` selects between two immutable certificates through an atomic flag switched after successful handshake/queue; do not mutate an active TLS config or fail native initialization accidentally. Failed Resume never starts/sends the tool and stays held until explicit cancellation drains it. Revoke cancels the real operation; assert no pending target for it, no later send after releasing fixture barriers, no consumed Allow/once/audit. Keep original default/selection frozen. Do not label these consumed-Allow failures.

Add `TestNativeProbeRetainsFrozenPolicyAndOriginOnlyIdentity` and `TestNativeProbeRejectsUnresolvedPlan` in the native package using existing real fixture binding plans. Assert exact origin/authority/SNI, Auxiliary/strictTLS, plan 0/empty, access 0, and the original resolved policy; zero/unresolved plan yields invalid target. Retain generic prober wrong-kind/hostname/routed-plan negative contracts. A producer unit test is supplementary to the actual Runtime proof.

- [ ] **Step 5: Verify, drain, and submit the first review unit.** Use the commands below and record actual matched tests, first failure, GREEN, normal/race output, code hashes and fixture limits. Root reviews before staging/committing. This unit closes Hold only; Task 2 remains required for the native family.

## Task 2: Missing native exits and consumed failure effects

**Files:** create `internal/productruntime/native_egress_matrix_test.go`; extend only `native_egress_fixture_test.go` with the listed protocol modes/barriers. Production remains unchanged unless a separate contained RED demonstrates a specific defect and root approves its exact scope.

**Consumes:** Task 1 fixture, same real Runtime/Gate/Review/Store, immutable expectation/audit checks, and the accepted native protocol flows in `egress_network_test.go`. **Produces:** Physical FP/FD/B and linked-child proof plus post-Start failure accounting, without promoting prior G evidence into new full-client acceptance.

- [ ] **Step 1: Encode expectations before executing the fixture.** Add `TestRuntimeNativeEgressSelectionAndLinkedChildren` with three rows. A/B are distinct published SOCKS profiles, each revision 1; changing default is a real expected-revision write.

| Row | Authored native choice / initial default | Expected initial selection | After default changes to B | New independent approved call |
| --- | --- | --- | --- | --- |
| FP | Explicit A / B at setting 2 | A/1, flow, default 0 | Parent-linked reply and recovery retain A/flow/0 | A/flow/0 |
| FD | Explicit built-in Direct / B at setting 2 | profile.direct/1, flow, default 0 | Linked children stay Direct/flow/0 | Direct/flow/0 |
| B | Inherit / genuinely unset setting 1 | profile.direct/1, builtin, default 1 | Linked children retain Direct/builtin/1 after setting B at 2 | B/1, global, default 2 |

For FP/FD the post-parent edit must actually advance the default setting: set A then B as needed, with both writes counted, while never changing the authored native flow. The frozen explicit fact remains source flow/default 0. Do not mislabel an explicitly selected global Direct as builtin. G call/reply/recovery remains carried from the accepted prior test; its new Hold cell is Task 1.

In code use literal expected tuples per row rather than deriving expected facts from the observed binding/audit/probe:

```go
cases := []struct {
    name string
    want runtimeNativeExpected
}{
    {"flow_proxy", runtimeNativeExpected{"profile.native-a", 1, egressprofile.SelectionFlow, 0}},
    {"flow_direct", runtimeNativeExpected{"profile.direct", 1, egressprofile.SelectionFlow, 0}},
    {"builtin", runtimeNativeExpected{"profile.direct", 1, egressprofile.SelectionBuiltin, 1}},
}
```

- [ ] **Step 2: Implement real independent call and linked branches in each row.** After the three-message handshake, create (a) an independent listening GET returning a server ping callback ID `native-parent-ping`, and (b) a genuinely approved tools/call ID 44 with body `{"jsonrpc":"2.0","id":44,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"native-token"}}}` returning SSE `id: native-progress` and a progress notification with token `native-token`, then EOF. Require the initiating tool terminal `failed/stream_detached`, durable logical operation still `started`, one consumed Allow and once, and its immutable failed physical audit. The parent callback GET and the tool stream are distinct parents, not a single fabricated relationship.

Edit defaults after both real parents exist. Send callback POST `{"jsonrpc":"2.0","id":"native-parent-ping","result":{}}` through Review/Allow and the existing registered callback witness; require `native_tool_bindings.parent_operation_id` to equal the actual callback GET operation. Send client-driven GET with exact `Last-Event-Id: native-progress`; require existing StreamAuthority association and a separate child once/audit. At this pin `BindRecoveryGET` keeps its exact association in the real Store's private recovery map, not a durable parent-operation column: prove the relation by the actual observed cursor, child route/binding selection, and successful separate completion of the exact initiating logical RPC. Do not infer or rebuild StreamAuthority from SQL. Its terminal SSE has ID `native-terminal` and JSON-RPC result for 44. Assert original logical operation becomes completed while its original physical failed audit is unchanged. Recovery GET has no new tool approval; it must not be counted as another consumed Allow.

Freeze initial and terminal per-operation audits at actual transport-entry barriers. There are eight native HTTP requests total: handshake 3 + callback parent GET 1 + initiating tool POST 1 + linked reply POST 1 + recovery GET 1 + independent next approved tool POST 1. The first seven use the original selected route; the last uses the row's new independent expectation. Separate expected counter deltas for each phase; do not count TLS-only probes as HTTP or erase the original detached POST from totals. A new approved tool call with ID 45 returns a complete JSON result rather than another detached stream, has its own once/Allow and observes the row's new independent expectation. Original Authorization/Cookie survive all native sends; route metadata is never replaced with managed-account identity.

Strengthen one builtin linked-recovery row with actual Runtime Hold: queue the genuine recovery GET after the parent/default edit, assert canonical probe keeps old Direct policy, resume through the concrete prober, then send exactly one child GET via Direct. This proves child Hold uses the retained parent fact, independently of Task 1's independent tool call. The later independent approved call uses B. Add one callback Hold case under FP with the same old-flow proof; do not multiply every selection by every failure.

Carry the already accepted `TestNativeRelatedChildrenKeepRealParentExitAcrossDefaultEditOrFailure` zero-resolver-read/unavailable-default proof; its narrower transport-double limit stays explicit, alongside the accepted G wire test's changed-and-unavailable-default proof. The new FP/FD/B physical observations extend that evidence. No temporary SQL default fault or new resolver injection is required for this plan, and no live authority is reconstructed to manufacture an observation.

- [ ] **Step 3: Add consumed-Allow/tool/child failure tests at actual transport entry.** Add `TestRuntimeNativeEgressConsumedFailureEffects`, with fresh fixtures for `tool_proxy_down`, `reply_proxy_down`, `recovery_proxy_down`, and `tool_accepted_then_dropped`. Use global A initially and edit to B only after the target operation's real Start/Verify has reached the test transport barrier. At that boundary snapshot the initial audit and once/Allow; then close A and release into the actual dial. For accepted-then-dropped, the private endpoint reads and records the full tool POST once, signals a barrier, then closes the real connection without a response; do not return a manufactured transport error as the network proof.

```go
// The barrier lives only inside the private test transport/endpoint.
select {
case <-release:
case <-request.Context().Done():
    return nil, request.Context().Err()
}
// Continue to the real builder-derived dial/strict TLS/HTTP operation.
```

Precompute effects, using deltas from successful handshake/parent setup:

| Failure boundary | New HTTP at endpoint | Required Allow consumed / once / audit for target | Required terminal behavior |
| --- | --- | --- | --- |
| Approved tool, selected proxy lost after Start | 0 | 1 / 1 / 1 | logical uncertain/execution_uncertain; audit failed/execution_uncertain with unobserved byte counts; no alternate send |
| Approved linked reply, selected parent proxy lost after Start | 0 | 1 / 1 / 1 | same conservative uncertainty; exact real parent retained in binding, no B/Direct send |
| Client recovery GET, selected parent proxy lost after Start | 0 | 0 / 1 / 1 | physical failed/transport_failed; `OutcomeFailed/ReasonStreamDetached` while stream remains live; initiating logical tool stays started until ordinary cleanup, no second POST |
| Approved tool accepted fully, response dropped | 1 POST | 1 / 1 / 1 | uncertainty/replay barrier; no second POST through A/B/Direct |

`sendClaimed` sets transport-entered before RoundTrip, so a proxy-dial failure after this point is not proven unsent by the executor. Do not change that safety classification to make the test expect zero known bytes. The recovery branch calls `FinishAttempt`, preserving its own physical terminal separately from the original logical RPC; inspect it before fixture Close invokes ordinary cleanup.

For every target query one `native_tool_once` row, join its `attempt_id` to the exact audit and verify parent operation equality; query actual Approval. Replay the same captured request after join and require zero extra transport entry/HTTP/once/audit. Attempt a real repeated `Store.Start` for that operation with the current real `GetScope` revision and a fresh attempt ID; require failure and no receipt or new audit. Do not reconstruct a receipt from SQL. Existing same-Store/foreign/expired/recovered receipt tests remain regression evidence for the sealed receipt boundary.

- [ ] **Step 4: Self-review and verify the complete native closure.** Confirm every new row has actual bytes/counters, expected facts assigned before execution, per-ID audit correlation, native credentials, genuine Review and separate linked authority. Missing evidence may pass immediately; do not introduce an artificial RED or edit production to create one. Root independently reviews the owned Task 2 diff and the combined B3 scope before root staging/commit and updating the native ledger.

## Audit assertions and deterministic cleanup shared by both tasks

For every started native operation, assert purpose `client_tool_service`, parent kind `client_operation` and exact operation ID, caller `core`, actual Environment policy ID/revision and `authority=environment`, actual native route `RuleID`, target `https://native.example`, exact profile ID/revision/source/default revision, no account identity/settings revision, and expected payload (`client_data` for tool/reply POST; `none` for GET). Keep initial `Attempt.ID`, `Parent`, `Decision`, `TargetOrigin`, `StartedAt` and sequence unchanged at terminal. Use `EgressAttempts().List` filtered by `ParentKind`, `ParentID`, `Purpose` and require exactly one; also match `native_tool_once.attempt_id`. Read-only SQL is allowed for exact native binding/once joins, never for granting authority.

```go
page, err := r.EgressAttempts().List(ctx, egressaudit.PageRequest{
    Limit: 2, ParentKind: egressaudit.ParentClientOperation,
    ParentID: id.String(), Purpose: egressaudit.PurposeClientToolService,
})
if err != nil || len(page.Items) != 1 { t.Fatalf("native audit: %v %#v", err, page) }
// Snapshot at the transport-entry barrier, compare exact immutable values
// against this same record after physical terminal; compare expected tuple too.
```

Waiting-before-Start failures assert zero outbound audit/once and `ConsumedAt == nil`; retain the actual native operation/approval closure as observed rather than demanding a nonexistent outbound failure audit. Inherited default-read rejection may occur even earlier; it cannot be relabeled a consumed decision. No native probe audit row is expected merely for a TLS reachability check.

Fixture barriers must observe actual queue/approval/transport/server-read events. Every barrier selects its context; every launch has one cancel and buffered terminal result. Cleanup first cancels operations and releases barriers idempotently, then closes owned connections/listeners to interrupt blocked copy/handshake/body work, joins all Serve/probe/copy/handler goroutines within explicit fixture deadlines, and shuts down Runtime/SQL after relevant callbacks return. Assert Gate `ActiveActions`, `EnteringActions`, `ActiveEgress`, `QueuedRequests`, `HeldBytes` all zero and empty pending targets after cancellation/success; no late HTTP on later gate release. Do not use a canceled context for required cleanup writes. Record goroutine/copy completion signals rather than assuming listener Close drained accepted sockets. Bound observation/join at 10 seconds per phase, cleanup at the existing Runtime shutdown bound; these are test harness safeguards, not native timeout changes. Do not extend native Header/idle/tool/decision/probe limits to pass a failure.

## Commands and evidence handoff

Commands below are execution instructions, not claims of runs performed during planning. Predeclare exact test names and cohort counts before running. Preserve one sole process and wait/drain it; a whole-suite watchdog expiration remains a harness failure with its original output, not a product pass or a reason to edit production deadlines.

Execution ownership of these commands is explicit: Task1 runs its RED/GREEN, Task1 Runtime group, the narrow native producer/receipt group, adjacent prober/startup checks and rules after its conditional production change. Task2 runs its own group and one normal rerun of the Task1 Runtime group only if shared native fixture paths changed; it does not repeat unchanged producer/native race or adjacent package checks just to restate the same proof. Any additional regression follows a concrete new source change/root ruling. Reports must keep the two units' source ranges separate.

```sh
# First contained behavioral RED, then same command GREEN after root ruling.
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test -count=1 -timeout=180s ./internal/productruntime -run '^TestRuntimeNativeEgressHoldUsesFrozenConcreteProbe$' -v

# Task 1 normal and identical count5 race selection.
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test -count=1 -timeout=180s ./internal/productruntime -run '^TestRuntimeNativeEgressHold(UsesFrozenConcreteProbe|FailuresBeforeStart)$' -v
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test -race -count=5 -timeout=300s ./internal/productruntime -run '^TestRuntimeNativeEgressHold(UsesFrozenConcreteProbe|FailuresBeforeStart)$' -v

# Task 2 normal and identical count5 race selection.
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test -count=1 -timeout=180s ./internal/productruntime -run '^TestRuntimeNativeEgress(SelectionAndLinkedChildren|ConsumedFailureEffects)$' -v
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test -race -count=5 -timeout=300s ./internal/productruntime -run '^TestRuntimeNativeEgress(SelectionAndLinkedChildren|ConsumedFailureEffects)$' -v

# Narrow current-source native/receipt regression; no broad budget/migration run.
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test -count=1 -timeout=180s ./internal/nativemcp -run '^(TestNativeProbeRetainsFrozenPolicyAndOriginOnlyIdentity|TestNativeProbeRejectsUnresolvedPlan|TestNativeInheritedExitUsesActualSOCKSTLSAndDefaultEditChangesNextOperation|TestNativeRelatedChildrenKeepRealParentExitAcrossDefaultEditOrFailure|TestNativeIndependentOperationsFreezeActualDefaultWithoutResolvingIngressLease|TestNativeSendRequiresSameStoreCommittedReceipt|TestNativeServeUnsentClosureRequiresOwnImmediateStartIdentity|TestNativePostEntryFailuresStayUnknown)$' -v
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test -race -count=5 -timeout=300s ./internal/nativemcp -run '^(TestNativeProbeRetainsFrozenPolicyAndOriginOnlyIdentity|TestNativeProbeRejectsUnresolvedPlan|TestNativeInheritedExitUsesActualSOCKSTLSAndDefaultEditChangesNextOperation|TestNativeRelatedChildrenKeepRealParentExitAcrossDefaultEditOrFailure|TestNativeIndependentOperationsFreezeActualDefaultWithoutResolvingIngressLease|TestNativeSendRequiresSameStoreCommittedReceipt|TestNativeServeUnsentClosureRequiresOwnImmediateStartIdentity|TestNativePostEntryFailuresStayUnknown)$' -v

# Actual adjacent prober/Runtime startup cleanup regressions.
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test -count=1 -timeout=180s ./internal/originaltransport -run '^TestOriginalTLSProbe' -v
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test -count=1 -timeout=180s ./internal/productruntime -run '^TestNativeRuntime(ContentProfileComposition|RecoversSameStoreAndClosesBeforeAssignmentDrain)$' -v
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go run ./cmd/repositorycheck
git diff --check
```

Do not automatically add all-package native/Store/client/migration runs. Root may request affected normal package coverage after inspecting the final diff; predeclare a cost-aware aggregate harness budget using prior measured durations, preserve product deadlines and distinguish selection completeness from wall-clock allowance. No automatic second process or broad rerun on timeout. Record the precise limitation if a run cannot finish.

Each unit's report includes source/base and exact owned paths/hashes; real RED versus setup failure; exact matched tests/count5 cohorts; per-operation selection/authority/once/Allow/HTTP/probe/audit evidence; joined resources; root production ruling if any; actual private adapter versus production/installed-client limits. No worker stages or commits. Root performs independent spec/security/quality review, resolves findings, then stages/commits owned paths. B3 closure requires both units and carried evidence; B4/B5 and Phase C remain open.

## Plan self-review

Root preflight (2026-10-12): fully read the plan/report and checked nativeProbe's existing unresolved guard plus Gate.Acquire/Resume at the source pin. Enqueue calls mutatedLocked and Resume checks expectedRevision before probing; corrected the stale Enter revision in the original sample to the actual current snapshot so a fixture CAS failure cannot masquerade as the intended native RED. Kept the existing resolved binding and producer-only correction conditional; assigned regression cohorts to their actual changed-source owner to avoid duplicate campaigns. This prepares B3 only, with no executed native behavioral claim.

The approved native Task 3 facts remain carried, including I1 closure. Task 1 addresses only the actual queued producer/dispatcher/prober mismatch and pre-Start outcomes, with a native authority proof and no generic prober change. Task 2 supplies missing FP/FD/B physical call/reply/recovery, independent next calls, linked Hold, and consumed-Allow/tool/child failure effects. Audit expectations precede observation and keep initial/terminal identity separate from logical recovery. No hypothetical full matrix, schema migration, new permission, broad MCP architecture, client workaround, retry service or continuation API has been added. Production correction is still conditional on real RED and root ruling; planning executes no tests or networks.
