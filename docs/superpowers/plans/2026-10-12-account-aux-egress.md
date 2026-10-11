# Account auxiliary egress acceptance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fill the existing Task6 real-wire evidence gaps for manual OAuth refresh, owned endpoint models, and first OAuth login without changing their authority or cancellation contracts.

**Architecture:** Exercise a real temporary Runtime/Store, account Manager, host-selected synthetic SecretStore, strict private TLS server, real SOCKS5 listeners, and the Runtime's concrete Provider/Auxiliary Resume probers. Freeze expected selections from the test inputs before admission, record each SQL audit start by attempt ID at the send boundary, and compare its terminal independently. These are acceptance increments for implemented behavior; change production code only for a reproduced, operation-scoped defect, and escalate a contract change.

**Tech Stack:** Go module `go 1.26.0`, toolchain `go1.26.9`; use `/tmp/vibermate-go-security.JcRyfL/go/bin/go` with `GOTOOLCHAIN=local`; existing `testing`, `httptest`, `crypto/tls`, `egressnetwork`, `runtimepersistence`, `provideraccount`, `codexoauth`, `offlinehold`, and SQL egress audit.

**Spec:** `docs/superpowers/specs/2026-10-05-global-default-egress-design.md`; Task6 in `docs/superpowers/plans/2026-10-05-global-default-egress.md`. Gap/evidence authority: `.superpowers/sdd/2026-10-05-global-default-egress/phase-b-remaining-audit.md`.

## Global Constraints

- Approved precedence: “账号明确选择的出口 > 当前请求所属流量策略明确选择的出口 > 全局默认出口 > 内置直连。” Independent account operations have AP/AD/G/B; first login has G/B. Do not invent flow/account layers for these operations.
- “代理不可用、DNS/TLS 失败、取消、配置读取失败，都不能触发直连或换代理重试。”
- “每次新出站逻辑操作在 Offline 排队、凭据刷新、DNS、连接或出站审计开始之前选定一次实际出口”；“同一操作的探测、等待、发送及终态共用该副本。”
- “首次 OAuth 从启动登录时冻结默认出口，授权码兑换不能临时换路，也不能制造假 AccountRef”；“浏览器打开授权页仍由浏览器自己的网络设置决定。”
- “不新增独立代理服务、泛化 URL 转发接口或新的运行时依赖。” No public/client configuration, real credentials, public DNS, token traffic, browser automation, or user data directories.
- Keep purpose, authority, account settings revision, endpoint/realm matching, header policy, secret CAS, target checks and trust intact. Do not weaken a generic prober, relabel kinds, or generalize the fixed token branch.
- Implementation starts only after the current owned-quota/history/reset writer closes and root explicitly dispatches this plan. This document grants no source ownership, new Goal, or additional final release gate.
- Source discovery for this plan was pinned to `076897acebb145e69bde5462dd8f50cd837c9e80`; the authoritative audit document is an on-disk artifact absent from that commit. Execution must reconcile the current reviewed source before editing; preserve the concurrent writer's owned read/reset correction and evidence.
- Use the current pinned toolchain and locked dependencies with `GOTOOLCHAIN=local GOFLAGS=-mod=readonly`; do not disable checksum verification or introduce a new dependency-fetch restriction as a release gate. Private test network containment applies to fixture Runtime traffic. No toolchain/dependency version substitution or module edit.
- Root owns staging/commits after independent review. Implementers never stage, commit, push or mutate HEAD. Each task keeps its own SDD report/brief/source hashes under this plan's workspace; the parent global ledger receives only the proved-cell summary.
- Run each new selected normal test cycle and the original required targeted race count5. Do not repeat unaffected whole-package suites for tests-only changes; any actual production correction gets the named affected consumer regression. Full applicable final-source checks remain at candidate freeze.

---

## Boundaries and retained evidence

Carry these existing P cells with their recorded source range; their existence is not current combined-source release acceptance:

| Family | Carry | Fill here |
| --- | --- | --- |
| Manual refresh | Real `Manager.RefreshCredential` G token POST, default edit/token1/rotation1/next epoch2 (`account_global_integration_test.go:106,179–213`) | Real AP/AD/B sends; manual Hold/edit/caller cancellation/failed concrete probe/proxy-down/drain and zero rotation on aborted or failed exchange |
| Owned endpoint models | Real Manager G GET/down (`account_global_integration_test.go:20` + `account_egress_integration_test.go:392–439`); actual Runtime G Provider Hold in `product_update_control_test.go:172,573–642` | Actual endpoint-matching AP/AD/B GET; own frozen/default edit/new-operation selection, cancellation, failed probe, selected-proxy loss and Direct TLS failure |
| First OAuth | Actual G `Login.Start`→`Complete`, token1/save1/AccountRef0/frozen audit (`account_egress_integration_test.go:344–383`) | Actual B POST; actual Runtime G/B lifecycle freeze/Hold, G down/save0/Direct0/no retry, pending cancellation, exchanging ownership, failed probe and drain |

`token_probe_contract_test.go` already proves the closed JSON/form token producer and strict Auxiliary TLS shape. It does not prove either manual refresh or Login lifecycle. Owned quota/history/reset, captured reads, managed/semantic/original model traffic, native, blind/cleartext, metadata/prices, update, UI, Phase C clients/CA/data/soak and final production qualification are outside this plan.

## File structure and existing interfaces

Create only these acceptance files initially, all in `package productruntime_test`:

- `internal/productruntime/account_aux_fixture_test.go`: contained TLS/SOCKS/Store/Runtime construction, safe synthetic credential creation, route counters, audit-by-ID and bounded wait helpers. Setup belongs to Task1, its first consumer.
- `internal/productruntime/manual_refresh_egress_matrix_test.go`: actual manual account entry and owned rotation behavior.
- `internal/productruntime/endpoint_models_egress_matrix_test.go`: endpoint-matching leases and owned GET behavior.
- `internal/productruntime/first_login_egress_matrix_test.go`: actual Runtime-owned Login state machine and persistence.

