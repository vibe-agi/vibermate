# Blind/Cleartext Egress Closure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement the single task below. Root owns dispatch, Git and independent review. This document authorizes planning only; execution follows B1 → B2 prepared → B3 prepared → B4 → B5 freeze/release.

**Goal:** Close the existing blind/cleartext missing evidence cells using real admitted sends, persistent defaults/audit and concrete blind transport/probes, without expanding network authority.

**Architecture:** Keep the production Handler → frozen Resolved → actual blind Dialer → Offline Gate → policy dialer path. Add a bounded local Runtime test fixture for builtin CONNECT and generic HTTP, plus an armed configuration-read fault in the existing authenticated Handler fixture. Missing evidence may pass with unchanged production; no product correction is authorized without an actual behavioral RED and root ruling.

**Tech Stack:** Existing Go/SQLite/net/http/SOCKS fixtures; executable `/tmp/vibermate-go-security.JcRyfL/go/bin/go` (Go1.26.9), `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly`; go.mod stays unchanged.

**Spec:** `docs/superpowers/specs/2026-10-05-global-default-egress-design.md`; parent `docs/superpowers/plans/2026-10-05-global-default-egress.md`, Tasks4/6. Evidence: `.superpowers/sdd/2026-10-05-global-default-egress/{phase-b-remaining-audit,task-4-report,task-4-final-results,task-4-review}.md`.

## Global Constraints

- Planning source pin: `fc82acf0a81d21ecb0abac402ce7315a3ae8eaa9`; all product/test reads used `git show`/`git grep` at that commit. No `.codegraph/` here. Ignore B1 review WIP. Root must reconcile the execution source after B1/B2/B3 without silently widening this plan.
- Original Task4 closed at `279f85c75e6feef7062b5010752f9b04ba7a407d`, independently approved with no findings. Carry its G blind live/frozen/held/down and G cleartext/header/default-port/audit evidence; do not reopen its startup/probe-allowance/default-port fixes or whole-package campaigns.
- Generic blind branches have no applicable Account/Client Flow. Preserve `Resolve(ctx, Inherit(), Inherit())`, source `global`/`builtin` only, after real admission and before action/outbound audit/Hold/DNS/dial. Attributed Environment is not a borrowed flow. Configuration error never means unset.
- Preserve existing admitted `blind.fixture:443` and `blind.fixture:80` fixture targets, current exact network rule/Capture authority and ingress `TargetAccessPublicOnly`. The existing explicit exact-host/port owner rule produces `TargetAccessOwnerConfigured`; the default supplies no permission. No new allowed targets, private exemption, payload inspection, provider path, public hook, UI or future MCP work.
- Blind probes remain opaque TCP connect-close, no TLS/HTTP/read/write claim; actual queued `TargetAccess`, policy and identities survive unchanged. Production15s Dial and5s probe bounds remain. No fake successful permission, sender or prober; fault doubles may supply configuration errors only.
- Retain generic HTTP origin-form/header/body behavior and provider/control/callback direct-only exceptions. No `HTTP_PROXY`, default-transport substitution, retries or fallback. HTTP503 is not selected-proxy loss.
- Planning runs no tests/builds/network/provider/user-state operations or Git mutations. Execution uses private temporary paths/listeners only and records exact commands, first failures, hashes, audit IDs, terminal results and cleanup. Broader full-client/schema/installed qualification remains B5/Phase C.

## Carried proof and precise missing cells

| Carried, accepted proof | New evidence required by this task |
| --- | --- |
| `TestActualRuntimeBlindDefaultEditPreservesLiveTunnel`, `TestActualRuntimeBlindHoldProbesAndSendsFrozenDefault`, public-only refusal and original module/Handler down controls | Actual persisted-unset builtin CONNECT and generic cleartext sends; a real DefaultManager read failure before blind egress |
| `TestAuthorizedBlindForwardUsesRealGlobalSOCKSAndAudit`, omitted/explicit80 journal and malformed-denial controls | Generic cleartext selected-proxy loss with actual failed connection and zero Direct fallback; actual Runtime Hold/edit/concrete probe/send, queued cancellation, failed probe and released-Dial failure, all drained |

