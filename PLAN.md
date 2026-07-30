# M0.8 Assembly Acceptance Convergence Plan

Status: active
Created: 2026-07-30
Implementation baseline: `8fb4401a16bb1866d0f2c1cbafcf82ec75827515`

## Objective

Close the first production-assembly evidence loop for the macOS arm64 M0
slice. A single clean App bundle must carry the launcher and daemon built from
the same Git revision, start the sole `ProductRuntime`, run fixed Claude Code
2.1.220 through the authenticated loopback proxy, and produce a private,
auditable v3 acceptance report.

This is an implementation and evidence plan, not a Preview or Release plan.

## Design authority

The read-only design repository remains authoritative:

- `docs/design/18-production-composition.md` sections 7 and 8;
- `docs/design/17-validation-roadmap.md` sections 2, 5, and 6;
- `docs/design/02-architecture.md` Access and Exchange ownership;
- `docs/design/10-client-compatibility.md` sections 5 and 6;
- `docs/design/20-transparent-hold-and-resume.md`;
- `docs/adr/0006-agent-endpoint-mitm-allowlist.md`;
- `docs/adr/0012-planned-offline-core-egress-gate.md`.

The design repository is never modified by this plan.

## Required invariants

1. `productruntime.Start` remains the only business composition root.
2. SQLite is the only durable Access authority. Data-plane code obtains one
   immutable active plan through `SnapshotResolver` and never uses a writer,
   repository, or `WriteResult` snapshot.
3. Every external egress requires the runtime-owned Offline Hold action and
   egress leases. Enter Hold and Exchange admission share the established
   atomic cut.
4. A held provider target remains bound to Access revision, `PlanHash`, origin,
   authority, transport kind, and SNI when applicable.
5. Remote provider targets remain strict HTTPS with system roots and the
   frozen transport-fingerprint plan.
6. Cleartext provider traffic is accepted only for an explicitly configured
   literal loopback IP, Direct egress, no ambient proxy, and exact post-dial
   TCP peer verification before authenticated HTTP bytes are written.
7. Secret values enter only the explicit development `SecretStore`. They never
   enter SQLite, Access plans, command lines, reports, logs, or source files.
8. Each real Exchange resolves exactly one active plan and revalidates the
   frozen AgentEndpoint evidence.
9. Tool output and terminal stream events remain behind the durable approval
   barrier.
10. Product readiness is Host-owned and published only after the runtime,
    listeners, routes, and discovery record are complete.
11. Shutdown and failure evidence must remain honest. A failed drain is not
    reported as stopped, and a component test is not described as packaged
    assembly evidence.

## Development method

All behavior changes follow a test-driven loop:

1. add or tighten a deterministic failing unit, contract, integration, or
   acceptance assertion;
2. implement the smallest production-path change that satisfies the design
   invariant;
3. run the focused test and race test;
4. run repository structural checks;
5. rerun the affected vertical scenario;
6. commit only a coherent reviewed slice.

No acceptance assertion may be weakened to accommodate a provider, Agent, or
timing failure. Safe diagnostic additions use closed reason enums and never
capture provider text, prompts, tool arguments, headers, or credentials.

## Bottom-up execution order

### 1. Freeze the local provider transport boundary

- [x] Add typed strict-TLS versus literal-loopback-cleartext target identity.
- [x] Reject remote HTTP, `localhost`, LAN, private-CIDR, and mapped-address
  cleartext origins.
- [x] Keep remote uTLS/fingerprint behavior unchanged.
- [x] Add a separate no-proxy loopback transport with exact peer verification
  before HTTP write.
- [x] Extend Offline Hold probe identity and transport-appropriate,
  no-credential probing.
- [x] Cover valid loopback, invalid origins, changed peers, zero-write failure,
  compiler, control input, client, probe, structural-good, unit, and race
  paths.
- [x] Freeze the change as commit `8fb4401`.

### 2. Establish one frozen packaged artifact

- [ ] Build the App bundle from a standalone clean checkout at the final
  plan-bearing commit.
