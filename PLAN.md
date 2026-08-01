# M1.0-A Revision-Authorized Root and Leaf Authority

Status: complete, including the narrow DNS/IP classification correction
Created: 2026-08-01
Completed: 2026-08-01
Implementation baseline: `cf3f599e11ee13f82fef0e8a6b8c09e38878b124`
Frozen implementation candidate: `c19cca4eb2842aa00d8e8fc17160b342a111f0b6`
Superseded implementation candidate: `6d3b0ec7196f0d8e8fc71afc6c894e180cbe8ca6`

## Objective

Promote the existing single persistent local Root into a stable immutable
public identity with a persistent revision, and move the real AgentEndpoint
leaf path to revision-authorized, bounded, concurrent-safe, invalidatable
issuance.

This slice does not mutate an operating-system trust store, add Host routes or
UI, widen the protocol/provider matrix, or rotate the Root. Existing M0.9
Claude/Codex, Hold/Resume, exact CONNECT/SNI, per-request endpoint
revalidation, and strict upstream system-root TLS behavior must remain intact.

## Read-only design authority and checkpoints

The authoritative design repository is
`/Users/null/Code/github/vibe-agi/vibermate-design`. Its current disk contents,
including uncommitted work, are read-only implementation authority. CodeGraph
is used before text search when its `.codegraph/` index exists.

The relevant baseline is:

- `CONTRIBUTING.md`;
- `docs/design/00-overview.md`;
- `docs/design/02-architecture.md`;
- `docs/design/06-security.md`;
- `docs/design/10-client-compatibility.md`;
- `docs/design/11-delivery-and-operations.md`;
- `docs/design/14-technology-stack.md`;
- `docs/design/18-production-composition.md`;
- `docs/design/19-hosts-and-deployment.md`;
- `docs/adr/0006-agent-endpoint-mitm-allowlist.md`;
- `docs/adr/0007-client-and-upstream-authority-separation.md`;
- `docs/adr/0011-shared-runtime-and-host-shells.md`;
- `docs/adr/0013-core-language-bridge-and-typed-transformer-adapters.md`.

The initial ordered SHA-256 manifest digest for those files is
`7851e4dea6292c93d731deafa352e092352e0ddcc6dbf16ca0e4f7d1b1e32611`.
Re-read and re-hash them after contract tests, after production wiring, and
immediately before freeze. A changed digest requires a semantic diff and goal
reconciliation; it is not automatically accepted or ignored. Record every
checkpoint in the archived completion plan.

Checkpoint log:

- Post-contract checkpoint, 2026-08-01: ordered manifest digest
  `2d38c6df509a15fb9ffc3b60345ce5421cc97d8a3e967887f3b86d8e93c00cea`.
  The live design changed while contracts were being implemented. Re-reading
  ADR-0006 decisions 12–15 confirms the same DER authority, revision-1 v1→v2
  migration, projection-owned admission/revocation cut, bounded concurrent
  cache, and lifecycle semantics implemented here. Production composition
  §8.1 now explicitly keeps Language Bridge out of this Root/leaf slice. No
  M1.0-A object, trust boundary, dependency, or scope correction was required.
- Post-production-wiring checkpoint, 2026-08-01: ordered manifest digest remains
  `2d38c6df509a15fb9ffc3b60345ce5421cc97d8a3e967887f3b86d8e93c00cea`.
  The real ClientHello admission path and the transactional disabled-Access
  withdrawal match ADR-0006: CONNECT/SNI is checked before signing, active
  projection publication is the revocation cut, and disabling removes the
  endpoint from new admission while synchronously invalidating derived cache.
  Language Bridge remains outside this slice.
- Pre-candidate-gates checkpoint, 2026-08-01: ordered manifest digest remains
  `2d38c6df509a15fb9ffc3b60345ce5421cc97d8a3e967887f3b86d8e93c00cea`.
  A fresh CodeGraph-first review and direct semantic check found no change to
  ADR-0006 decisions 12–15 or production-composition section 8.1. The live
  Language Bridge work remains a later Core capability and did not alter the
  Root/leaf authority, trust boundary, dependencies, or completion criteria.
