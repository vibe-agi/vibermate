# Captured and Original Egress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Main dispatches one writer only after the current writer closes; main alone commits after review. Implementers perform no Git mutations.

**Goal:** Close the remaining existing Task6 captured-account, semantic client-credential Hold/fault, and raw/transformed-original physical egress evidence, correcting only the reproduced captured-read probe producer gap.

**Architecture:** Exercise the real Runtime, Store, published Environment, authenticated capture/CONNECT admission, actual account Manager/Reader and immutable per-attempt SQL audit through contained SOCKS and strict TLS fixtures. Preserve authored route authority and frozen resolved egress through queue, concrete probe and send; a narrowly validated captured-read producer may project origin TLS reachability while the compiled route remains the actual send and audit authority. Keep semantic `CredentialClientPassthrough`, raw `originaltransport` and transformed original-destination paths distinct.

**Tech Stack:** Go module `go 1.26.0`, toolchain `go1.26.9`; `/tmp/vibermate-go-security.JcRyfL/go/bin/go`, `GOTOOLCHAIN=local GOFLAGS=-mod=readonly`; existing `testing`, `httptest`, TLS, SOCKS, Runtime, `provideraccount`, `accountoperation`, `offlinehold`, and SQL audit. No dependency changes.

**Spec:** Approved `docs/superpowers/specs/2026-10-05-global-default-egress-design.md`; Task6 of `docs/superpowers/plans/2026-10-05-global-default-egress.md`; current authority/evidence `.superpowers/sdd/2026-10-05-global-default-egress/phase-b-remaining-audit.md`, whose latest root closure notes supersede historical cells.

## Global Constraints

- Account explicit > flow explicit > global > builtin Direct. Explicit Direct wins. Original-credential operations have no managed account layer.
- Inheritance is configuration selection only. Proxy, DNS, strict TLS, cancellation and configuration-read failures never cause retries, another proxy, Direct fallback or a repeated POST.
- Freeze before queue/probe/send. Default edits affect subsequent independent operations only; retain actual `Resolved` profile, source and default revision throughout each operation.
- Preserve captured history permission, actual account membership/realm/revision and revocation checks, original caller credentials, existing target/private-peer constraints, body/header/stream contracts and per-attempt audit authority. Network selection grants no access.
- No generic prober weakening, purpose/kind relabeling, routed-plan erasure across families, authored Environment/digest mutation, synthetic credential authority or SQL reconstruction of permission.
- Carry reviewed managed/token/owned/manual-refresh/endpoint-model/new-login work. Exclude owned quota/history/reset, manual refresh, endpoint models, first login, native/UDP/blind, metadata/update/UI, new features and Phase C. This is neither a new Goal nor release approval.
- Planning source pin: `86fc9ab45d6c0745460285fa0cbed0a7fa7f032d`. All planning source/design reads used `git show`/`git grep` at this pin. No `.codegraph/` exists. Do not read concurrent first-login WIP. At dispatch, main supplies the reviewed execution base and exact changed-file hashes; preserve subsequent reviewed work and adapt against that authorized base without reverting it.
- Only private synthetic Store/credentials/captures and fixture sockets. No external provider/user state. Runtime fixture network traffic must reject every unlisted dial/DNS/HTTP target; strict certificates remain verified.
- Each task owns `.superpowers/sdd/2026-10-12-captured-original-egress/task-N-{report,manifest,review}.md` and uniquely named normal/race/RED logs in that directory. Record execution base, files/hashes, commands/exit status, cases, per-ID audits, HTTP/probe/dial counts and drain. Do not edit another plan's ledger; main updates shared closure notes after independent spec/security/quality review.
- Workers use `apply_patch`, format Go, run targeted normal and `-race -count=5`, and hand off file hashes plus evidence. No worker branch/index/commit/rebase operations. Main alone commits the independently reviewed task.
- If a new test passes unchanged, retain that baseline as evidence; absent evidence is not a production defect. Only a real behavioral RED authorizes a scoped production correction. Escalate an actual target/permission/interface ambiguity with the RED and authority chain; do not invent one from missing coverage.

## Carried evidence and file map

The audit already records semantic FP/FD/G/B positive/frozen sends and G/FP proxy-down in `internal/productruntime/egress_network_test.go:228–237,499–539,1068–1175`. Carry these results; do not recreate or redesign them. Original account G and a specific original-destination G model/frozen/down send are recorded at `:177–181`; carry them. Originaltransport's response-double freeze test and original pipeline liveness tests remain useful contracts, but do not count as new physical Runtime cells.

| File | Responsibility / owner |
| --- | --- |
| `internal/productruntime/captured_original_egress_fixture_test.go` (new, Task1) | Shared private Runtime/capture/network fixture, expected-selection inputs, synchronized send/stream checkpoints, counters, per-ID audit and drain |
| `internal/productruntime/captured_account_egress_test.go` (new, Task1) | Managed six layers, original missing three layers, real captured recovery RED/GREEN and permissions/faults |
| `internal/providertransport/captured_account_probe.go` (new, conditional Task1) | Closed captured-read producer validation and origin-only reachability projection |
| `internal/providertransport/captured_account_probe_test.go` + `captured_account_probe_integration_test.go` (new, conditional Task1) | Test-only private producer bridge plus external real Store/Manager/compiled-plan assembly, avoiding the existing provideraccount→exchange→providertransport cycle |
| `internal/providertransport/runtime_fetch.go:509–535` (conditional Task1) | Dispatch only `PurposeRouteOperation` with a captured account-read spec to the closed producer |
| `internal/productruntime/semantic_original_egress_test.go` (new, Task2) | Remaining semantic original-credential Hold/cancellation/Direct-target/TLS evidence |
| `internal/productruntime/original_forward_egress_test.go` (new, Task3) | Nonsemantic raw and original-destination model physical/frozen/stream evidence |

