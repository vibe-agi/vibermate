# Global Default Egress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Execute the six units serially, each with behavioral RED→GREEN, self-review, owned commit and independent spec/security + quality review. No concurrent source writers.

**Goal:** Make every existing ViberMate-owned external operation honor explicit account/flow exits or the persistent global default, without proxy-failure direct fallback or changes to in-flight work.

**Architecture:** Resolve one immutable effective exit above the existing dialer, where the actual winning account/flow is known. Preserve authored choices and historical digests separately from effective execution facts. Reuse real leases, Offline Hold, strict transports, audit and native receipt authority; the default is configuration, not authority.

**Tech Stack:** Go1.26.8 (go.mod remains1.26.0), Flutter3.41.5 revision2c9eb20739dfec95e2c74bd3dfa4601b0a8a36aa, SQLite unpublishedv2 + verifiedv1 upgrade, existing HTTP/SOCKS/DoH. No new runtime dependency.

**Spec:** `docs/superpowers/specs/2026-10-05-global-default-egress-design.md`, explicitly approved by the user after written review. Parent `2026-10-03-production-readiness-design.md` and its full release gates remain binding.

## Global constraints and sequence

- Work only in the existing isolated `fix/production-readiness-20261003` worktree. Original dirty workspace, running App/9666, real accounts/DB/trust/system proxy and user processes remain untouched. All network validation uses private synthetic TLS/SOCKS/DoH servers with rejected unexpected destinations.
- Native claim Tasks1/2 are independently approved at8e078c4/3e7b69e; normal/race/18-client evidence is source-bound, not proof for later changes. This plan runs before native upstream continuation so its effective-egress contract is available to the continuation leases/binding. Rebase that written plan's interfaces after this plan without dropping its original authority/recovery gates.
- Selection order: explicit account > explicit applicable flow > global default > built-in direct. Explicit direct is an override. Native original-client tools never borrow a managed model account's exit. No lookup error, TLS/DNS/proxy failure or unknown commit becomes absence or a retry via another exit.
- Freeze before credential refresh, Offline queue/probe, actual outbound audit and network. Incoming Activity start may remain earlier with account/exit unselected; do not reorder it merely to fill metadata. No changing a running request, token exchange, native logical stream or open tunnel after a default edit.
- Preserve exact published configuration bytes/digests, old audit/once/approval rows and all user data. Extend only unpublishedv2/bootstrap/delta, keep frozenv1 SHA256393b7d67f8ffd1952fc516a150e1a2a68707b67536fbc2e7f5a7f17208424cb1. Reject unknown oldprivatev2 layouts unchanged; never reset. New changes must update migration reconciliation/default-row checks, verified backup/restore and strict schema parity.
- Existing loopback/control/server trust and private managed-provider restrictions remain. No global environment mutation, broad URL fetch, redirected token request, real reset redemption or automatic installation. External browser/Homebrew/downloaders are outside Runtime egress.
- Use `env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly /usr/local/go/bin/go ...`. Enumerate test selections, preserve first failures/terminal handles; no compilation error counted as behavioral RED. Keep count5 targeted race; choose bounded groups before execution from measured cost, not after failures. Never run a second suite while a worker owns one.
- Main must supply exact per-unit BASE at dispatch, FULL-read the handoff and use one independent review package. Do not call all-global support complete until every existing production family in Task6 passes through real network seams.

## Shared interface and representation decisions

Authored protocol/tool `egressProfile` becomes `egressprofile.Choice` in Task3, not a special Direct profile. A comparable private tag/value preserves existing explicit profile JSON field order/bytes; inherit is exactly `{"mode":"inherit"}`. Zero/missing/null/duplicate/case-alias/mixed/unknown JSON is invalid. Account optional override remains its existing null/zero representation; do not unnecessarily migrate account JSON.

Task1 introduces these exact names in `internal/egressprofile/default.go` and `choice.go`:

```go
type Choice struct { kind uint8; profile ProfileRevision }
func Inherit() Choice
func Explicit(ProfileRevision) (Choice, error)
func (Choice) Validate() error
func (Choice) ExplicitProfile() (ProfileRevision, bool)
// MarshalJSON/UnmarshalJSON: old explicit object or exact {"mode":"inherit"}.

type SelectionSource string // account, flow, global, builtin
const (
    SelectionAccount SelectionSource = "account"
    SelectionFlow SelectionSource = "flow"
    SelectionGlobal SelectionSource = "global"
    SelectionBuiltin SelectionSource = "builtin"
)
type Resolved struct { profile ProfileRevision; source SelectionSource; defaultRevision uint64 }
func (Resolved) Validate() error
func (Resolved) Profile() ProfileRevision
func (Resolved) Source() SelectionSource
func (Resolved) DefaultRevision() uint64
// For typed stored-evidence reconstruction ONLY, not send authority.
func RestoreResolved(ProfileRevision, SelectionSource, uint64) (Resolved, error)

type ProfileRef struct { ID ID; Revision Revision }
type DefaultSetting struct { Revision uint64; Profile *ProfileRevision }
type SetDefaultCommand struct { ExpectedRevision uint64; Profile *ProfileRef }
type DefaultCommitResult struct { Outcome CommitOutcome; Setting DefaultSetting }
type DefaultRepository interface {
    LoadDefault(context.Context) (DefaultSetting, error)
    WriteDefault(context.Context, uint64, DefaultSetting) (DefaultCommitResult, error)
}
type Resolver interface { Resolve(context.Context, Choice, Choice) (Resolved, error) }
type Defaults interface {
    Resolver
    GetDefault(context.Context) (DefaultSetting, error)
    SetDefault(context.Context, SetDefaultCommand) (DefaultSetting, error)
}
func NewDefaultManager(DefaultRepository, Controller) (*DefaultManager, error)
```

`Resolve(ctx, accountChoice, flowChoice)` validates BOTH choices, then returns the first explicit without reading default storage. Explicit sources carry defaultRevision0. Otherwise load exactly one valid default setting: a selected profile yields sourceglobal, unset yields builtinDirect; both carry the actual settings revision. `RestoreResolved` rejects source/profile/revision inconsistencies; any DirectID must exactly equal built-in Direct, and builtin source requires that exact profile. It proves structure only. Native stored/current scope and Store issuer checks remain separate. Read the default row and its exact profile within one transaction/snapshot; no two-read mixture during concurrent setting edits.

## Task 1: Durable global setting, strict choices and owner control

**Files:** new `internal/egressprofile/{choice,default}.go` + tests; new `internal/runtimepersistence/egress_default_repository.go` + tests; `store.go`, `schema.sql`, `schema/v2.sql`, `migrations/001_to_002.sql`, `schema_migration.go` and schema/migration/backup tests; `internal/productruntime/runtime.go`; new `internal/desktopcontrol/egress_default.go` + tests and handler routes/options; both real host composition paths, `internal/desktophost/host.go` and `internal/runtimecontrol/application.go` used by `internal/serverhost/host.go`, with focused desktop/server authenticated route tests. Do not yet change live outbound selection or authored Environment field types.

**Produces:** the shared interfaces above, `Store.EgressDefaultRepository() egressprofile.DefaultRepository`, `Runtime.EgressDefaults() egressprofile.Defaults`, owner-only `GET /api/v1/egress-default` and `PUT /api/v1/egress-default`.

- [x] **RED:** Add compilable fail-closed initial declarations, then `TestDefaultEgressPersistsAndResolves` using real temporary Store and published SOCKS profile. Set default, resolve inherit/inherit, close/reopen Store and require same profile+revision; current absence of persistence/resolution must fail behavior. Pure Choice tests compare explicit encoded bytes to `json.Marshal(ProfileRevision)` and reject malformed inherit. Do not count missing-symbol errors as RED.
- [x] **Storage and migration:** Add a singleton default row, seeded revision1/unset on fresh and verified upgraded DB. Store ID/revision only; validate selected exact revision against published rows inside the write transaction (built-in direct is the special exact DirectID/revision1). No FK to a nonexistent built-in profile row. Use current operation gate/CAS/committer/reconciliation. A missing or malformed singleton is corruption, not implicit direct.