Use the existing external test bridge without adding a production API:

```go
productruntime.ProductUpdateTestOptions(t *testing.T, gate offlinehold.RuntimeCoordinator) productruntime.Options
productruntime.StartProductUpdateTestRuntime(ctx context.Context, options productruntime.Options,
    transport providertransport.Transport, builder *egressnetwork.Builder,
    roots *x509.CertPool) (*productruntime.Runtime, error)
// product_update_export_test.go:15–47 injects both real concrete probers and
// the real authenticators/provider client, Runtime Store and default resolver.

// Existing external fixture in product_update_control_test.go:94–145:
updateSOCKS(t *testing.T, target string, lifetime time.Duration) (net.Listener, *atomic.Int64, *atomic.Int64)
// It accepts only SOCKS5 CONNECT 93.184.216.34:443 and bridges to target.

// Actual operation interfaces:
provideraccount.Controller.RefreshCredential(context.Context, provideraccount.ID, uint64) (provideraccount.View, error)
provideraccount.Controller.AcquireEndpointCredential(context.Context, provideraccount.ID, upstreamendpoint.Endpoint) (providerauth.Lease, error)
(*productruntime.Runtime).FetchEndpointModels(context.Context, upstreamendpoint.Endpoint, providerauth.Lease) (*http.Response, error)
codexoauth.LoginController.Start(context.Context, string, codexoauth.LoginAccount, string) (codexoauth.LoginView, error)
codexoauth.LoginController.Complete(context.Context, string, string, string) (codexoauth.LoginView, error)
codexoauth.LoginController.Cancel(context.Context, string, string) error
codexoauth.LoginController.Status(context.Context, string, string) (codexoauth.LoginView, error)
```

Do not widen `runtimeOriginalWire` or the update/metadata helpers' accepted URLs. The new wire accepts exactly `POST https://auth.openai.com/oauth/token` and `GET https://api.openai.com/v1/models`. The new network resolver accepts exactly their two hosts; its Direct mapping always remains available so unintended Direct is counted, including in proxy-failure rows.

## Shared acceptance mechanics (created inside Task1)

Define the following test-only interfaces in `account_aux_fixture_test.go`. All three tasks consume these names; do not change their meaning between tasks.

```go
type auxNetwork struct {
    proxyA, proxyB, target string
    a, b, direct, denied, dns atomic.Int64
}

func (n *auxNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
    var target string
    switch {
    case network != "tcp":
        n.denied.Add(1)
        return nil, errors.New("account auxiliary fixture denied network")
    case address == n.proxyA:
        n.a.Add(1); target = n.proxyA
    case address == n.proxyB:
        n.b.Add(1); target = n.proxyB
    case address == "auth.openai.com:443" || address == "api.openai.com:443" || address == "93.184.216.34:443":
        n.direct.Add(1); target = n.target
    default:
        n.denied.Add(1)
        return nil, errors.New("account auxiliary fixture denied target")
    }
    return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, target)
}

func (n *auxNetwork) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
    if network != "ip" || (host != "auth.openai.com" && host != "api.openai.com") {
        n.denied.Add(1)
        return nil, errors.New("account auxiliary fixture denied DNS")
    }
    n.dns.Add(1)
    return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

type auxSecrets struct {
    secretstore.Store
    creates, rotations atomic.Int64
}

func (s *auxSecrets) Replace(ctx context.Context, c secretstore.ReplaceCommand) (secretstore.Metadata, error) {
    m, err := s.Store.Replace(ctx, c)
    if err == nil {
        if c.ExpectedRevision == 0 { s.creates.Add(1) } else { s.rotations.Add(1) }
    }
    return m, err
}

type auxWire struct {
    builder *egressnetwork.Builder
    roots *x509.CertPool
    sends atomic.Int64
    before func()
    rejectTLS bool
}

func (w *auxWire) RoundTrip(req *http.Request, d providertransport.TransportDispatch) (*http.Response, transportprofile.Evidence, error) {
    allowed := req.Method == http.MethodPost && req.URL.String() == codexoauth.TokenURL ||
        req.Method == http.MethodGet && req.URL.String() == "https://api.openai.com/v1/models"
    if !allowed { return nil, transportprofile.Evidence{}, errors.New("account auxiliary fixture denied request") }
    w.sends.Add(1)
    if w.before != nil { w.before() }
    dialer, err := w.builder.Dialer(d.EgressPolicy())
    if err != nil { return nil, transportprofile.Evidence{}, err }
    roots := w.roots
    if w.rejectTLS { roots = x509.NewCertPool() }
    tr := &http.Transport{DialContext: dialer.DialContext, DisableKeepAlives: true,
        TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
        TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: time.Second}
    response, err := tr.RoundTrip(req)
    tr.CloseIdleConnections()
    return response, transportprofile.Evidence{}, err
}

type auxFixture struct {
    runtime *productruntime.Runtime
    gate *offlinehold.Gate
    secrets *auxSecrets
    network *auxNetwork
    wire *auxWire
    proxyA, proxyB net.Listener
    callsA, callsB, liveA, liveB *atomic.Int64
    profileA, profileB egressprofile.ProfileRevision
    startsMu sync.Mutex
    starts map[string]egressaudit.DecisionRef
}
```

Implement `newAuxFixture(t *testing.T, handler http.Handler) *auxFixture` with this exact composition: generate one P256 self-signed test leaf with DNSNames `auth.openai.com`, `api.openai.com`, validity now±one hour, ServerAuth/DigitalSignature; use `httptest.NewUnstartedServer(handler)`, install that leaf, `StartTLS`, and register server close. Start two existing `updateSOCKS` listeners with the server's exact private listener address and 4-second tunnel lifetime. Install `auxNetwork` with both listener addresses and the same contained Direct mapping. Construct `egressnetwork.NewBuilder(BuilderOptions{BaseDialer: network, SystemResolver: network})`, add only the fixture leaf to roots, and build `auxWire`.

