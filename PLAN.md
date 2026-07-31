# M1.0 Proxy/Trust Foundation Plan

Status: active
Created: 2026-07-31
Implementation baseline: `750e8aa55522d52bfe432084b4e97e9246eea8db`

## Objective

Close the first installable trust boundary without widening the Agent protocol
matrix. A macOS arm64 Desktop user must be able to inspect, explicitly install,
verify, and explicitly remove the one existing installation Root through a
Host-owned platform adapter. Exact AgentEndpoint leaf issuance must become
bounded, revision-aware, invalidatable, and safe under concurrent cold starts.

This slice keeps the current one-listener, exact CONNECT/SNI, H1 semantic path
working. It does not add another Root, a client-private trust store, a secret
backend, an OpenAI Chat Agent edge, or automatic system changes.

## Read-only design authority

- `docs/design/00-overview.md` M1 proxy foundation boundary;
- `docs/design/02-architecture.md` sections 5.3, 9, and 14;
- `docs/design/06-security.md` sections 9.5 and 10;
- `docs/design/10-client-compatibility.md` sections 1 through 3 and 8;
- `docs/design/11-delivery-and-operations.md` section 2;
- `docs/design/14-technology-stack.md` section 4;
- `docs/design/15-local-control-api.md` platform Root routes;
- `docs/design/18-production-composition.md` sections 7 and 8;
- `docs/design/19-hosts-and-deployment.md` Desktop Host boundary;
- `docs/adr/0006-agent-endpoint-mitm-allowlist.md`;
- `docs/adr/0007-client-and-upstream-authority-separation.md`;
- `docs/adr/0011-shared-runtime-and-host-shells.md`.

The design repository remains read-only. External proxy implementations may
inform wire fixtures, cache failure tests, and performance questions only. No
external code, package layout, names, registries, patched dependency, or
documentation enters this repository.

## Current implementation gap audit

| Boundary | Already implemented and proved | Missing before the product can claim it |
|---|---|---|
| Root identity | One persistent ECDSA P-256 Root, private files, reopen continuity, exact-host leaves | OS trust inspection/install/remove, explicit Root revision, replacement lifecycle |
| Leaf issuance | Canonical DNS/IP validation, exact SAN, race-safe mutex cache | Bounded LRU, same-key cold-start coalescing, panic/cancel waiter release, endpoint/root revision cache identity, invalidation |
| Downstream proxy | One authenticated loopback CONNECT listener, exact AgentEndpoint lookup, SNI equality, per-request endpoint revalidation | General connection policy, unmatched-endpoint blind tunnel, system/application proxy ownership |
| HTTP transport | H1 MITM, SSE, cancellation, bounds, strict upstream TLS | H1 wire shadow, H2 ingress/upstream, compression matrix, blind WebSocket forwarding |
| Fingerprint | Captured ClientHello, uTLS observed/standard H1 profiles, requested/effective fallback evidence | H1 wire-shape evidence, H2 capability matrix and wire fixtures, pool isolation |
| Desktop delivery | Packaged ad-hoc App and Host-owned readiness | Root authorization UX, signed helper decision, system proxy restore, signing/notarization/installer |

Only the first two rows and the explicit Desktop Root action are in M1.0.
Connection policy, blind tunneling, system proxy ownership, H2, and release
packaging remain separately reviewable successors.

## Required invariants

1. Each installation has exactly one Root. No per-Access, per-Profile,
   temporary, remote, or client-private Root mode is introduced.
2. Root private-key bytes remain inside `internal/localca` and never cross a
   control, UI, report, log, helper argument, or platform-adapter boundary.
3. A `TrustStoreChangePlan` is derived from the current immutable public Root
   identity. The UI cannot supply an arbitrary certificate path, fingerprint,
   trust store, or removal target.
4. Trust mutation occurs only after an authenticated explicit user action. App
   startup, daemon restart, tests, status polling, and Access apply never prompt
   for OS authorization.
5. `applied` is reported only after a fresh platform inspection observes the
   expected trust state. Cancellation, timeout, insufficient privilege,
   unknown observation, and manual fallback remain non-success outcomes.
6. The Desktop Host owns the platform adapter. `ProductRuntime` owns the Root
   and public Root projection but does not invoke an OS command or select a
   platform driver.
7. Platform selection uses typed construction and ordinary platform-specific
   compilation. There is no string driver registry, blank-import registration,
   global service locator, or `func(any)` injection.
8. Leaf issuance accepts a typed request containing the exact active
   AgentEndpoint identity/revision, Root revision, canonical host/SAN, and
   algorithm. A host string alone is no longer sufficient authority.
9. Cache identity includes every value that changes the leaf result. The cache
   is bounded and failures are not cached. Same-key concurrent misses perform
   one generation; panic, cancellation, and errors release every waiter.
10. Endpoint disable/delete or revision replacement prevents any later issue
    from reusing the old authorization and removes obsolete entries from the
    bounded cache. Existing established connections retain their already
    completed TLS state and are still revalidated per HTTP request.
11. The existing strict upstream system-root TLS path is unchanged. The local
    Root is never added to provider trust and no skip-verification option is
    introduced.
12. All new user-facing text uses synchronized nonempty `en-US` and `zh-CN`
    catalogs; API status/reason codes remain language-independent.

## Bottom-up TDD execution

### 1. Freeze public Root and trust-operation contracts

- [ ] Add immutable `RootIdentity`, `RootRevision`, `TrustStoreChangePlan`,
  `TrustStoreChangeResult`, `TrustObservation`, and closed status/reason enums.
- [ ] Bind every plan to one exact current fingerprint, certificate digest,
  expected system store, operation, and authorization/manual-fallback policy.
