# ACP ecosystem and reuse assessment

Date: 2026-09-07, Asia/Singapore. Repository/API observations were collected around
2026-09-06 16:44 UTC. This is a bounded, primary-source survey of 24 relevant
repositories plus first-party editor documentation, not a claim to have audited
every ACP project or certified any third-party agent as safe.

## Recommendation

Start with **a managed, byte-preserving stdio launch path**, using existing ACP
agents/adapters. Do not implement another Codex or Claude adapter, introduce a
second user-account system for ACP sessions, or put a schema-decoding proxy in the
forwarding path just to capture network traffic. These are design recommendations
for ViberMate, not requirements imposed by ACP.

Reuse priorities:

1. **Codex:** use the maintained
   [`@agentclientprotocol/codex-acp`](https://github.com/agentclientprotocol/codex-acp/blob/1a3c01e8ca317f83e3b60bc5632cf052882bea15/README.md)
   adapter. It already starts Codex App Server and maps ACP requests, session
   state, authentication, permissions, and Codex events. The old
   [`zed-industries/codex-acp`](https://github.com/zed-industries/codex-acp#readme)
   is archived and explicitly directs new installs to the ACP-organization
   package. Do not build on the obsolete `@zed-industries/codex-acp` package.
2. **Claude:** use
   [`@agentclientprotocol/claude-agent-acp`](https://github.com/agentclientprotocol/claude-agent-acp/blob/3e23c5b960b66a6d2c892e7524c952e731c076a7/README.md),
   the ACP adapter around the official Claude Agent SDK. Its ownership is the
   ACP organization; this is not a claim that Anthropic itself maintains the ACP
   adapter.
3. **Protocol and lifecycle reference:** the
   [official Rust SDK](https://github.com/agentclientprotocol/rust-sdk/blob/726c5030bfaa88cfdac2fb1f71a63abb331ce586/README.md)
   has a proxy/conductor implementation and dedicated test utilities. Borrow its
   lifecycle and transparent-forwarding lessons; adding a Rust runtime solely for
   these ideas is unnecessary. Its `_proxy/*` extension is explicitly
   [provisional](https://github.com/agentclientprotocol/rust-sdk/blob/726c5030bfaa88cfdac2fb1f71a63abb331ce586/md/protocol.md),
   not mandatory stable ACP.
4. **Go:** `coder/acp-go-sdk@v0.13.5` is the conservative small-dependency
   client/agent SDK candidate, but it is not the most up-to-date. Also evaluate
   `Tangerg/acp@v0.2.2` if ViberMate later needs a typed ACP endpoint. For a
   transparent relay, neither should decode/re-encode the traffic. See the
   source-level comparison below.
5. **Interop oracle:** use a pinned
   [official TypeScript SDK](https://github.com/agentclientprotocol/typescript-sdk/blob/5e2cfcabb5303dc93c093da788b68460b9958526/README.md)
   fixture and the existing adapter test patterns, rather than inventing a
   parallel interpretation of ACP. A mock's success must be distinguished from
   real editor/agent acceptance.

## What ACP does—and does not—mean

ACP here is **Agent Client Protocol**, connecting an editor/client to a coding
agent. Its stable wire protocol version is **1**. The latest stable JSON schema
release observed was **`schema-v1.21.0`**, while `v1.7.0` is the separate Rust
schema-crate artifact version. Draft v2 was published as
`schema-v2.0.0-alpha.3`; SDK/crate/schema semantic versions must not be substituted
for `initialize.protocolVersion`.
([Versioning explanation](https://github.com/agentclientprotocol/agent-client-protocol/blob/0d6f1549583064f898661179d41aba5044c49b3e/README.md),
[v1 schema release](https://github.com/agentclientprotocol/agent-client-protocol/releases/tag/schema-v1.21.0),
[v2 draft release](https://github.com/agentclientprotocol/agent-client-protocol/releases/tag/schema-v2.0.0-alpha.3))

MCP is a separate tool/context protocol. ACP sessions can supply MCP server
configuration to the agent; that does not turn an MCP server, a Codex App Server
connection, or a normal terminal CLI into an ACP agent. ViberMate wrapping a
process likewise does not supply an ACP adapter that the process lacks.
([ACP session setup](https://agentclientprotocol.com/protocol/v1/session-setup),
[Codex adapter boundary](https://github.com/agentclientprotocol/codex-acp/blob/1a3c01e8ca317f83e3b60bc5632cf052882bea15/README.md))

The official SDK list currently contains **Kotlin, Java, Python, Rust, and
TypeScript**. The Go and Dart projects are listed as **community** libraries.
In particular, a community Dart repository's self-description as “Official” must
not override that upstream classification.
([Official list](https://github.com/agentclientprotocol/agent-client-protocol/blob/0d6f1549583064f898661179d41aba5044c49b3e/README.md),
[Community list](https://agentclientprotocol.com/libraries/community),
[Dart repository](https://github.com/SkrOYC/acp-dart))

## Repository inventory

Dates below are UTC. “Push” is GitHub's repository `pushed_at` field: it can be a
branch, tag, automation, or dependency update, **not necessarily a default-branch
feature change**. A release is a published GitHub Release unless explicitly
marked “tag only”; no Release does not mean no Go module tag. Repository names
identify their current owners. The API links support push/archive/license
observations; release links support version/date observations. Stars were
inspected but intentionally are not used as quality scores.

### Specification and official SDKs

| Repository / owner | Role | Last push | Latest observed release | Archived | License |
| --- | --- | --- | --- | --- | --- |
| [agentclientprotocol/agent-client-protocol](https://api.github.com/repos/agentclientprotocol/agent-client-protocol) | Specification, schemas | 2026-09-06 | [schema-v1.21.0](https://github.com/agentclientprotocol/agent-client-protocol/releases/tag/schema-v1.21.0), 2026-08-20; draft v2 alpha.3 same day | No | Apache-2.0 |
| [agentclientprotocol/typescript-sdk](https://api.github.com/repos/agentclientprotocol/typescript-sdk) | Official TS client/agent SDK | 2026-09-05 | [v1.4.0](https://github.com/agentclientprotocol/typescript-sdk/releases/tag/v1.4.0), 2026-08-20 | No | Apache-2.0 |
| [agentclientprotocol/rust-sdk](https://api.github.com/repos/agentclientprotocol/rust-sdk) | Official Rust SDK, proxy/conductor | 2026-09-04 | [v2.1.0](https://github.com/agentclientprotocol/rust-sdk/releases/tag/v2.1.0), 2026-09-04 | No | Apache-2.0 |
| [agentclientprotocol/python-sdk](https://api.github.com/repos/agentclientprotocol/python-sdk) | Official Python SDK | 2026-09-03 | [0.12.1](https://github.com/agentclientprotocol/python-sdk/releases/tag/0.12.1), 2026-08-16 | No | Apache-2.0 |
| [agentclientprotocol/kotlin-sdk](https://api.github.com/repos/agentclientprotocol/kotlin-sdk) | Official Kotlin SDK | 2026-09-01 | [v0.32.0](https://github.com/agentclientprotocol/kotlin-sdk/releases/tag/v0.32.0), 2026-09-01 | No | Apache-2.0 |
| [agentclientprotocol/java-sdk](https://api.github.com/repos/agentclientprotocol/java-sdk) | Official Java SDK | 2026-08-28 | [v0.17.0](https://github.com/agentclientprotocol/java-sdk/releases/tag/v0.17.0), 2026-08-28 | No | Apache-2.0 |

### Go and Dart choices

| Repository / owner | Role and assessment | Last push | Latest observed release | Archived | License |
| --- | --- | --- | --- | --- | --- |
| [coder/acp-go-sdk](https://api.github.com/repos/coder/acp-go-sdk) | Low-dependency Go endpoint SDK; stronger established baseline, older schema | 2026-06-05 | [v0.13.5](https://github.com/coder/acp-go-sdk/releases/tag/v0.13.5), 2026-06-02 | No | Apache-2.0 |
| [Tangerg/acp](https://api.github.com/repos/Tangerg/acp) | Current stable-schema Go SDK; strongest newer alternative to spike | 2026-09-05 | [v0.2.2 tag](https://github.com/Tangerg/acp/tree/v0.2.2); [no GitHub Releases observed](https://api.github.com/repos/Tangerg/acp/releases) | No | Apache-2.0 |
| [spachava753/acp-sdk](https://api.github.com/repos/spachava753/acp-sdk) | Go SDK derived from MCP Go SDK; conformance/security tooling, unstable-schema scope | 2026-08-23 | [v0.4.0](https://github.com/spachava753/acp-sdk/releases/tag/v0.4.0), 2026-08-23 | No | Apache-2.0 changes plus inherited terms; see note below |
| [eino-contrib/acp](https://api.github.com/repos/eino-contrib/acp) | Go endpoint and WS proxy implementation; too broad for first stdio path | 2026-08-20 | [v0.0.4](https://github.com/eino-contrib/acp/releases/tag/v0.0.4), 2026-06-29 | No | Unresolved: API returns no license; no root LICENSE found at inspected pin |
| [ironpark/acp-go](https://api.github.com/repos/ironpark/acp-go) | Community Go SDK; lower current maintenance/release evidence | 2026-04-25 | [No GitHub Releases](https://api.github.com/repos/ironpark/acp-go/releases) or [tags](https://api.github.com/repos/ironpark/acp-go/tags) observed | No | MIT |
| [keepmind9/acp-sdk-go](https://api.github.com/repos/keepmind9/acp-sdk-go) | Alternate Go SDK; not shortlisted over stronger options | 2026-04-08 | [No GitHub Releases observed](https://api.github.com/repos/keepmind9/acp-sdk-go/releases) | No | MIT |
| [SkrOYC/acp-dart](https://api.github.com/repos/SkrOYC/acp-dart) | Community Dart SDK; unnecessary for a Flutter UI backed by Go | 2026-03-06 | [No GitHub Releases observed](https://api.github.com/repos/SkrOYC/acp-dart/releases) | No | MIT |

The `spachava753` license is not simply “unknown” despite GitHub's `NOASSERTION`:
its actual [LICENSE](https://github.com/spachava753/acp-sdk/blob/cd2aa97e9f381bf5358143c2c41ec12701779934/LICENSE)
states new changes are Apache-2.0 and inherited upstream contributions retain
their terms. Review notices if importing source. Similarly, the active Codex
adapter's [LICENSE](https://github.com/agentclientprotocol/codex-acp/blob/1a3c01e8ca317f83e3b60bc5632cf052882bea15/LICENSE)
contains Apache-2.0 despite its API classification. This is source provenance
checking, not a legal opinion.

### Agents, clients, adapters, and related reuse

| Repository / owner | ACP relationship | Last push | Latest observed release | Archived | License |
| --- | --- | --- | --- | --- | --- |
| [agentclientprotocol/codex-acp](https://api.github.com/repos/agentclientprotocol/codex-acp) | Maintained Codex App Server → ACP adapter | 2026-09-06 | [v1.10.0](https://github.com/agentclientprotocol/codex-acp/releases/tag/v1.10.0), 2026-09-04 | No | Apache-2.0, inspected LICENSE |
| [agentclientprotocol/claude-agent-acp](https://api.github.com/repos/agentclientprotocol/claude-agent-acp) | Claude Agent SDK → ACP adapter | 2026-09-05 | [v0.75.1](https://github.com/agentclientprotocol/claude-agent-acp/releases/tag/v0.75.1), 2026-09-05 | No | Apache-2.0 |
| [zed-industries/codex-acp](https://api.github.com/repos/zed-industries/codex-acp) | Obsolete Codex adapter; do not adopt | 2026-07-22 | [v0.16.0](https://github.com/zed-industries/codex-acp/releases/tag/v0.16.0), 2026-06-08 | **Yes** | Apache-2.0 according to README; API NOASSERTION |
| [google-gemini/gemini-cli](https://api.github.com/repos/google-gemini/gemini-cli) | Native ACP agent, `gemini --acp` | 2026-09-06 | Stable [v0.58.0](https://github.com/google-gemini/gemini-cli/releases/tag/v0.58.0), 2026-09-01; newer nightly excluded | No | Apache-2.0 |
| [anomalyco/opencode](https://api.github.com/repos/anomalyco/opencode) | Native ACP agent, `opencode acp` | 2026-09-06 | [v1.18.29](https://github.com/anomalyco/opencode/releases/tag/v1.18.29), 2026-09-04 | No | MIT |
| [aaif-goose/goose](https://api.github.com/repos/aaif-goose/goose) | Native ACP server and ACP-provider client; formerly `block/goose` | 2026-09-06 | [v1.49.0](https://github.com/aaif-goose/goose/releases/tag/v1.49.0), 2026-09-03 | No | Apache-2.0 |
| [zed-industries/zed](https://api.github.com/repos/zed-industries/zed) | Production editor ACP client | 2026-09-06 | Stable [v1.18.1](https://github.com/zed-industries/zed/releases/tag/v1.18.1), 2026-09-04; preview excluded | No | Mixed per component; API NOASSERTION; use as behavior reference |
| [openclaw/acpx](https://api.github.com/repos/openclaw/acpx) | Headless ACP client, persistence, draft conformance corpus | 2026-09-06 | [v0.14.0](https://github.com/openclaw/acpx/releases/tag/v0.14.0), 2026-09-06 | No | MIT |
| [agentclientprotocol/registry](https://api.github.com/repos/agentclientprotocol/registry) | Agent distribution metadata and authentication handshake checks | 2026-09-06 | [v2026.09.06-5b72618](https://github.com/agentclientprotocol/registry/releases/tag/v2026.09.06-5b72618), 2026-09-06 | No | Apache-2.0; individual agents have separate licenses |
| [ky2renzzz/acp-core](https://api.github.com/repos/ky2renzzz/acp-core) | Rust byte-preserving record/replay proxy; reference only | 2026-06-10 | [No GitHub Releases observed](https://api.github.com/repos/ky2renzzz/acp-core/releases) | No | Apache-2.0 |
| [mmonad/codex-acp-gateway](https://api.github.com/repos/mmonad/codex-acp-gateway) | Rust protocol translator and WS–stdio gateway; different scope | 2026-03-02 | [No GitHub Releases observed](https://api.github.com/repos/mmonad/codex-acp-gateway/releases) | No | Apache-2.0 |

The native-agent commands above are confirmed by
[Gemini CLI's docs](https://geminicli.com/docs/cli/acp-mode/),
[OpenCode's docs](https://opencode.ai/docs/acp/), and
[Goose's ACP registry entry](https://github.com/agentclientprotocol/registry/blob/39df6a03d94d60a16e2b7c52eb9f768fb40d57ff/goose/agent.json).
Goose documents both its ACP server and its ability to use ACP agents as
providers on its [first-party site](https://block.github.io/goose/).
The two smaller gateways were examined as relevant existing wheels, not selected
as dependencies: [acp-core](https://github.com/ky2renzzz/acp-core) focuses on
recording/replay, while
[codex-acp-gateway](https://github.com/mmonad/codex-acp-gateway) adds translation and
remote WebSocket exposure. Neither is needed to attach ViberMate's existing
managed process/network runtime to an editor's local stdio child.

## Go SDK comparison: evidence beyond stars

### `coder/acp-go-sdk@v0.13.5`

Inspected commit: `0845a3bb9eddda5bfc22a94dd3598c90cb842451` (also the v0.13.5
tag). The module has Go 1.21 as its language floor and no `require` dependencies;
generator dependencies are in a separate module. It implements both sides of ACP,
extension handlers, cancellation, ordered notifications, and response-scoped
notification barriers. Unit, JSON-golden, cancellation, and barrier tests are
present; CI runs tests and builds examples.
([go.mod](https://github.com/coder/acp-go-sdk/blob/0845a3bb9eddda5bfc22a94dd3598c90cb842451/go.mod),
[connection tests](https://github.com/coder/acp-go-sdk/blob/0845a3bb9eddda5bfc22a94dd3598c90cb842451/connection_notification_barrier_test.go),
[Makefile](https://github.com/coder/acp-go-sdk/blob/0845a3bb9eddda5bfc22a94dd3598c90cb842451/Makefile))

Its source pins schema `0.13.5`, substantially older than the currently published
stable schema. It must not be described as implementing every latest capability.
The transport also imposes a fixed **10 MiB** line limit, and malformed JSON is
logged with the entire raw line. That is unacceptable as-is for a ViberMate
privacy-sensitive observer; those are implementation choices, not ACP protocol
limits. Use a redacting logger and explicit size policy if using this endpoint
SDK, and do not route transparent traffic through it.
([schema pin](https://github.com/coder/acp-go-sdk/blob/0845a3bb9eddda5bfc22a94dd3598c90cb842451/schema/version),
[receive loop](https://github.com/coder/acp-go-sdk/blob/0845a3bb9eddda5bfc22a94dd3598c90cb842451/connection.go#L365))

### `Tangerg/acp@v0.2.2`

Inspected commit: `a49191166a4db11d4a5855d75e78f2fec7941989`. This is the strongest
newer Go alternative found for a **stable-v1 typed endpoint**, not a relay. It
pins published `schema-v1.21.0` with provenance hashes, explicitly excludes draft
v2, and requires Go 1.25. Its CI matrix includes macOS, Linux, and Windows with
race tests; the repository includes fuzz, limits, lifecycle, cancellation, golden,
and interoperability tests. A v0.2.2 tag CI run was green at inspection.
([schema provenance](https://github.com/Tangerg/acp/blob/a49191166a4db11d4a5855d75e78f2fec7941989/schema/README.md),
[Go floor/dependency](https://github.com/Tangerg/acp/blob/a49191166a4db11d4a5855d75e78f2fec7941989/go.mod),
[CI definition](https://github.com/Tangerg/acp/blob/a49191166a4db11d4a5855d75e78f2fec7941989/.github/workflows/ci.yml),
[tag CI](https://github.com/Tangerg/acp/actions/runs/33938265972))

The counterweight is limited maintenance redundancy: the contributor API returned
one contributor, and the public tags were only v0.1.0 through v0.2.2. This is not
evidence of bad code, but it is too little release history to declare it a mature
replacement without an interoperability spike.
([Contributors](https://api.github.com/repos/Tangerg/acp/contributors),
[Tags](https://api.github.com/repos/Tangerg/acp/tags))

### Other Go options

- `spachava753/acp-sdk@v0.4.0`, inspected at
  `cd2aa97e9f381bf5358143c2c41ec12701779934`, is a credible alternate: it has
  explicit conformance coverage and CodeQL/Scorecard workflows, but exposes a
  generated **unstable** schema and carries inherited MCP implementation/license
  history. Its large GitHub contributor count must not be mistaken for ACP
  maintainer breadth; the README explicitly says it is derived from the Go MCP
  SDK.
  ([README](https://github.com/spachava753/acp-sdk/blob/cd2aa97e9f381bf5358143c2c41ec12701779934/README.md),
  [conformance corpus](https://github.com/spachava753/acp-sdk/tree/cd2aa97e9f381bf5358143c2c41ec12701779934/conformance),
  [security workflows](https://github.com/spachava753/acp-sdk/tree/cd2aa97e9f381bf5358143c2c41ec12701779934/.github/workflows))
- `eino-contrib/acp` at `eba57736f6cf237ed0fe2d1afb1b1bddfa106f4f` usefully
  separates protocol endpoints from a protocol-blind byte proxy. Its proxy's
  northbound transport is WebSocket and its module includes Gin, Hertz, and both
  WebSocket stacks; that is not a narrowly scoped local-stdio dependency.
  Licensing is unresolved from the inspected tree/API, so do not copy/adopt it
  until provenance is clarified.
  ([Architecture](https://github.com/eino-contrib/acp/blob/eba57736f6cf237ed0fe2d1afb1b1bddfa106f4f/README.md),
  [dependencies](https://github.com/eino-contrib/acp/blob/eba57736f6cf237ed0fe2d1afb1b1bddfa106f4f/go.mod),
  [tree](https://github.com/eino-contrib/acp/tree/eba57736f6cf237ed0fe2d1afb1b1bddfa106f4f))
- `ironpark/acp-go` and `keepmind9/acp-sdk-go` remain useful comparison material,
  but their older observed activity and lack of published GitHub releases did not
  establish a reason to prefer them over the three SDKs above. This is a
  maintenance-risk judgment, not a measured performance or correctness ranking.

## Editor/client compatibility: verified versus assumed

| Product | Verified ACP role and setup | Consequence for ViberMate |
| --- | --- | --- |
| Zed | Hosts external ACP agents; custom `agent_servers` entries accept `command`, `args`, `env`, and `type: "custom"`. [First-party docs](https://zed.dev/docs/ai/external-agents#custom-agents) | Make the configured executable ViberMate, with the existing ACP agent command after the wrapper's separator. Keep credentials/provider configuration owned by the downstream agent. |
| JetBrains AI Assistant | Hosts external agents, including custom entries in `~/.jetbrains/acp.json`; `command` is spawned as a subprocess. Documentation recommends absolute paths and supports use without a JetBrains AI subscription. [First-party docs](https://www.jetbrains.com/help/ai-assistant/acp.html) | Provide a separate JetBrains config example; do not assume Zed settings paths or `type` requirements are universal. |
| JetBrains Air | Also supports custom ACP agents via its own configuration-folder `acp.json`. [First-party docs](https://www.jetbrains.com/help/air/select-agents-and-models.html) | Treat Air as a separate profile, not an alias for AI Assistant's global file. |
| Cursor CLI | `agent acp` is an ACP **agent/server** over stdio; its first-party example initializes, authenticates using `cursor_login`, creates a session, then prompts. [First-party docs](https://cursor.com/docs/cli/acp), [Cursor announcement](https://cursor.com/blog/jetbrains-acp) | A valid target under ViberMate for an ACP client such as JetBrains. This is **not evidence** that Cursor's desktop editor can host arbitrary ACP agents. |
| Neovim integrations | OpenCode documents ACP setup for Avante.nvim and CodeCompanion.nvim. [Provider integration docs](https://opencode.ai/docs/acp/) | Useful second-wave client tests; actual plugin versions and first-party plugin configuration should be checked before shipping copy/paste recipes. |

Cursor is proprietary according to its
[registry declaration](https://github.com/agentclientprotocol/registry/blob/39df6a03d94d60a16e2b7c52eb9f768fb40d57ff/cursor/agent.json),
so it is an interoperability target, not an OSS implementation to copy. Registry
membership does not imply an agent is open source. JetBrains also documents an
ACP-in-WSL limitation; do not promise that matrix without separate validation.
([JetBrains limitations](https://www.jetbrains.com/help/ai-assistant/acp.html))

## Protocol requirements relevant to the wrapper

### Stdio and process ownership

Stable v1 specifies UTF-8 JSON-RPC messages delimited by newlines. The client
spawns the agent; stdout must contain protocol messages only, while diagnostics
belong on stderr. Streamable HTTP is still presented as draft in the v1 transport
specification. An SDK shipping HTTP/WebSocket support does not make a particular
network transport a universal editor capability.
([Transport specification](https://agentclientprotocol.com/protocol/v1/transports),
[TS experimental exports](https://github.com/agentclientprotocol/typescript-sdk/blob/5e2cfcabb5303dc93c093da788b68460b9958526/package.json))

ViberMate should therefore preserve both pipe directions and exit status, avoid
a TTY or interactive prompt on the ACP data pipes, and own bounded teardown of
the child/process group. It should not use a normal command's stdout for
“preparing capture,” login hints, or startup progress. These are implementation
requirements derived from the transport contract, rather than an assertion that
all SDKs already implement the desired process policy.

### Initialization, identity, and sessions

Forward `initialize` unchanged. Negotiation is between the editor and actual
agent: a requested supported version is echoed; unsupported versions result in
the agent's supported version, and the client should disconnect when it cannot
support that result. Omitted capabilities mean unsupported. New non-breaking
capabilities do not imply a protocol-major increment.
([Initialization specification](https://agentclientprotocol.com/protocol/v1/initialization))

`session/new` supplies the session's working directory and MCP servers and returns
an agent-generated session ID. A single connection may carry multiple
conversations. `session/load` is capability-gated and can replay history as
`session/update` notifications. Consequently, process cwd, capture/run ID, ACP
session ID, ViberMate Runtime User, and downstream provider account are different
identities; no protocol requirement calls for a temporary user account per
session.
([Session specification](https://agentclientprotocol.com/protocol/v1/session-setup))

For ViberMate, keep one explicit process/run identity and observe ACP session
identities only where supported. Do not attribute each replayed history item as
new model usage, or label a reported `agentInfo.version` as cryptographically
verified. This is an application-model recommendation.

### Authentication and permission decisions

Agent authentication is negotiated in `authMethods`; the default `agent` type
uses the agent's protocol-driven flow, while `terminal` is a different lifecycle.
For terminal auth, current normative docs say the client reproduces the configured
program/base invocation, appends method arguments, applies method environment,
runs a separate interactive process, and reconnects after success. A terminal
method must not be sent as `authenticate`.
([Authentication specification](https://agentclientprotocol.com/protocol/v1/authentication))

**Document conflict found:** the registry's checked-in
[AUTHENTICATION.md](https://github.com/agentclientprotocol/registry/blob/39df6a03d94d60a16e2b7c52eb9f768fb40d57ff/AUTHENTICATION.md)
still describes replacing the base arguments instead of appending them. Follow
the normative specification when designing, but explicitly test actual supported
editor versions. A wrapper cannot safely advertise terminal authentication unless
it can reproduce the correct downstream login invocation under those clients.

`session/request_permission` is an agent-to-client request, not a one-way log
notification. The client chooses an offered option or returns cancellation;
cancelling a prompt requires outstanding permission requests to receive the
cancelled outcome. Do not silently approve these requests merely because a local
ViberMate launch itself was explicitly authorized.
([Tool permission lifecycle](https://agentclientprotocol.com/protocol/v1/tool-calls))

Likewise, `fs/*` and `terminal/*` are capability-gated client operations. A relay
must forward them to the editor rather than accidentally handling them in the
runtime server's own filesystem or shell.
([Terminal specification](https://agentclientprotocol.com/protocol/v1/terminals),
[File system specification](https://agentclientprotocol.com/protocol/v1/file-system))

### Unknown fields, extensions, and cancellation

ACP reserves `_meta` for extension metadata and underscore-prefixed methods for
custom methods. Preserve unknown fields, IDs, notifications, and bidirectional
requests, including features newer than ViberMate's observer. Do not turn a
transparent transport into a restrictive protocol translator.
([Extensibility](https://agentclientprotocol.com/protocol/v1/extensibility))

A concrete interoperability caveat is Cursor's documented vendor methods:
`cursor/ask_question` and `cursor/create_plan` are **blocking requests**, while
`cursor/update_todos`, `cursor/task`, and `cursor/generate_image` are notifications.
Their names do not follow the underscore convention above. The wrapper should
still pass them through unchanged; a schema/name allowlist that drops the
blocking requests can leave the agent waiting forever. Their UI handling remains
the upstream editor's responsibility.
([Cursor extension methods](https://cursor.com/docs/cli/acp#cursor-extension-methods))

The Rust conductor specifically preserves original raw parameters when a typed
request is unchanged, to avoid dropping extensions during v1/v2 interpretation.
Its documentation also warns that cancellation IDs are hop-local when a proxy
allocates new IDs. A byte relay that never changes IDs can forward them as-is;
a typed terminating proxy must maintain explicit request/cancellation mappings.
([Conductor](https://github.com/agentclientprotocol/rust-sdk/blob/726c5030bfaa88cfdac2fb1f71a63abb331ce586/md/conductor.md),
[Cancellation mapping](https://github.com/agentclientprotocol/rust-sdk/blob/726c5030bfaa88cfdac2fb1f71a63abb331ce586/md/request-cancellation.md))

## Security and quality signals

No third-party project was installed or executed for this survey. Source trees,
release metadata, public CI results, and published advisory indexes were inspected.
The following are **observations**, not ViberMate acceptance-test results:

| Project | Concrete quality signal observed | Scope/caveat |
| --- | --- | --- |
| Coder Go SDK | [CI green at 0845a3b](https://github.com/coder/acp-go-sdk/actions/runs/26826178365); cancellation/barrier/golden tests | Old schema pin; no claim of current optional-feature completeness or our own test execution |
| Official TypeScript SDK | [CI green at 5e2cfca](https://github.com/agentclientprotocol/typescript-sdk/actions/runs/33533262102); generation checks, lint/build/tests | v2 and network APIs explicitly experimental |
| Official Rust SDK | [Security audit green at 726c503](https://github.com/agentclientprotocol/rust-sdk/actions/runs/33895770071); [CI matrix](https://github.com/agentclientprotocol/rust-sdk/blob/726c5030bfaa88cfdac2fb1f71a63abb331ce586/.github/workflows/ci.yml), feature-combination tests, daily [cargo-deny](https://github.com/agentclientprotocol/rust-sdk/blob/726c5030bfaa88cfdac2fb1f71a63abb331ce586/.github/workflows/deny.yml) | Provisional proxy extensions must not be confused with stable wire requirements |
| Active Codex adapter | [CI](https://github.com/agentclientprotocol/codex-acp/actions/runs/34024244229) and [E2E](https://github.com/agentclientprotocol/codex-acp/actions/runs/34024244234) green at 1a3c01e; tests cover auth/session/permission/process-exit behavior | E2E belongs to upstream's environment; not proof of ViberMate integration |
| Claude adapter | [CI green at 3e23c5b](https://github.com/agentclientprotocol/claude-agent-acp/actions/runs/33972270931); [pinned ACP and Claude SDK dependencies](https://github.com/agentclientprotocol/claude-agent-acp/blob/3e23c5b960b66a6d2c892e7524c952e731c076a7/package.json) | Upstream agent/service terms and auth lifecycle remain separate |
| acpx | [CI green at d1ec6e8](https://github.com/openclaw/acpx/actions/runs/34008867642); [20-case draft conformance profile](https://github.com/openclaw/acpx/blob/d1ec6e87b72f649f54e9288903d05cca1a9b776c/conformance/README.md) | Not an official certification suite; real-adapter nightly mode is optional and can be non-blocking |

The public repository advisory endpoints returned empty lists for
[Coder SDK](https://api.github.com/repos/coder/acp-go-sdk/security-advisories),
[TypeScript SDK](https://api.github.com/repos/agentclientprotocol/typescript-sdk/security-advisories),
[Rust SDK](https://api.github.com/repos/agentclientprotocol/rust-sdk/security-advisories),
[Codex adapter](https://api.github.com/repos/agentclientprotocol/codex-acp/security-advisories),
[Claude adapter](https://api.github.com/repos/agentclientprotocol/claude-agent-acp/security-advisories),
and [acpx](https://api.github.com/repos/openclaw/acpx/security-advisories) at the
snapshot. OSV package-version queries also returned no entries for Coder v0.13.5,
TS 1.4.0, Rust `agent-client-protocol` 2.1.0, Codex adapter 1.10.0, and Claude
adapter 0.75.1, using the [OSV query API](https://google.github.io/osv.dev/api/).
This is **not** a transitive dependency audit, proof of absence of vulnerabilities,
or visibility into unpublished advisories. Recheck after selecting exact
artifacts and lockfiles.

Do not copy acpx's trust assumptions unchanged into a team service. Its
[security policy](https://github.com/openclaw/acpx/blob/d1ec6e87b72f649f54e9288903d05cca1a9b776c/SECURITY.md)
explicitly assumes a trusted local machine/user, stores session/config state
locally, and runs agents with that user's privileges. ViberMate must retain its
own Runtime User authorization and workspace/network boundaries. Do not log auth
frames, environment values, full malformed messages, or private conversation
content merely to diagnose ACP startup.

The registry validates advertised authentication methods and updates versions
automatically; it is a distribution aid, not a security review or complete
protocol-conformance guarantee. Pin reviewed package versions/artifact hashes
for repeatable release tests rather than automatically running `npx ...@latest`
when the user opens an editor.
([Registry scope](https://github.com/agentclientprotocol/registry/blob/39df6a03d94d60a16e2b7c52eb9f768fb40d57ff/README.md))

## ViberMate implementation acceptance checklist

These are proposed release gates, not claims already verified by this research:

- Editor launches the wrapper directly with an absolute executable path; paths
  and arguments containing spaces remain separate argv entries, without shell
  interpolation or dependence on the editor inheriting an interactive shell.
- Both directions remain byte-identical through the wrapper, including unknown
  fields, Unicode, large frames, IDs, permission requests, cancellation, and
  extension messages. Observation/retention limits do not truncate forwarded
  traffic or leak bodies into error logs.
- No human-facing text is written to ACP stdout. Child stderr is handled with a
  documented privacy policy; startup failure is actionable without corrupting
  the JSON-RPC stream.
- Parent EOF, child exit, runtime rejection, broken pipes, and cancellation have
  bounded teardown with no orphan child/process group. Backpressure does not
  deadlock either direction.
- Existing owner/member login and managed-run authorization apply. An ACP session
  is not a new human account. Local explicit launch does not silently auto-approve
  the downstream agent's file/tool permission requests.
- `session/new.cwd` and any additional workspace roots are attributed separately
  from the wrapper's launch cwd; multi-session and history replay behavior do not
  double-count model/API usage.
- Login-required and terminal-auth flows are exercised in each supported editor,
  including the documented base-argument discrepancy. Agent/provider credentials
  are neither replaced by an ACP session ID nor persisted in ViberMate logs.
- Content recording disabled still allows routing/scripts/agent behavior to run;
  an ACP observer must not create a second, undeclared transcript store.
- Acceptance covers pinned Zed + JetBrains profiles with active Codex/Claude
  adapters, then native Cursor/Gemini/OpenCode targets as explicitly supported.
  Separate transport compatibility from successful provider traffic inspection:
  an agent speaking ACP does not automatically make all its network protocols
  compatible with ViberMate's existing capture adapters.

## Research limits and decisions left open

The survey supports a first **local stdio** implementation and concrete reuse
choices. It does not establish remote ACP multi-tenancy, WebSocket/HTTP transport
security, Cursor-desktop custom-agent hosting, every editor release's terminal
authentication behavior, or compatibility with every provider's network path.
Those require scoped implementation tests, not broader marketing claims.

No third-party source was copied into ViberMate by this research. The only
repository change is this note; source clones were kept in a temporary research
directory. If a later implementation imports an SDK or fixture corpus, record
its exact version, license/notices, security scan, and interoperability test
results in the normal dependency/release workflow.