Execution is sequential. Shared setup is inside Task1, initially implementing only the managed_read/original_read families actually consumed there. Tasks2/3 may add their own test-only family/stream cases to the same fixture when they become consumers; those named extensions are part of that task's diff/review and require the affected prior fixture cases, not a repeat of unrelated packages. Do not prebuild untested semantic/raw/transformed execution in Task1. Main's later dispatch sidecar includes actual reviewed fixture interfaces/hash/manifest, these constraints and source-backed amendments; a worker reading only its task must receive those inputs.

### Task 1: Captured account reads, actual recovery RED and closed producer

Status 2026-10-12: implementation and targeted normal/race completed; independent initial review's I1 test-cleanup finding closed by focused fix1 and scoped re-review. M1 parent-ID assertion limits and M2 expected TLS log noise remain nonblocking and recorded for final review. Exact evidence/source hashes and commit are in this plan's SDD ledger; this does not close Tasks2/3 or the release.

**Files:** Create `captured_original_egress_fixture_test.go` and `captured_account_egress_test.go`; conditionally create `captured_account_probe.go`, `captured_account_probe_test.go`, `captured_account_probe_integration_test.go` and modify only `runtime_fetch.go`'s producer dispatch. The extra integration test file uses package providertransport_test to assemble the real Store/Manager/compiled plan; the internal *_test.go bridge inspects private runtime specs, following existing ExerciseOwnedAccountGlobalSOCKS without a production export. Read existing `egress_network_test.go:50–175,354–404,404–541,564–619`, `account_proxy_test.go:32–108,130–245`, `account_operation_test.go:192–284,287–348`, `provideraccount/account_read.go:14–31`, `accountoperation/reader.go:127–193`, `providertransport/account_read.go:30–51,71–87`, `owned_account_probe.go`, `originaltransport/probe.go:88–145`, and `productruntime/offline.go:50–87`. Own Task1 SDD artifacts and logs.

**Interfaces:** Consumes `startWithBuilders(ctx, options, runtimeBuildOverrides) (*Runtime,error)`, `testOptions(t,hostcontract.Desktop(),gate)`, `runtimeSOCKSFixture(t,upstream) (net.Listener,*atomic.Int64)`, `runtimeClosedNetwork`, `runtimeOriginalWire`, `runtimeContainedProvider`, `compatibilityEnvironment(t,environment.ClientProtocolOpenAIResponses,protocolspec.DialectOpenAIResponses)`, `Runtime.ProxyHandler`, `Runtime.AccountReads() *accountoperation.Reader`, `Manager.AcquireReadCredential(ctx,plan) (providerauth.Lease,error)`, `Reader.ReadCaptured(ctx,plan,accountoperation.Source) (accountoperation.Result,error)` and `Runtime.EnterOfflineHold/ResumeOfflineHold(ctx,revision) (offlinehold.Snapshot,error)`. Preserve these production interfaces.

Produces test-only `newCapturedOriginalFixture(t *testing.T, cfg capturedOriginalConfig) *capturedOriginalFixture`, `(*capturedOriginalFixture).open(ctx context.Context, method,path,body string) (*http.Response,error)`, `.send(ctx context.Context, method,path,body string) capturedOriginalReply`, `.waitQueue(t *testing.T) []offlinehold.ProbeTarget`, `.editDefault(t *testing.T)`, `.assertAudits(t *testing.T, want int)`, and `.drain(t *testing.T)`. `open` returns the live response for Task3 streaming; `send` consumes/closes it for complete-response cases. Send checkpoint channels `entered` / `release` and stream channels `streamEntered` / `streamRelease` are owned by this fixture and are initialized only for their named edge. A test sends in a joined goroutine, waits for `entered`, calls `editDefault`, closes `release` via `sync.Once`, then joins. A stream test uses `open`, reads an actual first frame, edits and closes `streamRelease`. The fixture and input/output shape are owned here:

```go
type capturedOriginalConfig struct {
    family string // "managed_read", "original_read", "semantic", "raw", "original_model", "transformed"
    choice string // "account_proxy", "account_direct", "flow_proxy", "flow_direct", "global", "builtin"
    edge string   // case tables below; exact literals, never arbitrary destination data
    history bool
    oauth bool
}
type capturedOriginalReply struct { Status int; Header http.Header; Body []byte; Err error }
type capturedOriginalExpected struct {
    Source egressprofile.SelectionSource
    ProfileID string
    DefaultRevision, SettingsRevision uint64
}
```

Store Runtime/gate/options, synthetic account, published aggregate and capture, client/transport, strict TLS server, two SOCKS listeners A/B, physical request/probe/dial counters, captured request bytes/headers, and `map[string]egressaudit.DecisionRef` starts in the fixture; synchronize all cross-goroutine observations with channels/mutexes. Use a `sync.Once` release closure and bounded join on every error path. Keep the snapshot/assertions in task-local helpers; do not add product exports for test access.

- [x] **Step 1: Build the fixture as this task's first consumer.** Adapt the existing private certificate/network/builder composition verbatim in a new file; preserve existing helpers unchanged. Certificate covers only `chatgpt.com` (and `auth.openai.com` only if retaining the existing wire seam); roots explicitly trust the fixture cert, and wrong-name/untrusted-cert rows must fail normally. A/B SOCKS listeners route only their closed canonical destination to the synthetic TLS server. Direct support maps only the existing literal `chatgpt.com:443`/`127.0.0.1:443` to this server and counts attempts before dialing. Add a task-local wire wrapper rather than broadening `runtimeOriginalWire`'s shared whitelist: allow only GET `/backend-api/wham/usage`, GET `/backend-api/wham/profiles/me`, GET `/backend-api/plugins/featured` with the later exact query, and POST `/backend-api/codex/responses`. Reject all other request shapes before socket use. No token request is allowed: synthetic managed credentials are valid, automatic refresh false, epoch1.