- Final pre-freeze checkpoint, 2026-08-01: ordered manifest digest remains
  `2d38c6df509a15fb9ffc3b60345ce5421cc97d8a3e967887f3b86d8e93c00cea`.
  CodeGraph was consulted first, followed by a direct reread of ADR-0006
  decisions 12–15, production-composition section 8.1, and ADR-0013's scope
  exchange. The frozen implementation still matches the live DER identity,
  revision, admission cut, cache, and lifecycle requirements. Language Bridge
  remains explicitly excluded, so no final goal correction was required.
- DNS/IP correction checkpoint, 2026-08-01: ordered manifest digest remains
  `2d38c6df509a15fb9ffc3b60345ce5421cc97d8a3e967887f3b86d8e93c00cea`.
  A CodeGraph-first review followed by direct ADR-0006 and production-
  composition checks confirmed that the correction closes an implementation
  gap in the existing DNS-only production admission contract. It does not add
  IP issuance, change the trust boundary, or pull Language Bridge into this
  slice.
- Final correction pre-freeze checkpoint, 2026-08-01: after the clean packaged
  runs and non-cached full ordinary/race suites, the ordered manifest digest
  remains
  `2d38c6df509a15fb9ffc3b60345ce5421cc97d8a3e967887f3b86d8e93c00cea`.
  A final CodeGraph-first lookup and direct reread confirmed ADR-0006 still
  requires DNS-only production admission and section 8.1 still defers Language
  Bridge. No goal correction was required.

Language Bridge remains a later Core slice. Its typed transformer, policy,
ledger, Hold, secret, budget, codec, UI, and localization work must not enter
this Root/leaf authority goal. New developer-facing source remains English;
user-facing copy, if any unexpectedly becomes necessary, must use synchronized
`en-US` and `zh-CN` message keys. Error/reason codes remain language-neutral.

## Required invariants

### Root material and identity

1. Each installation has exactly one ECDSA P-256 Root. No per-Access,
   per-Profile, temporary, remote, or client-private Root is introduced.
2. Certificate DER is the Root material authority. Its SHA-256 digest is the
   only machine identity. A display fingerprint is derived from that digest;
   it is never a second persisted truth.
3. `RootRevision` is typed, persistent, starts at 1, and remains unchanged on
   ordinary close/reopen. Certificate delivery paths do not participate in
   identity, equality, authorization, or cache keys.
4. Existing v1 state is migrated without replacing its key or certificate.
   Migration writes a same-directory private temporary manifest, fsyncs the
   file, atomically renames it, and fsyncs the directory. A crash can expose a
   complete v1 or complete v2 manifest, never a truncated manifest. Every load
   recomputes the DER digest and rejects any manifest mismatch.
5. Migration, malformed state, permission failure, digest mismatch, partial
   Root state, or unsupported manifest data fail closed and never silently
   generate a replacement Root.
6. Immutable `RootIdentity` exposes only public certificate identity,
   revision, validity, and algorithm evidence. Root private-key bytes and
   private-key types remain structurally confined to `internal/localca`; they
   never cross Host, API, log, report, helper, UI, or test-business seams.
   Leaf private keys may cross only the existing local TLS-serving boundary.

### Projection-owned issuance admission

7. Production issuance no longer accepts a host string. A typed
   `LeafIssuanceRequest` freezes Root revision, AccessID, AgentEndpoint
   ID/revision, complete ClientOrigin, canonical SAN identity, and typed leaf
   key algorithm.
8. The request type can represent DNS and IP SAN identity, but this production
   slice authorizes canonical DNS only. `NewDNSName` and the shared validity
   predicate reject every value accepted by `netip.ParseAddr`, so an IP literal
   cannot be relabeled as DNS. IP-literal admission remains `unsupported` until
   a separate accepted design decision permits it.
9. A structurally valid request is not proof of authorization. Only the sole
   active Access projection can grant an unforgeable, revision-scoped issuance
   admission after exact ClientOrigin and CONNECT/SNI validation. Callers
   cannot construct, replay, or promote request fields into authority.
10. Active projection publication is the endpoint-revocation linearization
    point. Admissions granted before the cut may finish and serve their
    existing waiters. Admissions requested after the cut for a disabled,
    deleted, or replaced endpoint revision fail closed.
11. An invalidated in-flight generation may finish for pre-cut waiters but
    cannot repopulate the cache. An already delivered certificate or
    established TLS connection is not described as retroactively revoked;
    every inner HTTP request continues to revalidate the current endpoint.
12. Cache state is derived data, never an authorization table. Cache hits are
    considered only after current projection admission succeeds.