```sql
CREATE TABLE runtime_egress_default(
  singleton INTEGER PRIMARY KEY CHECK(singleton=1),
  revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9223372036854775807),
  egress_id TEXT,
  egress_revision INTEGER,
  CHECK((egress_id IS NULL AND egress_revision IS NULL) OR
        (egress_id IS NOT NULL AND length(CAST(egress_id AS BLOB)) BETWEEN 1 AND 128
         AND egress_revision IS NOT NULL AND egress_revision BETWEEN 1 AND 9223372036854775807))
) STRICT;
INSERT INTO runtime_egress_default(singleton,revision) VALUES(1,1);
```

Update `migrationAdditionsEmpty` to require exactly that initial singleton during post-upgrade certification; preserve all original legacy fingerprint rows/sequences. Freeze a test-only prechange privatev2 schema and prove Inspect/Open reject it without altering DB/WAL/SHM/receipt. Finalbootstrap andv1delta schemas must match. No user directory migration.
- [x] **Implement resolver/control:** DefaultManager owns no stale cache; Get/Resolve use repository snapshots, Set validates published exact reference and advances one settings revision. Equal selection can be a validated no-op at the expected revision. Read errors never call Direct. Control input is exactly `{"profile":null}` or `{"profile":{"id":"...","revision":N}}`; expected revision/idempotency headers use existing mutation protocol; body cannot supply policy. Response is `{"revision":N,"profile":null-or-fullProfile,"effective":{"profile":fullProfile,"source":"global-or-builtin","defaultRevision":N}}`, derived from the SAME returned setting, not a second Resolve/read. Runtime wires one default manager after profile manager construction. Endpoint unavailable if dependency missing, never a second fallback store. DefaultSetting returned Profile pointers are copied, never shared mutable state.
- [x] **GREEN/faults:** Test unset/explicitDirect/SOCKS, explicitaccount/explicitflow with failing default repository, malformed defaults, reference missing/changed, unknown revision, duplicate/case-alias input, CAS conflict, rollback/commit-then-error/reconciliation failure, restart and deletion/corruption refusal. Use actual Store writes for persistence; caller output mutation cannot change later resolutions. No upstream/external sockets in this unit; the isolated loopback listeners required by the real Host/control composition test are permitted.
- [x] **Verify/commit/review:** Run affected `egressprofile`, `runtimepersistence`, `runtimedata`, `desktopcontrol`, `productruntime` normal tests; targeted defaults/migration race, repository checker CLI/tests and diff. Self-review actual error/cache/CAS paths, commit owned files and request independent storage/authority review. Do not expose a UI ‘global active’ claim yet.

## Task 2: Account/runtime operations, first OAuth and actual egress evidence

**Files:** `internal/provideraccount/{manager,settings,account_read,refresh}.go`, `internal/providerauth` AccountRef definition/validation, `internal/providertransport/{client,plan,runtime_fetch,account_read,account_reset,probe}.go`, `internal/codexoauth/{login,manager}.go`, `internal/productruntime/{runtime,builders,codex_oauth}.go`, `internal/egressaudit/{attempt,view}.go`, Store egress-attempt repository/schema/delta/migration tests; actual account SOCKS/TLS fixtures and OAuth manager tests. No Environment choice format activation until Task3.

**Interfaces:** add `provideraccount.Manager.BindEgressDefaults(egressprofile.Resolver) error` alongside existing profile binding; `providerauth.AccountRef.ResolvedEgress egressprofile.Resolved` accompanies its existing effective `EgressProfile` with exact equality validation. `providertransport` receives `Resolver` via production composition for account-free operations. The separate login interface is:

```go
type LoginTokenClient interface {
    FreezeLoginEgress(context.Context) (egressprofile.Resolved, error)
    ExchangeLoginCode(*http.Request, egressprofile.Resolved) (*http.Response, error)
}
// codexoauth.LoginOptions.TokenClient replaces its first-login Client field;
// existing HTTPClient.Do(request, AccountRef) remains for account refresh.
```