The historical whole-loopback180s failure and L1 partial-transcript limit remain recorded. New focused execution does not retroactively replace them or establish shipping/all-family acceptance.

## Files and current interfaces

One task is meaningful to review: its fixture, positive paths, failure boundaries and per-ID audit jointly establish this family. Do not create a task per row.

**Create:** `internal/productruntime/blind_cleartext_egress_closure_test.go`: local closed Runtime fixture, six Runtime test entry points with subcases, deterministic barriers, wire/SQL assertions and cleanup. Keep helpers private to this file.

**Modify tests only:** `internal/loopbackproxy/blind_default_integration_test.go`: armed repository fault and `TestBlindDefaultReadFailureBeforeNetwork` with CONNECT/cleartext subcases. `internal/loopbackproxy/handler_integration_test.go`: one optional test-composition repository decorator, applied before constructing the existing DefaultManager; no production options change.

**Read-only production responsibilities:** `loopbackproxy/handler.go:2449–2970` authorizes/freezes/audits and calls both blind branches; `blindtunnel/dialer.go:53–140` freezes target and performs actual Dial; `blindtunnel/probe.go:69–155` concrete reachability; `productruntime/{runtime,builders,offline}.go` composes the one manager/handler, concrete prober and Resume dispatcher. Production edits require the separate RED/ruling boundary below.

**Current reusable signatures (not new exports):**

```go
// productruntime package tests:
type runtimeBlindNetwork struct { allowed map[string]bool; denied atomic.Int64 }
func (*runtimeBlindNetwork) DialContext(context.Context, string, string) (net.Conn, error)
func (*runtimeBlindNetwork) LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
func runtimeSOCKSFixture(*testing.T, string) (net.Listener, *atomic.Int64)
func runActualRuntimeBlindDefault(*testing.T, bool, bool)
func waitForQueuedTargets(*testing.T, *offlinehold.Gate, int)
func shutdownRuntime(*testing.T, *Runtime) // bounded10s Runtime.Shutdown
// runtimeBuildOverrides has blindProber func() (*blindtunnel.ReachabilityProber, error), no proxy/default override.

// loopbackproxy_test package:
func newBlindPrivateProxy(*testing.T, string, uint16) *blindPrivateProxy
func newContainedBlindProxyFixture(*testing.T, *blindPrivateProxy, ...func(*loopbackproxy.Options)) (*proxyFixture, *blindContainedNetwork)
func newContainedBlindProxyFixtureWithPolicy(*testing.T, *blindPrivateProxy, connectionpolicy.Snapshot, context.Context, ...func(*loopbackproxy.Options)) (*proxyFixture, *blindContainedNetwork)
func publishBlindDefault(*testing.T, *proxyFixture, *blindPrivateProxy) egressprofile.ProfileRevision
type proxyFixtureComposition struct {
    compiler environment.Compiler
    configure func(*environment.Environment)
    configureProxy func(*loopbackproxy.Options)
    ingressContext context.Context
}
// Current fixture owns real store/profiles/defaults/connections/egress/admissions/assignments.

// Concrete production dependencies:
func blindtunnel.NewDialerWithBuilder(offlinehold.Coordinator, *egressnetwork.Builder) (*blindtunnel.Dialer, error)
func blindtunnel.NewReachabilityProberWithBuilder(*egressnetwork.Builder) (*blindtunnel.ReachabilityProber, error)
func (*blindtunnel.Dialer) Dial(context.Context, blindtunnel.DialRequest) (net.Conn, offlinehold.Lease, error)
// DialRequest{RequestID,Action,Authority,Host,Port,Egress}; no success adapter.
func egressprofile.NewDefaultManager(egressprofile.DefaultRepository, egressprofile.Controller) (*egressprofile.DefaultManager, error)
```