### Bounded concurrent authority

13. The leaf cache is a bounded LRU keyed by Root revision, AccessID,
    AgentEndpoint ID/revision, complete ClientOrigin, canonical SAN, and typed
    leaf-key algorithm. Capacity is a validated typed option.
14. Same-key cold issuance performs one generation. Different-key expensive
    generation is not serialized by one global mutex.
15. Shared generation is owned by the Authority lifecycle context and an
    independent bounded deadline, never by the first waiter. Each waiter may
    cancel independently without killing work still needed by another waiter.
16. Signing/random failures, cancellation, timeout, panic, invalidation, and
    shutdown wake every waiter with typed failure. Failures and invalidated
    results are never cached. Returned certificates are independently owned.
17. `Shutdown(context.Context)` closes admission, cancels generation, drains
    owned work within the caller's deadline, clears derived state, rejects new
    issuance, and is idempotent. It cannot report a completed stop while owned
    work is still active.

### Composition and non-regression

18. The real authenticated loopback Proxy obtains projection-owned admission
    and calls only the new authority. The old `Issue(string)` interface and all
    compatible bypasses are removed from production and tests.
19. Endpoint disable/delete/replacement makes old entries unreachable and
    removes them from the cache without giving `internal/localca` a second
    mutable Access registry.
20. ProductRuntime continues to own the one Authority and shutdown tree.
    DesktopHost remains the readiness owner. No trust-store operation, platform
    command, authorization prompt, or new readiness claim is added.
21. Strict provider TLS continues to use system trust and exact target
    hostname verification. The local Root cannot enter upstream trust.
22. Stage names such as `M1.0-A` appear only in plans, commits, and evidence;
    they do not become production package, type, function, catalog, schema, or
    reason-code names.

## Bottom-up TDD execution

### 1. Freeze Root identity and migration contracts

- [x] Add typed immutable Root identity/revision/digest/algorithm values and
  defensive-copy public certificate material.
- [x] Add v1 fixtures and prove private atomic v2 migration preserves exact key,
  certificate DER, identity, permissions, and revision across reopen.
- [x] Inject migration failure points around temporary write, file sync,
  rename, and directory sync; prove complete old/new visibility and no Root
  regeneration.
- [x] Reject digest drift, unknown/trailing manifest data, bad revisions,
  partial files, symlinks, and widened permissions.

### 2. Freeze projection admission and revocation contracts

- [x] Define the typed request and projection-owned admission without an
  exported constructor or reusable bearer representation.
- [x] Prove zero/forged admissions, stale Root/endpoint revisions, foreign
  AccessID/ClientOrigin evidence, mismatched SAN/algorithm, disabled or
  withdrawn endpoints, CONNECT/SNI mismatch, and IP literals fail closed.
  Bare IPv4 and IPv6 values are covered both at the certificate-identity
  constructor and through an IP `ClientOrigin` projection admission.
  Endpoint deletion has no writer/control operation in this slice; the same
  typed withdrawal cut is already the projection behavior a future durable
  delete must invoke.
- [x] Prove the publication cut: pre-cut admissions may complete, post-cut
  admissions fail, and invalidated in-flight results cannot resurrect cache.
- [x] Preserve immutable old plan handles and per-request endpoint
  revalidation without treating them as fresh signing authority.

### 3. Build the bounded concurrent leaf authority

- [x] Promote design-pinned `github.com/hashicorp/golang-lru/v2` and
  `golang.org/x/sync/singleflight` to direct fixed dependencies.
- [x] Replace the unbounded map and global generation mutex with the full-key
  bounded LRU, same-key coalescing, lifecycle-owned generation, and explicit
  invalidation generations.
- [x] Prove capacity/eviction, cache-key isolation, same-key issuance count,
  different-key parallelism, independent waiter cancellation, leader panic,
  random/signing failure, timeout, invalidation, and retry-after-failure.
- [x] Prove race-safe shutdown/invalidation/issuance and bounded idempotent
  drain under `go test -race`.

### 4. Migrate the production Proxy and composition

- [x] Replace `loopbackproxy.CertificateAuthority` and handler use with the
  typed context-aware authority/admission seam.
- [x] Wire the sole active projection into issuance without a callback
  registry, string driver registry, global locator, blank-import registration,
  or a second endpoint snapshot.