Use imports already present in the contained update/metadata tests: `context`, `crypto/ecdsa`, `crypto/elliptic`, `crypto/rand`, `crypto/tls`, `crypto/x509`, `encoding/base64`, `encoding/json`, `errors`, `fmt`, `io`, `math/big`, `net`, `net/http`, `net/http/httptest`, `net/netip`, `net/url`, `sync`, `sync/atomic`, `testing`, `time`; and the existing internal `codexoauth`, `egressaudit`, `egressnetwork`, `egressprofile`, `offlinehold`, `productruntime`, `provideraccount`, `providerauth`, `providertransport`, `secretstore`, `transportprofile`, `upstreamendpoint` packages. Each new file imports only the subset it uses; no dependency is added.

Create the real Gate using `offlinehold.New(offlinehold.DefaultConfig())`; get `ProductUpdateTestOptions(t, gate)`, wrap `options.Secrets` in `auxSecrets`, and call `StartProductUpdateTestRuntime(t.Context(), options, wire, builder, roots)`. Publish two exact profiles with IDs `profile.account-aux-a`/`profile.account-aux-b` and `DefaultPolicy()` with SOCKS5 endpoint A/B. Leave the persisted default unset at revision1; each case selects its own default. Register bounded Runtime shutdown (5 seconds) before socket/server cleanups and fail cleanup errors. No faked Resolver, AccountRef, lease or Login Persist adapter.

Set `wire.before` once at fixture construction. It lists `runtime.EgressAttempts().List(context.Background(), egressaudit.PageRequest{Limit: 100})`, locks `startsMu`, and copies only nonterminal records into `starts[attempt.ID()] = attempt.Decision()`. Never infer an expected profile from the observed ProxyID. All mutations of hooks and TLS faults happen before starting a worker, or inside a single `before` hook; synchronize inter-goroutine observations with channels/mutexes.

Implement these small helpers alongside the fixture:

```go
func auxWait(t *testing.T, ready func() bool) {
    t.Helper()
    until := time.Now().Add(2 * time.Second)
    for !ready() {
        if time.Now().After(until) { t.Fatal("account auxiliary fixture did not reach observed state") }
        time.Sleep(time.Millisecond)
    }
}

func auxCallback(t *testing.T, view codexoauth.LoginView) string {
    t.Helper()
    auth, err := url.Parse(view.AuthorizationURL)
    if err != nil { t.Fatal(err) }
    return auth.Query().Get("redirect_uri") + "?" + url.Values{
        "state": {auth.Query().Get("state")}, "code": {"synthetic-code"},
    }.Encode()
}

func auxToken() string {
    claims := fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":"synthetic-aux-account"}}`, time.Now().Add(time.Hour).Unix())
    return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".synthetic"
}

func (f *auxFixture) setDefault(t *testing.T, expected uint64, profile *egressprofile.ProfileRevision) {
    t.Helper()
    var ref *egressprofile.ProfileRef
    if profile != nil { ref = &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision} }
    if _, err := f.runtime.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: expected, Profile: ref}); err != nil { t.Fatal(err) }
}

func (f *auxFixture) assertDrained(t *testing.T) {
    t.Helper()
    auxWait(t, func() bool {
        s := f.gate.Snapshot()
        return s.QueuedRequests == 0 && s.ActiveEgress == 0 && s.ActiveActions == 0 &&
            s.EnteringActions == 0 && s.HeldBytes == 0 && f.liveA.Load() == 0 && f.liveB.Load() == 0
    })
}
```

Implement `auxCreateAccount(t *testing.T, f *auxFixture, id provideraccount.ID, oauth bool) provideraccount.View`: for OAuth, call `codexoauth.ImportAuthJSON` on `auth_mode:chatgpt`, id/access=`auxToken()`, refresh=`synthetic-refresh-old`, account_id=`synthetic-aux-account`, current last_refresh; marshal `Credential`, wrap via `providerauth.NewMaterial`, marshal Material, make `secretstore.NewValue`, create with `ChatGPTOfficialID`, CodexOAuth driver and `AutomaticRefresh:false`. For static models, material credential=`synthetic-aux-bearer`, `OpenAIPlatformID`, StaticHeader driver, header assignments `X-Aux-Fixture: owned`, deletion `X-Aux-Deleted`; use `providerauth.NewMaterial("synthetic-aux-bearer", map[string]string{"X-Aux-Fixture":"owned"}, []string{"X-Aux-Deleted"})`. Destroy every Value/Material/Credential and clear every temporary marshaled byte slice. This repeats the established account_global and mixed-update creation seams, not a new secret backend.

Implement `auxSetChoice(t *testing.T, f *auxFixture, account provideraccount.View, choice string) provideraccount.View`: AP→profileA, AD→`egressprofile.Direct()`, G/B→zero ProfileRevision, using `ProviderAccounts().SetSettings` and `SettingsCommand{ID: account.Account.ID, ExpectedRevision: account.Account.SettingsRevision, EgressProfile: selected, AutomaticRefresh:false}`. AP/AD/G begin with real persisted default B at revision2; B leaves default unset revision1. AP/AD expected source Account/default0; G expected Global/default2/profileB; B expected Builtin/default1/Direct. Explicit rows thereby conflict with a reachable lower-layer proxy.

Implement `auxAssertAudit(t *testing.T, f *auxFixture, purpose egressaudit.EgressPurpose, origin string, expectedResolved egressprofile.Resolved, accountID string, settings uint64, outcome egressaudit.Outcome)` to require exactly one SQL record for this operation (before a next-operation call), actual purpose/target origin, `PayloadRuntime`, `CallerCore`, real Runtime parent, terminal state/outcome, expected `SelectionSource`, `DefaultRevision`, exact profile ID/revision, account ID/settings revision and the exact authority returned by `egressaudit.AuthorityForPurpose(purpose)`. Require a `starts` entry with the same attempt ID and equal entire DecisionRef. Account operations require `PolicyID=accountID`, `PolicyRevision=settings`, `RuleID=account.egress`; credential-free G login requires `PolicyID=runtime.egress-default`, `PolicyRevision=expectedResolved.DefaultRevision()`, `RuleID=global.default`; B login retains `BuiltInDirectDecision(AuthorityForPurpose(purpose))` policy/rule fields with actual builtin selection/default revision and no account. For multiple physical operations require one-to-one start/terminal pairing by ID and expected decisions per named operation, not slice order or the last attempt.

## Task 1: Manual refresh AP/AD/B and owned Hold/cancellation/failure

**Files:**
- Create: `internal/productruntime/account_aux_fixture_test.go` (shared mechanics above).
- Create/Test: `internal/productruntime/manual_refresh_egress_matrix_test.go`.
- Production inspection if a new test exposes a defect: `internal/provideraccount/refresh.go:15–86`, `internal/codexoauth/manager.go:180–277,286–399`, `internal/providertransport/runtime_fetch.go:164–221,493–507`; no unconditional production edit.

**Interfaces:** Consumes the existing Runtime bridge, `ProviderAccounts().RefreshCredential(ctx, id, expectedEpoch)` and actual Runtime-bound OAuth preparer. Produces the shared fixture/helpers above, `runManualAuxCase(t *testing.T, choice, edge string)`, `TestActualRuntimeManualRefreshEgressMatrix` and `TestActualRuntimeManualRefreshFailureAndOwnership`. No new production interface.

- [ ] **Step 1: Add the real manual cases and the contained fixture.** Use these exact test tables, with each row constructing a fresh Runtime and no `t.Parallel`:

```go
func TestActualRuntimeManualRefreshEgressMatrix(t *testing.T) {
    for _, choice := range []string{"AP", "AD", "G", "B"} {
        t.Run(choice, func(t *testing.T) { runManualAuxCase(t, choice, "send_edit") })
    }
}

