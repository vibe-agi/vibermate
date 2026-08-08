# Runtime Module Map

This is the implementation index for the current Environment-first production
slice. The design repository remains the architecture authority. A module being
present does not imply that its product surface is complete.

| Module | Owns | Does not own | Current evidence |
|---|---|---|---|
| `internal/environment` | Typed Environment aggregate; exact ClientEndpoint, ProtocolPlan, UpstreamRoute, account/model/plugin policy references; canonical digest; immutable snapshot resolver; private draft, impact preview, and CAS publication; Core-owned `system_transparent`. | Capture identity, secret values, network I/O, UI, or provider execution. | Aggregate/reference validation, alias isolation, shared origins across Environments, monotonic revisions, draft conflicts, impact classification, publication failure, poison/reopen, concurrency and race tests. |
| `internal/captureidentity`, `internal/captureassignment` | Globally typed managed/manual Capture references and the durable Capture → Environment assignment. Launch authority freezes Environment revision/digest plus protected and managed-credential scopes. | Client process verification, endpoint matching, routing, or secret access. | Create/resolve/switch CAS, hot/reconnect/restart boundaries, launch-scope expansion refusal, Environment-publish transition checks, SQLite reopen and race tests. |
| `internal/capturerun`, `internal/manualcapture`, `internal/capturecredential`, `internal/captureadmission` | Durable managed and manual Capture lifetimes; opaque proxy capabilities; route-neutral authenticated admission and attribution. | Environment, route, account, model, or plugin selection. | Credential namespace separation, one-time issue/rotation/revocation, recognized-client evidence, signal/shutdown behavior, owner isolation, reopen and race tests. |
| `internal/capturegrant`, `internal/capturecontrol`, `internal/manualcaptureclient` | Control-authorized Capture issuance. The selected Environment is resolved before creation; transparent grants omit Root material and semantic grants freeze exact authority. | Calling-principal authentication, direct repository access, or caller-selected authority expansion. | Pre-create context binding, TOCTOU refusal, transparent/semantic Root boundaries, local HTTP/CLI contracts, cancellation and redaction tests. |
| `internal/runtimepersistence` | The single unreleased SQLite baseline, schema identity, repository operation gate, Environment/draft/assignment, Capture, ProviderAccount configuration, Activity, approval, connection, egress and runtime metadata authority. | Snapshot compilation, protocol behavior, secret values, or HTTP DTOs. | CAS, rollback, ambiguous-commit reconciliation, permissions, corruption, cancellation/drain, source-bound schema, archive recovery, reopen and race tests. |
| `internal/productruntime` | Sole business-runtime composition and shutdown tree: persistence, Environment projection, Captures, assignments, transports, Exchange, local CA, proxy, journals, approvals and Offline Hold. | Listener binding, Desktop discovery/readiness, system trust mutation, or UI. | Startup rollback, recovery, lifecycle order, Environment health, provider/proxy assembly, shutdown and race tests. |
| `internal/desktophost`, `internal/desktopdaemon`, `cmd/vibermated` | Sole production Host composition, literal-loopback proxy/control listeners, authenticated Webview session, private discovery, readiness and process lifetime. | Business configuration authority or provider semantics. | Composition-uniqueness structural checks, real listener/bootstrap tests, generation ownership, child launch, graceful shutdown and rollback tests. |
| `internal/desktopcontrol` | Authenticated local Environment, ProviderAccount, Capture assignment, Activity/Exchange, approval, connection-policy, Manual Capture, status and Offline Hold HTTP projections. | SQLite access, secret values, localization, or a second write authority. | Exact route/ID parsing, origin/capability separation, secret-free account responses, CAS/idempotency, typed restart result, frozen Activity references, invalid legacy route rejection and race tests. |
| `internal/localca`, `internal/certidentity` | Persistent public Root identity/revision, private signing key, exact-DNS leaf issuance, bounded cache/singleflight, and Environment-owned one-use leaf admission. | System trust mutation or endpoint authorization independent of the current Environment. | Manifest recovery, cache/revocation, SNI/SAN/origin agreement, waiter cancellation, shutdown and race tests. |
| `internal/loopbackproxy` | Authenticated CONNECT/HTTP handling, policy before dial, blind forwarding for transparent Captures, exact semantic MITM, path classification, per-request endpoint revalidation and data-plane shutdown. | Listener ownership, Root trust installation, provider secret retrieval, or configuration writes. | Transparent no-MITM forwarding, exact ClientHello admission, H1/H2 intersection, connection draining, semantic/opaque separation, unsupported payload rejection, audit and race tests. |
| `internal/protocolspec`, `internal/operationcatalog`, `internal/pathcapability`, `internal/protocolpath` | Immutable operation and protocol definitions plus the closed selector for executable Anthropic Messages and OpenAI Responses paths. | Network I/O, routing, accounts, or global codec registration. | Canonical method/path/query classification, payload class, unsupported operation refusal, request/response translation and streaming tests. |
| `internal/exchange` | One frozen Request execution: Environment/Endpoint/Protocol/Route/Account evidence, codec invocation, attempts, downstream commit ledger, safe fallback boundaries and tool approval handoff. | Environment mutation, listener handling, persistence queries, or secret storage. | Anthropic and Responses complete/SSE paths, tool commit barriers, no replay after visible commit, account lease sequencing, cancellation, correlation and race tests. |
| `internal/provideraccount` | Global upstream account configuration, built-in Anthropic/OpenAI authentication realms, SecretStore references, credential health and attempt-scoped managed leases. | Secret bytes in SQLite/UI, Environment ownership, route choice, provider I/O, or captured-client credential ingestion. | Create/recover/rotate, missing-credential fail-closed, Environment compilation, secret exclusion, SQLite reopen and race tests. |
| `internal/providerauth`, `internal/providertransport`, `internal/originaltransport`, `internal/transportprofile`, `internal/wireprofile` | Account lease/auth application, strict provider/original transport, explicit product presentation, same-protocol wire variants, TLS and response ownership. | Route selection from host names, arbitrary header forwarding, silent redirects, or exact fingerprint claims. | Secret-after-egress ordering, redirect and cleartext refusal, strict TLS, real H1/H2 services, response terminal classification, shutdown and race tests. |
| `internal/offlinehold` | Action/egress admission cut, bounded queue, exact probes, FIFO release, authoritative safe-to-disconnect and shutdown. | Route choice, retry policy, or UI readiness. | Hold races, cancellation, probe/release, deadlines, accounting and race tests. |
| `internal/activity`, `internal/connectionevent`, `internal/egressaudit`, `internal/toolapproval` | Body-free runtime evidence and typed approval lifecycle using frozen Environment/Endpoint/Protocol/Route/Account references. | Prompt/secret storage, current-configuration reinterpretation, or network execution. | JSON closure, relationship validation, pagination, SQLite reopen, expiry/CAS, sensitive-data exclusion and race tests. |
| `cmd/vibermate`, `internal/runlauncher`, `internal/cliinstall` | `vibermate run [--env ID] -- command`, signal-safe one-child supervision, Manual Capture CLI, and receipt-backed `~/.local/bin/vibermate` installation. | Daemon startup, shell-profile mutation, route/account inference, or persistent secrets. | Real Host child launch, stable machine/workspace evidence, environment selection, SIGINT escalation, CLI install collision/tamper and cross-platform tests. |
| `ui/desktop` | Native-shell React workspace for Captures, Requests, Environments, managed Accounts, policy/approvals, settings and truthful deferred resources. Only the native host acquires the local control session. | Browser storage, direct Tauri imports outside the host adapter, secret display after one-time grant, or hidden routing authority. | 104 component/control tests, production build, 13 Playwright desktop/mobile scenarios, managed-account selection, locale drift checks, keyboard focus and no-overflow checks. |
| `internal/secretstore`, `internal/hostsecret` | Typed non-listable secret references, destroyable values and explicit host factory. | Account selection, SQLite secret storage, or release-grade protection in ordinary builds. | CAS, permissions, symlink/path rejection, value destruction, reopen and race tests. The development file driver remains plaintext-equivalent at rest. |
| `cmd/vibermate-acceptance`, `internal/acceptancereport`, `cmd/vibermate-acceptance-verify` | Opt-in packaged deterministic/credentialed evidence with current Environment publication, Capture assignment and recovery checks. | Ordinary CI, provider fixtures, secret disclosure, system trust mutation, or acceptance of historical report schemas. | Producer/verifier schema closure, artifact/source freshness, permission/redaction and substitution tests. No current working-tree packaged report exists. |
| `internal/repositorycheck`, `locales` | Structural architecture guards, production-composition uniqueness, forbidden legacy vocabulary, SDK/egress isolation, and canonical bilingual catalogs. | Product completeness claims. | Public known-good/injected-bad fixtures and locale key/parameter parity. |

