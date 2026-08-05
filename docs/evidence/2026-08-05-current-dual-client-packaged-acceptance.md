# Current Dual-Client Packaged Acceptance Evidence

Date: 2026-08-05

## Frozen candidate

- Source revision: `112600f822794d226bf96ff296c3334c26f5d7b1`
- Source state: clean
- Platform and architecture: macOS 26.5.2 (25F84), arm64
- Desktop and sidecar profiles: release
- Report schema: `vibermate.m0-assembly-acceptance/v6`
- Claude Code 2.1.220: 18 of 18 deterministic checks passed
- Codex CLI 0.145.0: 19 of 19 deterministic checks passed

The private reports remain outside Git in a `0700` directory and have mode
`0600`:

| Client | Bytes | Report SHA-256 |
| --- | ---: | --- |
| Claude Code 2.1.220 | 8,837 | `9d9e841fba3d90c07bce1245d55db3932ad9f82cdb4281625458e1f39817a7a8` |
| Codex CLI 0.145.0 | 9,163 | `77df4cc62f8ebefa2a7b6522bd3a083113d81dbc7ff5868ef9283999f48f9faf` |

The independently built acceptance executable has SHA-256
`0ac6438d22fbe85b941943b9418d275d0a6ee8f0b5aafa9d7c5c7067747ab558`.
The independently built report verifier has SHA-256
`e3b0028d97000631062080e3abf947ebec888879aa9c195fef6825d03e4c717a`.

Both reports bind the same release-profile App:

| Artifact | Bytes | SHA-256 |
| --- | ---: | --- |
| App bundle | 47,247,714 | `13f0a595c08c14b2741fdda74ec4c24cb5326600b243bda9f00271d3ac293ce8` |
| Desktop executable | 13,114,080 | `d051899f9485e6082b8c34603da0356db0f340809ae2336cfe2cf1c69150f4cc` |
| daemon | 24,368,194 | `67ed6cb70cc8c6cc55b2a06b0c55dc96fce243367ce7632aae5f959aadc39fd2` |
| launcher | 9,691,426 | `f2a317b3c65640c8efce18d990c1ea2c1ef53780f948bc365c4c6d25a40bb9a0` |
| embedded build manifest | 1,741 | `7b723ba74278cdf6968a4b3686778f95faf9f0831b24e71f779522f153649aff` |

The fixed client entrypoints remain independently bound to the catalogued
compound releases:

| Client entrypoint | SHA-256 |
| --- | --- |
| Claude Code 2.1.220 native executable | `8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081` |
| Codex CLI 0.145.0 wrapper | `134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477` |

The Codex verifier also rehashed its main package metadata, platform package
metadata, and native child. Their placement and all four digests matched the
frozen nested package shape; a default npm hoisted layout did not satisfy those
frozen artifact coordinates.

The build and acceptance host both report Go 1.25.12, Node 22.23.1, Rust and
Cargo 1.88.0, pnpm 10.33.2, and Tauri CLI 2.11.4.

## Discovery and correction

Two failed attempts against the preceding clean source were retained outside
Git rather than overwritten:

1. The interactive shell exposed Node 25.8.1. Provenance failed before client
   traffic because the frozen host contract requires Node 22.23.1. The same
   source and artifacts passed that boundary under the pinned Node toolchain.
2. The exact Claude bytes were supplied through a lexical path named
   `2.1.220`. Release selection correctly refused to infer the `claude` label
   from the resolved target. The runner previously reported only a late generic
   release mismatch.

The candidate above moves the second failure to configuration parsing. A fixed
client invocation path must be lexically named `claude` or `codex`; its symlink
target and complete release artifacts are still resolved and hashed
independently. Tests prove that a version-file path is rejected and a correctly
labelled wrapper path preserves the verified target.

## Exercised packaged behavior

Both fixed clients crossed the actual App bundle, packaged daemon and launcher,
authenticated proxy, exact AgentEndpoint MITM, explicit connection policy,
Offline Hold, durable ConnectionEvent, SQLite reopen, graceful shutdown, and
forced daemon termination. The reports use a unique missing SecretRef, so the
semantic request fails at `provider_credential_unavailable` before provider
HTTP traffic.

The Codex report additionally proves that the fixed client surfaced HTTP 426,
the proxy recorded its bounded WebSocket-to-HTTP transition, and the later HTTP
Exchange reached the missing-credential terminal. It does not claim successful
Responses WebSocket semantics.

The operator-controlled probe target was a reachable literal-loopback service.
Its coordinates and any credential are intentionally absent from repository
artifacts. Deterministic mode performs no credentialed model request.

## Role-based UI review

The browser preview was exercised at 1440 by 1000 as three users:

- an existing Claude user comparing the current-login and managed routes;
- a Codex user adding an explicit OpenAI-compatible route; and
- a user operating three terminals grouped under one stable machine/workspace.

The current-login path remains the recommended default, managed routing remains
an explicit choice, and the workspace route switch explains the stop-select-
restart boundary across credential sources. The browser preview intentionally
cannot mutate the terminal command. Inspection of the real Desktop branch
confirmed that the same launch panel exposes in-place install, refresh, and
copy actions; Settings is only the recovery path for conflicts. No UI change
was needed from this review.

## Final gates

- `make check`, including generated-file drift, repository structure,
  immutable workflow pins, native-secret builds, Windows/Linux cross-builds,
  React/TypeScript, Playwright 24/24, and Rust/Tauri checks;
- uncached `go test -count=1 ./...`;
- uncached `go test -race -count=1 ./...`;
- fixed `govulncheck@v1.6.0`: zero reachable vulnerabilities;
- production UI audit: no known vulnerability; and
- Rust audit: the existing 16 allowed unmaintained warnings, with
  RUSTSEC-2024-0429 excluded only after the repository's reachability guard.

## What this evidence does not prove

- a credentialed packaged request or successful model output at this revision;
- a signed, notarized, installed, or Gatekeeper-approved distribution;
- arbitrary, recognized, or generic client releases;
- interactive TUI token rendering, successful Responses WebSocket semantics,
  tools, compaction, or long multi-turn sessions;
- raw ClientHello passthrough, exact JA3/JA4 parity, or exact H2 SETTINGS and
  header ordering;
- original-passthrough packaged traffic, payload-bearing auxiliary forwarding,
  system Root trust, Keychain behavior, Server/LAN mode, plugins, Language
  Bridge, Preview, or Release readiness.