func TestActualRuntimeManualRefreshFailureAndOwnership(t *testing.T) {
    for _, row := range [][2]string{
        {"G", "held_edit"}, {"G", "caller_cancel_owned_success"},
        {"G", "cancel_before_entry"}, {"G", "failed_probe_shutdown"},
        {"G", "held_shutdown"}, {"AP", "proxy_down"}, {"G", "proxy_down"},
        {"AD", "tls_failure"}, {"B", "tls_failure"}, {"G", "token_503"},
    } {
        t.Run(row[0]+"/"+row[1], func(t *testing.T) { runManualAuxCase(t, row[0], row[1]) })
    }
}
```

In `runManualAuxCase`, create a synthetic OAuth account (epoch1, automatic off), set its choice through real settings, compute expected selection from inputs, reset secret create/rotation counters after setup, then call the real entry in a joined worker:

```go
ctx, cancel := context.WithCancel(t.Context())
defer cancel()
done := make(chan error, 1)
go func() {
    _, err := f.runtime.ProviderAccounts().RefreshCredential(ctx, account.Account.ID, 1)
    done <- err
}()
```

The private token handler must reject anything except fixed host/path POST, empty Authorization, Content-Type application/json, and exactly `{grant_type:refresh_token,refresh_token:synthetic-refresh-old,client_id:codexoauth.ClientID}`. Respond with access=`auxToken()`, refresh=`synthetic-refresh-new`; count actual POST before returning 200, or return 503 only for `token_503`. Store counters plus exact secret epoch/content prove rotation; never count a successful `RefreshCredential` return as the sole rotation proof.

- [ ] **Step 2: Implement each table row's barriers and assertions.** For `send_edit`, capture the start and change default inside the wire `before` hook: AP/AD/G clear default2→3, B sets A default1→2. Actual send must retain the prior expected route. For `held_edit`, enter Hold before the worker, wait for exactly one actual queued target, and then clear default2→3 before real `ResumeOfflineHold`.

Assert the manual queued target is Auxiliary/TLS, canonical origin/ref `https://auth.openai.com`, authority/SNI `auth.openai.com`, `PlanRevision=0`, `PlanDigest=""`, `TargetAccess=0`, and exact frozen policy. There must be no HTTP, socket or outgoing SQL start while queued. Successful real Resume adds one SOCKS/TLS probe and one token socket, but only one HTTP POST and one secret rotation. No model target or model POST is involved.

For `caller_cancel_owned_success`, reach that same held queue, cancel the caller, confirm the operation has not finished and epoch remains1, then Resume through the concrete Runtime prober. Expect token1/rotation1/epoch2, the same frozen route, and `RefreshCredential` ultimately returns the caller context error. For a deterministic account-operation gate assertion while held, call `SetSettings` with the current settings revision and require `ErrOperationInProgress`; do not call a second manual refresh or depend on scheduling to infer ownership. The contract is already explicit:

The successful `send_edit`, `held_edit` and `caller_cancel_owned_success` rows all perform the named default edit before their physical send/Resume; for the caller-cancel row, clear B2→unset3 after observing the queue and before Resume. This makes the later next-lease expectation explicit. Failure rows do not run the success-only next-admission assertions.

```go
// provideraccount/refresh.go:72–85
// Refresh removes caller cancellation once started and keeps the account gate
// through the bounded rotation, then returns the caller's cancellation error.
// codexoauth/manager.go:253–260 owns one default20s refresh operation.
```

