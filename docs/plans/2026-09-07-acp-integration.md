# ACP integration: research and first implementation slice

Date: 2026-09-07. Branch: `feat/acp-integration`, based on published `v0.1.8`
(`c41bc857143cba8287ce15ffd7d2e0cbb6bb05b0`).

Status: ecosystem research and the opaque transport foundation are implemented.
**There is no public `vibermate acp` command, ACP server persistence, or ACP App/Web
view in this slice.** The foundation is not yet composed into the launcher. This
document is an implementation plan, not a compatibility or release announcement.

The existing uncommitted `feat/acp-stdio` worktree remains untouched. Its custom
observer and ACP flags are research input only; they have not been merged into
this branch or the released HTTP capture path.

## Product direction

An ACP-capable editor starts ViberMate; ViberMate starts the user's existing ACP
agent and relays its stdio protocol. The editor keeps its native chat, files,
terminal, permission prompts, and agent login. ViberMate adds runtime attribution,
optional evidence, and explicitly supported traffic policy without becoming
another Codex/Claude adapter.

Do not create a temporary Runtime User for each editor process or workspace.
Reuse the existing authenticated local App or remote Runtime User login. Provider
authentication remains between the ACP client and agent, distinct from ViberMate
management access. Never prompt for a ViberMate password on ACP protocol stdin.

The wrapper can be launched from any directory. Its launch directory is only a
process fact: each ACP session can claim a different workspace. An ACP session
ID is not an account, a credential, or proof of workspace authority.

## Research and reuse decisions

The [ecosystem survey](../research/2026-09-07-acp-ecosystem.md) covers 24 repositories,
primary specifications, editor documentation, licenses, release activity, tests,
CI, and public advisory checks. The [bridge-pattern review](../research/2026-09-07-acp-bridge-patterns.md)
pins the source and regression tests relevant to this implementation. These are
dated evidence snapshots, not a claim that every ACP project has been audited.

| Responsibility | Selection | Reason and boundary |
| --- | --- | --- |
| Codex ↔ ACP adaptation | Active `agentclientprotocol/codex-acp` | Reuse its Codex App Server integration, auth, tools, history, and extensions. The old `zed-industries/codex-acp` is archived; do not build on it. |
| Claude ↔ ACP adaptation | `agentclientprotocol/claude-agent-acp` | Reuse its Claude Agent SDK integration and terminal-auth support. Maintained by the ACP organization, not a claim that Anthropic maintains the adapter. |
| Lifecycle and semantic proxy patterns | Official `agentclientprotocol/rust-sdk` | Reference its existing proxy/conductor, duplex shutdown, descendant cleanup, and ordering tests. Do not port the whole Rust SDK to Go. |
| ViberMate's first transport layer | Go standard-library byte relay | No ACP SDK is necessary to forward opaque streams. Preserve future fields, vendor extensions, original IDs, and serialization exactly. |
| Later Go protocol endpoint/observer | Re-evaluate `coder/acp-go-sdk`, `Tangerg/acp`, and `spachava753/acp-sdk` | Coder has a longer track record but an older schema and a 10 MiB line reader. Tangerg has newer stable-schema and cross-platform test evidence but a short, concentrated maintenance history. Spachava's unstable schema and inherited MCP lineage need care. No dependency selected yet. |

The stable wire protocol is version `1`; schema/SDK release numbers are separate.
Do not introduce the draft v2 proxy protocol simply because an SDK's release is
named v2. The Go SDKs' typed decode/re-encode paths are not byte-transparent
forwarders; some also log malformed payloads. A future observer's parse budget
must never become a forwarding limit.

Cursor's documented `agent acp` makes **Cursor CLI an ACP agent**. It does not
prove that the Cursor editor can host arbitrary external ACP agents. Initial GUI
acceptance should use the documented external-agent flows in Zed and JetBrains,
then verify the user's exact Cursor integration separately. Preserve Cursor's
blocking `cursor/ask_question` and `cursor/create_plan` requests as well as its
notifications; extensions are not limited to names beginning with `_`.

## Implemented: transport foundation

`internal/acpbridge.Relay` has one narrow responsibility: copy both directions
between two endpoints, using two fixed 32 KiB buffers. It does not parse JSON,
rewrite capabilities or IDs, answer permissions, log payloads, or retain content.
It does not open a listener or launch a child.

