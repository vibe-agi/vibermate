# M1.0-C0l Release Path and the Last Translation

Status: complete
Created: 2026-08-02
Implementation baseline: `cb83ae8`
Branch: `m1/root-leaf-foundation`
Predecessor: `docs/plans/archive/2026-08-02-m1.0-c0k-a-failure-you-can-diagnose.md`
Defers: `docs/plans/deferred/2026-08-01-m1.0-c-macos-trust-observation.md`,
`docs/plans/deferred/2026-08-02-unrecognized-client-is-silent.md`

## What this slice closed

- The OpenAI Responses client dialect had no live evidence behind it. It has
  one now: a real model answers a Responses request through the whole
  Exchange, and the answer comes back as a Responses object rather than a
  chat completion relabelled.
- There was no release build. `-tags vibermate_native_secrets` selected a
  factory no file provided, so the packaged acceptance harness, which
  requires that tag, could never have run. The macOS Keychain backend exists
  now, and `make check` builds and vets under the tag so it cannot go missing
  again unnoticed.

## What is still open

- The client catalog carries evidence for one release of each client, and a
  newer install is launched without a trust root. The window and the terminal
  both say so; the catalog itself needs verified release material.
- Windows and Linux release backends refuse rather than degrade, which is
  what design 06 asks for and is not the same as supporting them.
- A stream abandoned by its client is now covered through the proxy: it is
  recorded as canceled, its outbound reaches a terminal, and the runtime still
  drains.
- Failover is not merely unverified, it is unimplemented. `RouteSet` carries
  candidates and the pipeline selects one frozen plan with no attempt loop, so
  there is no second attempt to test. What is pinned is the rule that has to
  survive the day there is one: once bytes have reached the client the answer
  is committed and must not be sent again, which design 02 §12 calls
  `pre_first_byte_idempotent_only`.
- The Responses WebSocket path is refused with a stable reason rather than
  attempted, which is a recorded state rather than a gap in coverage.
