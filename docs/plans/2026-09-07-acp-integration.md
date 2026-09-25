# ACP integration: usable observation milestone

Date: 2026-09-07. Branch: `feat/acp-integration`. Published v0.1.9 source
`3420aed6a6eb97d67b31bae64752419d6c362280` is merged at
`e854571e8c68d32a61ea2123ec0a6e14b2aece22`. The released main worktree and older
`feat/acp-stdio` prototype are not changed by this implementation.

## Shipped in the development build

The public command is `vibermate acp [--server URL] [--record-content] -- <agent>
[args...]`. An ACP editor starts ViberMate, and ViberMate starts the existing
adapter. Use the [setup guide](../acp-quickstart.md), or Settings -> Access &
launch -> Connect an ACP editor, to generate Zed/JetBrains configuration.

This milestone is **ACP observation only**, not HTTP interception/enforcement.
`--env` is rejected. No proxy, CA, provider credential, HTTP account overwrite,
model mapping, script, network rule, or launch-environment overlay is injected.
The editor retains auth, permissions, filesystem/terminal requests and effective
env/argv/cwd. No alias, shell, automatic adapter download, or temporary user is
created. The existing `vibermate run` HTTP path remains separate.

## Research and reuse

- [Ecosystem survey](../research/2026-09-07-acp-ecosystem.md): 24 repositories,
  primary specifications, release activity, license, CI/test and advisory checks.
- [Bridge patterns](../research/2026-09-07-acp-bridge-patterns.md): pinned source
  and tests, including official Rust SDK proxy/conductor lifecycle patterns.
- [Cursor integration](../research/2026-09-07-cursor-acp-integration.md): Cursor
  CLI `agent acp` is an Agent, not evidence that Cursor desktop hosts arbitrary
  external ACP Agents. Initial config targets use documented Zed/JetBrains flows.
- [Published-adapter acceptance](../research/2026-09-07-acp-acceptance.md):
  Codex ACP 1.10.0 (isolated Codex 0.153.3) and Claude ACP 0.75.1, Node 22.23.2.

Reuse the active `agentclientprotocol/codex-acp` and
`agentclientprotocol/claude-agent-acp` adapters; do not duplicate their protocol,
tool, model, history or login implementations. The Go bridge remains an opaque
byte relay, not a typed decode/re-encode protocol endpoint. Stable wire v1 and
SDK release numbers are distinct. Unknown extensions still pass unchanged.

## Module boundaries

| Module | Owned behavior |
| --- | --- |
| `internal/acpbridge` | Fixed-buffer duplex relay, half-close/drain, backpressure, complete byte preservation and payload-free failures. |
| `internal/runlauncher` | Exact effective invocation, owned cancellable duplicate stdio, separate unretained stderr, PID attachment, signals, bounded group cleanup/reaping, heartbeat and final snapshot/finish. Real TTY invocations bypass registration and observation for terminal auth. |
| `internal/acpobservation` | Bounded projection of confirmed native sessions, fresh prompts, visible text opt-in, tool notification counts, auth-required status and self-reported version. Owns immutable recording authority, not transport or HTTP usage. |
| `internal/runtimepersistence` | Additive digest-checked ACP extension, live-parent and Runtime User revocation checks, monotonic/idempotent final snapshot storage, retention purge on ACP operations and cascade on Capture deletion. |
| `internal/capturecontrol` | Live run-capability scoped start/observe actions; malformed/oversized publication is rejected without private diagnostics. |
| `internal/desktopcontrol` | Owner-only ACP read and batch connection markers in Capture listings. No prompt content is loaded to label a list. Shared by App and Server Web. |
| `ui/flutter_app` | ACP-only Capture display and units, session selection, version provenance, auth/recording/completeness notices, optional text and bilingual editor setup. No fake HTTP turn counts or policy picker on ACP. |

See [ADR 0010](../adr/0010-observe-acp-without-inferring-http-authority.md) and
[domain language](../../CONTEXT.md). A session directory is a claim, never file
authority. Native IDs are connection-scoped and never globally merged. History
replay is not a fresh prompt; opposite-direction request IDs are independent.
“Agent returned” means the RPC returned, not that upstream model execution
succeeded. Auth/permission payloads, thought chunks, raw errors and stderr never
enter snapshots. Metadata-only is the default; content opt-in still obeys the
exact frozen launch revision. Observation limits never truncate protocol traffic.