Use real authenticators and `providertransport.NewClient` with `EgressDefaults`, `Coordinator`, `InstanceIDs`, `Audit`, `RawEvidence` from `providerBuildRequest`; use `runtimeProviderWire`'s actual dispatch policy mechanics. `options.OriginalTransport` uses the private real-socket wrapper, and both Runtime prober overrides remain the concrete `NewTLSProberWithBuilder(builder,roots)` / `NewProviderProberWithBuilder(builder,roots)` constructors.

Create accounts through `Runtime.ProviderAccounts().Create`, using the valid static material pattern in `account_operation_test.go:205–240` (and a valid Codex OAuth material row with no rotation). Set account AP/AD via real `SettingsCommand`; acquire through captured Manager/Reader, never a hand-typed AccountRef. Configure and publish real Environment:

```go
aggregate, _, _ := compatibilityEnvironment(t, environment.ClientProtocolOpenAIResponses, protocolspec.DialectOpenAIResponses)
origin, err := originidentity.ParseClientOrigin("https://chatgpt.com")
if err != nil { t.Fatal(err) }
aggregate.ID, aggregate.Name = "captured.original.egress", "Private captured/original egress"
aggregate.ClientEndpoints[0].ClientOrigin = origin
plan := &aggregate.ClientEndpoints[0].ProtocolPlans[0]
plan.EgressProfile = egressprofile.Inherit()
route := &plan.Destination.Upstream.Routes[0]
// Managed: endpoint comes from r.UpstreamEndpoints().Get(ChatGPTOfficialID),
// fixed account and references from the real Create result, capabilities from endpoint.
// Original read/semantic: same real endpoint, AccountSelectionOriginal.
// This permission is managed authority, invalid on AccountSelectionOriginal.
route.AllowAccountHistory = cfg.family == "managed_read" && cfg.history
```

For FP/FD assign `egressprofile.Explicit(A/Direct)` to `plan.EgressProfile`. Publish using `SaveDraft` → `Preview` → `Publish`. Use actual manual capture and `CaptureAssignments().Create` for the real Runtime proxy, as `egress_network_test.go:404–411,541–543` does. Also run one managed-run/Codex `CreateForLaunch` admission row using `account_proxy_test.go:46–108`'s actual client profile; its native base-path authority must remain intact. Downstream only dials the Runtime httptest listener, authenticates the capture and trusts Runtime's local CA. Send real CONNECT+HTTPS through `r.ProxyHandler()`; do not call a response-double replacement proxy.

Selection inputs are explicit, not inferred from observed SQL:

| Choice | Account | Flow | Default before operation | Expected source/profile/default revision/settings |
| --- | --- | --- | --- | --- |
| AP | A | B proxy | B proxy, revision2 | account/A/0/2 |
| AD | Direct | A proxy | B proxy, revision2 | account/Direct/0/2 |
| FP | inherit | A | B proxy, revision2 | flow/A/0/1 |
| FD | inherit | Direct | B proxy, revision2 | flow/Direct/0/1 |
| G | inherit | inherit | A proxy, revision2 | global/A/2/1 |
| B | inherit | inherit | unset revision1 | builtin/Direct/1/1 |

For original/semantic/raw/transformed families, omit account configuration entirely and expect AccountID empty / AccountSettingsRevision0; only FP/FD/G/B apply. Publish A as `profile.captured-original-a`, B as `profile.captured-original-b`, both revision1. `editDefault` changes revision2 to unset revision3, or unset revision1 to B revision2. A next independent operation is physically sent, not merely resolved: explicit selections remain; initial G now builtin3; initial B now global/B/2.

Wire fixtures return valid quota facts with `account_id: workspace-B` and rate limits; history uses `{"stats":{"lifetime_tokens":1200}}`. Managed requests must carry managed bearer/workspace-B and no caller Cookie, proxy credentials or original bearer; original requests retain exact `Bearer original-A`, `ChatGPT-Account-Id: workspace-A`, `Cookie: original-A-cookie`, and `User-Agent: captured-original-fixture/1`. Return exact response bytes with no-store only where the real managed Reader adds it. Record raw bytes/headers at the actual server; secrets stay synthetic and redacted in logs.

- [x] **Step 2: Write/run the actual held managed quota acceptance first, preserving its RED.** Name `TestActualRuntimeCapturedManagedReadConcreteRecovery`. Enter Hold before sending GET `/backend-api/wham/usage` in a joined goroutine, observe exactly one queued target with real Environment revision/digest and endpoint TargetRef, and assert HTTP0/proxy0/Direct0. Edit the default only after that observation; call actual `r.ResumeOfflineHold`. The pinned source is expected to reject with `original-origin probe unexpectedly contains a routed plan identity` before socket probing. Do not assert that failure as desired behavior: the acceptance assertion requires successful Resume followed by exactly one quota GET on A, one probe connection plus one send connection, B0/Direct0, correct managed credentials and captured parent/audit. On RED/error cancel the caller, join it, begin gate shutdown if necessary, drain and record zero-send/zero-rotation/no-retained-actions evidence before returning failure.

