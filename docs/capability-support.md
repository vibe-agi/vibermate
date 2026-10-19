# Capability and Support Matrix

This matrix states what the current source offers. A passing unit test and a
published product are not interchangeable evidence.

Published-status snapshot at candidate preparation: **2026-10-19** (Asia/Singapore).

Source package version: **0.1.31**. Latest published release: **v0.1.30**.

At that snapshot, [v0.1.30](https://github.com/vibe-agi/vibermate/releases/tag/v0.1.30)
was the latest published release; v0.1.31 is an unpublished source candidate.
The historical v0.1.24 [upgrade and rollback boundary](releases/v0.1.24.md#upgrade-and-rollback-boundary)
and [ADR 0021](adr/0021-one-schema-revision-without-migrations.md) describe
schema-revision-1 data compatibility. Current source upgrades and private rollback
are documented under [private rollback snapshots](deployment.md#private-rollback-snapshots);
the historical boundary does not establish candidate downgrade compatibility.

- **Released** — part of the latest published release.
- **Available** — implemented and tested in the current source; not yet part
  of a published release.
- **Experimental** — implemented, but depends on an upstream or compatibility
  contract that may change.
- **Unsupported** — not offered as a working product capability.

## Runtime and deployment

| Capability ID | Status | Current boundary |
| --- | --- | --- |
| `macos-app` | Released | macOS 14+ Universal App for Apple silicon and Intel. v0.1.23 was signed, notarized and installed by [run 37006092239](https://github.com/vibe-agi/vibermate/actions/runs/37006092239); Homebrew cask `vibe-agi/tap/vibermate`. Development Preview uses a separate App identifier and UI preferences domain. |
| `linux-server-web` | Released | Native Runtime Server, CLI, and Web workbench archives for Linux x86-64 and ARM64, built and verified by [run 37006108103](https://github.com/vibe-agi/vibermate/actions/runs/37006108103). |
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
| `managed-client-authentication` | Available | Explicit `vibermate run --env work --managed-auth -- codex` starts a supported recognized client using an existing managed destination without requiring a local OpenAI login; Claude uses the existing managed gateway-token path. Native Codex 0.163.0 synthetic acceptance covers fresh interactive startup and UUID resume. Local auth/config/history stay client-owned; catalogs, quota and Cloud/connector features retain their own supported boundaries. |
| `provider-static-credentials` | Released | Upstream API credentials are managed independently from Runtime User login and are injected only after route selection. |
| `codex-oauth` | Experimental | OAuth login and `auth.json` import use one managed Codex credential format and explicit manual refresh. Auto refresh defaults off for new imports and on for OAuth-created accounts; replacing credentials preserves the account's setting. When enabled, ViberMate owns refresh for its copy; refreshing the original file can invalidate either copy. This is not an official OpenAI integration or a stable public API contract. |
| `codex-quota-history` | Experimental | An Owner may explicitly query the selected managed Codex account's upstream quota and, when separately authorized, history. These observations are not ViberMate traffic statistics or billing authority. |
| `codex-reset-credit` | Experimental | An Owner can inspect and explicitly consume one selected banked reset credit through a confirmed, idempotent management operation. This depends on an upstream compatibility contract and never purchases a paid instant reset. |
| `codex-cyber-access` | Experimental | Account Standard, Daybreak Blue and Daybreak Red preferences take precedence over the client's mode on exact official ChatGPT Codex routes without changing the final model. Standard needs no capability lookup. Blue/Red use the preferred mode when advertised by that exact final model; otherwise they use Standard only if the same model explicitly advertises it. Missing, empty or failed capability discovery and models with no compatible mode retain the unavailable failure. Exact native Guardian requests derive Blue from a frozen Red preference before compatibility resolution without changing the account. No automatic account retry is introduced. |
| `codex-native-model-directory` | Experimental | The current source captures bodyless native model reads with a validated client_version query, managed credentials, bounded metadata/alias projection and account/route-scoped ETags. Native HTTP turn-state continuity is bounded in memory; new direct header recordings redact its values while live forwarding remains exact. |
| `native-cli-identity-rewrite` | Unsupported | Routing an upstream account changes the actual request credentials. It does not rewrite the CLI's local `auth.json`, local profile, or every native `/status` identity field. |
| `automatic-account-failover` | Unsupported | One request does not silently move between accounts after an authentication or quota failure. |
| `arbitrary-client-compatibility` | Unsupported | Manual proxy access does not imply semantic parsing, identity attribution, or tested compatibility for every application. |
| `editor-acp` | Experimental | Bounded ACP observation through App or Server. VS Code 1.139.0 with ACP Client 0.2.0 passed isolated auth/session/prompt/tool/cancel/EOF/reconnect acceptance; Codex ACP 1.13.1 passed its real auth-required boundary. ACP does not inherit HTTP routing/account policy. |
| `paseo-profile-picker` | Experimental | v0.1.23 includes an open-source Paseo 0.11.0-beta.3 plugin with a shared desktop/Web/mobile picker and persistent per-conversation Environment choice. It reuses managed CLI capture and adds a permission-filtered launch-choice catalog. Deterministic and official compiler checks do not claim live desktop or physical mobile acceptance. See [Paseo integration](paseo-integration.md). |

## Evidence, storage, and extensions

| Capability ID | Status | Current boundary |
| --- | --- | --- |
| `retained-evidence` | Released | Conversation-first reading separates verified incremental input and Agent text from tools, system context and raw evidence. History, large bodies and protocol metadata load in bounded pages; unopened checkpoints do not hydrate history. Unloaded content is distinct from omitted recording. Inspector state and reading position are preserved. Recording mode and retention still control semantic and Raw HTTP evidence. The SQLite archive is not encrypted by ViberMate; recognized credential fields are removed by bounded rules, not by a claim that arbitrary content is secret-free. |
| `reader-human-input` | Released | User rows require verified text, refusal, or an input-image attachment. Claude-style wire-user tool returns stay in the tool inspector; mixed human content retains observed order. Unproved deferred messages use a neutral content-loading entry until canonical blocks are loaded. Flutter VM and Chrome widget fixtures cover text, two retained PNG attachments, and repeat/replay anchors. Deferred navigation is checked at 390px with 200% text in English and Simplified Chinese; the two-image order, preview return, and text-only copy are checked at that size in English. These synthetic fixtures do not certify every client version, image format, or live mobile client. Missing tool content means only that a result is unavailable, not that a tool is running. |
| `native-tool-reader` | Released | Capture-scoped Tool service rows and separate Parameters/Result details use native operations, never model Exchanges or guessed conversation links. Authenticated reads verify retained bytes, bound pages and image dimensions, and invalidate stale work on lifecycle changes. Synthetic real-executor/SQLite/router tests cover text, structured JSON, PNG, cursor scope and deletion; the codec validates PNG/JPEG/WebP. VM and Chrome fixtures cover actual HTTP parsing, text/PNG details, late results, bounded caching, keyboard access and EN/zh 390px/200% layouts, including the full shell. Production content profiles remain unregistered: unknown content is unavailable. This does not establish actual-client compatibility or production tool-result profiles. Chrome behavior fixtures disable incidental splash shaders; real packaged theme assets still require artifact acceptance. |
| `body-free-usage` | Released | Runtime-wide statistics are independent of body recording, with permission-scoped caller/model breakdowns and reference API-equivalent USD costs, not provider bills. Inline request details and explicitly bounded loaded-request totals reuse collected usage; missing data is not zero, and reasoning subsets are not counted twice. New databases enable collection with 365-day retention; existing choices are preserved. Git project and branch attribution is a launch-time snapshot for new managed runs; missing history is not invented or backfilled. |
| `raw-stage-compare` | Released | The workbench compares retained client/upstream request and response stages without rewriting retained bytes. |
| `outbound-visibility` | Released | The workbench distinguishes inspected HTTP, decoded content, blind forwarding, and traffic not observed by ViberMate. It cannot infer a local file path from network bytes. |
| `runtime3-upgrade` | Released | The current source uses monolithic vibermate-runtime revision 3. Guarded startup upgrades published revisions 1→2→3 or 2→3 with independently verified private snapshots. New readers support exact revisions 1/2/3; older readers refuse revision 3. Rollback restores the preserved old-format snapshot into a new directory. See the [operator boundary](backup-and-restore.md#format-compatibility-and-rollback). |
| `verified-backup-restore` | Released | Offline backup, verification, and restore are manifest-bound. Provider secrets and externally supplied TLS keys are excluded. |
| `release-check` | Released | Settings compares App, Runtime, and terminal-command builds and checks the official GitHub Release only after an explicit user action. |
| `automatic-updates` | Unsupported | Releases, checksums, Homebrew, and the website are published explicitly; the product does not silently self-update. |
| `plugins` | Unsupported | The JavaScript transform sandbox is not a plugin marketplace or general extension runtime. |
| `postgresql-runtime-store` | Unsupported | The current runtime store is SQLite. A PostgreSQL backend and verified migration are later-stage work. |
| `team-knowledge-base` | Unsupported | Captured evidence is not yet an approved, independently retained team knowledge-document system. |

The 0.1.31 candidate retains the existing monolithic recording architecture.
Optional recording may still contend with control work; this release does not
complete the separate storage-isolation work or deploy a remote Runtime.

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

Codex keeps its native provider and persisted login selection. The launcher
preserves authentication inputs and translates only explicit `CODEX_BASE_URL`
or `OPENAI_BASE_URL` settings; exported-key presence does not establish the
native destination. With no explicit target, Capture retains the selected
Environment's declared origin boundary and admits observed requests against it.
A custom native provider does not automatically gain managed origin or Account
authority. Native TUI login requirements remain Codex-owned; Capture does not
supply a placeholder key or promise login-free startup.

Interactive Codex captures, including resume and fork, must own their request
backend. For a catalog-known Codex family, the launcher reads native help with a
30s deadline and a combined 64 KiB output limit, without Capture credentials
or proxy environment. It adds `--no-daemon` when supported (confirmed startup
contract in 0.159.2 and 0.160.0). The exact verified 0.145.0 adapter instead uses
its mandatory launch-recipe config overrides, which prevent implicit daemon
reuse. A failed or timed-out probe is reported separately from a successful help
result that lacks the required capability. A help result never changes release
recognition or grants Root/tool authority. Ordinary exec/review and foreground app-server retain their native
arguments. Remote connections, app-server daemon/proxy, agents, unknown options
and unproven session-control modes fail before the captured command starts;
existing backends are left running. Startup-contract checks do not by themselves
prove provider or MCP compatibility.

Release packaging evidence proves only the exact tagged artifacts. Deterministic
or mocked provider tests do not prove a live provider account, and one real
provider acceptance run does not prove every provider, model, client, or
editor. The [packaged acceptance contract](m0-acceptance.md), [deployment
guide](deployment.md), and [runtime module map](module-map.md) state the narrower
boundaries behind this table.

Remote launch and ACP session authentication use a separate 30s default budget;
an explicit `Config.ControlTimeout` also bounds authentication. Lifecycle control
(attach, heartbeat and finish) retains its 5s default, and Capture creation keeps
its separate 90s default. Shorter caller deadlines and cancellation apply to each.
