# Explicit Provider and First-Use Closure

Date: 2026-08-05

## Frozen implementation candidate

- Source revision: `203bae6e90219fe36bc20b9c32da8d7b53ed7446`
- Source state: clean
- Platform and architecture: macOS arm64
- App profile: development
- Report schema: `vibermate.m0-assembly-acceptance/v6`
- App bundle SHA-256: `0ae1c9a040088e740f683e53d4675d9b1e015002bf95a395ef11588cf217fe13`
- Acceptance executable SHA-256: `83f0f535030630f79f042ced54fe31fbbfa87189539efc3d864f460fbd7a8669`

The provider origin and model were explicit inputs matching the configured
Access. Their values and the provider credential are intentionally absent from
this document. The credential remained behind the development file
`SecretStore`; no command accepted or printed its value.

## User-journey result

The Access page was reviewed as a developer's first connection task: choose an
AI tool, choose where requests go, save the credential, then start the tool.
The production form now begins without a provider origin, model, or route name.
Choosing a compatible or custom service still leaves the model empty, keeps
Save disabled, and requires an explicit value. Choosing an official service is
the only action that applies that service's convenience preset.

The default upstream presentation remains `follow-client`. Named product
presentation is an explicit choice and never changes the selected route,
model, or account. A 1440 by 1000 light-mode review with reduced motion
confirmed that the empty compatible-service state is visible without exposing
advanced fields. The existing request-path runway remains the page's visual
anchor; no decorative redesign was introduced.

The browser suite passed 24 of 24 scenarios, including the first connection,
explicit empty compatible model, both locales, 390-pixel boundaries, keyboard
navigation, three workspace-scoped terminals, and manual application capture.
The React suite passed 379 tests, including two direct Access-default tests.

## Packaged fixed-client evidence

Five reports remain outside Git in a private `0700` directory. Every report
has mode `0600`. The first Claude attempt used Node 25 from the interactive
shell and failed closed at `build-provenance` before client traffic. Its report
was retained unchanged. The retry used the same source, App, acceptance
executable, and fixed client under the pinned Node 22.23.1 environment.

| Client and mode | Result | Bytes | Report SHA-256 |
| --- | ---: | ---: | --- |
| Claude Code 2.1.220 initial environment check | 0/1 | 4,910 | `5c5e3447acbe1a37fbb4b81490426265a7127d8a955df9a121d2aebff523d271` |
| Claude Code 2.1.220 deterministic retry | 18/18 | 8,770 | `6cb3a7b0a6f49f4fd453c3a2938ed8f8633ee1fe3d9c485c6f3dbe01258f1d08` |
| Codex CLI 0.145.0 deterministic | 19/19 | 9,062 | `2b6bf8c8703804a414b3b1ef0adf5f2946dc681a3d4682a27829e9ee84e627da` |
| Claude Code 2.1.220 credentialed | 26/26 | 10,164 | `1481b7b6fd3146abd809af682ad109b3a63b1fc480c3300fd7f8382eb4b873ae` |
| Codex CLI 0.145.0 credentialed | 29/29 | 10,905 | `492d21c515ac4cc813980daad5450c459524687f09f1fa64a16086ffae2cef34` |

An independently built verifier accepted both deterministic reports only for
the exact source revision, App, embedded manifest, daemon, launcher,
acceptance executable, configuration inputs, and fixed client release.

The credentialed reports prove real upstream replies, streaming, typed tool
approval and execution, Hold/probe/Resume, client interruption, shutdown, and
SQLite recovery for these two fixed client versions. They also prove that
removing the implicit provider defaults did not replace the route with a test
fixture or bypass the configured Access.

## Gates and boundaries

The candidate passed the pinned Node 22.23.1 `make check`, uncached Go tests,
the full race suite, vet, module verification, the fixed `govulncheck` command,
production UI dependency audit, and the forbidden-term scans in both
repositories. Rust audit still reports the existing 16 allowed unmaintained
warnings; this evidence does not describe it as warning-free.

This remains fixed-client development-profile evidence. It does not prove
arbitrary client versions, Keychain storage, operating-system Root trust,
signed/notarized distribution, Server mode, plugins, successful Responses
WebSocket semantics, TUI token rendering, or raw ClientHello/JA3/JA4 parity.