`runtimeSOCKSFixture` currently accepts only the fixed443 SOCKS request, and `runActualRuntimeBlindDefault` permits only proxy sockets and aggregates terminal decisions by ProxyID. Do not change either shared helper to accommodate this task. The local fixture below supplies exact80/443 socket mapping and stronger own-start correlation.

### Task 1: Close remaining admitted blind/cleartext wire, Hold and failure evidence

**Interfaces produced:** local test fixture owns `*Runtime`, `*offlinehold.Gate`, exact contained builder, first/second SOCKS listeners, synthetic upstream, counters, channels and request workers. New entry points are `TestActualRuntimeBlindBuiltinUnset`, `TestActualRuntimeCleartextBuiltinUnset`, `TestActualRuntimeCleartextProxyLoss`, `TestActualRuntimeCleartextHoldFrozenDefault`, `TestActualRuntimeCleartextHoldCancellation`, `TestActualRuntimeCleartextHoldFailureBoundaries`; Handler read-fault entry point is `TestBlindDefaultReadFailureBeforeNetwork`. No later task consumes a new product interface.

- [ ] **Step 1: Add closed local fixtures before any behavioral execution.** Follow actual Runtime creation from `blind_default_test.go:104–240`: real Gate, `testOptions(t, hostcontract.Desktop(), gate)`, actual `blindTunnelDialer{coordinator: gate, dialer: dialer}`, real ManualCapture creation and SystemTransparent assignment. Configure the concrete prober on every fixture, including negative paths:

```go
builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{
    BaseDialer: network, SystemResolver: network,
})
if err != nil { t.Fatal(err) }
dialer, err := blindtunnel.NewDialerWithBuilder(gate, builder)
if err != nil { t.Fatal(err) }
options.BlindTunnels = &blindTunnelDialer{coordinator: gate, dialer: dialer}
options.OriginalTransport = &noNetworkOriginalTransport{}
r, err := startWithBuilders(t.Context(), options, runtimeBuildOverrides{
    blindProber: func() (*blindtunnel.ReachabilityProber, error) {
        return blindtunnel.NewReachabilityProberWithBuilder(builder)
    },
})
if err != nil { t.Fatal(err) }
```

Use a file-local network type with immutable `map[string]string` logical-to-physical destinations: proxy addresses map to themselves, only the case's exact `127.0.0.1:443` or `127.0.0.1:80` maps to its allocated private upstream. DNS allows `network=="ip" && host=="blind.fixture"` and returns only127.0.0.1; all other DNS returns an error without OS lookup. Count direct-target calls separately from unlisted rejected calls, selected proxy attempts, accepted SOCKS handshakes/onward sockets and origin HTTP requests. Proxy-case allowlists still include the valid Direct fixture mapping, so a fallback creates a measurable wrong Direct socket rather than being hidden by a refusal-only counter. The upstream accepts only these already-admitted logical target mappings; no listener accepts arbitrary destinations. Client ingress sockets have a separate exact Runtime listener allowlist.

Implement the local base dial with this ordering. Its constructor fills immutable maps before workers start; counters/barriers remain under the mutex. The keyed barriers cover the exact address and dial ordinal; register them before sending a request:

```go
type b4DialKey struct { address string; ordinal int }
type b4DialBarrier struct { entered chan struct{}; release chan struct{} }
type b4Network struct {
    destinations map[string]string
    mu sync.Mutex
    entries map[string]int
    physical map[string]int
    barriers map[b4DialKey]*b4DialBarrier
    unlisted atomic.Int64
    lookups atomic.Int64
}
func (network *b4Network) DialContext(ctx context.Context, kind, address string) (net.Conn, error) {
    destination, ok := network.destinations[address]
    if kind != "tcp" || !ok {
        network.unlisted.Add(1)
        return nil, errors.New("B4 blocked unlisted socket")
    }
    network.mu.Lock()
    network.entries[address]++
    barrier := network.barriers[b4DialKey{address, network.entries[address]}]
    network.mu.Unlock()
    if barrier != nil {
        close(barrier.entered)
        select {
        case <-barrier.release:
        case <-ctx.Done(): return nil, ctx.Err()
        }
    }
    network.mu.Lock()
    network.physical[address]++
    network.mu.Unlock()
    return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, kind, destination)
}
func (network *b4Network) LookupNetIP(_ context.Context, kind, host string) ([]netip.Addr, error) {
    network.lookups.Add(1)
    if kind != "ip" || host != "blind.fixture" {
        return nil, errors.New("B4 blocked unlisted DNS")
    }
    return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}
```

Take counter snapshots under `mu`. Cleanup closes each barrier.release through a fixture-owned sync.Once; only its one matching Dial closes entered. Each successful/failed real connection remains the result of net.Dialer, never this synchronization layer.

Local SOCKS accepts only no-auth SOCKS5 and exact IPv4 loopback plus the case's443/80 port, opens only the allocated upstream, returns the real success/failure reply and copies bytes with both copy workers joined. Before replay/copy, malformed handshake/extra attempts are recorded as fixture failures. One opaque probe reaches the upstream and closes with EOF and zero application bytes; any probe byte fails. The HTTP origin handler records exactly one prescribed method/path/headers/body per successful send. Observation occurs at the synthetic origin, not in the blind product or prober.

Ingress uses `TargetAccessPublicOnly`; persist `MatchExactHostPort("blind.fixture", port)` with `DecisionAllow`, `ModeDenyUnknown` through `r.ConnectionRules().Replace`, as original owner-rule coverage does. Assert queued allowance `TargetAccessOwnerConfigured`; do not infer it from loopback/proxy settings. CONNECT uses actual authenticated server/hijacker and opaque echo. Generic HTTP uses the actual authenticated Runtime Handler. Success/failure transport requests may use an actual private HTTP proxy client; the held-cancellation subcase invokes the same Handler with `httptest.NewRecorder` and a caller-cancelable request context, avoiding a race about when net/http notices an unread-body disconnect. This is caller-context cancellation proof, not a new disconnected-ingress claim.

- [ ] **Step 2: Add real persisted-unset CONNECT and cleartext evidence.** Each test has a fresh real Runtime Store. Publish `profile.b4-a` r1 (no send), set it using ExpectedRevision1 to setting2, then clear with ExpectedRevision2/Profile nil to setting3. Assert `GetDefault` returns `Revision:3, Profile:nil`; the actual Handler resolves the real manager. Do not use `RestoreResolved` to supply the selection. Only one admitted send per test:

```go
setting, err := r.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{
    ExpectedRevision: 2, Profile: nil,
})
if err != nil || setting.Revision != 3 || setting.Profile != nil {
    t.Fatalf("persisted unset setting=%+v err=%v", setting, err)
}
want := egressaudit.BuiltInDirectDecision(egressaudit.AuthorityNetwork)
want.ProxyID, want.ProxyRevision = "profile.direct", 1
want.SelectionSource, want.DefaultRevision = egressprofile.SelectionBuiltin, 3
```

Block the actual base Direct dial before socket creation. Read exactly one nonterminal durable attempt from `r.EgressAttempts()`, store it by its ID, then release the real socket. CONNECT must tunnel opaque bytes; cleartext must POST exact `/b4`, body `b4-opaque-body`, original `Authorization: Bearer client-b4`, `X-B4: retained`, plus proxy auth and `Connection: X-B4-Hop`/`X-B4-Hop: stripped`. The origin gets origin-form `/b4`, exact body and end-to-end headers; proxy authorization, Proxy-Connection and nominated/hop-by-hop headers disappear. One Direct target socket, zero proxy sockets and unlisted/DNS violations; HTTP1 for cleartext. No Environment/model/account payload processing. Close tunnel/response, then compare its own terminal with the captured start and literal builtin decision.

