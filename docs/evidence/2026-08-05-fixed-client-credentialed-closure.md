# Fixed-Client Credentialed Packaged Closure

Date: 2026-08-05

## Frozen implementation candidate

- Source revision: `3064a417baa93426ad221947c3eeba920938ab5b`
- Source state: clean
- Platform and architecture: macOS arm64
- App profile: development
- Report schema: `vibermate.m0-assembly-acceptance/v6`

Four reports remain outside Git in a private `0700` directory. Every report
has mode `0600`, binds the clean candidate above, and contains no prompt,
response body, header, tool argument, thread identity, or secret value.

| Client and mode | Result | Bytes | Report SHA-256 |
| --- | ---: | ---: | --- |
| Claude Code 2.1.220 deterministic | 18/18 | 8,758 | `688bd79288cd18df5d4209e1c2efac9d02c5dd94bb3065c7573ecc2c8fccc6e8` |
| Codex CLI 0.145.0 deterministic | 19/19 | 9,051 | `f38ab1d9dee0d720f9658148cea3af8d76ab3cbe84c86fab6dc709d0c5e3e868` |
| Claude Code 2.1.220 credentialed | 26/26 | 10,156 | `319a4726e5d9da2c7d93092360c0ce511774310d89db86c327026af02860db7f` |
| Codex CLI 0.145.0 credentialed | 29/29 | 10,900 | `5ac7d53588a4a672c08de4d7d2915f97b19930f8f5d5ec0825543eebcd7c6f5a` |

An independently built verifier re-read the source checkout, App, embedded
build manifest, packaged daemon and launcher, acceptance executable, fixed
client entrypoint, and frozen configuration inputs. It accepted both
deterministic reports only for the exact candidate revision and client
release.

## Closed integration gaps

The full runner now uses two isolated databases. The deterministic phase owns
a fresh missing logical credential reference and drains completely. The
credentialed phase then creates revision 1 from an empty database with the
configured logical reference. It never mutates an existing binding or adds a
caller-selected credential binding through ordinary Access edit.

The fixed Codex `exec` tool is a JavaScript orchestration surface, not a raw
shell tool. The acceptance request now supplies valid bounded JavaScript which
invokes its nested command tool. The real run proves that the exact intent is
durably pending with no side effect, one `allow-once` decision releases it, and
the expected proof exists before the captured client is deliberately
interrupted. A provider response naming a tool absent from the request catalog
remains a terminal codec failure; no alias or guessed tool is executed.

Claude emits the same tool-use identity in both incremental and completed
envelopes. Acceptance now counts unique direct protocol identities instead of
recursively counting type-like data inside tool input. The real run proves the
`Write` intent remained behind the same durable barrier, produced the exact
proof, and then completed normally.

## What the credentialed reports prove

For the two fixed releases, the packaged App path proves:

- a real unheld provider reply through the typed Access and provider route;
- Codex HTTP fallback and private thread continuity across `exec resume`;
- durable fail-closed tool approval and post-decision execution;
- planned Hold, exact probe, Resume, and successful continuation;
- captured-client interruption while the runtime remains healthy;
- daemon graceful shutdown, forced termination, SQLite reopen, and interrupted
  ConnectionEvent recovery; and
- private credential metadata use without exposing the secret value.

## Boundaries

This is fixed-client development-profile evidence, not a Preview or Release
claim. It does not prove arbitrary client releases, interactive TUI rendering,
successful Responses WebSocket semantics, Keychain storage, system Root trust,
signed or notarized distribution, LAN Server mode, plugins, or physical
sleep/power-loss recovery.

The default upstream presentation remains `follow-client`. MITM necessarily
creates a new upstream TLS connection, so these reports do not claim raw
ClientHello, JA3/JA4, HTTP/2 SETTINGS, or header-order identity with the
captured client. Named client emulation remains an explicit user choice.