- [ ] Reject unknown fields, arbitrary paths, stale Root revisions, replayed
  plans, and install/remove outcome contradictions.
- [ ] Keep certificate PEM available only through the existing public Root
  boundary and keep the private key structurally unreachable.

### 2. Harden the local leaf authority

- [ ] Promote the design-pinned LRU and singleflight dependencies to direct,
  fixed module requirements when production use begins.
- [ ] Replace the unbounded host map with a bounded typed cache keyed by Root
  revision, AgentEndpoint ID/revision, exact SAN identity, and algorithm.
- [ ] Coalesce concurrent cold issuance and prove leader panic, cancellation,
  random failure, and signing failure wake all waiters without caching failure.
- [ ] Add explicit endpoint invalidation/reconciliation after active-plan
  publication without making the CA a second Access authority.
- [ ] Preserve close/reopen Root identity, exact SAN, private permissions,
  independent returned certificates, and bounded shutdown.

### 3. Implement the macOS Host trust adapter

- [ ] Build a typed Darwin adapter around fixed platform operations with
  explicit argv, bounded output, cancellation, deadlines, and no shell command
  construction.
- [ ] Inspect the system trust store by exact certificate fingerprint and
  distinguish trusted, not trusted, unknown, and duplicate/conflicting state.
- [ ] Execute install/remove only from a validated current plan, then always
  re-inspect before returning a result.
- [ ] Map user cancellation, timeout, permission denial, unavailable automatic
  removal, and manual Keychain Access recovery to stable outcomes.
- [ ] If a signed helper or native authorization API is required for a real
  applied result, freeze that Host dependency choice before adding it; do not
  smuggle reusable administrator authority into the daemon.
- [ ] Prove the adapter cannot install or remove an arbitrary certificate and
  cannot expose the Root private key.

### 4. Compose authenticated Desktop control

- [ ] Add only the contracted Root status/install/remove routes needed by this
  slice, with existing read/write capabilities, exact Origin/Host/Fetch
  Metadata checks, strict bodies, idempotency, and bounded operations.
- [ ] Keep the adapter outside `productruntime.Start`; DesktopHost receives it
  as an explicit typed dependency and merges its observed state into the Host
  projection.
- [ ] Ensure failed or canceled trust operations do not affect runtime
  initialization, proxy readiness, Access projection health, or the existing
  `vibermate run` same-Root export path.
- [ ] Make shutdown cancel and drain active inspections/mutations without
  reporting a false completed state.

### 5. Add UI only after Host contracts pass

- [ ] Show current Root fingerprint, expiry, private-file health, observed
  trust state, and one explicit install/remove action.
- [ ] Explain the system authorization and exact cleanup consequence before
  mutation; do not ask for or retain an administrator password in VibeMate.
- [ ] Render cancellation, manual steps, residual trust, and retry from stable
  i18n keys; never infer success from a closed dialog.
- [ ] Keep trust actions separate from provider credential storage and Access
  apply.

### 6. Prove the real macOS boundary

- [ ] Add deterministic executor fixtures for applied, canceled, timed out,
  permission denied, needs-manual, malformed output, and residual-trust cases.
- [ ] Add an opt-in macOS system-trust acceptance that installs only the
  disposable current test Root after explicit user authorization, verifies a
  real TLS client handshake, removes that exact Root, and verifies it is no
  longer trusted.
- [ ] Resolve targets read-only before every destructive trust action and
  retain only fingerprint/status/timing evidence.
- [ ] Keep ordinary CI and packaged M0 acceptance free of authorization
  prompts.

### 7. Final gates

- [ ] `make check`
- [ ] `go test ./...`
- [ ] `go test -race ./...`
- [ ] `go vet ./...`
- [ ] `go mod tidy -diff`
- [ ] `go mod verify`
- [ ] fixed `govulncheck ./...`
- [ ] pinned frontend TypeScript/unit/build checks
- [ ] pinned Rust format/tests and an honestly reported RustSec warning set
- [ ] `git diff --check`
- [ ] clean tracked source and opt-in evidence bound to its exact revision

## Explicitly deferred

- Root replacement/old-new migration windows and uninstall orchestration;
- system or per-application proxy enable/compare-and-restore;
- connection `allow/deny/ask`, unmatched-endpoint blind tunneling, and L4
  firewall persistence;
- H1 wire-shadow replay, HTTP/2, compression expansion, and blind WebSocket;
- upstream HTTP/SOCKS proxy profiles and transport pooling;
- additional Agent protocols, providers, models, or fixed executable releases;
- native release SecretStore, signing, notarization, installers, Server, and
  Windows/Linux.

## Completion statement

This plan is complete only when evidence supports:

> The macOS Desktop Host can derive an exact change plan for the installation's
> single persistent Root, perform an explicitly authorized install or removal,
> re-inspect the system trust store before reporting the result, and serve only
> bounded revision-authorized AgentEndpoint leaves from a concurrent-safe
> cache. No startup path prompts, no private key crosses the Host boundary, and
> strict provider TLS remains independent.

Even then, VibeMate is not Preview-ready or Release-ready. It will still lack
system-proxy ownership, unmatched-endpoint blind tunneling, H2, full M1
fingerprint/wire evidence, signed/notarized delivery, release secret protection,
and the multi-platform acceptance matrix.

## Successor order

1. M1.1 Connection Policy, blind tunnel, and system/application proxy recovery.
2. M1.2 H1 wire preservation, compression, and blind WebSocket transport.
3. M1.3 H2 semantic transport, stream isolation, and capability evidence.
4. Re-audit packaging/signing and the remaining M1 acceptance boundary before
   resuming deferred protocol-width work.