- [ ] **RED:** Extend real `TestAccountEgressThroughSOCKSForEverySupportedOperation` with accountunset/globalSOCKS for independent quota/history/models/reset and test first Login.Start→real callback→fixed token endpoint. Assert real proxy hit, zero direct target dials and the same frozen exit for reset details+consume. Managed model/pre-lease refresh must already preserve account-explicit > concrete flow-explicit precedence, including explicitDirect against a different global, and share the selected exit between their owned refresh and model; actual inherited model-flow/global activation belongs to Task3 when authored Choice exists. Do not reinterpret legacy concrete Direct as inherit merely to make a Task2 test green. Current hardcoded owner/first-login Direct must fail the new global outcomes; no live login/credit.
- [ ] **Freeze account once:** In actual acquire/manual-refresh ownership, copy and validate current account/settings, take existing operation/in-flight ownership, release manager.mu, resolve account override vs current concrete flow expressed through Explicit (owner/endpoint discovery uses Inherit), then pass the same Resolved/profile to credential preparation and resulting lease. Recheck existing revision/epoch/closing rules without resolving again. Explicit account succeeds despite unavailable default storage. Concurrent shared refresh belongs to its own already-frozen operation: a waiter neither changes that request nor describes its route as the waiter's route; after joining, its own subsequent model send uses its one independently admitted resolved exit. Test the shared-wait ordering with real gates and no second token exchange.
- [ ] **Freeze runtime and first login:** For models.dev directory/prices and other existing credential-free fetches, resolve inherit/inherit once at logical operation entry before Hold/audit. Account-backed fetches accept only their leased resolved value. First Login freezes before publishing pending state/listener/authorizationURL, outside login.mu then rechecks lifecycle; pending carries exit through code exchange. Keep exact token URL/method/content type, bounded body erasure, no Authorization access token, original callback/origin checks, and no AccountRef fabrication. Global failure creates no usable pending login/listener.
- [ ] **Typed audit:** Extend `DecisionRef/DecisionView` with `SelectionSource` (empty for legacy only) and `DefaultRevision`. Add two checked columns with legacy empty/0 defaults to `runtime_egress_attempts` and its v1 delta. Structural legacy decoder permits empty/0; every newly touched producer must provide a valid full pair. Global/builtin require positive setting revision; explicitaccount/flow require0. Existing PolicyID/Authority/AccountSettingsRevision retain their original meanings; actual ProxyID/revision, probes and socket derive from the same Resolved. Complete/recovery/read reconstruct exactly, never consult current defaults. Migration asserts new legacy columns remain initial values, legacy typed fingerprint ignores only declared new columns.

For account-free runtime operations that now actually select global, replace the previous BuiltInDirect decision with typed constant policy ID `runtime.egress-default`, policy revision equal to the frozen default-setting revision, and stable rule ID `global.default`; retain the purpose's existing authority. This is an actual configuration owner, not encoding metadata in RuleID. Builtin-from-unset retains the built-in direct policy reference and separately records its real DefaultRevision. Account/captured-flow policy references remain theirs with truthful effective selection fields; never label a proxied account-free request BuiltInDirect. No account ID/settings revision may be invented for first login or public metadata.
- [ ] **GREEN/fault/race:** Real SOCKS per operation, proxy down/DoH failure/cancel → zero direct dials; mixed defaults changed while credential rotation/hold/details/PKCE is held retain old exit and exact counts, next independent operation sees new. Verify no token/credit retries, no credentials in audit, error propagation and secret CAS invariants. Incoming Activity ordering stays unchanged. Run affected packages, targeted race, schema/backup compatibility and repository checks, then owned commit and independent review.

## Task 3: Authored inheritance, real request freeze and native provenance

**Files:** `internal/environment/{types,aggregate,snapshot,execution,client_tools,manager}.go`, new `egress_choice.go` and tests; `captureassignment/{manager,types}.go` where applicable; `exchange/{contracts,pipeline,selection}.go`; `provideraccount/manager.go`, `originaltransport/{request,client}.go`, `nativemcp/{executor,session,transport,stream,native_sensitive_callback}.go`, and `productruntime/runtime.go` composition; `nativetool/{types,binding_evidence}.go`; Store native binding/audit/stream/callback/schema owners; `desktopcontrol/environments.go` / native-route builder; Dart strict Environment/native DTOs and their roundtrip tests. No user-visible selection control until Task6, but DTOs must accept and preserve the new legitimate shape.