For `cancel_before_entry`, cancel before the call; require context error, token0/rotation0/epoch1, queue0/actions0/audit0/sockets0. For `failed_probe_shutdown`, queue, clear default, close selected B listener, call actual Resume and require error/StateHeld/queue1/POST0/rotation0; then start Runtime shutdown and join the manual worker. For `held_shutdown`, shut down while queued without Resume. These two rows require POST0/rotation0/unchanged physical credential epoch1 and no outgoing send audit; drain all workers/actions/held bytes and sockets. Do not claim `codexoauth.Manager.Shutdown` itself cancels a refresh: `manager.go:436–453` waits active refreshes; the real Runtime provider/Hold shutdown supplies this abort boundary.

For AP/G `proxy_down`, close A/B respectively after selection but before dialing. Require selected-proxy dial1, successful SOCKS CONNECT0, Direct0, other-proxy0, HTTP0, rotation0, epoch1, send attempt1 and immutable failed SQL terminal1. For AD/B `tls_failure`, use `wire.rejectTLS=true` before starting; require Direct1, SOCKS0, HTTP0, rotation0 and failed terminal1. For token503 require one selected SOCKS connection/POST, no retry or rotation, `ErrRefreshUnavailable`, epoch1; the transport audit describes completed receipt of a 503 response and must not be forced to `OutcomeFailed` if the body completed normally. Derive the concrete audit outcome from the existing transport/reader boundary and retain that observed received-response evidence.

After each successful row, read actual account health/secret epoch2 and inspect the credential material through `options.Secrets` (fixture wrapper); require refresh=`synthetic-refresh-new` and access token matching the handler. Acquire and release one next endpoint lease to prove next selection: AP/AD remains Account/default0; G becomes Builtin/default3; B becomes Global A/default2. This lease assertion is not claimed as a next B network send. Drain first before acquiring next to avoid conflating operations.

- [ ] **Step 3: Run targeted baseline acceptance.**

```bash
export GOFLAGS=-mod=readonly
GOTOOLCHAIN=local /tmp/vibermate-go-security.JcRyfL/go/bin/go version
GOTOOLCHAIN=local /tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/productruntime -run '^TestActualRuntimeManualRefresh(EgressMatrix|FailureAndOwnership)$' -count=1 -timeout=90s -v
```

Expected version: output starts `go version go1.26.9` and reports the executor's actual host OS/architecture. Expected tests: implemented cells pass without a forced RED. For a real failure, save the exact test/subtest output, observed queued target/state/counters and physical secret epoch; classify fixture defect versus product defect using the pinned seams. Fix test setup first if it does not reach the actual intended entry. A failed probe alone is not an unexpected product failure when the proxy was deliberately closed.

- [ ] **Step 4: Correct only a reproduced defect, or leave production unchanged.** If token producer identity differs from the already reviewed closed branch, retain the negative evidence and escalate rather than loosening `TLSProber` or replacing the resolver. If an implementation correction changes cancellation/retained credential behavior, obtain a scoped root decision backed by the failing case and exact source; do not erase an exchanged grant. Record already-correct baseline passes as acceptance evidence.

- [ ] **Step 5: Verify/review the complete Task1 deliverable and commit when dispatched.**

```bash
gofmt -w internal/productruntime/account_aux_fixture_test.go internal/productruntime/manual_refresh_egress_matrix_test.go
GOTOOLCHAIN=local /tmp/vibermate-go-security.JcRyfL/go/bin/go test -race ./internal/productruntime -run '^TestActualRuntimeManualRefresh(EgressMatrix|FailureAndOwnership)$' -count=5 -timeout=120s -v
git diff --check
```

Retain normal/race output and evidence report under this plan's SDD directory with source commit/hash manifest. Reviewer gate: actual manual entry, selected-route counters, correct caller ownership, rotation count/epoch, concrete probes, immutable per-ID SQL starts/terminals and full drain. For any actual production correction, name and run its affected existing consumer tests before reporting; do not rerun four unchanged full packages merely for this test-only fixture. Root stages the exact reviewed files and commits after review, never the implementer.

## Task 2: Endpoint-matching model discovery AP/AD/B and frozen failure boundaries

**Files:**
- Create/Test: `internal/productruntime/endpoint_models_egress_matrix_test.go`.
- Reuse: `internal/productruntime/account_aux_fixture_test.go`.
- Production inspection if a test fails: `internal/provideraccount/manager.go:591–630`, `internal/providertransport/runtime_fetch.go:104–132,236–258,345–362,509–540`, `internal/providertransport/probe.go:82–200`; no automatic read/reset producer edit.

**Interfaces:** Consumes the Task1 fixture/helpers, actual `AcquireEndpointCredential(ctx,id,endpoint) (providerauth.Lease,error)` and `Runtime.FetchEndpointModels`. Produces `runEndpointModelsAuxCase(t *testing.T, choice, edge string)` and `TestActualRuntimeEndpointModelsEgressMatrix`/`TestActualRuntimeEndpointModelsHoldAndFailure`. No production interface change.

- [ ] **Step 1: Write exact operation tests.**

```go
func TestActualRuntimeEndpointModelsEgressMatrix(t *testing.T) {
    for _, choice := range []string{"AP", "AD", "G", "B"} {
        t.Run(choice, func(t *testing.T) { runEndpointModelsAuxCase(t, choice, "send_edit") })
    }
}

func TestActualRuntimeEndpointModelsHoldAndFailure(t *testing.T) {
    for _, row := range [][2]string{
        {"G", "held_edit"}, {"G", "held_cancel"}, {"G", "failed_probe_cancel"},
        {"AP", "proxy_down"}, {"G", "proxy_down"},
        {"AD", "tls_failure"}, {"B", "tls_failure"},
        {"G", "wrong_endpoint"}, {"G", "wrong_realm"},
    } {
        t.Run(row[0]+"/"+row[1], func(t *testing.T) { runEndpointModelsAuxCase(t, row[0], row[1]) })
    }
}
```

`runEndpointModelsAuxCase` creates a real static account for OpenAIPlatform, changes settings through the Manager, obtains the actual endpoint through `UpstreamEndpoints().Get(ctx, upstreamendpoint.OpenAIPlatformID)`, then acquires the endpoint lease and snapshots its AccountRef before any Hold/DNS/send:

```go
lease, err := f.runtime.ProviderAccounts().AcquireEndpointCredential(ctx, account.Account.ID, endpoint)
if err != nil { t.Fatal(err) }
defer lease.Release()
ref, ok := lease.Account()
if !ok || ref.Validate() != nil { t.Fatal("missing real endpoint lease") }
done := make(chan error, 1)
go func() {
    response, err := f.runtime.FetchEndpointModels(ctx, endpoint, lease)
    if response != nil && response.Body != nil {
        _, readErr := io.Copy(io.Discard, response.Body)
        err = errors.Join(err, readErr, response.Body.Close())
    }
    done <- err
}()
```

Handler asserts exact `GET /v1/models`, no query/body, `Host=api.openai.com`, `Authorization=Bearer synthetic-aux-bearer`, `X-Aux-Fixture=owned`, `X-Aux-Deleted` absent; reply `{"data":[{"id":"synthetic-model"}]}`. Do not swap in a hand-built lease/account or models.dev metadata source. There is no token POST or secret rotation in this task.

- [ ] **Step 2: Implement operation-specific wire/Hold/default and negative assertions.** AP/AD/G/B `send_edit` changes the default at `wire.before` after SQL start and before dialing, as Task1 specifies; require one successful GET via the original lease route, immutable expected `PurposeUpstreamModelDiscovery`/`https://api.openai.com` audit and rotation0. Then acquire a *new actual endpoint lease* and perform a second GET: AP/AD still explicit, G now builtin3, B now Global A2. Pair both attempts independently with their own starts and precomputed decisions, and require exactly two total GETs, never deriving expected route from observed audit.

For G `held_edit`, enter Hold before fetching; wait for one actual queued Provider target matching endpoint ID/network origin/authority/SNI and the lease's policy. Keep actual nonzero runtime PlanRevision/digest; this is a Provider producer, not the closed Auxiliary token producer. Do not zero the plan or canonicalize endpoint TargetRef to bypass a prober. Change default B2→unset3 only after observing that queued target. Actual `ResumeOfflineHold` must add one concrete TLS probe and one GET socket on B, with HTTP GET1, no HTTP from probe, Direct0/A0/rotation0. The new independent GET must then use builtin Direct with source/default3.

For `held_cancel`, cancel the fetch's real caller context while queued; require worker error, SQL outgoing start0/terminal0/GET0/socket0, all queue/action/body state drained. Resume the empty queue once and assert no late GET. For `failed_probe_cancel`, close B only after queue/default edit, require actual Resume error/held queue1/GET0; caller cancellation then joins/drains, selected proxy dial1, Direct0/A0/HTTP0/rotation0/audit0. No new-operation GET is started in these fault rows.

For proxy-down and Direct TLS failure, require one attempted send/audit start and one same-decision failed terminal, one selected-route dial, no GET, no second connection, no alternate proxy or Direct fallback, and rotation0. Direct TLS rows retain the real certificate verification failure (`RootCAs` empty in the test wire); do not set InsecureSkipVerify or replace TLS with a canned error.

`wrong_endpoint`: get the actual AnthropicOfficial endpoint and call Manager acquisition for the OpenAI account; require `ErrEndpointMismatch` and zero send/queue/audit/DNS/rotation. `wrong_realm`: clone the valid OpenAI endpoint with `RealmID` from the valid AnthropicOfficial endpoint, first require clone.Validate()==nil so this is a realm mismatch rather than malformed test input; acquisition must return `ErrEndpointMismatch` with the same zero effects. These exercise the real association/compatible-origin/realm boundary before a lease exists. Do not forge a cross-realm credential to test transport matching.

- [ ] **Step 3: Run the acceptance baseline, classify any failure, and make only an evidenced narrow correction.**

```bash
GOTOOLCHAIN=local /tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/productruntime -run '^TestActualRuntimeEndpointModels(EgressMatrix|HoldAndFailure)$' -count=1 -timeout=60s -v
```

Expected: source already implements account priority and a Provider target accepted by the concrete Provider prober. A baseline pass fills E cells; no forced fake RED. Retain an unexpected failing real queued identity and investigate only this producer/consumer. The owned read/reset Auxiliary contract belongs to the previous writer and is not this task's route.

- [ ] **Step 4: Verify/review and commit the independent endpoint-model deliverable.**

```bash
gofmt -w internal/productruntime/endpoint_models_egress_matrix_test.go
GOTOOLCHAIN=local /tmp/vibermate-go-security.JcRyfL/go/bin/go test -race ./internal/productruntime -run '^TestActualRuntimeEndpointModels(EgressMatrix|HoldAndFailure)$' -count=5 -timeout=90s -v
git diff --check
```

Evidence/review gate: actual endpoint acquisition, AP/AD/B physical GETs, existing G carry explicitly scoped, own concrete Provider Hold, secret/header authority, next-operation physical send, cancellation/no retry and per-ID SQL parity. Update this plan's SDD, then root carries only named proved cells to the parent ledger and commits after review. Tests-only changes need no repeated whole provider/account suite; an actual production fix names its affected consumers separately.

## Task 3: Actual first Login.Start/Complete G/B, exchange ownership and lifecycle drain

**Files:**
- Create/Test: `internal/productruntime/first_login_egress_matrix_test.go`.
- Reuse: `internal/productruntime/account_aux_fixture_test.go`.
- Inspect for a reproduced lifecycle gap: `internal/productruntime/runtime.go:679–699,878–950,955,1344–1403`, `internal/codexoauth/login.go:142–230,249–345,379–418`, `internal/productruntime/codex_oauth.go:20–129`.
- Conditional minimal correction, authorized by root if the contained regression is RED: `internal/productruntime/runtime.go` normal cleanup registration, with the contained regression in this test file. No unconditional Login contract change.