- [ ] Derive packaged `vibermate`, packaged `vibermated`, and the acceptance
  runner from that same revision.
- [ ] Verify the embedded manifest, sidecar digests, clean Git identity, pinned
  Go/Node/Rust toolchains, and development build profile through the runner.
- [ ] Run deterministic acceptance and retain a mode-`0600` report.

### 3. Bind the development credential without weakening secret boundaries

- [ ] Store the Cherry Studio development key through the write-only App
  control path under an existing logical `SecretRef`.
- [ ] Verify only configured state and a nonzero secret revision; do not read
  or print the value.
- [ ] Query the local authenticated model catalog through a bounded,
  redaction-safe path and confirm the exact `glm-5` and `kimi-k3` identifiers.
- [ ] Keep native Keychain work outside this M0 development profile.

### 4. Run the credentialed vertical chain

- [ ] Apply the complete Access route to
  `http://127.0.0.1:23333/v1` using a confirmed fixed model.
- [ ] Prove a normal response with a trusted marker and incremental deltas.
- [ ] Prove a real tool intent becomes durable pending approval, remains
  hidden before `allow-once`, and completes only after approval.
- [ ] Prove a request admitted after Enter Hold sends zero provider bytes
  before Resume, probes the exact frozen loopback peer, and resumes streaming.
- [ ] Prove Agent `SIGINT` after the first delta converges without taking down
  the shared runtime.
- [ ] Prove daemon `SIGKILL` releases generation ownership and that the next
  generation recovers the committed Access revision from SQLite.
- [ ] Retain one mode-`0600` credentialed v3 report from the frozen artifact.

### 5. Correct failures only at their owning layer

- [ ] Protocol failures receive protocol fixtures and codec tests.
- [ ] Provider-envelope failures receive sanitized wire-shape tests.
- [ ] Hold ordering failures receive deterministic admission/probe tests.
- [ ] Proxy or ingress failures receive CONNECT/MITM and endpoint-revalidation
  tests.
- [ ] Host or recovery failures receive packaged lifecycle/provenance tests.
- [ ] Provider-specific instability is recorded as external evidence and does
  not weaken core contracts.

### 6. Final gates and evidence audit

- [ ] `make check`
- [ ] `go test ./...`
- [ ] `go test -race ./...`
- [ ] `go vet ./...`
- [ ] `go mod tidy -diff`
- [ ] `go mod verify`
- [ ] `govulncheck ./...`
- [ ] pinned frontend TypeScript/unit/build checks
- [ ] pinned Rust format/tests and an honestly reported RustSec warning set
- [ ] `git diff --check`
- [ ] clean worktree after the final commit
- [ ] report provenance matches the final clean commit and exact App bundle

## Completion statement

This plan is complete only when current evidence supports the following
statement:

> One fixed Claude Code 2.1.220 build can traverse the clean packaged macOS
> arm64 VibeMate M0 assembly through the sole ProductRuntime and a complete
> immutable Access plan, use controlled provider egress, stream a normal
> response, cross the durable tool-approval barrier, enter and resume planned
> hold, converge on SIGINT, and recover committed SQLite state after daemon
> termination. The evidence is bound to one clean build and contains no secret
> or semantic payload.

Even then, VibeMate is not Preview-ready or Release-ready. Physical
sleep/network removal, native secret protection, signing/notarization,
installer lifecycle, Server, Windows/Linux, additional clients/codecs,
multi-profile routing, and full product UI remain outside this plan.

## Archive and successor protocol

After every checkbox and the completion statement are proven:

1. move this file without rewriting its history to
   `docs/plans/archive/2026-07-30-m0.8-assembly-acceptance.md`;
2. add final report paths, hashes, and the completing Git revision to the
   archived copy;
3. create a new root `PLAN.md` from the then-current implementation and
   read-only design;
4. make the successor plan bottom-up and TDD-driven, with UI work after its
   storage, contracts, runtime, and data-plane dependencies;
5. create the corresponding Codex Goal only after the successor plan exists.
