# Cursor ACP: host integration and ViberMate boundaries

Date: 2026-09-07, Asia/Singapore. Primary documentation and public source were
inspected around 02:40–03:00 UTC. This is **documentation/source verification,
not a successful live Cursor/Zed/JetBrains interoperability test**. No third-party
agent was installed or executed; no credentials or model requests were used.

## Which side is Cursor?

The established integration is **an editor hosting Cursor's CLI agent**. Cursor's
JetBrains documentation explicitly assigns JetBrains the ACP client role and
Cursor the agent/server role; its March 2026 announcement describes bringing
Cursor into JetBrains. A paid Cursor plan is required for that integration, and
usage follows the Cursor subscription—not an independently configured local
Codex or Claude account.
([Cursor integration guide](https://cursor.com/docs/integrations/jetbrains),
[Cursor announcement](https://cursor.com/blog/jetbrains-acp))

Zed likewise documents Cursor as an External Agent, explicitly separating this
from Zed's LLM-provider configuration.
([Pinned Zed documentation](https://github.com/zed-industries/zed/blob/1870e269ad88802147f2baec3086abb67d17260a/docs/src/ai/external-agents.md#cursor))

The inspected Cursor documentation does **not establish that the Cursor desktop
editor itself can host arbitrary custom ACP subprocesses**. Its documentation
index places ACP under CLI; desktop customization documents plugins, skills,
subagents, hooks, and MCP. This is an evidence limit, not proof that a third-party
extension or undocumented integration is impossible. Plugin “agents” and MCP
configuration must not be assumed to be an external ACP host configuration.
([Documentation index](https://cursor.com/llms.txt),
[Plugin scope](https://cursor.com/docs/plugins),
[CLI ACP interface](https://cursor.com/docs/cli/acp))

Recommended ViberMate topology, **design inference**:

```text
Zed / JetBrains ACP UI
    ↕ ACP stdin/stdout
ViberMate managed stdio launcher
    ↕ unchanged ACP stdin/stdout
Cursor CLI agent
    ↕ Cursor's own network protocol and account
Cursor services
```

“I use Cursor” therefore needs one distinction: Cursor desktop, Cursor CLI in a
terminal, or Cursor CLI hosted through ACP in another editor. They are not
interchangeable integration targets.

## Native executable and distribution

At registry commit `50f99cde7470920f24b8aec9c195f25281675ae9`, Cursor is a
**native ACP agent**, not an open-source adapter requiring another ACP wrapper.
Its manifest names Cursor as author, version `2026.09.02`, license
`proprietary`, and Cursor's terms as the license URL. The package archives are
published by `downloads.cursor.com`, under release `2026.09.02-c22c1a3`.
([Pinned manifest](https://github.com/agentclientprotocol/registry/blob/50f99cde7470920f24b8aec9c195f25281675ae9/cursor/agent.json))

| Registry platforms | Executable relative to extracted archive | Arguments |
| --- | --- | --- |
| macOS / Linux, arm64 and x86-64 | `./dist-package/cursor-agent` | `["acp"]` |
| Windows, arm64 and x86-64 | `./dist-package\cursor-agent.cmd` | `["acp"]` |

The same manifest supplies no environment overrides or SHA-256 field. That
observation does not establish whether other distribution layers verify
signatures. The registry's own open-source license does not license Cursor's
binary. Do not silently rebundle or replace the vendor-managed executable.
([Manifest evidence](https://github.com/agentclientprotocol/registry/blob/50f99cde7470920f24b8aec9c195f25281675ae9/cursor/agent.json))

The CLI documentation uses `agent acp`; the registry invokes `cursor-agent acp`.
Resolve the actual executable for GUI configuration instead of assuming an
interactive-shell alias or a registry cache location is stable.
([CLI invocation](https://cursor.com/docs/cli/acp),
[Registry invocation](https://github.com/agentclientprotocol/registry/blob/50f99cde7470920f24b8aec9c195f25281675ae9/cursor/agent.json))

## Host configuration and update ownership

These are **native-agent configuration examples**, not tested ViberMate setup
instructions. Replace the executable placeholder with a real absolute path.
Keep an existing vendor installation intact while validating a separate managed
entry.

Zed custom entry, using its documented `agent_servers` schema:

```json
{
  "agent_servers": {
    "cursor-local": {
      "type": "custom",
      "command": "/absolute/path/to/cursor-agent",
      "args": ["acp"],
      "env": {}
    }
  }
}
```

Zed also offers registry installation. A custom command is a different launch
path, not evidence that the registry's updater will manage that command.
([Zed configuration](https://zed.dev/docs/ai/external-agents#custom-agents),
[Pinned custom/registry implementations](https://github.com/zed-industries/zed/blob/1870e269ad88802147f2baec3086abb67d17260a/crates/project/src/agent_server_store.rs))

JetBrains custom entry in `~/.jetbrains/acp.json`:

```json
{
  "agent_servers": {
    "Cursor local": {
      "command": "/absolute/path/to/cursor-agent",
      "args": ["acp"],
      "env": {}
    }
  }
}
```

AI Assistant documents subprocess execution, argument arrays, and process
environment variables. Registry agents are installed and updated through
Settings → Tools → AI Assistant → Agents; an available update receives a blue
indicator. Organization policy can restrict custom agents or registry entries.
ACP does not require a JetBrains AI subscription, but the agent's own plan still
applies. Current documentation excludes ACP in WSL. These statements do not
certify all older IDE builds or JetBrains Air's separate configuration.
([JetBrains ACP guide](https://www.jetbrains.com/help/ai-assistant/acp.html))

ViberMate recommendation: show the editor configuration it will add, the resolved
vendor executable, and who updates each component. Do not overwrite an existing
entry or inject credentials into shared settings without explicit authorization.

## Zed source: verified implementation details

Source pin: `1870e269ad88802147f2baec3086abb67d17260a`.

- **Spawn:** local ACP processes use the project's first ordered root as launch
  cwd, receive the assembled environment, and have separate piped stdin, stdout,
  and stderr. Remote projects take a different command-building path.
  ([`stdio`](https://github.com/zed-industries/zed/blob/1870e269ad88802147f2baec3086abb67d17260a/crates/agent_servers/src/acp.rs#L806))
- **Environment precedence differs:** custom-agent assembly merges project
  environment, then configured environment, then Zed's extra environment.
  Binary registry assembly merges project environment, manifest environment,
  Zed extras, then user settings. Zed extras include proxy configuration; custom
  proxy settings therefore cannot be assumed to win before ViberMate starts.
  ([Custom assembly](https://github.com/zed-industries/zed/blob/1870e269ad88802147f2baec3086abb67d17260a/crates/project/src/agent_server_store.rs#L1475),
  [Registry assembly](https://github.com/zed-industries/zed/blob/1870e269ad88802147f2baec3086abb67d17260a/crates/project/src/agent_server_store.rs#L1151),
  [Extra environment](https://github.com/zed-industries/zed/blob/1870e269ad88802147f2baec3086abb67d17260a/crates/agent_servers/src/custom.rs#L225))
- **Agent-ID-dependent capability:** only ID `cursor` receives
  `_meta.parameterizedModelPicker = true`. A separately named custom entry such
  as `cursor-local` or `vibermate-cursor` does not receive that flag. Tests
  explicitly cover both cases. Consequently, unchanged stdio forwarding alone
  does not prove complete model-picker/UI parity. Do not fabricate a capability
  that the host did not advertise.
  ([Capability selection](https://github.com/zed-industries/zed/blob/1870e269ad88802147f2baec3086abb67d17260a/crates/agent_servers/src/acp.rs#L766),
  [Tests](https://github.com/zed-industries/zed/blob/1870e269ad88802147f2baec3086abb67d17260a/crates/agent_servers/src/acp.rs#L2970))
- **Registry command override:** the inspected `Registry` settings variant has
  environment/mode/config-option fields but no command field; `Custom` carries
  the command. A managed command retaining ID `cursor` would need a separately
  verified custom-entry strategy, not an assumed registry command override.
  Replacing the existing Cursor entry is not authorized by this research.
  ([Settings variants](https://github.com/zed-industries/zed/blob/1870e269ad88802147f2baec3086abb67d17260a/crates/project/src/agent_server_store.rs#L1533))
- **Terminal authentication:** the first-class terminal-auth branch is
  `AcpBetaFeatureFlag`-gated and obtains a command with the auth method's extra
  arguments/environment; custom and binary registry builders append extra
  arguments. Legacy metadata authentication remains another path. This is
  observed Zed behavior, not evidence that Cursor advertises terminal auth in
  every version. Wrapper login handling must be separately tested.
  ([Auth dispatch](https://github.com/zed-industries/zed/blob/1870e269ad88802147f2baec3086abb67d17260a/crates/agent_servers/src/acp.rs#L1852),
  [Registry argument append](https://github.com/zed-industries/zed/blob/1870e269ad88802147f2baec3086abb67d17260a/crates/project/src/agent_server_store.rs#L1317))

Zed's documented Cursor/Gemini limitation in the parallel-agents page concerns
**thread import**, not blanket ACP support or every possible session-load flow.
([Thread import scope](https://zed.dev/docs/ai/parallel-agents))

## Authentication, workspace and protocol compatibility

Cursor documents an initialize → `authenticate(cursor_login)` → session
new/load → prompt lifecycle. ACP uses JSON-RPC over newline-delimited stdio.
Its blocking `cursor/ask_question` and `cursor/create_plan` methods require
client responses; `cursor/update_todos`, `cursor/task`, and
`cursor/generate_image` are documented notifications. Its ACP mode supports
user/project `.cursor/mcp.json`, requires launching from the project directory
for project MCP, and excludes dashboard-managed team MCP. Preserve these
messages and configuration boundaries; do not mistake the vendor's tutorial
auto-approval for a production permission policy.
([Cursor ACP contract](https://cursor.com/docs/cli/acp))

For individuals, prefer Cursor's browser-based `agent login` and its locally
stored authentication. Automation may use `CURSOR_API_KEY`; no ViberMate account
should require sharing another person's Cursor credential. `NO_OPEN_BROWSER=1`
supports manually opening the login URL. Authentication method and secret
storage remain Cursor responsibilities.
([Cursor authentication](https://cursor.com/docs/cli/reference/authentication))

Preserve existing `HOME`, `PATH`, `CURSOR_CONFIG_DIR`, and `XDG_CONFIG_HOME`
semantics. Cursor documents the latter two as configuration-location overrides;
project `.cursor/cli.json` supports only permissions, while other CLI settings
are global. A deliberately isolated config directory needs its own correctly
initialized authentication/configuration; do not silently point it at an empty
directory and claim the original login will carry over.
([Configuration locations](https://cursor.com/docs/cli/reference/configuration))

## ACP visibility is not HTTP semantic compatibility

Cursor **does document proxy/CA support**: `HTTP_PROXY`, `HTTPS_PROXY`,
`NODE_USE_ENV_PROXY=1`, and `NODE_EXTRA_CA_CERTS` for inspection. Its global
`network.useHttp1ForAgent` option selects HTTP/1.1 SSE instead of the default
HTTP/2 agent transport. This is a supported configuration surface, not a verified
ViberMate integration result for the pinned Cursor binary.
([CLI proxy configuration](https://cursor.com/docs/cli/reference/configuration#proxy-configuration))

Cursor's network guide describes HTTP/2 bidirectional streaming, Connect-style
health-service requests, multiple agent-service endpoints, and inspection or
certificate-pinning constraints. **Inference:** neither a model named GPT/Claude
nor an endpoint override proves OpenAI Responses/Anthropic Messages wire
compatibility or interchangeable provider accounts. ViberMate must report ACP
launch/stdio success separately from HTTP routing, capture, or account-overwrite
support.
([Cursor network requirements](https://cursor.com/docs/enterprise/network-configuration))

## Remaining acceptance work

The following are required tests/research gaps, not claims of current support:

1. Compare native registry Cursor with a separately named managed custom entry in
   actual Zed and JetBrains builds: initialize metadata, models, login, prompts,
   approval, question/plan UI, cancel, session load, and editor shutdown.
2. Verify effective launch cwd versus each session's cwd, project MCP and config
   selection, host proxy overrides, and resolved executable after vendor updates.
3. Test absent/expired auth without exposing secret values; test any advertised
   terminal-auth method without recursive wrapper invocation or stdout pollution.
4. Validate lossless forwarding of unknown methods, IDs, notifications and large
   messages; bounded backpressure, EOF, stderr, child cleanup, and permission
   rejection must remain correct. A relay must not auto-approve tool requests.
5. Measure real Cursor HTTP behavior behind ViberMate separately, including
   HTTP/2 and optional HTTP/1.1, CA failures and unsupported service protocols.
6. Cursor desktop custom ACP hosting and full JetBrains vendor-extension handling
   remain unverified. JetBrains documentation was inspected, not its internal
   plugin implementation. Logs can contain chat/secrets; do not collect or share
   extended logs by default.
   ([JetBrains logging warning](https://www.jetbrains.com/help/ai-assistant/acp.html#collect-acp-logs))

This note intentionally does not install Cursor, alter editor settings, change
accounts, or certify a release. It narrows the implementation and validation
boundary before such actions.