```go
if _, err := f.r.EnterOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil { t.Fatal(err) }
ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
defer cancel()
done := make(chan capturedOriginalReply, 1)
go func() { done <- f.send(ctx, http.MethodGet, "/backend-api/wham/usage", "") }()
targets := f.waitQueue(t)
t.Logf("captured managed queue=%+v targets=%+v", f.gate.Snapshot(), targets)
f.editDefault(t)
_, resumeErr := f.r.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision)
if resumeErr != nil {
    t.Errorf("captured managed concrete recovery: %v", resumeErr)
    cancel()
    <-done
    f.gate.BeginShutdown()
    f.drain(t)
    return
}
reply := <-done
if reply.Err != nil || reply.Status != http.StatusOK { t.Errorf("captured quota reply: %+v", reply) }
f.assertAudits(t, 1)
f.drain(t)
```

The fixture's `.waitQueue` follows the existing bounded `runtime.Gosched` observation pattern; use deadlines/channels, no sleep deciding the freeze point. RED run only this test; capture exit status and target identity in Task1's RED log. If the authorized execution base already contains an equivalent reviewed correction, first run the unchanged acceptance and report that fact instead of manufacturing a RED.

```sh
GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go version
GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/productruntime -run '^TestActualRuntimeCapturedManagedReadConcreteRecovery$' -count=1 -timeout=45s -v
```

- [x] **Step 3: Only after that RED, implement the closed producer and its contract tests.** The authority path is Runtime authenticated capture → frozen assignment RequestPlan → `Reader.ReadCaptured` history check → `Manager.AcquireReadCredential` actual scoped lease → `NewCapturedAccountRead` validation → `ReadAccount` → `fetchRuntimeJSON`. Only the last reachability projection changes; retain `spec.captured`, frozen plan/route, credential, purpose `route_operation`, coarse kind Auxiliary, resolved exit and send/audit behavior. A narrow starting implementation is:

```go
func capturedAccountProbeTarget(spec runtimeFetchSpec) (offlinehold.ProbeTarget, error) {
    invalid := errors.New("captured account probe target differs from closed operation")
    if spec.purpose != egressaudit.PurposeRouteOperation || spec.captured == nil ||
        spec.method != http.MethodGet || len(spec.body) != 0 || spec.contentType != "" ||
        !upstreamendpoint.IsChatGPTCodexOrigin(spec.target.Origin()) {
        return offlinehold.ProbeTarget{}, invalid
    }
    c := spec.captured
    request, err := NewCapturedAccountRead(c.plan, spec.credential, c.connectionID, c.operationID)
    if err != nil || request.read.Origin() != spec.target.Origin() ||
        request.read.Path() != spec.relativePath || request.targetRef != spec.targetRef {
        return offlinehold.ProbeTarget{}, invalid
    }
    if request.read.ID() != upstreamservice.CodexRateLimits && request.read.ID() != upstreamservice.CodexUsageHistory {
        return offlinehold.ProbeTarget{}, invalid
    }
    account, ok := spec.credential.Account()
    if !ok || account.Validate() != nil || account != spec.account || spec.resolved != account.ResolvedEgress {
        return offlinehold.ProbeTarget{}, invalid
    }
    // The validated service may have /backend-api or /backend-api/codex
    // as its provider base path; TLS reachability has no path component.
    const origin = "https://chatgpt.com"
    target := offlinehold.ProbeTarget{Kind: offlinehold.EgressAuxiliary,
        Transport: offlinehold.ProbeTransportStrictTLS,
        TargetRef: origin, NetworkOrigin: origin,
        HTTPAuthority: spec.target.HTTPAuthority(), TLSServerName: spec.target.TLSServerName(),
        EgressPolicy: spec.egressProfile().Policy}
    return target, target.Validate()
}
```

Dispatch immediately after the existing owned branch, only when purpose RouteOperation **and** `captured != nil`; do not replace the generic path for other callers. The closed target has PlanRevision0/PlanDigest empty/TargetAccess0 only because a verified fixed quota/history GET needs origin TLS reachability, not routed model execution. Its original Environment digest/revision remain in `spec.captured` and the real send/audit. If the actual authority path cannot prove those invariants, stop with the concrete choice needed; do not broaden the helper, weaken prober checks or relabel purpose.

Producer tests named `TestCapturedAccountProbeClosedProducerAndConcreteTLSReachability` must construct a real compiled quota/history plan with a real Manager lease, derive `NewCapturedAccountRead`, and set runtime spec as `fetchRuntimeJSON` does. Follow `owned_account_probe_test.go`'s concrete prober reachability seam, preserving its existing owned exclusion of captured specs. Cover both GETs across the three already recognized provider origin/base-path forms; each generates canonical path-free TLS target and actual strict prober success with HTTP0 while the original plan/request retains its provider origin and nonzero Environment identity. Rejection table: nil capture; nil/nonmanaged credential; zero/mismatched AccountRef; wrong account ID/revision/realm; missing connection/operation ID; wrong target ID/origin/path; wrong method/body/content type/purpose; history without permission; reset-details/consume; unknown read; mismatched resolved/account exit; cross-origin original account. Add a control showing existing generic routed Auxiliary identity is still rejected by the original prober. Never mutate an actual lease to simulate authority; use isolated spec/constructor negatives. For denied history use production compiled route false, not an invalid hand-edited plan.

- [x] **Step 4: Fill this task's physical table and freeze/fault/permission assertions.** Names: `TestActualRuntimeCapturedAccountReadEgressMatrix`, `TestActualRuntimeCapturedAccountReadHoldAndFailure`, `TestActualRuntimeCapturedAccountReadAuthority`.

