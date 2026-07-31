# M1.0-A Revision-Authorized Root and Leaf Authority

Status: active
Created: 2026-08-01
Implementation baseline: `cf3f599e11ee13f82fef0e8a6b8c09e38878b124`

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
   slice authorizes canonical DNS only. IP-literal admission remains
   `unsupported` until a separate accepted design decision permits it.
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
- [ ] Re-run deterministic and credentialed M0.9/M0.9.1 packaged acceptance on
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
- [ ] final design checkpoint
- [ ] clean candidate commit, frozen evidence bound to that commit, and clean
  tracked worktree

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