## Initialization boundary

`productruntime.Start` commits only internal initialization after SQLite,
Environment recovery, Capture authorities, Offline Hold, transports, Exchange,
local CA and proxy construction succeed. `desktophost.Start` is the only
Desktop readiness owner:

```text
acquire generation ownership
  -> start ProductRuntime
  -> bind proxy and authenticated control listeners
  -> compose Capture and Environment control routes
  -> publish private discovery
  -> publish Desktop ready
```

Shutdown withdraws discovery and admissions first, then drains HTTP work,
Capture/assignment operations, Exchange/egress, background monitors and SQLite
in owner order. A failed cleanup reports `stop_failed`; it is not described as a
successful stop.

## Request authority

The Request linearization order is:

```text
authenticate Capture
  -> resolve one Capture assignment
  -> resolve one immutable Environment snapshot
  -> match exact ClientEndpoint and ProtocolPlan
  -> freeze Route and Account policy
  -> execute attempts behind Offline Hold and commit ledger
  -> append frozen Activity evidence
  -> return only after downstream commit accounting
```

Publishing a newer Environment revision cannot mutate an existing Request.
Already-delivered leaf certificates cannot be retroactively revoked from an
existing TLS connection, so every request on that connection revalidates its
current endpoint and Capture boundary.

## Current completion boundary

The repository proves a coherent local Desktop source composition and browser
prototype. It still does not prove Server Host, linked-session account
connectors, live provider acceptance, automatic account failover, plugins,
QualityRun, Language Bridge, system trust mutation, native secret
storage, signed packaging, notarization, installation, or Preview/Release
readiness.