| Rows | Exact cases |
| --- | --- |
| Managed physical | AP/AD/FP/FD/G/B × quota + allowed history; each `send_edit`, then a second independent physical GET with new expected selection |
| Original missing physical | FP/FD/B × quota + history, original route, original credentials; carry original G positive/frozen/down |
| Managed held | G quota actual RED→GREEN above; FP allowed history; AD and B quota Hold/edit/probe; G held cancel; G failed closed-proxy probe; FD wrong-name TLS probe |
| Original held | G quota and FP history Hold/edit/concrete original probe; G held cancel/closed-proxy probe; FD wrong-name TLS probe |
| Send failures | Managed AP/FP/G selected-proxy closed at observed send boundary; managed AD/B one target-dial failure and one strict-TLS failure each; original FP proxy loss, FD/B target-dial and TLS failure each |
| Authority | managed history denied (403, no lease/send/audit); captured reset details and consume rejected; association revoked before acquisition; unbound JS account selector; mismatched/cross-origin original account; missing/invalid capture capability; one actual managed-run launch row; valid OAuth managed quota row rotation0 |

For each successful Hold row, observe the real queued identity then edit; A route remains A even if default becomes Direct. Probe is a separate TLS connection, not an HTTP GET. One probe + one send, one HTTP response, no resend. Direct held rows similarly count probe/send separately. A failed probe keeps queue Held and HTTP0; cancel/shutdown, join, then drain. A canceled queued caller must never later send even after attempted Resume. Preserve history permission differences: managed history requires AllowAccountHistory; original caller history is original authority, as existing `TestOriginalAccountQueriesThroughCONNECT` records.

At actual wire entry read `EgressAttempts().List` and snapshot each nonterminal decision keyed by its attempt ID; then block until test edits default or injects failure. `assertAudits` compares that same ID's terminal decision with its own start and precomputed row expectation. Managed purpose `PurposeRouteOperation`, payload Control, Environment authority, PolicyID/revision and route ID, ParentClientOperation and actual ConnectionID, real account/settings. The separate original-account GET producer passes `EgressAuxiliary`, so its audit purpose is existing `PurposeAgentProbe` (`egressaudit/attempt.go:695–696`), `AuthorityNetwork`, `ParentOriginalRequest`, and AccountID empty; preserve that purpose instead of relabeling it to RouteOperation or OriginalOrigin. Assert each emitted attempt once, correct outcome for transport failure vs received response, no orphan terminal. Queue cancellation before audit start has zero audit; do not invent an audit for a send that never began. HTTP503, if used as a response-contract row, is a received exchange and is not proxy-loss evidence.

Use a fresh fixture per fault; prove unavailable selected proxy sees zero target Direct attempts, HTTP0 and no second attempt. Make Direct faults deterministic in the closed dialer/TLS fixture; one selected target attempt, HTTP0 for dial/TLS failure, zero A/B proxy connections, no retry. After each case close response/client idle connections, join request/server producers, use `gate.Drain` and verify ActiveActions/ActiveEgress/QueuedRequests/HeldBytes all0; drain actual body admission and bounded Runtime shutdown. Server and SOCKS workers must join. Require zero refresh/save/secret revision changes.

- [x] **Step 5: Run targeted normal/race, review and hand off.** Retain the real RED and corrected GREEN under Task1 SDD. Main reviews spec/security/quality before commit and dispatching Task2. Run the producer tests only if created; run the existing owned closed producer once to ensure captured dispatch does not change it. Normal/race scopes are exact and do not repeat whole packages:

```sh
GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/productruntime -run '^TestActualRuntimeCaptured(ManagedReadConcreteRecovery|AccountRead(EgressMatrix|HoldAndFailure|Authority))$' -count=1 -timeout=120s -v
GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test -race ./internal/productruntime -run '^TestActualRuntimeCaptured(ManagedReadConcreteRecovery|AccountRead(EgressMatrix|HoldAndFailure|Authority))$' -count=5 -timeout=300s -v
GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/providertransport -run '^Test(CapturedAccountProbeClosedProducerAndConcreteTLSReachability|OwnedAccountProbeClosedProducerAndConcreteTLSReachability)$' -count=1 -timeout=60s -v
GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test -race ./internal/providertransport -run '^TestCapturedAccountProbeClosedProducerAndConcreteTLSReachability$' -count=5 -timeout=90s -v
```

### Task 2: Semantic original-credential model Hold and remaining Direct faults

Status 2026-10-12: tests-only closure on unchanged production; normal11, count5 race55 and18 affected captured-read cases passed. Independent spec/security and quality Approved, no Critical/Important. T2-M1 expected TLS log noise and T2-M2 post-failed-probe zero-send reassertion remain nonblocking in the SDD ledger. Actual upstream-request identity plus persisted Exchange are correlated; no separate durable upstream-attempt row is claimed. Commit/precise evidence are recorded in the ledger; Task3 and release remain open.

**Files:** Create `internal/productruntime/semantic_original_egress_test.go`; extend the reviewed `captured_original_egress_fixture_test.go` only for the newly consumed semantic family, preserving existing managed/original-read behavior. Name the affected old fixture cases if a shared path changes, and include that small covering rerun in the report. Read `egress_network_test.go:228–246,499–539,1112–1123`, `exchange/selection.go`'s UsesOriginalAccount arm, `providertransport/plan.go:145–159,229–286`, `providertransport/client.go:263–289,895–911` and `providertransport/probe.go`. Own Task2 SDD artifacts/logs. No production edit is expected; any observed production RED requires its own scoped proposal/review.

**Interfaces:** Consumes Task1 sidecar with fixture signatures, exact config/expected table, reviewed hash and manifest. Uses `newCapturedOriginalFixture(t,capturedOriginalConfig{family:"semantic",choice:choice,edge:edge})`, `.send`, `.waitQueue`, `.editDefault`, `.assertAudits`, `.drain`. Produces `TestActualRuntimeSemanticOriginalHoldAndFailure`. Runtime actual published upstream route has `AccountSelectionOriginal`, identity-preserving client/provider origin and codec, and genuine `CredentialClientPassthrough`; never substitute SystemTransparent or `originaltransport.Do`.