**Interfaces:** authored fields retain JSON key `egressProfile` but use comparable Choice. The original-forwarding RequestLease and each native logical operation's ClientToolPlan get one private Resolved value; a missing effective accessor errors, never returns placeholderDirect. Native ingress leases retain validated authored scope until RPC classification identifies an independent operation or an existing callback/stream parent; do not manufacture another lease or mutate an issued lease to resolve that distinction. Add `EgressDefaults egressprofile.Resolver` to `nativemcp.Dependencies`, wired to the same Runtime default manager, for independent native operations only. AccountLeaseRequest carries authored flow Choice until an account winner is known. These are the new execution interfaces:

```go
func (plan environment.RequestPlan) EgressChoice() egressprofile.Choice
func (plan environment.RequestPlan) WithResolvedEgress(egressprofile.Resolved) (environment.RequestPlan, error)
func (plan environment.RequestPlan) ResolvedEgress() (egressprofile.Resolved, error)
func (plan environment.ClientToolPlan) WithResolvedEgress(egressprofile.Resolved) (environment.ClientToolPlan, error)
func (plan environment.ClientToolPlan) ResolvedEgress() (egressprofile.Resolved, error)
// Keep ClientToolPlan.Route() authored; it must not silently substitute global.
func (compiler environment.Compiler) CompileSystemTransparentRevision(environment.Revision) (environment.EnvironmentSnapshot, error)
```

- [ ] **RED/legacy bytes:** Create actual old Direct/proxy Environment and native-route byte/digest fixtures from fixed938. Choice-aware compilation must roundtrip them exactly, and valid newinherit must reach an effective real SOCKS path. Unknown/null/missing/mixed choice cannot reach transport. Fail-closed scaffolding may establish compilable shape; do not use compile failure as RED.
- [ ] **Authored representation:** Replace direct field access with strict Choice validation, clone/compare/canonical DTO handling. Normal publish validates explicit profile against actual published revision, accepts inherit without prematurely resolving global. Preserve old explicit JSON order/digest. New draft defaults inherit. Built-in SystemTransparent requires a NEW current revision with inherit; retain exact frozen V1 Direct definition and system-only compiler path. Manager.GetRevision returns exactV1 or newV2, while Resolve/Get/current projection useV2; unknown versions reject. Old assignments still resolve V1+digest, not silently relabeled. Never put system configuration into user heads or route regular Restore through privileged system normalization.
- [ ] **Freeze at real admission:** Add Resolver to captureassignment options/composition. Actual original-forwarding BeginRequest resolves only after valid authored plan/assignment checks and outside state.mu; release request counters on failure. Native BeginRequest validates authored scope but cannot resolve global blindly: the current caller has not yet parsed the RPC or associated a reply/recovery with its real parent. In the native executor, after bounded classification and current session/parent association, freeze exactly once for a new independent operation outside owner/session locks, or copy the validated real parent's effective plan for a related child without reading current defaults. Recheck existing parent/session/current-authority conditions at Bind/Start after any unlocked lookup. This happens before outbound audit, Offline Hold and send, and creates no second registry or authority. Managed plans keep authored flow until actual Account.acquire, so broken global cannot block an explicit account. Existing direct getters at send sites are replaced with checked Resolved access; original/raw/transformed HTTP, Offline target and audit use that exact value. Explicit local-only cleartext provider transports remain their documented direct-only boundary, not a global bypass for general internet traffic.
- [ ] **Native effective binding:** Preserve Route/authoredEnvironment equality in Store.nativeEnvironment; add a separate effective fact to ClientToolPlan, Binding and BindingEvidence. Old binding/v1 hashing uses the original struct/order only. New bindings use domain `native-binding/v2` over the original evidence plus canonical Resolved evidence; newStart/Verify require v2 effective consistency. Store a typed immutable `native_tool_binding_egress` row owned by operation/binding with exact profile JSON, closed source, default revision and binding version. Read original stored evidence for Verify/audit reconstruction, never resolve live global there. SQL ownership cascades from operation; immutable insert/update/delete rules parallel original binding. Explicit route requires identical sourceflow/profile; inherit requires global/builtin, never sourceaccount. Historical v1 remains readable as historicalunknown, cannot manufacture new live permission or inherit from current defaults.
- [ ] **Native child/stream invariants:** Original native request freezes before Bind/Start; callback and recovery child carry the effective exit from their real existing parent/witness/StreamAuthority. `executor.bindRequest`'s existing `parent.Plan()` override is the callback seam; recovery uses the exact registered stream's frozen binding plan and retains fresh own receipt/audit. The session's `s.plan` remains authored session identity, not a session-lifetime effective exit: independent calls and unrelated listening GETs in that same session resolve anew. Bind from the selected per-operation plan; use that same binding plan for outbound construction, Offline probe and physical send, not the ingress local variable after a parent override. Tests must change or make unavailable the current default while a real callback/recovery remains live: the child retains the old exit with zero new default reads, while the next unrelated operation sees the new setting or fails closed. No SQL lookup may recreate parent send authority, no account aliasing, no second registry, no extra POST. Store audit reconstruction/NewClientToolAttempt checks effective against binding as well as authored scope.
- [ ] **Resolve adjacent known candidates:** Run real tests for OpenObservedStream expired-logical-terminal pruning and both BindRecoveryGET unavailable-drop points before accepting this native change. Use genuine receipt, logical completed/openphysical, injected real rollback/commit-error, old/new child ordering; retain first failure. If the existing cleanup owner can be lost, fix only demonstrated drop decisions by the reviewed existing I3 preservation contract; otherwise record actual safe predicate/evidence. No blanket rewrite of permanent Recover/Store.Close. This closes the previously recorded candidate audit, not arbitrary scope growth.
- [ ] **GREEN/migration/review:** Actual original/native/account-explicit/global routing, default edit while parent/callback/recovery held, explicitDirect, stale/foreign/default-mismatched child, oldreceipt denied, no duplicate sends, counts/approval immutable. Preserve full populated-v1 upgrade, frozen builtinV1 and nativev1 golden, reject oldprivatev2 untouched, bootstrap/delta/native-schema parity and bounded retention/Capture deletion. Run affected Go/Dart normal, focused native/lease/schema race and repository rules; self-review, owned commit, independent authority/data review. Update continuation plan Task1/2/3 to consume original Resolved in child leases/origin insertion/physical GETs before its implementation; its original46 and modern/145 recovery gates remain.

