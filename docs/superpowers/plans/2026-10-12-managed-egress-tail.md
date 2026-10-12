# Managed Egress Tail Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. This draft does not dispatch implementation; root owns scheduling, independent review, and all Git actions.

**Goal:** Close the two approved B2 managed-egress evidence gaps: successful rotation followed by selected-proxy loss before the first model send, and an independently specified next-operation audit expectation.

**Architecture:** Extend the existing contained Runtime fixture only. A request-aware test hook pauses the authenticated, audited first model request before the real selected SOCKS dial; the test verifies persisted rotation, closes that proxy, changes the default, and releases the dial. Identify the priority matrix's next operation by its new attempt ID and compare it against literal row inputs.

**Tech Stack:** Go1.26.9 via `/tmp/vibermate-go-security.JcRyfL/go/bin/go` with `GOTOOLCHAIN=local GOFLAGS=-mod=readonly`; existing Go testing, temporary Runtime SQLite Store and host SecretStore, private strict TLS server, real loopback SOCKS5 fixture, channels, and atomics; no new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-05-global-default-egress-design.md`; carried managed row in `.superpowers/sdd/2026-10-05-global-default-egress/phase-b-remaining-audit.md` and scoped approval in `task-6-managed-2026-10-09-review.md` beside it.

## Global Constraints

- Approved priority: account explicit > applicable flow explicit > global > persisted unset builtin Direct. Explicit Direct remains explicit.
- “代理不可用、DNS/TLS 失败、取消、配置读取失败，都不能触发直连或换代理重试。”
- “每次新出站逻辑操作在 Offline 排队、凭据刷新、DNS、连接或出站审计开始之前选定一次实际出口”； the token and dependent model retain that selection.
- “不新增独立代理服务、泛化 URL 转发接口或新的运行时依赖。”
- Keep existing authority, TLS verification, token body ownership, account settings and secret epochs, audit parent identity, and release ownership.
- B1 captured/original precedes this B2; B3 existing-native, B4 blind, and B5 candidate/release follow. This plan does not change or start that order.
- The accepted six-layer matrix, token producer/prober, Hold, metadata, owned operations, OAuth, UI, and native work are not reopened. Preserve the accepted two-Resume managed Hold boundary for later review; no Resume UX redesign.
- Missing evidence is not a behavioral failure. An unchanged-production PASS is valid closure. Production edits require an actual behavioral failure and root's explicit ruling on its narrow correction; never manufacture RED.
- No package-wide campaign, real provider/account requests, installed transport qualification, or full production/release claim. Root alone performs Git after independent review.

---

## Pinned source and carried evidence

All source locations below refer to `d91ac52b66fc1c735cce2eafe09f93eafee700d9`, read through `git show`/`git grep` in `.worktrees/production-readiness`, which has no `.codegraph/`. Concurrent captured-account WIP is not evidence for this draft. Before implementation, root supplies the B1-reviewed execution baseline and checks only overlap with the named fixture; do not transplant current WIP into this plan's proof.

| Boundary | Exact pinned source / implication |
| --- | --- |
| Closed physical network | `internal/productruntime/egress_network_test.go:50–76`: only configured loopback proxies are dialed; optional literal canonical Direct targets map to the private TLS listener. Direct and denied dials are counted independently. |
| Real wire seam | Same file `:78–105`: destination whitelist, existing `before func()`, then `Builder.Dialer(policy)` and a fresh strict TLS `http.Transport`, keepalives disabled. A request-aware hook can run before that dial without a production seam. |
| SOCKS and worker ownership | Same file `:122–175`: exact SOCKS greeting/127.0.0.1:443 request, private upstream only, successful tunnel counter and joined copy goroutine; cleanup currently closes listener and waits workers. |
| Approved managed fixtures | Same file `:192–219` and `:248–589`: priority rows, synthetic expired credential, actual Manager/capture/published Environment/compiler/Runtime, private root trust and real CONNECT client. Temporary Store/host secrets originate in `runtime_test.go:416–443`. |
| Existing HTTP failures | Same file `:620–675`: token503/model503 are completed physical HTTP exchanges, not post-rotation network-loss proof. |
| Actual P3 | Same file `:750–784`, specifically `:760–762`; next request at `:815–838`. Expected route is currently switched only if observed ProxyID is already `profile.runtime-other`. Physical next route is independently counted. The old source citation is still exact at this pin. |
| Rotation assertion | Same file `:1178–1204`: `assertRuntimeManagedSecret` reads a specific epoch, destroys copied material, and compares synthetic access/refresh/account bytes. Also inspect latest metadata revision to exclude a second rotation. |
| Real production send boundary | `internal/providertransport/client.go:302–317,346–363,386–425`: acquire, authenticate at frozen epoch, append durable start audit, then transport. Transport errors complete Failed and return at `:432–458`; `:523–551` maps connection refusal to `connection_failed`. `:295–300,838–850` releases provider egress on error. |
| Rotation and account ownership | `internal/provideraccount/manager.go:722–766`: resolve before Prepare, retain resolved selection, observe prepared epoch, return real lease. `internal/exchange/pipeline.go:341–355` releases material after candidate error; `selection.go:87–90` calls its release, and `manager.go:1032–1037` is idempotent. This source ownership is not a separately counted account-lease observation. |
| Truthful token audit | `runtime_fetch.go:365–389,415–440,546–621`: own RuntimeAction parent and account decision; response body completion and separate action release. Provider audit has UpstreamAttempt parent at `client.go:1097–1109`. Do not force token and model to share a parent. |
| Checked plan types | `internal/egressprofile/default.go:63–69,84–87`: `SetDefault` returns `DefaultSetting` with `Revision uint64`. `internal/egressaudit/attempt.go:87–106`: `PurposeCredentialRefresh` maps to `AuthorityRuntime`; `PurposeProviderAttempt` maps to `AuthorityEnvironment`. |

Carried evidence is the 2026-10-09 independent spec/security and quality approval, the recorded managed normal/race cohorts, six positive priority rows, HTTP503 effects, frozen send/default edit, held G's two concrete targets/two Resume actions, cancellation/retained refresh, per-attempt audit and drain. The review expressly excludes successful rotation then pre-model proxy loss and flags P3 as a test weakness, with no actionable production defect. Those records are prior scoped evidence, not newly executed tests or current full-family acceptance.

### Task 1: Prove post-rotation proxy loss and make next-operation audit expectations independent

**Files:**

- Modify/Test: `internal/productruntime/egress_network_test.go`: additive request-aware wire hook, count all selected-proxy dial attempts, expose bounded SOCKS worker drain, one new managed test branch, literal matrix inputs and attempt-ID-based expectations. Keep the existing helper and fixture authority.
- Read only unless root rules after actual behavioral failure: `internal/providertransport/client.go`, `internal/provideraccount/manager.go`, `internal/exchange/pipeline.go`, `internal/exchange/selection.go`, `internal/providertransport/runtime_fetch.go`. No production correction is pre-authorized or preselected by this draft.
- Execution evidence: root allocates the B2 manifest/report/review/log paths in the active SDD directory before execution; record the actual baseline, changed-file hashes, exact commands/results and conditional ruling there. This planning draft does not allocate a new implementation artifact tree.

**Interfaces:**

- Retain `runtimeSOCKSFixture(t *testing.T, upstream string) (net.Listener, *atomic.Int64)` and `runRuntimeInheritedSOCKSTLS(t *testing.T, model, managed, accountOverride bool, semanticCases ...runtimeSemanticCase)`.
- Add `runtimeOriginalWire.beforeRequest func(*http.Request) error`; configure before starting requests and never mutate it concurrently. It observes only the closed existing requests, returns context errors on teardown, and otherwise permits the real transport error. Keep existing `before func()` users unchanged.
- Add `runtimeClosedNetwork.proxyDials atomic.Int64`; increment once after the exact proxy-address allowlist check and before actual `net.Dialer.DialContext`. Existing `direct`, `denied` and successful SOCKS `calls` keep their distinct meanings.
- Return a test-only `*runtimeSOCKSListener` as the existing `net.Listener`; `Drain(context.Context) error` closes the owned listener once and joins existing workers. No production APIs are added.
- Add `TestActualRuntimeManagedRotationThenProxyLossBeforeModelSend`, calling the real managed helper with failure selector `post_rotation_proxy_loss`; this branch does not use Offline Hold or Resume.
- Extend `runtimeManagedCase` with `nextSource egressprofile.SelectionSource`, `nextDefaultRevision uint64`, `nextProfileID string`. Six literal rows define expectations for both operations.

- [ ] **Step 1: Add the narrowly observable fixture boundaries.**

Add `beforeRequest` to `runtimeOriginalWire`, invoke it after the destination whitelist and existing `before`, before `p.builder.Dialer(policy)`:

```go
if p.beforeRequest != nil {
    if err := p.beforeRequest(r); err != nil { return nil, err }
}
```

Add `proxyDials` to `runtimeClosedNetwork` and insert `n.proxyDials.Add(1)` immediately before its allowed proxy dial at pinned line69, after the rejection branch. Add this test-only listener wrapper:

```go
type runtimeSOCKSListener struct {
    net.Listener
    workers *sync.WaitGroup
    once sync.Once
    drained chan struct{}
}
func (l *runtimeSOCKSListener) Drain(ctx context.Context) error {
    l.once.Do(func() {
        _ = l.Listener.Close()
        go func() { l.workers.Wait(); close(l.drained) }()
    })
    select {
    case <-l.drained: return nil
    case <-ctx.Done(): return ctx.Err()
    }
}
```

Replace only the existing SOCKS helper's cleanup/return at pinned lines173–174, after its worker startup:

```go
owned := &runtimeSOCKSListener{Listener: listener, workers: &workers, drained: make(chan struct{})}
t.Cleanup(func() {
    ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
    defer cancel()
    if err := owned.Drain(ctx); err != nil { t.Error("private SOCKS workers did not drain", err) }
})
return owned, count
```

The accept worker's existing initial WaitGroup entry prevents a zero count while it can add connection workers. Closing the listener precedes Wait. Keep current connection deadlines/copy joins, exact SOCKS bytes, private upstream, strict roots/hostnames, and disabled keepalives.

- [ ] **Step 2: Add one actual Runtime test for rotation then physical proxy loss.**

```go
func TestActualRuntimeManagedRotationThenProxyLossBeforeModelSend(t *testing.T) {
    runRuntimeInheritedSOCKSTLS(t, true, true, false,
        runtimeSemanticCase{managedFailure: "post_rotation_proxy_loss"})
}
```

Add a dedicated `if managedFailure == "post_rotation_proxy_loss"` branch after the captured request headers at pinned lines618–619 and before the existing HTTP503 branch. The existing failure selector already enables the contained Direct mapping at line344: this makes a forbidden fallback capable of reaching the TLS server, and therefore visible in both Direct and model counters. Use the following branch code:

```go
if managedFailure == "post_rotation_proxy_loss" {
    wire.before = nil // This branch owns the edit after persisted rotation.
    type sendEvent struct { start egressaudit.Attempt; authOK bool; err error }
    events := make(chan sendEvent, 4)
    release := make(chan struct{})
    var releaseOnce sync.Once
    var tokenDispatches, modelDispatches atomic.Int64
    wire.beforeRequest = func(req *http.Request) error {
        purpose := egressaudit.PurposeCredentialRefresh
        authOK := true
        if req.URL.Host == "auth.openai.com" && req.URL.Path == "/oauth/token" {
            tokenDispatches.Add(1)
            authOK = req.Method == "POST" && req.Header.Get("Authorization") == ""
        } else if req.URL.Host == "chatgpt.com" && req.URL.Path == "/backend-api/codex/responses" {
            purpose = egressaudit.PurposeProviderAttempt
            modelDispatches.Add(1)
            authOK = req.Method == "POST" && req.Header.Get("Authorization") == "Bearer "+newToken && req.Header.Get("ChatGPT-Account-Id") == "synthetic-runtime-account"
        } else { return errors.New("managed tail hook rejected destination") }
        page, err := r.EgressAttempts().List(req.Context(), egressaudit.PageRequest{Limit: 10})
        var start egressaudit.Attempt
        matches := 0
        if err == nil {
            for _, record := range page.Items {
                if !record.Attempt.Terminal() && record.Attempt.Purpose() == purpose {
                    start = record.Attempt
                    matches++
                }
            }
            if matches != 1 { err = fmt.Errorf("send boundary has %d current audits", matches) }
        }
        select {
        case events <- sendEvent{start: start, authOK: authOK, err: err}:
        case <-req.Context().Done(): return req.Context().Err()
        }
        if purpose == egressaudit.PurposeProviderAttempt {
            select {
            case <-release:
            case <-req.Context().Done(): return req.Context().Err()
            }
        }
        return nil // The real closed-listener dial, not an injected error, fails.
    }
    type result struct { status int; err error }
    finished := make(chan result, 1)
    joined := false
    defer func() {
        releaseOnce.Do(func() { close(release) })
        cancelRequest()
        if !joined {
            select {
            case <-finished:
            case <-time.After(4*time.Second): t.Error("managed tail request did not join")
            }
        }
    }()
    go func() {
        response, err := client.Do(request)
        got := result{err: err}
        if response != nil {
            _, readErr := io.Copy(io.Discard, response.Body)
            closeErr := response.Body.Close()
            got.status = response.StatusCode
            got.err = errors.Join(got.err, readErr, closeErr)
        }
        finished <- got
    }()
    receive := func() sendEvent {
        t.Helper()
        select {
        case event := <-events:
            if event.err != nil || !event.authOK { t.Fatalf("invalid managed send boundary: %v authOK=%t", event.err, event.authOK) }
            return event
        case got := <-finished:
            joined = true
            t.Fatalf("managed tail ended before model dial gate: %+v", got)
        case <-time.After(2*time.Second): t.Fatal("managed tail send boundary not observed")
        }
        return sendEvent{}
    }
    tokenStart, modelStart := receive().start, receive().start
    if tokenStart.Purpose() != egressaudit.PurposeCredentialRefresh || modelStart.Purpose() != egressaudit.PurposeProviderAttempt || tokenStart.ID() == modelStart.ID() {
        t.Fatal("token/model attempt identities not distinct")
    }
    account, err := r.ProviderAccounts().Get(t.Context(), managedAccount.Account.ID)
    metadata, inspectErr := options.Secrets.Inspect(t.Context(), managedAccount.Account.SecretRef)
    if err != nil || inspectErr != nil || account.Account.ID != managedAccount.Account.ID || account.Account.SecretRef != managedAccount.Account.SecretRef || account.Health.CredentialEpoch != 2 || metadata.Revision != 2 || metadata.State != secretstore.StateConfigured || tokenDispatches.Load() != 1 || modelDispatches.Load() != 1 || refreshCalls.Load() != 1 || upstreamCalls.Load() != 0 || calls.Load() != 1 || network.proxyDials.Load() != 1 || network.direct.Load() != 0 || network.denied.Load() != 0 || bad.Load() != 0 {
        t.Fatal("model gate was not after one successful persisted rotation and before first model dial", err, inspectErr)
    }
    assertRuntimeManagedSecret(t, options.Secrets, managedAccount, 2, newToken, "synthetic-refresh-new")
    page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
    if err != nil || len(page.Items) != 2 { t.Fatal("pre-loss audit pair missing", err) }
    tokenCompleted := false
    for _, record := range page.Items {
        if record.Attempt.ID() == tokenStart.ID() {
            tokenCompleted = record.Attempt.Terminal() && record.Attempt.Outcome() == egressaudit.OutcomeCompleted
        }
    }
    if !tokenCompleted { t.Fatal("proxy loss preceded token completion") }
    if err := proxy.Close(); err != nil { t.Fatal("close selected fixture proxy", err) }
    edited, err := r.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 2})
    if err != nil || edited.Revision != 3 || edited.Profile != nil { t.Fatal("default clear at model boundary failed", err) }
    releaseOnce.Do(func() { close(release) })
    select {
    case got := <-finished:
        joined = true
        if got.err != nil || got.status < 400 { t.Fatalf("proxy loss did not become Runtime failure: %+v", got) }
    case <-time.After(2*time.Second): t.Fatal("failed model request did not return")
    }
    drain, cancel := context.WithTimeout(t.Context(), 4*time.Second)
    defer cancel()
    if err := gate.Drain(drain); err != nil { t.Fatal("managed tail gate did not drain", err) }
    snapshot := gate.Snapshot()
    if snapshot.ActiveActions != 0 || snapshot.EnteringActions != 0 || snapshot.ActiveEgress != 0 || snapshot.QueuedRequests != 0 || snapshot.HeldBytes != 0 {
        t.Fatal("managed tail retained gate ownership", snapshot)
    }
    if err := proxy.(*runtimeSOCKSListener).Drain(drain); err != nil { t.Fatal("managed tail SOCKS workers did not drain", err) }
    account, err = r.ProviderAccounts().Get(t.Context(), managedAccount.Account.ID)
    metadata, inspectErr = options.Secrets.Inspect(t.Context(), managedAccount.Account.SecretRef)
    if err != nil || inspectErr != nil || account.Health.CredentialEpoch != 2 || metadata.Revision != 2 || tokenDispatches.Load() != 1 || modelDispatches.Load() != 1 || refreshCalls.Load() != 1 || upstreamCalls.Load() != 0 || calls.Load() != 1 || network.proxyDials.Load() != 2 || network.direct.Load() != 0 || network.denied.Load() != 0 || bad.Load() != 0 {
        t.Fatal("proxy loss replayed token/model, rotated again or fell back", err, inspectErr)
    }
    assertRuntimeManagedSecret(t, options.Secrets, managedAccount, 2, newToken, "synthetic-refresh-new")
    page, err = r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
    if err != nil || len(page.Items) != 2 { t.Fatal("proxy loss created extra/missing attempts", err) }
    starts := map[string]egressaudit.Attempt{tokenStart.ID(): tokenStart, modelStart.ID(): modelStart}
    for _, record := range page.Items {
        audit := record.Attempt
        start, known := starts[audit.ID()]
        decision := audit.Decision()
        wantOutcome, wantError := egressaudit.OutcomeCompleted, ""
        if audit.ID() == modelStart.ID() { wantOutcome, wantError = egressaudit.OutcomeFailed, "connection_failed" }
        if !known || !audit.Terminal() || audit.Outcome() != wantOutcome || audit.ErrorClass() != wantError || audit.Decision() != start.Decision() || audit.Parent() != start.Parent() || audit.Purpose() != start.Purpose() || audit.TargetOrigin() != start.TargetOrigin() || audit.StartedAt() != start.StartedAt() || decision.SelectionSource != egressprofile.SelectionGlobal || decision.DefaultRevision != 2 || decision.ProxyID != "profile.runtime-original" || decision.ProxyRevision != 1 || decision.AccountID != "account.private-runtime" || decision.AccountSettingsRevision != 1 {
            t.Fatal("post-rotation loss changed per-ID audit identity/outcome", audit)
        }
        if audit.ID() == tokenStart.ID() && (audit.Parent().Kind != egressaudit.ParentRuntimeAction || audit.TargetOrigin() != "https://auth.openai.com" || decision.Authority != egressaudit.AuthorityRuntime || decision.PolicyID != "account.private-runtime") { t.Fatal("token authority changed", audit) }
        if audit.ID() == modelStart.ID() && (audit.Parent().Kind != egressaudit.ParentUpstreamAttempt || audit.TargetOrigin() != "https://chatgpt.com" || decision.Authority != egressaudit.AuthorityEnvironment || decision.PolicyID != environmentID.String()) { t.Fatal("model authority changed", audit) }
    }
    return
}
```

The result/authority types in this block were checked at the pin as recorded above. The fixture only pauses before first model socket acquisition, after authentication and audit. Deadlines are failure guards, not sequencing sleeps. No second request is sent in this loss case; the existing matrix owns next-operation evidence. Do not assert model HTTP receipt, model success, or zero audit BytesOut merely because the remote model POST count is zero: production audit records logical request-body size on connect failure.

- [ ] **Step 3: Run the new behavior once on existing production.**

Root supplies the B1-reviewed execution baseline and identifies any overlap with the pinned fixture/type declarations above. Format the one test file and run:

```sh
gofmt -w internal/productruntime/egress_network_test.go
GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/productruntime -run '^TestActualRuntimeManagedRotationThenProxyLossBeforeModelSend$' -count=1 -timeout=60s -v
```

Expected behavioral result: token completed/epoch2; physical selected-proxy connect failure at first model dispatch; token1/model0/rotation1, successful SOCKS1/proxy dial2/Direct0/denied0, exactly two immutable per-ID audits with distinct truthful outcomes and parents, drained gate/workers and normal Runtime shutdown. Compile/fixture errors are not behavioral RED. If the behavior passes unchanged production, preserve that PASS as the missing-evidence closure. If it fails behaviorally, retain raw failure/counters/snapshots, release/join teardown, and stop production edits until root rules; no speculative production patch or broad prober/selection change.

- [ ] **Step 4: Replace P3 with literal next expectations and ID-based membership.**

Replace the six matrix row literals with these independently supplied next fields:

```go
type runtimeManagedCase struct {
    choice string
    source egressprofile.SelectionSource
    profileID string
    defaultRevision, settingsRevision uint64
    nextSource egressprofile.SelectionSource
    nextDefaultRevision uint64
    nextProfileID string
}
```

Use these six rows in the existing `[]runtimeManagedCase` literal:

```go
{choice: "account_proxy", source: egressprofile.SelectionAccount, profileID: "profile.runtime-original", settingsRevision: 2, nextSource: egressprofile.SelectionAccount, nextProfileID: "profile.runtime-original"},
{choice: "account_direct", source: egressprofile.SelectionAccount, profileID: "profile.direct", settingsRevision: 2, nextSource: egressprofile.SelectionAccount, nextProfileID: "profile.direct"},
{choice: "flow_proxy", source: egressprofile.SelectionFlow, profileID: "profile.runtime-original", settingsRevision: 1, nextSource: egressprofile.SelectionFlow, nextProfileID: "profile.runtime-original"},
{choice: "flow_direct", source: egressprofile.SelectionFlow, profileID: "profile.direct", settingsRevision: 1, nextSource: egressprofile.SelectionFlow, nextProfileID: "profile.direct"},
{choice: "global", source: egressprofile.SelectionGlobal, profileID: "profile.runtime-original", defaultRevision: 2, settingsRevision: 1, nextSource: egressprofile.SelectionGlobal, nextDefaultRevision: 3, nextProfileID: "profile.runtime-other"},
{choice: "builtin", source: egressprofile.SelectionBuiltin, profileID: "profile.direct", defaultRevision: 1, settingsRevision: 1, nextSource: egressprofile.SelectionGlobal, nextDefaultRevision: 2, nextProfileID: "profile.runtime-other"},
```

Declare `initialIDs := map[string]bool{}` before `checkAudit` at pinned line750. Inside that closure add `newModels := 0`; replace only the observed-ProxyID conditional at lines760–762 with:

```go
if !next {
    initialIDs[audit.ID()] = true
} else if !initialIDs[audit.ID()] {
    newModels++
    if audit.Purpose() != egressaudit.PurposeProviderAttempt {
        t.Fatal("next managed request introduced another token attempt", audit.ID())
    }
    wantSource, wantRevision, wantID = matrix.nextSource, matrix.nextDefaultRevision, matrix.nextProfileID
}
```

After the existing token/model count check add:

```go
if len(initialIDs) != 2 || (next && newModels != 1) {
    t.Fatalf("initial/next attempt membership initial=%d newModels=%d", len(initialIDs), newModels)
}
```

The first `checkAudit(2,false)` saves the initial IDs. `checkAudit(3,true)` then requires exactly one new model ID and preserves the initial decisions' original literal expectations; the new decision never supplies its own expected selection. Keep the existing before-send `started` map, independent physical A/B/Direct counters, account settings/epoch/secret checks, target assertions, and drain.

- [ ] **Step 5: Run only the bounded normal and race cohorts and retain truthful evidence.**

```sh
GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/productruntime -run '^TestActualRuntimeManaged(RotationThenProxyLossBeforeModelSend|RefreshAndModelPriorityMatrix)$' -count=1 -timeout=90s -v
GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test -race ./internal/productruntime -run '^TestActualRuntimeManaged(RotationThenProxyLossBeforeModelSend|RefreshAndModelPriorityMatrix)$' -count=5 -timeout=300s -v
GOTOOLCHAIN=local GOFLAGS=-mod=readonly /tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/productruntime -run '^TestActualRuntime(ManagedRefreshAndModelShareFrozenGlobalExitThroughSOCKSTLS|ExplicitAccountRefreshAndModelIgnoreUnavailableGlobal|ManagedRefreshHoldUsesConcreteProbers|ManagedRefreshFailureBoundaries|OriginalAccountReadUsesFrozenGlobalExitThroughSOCKSTLS|OriginalCredentialModelUsesFrozenGlobalExitThroughSOCKSTLS|SemanticClientCredentialsUseGlobalExitWithoutAccount|SemanticClientCredentialsRespectFlowAndBuiltin)$' -count=1 -timeout=120s -v
```

The first two commands cover the one new test plus six existing priority subtests; race count5 means 35 case executions, not a whole-family qualification. The final command covers only existing callers of the affected shared test fixture and preserves the adopted staged Hold boundary. Do not run package-wide tests/builds, network tools, or add dependencies as part of this unit. A failing required check is retained and classified; broaden checks only after root approves an actual production correction and its specific affected paths.

- [ ] **Step 6: Hand off one reviewable task; root owns integration.**

Record the execution baseline and one-file test hash/diff, command exit results and raw logs, literal expected/observed counters, pre-loss epoch2 token completion/model-start snapshot, final per-ID terminal decisions/parents, exact secret epoch checks without secret values, gate/socket joins, and Runtime shutdown. Report unchanged-production PASS honestly or the actual behavioral RED plus root ruling. Independent review assesses just these two gaps and the fixture containment; root performs any Git actions afterwards. Preserve B3/B4/B5 and the two-Resume review boundary. No execution-choice question or dispatch is part of this draft.

## Plan self-review

Root preflight correction (2026-10-12): actual `offlinehold/coordinator.go:656` makes SafeToDisconnect true only while Held, not merely when admission is empty. This new failure test stays Online, so require zero ownership counters and successful Drain rather than a false Held-only flag. Commands use the existing pinned Go1.26.9/read-only-module toolchain, not whichever `go` happens to be on PATH. Before B2 dispatch, account for the additional B1 consumers of the shared SOCKS helper and include their affected small regression selection; this draft does not grant final acceptance or start execution.

One task covers precisely the approved two gaps. The new loss case uses real Store/Manager/capture/compiler/Runtime, strict TLS and SOCKS; no response double or fake network exception substitutes for the fault. Literal next inputs and attempt-ID membership remove the observed-output expectation. The field/method/constant signatures in plan code match the pin, including `DefaultSetting.Revision` and token Runtime authority. Production corrections remain conditional. No tests, builds, network requests, Git mutations, source edits, or subagents were run during planning.