- [x] **Step 1: Add the exact remaining acceptance table; run unchanged baseline.** Carry the four positive/send-freeze and G/FP down proofs; do not repeat their implementation. Add:

```go
rows := []struct{ choice, edge string }{
    {"global", "hold_edit"}, {"flow_proxy", "hold_edit"},
    {"flow_direct", "hold_edit"}, {"builtin", "hold_edit"},
    {"global", "held_cancel"}, {"global", "held_failed_probe"},
    {"flow_direct", "held_tls_failure"},
    {"flow_direct", "target_dial_failure"}, {"builtin", "target_dial_failure"},
    {"flow_direct", "strict_tls_failure"}, {"builtin", "strict_tls_failure"},
}
```

Use POST `/backend-api/codex/responses`, body `{"model":"synthetic","input":"hello","stream":false}` and the exact original caller Authorization/account/Cookie/User-Agent fixture headers. The fixture's real server validates the parsed semantic three-field body, original credentials, no Proxy-Authorization, response ID and zero managed token requests. Compile/publish and acquire admission through actual Runtime, as Task1 does. The table is case data for the named test; complete every row's execution/assertions in Step2 before running the baseline.

- [x] **Step 2: Implement the row actions/assertions using the actual Provider prober.** Hold rows follow Task1's concrete queue/edit/resume/join code with the semantic POST. Require EgressProvider and exact published Environment revision, but do not mistake PlanDigest for raw EnvironmentDigest: `RequestProvenance.probePlanDigest` at plan.go:229–247 hashes the frozen Environment identity, destination/route and normalized proxy/resolver fields. Derive the expected digest independently from the literal fixture inputs using that actual encoding (not the production digest helper), require exact target shape/policy, AccountRef0 and HTTP0 before Resume. `hold_edit`: one concrete TLS probe plus one POST on the original selected exit, exact original credential bytes, no OAuth/account effects; a second independent POST must physically use the new default for G/B and retain explicit FP/FD. Precompute separate next-operation selection before observing SQL; never derive it from observed ProxyID.

`held_cancel`: cancel queued caller, join, verify no late POST after a subsequent Resume request; no managed credential/refresh flight can survive because this family has none. `held_failed_probe` closes A after observing target/default edit; Resume fails and retains queue, POST0/Direct0, then cancel/shutdown/drain. `held_tls_failure` uses normal cert validation, HTTP0 and no release. FD/B `target_dial_failure`/`strict_tls_failure`: selected Direct attempts1, physical HTTP0, A/B0, immutable failed attempt if send audit started, no retries; preserve typed target/private-peer checks rather than installing insecure TLS or arbitrary address bypasses.

The actual successful Hold action, inside each `hold_edit` subtest, uses these calls (the canceled/failed rows use the preceding explicit branch actions before Resume/join):

```go
f := newCapturedOriginalFixture(t,capturedOriginalConfig{family:"semantic",choice:row.choice,edge:row.edge})
if _, err := f.r.EnterOfflineHold(t.Context(),f.gate.Snapshot().Revision); err != nil { t.Fatal(err) }
ctx, cancel := context.WithTimeout(t.Context(),3*time.Second)
defer cancel()
done := make(chan capturedOriginalReply,1)
go func() { done <- f.send(ctx,http.MethodPost,"/backend-api/codex/responses",`{"model":"synthetic","input":"hello","stream":false}`) }()
targets := f.waitQueue(t)
if len(targets) != 1 || targets[0].Kind != offlinehold.EgressProvider || targets[0].PlanRevision == 0 || targets[0].PlanDigest == "" { t.Error("missing semantic Provider plan") }
f.editDefault(t)
if _, err := f.r.ResumeOfflineHold(t.Context(),f.gate.Snapshot().Revision); err != nil {
    t.Error(err)
    cancel()
    <-done
    f.gate.BeginShutdown()
    f.drain(t)
    return
}
reply := <-done
if reply.Err != nil || reply.Status != http.StatusOK { t.Errorf("semantic reply: %+v",reply) }
f.assertAudits(t,1)
next := f.send(t.Context(),http.MethodPost,"/backend-api/codex/responses",`{"model":"synthetic","input":"hello","stream":false}`)
if next.Err != nil || next.Status != http.StatusOK { t.Errorf("next semantic reply: %+v",next) }
f.assertAudits(t,2)
f.drain(t)
```

Audit: PurposeProviderAttempt, Environment/route authority and PolicyID/revision, `ParentUpstreamAttempt` with the real upstream-attempt ID and `ExchangeID` (`providertransport/client.go:1102–1106`), AccountID empty/AccountSettingsRevision0, initial/terminal decision equality by attempt ID, exact source/profile/default revision input, one attempt per physical POST. Validate that typed parent against the real Runtime Exchange/attempt records; do not repurpose a client-operation ID. Before-admission cancellation/probe failure creates no send audit; emitted starts must close. Drain gate, body admission, pipeline/response and fixture/socket workers. No artificial RED: if unchanged source passes all remaining evidence, this is tests-only.

- [x] **Step 3: Targeted normal/race and independent review.** Update only Task2 SDD, record actual row counts and production unchanged/changed hashes. Main reviews/commits before Task3.

```sh
GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/productruntime -run '^TestActualRuntimeSemanticOriginalHoldAndFailure$' -count=1 -timeout=75s -v
GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test -race ./internal/productruntime -run '^TestActualRuntimeSemanticOriginalHoldAndFailure$' -count=5 -timeout=120s -v
```