## Task 4: Blind/cleartext forwarding and matching probes

**Files:** `internal/blindtunnel/{dialer,probe}.go`, `internal/loopbackproxy/handler.go`, `internal/productruntime/{offline,original_transport,builders,runtime}.go`, connection/audit projections and real tunnel/egressnetwork/Offline tests. Reuse Task1 resolver and Task2 typed audit selection.

**Interfaces:** `blindtunnel.DialRequest` gains required `Egress egressprofile.Resolved`; dialer builds from `Egress.Profile().Policy` per new connection, not a host-wide default. Caller freezes once before attempt audit and Hold; probe target receives identical policy. No caller-supplied destination beyond existing authorized target.

- [ ] **RED:** Actual allowed private test CONNECT and cleartext-forward request with globalSOCKS must increment only SOCKS target counter and record actual proxy, while blind resume probe uses same path. Current construction/probe Direct fails. Existing unauthorized/private-target guards still produce zero target sockets.
- [ ] **Implementation:** Replace blind constructor's fixedDefaultPolicy with existing policy builder at each admitted request using requiredResolved. Handler obtains global only when no applicable explicit flow exists; immutable tunnel/probe/audit hold the same selection. Existing network decision mode/rules, approval, authorization, unknown host/port policy and payload opacity unchanged. Policy resolver uses explicit DoH transport as authored; no recursiveglobal routing of SOCKS/DoH bootstrap.
- [ ] **GREEN:** Proxy failure before and after Hold cannot make target direct/probe direct; default changes while tunnel open leave it intact, next tunnel usesnew. Genuine existing cleartext outside-provider forwarding retains headers/body rules, while local control/callback/provider exemptions remain functional. Test strict TLS and private-address checks with proxied DNS resolution; network selection never expands allowed targets. Run blind/loopback/Offline/egressnetwork normal/race, audit evidence consistency, then commit and independent network review.

## Task 5: Closed Runtime update check, no Flutter bypass

**Files:** new `internal/productupdate/{update,update_test}.go`; `providertransport/runtime_fetch.go`, Runtime composition/accessor and `productruntime/offline.go` resume dispatch; `originaltransport/probe.go` and focused probe/Runtime Hold tests; new `desktopcontrol/product_update.go` + tests; `ui/flutter_app/lib/core/api/{product_update_api,control_api}.dart`, preview API and existing update tests.