- [x] Prove exact CONNECT/SNI/SAN issuance through the real handler, stale
  revision rejection, multi-Access isolation, endpoint publication races, and
  current-request revalidation on persistent connections.
- [x] Prove ProductRuntime reopen identity continuity, failure rollback,
  shutdown ordering, and unchanged upstream strict TLS.

### 5. Reconcile design, documentation, and evidence

- [x] Run the post-contract and post-wiring design checkpoints and record any
  semantic changes and resulting implementation corrections.
- [x] Update `docs/module-map.md`, README status, and package documentation with
  exact authority, lifecycle, evidence, and non-evidence boundaries.
- [x] Add structural checks only for concrete newly protected shapes, each
  with public-entry bad and known-good repository fixtures. No new text scanner
  was added: package visibility, unexported request construction, one-use typed
  admission, and the replaced proxy interface make the concrete bypass shapes
  unrepresentable; the existing public repository checker remains green.
- [x] Re-run deterministic and credentialed M0.9/M0.9.1 packaged acceptance on
  the clean candidate because the production certificate path changed. Reports
  must bind the exact candidate source and preserve honest client-specific
  wording and private permissions.

### 6. Final gates and freeze

- [x] `make check`
- [x] `go test ./...`
- [x] `go test -race ./...`
- [x] `go vet ./...`
- [x] `go mod tidy -diff`
- [x] `go mod verify`
- [x] fixed `govulncheck ./...`
- [x] pinned frontend TypeScript/unit/build checks
- [x] pinned Rust format/tests and honestly reported RustSec warnings
- [x] `git diff --check`
- [x] final design checkpoint
- [x] clean candidate commit, frozen evidence bound to that commit, and clean
  tracked worktree

## Frozen candidate and evidence

- Implementation source: clean commit
  `c19cca4eb2842aa00d8e8fc17160b342a111f0b6`.
- Standalone clean checkout:
  `/private/tmp/vibermate-m10a.AtPYtn/source`.
- Packaged development-profile App:
  `/private/tmp/vibermate-m10a.AtPYtn/source/ui/desktop/src-tauri/target/release/bundle/macos/VibeMate.app`.
- App-bundle digest recorded by the acceptance provenance:
  `820dd17cd112f76554921d4e7d3c65a0a9fd5191328c1f50ba9950dbca61e349`.
- Packaged daemon digest:
  `441eef35dca15946c2e42a057824aa5d3402c69753e28eb5de62eb4e1682a59f`.
- Packaged launcher digest:
  `063b01d8fb89de6067723b80b3bac05a82784951658a19974c48bd3143860042`.
- Embedded build-manifest digest:
  `efc212717c32923e2e4454809763091aeb463eb7d15163164a3ccf4f5e557905`.
- Acceptance-runner digest:
  `3c2988f861a68704cc41b8b7528ffeb589ae93404f02d53b55bdafc6de60511c`.
- Deterministic report:
  `/private/tmp/vibermate-m10a.AtPYtn/deterministic-report-c19cca4.json`,
  SHA-256
  `e709f28e8b43b895462f6035e97911bd80751525da1e60fd03716e0cfc24f1c2`,
  mode `0600`, 17 of 17 checks passed.
- Credentialed report:
  `/private/tmp/vibermate-m10a.AtPYtn/credentialed-report-c19cca4-retry.json`,
  SHA-256
  `bb2123d96e117c1aac600777805354545ade785540c7ecc5d73563ba04c51a78`,
  mode `0600`, 25 of 25 checks passed using fixed Codex CLI 0.145.0,
  the existing development SecretRef, literal-loopback Cherry Studio, and
  `dashscope:glm-5`. No secret value appeared on the command line or in either
  report; a post-run sensitive-field/token scan was empty.
- Both reports bind the same clean source and App members, Go 1.25.12,
  Node 22.23.1, pnpm 10.33.2, and Rust/Cargo 1.88.0. The deterministic report
  exercises the revised authorized ClientHello/leaf path without provider
  traffic. The credentialed report also exercises Responses streaming,
  `exec resume`, the Codex `exec` approval barrier, Hold/Resume, signal
  cancellation, and bounded drain.