**Interfaces:** Consumes the fixture, `Runtime.CodexOAuthLogins() codexoauth.LoginController`, Runtime account/secret persistence, and actual `Runtime.Shutdown(ctx) error`. Produces `runFirstLoginAuxCase(t *testing.T, choice, edge string)`, `TestActualRuntimeFirstLoginEgressMatrix`/`TestActualRuntimeFirstLoginOwnershipAndDrain`. Never uses a custom Persist success counter as the whole Runtime proof.

**Concrete lifecycle regression and conditional correction:** On the pinned source `codexOAuth.Shutdown` and `codexLogins.Shutdown` are registered only on startup `pending` (runtime.go:687,699), not final `cleanups`; `pending` is cleared at955. Runtime normal shutdown invokes `cleanups.shutdown` (1388). LoginManager.Shutdown clears timers/listeners, cancels owned exchanges and waits `active` (login.go:404–419). Existing provider/Hold shutdown may abort the held POST, but does not by itself prove the Login manager, a pending timer or loopback listener has drained. Root independently checked this source chain and authorized a narrow final-cleanup registration correction if the actual Runtime regression below confirms it. This conditional fix stays in the existing lifecycle scope and needs no new architecture choice. Any broader cancellation/retained-refresh change remains an escalation with exact source and observed failure.

- [ ] **Step 1: Add actual Runtime login case tables.**

```go
func TestActualRuntimeFirstLoginEgressMatrix(t *testing.T) {
    for _, row := range [][2]string{
        {"G", "start_edit_complete"}, {"B", "start_edit_complete"},
        {"G", "held_edit"}, {"B", "held_edit"}, {"G", "proxy_down"},
    } {
        t.Run(row[0]+"/"+row[1], func(t *testing.T) { runFirstLoginAuxCase(t, row[0], row[1]) })
    }
}

func TestActualRuntimeFirstLoginOwnershipAndDrain(t *testing.T) {
    for _, edge := range []string{"pending_cancel", "caller_cancel_owned_success",
        "failed_probe_shutdown", "held_shutdown", "pending_shutdown", "pending_loopback_shutdown"} {
        t.Run(edge, func(t *testing.T) { runFirstLoginAuxCase(t, "G", edge) })
    }
}
```

`runFirstLoginAuxCase` begins with no ProviderAccount and creates no fake lease. Use the actual Runtime controller with Mode=create, ID=`account.aux-login`, EndpointID=`upstreamendpoint.ChatGPTOfficialID.String()`, DisplayName=`Synthetic aux login`, owner=`synthetic-owner`, callback mode manual (loopback only for the dedicated contained listener drain row):

```go
logins := f.runtime.CodexOAuthLogins()
view, err := logins.Start(t.Context(), "synthetic-owner", codexoauth.LoginAccount{
    Mode: "create", ID: "account.aux-login", EndpointID: upstreamendpoint.ChatGPTOfficialID.String(),
    DisplayName: "Synthetic aux login",
}, mode)
if err != nil { t.Fatal(err) }
callback := auxCallback(t, view)
type completion struct { view codexoauth.LoginView; err error }
ctx, cancel := context.WithCancel(t.Context())
defer cancel()
done := make(chan completion, 1)
go func() {
    result, err := logins.Complete(ctx, "synthetic-owner", view.ID, callback)
    done <- completion{result, err}
}()
```

Start itself must create no token HTTP/sockets/egress audit/account/secret. Capture expected G B/default2 or builtin Direct/default1 at Start, then edit default before Complete (`start_edit_complete`) or after observing the queued exchange (`held_edit`). G clears2→3; B sets proxyA1→2. The authorization URL is parsed solely for synthetic callback construction; no browser/open URL request occurs.

The private token handler requires fixed host/path POST, no Authorization, Content-Type application/x-www-form-urlencoded, exactly one each of grant_type=authorization_code, client_id=Codex ClientID, code=synthetic-code, redirect_uri equal to parsed trusted Start redirect and nonempty code_verifier with no other form keys. Reply JSON `id_token`, `access_token`=`auxToken()`, `refresh_token=synthetic-login-refresh`. Assert true Runtime persistence by `ProviderAccounts().Get/List`: one account matching destination, automatic refresh on, unlinked associations, epoch1 and one physical secret create/rotation0; inspect the stored material to verify synthetic token bytes. Failure/pending cancel rows require account absent, create0 and rotation0. Avoid logging verifier/code/form/token values in evidence.

- [ ] **Step 2: Implement success/freeze, pending cancel and caller ownership cases.** G/B physical success has token POST1/save1, route G SOCKS B1/Direct0 or B Direct1/SOCKS0; SQL account ID empty/settings0, `PurposeCredentialRefresh`, target auth origin, expected source/profile/default at Start, exact typed policy/rule and own start-terminal equality. Repeat Complete with the same callback after terminal success; it returns the terminal view with no second POST/save. Start and complete a new independent login with a distinct account ID after the default edit to prove the *actual new* route (G→builtin3, B→Global A2), token POST totals2/save totals2, and independent start/terminal pairs. Do not replace this with `FreezeLoginEgress()` alone.

Held G/B cases inspect one real Auxiliary/TLS auth-origin target with zero plan identity/TargetAccess and exactly the original policy; no HTTP/socket/audit while queued. Resume through Runtime's real prober adds precisely one reachability socket plus one token socket on the frozen G or B route, token1/save1 and no probe HTTP. Drain before the next login.

`pending_cancel`: Start, call Cancel before Complete, require Status cancelled and Complete terminal cancelled, token0/save0/socket0/audit0; no login exchange begins. `caller_cancel_owned_success`: start Complete under Hold, observe State exchanging and the actual queue, cancel the Complete caller, require `Cancel(context.Background(), owner,id)` returns `ErrLoginBusy` and duplicate Complete returns `ErrLoginBusy`. Confirm account absent/token0; then actual Resume completes the *owned* exchange once and persists one account. Require completion State completed with nil error even though the original caller is canceled, plus terminal token1/save1, then no replay on subsequent Complete. Existing `Login.Complete` deliberately uses a background25s operation; canceling its caller is not an exchange abort.