**Interfaces:** new `productupdate.Reader` implements `Check(context.Context) (productupdate.Result,error)`, where `Result{CheckedAt time.Time; Release Release}` and `Release{Tag,Name,URL string; PublishedAt time.Time}` contain only validated bounded metadata. `productupdate.New(egressprofile.Resolver, Transport, func()time.Time) (*Reader,error)` consumes `Transport{FetchProductRelease(context.Context, egressprofile.Resolved) (*http.Response,error)}`; that provider method accepts no target/path from caller. Owner-only `POST /api/v1/product-update/check` with strict `{}` body returns `{"checkedAt":"...","release":{"tag":"vX.Y.Z","name":"...","url":"https://github.com/vibe-agi/vibermate/releases/tag/vX.Y.Z","publishedAt":"..."}}`. Local App version/channel comparison remains in Flutter.

- [ ] **RED:** Drive actual authenticated control request to private canonical GitHub TLS endpoint via globalSOCKS; expect exact fixed GET, bounded response, update purpose/audit. Flutter update action must call its ControlAPI, not construct a host `http.Client`. Existing direct GitHub service fails this behavioral assertion.
- [ ] **Implementation:** Preserve exact `api.github.com/repos/vibe-agi/vibermate/releases/latest`, Accept/API-version headers, draft/prerelease rejection,256KiB response and8s operation budget, strict version/date/allowed-release-URL checks and no redirects. Resolve global once before queue/audit; no GitHub/credential token added, no download/install endpoint. Runtime reader handles only this closed operation. Keep installation detection and currentApp/CLI/runtime version display local; never use remoteServer installchannel as localApp fact. Do not leak raw network errors.
- [ ] **Real Hold/resume integration:** Keep actual `PurposeUpdate`/`EgressUpdate`; do not relabel the update as auxiliary merely to bypass missing dispatch. Current `runtimeResumeProber` and `originaltransport.TLSProber` reject this kind because no Runtime update operation existed. Add only the update arm to the existing strict TLS prober path, preserving the queued target and frozen policy, exact origin/authority/SNI agreement, timeout, TLS verification and no HTTP headers/body/credentials during probes. Plugin egress remains unsupported; no arbitrary caller-supplied URL or new external-operation permission. With actual Runtime Hold, enqueue the fixed update check, change default, resume through the real dispatcher and prober, and prove probe plus fixed metadata GET keep the original exit. Cancellation/8s expiry drains the queued request without a later send; mixed queued provider/update requests resume without the update poisoning the entire probe set.
- [ ] **GREEN:** UnknowncallerURL/extra body/method/redirect/oversize/malformedrelease → deny or sanitizedunavailable; proxy down→zero direct targetdials. UI receives samemetadata via local/remoteRuntime, oldserver404/unauthorized/runtimeunavailable→unavailable, no direct fallback. Run actualcontrol+SOCKS, Runtime Hold/update-probe and FlutterAPI/updateview tests, repository checks, commit/review.

## Task 6: Global/default/inheritance UX and full egress-family acceptance

**Files:** `ui/flutter_app/lib/features/workbench/{settings_view,egress_profile_editor,provider_accounts_view,workbench_controller}.dart`; strict control models/API/preview; `app_copy.dart`; setting/account/environment/native/update widget/API tests; bounded real Go integration fixtures in existing providertransport/productruntime/blindtunnel/desktophost tests; release/support docs.

- [ ] **UI RED:** Using real widgets+fake control transport, click ‘set as default’ and require PUT with current expectedrevision/exactprofile reference then show only acknowledged default. ExplicitDirect and inherit render differently; existing code has no defaultaction and must fail. Existing profile edit/create functions continue using their original revision semantics.
- [ ] **UI implementation:** Add currentdefaultsummary and ‘set as default’/‘clear default’ actions; builtinDirect selectable/noteditable. On selecting latest profile revision update default only via explicitaction; if defaultpinsolderrevision show actualversion in details. Handle loading/conflict/unconfirmedwrite by rereading, never optimisticfalse-success or blind retry. Account inheritance copy states independentoperationsuseglobal; flow chooser adds inherit and currentresolvedpreview, saves Choice not previewvalue. RemoteRuntime label explains proxy address belongs toServer; no realconfiguration change by agent. Keep permissions and disabledpreview behavior.
- [ ] **Real family matrix:** For model+preleaserefresh, ownedquota/history/details+reset, capturedread, endpointmodels, firstOAuth, models.dev/prices, originalraw/transformed, nativecall/reply/recovery, blind/cleartext/probe and update, assert priority table at actual destination/proxy counters and immutable audit. Include explicitaccountproxy/explicitaccountDirect/explicitflowproxy/explicitflowDirect/globalproxy/unsetbuiltin; absence of a layer skips only thatlayer. Required identity assertion shape:

```go
if directTargetDials != 0 || proxyTargetDials != 1 { t.Fatal("wrong frozen egress") }
if after.Decision() != before.Decision() { t.Fatal("terminal rewrote selection") }
if sentPOSTs != 1 { t.Fatal("egress failure retried an external action") }
```

Use real bytes/operation-specific counts; these three checks are not substitutes for actual Account/ToolDecision/once/credential/target assertions. Closedproxy cases require failure+zero directtargetdials, not a successfulmock response. Holdrequest/refresh/login/tunnel/child whilechangingdefault; release/joinallrealconsumers and prove old/new outcomes without sleep-chosen winners.
- [ ] **UI/subproject verification:** EN/zh-CN390px/200%keyboard accessibility, sameprofile r1/r2, accountunset/exactDirect, staledefault/conflict/readonly views, olderServerunavailable and preservation of selection across refresh. Run fullFlutterformat/analyze/tests and affectedChrome tests; affectedGo normal, scopedrace, repository checks and source-bound real egress-family integration. Update capability docs only for actual proved coverage; do not promote fake/synthetic classifier to production. Add explicit global+override scenarios to the master final-source exactclients/native46/soak/installedcandidate gates: those execute after continuation/other remaining source work, not as a circular prerequisite for beginning continuation.
- [ ] **Commit/final subproject review:** Freeze exactsource, recordallfirstfailures/outcomes/pins/artifacthashes and per-family evidence matrix, independently review fullsix-unitrange. Only then mark global-default subproject complete; master continuation/full46/CA/recordingdiagnosis/60minsoak/10day/release still remain if not separately satisfied.

## Verification commands and source-bound handoff

Each unit runs its targetedRED alone, thenGREEN and the packages listed in thatunit. Final common checks are:

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly /usr/local/go/bin/go test -count=1 ./internal/egressprofile ./internal/egressnetwork ./internal/provideraccount ./internal/providertransport ./internal/codexoauth ./internal/environment ./internal/captureassignment ./internal/nativetool ./internal/nativemcp ./internal/runtimepersistence ./internal/runtimedata ./internal/originaltransport ./internal/blindtunnel ./internal/loopbackproxy ./internal/productruntime ./internal/desktopcontrol
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly /usr/local/go/bin/go run ./cmd/repositorycheck
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly /usr/local/go/bin/go test -count=1 ./internal/repositorycheck
git diff --check
```

Never call a listed test passed without actualmatchingexecution; test files named by this plan are created in the owningunit, not assumedexisting. Newinterfaces must be minimally fail-closed scaffolding before behavioralRED. Root must recheck type names and exact ownership after each independentlyapprovedunit before issuing nextbrief; justified changes go in this plan and report, not secret compatibility wrappers.

## Controller self-review

Spec coverage: Task1 ownsdurability/precedence/choice/control;Task2 realaccounts/publicmetadata/firstOAuth+audit;Task3 strictinherit/oldcanonical/nativeeffectiveauthority/builtinversioning/adjacentdropgates;Task4 blind/probes;Task5 realFlutterexternalupdategap;Task6 UX/completefamilymatrix/finalsourceacceptance. Explicitdirect/error-vs-unset/inflightfreeze/noauthoritymint/localcontrol areglobalinvariants andnegativecontrols. Default state isonepersistentowner, low-level dialer remains purepolicyconsumer; no newgenericproxy/framework. OldV1 data andhistoricalconfig/digests preserved, privateV2 incompatibilityrejects ratherthanreset. Nativecontinuation sequencingexplicit andallbroadergoalgatesretained.