- [ ] **Step 3: Add the real DefaultManager read-failure negative.** In the existing Handler fixture, extend only `proxyFixtureComposition` with `defaultRepository func(egressprofile.DefaultRepository) egressprofile.DefaultRepository`; default nil preserves existing construction. Before `NewDefaultManager` at `handler_integration_test.go:1371`, apply the decorator to `store.EgressDefaultRepository()` while retaining the existing actual profiles manager:

```go
defaultRepository := store.EgressDefaultRepository()
if composition != nil && composition.defaultRepository != nil {
    defaultRepository = composition.defaultRepository(defaultRepository)
}
defaults, err := egressprofile.NewDefaultManager(defaultRepository, profiles)
```

Add this test-local fault; it delegates real reads during setup and is armed only after Capture/assignment/handler construction. It never returns a successful invented setting or permission:

```go
type blindDefaultReadFault struct {
    egressprofile.DefaultRepository
    armed atomic.Bool
    failures atomic.Int64
}
func (f *blindDefaultReadFault) LoadDefault(ctx context.Context) (egressprofile.DefaultSetting, error) {
    if f.armed.Load() {
        f.failures.Add(1)
        return egressprofile.DefaultSetting{}, egressprofile.ErrDefaultUnavailable
    }
    return f.DefaultRepository.LoadDefault(ctx)
}
```

Wire this through `newProxyFixtureConfigured` with the real contained Dial module and exact rule/ingress setup from `newContainedBlindProxyFixtureWithPolicy`, using the new decorator (do not add a production Runtime default override). Arm, issue one authenticated CONNECT and one ordinary cleartext request in separate fixtures/subcases, and assert503/sanitized blind failure, exactly one failed repository read, zero DNS/base/proxy/upstream sockets, Gate queued/actions/egress0, outbound attempts0. The connection's actual attempted/allowed/failed terminal journal remains visible with the same connection ID and host/port. Selection fails before an outbound ID/action/attempt exists; do not fabricate a failed outbound audit record. This is actual manager-read failure through admitted Handler, while Runtime composition is covered by Steps2/4/5.

- [ ] **Step 4: Add actual Runtime cleartext Hold/edit/concrete-probe/send and next-operation evidence.** Publish `profile.b4-a` r1 and `profile.b4-b` r1 at the first/second private SOCKS listeners. Persist A/default2. Enter real Hold, start exactly one authenticated cleartext POST. Wait for queued1 by condition under a bounded context; capture its nonterminal SQL attempt before Resume. Inspect `r.resumeProbeTargets()`:

```go
wantTarget := offlinehold.ProbeTarget{
    Kind: offlinehold.EgressBlindTunnel, Transport: offlinehold.ProbeTransportTCP,
    TargetRef: "blind.fixture:80", NetworkOrigin: "blind.fixture:80",
    HTTPAuthority: "blind.fixture:80", EgressPolicy: profileA.Policy,
    TargetAccess: egressnetwork.TargetAccessOwnerConfigured,
}
// Exact equality also proves plan/SNI fields remain zero/empty.
if len(targets) != 1 || targets[0] != wantTarget { t.Fatalf("queued target=%+v", targets) }
```

Set B using ExpectedRevision2 → default3 before Resume. Actual Runtime dispatcher invokes the concrete blind prober, opening/closing A once with zero application bytes, then actual Dial opens A once and sends the POST once. Capture no HTTP request during the probe. Drain the completed action and observe Online with active/queued/actions0 before the next operation; no second Resume or time-chosen winner. Start exactly one independent cleartext POST; block B's first actual Dial, capture that request's own start, then release. It uses B/default3 once. Final budgets: A probe1+send1, B send1, HTTP2, Direct0, unlisted0; immutable start/terminal decision A/global2 and B/global3 respectively. Do not derive expected route from terminal ProxyID or group attempts by profile.

