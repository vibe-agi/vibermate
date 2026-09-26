# Capability and Support Matrix

This matrix separates what users can install from work that exists only in
source. A passing unit test, an isolated development branch, and a published
product are not interchangeable evidence.

Source package version: **0.1.14**. Latest published release: **v0.1.13**.

- **Released** — included in the latest published release and covered by the
  stated release evidence.
- **Experimental** — included in the release, but depends on an upstream or
  compatibility contract that may change.
- **Branch-only** — implemented in the current stabilization source or a named
  development branch, but not included in the latest release.
- **Unsupported** — not offered as a working product capability.

## Runtime and deployment

| Capability ID | Status | Current boundary |
| --- | --- | --- |
| `macos-app` | Released | macOS 14+ Universal App for Apple silicon and Intel. The exact v0.1.13 DMG passed Developer ID signing, Apple notarization/stapling, Gatekeeper, and an isolated installed-App smoke test in the [protected release run](https://github.com/vibe-agi/vibermate/actions/runs/35752467645). That result applies to the tagged artifact, not later source commits. |
| `linux-server-web` | Released | Native Runtime Server, CLI, and Web workbench archives for Linux x86-64 and ARM64. |
| `docker-server-web` | Released | The same Runtime Server and Web workbench can run from the versioned Docker/Compose files; Docker does not create a separate account or certificate model. |
| `local-web-http` | Released | Native or container Web on loopback HTTP; no domain or certificate is required. |
| `remote-web-tls` | Released | Explicit private-CA DNS/IP identity, automatic public-domain HTTPS, or operator-provided certificate files. Server HTTPS identity remains separate from the Proxy CA except in the explicitly selected private-CA mode. |
| `web-manual-proxy-login` | Branch-only | The v0.1.14 candidate lets an Owner create, rotate, revoke, and deliver a manual proxy login and its public Proxy CA through the Web management API. It becomes Released only after the candidate artifacts pass the release gates. |
| `windows-runtime` | Unsupported | There is no Windows App, Server, or managed launcher release. |

## Clients, routing, and accounts

| Capability ID | Status | Current boundary |
| --- | --- | --- |
| `managed-claude-codex` | Released | `vibermate run` starts recognized Claude Code or Codex CLI processes through a local App or an explicitly selected Server. Release evidence is version- and path-specific; it is not a claim that every future client build is compatible. |
| `provider-static-credentials` | Released | Upstream API credentials are managed independently from Runtime User login and are injected only after route selection. |
| `codex-oauth` | Experimental | OAuth login and `auth.json` import use one managed Codex credential format, automatic preparation, and explicit manual refresh. ViberMate becomes the refresh owner for the imported copy; continuing to refresh the original file can invalidate either copy. This is not an official OpenAI integration or a stable public API contract. |
| `codex-quota-history` | Experimental | An Owner may explicitly query the selected managed Codex account's upstream quota and, when separately authorized, history. These observations are not ViberMate traffic statistics or billing authority. |
| `codex-reset-credit` | Branch-only | The v0.1.14 candidate can inspect and explicitly consume one selected banked reset credit through an Owner-only, confirmed, idempotent management operation. It becomes Experimental only after the candidate artifacts pass the release gates. |
| `native-cli-identity-rewrite` | Unsupported | Routing an upstream account changes the actual request credentials. It does not rewrite the CLI's local `auth.json`, local profile, or every native `/status` identity field. |
| `automatic-account-failover` | Unsupported | One request does not silently move between accounts after an authentication or quota failure. |
| `arbitrary-client-compatibility` | Unsupported | Manual proxy access does not imply semantic parsing, identity attribution, or tested compatibility for every application. |
| `editor-acp` | Branch-only | The v0.1.14 candidate includes bounded ACP observation through App or Server. VS Code 1.139.0 with ACP Client 0.2.0 passed isolated auth/session/prompt/tool/cancel/EOF/reconnect acceptance; Codex ACP 1.13.1 passed its real auth-required boundary. It becomes Experimental only after the candidate artifacts pass the release gates, and ACP does not inherit HTTP routing/account policy. |

## Evidence, storage, and extensions

| Capability ID | Status | Current boundary |
| --- | --- | --- |
| `retained-evidence` | Released | Recording mode and retention control semantic and Raw HTTP evidence. The SQLite archive is not encrypted by ViberMate; recognized credential fields are removed by bounded rules, not by a claim that arbitrary content is secret-free. |
| `raw-stage-compare` | Branch-only | The v0.1.14 candidate compares retained client/upstream request and response stages without rewriting retained bytes. It becomes Released only after the candidate artifacts pass the release gates. |
| `outbound-visibility` | Branch-only | The v0.1.14 candidate distinguishes inspected HTTP, decoded content, blind forwarding, and traffic not observed by ViberMate. It cannot infer a local file path from network bytes. It becomes Released only after the candidate artifacts pass the release gates. |
| `verified-backup-restore` | Branch-only | The v0.1.14 candidate provides manifest-bound backup, verification, and offline restore. Provider secrets and externally supplied TLS keys are excluded. It becomes Released only after the candidate artifacts pass the release gates. |
| `release-check` | Released | Settings compares App, Runtime, and terminal-command builds and checks the official GitHub Release only after an explicit user action. |
| `automatic-updates` | Unsupported | Releases, checksums, Homebrew, and the website are published explicitly; the product does not silently self-update. |
| `plugins` | Unsupported | The JavaScript transform sandbox is not a plugin marketplace or general extension runtime. |
| `postgresql-runtime-store` | Unsupported | The current runtime store is SQLite. A PostgreSQL backend and verified migration are later-stage work. |
| `team-knowledge-base` | Unsupported | Captured evidence is not yet an approved, independently retained team knowledge-document system. |

## Evidence rules

Release packaging evidence proves only the exact tagged artifacts. Deterministic
or mocked provider tests do not prove a live provider account, and one real
provider acceptance run does not prove every provider, model, client, or
editor. The [packaged acceptance contract](m0-acceptance.md), [deployment
guide](deployment.md), and [runtime module map](module-map.md) state the narrower
boundaries behind this table.