Ownership is explicit: after validating arguments, the relay owns four independent
closeable stream halves. Closing a half must interrupt its pending I/O. Borrowed
terminal handles and arbitrary readers whose `Close` cannot unblock `Read` do not
satisfy the contract. The future launcher adapter must supply suitable handles;
passing `os.Stdin` directly is not that implementation.

Client EOF half-closes agent input and drains final agent output. Agent EOF
unblocks idle client input; if already-read input cannot be delivered, the result
is an explicit payload-free incomplete-transfer failure. Cancellation/I/O failure
closes owned streams and joins both pumps. Graceful output-close failures are
reported. Byte counters count destination acceptance, not prompts, successful
model calls, or billing usage.

The caller still owns child stderr, context deadlines, process groups, exit
status, and reaping. Neither this package nor its real-child test establishes a
production process supervisor. Error strings are safe; unwrapped I/O causes can
contain private data and must not be logged. Nothing diagnostic belongs on ACP
stdout. Child stderr is not inherently safe to persist either.

Current synthetic tests cover:

- Both directions, reverse permission/file requests, same-valued opposite-direction
  IDs, unknown fields/methods, Cursor extensions, and load/resume/cancel envelopes.
- Exact bytes: CRLF, fragments, non-UTF-8, an unterminated message, and an 11 MiB
  frame beyond the inspected Coder SDK's limit. These are transport tests, not
  claims of semantic implementation of the methods inside the envelopes.
- EOF half-close/final drain; idle reader cancellation; backpressure in either
  direction; partial delivery interrupted by agent EOF; short writes and close
  failures; private-error suppression; pre-cancelled ownership and invalid input.
- A synthetic real child whose final stdout drains, stderr stays separate, and
  process is reaped, including assertion-failure cleanup.

No new dependency, account, persistence schema, HTTP policy bypass, content store,
or UI surface is introduced in this slice.

## Next slice: launcher and interactive authentication

Planned syntax, **not currently available**:

```text
vibermate acp -- codex-acp
vibermate acp -- claude-agent-acp
vibermate acp -- agent acp
```

Use argv directly, never an implicit shell. Everything after `--` belongs to the
child, including flags appended by an editor's authentication flow. Resolve the
configured executable and preserve its argument order, environment, directory,
exit code, and signals. Do not silently download or upgrade an agent adapter;
compatibility acceptance pins the adapter version separately.

Deepen the existing `internal/runlauncher` process seam. Its current Unix group
signal forwarding and foreground-terminal support are useful, but its
`exec.Cmd.WaitDelay` fallback kills only the direct process. ACP acceptance also
needs bounded whole-group escalation and descendant cleanup when a launcher
wrapper exits before its grandchildren. A process group is cleanup containment,
not a sandbox against a process deliberately escaping it.

The supervisor must:

1. Own child start, cancellable input handles, stderr forwarding, group signals,
   exit observation, grace periods, and one final reap. Avoid a blocked goroutine
   reading borrowed GUI stdin after child exit.
2. On client EOF, drain the child for a bounded grace period; on premature stdout
   EOF, preserve already delivered output, report undelivered input, and still
   collect the actual child exit status. A relay's success is not process success.
3. Treat `session/cancel` and `$/cancel_request` as protocol traffic, not process
   signals that kill sibling sessions.