- [ ] **Step 3: Implement down/failed probe/shutdown and reproduce the normal cleanup boundary.** G proxy-down closes B after Start and edits the default before Complete: one failed selected-proxy dial, SOCKS success0/HTTP0/save0/Direct0/A0, no retry; Complete returns a failed view with `Reason=login_exchange_failed` (not necessarily a Go error), immutable failed audit1, all operation resources drained.

`failed_probe_shutdown`: queue actual Complete, edit default, close B, run real Resume; require failed probe/held queue1/exchanging state/token0/save0/audit0, then Runtime shutdown and join Complete; require failed terminal Login view with no save and all queue/socket workers drained. `held_shutdown` performs the same actual Runtime drain without a probe. Use context.WithTimeout(context.Background(),5*time.Second), never rely on caller cancellation or sleep25s to stand in for shutdown. Observe account absence and SQL audit0 before shutdown closes Store, then use the joined completion, retained Login controller, physical secret effect counters and drained Gate/socket workers after shutdown; do not query a closed Runtime Store to assert no save.

`pending_shutdown`: Start without Complete, call actual Runtime Shutdown, and require `logins.Status(context.Background(), owner,id)` returns `ErrLoginNotFound` through the retained controller. `pending_loopback_shutdown`: Start loopback; if it returned manual because both existing ports are occupied, record the specific unavailable row as unverified and retain the always-run manual row. Never stop a user's listener or count a skip as that row passing; qualify this row later on an isolated available runner before closing its requirement. If loopback started, parse exact `127.0.0.1:1455`/`:1457` from its localhost redirect, verify the listener just created by this fixture using only a bounded local dial, close that connection, then Shutdown and require the retained Login controller unavailable and that owned listener refuses a bounded local dial. No browser/token Complete is sent. Because listener shutdown is graceful/asynchronous inside Login.clearLocked, allow bounded2s observation after manager shutdown, and assert no active exchange or late save.

Retain the first RED if normal Runtime shutdown leaves Status pending/exchanging or listener reachable. Apply the root-authorized minimal correction only for that reproduced failure: add the existing Login shutdown registration *after* provider registration in normal `cleanups`, so LIFO cancels/joins Login before provider/Store closure. Preserve startup rollback registration, Store-last ownership, existing provider/Hold drain and account operation gates. The exact production change is:

```go
cleanups.register("provider transport", provider.Shutdown)
cleanups.register("Codex OAuth login manager", codexLogins.Shutdown)
cleanups.register("original-origin transport", original.Shutdown)
```

Show pending manual/loopback and held exchange cases turn green. If the required correction goes beyond this registration (for example synchronous listener join or refresh-manager ownership), report that additional source-backed gap for a scoped decision; do not silently redesign it. Do not add a shortened/public Login exchange timeout or change Cancel from Busy.

- [ ] **Step 4: Run exact baseline and verify the approved correction if needed.**

```bash
GOTOOLCHAIN=local /tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/productruntime -run '^TestActualRuntimeFirstLogin(EgressMatrix|OwnershipAndDrain)$' -count=1 -timeout=90s -v
```

Positive selection and owned-caller cases may pass immediately. The concrete normal-cleanup assertions may reveal the source-backed gap; preserve output separately from deliberate proxy/probe failure evidence. Report why any skipped loopback row is not proven. Do not lower expectations merely to keep the matrix green.

- [ ] **Step 5: Verify/review and commit the bounded login deliverable.**

```bash
gofmt -w internal/productruntime/first_login_egress_matrix_test.go
GOTOOLCHAIN=local /tmp/vibermate-go-security.JcRyfL/go/bin/go test -race ./internal/productruntime -run '^TestActualRuntimeFirstLogin(EgressMatrix|OwnershipAndDrain)$' -count=5 -timeout=120s -v
GOTOOLCHAIN=local /tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/productruntime -run '^TestActualRuntime(ManualRefresh(EgressMatrix|FailureAndOwnership)|EndpointModels(EgressMatrix|HoldAndFailure)|FirstLogin(EgressMatrix|OwnershipAndDrain)|ManagedRefreshHoldUsesConcreteProbers|ManagedRefreshAndModelPriorityMatrix|ManagedRefreshFailureBoundaries|MixedProviderAndUpdateQueueUsesBothConcreteProbers)$' -count=1 -timeout=180s -v
git diff --check
```

If RED confirmed the root-authorized minimal normal cleanup correction, format that exact runtime.go change too and identify it in the report. Root alone stages/commits after review. Retain affected normal/scoped race logs, baseline-versus-correction distinction, source commit/file hash manifest, exact P/E/I cells and independent scoped review. The final normal cohort above runs once after this task and covers shared automatic token/Provider mixed-prober consumers of the normal Runtime cleanup correction; it is not a repeated per-task whole-package campaign or the final all-family/Phase C qualification.

## Self-review and execution handoff

Three independently reviewable tasks: Task1 manual refresh plus its contained shared fixture; Task2 endpoint models; Task3 first login with its explicit lifecycle decision. The plan preserves prior G proofs and fills only the authoritative audit's specified missing cells. All expected routes are inputs; audits are observed by their own attempt IDs; HTTP, successful SOCKS connections, attempted dials, credential CAS/save effects and lifecycle state are separate facts. Deliberate fault rows preserve current received-response and owned-cancellation semantics.

No contract/architecture choice remains unresolved. The normal Runtime/Login cleanup source gap is a conditional reproduction/correction under root's explicit scoped authorization, not an observed test failure yet. Root will self-review this plan and report, then commit/dispatch only after the current writer closes. Execution method is root's existing workflow; this planning task starts no agents, tests, network or source edits.
