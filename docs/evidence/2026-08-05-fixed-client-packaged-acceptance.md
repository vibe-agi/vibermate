# Fixed-Client Packaged Acceptance Evidence

Date: 2026-08-05

## Frozen candidate

- Source revision: `bbe3d2d35574481e2c10a57e28ca456ef51780b5`
- Source state: clean
- Platform and architecture: macOS arm64
- Desktop and sidecar profiles: release
- Report schema: `vibermate.m0-assembly-acceptance/v6`
- Claude Code 2.1.220: 18 of 18 deterministic checks passed
- Codex CLI 0.145.0: 19 of 19 deterministic checks passed

The reports remain outside Git in a private `0700` directory. Both report
files have mode `0600`:

| Client | Bytes | Report SHA-256 |
| --- | ---: | --- |
| Claude Code 2.1.220 | 8,815 | `33df67b2ed1a872b7f8521c1888cf5d5c029ef22e3d5bbfe3234821656ef47ac` |
| Codex CLI 0.145.0 | 9,108 | `8cd3d379eba0b051a32f4557b0238632fbd4981411ed88f0e9aa32f76a15fd3a` |

The independently built acceptance executable has SHA-256
`4a12c2ee224986c213d422a33f8c407a1dd8280674a4f6aa4b471e9ae2b590d0`.
The independently built report verifier has SHA-256
`0c1ec585848ab06336bc6c7fea099711bda9ef8d3cd2dc5efa4dc2a6af733f51`.
The verifier re-read the clean source checkout, App, embedded build manifest,
both sidecars, acceptance executable, fixed client, and frozen configuration.
It accepted each report only for the exact revision above.

The reports bind these shared release artifacts:

| Artifact | Bytes | SHA-256 |
| --- | ---: | --- |
| App bundle | 47,162,530 | `e232254047a06520863bb7be3a396a6588b3cb9c9e06c79337fd340b0218ad0f` |
| Desktop executable | 13,114,080 | `2ea1f1558afcd0b1e2fd0603bf3306c392922a80698262b5efc87debcf383690` |
| daemon | 24,283,010 | `7a8fe0b5be4cb4bc6d9c32f850cb28ea2cd002c70259b87723fb465d73d753dc` |
| launcher | 9,691,426 | `21e460da41c4b7d22a3cf51df23c4f24019311bb9e428cd0de81959323558435` |
| embedded build manifest | 1,741 | `27a5708fca54ac848ff9197b4822ccf863739fa93f70a9dfa9c2df3115ba1877` |

The fixed client entrypoints are bound separately:

| Client | Bytes | SHA-256 |
| --- | ---: | --- |
| Claude Code 2.1.220 | 256,908,272 | `8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081` |
| Codex CLI 0.145.0 | 7,236 | `134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477` |

The embedded build provenance records Go 1.25.12, Node 22.23.1, Rust/Cargo
1.88.0, pnpm 10.33.2, and Tauri CLI 2.11.4.

## Claude fallback ownership correction

The first strict current-client discovery run observed two terminal Exchanges
where the acceptance contract required one. This was not a Core retry. Offline
Hold had correctly committed an SSE response before provider execution, and
the fixed Claude client independently retried the same operation through its
non-streaming fallback after receiving the in-band missing-credential failure.

A direct client fixture established the fixed release's documented disable
switch. The correction gives an exact verified adapter a typed
`core_owned` streaming-fallback policy. The launcher then sets that client
switch so Core remains the sole retry and fallback authority. Recognized and
generic clients retain `client_default`; the launcher neither guesses nor
exports the switch for them. The acceptance count was not relaxed. The clean
release run then produced exactly one terminal Exchange and passed all 18
checks.

## Codex attribution correction

The first current Codex run reached the expected WebSocket 426 to HTTP
fallback path, but the acceptance guard still expected the older
`configured` source-confidence value. Production correctly attributed the
exact digest-bound client as `verified`. The guard was narrowed to require
`verified`, with a negative test proving that `configured` no longer passes.
The rebuilt clean release then passed all 19 Codex checks.

The Codex checks prove the client surfaced HTTP 426, the proxy observed the
bounded WebSocket-to-HTTP negotiation, and the authorized HTTP Exchange
reached the deterministic missing-credential terminal. They do not claim a
successful WebSocket data plane.

## Boundaries

Both runs use a unique missing provider `SecretRef`. They prove release-bundle
assembly, fixed-client launch, authenticated proxy admission, exact endpoint
MITM, dialect routing, deterministic failure, audit correlation, Hold,
shutdown, and SQLite reopen for the exercised paths.

They do not prove:

- a real provider credential or successful model output;
- interactive TUI token rendering;
- arbitrary, recognized, or generic client releases;
- successful Responses WebSocket semantics;
- system Root trust, Keychain storage, LAN Server mode, or plugins;
- original-passthrough behavior;
- exact JA3/JA4, raw ClientHello, or HTTP/2 SETTINGS/header-order parity; or
- Preview or Release readiness.

The deterministic failure occurs before provider transport performs an
upstream TLS dial. These reports therefore do not exercise the default
`follow-client` upstream presentation against a real TLS observer. That needs
separate black-box transport evidence.
