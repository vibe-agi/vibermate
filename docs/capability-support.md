# Capability and Support Matrix

This matrix states what the current source offers. A passing unit test and a
published product are not interchangeable evidence.

Source package version: **0.1.21**. Latest published release: **v0.1.21**.

[v0.1.21](https://github.com/vibe-agi/vibermate/releases/tag/v0.1.21) opens only
databases of its own schema revision and has no migrations
([ADR 0021](adr/0021-one-schema-revision-without-migrations.md)).

- **Released** — part of the latest published release.
- **Available** — implemented and tested in the current source; not yet part
  of a published release.
- **Experimental** — implemented, but depends on an upstream or compatibility
  contract that may change.
- **Unsupported** — not offered as a working product capability.

## Runtime and deployment

| Capability ID | Status | Current boundary |
| --- | --- | --- |
| `macos-app` | Released | macOS 14+ Universal App for Apple silicon and Intel. v0.1.21 was signed, notarized and installed by [run 36742979182](https://github.com/vibe-agi/vibermate/actions/runs/36742979182); Homebrew cask `vibe-agi/tap/vibermate`. |
| `linux-server-web` | Released | Native Runtime Server, CLI, and Web workbench archives for Linux x86-64 and ARM64, built and verified by [run 36743075571](https://github.com/vibe-agi/vibermate/actions/runs/36743075571). |
| `docker-server-web` | Released | The same Runtime Server and Web workbench can run from the versioned Docker/Compose files; Docker does not create a separate account or certificate model. |
| `local-web-http` | Released | Native or container Web on loopback HTTP; no domain or certificate is required. |
| `remote-web-tls` | Released | Explicit private-CA DNS/IP identity, automatic public-domain HTTPS, or operator-provided certificate files. Settings offers a read-only public-domain deployment guide; server HTTPS identity remains separate from the Proxy CA except in the explicitly selected private-CA mode. Real public-domain issuance requires separate deployment acceptance. |
| `web-manual-proxy-login` | Released | An Owner can create, rotate, revoke, and deliver a manual proxy login and its public Proxy CA through the Web management API. The proxy login is separate from Runtime User and upstream Account credentials. |
| `server-ip-allowlist` | Released | The Owner limits which networks may connect to the Server port (Web, API, CLI login and Agent traffic); refused clients are disconnected before TLS or HTTP. Loopback is always allowed. Clients are judged by the TCP peer or, from load balancers listed with `--trusted-proxies`, by their PROXY protocol v1/v2 header; HTTP forwarding headers are never trusted. |
| `windows-runtime` | Unsupported | There is no Windows App, Server, or managed launcher release. |

## Clients, routing, and accounts

| Capability ID | Status | Current boundary |
| --- | --- | --- |
| `managed-claude-codex` | Released | `vibermate run` starts recognized Claude Code or Codex CLI processes through a local App or an explicitly selected Server. Release evidence is version- and path-specific; it is not a claim that every future client build is compatible. |
| `provider-static-credentials` | Released | Upstream API credentials are managed independently from Runtime User login and are injected only after route selection. |
| `codex-oauth` | Experimental | OAuth login and `auth.json` import use one managed Codex credential format and explicit manual refresh. Auto refresh defaults off for new imports and on for OAuth-created accounts; replacing credentials preserves the account's setting. When enabled, ViberMate owns refresh for its copy; refreshing the original file can invalidate either copy. This is not an official OpenAI integration or a stable public API contract. |
| `codex-quota-history` | Experimental | An Owner may explicitly query the selected managed Codex account's upstream quota and, when separately authorized, history. These observations are not ViberMate traffic statistics or billing authority. |
| `codex-reset-credit` | Experimental | An Owner can inspect and explicitly consume one selected banked reset credit through a confirmed, idempotent management operation. This depends on an upstream compatibility contract and never purchases a paid instant reset. |
| `native-cli-identity-rewrite` | Unsupported | Routing an upstream account changes the actual request credentials. It does not rewrite the CLI's local `auth.json`, local profile, or every native `/status` identity field. |
| `automatic-account-failover` | Unsupported | One request does not silently move between accounts after an authentication or quota failure. |
| `arbitrary-client-compatibility` | Unsupported | Manual proxy access does not imply semantic parsing, identity attribution, or tested compatibility for every application. |
| `editor-acp` | Experimental | Bounded ACP observation through App or Server. VS Code 1.139.0 with ACP Client 0.2.0 passed isolated auth/session/prompt/tool/cancel/EOF/reconnect acceptance; Codex ACP 1.13.1 passed its real auth-required boundary. ACP does not inherit HTTP routing/account policy. |

## Evidence, storage, and extensions

| Capability ID | Status | Current boundary |
| --- | --- | --- |
| `retained-evidence` | Released | Conversation-first reading separates verified incremental input and Agent text from tools, system context and raw evidence. Inspector state and reading position are preserved; full checkpoints do not masquerade as new user messages. Recording mode and retention still control semantic and Raw HTTP evidence. The SQLite archive is not encrypted by ViberMate; recognized credential fields are removed by bounded rules, not by a claim that arbitrary content is secret-free. |
| `body-free-usage` | Released | Runtime-wide statistics are independent of body recording, with permission-scoped caller/model breakdowns and reference API-equivalent USD costs, not provider bills. Inline request details and explicitly bounded loaded-request totals reuse collected usage; missing data is not zero, and reasoning subsets are not counted twice. New databases enable collection with 365-day retention; existing choices are preserved. Git project and branch attribution is a launch-time snapshot for new managed runs; missing history is not invented or backfilled. |
| `raw-stage-compare` | Released | The workbench compares retained client/upstream request and response stages without rewriting retained bytes. |
| `outbound-visibility` | Released | The workbench distinguishes inspected HTTP, decoded content, blind forwarding, and traffic not observed by ViberMate. It cannot infer a local file path from network bytes. |
| `verified-backup-restore` | Released | Offline backup, verification, and restore are manifest-bound. Provider secrets and externally supplied TLS keys are excluded. |
| `release-check` | Released | Settings compares App, Runtime, and terminal-command builds and checks the official GitHub Release only after an explicit user action. |
| `automatic-updates` | Unsupported | Releases, checksums, Homebrew, and the website are published explicitly; the product does not silently self-update. |
| `plugins` | Unsupported | The JavaScript transform sandbox is not a plugin marketplace or general extension runtime. |
| `postgresql-runtime-store` | Unsupported | The current runtime store is SQLite. A PostgreSQL backend and verified migration are later-stage work. |
| `team-knowledge-base` | Unsupported | Captured evidence is not yet an approved, independently retained team knowledge-document system. |

## Evidence rules

Codex compatibility checks use the official `rust-v0.145.0`, `rust-v0.158.0`
and `rust-v0.159.2` source contracts. Current native request controls (including
numeric reasoning effort and uncorrelated tool-result history) stay on the
same-dialect wire; an unrepresentable value is not silently translated to another
provider. Client-executed tool searches still require the tool decision gate.
The native provider can negotiate WebSockets first: ViberMate returns 426 and
Codex selects its HTTP fallback. The legacy feature flag alone does not force
HTTP in current Codex. See the official [request types](https://github.com/openai/codex/blob/rust-v0.159.2/codex-rs/codex-api/src/common.rs),
[history types](https://github.com/openai/codex/blob/rust-v0.159.2/codex-rs/protocol/src/models.rs)
and [stream terminal handling](https://github.com/openai/codex/blob/rust-v0.159.2/codex-rs/codex-api/src/sse/responses.rs).

The codex-current-compatibility CI job exercises Codex 0.159.2 on macOS and Linux against
a synthetic proxy and the production Responses codec, including 426 fallback,
HTTP retries, SSE completion and usage. This is separate from digest-catalog
recognition and does not label other builds as verified. Go and Flutter also
validate the same generated `api/samples/content-contract.json` fixture, covering
preview normalization, all response stop reasons and empty terminals. Local
macOS Codex capture and `vibermate doctor` check the current Root's trust status
before recommending or starting a native-trust capture.

Release packaging evidence proves only the exact tagged artifacts. Deterministic
or mocked provider tests do not prove a live provider account, and one real
provider acceptance run does not prove every provider, model, client, or
editor. The [packaged acceptance contract](m0-acceptance.md), [deployment
guide](deployment.md), and [runtime module map](module-map.md) state the narrower
boundaries behind this table.