- [ ] **Step 5: Add real cleartext loss, queued cancel and failed-probe/released-Dial boundaries.** Each row is a new fixture with A/default2, optionally change to B/default3 after its start is frozen; no recovery/retry request beyond the stated budget.

| Test/subcase | Deterministic fault boundary | Exact send/probe budget and end state |
| --- | --- | --- |
| `TestActualRuntimeCleartextProxyLoss` | At A's first base-dial entry, capture durable start; close and join A's listener, then release to the real net.Dialer; it must attempt that exact closed address and fail | A physical attempt1/accept0, HTTP0, B0/Direct0;502, one failed terminal with original global2; all actions/egress/queued0 |
| `TestActualRuntimeCleartextHoldCancellation` | Actual queued request1/start captured and edit to B3; cancel that request's context before Resume; wait worker result/terminal and queue removal | DNS/sockets/HTTP0; own canceled terminal/global2. No artificial probe of an empty queue and no late send after cleanup; active/actions/queued0 |
| `TestActualRuntimeCleartextHoldFailureBoundaries/probe` | Actual queued request1/start captured/edit B3; close/join A's listener before one real Resume | Concrete prober attempts A once/fails; no HTTP, B0/Direct0; Resume transport-unavailable and Held/queued1 retained. Cancel caller, join and assert its own canceled terminal/global2 and queue0 |
| `TestActualRuntimeCleartextHoldFailureBoundaries/released-dial` | Let the real A probe connect-close successfully. Block A's second base-dial entry (actual send); wait for it, close/join A listener, release to real net.Dialer | A physical attempts2/accept1/opaque probe1, HTTP0, B0/Direct0; one failed terminal/global2, active/actions/queued0 |

The fault is connection loss, not an origin503 or injected sender result. Counters must show attempted failed sockets even when no SOCKS handshake is accepted. In the released-dial case use the second base-dial entry as synchronization after the successful real probe, not a fake prober or a sleep after Resume. Run Resume in an owned worker if it otherwise waits across the barrier, and always cancel/join it. Request HTTP status reflects Handler behavior; queued context cancellation may not deliver a client response and must be judged by real canceled worker/terminal/gate outcomes.

- [ ] **Step 6: Assert every request's own durable start/terminal and connection parent.** Local helpers return stored `egressaudit.Attempt` starts from actual SQL before release (online) or while queued (held). For each terminal read by ID, require ID/StartedAt/Decision/Parent/Purpose/PayloadClass/Caller/TargetOrigin/ConnectionID unchanged; Parent.Kind is `ParentBlindConnection`, Parent.ID equals ConnectionID, Parent.ExchangeID empty, CallerCore, PurposeBlindTunnel, PayloadOpaqueTunnel, AccountID empty/AccountSettingsRevision0. CONNECT TargetOrigin is `https://blind.fixture:443`; generic cleartext is `http://blind.fixture:80`. Both remain blind purpose/payload, not original/provider/semantic requests. Require the exact own connection timeline to contain attempted/allowed/terminal phases and matching blind decryption/host/port.

```go
want := egressaudit.BuiltInDirectDecision(egressaudit.AuthorityNetwork)
want.PolicyID, want.PolicyRevision, want.RuleID = "runtime.egress-default", 2, "global.default"
want.ProxyID, want.ProxyRevision = "profile.b4-a", 1
want.SelectionSource, want.DefaultRevision = egressprofile.SelectionGlobal, 2
// For independent next B: PolicyRevision=3, ProxyID="profile.b4-b",
// SelectionSource="global", DefaultRevision=3; profile revision remains1.
if start.ID() != terminal.ID() || start.StartedAt() != terminal.StartedAt() ||
   start.Decision() != want || terminal.Decision() != want ||
   start.Parent() != terminal.Parent() || start.ConnectionID() != terminal.ConnectionID() {
    t.Fatalf("own attempt mutated: start=%+v terminal=%+v want=%+v", start, terminal, want)
}
```