### Task 3: Raw/nonsemantic and transformed-original physical/frozen paths

**Files:** Create `internal/productruntime/original_forward_egress_test.go`; add only the newly consumed raw/original_model/transformed and stream support to `captured_original_egress_fixture_test.go`, via the actual reviewed fixture sidecar. Preserve prior family behavior and run named affected prior cases when a shared path changes. Read `operationcatalog/catalog.go:155–216`, `loopbackproxy/handler.go:1099–1131,1724–1803`, `originaltransport/request.go:192–201`, `originaltransport/client.go:153–271`, `exchange/selection.go:227–255`, `long_session_test.go:139–171`, `loopbackproxy/liveness_original_integration_test.go:162–302`. Own Task3 SDD artifacts/logs. No production change is presumed.

**Interfaces:** Consumes reviewed Task1 fixture/config/helpers/hash and Task2 reviewed evidence. Produces `TestActualRuntimeOriginalRawEgressMatrix`, `TestActualRuntimeOriginalTransformedEgressMatrix`, `TestActualRuntimeOriginalForwardHoldAndFailure`. Raw nonsemantic executes actual `originaltransport` through handler dispatch; original-destination models execute actual Exchange/provider path with `DestinationKindOriginal`. Transformed original never substitutes an upstream route with AccountSelectionOriginal (that is Task2).

- [ ] **Step 1: Write the physical matrices and exact fidelity inputs.** Raw nonsemantic GET is the existing catalogued opaque `/backend-api/plugins/featured?platform=synthetic%2Bfixture`, body empty, payload Control. It reaches EgressOpaque/PurposeOriginalOrigin via `serveOriginal`; this is not an invented endpoint or permission. Match exact raw query, Authorization/account/Cookie/User-Agent, no proxy credentials; return `Content-Type: application/octet-stream`, `X-Original-Fixture: raw`, bytes `{0,1,2,'r','a','w',255}` and require exact bytes/header at downstream.

Original-destination model uses `compatibilityEnvironment`, sets destination to `environment.DestinationPlan{Kind:environment.DestinationKindOriginal}`, egress choice from row, no Upstream route/account, and sends POST `/backend-api/codex/responses`. Carry its existing G no-transform positive/frozen/down evidence; add FP/FD/B actual model sends and streamed freeze. Transformed-original covers FP/FD/G/B with this exact published transform:

```go
plan.Destination = environment.DestinationPlan{Kind: environment.DestinationKindOriginal}
plan.Transforms = []codelibrary.TransformRevision{{
    ID:"captured-original.transform", Revision:1, CollectionID:"synthetic",
    DisplayName:"Synthetic original transform", PublishedAt:time.Now().UTC(),
    Policy:messagetransform.Policy{
        RequestJavaScript:`const p=JSON.parse(request.body); p.input="transformed-input"; request.body=JSON.stringify(p); request.headers["x-original-transform"]="request";`,
        ResponseJavaScript:`response.headers["x-original-transform"]="response"; const p=JSON.parse(response.body); if(p.type==="response.output_text.delta") p.delta="transformed-delta"; response.body=JSON.stringify(p);`,
    },
}}
```

Both streamed model requests use original `Bearer original-A`, account/workspace-A, Cookie and User-Agent. Body is `{"model":"synthetic","input":"hello","stream":true}`; untransformed request retains semantic content and original metadata, transformed server observes model/stream unchanged plus input transformed-input and exact request marker. Do not require byte equality where established semantic encoding reserializes JSON. Use valid `response.created`, `response.output_text.delta`, `response.completed` SSE as the existing original liveness test does, with a raw comment between events. Raw/no-transform stream preserves the original delta/comment according to existing contract; transformed stream yields transformed-delta/response marker and follows existing transformed event framing/comment behavior, without accidental managed heartbeat insertion. User-visible protocol/status/headers and terminal completion remain valid.

```go
func TestActualRuntimeOriginalRawEgressMatrix(t *testing.T) {
    for _, choice := range []string{"flow_proxy","flow_direct","global","builtin"} {
        t.Run(choice, func(t *testing.T) {
            f := newCapturedOriginalFixture(t,capturedOriginalConfig{family:"raw",choice:choice,edge:"send_edit"})
            done := make(chan capturedOriginalReply,1)
            go func() { done <- f.send(t.Context(),http.MethodGet,"/backend-api/plugins/featured?platform=synthetic%2Bfixture","") }()
            select { case <-f.entered: case <-time.After(2*time.Second): t.Fatal("raw send boundary missing") }
            f.editDefault(t)
            f.releaseOnce.Do(func(){close(f.release)})
            reply := <-done
            if reply.Err != nil || reply.Status != http.StatusOK || !bytes.Equal(reply.Body,[]byte{0,1,2,'r','a','w',255}) { t.Fatalf("raw fidelity: %+v",reply) }
            f.assertAudits(t,1)
            f.drain(t)
        })
    }
}
```

Complete the second physical operation/header/query/counter assertions in Step2 before baseline. `TestActualRuntimeOriginalTransformedEgressMatrix` iterates four choices for transformed `stream_edit`; add original_model FP/FD/B stream rows under that test as explicitly named `original_model/<choice>` cases. Family names distinguish untransformed proof from transformed proof.

- [ ] **Step 2: Implement default-edit and stream checkpoints and audits.** For each raw positive, pause at real wire entry after SQL initial audit but before dialing, edit default, release; one GET uses old selection. Send a second independent GET and validate old/new expected selection and two distinct start/terminal IDs. For original-model/transformed positive rows, server writes/flushes response.created and one delta, signals first-chunk checkpoint, waits on release; test observes actual downstream chunk before editing default. Release terminal frame and consume/close response. The in-progress stream uses its initial exit, POST1/no reconnect; a second independent POST physically uses the new inherited default or retained explicit selection. Precompute expectations before SQL observations.