4. Keep ordinary `vibermate run` behavior unchanged and regression-test it.
5. Give terminal-auth invocations real interactive stdio/TTY with no transcript
   observation. Standard auth appends advertised args to the configured command,
   overlays env, waits for exit zero, and then reconnects. Claude's `--cli auth
   login --claudeai` is one reference, not a universal magic-flag detector.

There is a source conflict: the current normative terminal-auth specification
says to append args; the registry's authentication guide still says to replace
them. Validate actual editor implementations/versions. Legacy auth descriptors
can also name another executable and bypass the wrapper. Do not hide these
differences by rewriting arbitrary authentication messages or credentials.

Before exposing a managed `acp` command, settle its runtime registration contract
with the following slice. An unregistered relay must never be displayed as a
managed/retained Capture or imply HTTP traffic-policy enforcement.

## Following slice: session observation and Runtime ownership

Design the ACP domain boundary before reusing the old prototype's ACP boolean or
creating server rows. In particular, do not omit Capture assignments, root checks,
or Environment validation merely to fit ACP into an HTTP-only issuance path.

Keep separate identities for runtime principal, device, wrapper connection
generation, native agent session, and request initiator/ID. Track successful
new/load/resume/fork results rather than interpreting every notification as a new
session. The same native session may reconnect through another process; multiple
sessions may coexist in one process. Agent-reported workspace roots are claims
with provenance, not a mutation of a frozen Capture workspace or automatic file
authorization. Update the domain model/ADR only when those invariants are settled.

A separate bounded observer may read copies after successful forwarding. It must
never block the protocol behind server persistence, drop bytes from forwarding,
or log unknown/malformed bodies. On oversized frames, queue pressure, or unsupported
semantics, mark observation incomplete/unavailable; do not fabricate a complete
conversation. Pending correlation, session count, and parse memory all need limits.

`session/load` replays history before its response. `session/resume` restores
without replay. Replayed updates are not new usage. An ACP prompt can contain
multiple model exchanges and tools; HTTP evidence can include internal/title
requests. Do not duplicate usage across layers or correlate by content/time
similarity. Only expose cross-layer links supported by exact evidence.

## Following slice: App/Web, policy, and retention

Keep local personal launch low-friction through existing local ownership checks;
remote/team launches use an existing authenticated Runtime User. Do not create a
second ACP account or token system. Agent tool approval remains in the editor
unless a separately designed, explicit ViberMate policy adds a decision boundary.

Separate three capabilities in UI and docs: ACP session visibility, ACP content
retention, and HTTP model-traffic capture/routing. Stdio forwarding does not prove
that a child honors proxy/CA settings or supports managed account replacement.
Validate each pinned adapter's HTTP behavior before offering that control.

Content retention is server-owned and independent of forwarding. Default the new
observer to metadata-only until policy is explicit. With content disabled, do not
serialize/enqueue prompts, tool arguments, auth metadata, or raw frames for later
disposal. Preserve honest bounded status/counters where policy allows. Never enable
`APP_SERVER_LOGS` or `CLAUDE_AGENT_LOGS` as an implicit recorder. This controls
ViberMate retention, not the upstream agent's own native history files.

The eventual UI must show agent/session/workspace provenance, reconnect/load
state, permission waiting, observation completeness, and failed process startup
without dummy sessions. Reuse the account and client-access pages; provide
copyable editor configurations and a connection check only once the command and
those specific GUI versions have passed acceptance.

## Delivery gates

| Gate | Required evidence | Current state |
| --- | --- | --- |
| Research selection | Pinned primary-source survey, activity/license/CI checks, source-level bridge patterns | Done; reports linked above. No third-party code executed. |
| Opaque transport | Exact-byte, bounded-buffer, EOF, backpressure, cancellation, privacy, and real-child fixture tests | Implemented; race-tested repeatedly. Not composed into CLI. |
| Production launcher | Group teardown/grandchildren, final nonzero exit, GUI stdin still open, huge unterminated stderr, actual TTY login, argv/env and cancellation | Pending. |
| Runtime/session authority | Multi-workspace claims, owner isolation, authenticated registration/reconnect, restart/reopen, retention-off, replay/correlation and unknown states | Pending design and implementation. |
| App/Web experience | Setup/configuration, meaningful session status, account separation, bilingual/accessibility tests | Pending. |
| Real interoperability | Pinned Codex/Claude adapters with Zed/JetBrains; Cursor's exact role; permissions, tools, images, multi-session, resume, auth and proxy behavior | Pending; synthetic transport tests are not substitute evidence. |
| Release | macOS/Linux acceptance, unchanged HTTP capture tests, actual feature UI/CLI support, documented limitations | Not a release candidate. No mainline merge or package publication in this slice. |

Run `go test -race ./internal/acpbridge` for the new boundary; run `go test ./...`,
`go vet ./...`, and `make check-format check-dependencies check-structural` for
repository regression checks. Later stages add their own tests before changing
this table's status. Real provider/GUI tests are opt-in and must not reuse or log
personal credentials from the development environment.

## Local verification of this slice

On 2026-09-07, the macOS development environment passed `go test ./...`,
`go test -race ./...`, `go vet ./...`, and
`make check-format check-dependencies check-structural`.
The ACP suite additionally passed 20 consecutive race-enabled runs and
`go test -race -cover ./internal/acpbridge` (94.3% statement coverage).
`GOOS=linux GOARCH=amd64 go build ./...` passed; that is cross-compilation,
not a Linux runtime or GUI acceptance test. No real agent/provider, editor,
Flutter UI, installer, or release acceptance is claimed by these results.
