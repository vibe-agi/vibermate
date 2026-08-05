# Current Dual-Client Credentialed Packaged Acceptance

Date: 2026-08-05

## Frozen candidate

- Source revision: `63de401374358af2f6630201c8d75dd5ab3ca9d9`
- Source state: clean
- Platform and architecture: macOS arm64
- App and sidecar profile: development
- Report schema: `vibermate.m0-assembly-acceptance/v6`
- App-bundle SHA-256:
  `e6f8855f7b9e4c3b735e8bed66b875647625d2db0b4be84e3b647ec4b9f45dab`
- Acceptance-executable SHA-256:
  `a95057d09a220c445d420ef43ceee15ece7999a0b93ef6eabef0830314c9928c`
- Packaged-daemon SHA-256:
  `be47d0fc5fd0773217729d90e0a299ed397b2ff05c946673c01d2656ca8e82dd`
- Packaged-launcher SHA-256:
  `df5b642d6e77c8547af6799d0eabe4757b14f0bf586299ccce110806ffacb903`

The provider coordinates and credential are operator-owned private inputs and
are deliberately absent from this committed evidence. The runner received only
a logical secret reference. The two passing reports remain outside Git in one
private `0700` directory and have mode `0600`.

| Fixed client | Result | Bytes | Private report SHA-256 |
| --- | ---: | ---: | --- |
| Claude Code 2.1.220 | 26/26 | 10,173 | `b9e35ba4538099ab7af219e429d1592794573a7a26d41a50ca9c438c7b28125e` |
| Codex CLI 0.145.0 | 29/29 | 10,948 | `89d4a8601e2968ddbe98e4f8ceb9a90a6987237af5abdc1c7bf7152fe4f7f49f` |

Both reports bind the exact clean source revision, App, embedded build
manifest, packaged sidecars, acceptance executable, fixed client, and frozen
configuration inputs. A separately built verifier accepted each report only
under an explicit `credentialed` expectation. Requiring `deterministic`,
omitting the mode, changing the source revision, or substituting an artifact
fails closed.

The development SecretStore contained three logical entries during the run. A
value-by-value comparison found none of their secret values in either report.
The reports also contain no prompt, response body, header, tool arguments, or
private thread identity.

## Observed sequence

The first Claude invocation used an ambient Node 25 host and failed at
`build-provenance` before fixed-client launch, daemon startup, or provider
traffic. That failed private report was retained. The same source, App, runner,
and client were then run under the pinned Node 22.23.1 and Rust 1.88.0
environment; no artifact was rebuilt between the failed invocation and the
successful retry.

The pinned Claude run passed all 26 checks. The pinned Codex run passed all 29
checks without a retry. Both included the complete isolated deterministic
phase before creating a separate credentialed database and generation.

## What this proves

For these two fixed client releases and this one operator-selected provider
route, the packaged production composition proves:

- clean build provenance and exact fixed-client release evidence;
- native-shell bootstrap, daemon readiness, private data directories, Access
  commit, exact ingress policy, and durable client-side ConnectionEvent audit;
- no-credential fail-closed behavior, graceful shutdown, forced termination,
  SQLite reopen, and interrupted-connection recovery in the isolated
  deterministic phase;
- a real unheld provider reply through the managed Access route;
- durable tool approval and post-decision execution (`Write` for Claude and
  `exec` for Codex);
- planned Hold, exact probe, Resume, and successful continuation;
- captured-client interruption while the shared runtime remains healthy;
- Codex HTTP fallback and private thread continuity across `exec resume`; and
- final bounded daemon drain.

The selected Access uses the default `follow-client` presentation. Client H1
stays H1 and client H2 stays H2; the test does not select named-product
emulation. VibeMate creates a new upstream connection after MITM, so
`follow-client` means bounded reconstruction of the observed client shape and
User-Agent, not byte-for-byte ClientHello passthrough.

## Boundaries

This remains fixed-client, development-profile evidence. It does not prove
arbitrary client releases, interactive TUI delta rendering, successful
Responses WebSocket semantics, exact JA3/JA4 or HTTP/2 SETTINGS/header-order
parity, Keychain storage, operating-system Root trust, signed/notarized
distribution, LAN Server mode, plugins, Language Bridge, physical sleep, power
loss, or Preview/Release readiness.

The reports prove the managed route only. They do not prove the system
`original_passthrough` route or a manual application capture.