- A fresh all-package run after the first M1.0-A evidence draft exposed a
  timing-dependent test assumption: the timeout-retry test asked real ECDSA
  generation to finish inside the same intentionally tiny deadline used to
  force its first timeout. Commit `6d3b0ec7196f0d8e8fc71afc6c894e180cbe8ca6`
  changed only that test to retry through a pre-generated valid leaf, preserving
  the production deadline and failure semantics. Candidate `c19cca4` descends
  from that correction and adds only the DNS/IP typed-boundary fix and its
  regression tests. The focused certificate-identity and Access packages then
  passed 50 ordinary and 20 race repetitions, followed by full ordinary and
  race suites. All reports from earlier candidates are superseded by the
  selected reports above, which were rebuilt from `c19cca4`.
- One discarded credentialed invocation from the same candidate and artifact
  set passed normal streaming and `exec resume`, then timed out waiting three
  minutes for the Codex tool turn to finish after approval. Its private failed
  report is
  `/private/tmp/vibermate-m10a.AtPYtn/credentialed-report-c19cca4.json`,
  SHA-256
  `2b51a3d9a54573b13e6d9cf992ee40769ae3a81e1e76c75571ff99e7f3745ba9`.
  It is not selected evidence. A bounded retry using the exact same frozen
  source and artifact digests passed all 25 checks and is the selected report.
- Two discarded preflight invocations correctly stopped before Runtime or
  client execution: one rejected ambient Node 25.8.1, and one rejected a
  canonical `codex.js` path that omitted the frozen `codex` invocation label.
  The selected reports use the pinned toolchains and the actual compound
  installation entrypoint.
- Post-commit `make check`, `go test ./...`, and `go test -race ./...` passed
  from the tracked candidate. Vet, tidy drift, module verification, generated
  and structural checks, pinned frontend/Rust checks, and diff checks passed.
  Fixed `govulncheck` found zero reachable vulnerabilities. `cargo audit`
  exited successfully with the existing 17 allowed warnings: 16 unmaintained
  advisories and `RUSTSEC-2024-0429` for `glib`; this is not described as a
  warning-free audit.

This evidence proves only the fixed macOS arm64 development-profile vertical
and normal/injected lifecycle boundaries described above. It does not prove
physical power loss, system Root installation, arbitrary clients, successful
Responses WebSocket, TUI delta rendering, release SecretStore protection,
signing/notarization, Preview readiness, or Release readiness.

## Explicitly excluded

- Installing, removing, replacing, or rotating a macOS/system Root;
- `security`, Keychain Access, privileged helpers, or OS authorization prompts;
- Root Control API, Tauri commands, UI, trust-state message keys, or Host trust
  adapters;
- system/application proxy ownership, ConnectionPolicy expansion, unmatched
  blind tunneling, H1 wire shadow, H2, compression, or WebSocket work;
- release SecretStore, signing, notarization, installers, Server, or
  Windows/Linux delivery;
- OpenAI Chat Agent ingress, new providers/models/fixed clients, Language
  Bridge, plugins, or other protocol-width work;
- Root rotation or old/new Root migration semantics beyond preserving the
  existing Root during manifest schema migration.

## Completion statement

This goal is complete only when evidence supports:

> The installation's one persistent Root has a continuous immutable public
> identity and revision, and the production Proxy can obtain projection-owned
> admission to issue only bounded revision-authorized DNS leaves for an exact
> active AgentEndpoint. Stale Root/endpoint revisions fail closed, invalidated
> work cannot resurrect cache, private Root material remains confined, and the
> frozen M0.9/M0.9.1 vertical does not regress.

No system trust has been changed. VibeMate is still not Preview-ready or
Release-ready.

## Successor order and product cadence

1. M1.0-B Desktop Trust Operation Foundation: typed change plan, macOS adapter,
   and deterministic executor tests without ordinary system mutation.
2. M1.0-C Desktop Trust Control and UX: Desktop-only API, Host composition,
   synchronized locale UI, cancellation, denied-permission, and manual-recovery
   states.
3. M1.0-D Real macOS Trust Acceptance: only after separate explicit user
   authorization, install the exact disposable test Root, prove a real TLS
   handshake, remove it, and verify no residual trust.
4. Immediately after M1.0-D, run the complete real macOS user task: understand
   Root risk, install/verify, run the fixed Agent, Hold/Resume, remove Root, and
   verify cleanup. Do not insert a new protocol/provider before this task.
5. Before a bounded external macOS arm64 `vibermate run` Preview, separately
   close release SecretStore, signing/notarization, recovery, and verifiable
   uninstall. M1.1-M1.3 breadth follows stable user evidence.