Assert no duplicate or extra attempts/HTTP requests. Log own outbound ID, connection/parent ID, exact expected/start/terminal decision, outcome and socket counters. Default-read fault has only the genuine connection ID and zero outbound IDs. Successful body sends have positive framing-byte counts; failed connection/probe/cancel sends have zero application request count. Do not use positive bytes to infer content inspection or a TLS claim.

- [ ] **Step 7: Make cleanup unconditional before running tests.** Register cleanup immediately after creating each listener/context/channel/worker; no `t.Fatal` may precede its cleanup. Under a bounded cleanup context, cancel requests/Resume/barriers, close all owned clients and accepted/onward sockets and listener, initiate Gate shutdown when needed, join request/Resume/SOCKS/upstream/copy workers, drain Handler/Gate and call the existing bounded Runtime shutdown. Use done-channel selects with deadlines instead of unbounded `WaitGroup.Wait` or `<-done`; the shared helper's existing limitations are not copied into new failure paths. Test-owned context bounds8s, private sockets3s, cleanup10s; these are fixture guards, not product timeout changes. Snapshot must end queued0/activeegress0/activeactions0; terminal counts must match budgets even after failure. No sleeps choose the winner; bounded condition polling may observe a queue/terminal/drain predicate only.

- [ ] **Step 8: Run focused evidence with unchanged production first.** Compilation/setup/containment mistakes are not behavioral RED. If all new cases pass, record tests-only missing-evidence closure. If an actual behavior fails, preserve full first log, exact source/test hashes/counters/audit IDs and all cleanup results; root decides the smallest production correction and its affected consumers before any production edit. Do not manufacture RED by removing accepted fixes. Implement the six new Runtime entry points (seven cases including the two failure boundaries) and the real-read fault's two subcases, then run the scoped normal cohort once. During iteration focus only the specific failing case; there is no mandatory second individual run of every already passing case:

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOTOOLCHAIN=local GOFLAGS=-mod=readonly VIBERMATE_CODEX_ACCEPTANCE= VIBERMATE_NATIVE_CODEX160= /tmp/vibermate-go-security.JcRyfL/go/bin/go test -json -p=1 ./internal/productruntime ./internal/loopbackproxy -count=1 -timeout=180s -run '^(TestActualRuntimeBlindBuiltinUnset|TestActualRuntimeCleartextBuiltinUnset|TestActualRuntimeCleartextProxyLoss|TestActualRuntimeCleartextHoldFrozenDefault|TestActualRuntimeCleartextHoldCancellation|TestActualRuntimeCleartextHoldFailureBoundaries|TestBlindDefaultReadFailureBeforeNetwork)$' > .superpowers/sdd/2026-10-12-blind-cleartext-egress-closure/task-1-normal.jsonl 2>&1
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOTOOLCHAIN=local GOFLAGS=-mod=readonly VIBERMATE_CODEX_ACCEPTANCE= VIBERMATE_NATIVE_CODEX160= /tmp/vibermate-go-security.JcRyfL/go/bin/go test -json -race -p=1 ./internal/productruntime ./internal/loopbackproxy -count=5 -timeout=180s -run '^(TestActualRuntimeBlindBuiltinUnset|TestActualRuntimeCleartextBuiltinUnset|TestActualRuntimeCleartextProxyLoss|TestActualRuntimeCleartextHoldFrozenDefault|TestActualRuntimeCleartextHoldCancellation|TestActualRuntimeCleartextHoldFailureBoundaries|TestBlindDefaultReadFailureBeforeNetwork)$' > .superpowers/sdd/2026-10-12-blind-cleartext-egress-closure/task-1-race.jsonl 2>&1
```

Run commands serially, recording terminal exit before the next command; root owns process/log review. On an alarm preserve failure and drain, report the actual unfinished worker; do not repartition/increase budget or label it environmental without root ruling.

- [ ] **Step 9: Verify the actually affected shared fixture consumers, format and hand off for root review.** With production unchanged, the new Runtime fixture is local and does not change the existing Runtime/blindtunnel/network/Offline helpers. Their already-reviewed source evidence is carried, not a reason for another count5 race campaign. The optional repository-decorator field changes the shared loopback Handler test composition, so run this named existing consumer cohort once normally, in addition to the new normal/count5 race group above:

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOTOOLCHAIN=local GOFLAGS=-mod=readonly VIBERMATE_CODEX_ACCEPTANCE= VIBERMATE_NATIVE_CODEX160= /tmp/vibermate-go-security.JcRyfL/go/bin/go test -json -p=1 ./internal/loopbackproxy -count=1 -timeout=180s -run '^(TestBlindConfiguredRequiresDefaultResolver|TestBlindGlobalDefaultNeverGrantsTargetOrCapturePermission|TestBlindProxyFailureKeepsFrozenAuditWithoutDirectFallback|TestAuthorizedBlindForwardUsesRealGlobalSOCKSAndAudit|TestCleartextDefaultPortKeepsRealConnectionAudit|TestMalformedCleartextKeepsRefusalVisibleWithoutEgress|TestCleartextRequestIsForwardedToItsOrigin|TestGenericCleartextRequestNeverReachesTheModelPipeline|TestUnauthenticatedCleartextForwardIsAudited|TestBlindTunnelDoesNotDialBeforeAuditAppend|TestCleartextForwardDoesNotDialBeforeAuditAppend|TestOriginalOpaqueForwardParentMatchesActualRequest)$' > .superpowers/sdd/2026-10-12-blind-cleartext-egress-closure/task-1-affected-normal.jsonl 2>&1
```

