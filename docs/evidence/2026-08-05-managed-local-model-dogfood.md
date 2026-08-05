# Managed Local Model Dogfood Evidence

Date: 2026-08-05
Implementation candidate: `b938bcf1845f802074a8becbb15516a1c9a89df5`
Source state during final gates: clean
Platform: macOS 26.5.2 (25F84), arm64
Go toolchain: 1.25.12
Desktop profile: development

## Live observations

The source-level production composition was exercised against a
user-supplied literal-loopback OpenAI-compatible service. Its origin and
credential are intentionally absent from repository artifacts and this
evidence. The development file SecretStore held the credential through the
ordinary typed SecretRef path.

The following installed clients and explicit managed routes completed:

- Claude Code 2.1.221 to GLM-5: terminal output `ready`;
- Codex CLI 0.146.0 to GLM-5: terminal output `ready`, with client-reported
  token usage; and
- Claude Code 2.1.221 to Kimi K3: terminal output `ready`.

Each client crossed the normal launcher, explicit Root approval,
authenticated loopback proxy, exact endpoint MITM, typed codec, managed
credential, provider transport, streaming response, and terminal client path.
No product-specific production branch was added for either model.

These live observations were made from the working tree subsequently frozen
as the candidate above. They are source-level dogfood observations, not a
packaged acceptance report or a clean-source provenance artifact.

## Fail-closed discovery

The first Codex attempt did not start the client. The test changed its
AgentEndpoint from the Anthropic origin and dialect to the Responses origin and
dialect after constructing the Access, leaving the Core-derived
`original_passthrough` Profile stale. The production compiler rejected the
candidate Access because the original Profile no longer matched the exact
AgentEndpoint.

The correction was limited to the test construction: after changing the
endpoint, it calls the public Core derivation function and then writes the
Access. Production validation was not weakened. The corrected Codex run
completed, and the whole desktop-host package passed.

## Product and UI correction

The current-login route remains the recommended default and upstream wire
presentation remains `follow-client`. H1/H2 follows the negotiated client
protocol and is not a user setting. Named product presentation remains an
explicit advanced choice.

A role-based browser review found that the current-login review copy promised
that every request stayed unchanged, while unsupported operation types are
intentionally stopped locally. English and Simplified Chinese now promise only
that supported model requests preserve their shape and streaming behavior, and
state that unsupported request types stop locally. A Playwright assertion
freezes that boundary.

## Final gates

- `make check`, including generated-file drift, repository structure,
  immutable workflow pins, native-secret builds, Windows/Linux cross-builds,
  React/TypeScript, Playwright 24/24, and Rust/Tauri checks;
- uncached `go test -count=1 ./...`;
- uncached `go test -race -count=1 ./...`;
- the fixed Codex SIGINT integration test for 20 ordinary and 10 race runs;
- fixed `govulncheck@v1.6.0`: zero reachable vulnerabilities;
- production UI audit: no known vulnerability;
- Rust audit: the existing 16 allowed unmaintained warnings, with
  RUSTSEC-2024-0429 excluded only after the repository's reachability guard;
- forbidden private-project term and private-origin scan: no match.

The development UI check ran with Node 25.8.1 and pnpm 10.33.2. It passed but
reported that the repository's release Node version is 22.23.1; this run is not
evidence of the pinned release toolchain.

## What this evidence does not prove

- a packaged, signed, notarized, or installed App run at this candidate;
- a non-loopback provider, remote TLS, or production credential protection;
- raw ClientHello passthrough, exact JA3/JA4 parity, or exact H2 SETTINGS and
  header ordering;
- arbitrary client versions, Responses WebSocket semantics, interactive TUI
  delta rendering, tools, compaction, or long multi-turn sessions;
- forwarding payload-bearing auxiliary operations such as `count_tokens`;
- Keychain or system trust mutation, Server/LAN mode, plugins, Language Bridge,
  Preview, or Release readiness.
