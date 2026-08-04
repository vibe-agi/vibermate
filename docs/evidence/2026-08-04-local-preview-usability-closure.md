# Local Preview Usability Closure Evidence

Date: 2026-08-04

## Frozen candidate

- Source revision: `f26db1195653afedcbdb0071a639ea33690fc34c`
- Source state: clean
- Platform: macOS arm64
- Desktop profile: development
- Fixed client: Claude Code 2.1.220
- Report schema: `vibermate.m0-assembly-acceptance/v6`
- Result: 18 of 18 deterministic checks passed

The private report remains outside Git at:

`/private/tmp/vibermate-local-preview-f26db11.sMGYwg/deterministic-report.json`

Its mode is `0600`, its size is 8,774 bytes, and its SHA-256 digest is:

`47f66a5b0832616d9f8c265d465f9895d2a76fd348b2974ba8ddd13b68ab7898`

The independently supplied acceptance executable has SHA-256:

`3411809d19ca2884b3b7badaa8051f24a1cb440215851fe8acafee9d5eef2b6d`

The v6 verifier re-read the clean checkout and rehashed the selected App,
daemon, launcher, acceptance executable, client entrypoint, build manifest, and
frozen configuration. Verification passed for the exact revision and client
above.

## Discovery attempts

Two pre-assembly invocations failed closed before the successful run:

1. The ambient Node v25 environment was rejected because the App was built
   with the pinned Node v22.23.1 toolchain.
2. Passing the exact 2.1.220 binary by its version filename was rejected because
   the fixed release requires the invocation label `claude`.

The successful invocation reused the same source, App, acceptance executable,
and client bytes. It used the pinned Node environment and a private `claude`
symbolic entrypoint whose dereferenced SHA-256 matched the catalogued release.
Neither rejection caused provider traffic or weakened the identity contract.

## Additional local proof

- The packaged terminal command traversed `not_installed`, `current`,
  `source_updated`, refresh, and `current` using its receipt-owned link at
  `~/.local/bin/vibermate`.
- The refreshed command completed `vibermate run -- /bin/echo` against the
  running packaged Desktop generation.
- A separate captured-process integration test drove a real child request
  through CONNECT/SNI admission, Access snapshot resolution, codec conversion,
  a local provider fixture, streaming response, and durable connection, egress,
  and activity evidence.
- A black-box local TLS/HTTP capture service observed the production
  connector's actual outbound ClientHello and request over TCP. It verified
  retained supported cipher-suite and extension ordering plus required changes
  to SNI, ALPN, random, and key-share state.

## Boundaries

This evidence does not prove credentialed provider success, arbitrary client
versions, Keychain protection, system Root installation, signed or notarized
distribution, LAN Server mode, application-wide capture, exact JA3/JA4 or
HTTP/2 fingerprint parity, plugins, or general Preview/Release readiness.