Root creates this plan's own SDD workspace before execution; retain every unique log there without overwriting first failures. If actual product/shared-runtime source later needs a correction, root names its affected extra normal/race checks from the real diff. Final candidate-wide checks remain B5; this tests-only unit does not pre-run or waive them. Gofmt only the three owned test paths; verify format and root-owned diff checks. Record source pin at execution, changed file hashes and generated-log hashes, every first failure and resolution, exact per-cell source/counters/IDs/drain. Root performs independent spec/security/quality review and Git adoption; no worker commit or staging. Mark only this B4 family evidence closed after approval, leaving B5 freeze/release and wider production gates open.

## Planner self-review

Root preflight (2026-10-12): read the full268-line draft and planning report; checked the pinned Store.EgressDefaultRepository interface return and actual Handler DefaultManager construction, plus Runtime resumeProbeTargets signature. The fault decorator preserves real configuration authority. Removed mandatory individual reruns of already-passing new cases and five unchanged-package regression/race campaigns; retained the exact new normal/count5 race union and named normally affected shared Handler fixture consumers. Assigned executable evidence to this plan's own SDD, not the earlier captured plan's directory. Execution handoff reconciliation after B1 closure adds its accepted TestOriginalOpaqueForwardParentMatchesActualRequest to the normal shared Handler-composition cohort, because the new optional repository decorator also touches that fixture; no repeat of B1's Runtime/race matrices. This is a prepared B4 task, not executed proof or a production correction.

The one task covers only the audit row's E cells: real persisted-unset CONNECT/cleartext, genuine default-read error before network, actual cleartext proxy loss and Runtime Hold/edit/probe/cancel/failure/drain. It retains accepted G proof and exact admission/allowance/opaque probe/header/audit contracts. Fault injection decorates configuration only; all successful probes/sends are concrete. No source contradiction or production defect has been demonstrated at this pin. No execution took place while writing this plan.