Limits per connection: 1 MiB observed frame, 64 sessions, 256 prompts, 128 KiB
visible text and 768 KiB encoded snapshot. Escaping counts toward the encoded
budget. Limit overflow is explicitly incomplete. Expired snapshots are purged on
subsequent ACP reads/writes/list operations; the small connection marker remains.
Deleting a Capture removes its observation. No idle periodic purge is claimed.

## Verification evidence

- Race-enabled relay/process fixtures: CRLF, fragmented and non-UTF-8 bytes,
  frames above 10 MiB, reverse permissions, same-valued IDs, unknown extensions,
  EOF/final drain, blocked GUI stdin, large stderr, delayed nonzero exit,
  cancellation, TERM/KILL escalation and descendants after wrapper exit.
- Actual macOS pseudo-TTY fixture preserves terminal auth and appended args.
  A signal during PID attachment is retained and forwarded; failed attachment
  kills and reaps the child before returning.
- Observer fixtures: multiple sessions, load replay, error sanitization,
  auth-required boundaries, metadata-only/full, UTF-8 and JSON expansion limits,
  interrupted requests, immutable snapshots and invalid durable revisions.
- Real DesktopHost/SQLite/HTTP/child end-to-end flow: register, attach,
  initialize, session/new, prompt, editor permission round trip, text response,
  EOF, durable final record and finished Capture. Both content modes tested.
- Real remote Host path over HTTP and pinned HTTPS: saved Runtime User login,
  device/workspace attribution, unchanged Agent env, owner-only Web reads.
- Authority/storage: wrong and cross-run capabilities, proxy vs control scope,
  pre-attach non-mutating authorization, content-upgrade rejection, final/stale
  revision rejection, logout/disabled-user fencing, v0.1.9 baseline upgrade,
  reopen/expiry/delete cascade, unfamiliar extension rejection without reset.
- Published adapters through the actual registered launcher in a network-disabled
  Linux ARM64 container: initialize, Codex unauthenticated auth-required boundary,
  Claude native session, EOF/exit and persisted final version/session evidence.
  No provider request, real credential or editor settings were used.
- Flutter widget tests: English/Chinese, 390/1100 px, session switching,
  metadata-only display and exact Zed/JetBrains/remote/Cursor CLI config arrays.
- Packaged CLI -> actual native-secret daemon -> Dart API -> WorkbenchController
  passed with content both off and on. All seven packaged Dart live tests passed
  in serial and parallel reruns; the actual packaged App launch/relaunch gate
  also passed. `check-flutter-macos` includes the ACP tests and runs packaged
  suites sequentially. One earlier run concurrent with other native acceptance
  returned two `secret_store_unavailable` startup failures; they did not recur in
  the standalone reruns. This is retained as a native startup reliability
  observation, not represented as a diagnosed or fixed Keychain defect.

Repository gates: `go test ./...`, `go test -race ./...`, `go vet ./...`,
`make check-format check-dependencies check-structural`, `flutter analyze`,
`flutter test`, Linux/Windows cross-builds, packaged macOS/Web build and the
opt-in live tests. Build outputs remain in this worktree's `dist`; no Homebrew,
GitHub release or mainline mutation is part of this milestone. The final full Go
race suite and Flutter analyzer passed; 374 ordinary Flutter tests passed, while
the seven packaged tests require their explicit opt-in paths and are verified
separately rather than silently counted as ordinary test coverage.

## Remaining acceptance, not implied by these tests

Actual GUI builds plus an explicitly authorized provider account are required
to certify editor login, live prompts/tools/images, rejection/cancel/resume,
multi-session interaction, and per-editor terminal-auth variants. Some legacy
auth descriptors name a separate executable; the wrapper preserves them rather
than claiming to capture that independent process. Cursor desktop external-agent
hosting and HTTP proxy/CA/account substitution require their own evidence.
These are not certified by protocol fixtures or isolated adapter initialization.