Raw audit retains PurposeOriginalOrigin, `AuthorityNetwork` / `ParentOriginalRequest`, payload Control for the allowed query, original target, AccountID empty; `originaltransport/client.go:427–455` uses the frozen resolved profile or runtime-default typed policy, not an Environment route. Provider-path original/transformed audit retains actual Environment original-destination provenance, PurposeProviderAttempt and `ParentUpstreamAttempt` with real attempt ID/ExchangeID, nonzero original plan/digest in Provider probe, no managed AccountRef/settings. Validate each emitted attempt's own initial/terminal equality and real parent record; check selected socket counts separately from HTTP and probes. The originaltransport reachability target is canonical origin with no plan identity, as its existing Request producer already defines. No revision/route ID fabrication.

- [ ] **Step 3: Add bounded Hold/fault/stream-disconnect cases and run baseline.** `TestActualRuntimeOriginalForwardHoldAndFailure` iterates this exact table:

| Family | Cases |
| --- | --- |
| raw | G Hold/edit, FP Hold/edit, FD/B Hold/edit; G held cancel/closed-proxy probe; FP/G selected-proxy loss; FD/B target-dial failure and strict-TLS failure |
| original_model | G Hold/edit concrete Provider probe; G held cancel/failed-probe; FD/B target-dial/TLS failure; G stream_disconnect |
| transformed | G/FP/FD/B Hold/edit; G held cancel/failed-probe; FP/G selected-proxy loss; FD/B target-dial/TLS failure; G stream_disconnect |

Hold observes actual target before edit; preserve each family's target shape/kind and concrete Runtime prober. Successful TLS probes cause HTTP0, then exactly one GET/POST on old route; canceled/failed probes never release payload, replay or create a second operation. Each `stream_disconnect` sends once, reads first flushed delta then cancels/closes downstream; join server producer and Runtime operation, preserve frozen failed/canceled terminal semantics, no replay to current default. Use a fresh fixture for faults and explicit join/release on every assertion failure. No live user traffic, no redirect/target/private-peer exceptions, no prober stubs. If unchanged source passes, record tests-only evidence; if an actual RED identifies a defect, submit a narrow source-backed correction under this task's authority before editing production.

- [ ] **Step 4: Targeted normal/race and one affected consolidation cohort.** Record Task3 normal/race and the single affected existing/new cohort in Task3 SDD; do not rerun unrelated owner/login/native/blind/UI packages. Require independent spec/security/quality approval of Task3; this is bounded family closure, not whole Task6/Phase-B/release qualification.

```sh
GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/productruntime -run '^TestActualRuntimeOriginal(RawEgressMatrix|TransformedEgressMatrix|ForwardHoldAndFailure)$' -count=1 -timeout=150s -v
GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test -race ./internal/productruntime -run '^TestActualRuntimeOriginal(RawEgressMatrix|TransformedEgressMatrix|ForwardHoldAndFailure)$' -count=5 -timeout=360s -v
GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/productruntime -run '^Test(ActualRuntime(Captured(ManagedReadConcreteRecovery|AccountRead(EgressMatrix|HoldAndFailure|Authority))|SemanticOriginalHoldAndFailure|Original(RawEgressMatrix|TransformedEgressMatrix|ForwardHoldAndFailure|AccountReadUsesFrozenGlobalExitThroughSOCKSTLS|CredentialModelUsesFrozenGlobalExitThroughSOCKSTLS)|SemanticClientCredentials(UseGlobalExitWithoutAccount|RespectFlowAndBuiltin))|ManagedAccountQueriesThroughCONNECT|OriginalAccountQueriesThroughCONNECT|CapturedAccountReadUsesFixedAccountBeforeAnyGeneration|CapturedAccountReadDoesNotRunAnUnboundTurnSelector|CapturedAccountReadRechecksRevokedAssociation)$' -count=1 -timeout=240s -v
```

## Planning self-review and handoff

Root Task3 harness preflight: the new table has44 cases/count5=220 executions with stream work. Task1's fresh-Store race275 executions took268.460s; Task2's55 took54.049s. Set only the Task3 aggregate race watchdog to360s to allow measured setup plus stream/transform work. Keep the exact union/count5 and all individual request/observation/join safeguards unchanged. This is a test-execution budget, not a user latency criterion or a product-timeout change; incomplete/failed runs remain failures to diagnose, never inferred passes.

Three independent reviewer gates: captured actual Manager/Reader authority plus recovery producer; semantic client-credential evidence; original raw/transformed physical stream behavior. There is no task per matrix cell. Shared setup belongs to Task1 and travels as an explicit dispatch sidecar. All expected routes are input facts; real sockets/probes, HTTP sends, account effects, SQL IDs and lifecycle drain are independently observed. Prior semantic four-layer/frozen/down and original G evidence is carried without reimplementation.

No unresolved pre-dispatch design choice was found at the source pin: a closed captured quota/history producer can revalidate the actual constructor/lease/plan and project only TLS reachability while preserving send/audit Environment authority. Implementation must first demonstrate the actual Runtime RED and stop only if that authority proof requires a broader interface/architecture decision. Main reads/self-reviews these docs, closes the active writer, supplies the reviewed base and commits/dispatches sequentially. Final Task6 consolidation, retained managed P3 audit assertion and two-Resume UX limits, Phase-C/client/installed/soak work retain their existing owners and gates.
