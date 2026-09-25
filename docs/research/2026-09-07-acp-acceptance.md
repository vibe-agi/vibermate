# ACP adapter acceptance: protocol, authentication, and editor setup

Date: 2026-09-07, Asia/Singapore. This supplements the
[ecosystem survey](2026-09-07-acp-ecosystem.md) and
[Cursor integration note](2026-09-07-cursor-acp-integration.md). Evidence comprises
first-party documentation, published adapter source, and the isolated experiments
below. **These are direct adapter tests, not a completed ViberMate/editor or
authenticated model acceptance test.** No personal account directories or
credentials were mounted or inspected; no editor settings changed. Execution
containers had networking disabled and no model prompt was sent.

## Fixed artifacts and commands

The npm `latest` records still identified these versions at inspection:

| Package pin | Executable, no arguments for ACP | Relevant runtime dependency |
| --- | --- | --- |
| `@agentclientprotocol/codex-acp@1.10.0` | `codex-acp` | Declares `@openai/codex ^0.153.3` and ACP SDK `^1.4.0`; the experiment fixed both to their lower versions. |
| `@agentclientprotocol/claude-agent-acp@0.75.1` | `claude-agent-acp` | Node `>=22`; Claude Agent SDK `0.3.257`, ACP SDK `1.4.0`. |

Do not add an `acp` subcommand to either adapter executable. A fixed top-level
package alone does not fix Codex's transitive dependencies; retain the generated
lockfile and use `npm ci` for repeat installation.
([Codex package manifest](https://github.com/agentclientprotocol/codex-acp/blob/v1.10.0/package.json),
[Claude package manifest](https://github.com/agentclientprotocol/claude-agent-acp/blob/v0.75.1/package.json),
[Codex npm record](https://registry.npmjs.org/@agentclientprotocol/codex-acp/1.10.0),
[Claude npm record](https://registry.npmjs.org/@agentclientprotocol/claude-agent-acp/0.75.1))

The experiment installed with `--ignore-scripts` and retained optional native
dependencies. It used Linux/arm64, Node `v22.23.2`, and image
`node@sha256:8a34c4ab3ea2c5cd194f07e317b2a8f09461d3c8b05c4e34c8ccd56d56024c4d`.
Its temporary files are under `/tmp/vibermate-acp-acceptance.ujhhOE`;
`adapters/package-lock.json` has SHA-256
`cd144b46b4df9b1cecee6f40729167d3e47ee2d677b941e5ac033fa3edf15ebf`.

## Stable v1 wire shapes

Each object below occupies one UTF-8 line on the actual stdio transport. These
are minimal examples, not a claim that all requests should be sent together.
Wait for initialization, then use the returned session ID for prompts. IDs can
also be strings; an observer must match their JSON type and connection direction.
([Transport](https://agentclientprotocol.com/protocol/v1/transports),
[Initialization](https://agentclientprotocol.com/protocol/v1/initialization),
[Published SDK schema](https://github.com/agentclientprotocol/typescript-sdk/blob/v1.4.0/schema/schema.json))

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{},"clientInfo":{"name":"acceptance-client","version":"0.0.0"}}}
{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/workspace","mcpServers":[]}}
{"jsonrpc":"2.0","id":2,"result":{"sessionId":"session-example"}}
{"jsonrpc":"2.0","id":3,"method":"session/load","params":{"sessionId":"session-example","cwd":"/workspace","mcpServers":[]}}
{"jsonrpc":"2.0","id":4,"method":"session/prompt","params":{"sessionId":"session-example","prompt":[{"type":"text","text":"Explain this project."}]}}
{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-example","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Example answer"}}}}
{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn"}}
{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"session-example"}}
```

`session/update` and `session/cancel` are notifications with no `id`. The update
discriminator is `params.update.sessionUpdate`, not a top-level field. Optional
message IDs and unknown extensions must survive forwarding.
([Prompt lifecycle](https://agentclientprotocol.com/protocol/v1/prompt-turn),
[Session update types](https://github.com/agentclientprotocol/typescript-sdk/blob/v1.4.0/src/schema/types.gen.ts#L3642))

The prompt outcome is the response to that prompt's request ID, with
`stopReason` equal to `end_turn`, `max_tokens`, `max_turn_requests`, `refusal`, or
`cancelled`. A JSON-RPC `error` is a separate outcome; process exit and text chunks
do not complete a pending prompt. The published SDK still labels response
`usage` unstable and optional. Codex can additionally report a vendor failure
under `_meta` while returning `end_turn`; a generic observer should display the
reported stop reason without claiming independently verified model success.
([Prompt response schema](https://github.com/agentclientprotocol/typescript-sdk/blob/v1.4.0/src/schema/types.gen.ts#L3190),
[Codex failure response](https://github.com/agentclientprotocol/codex-acp/blob/v1.10.0/src/CodexAcpServer.ts#L3254))

Call `session/load` only when `agentCapabilities.loadSession` is true. Its history
arrives as ordinary `session/update` notifications **before** the load response;
the requested session ID identifies the session. Do not count replayed messages
as new model turns or usage. The session-setup page shows `result:null`, whereas
SDK 1.4.0 defines an object response and both adapters' load implementations
return objects with optional state. Do not require `result.sessionId` on load.
The separately capability-gated `session/resume` restores without history replay.
([Load/replay contract](https://agentclientprotocol.com/protocol/v1/session-setup),
[Load response type](https://github.com/agentclientprotocol/typescript-sdk/blob/v1.4.0/src/schema/types.gen.ts#L2967),
[Codex load implementation](https://github.com/agentclientprotocol/codex-acp/blob/v1.10.0/src/CodexAcpServer.ts#L777),
[Claude load implementation](https://github.com/agentclientprotocol/claude-agent-acp/blob/v0.75.1/src/acp-agent.ts#L2151))

## Observed unauthenticated behavior

All rows used an empty `/workspace` session cwd, `mcpServers:[]`, and a fresh
container. These observations come from actual published npm executables:

| Adapter / client capabilities | Initialize | `session/new` | EOF |
| --- | --- | --- | --- |
| Codex 1.10.0 / `{}` | v1 success; `loadSession:true`; auth methods `api-key`, `chat-gpt` | JSON-RPC error `-32000`, `Authentication required` | Exit 0; no stderr in this run. |
| Claude 0.75.1 / `{}` | v1 success; `loadSession:true`; `authMethods:[]` | Success: UUID `sessionId`, `modes`, `configOptions` | Exit 0; diagnostic stderr, including an `ACP connection closed` rejected-Promise log. |
| Claude 0.75.1 / `{"auth":{"terminal":true}}` | Same identity/capabilities; two terminal auth methods below | Same successful unauthenticated creation | Same EOF behavior. |

Claude also emitted this extension notification while creating the session:

```json
{"jsonrpc":"2.0","method":"_auth/status_update","params":{"authStatus":{"kind":"none","label":"Not logged in"}}}
```

Thus neither successful initialization nor successful session creation proves
authentication. Codex's `checkAuthorization` explicitly raises `authRequired`;
Claude source converts authentication failures during prompts to the same error,
but no Claude prompt was executed here. All observed adapter stdout consisted
of newline-delimited JSON-RPC; stderr remained separate.
([Codex authorization check](https://github.com/agentclientprotocol/codex-acp/blob/v1.10.0/src/CodexAcpServer.ts#L498),
[Claude auth failure handling](https://github.com/agentclientprotocol/claude-agent-acp/blob/v0.75.1/src/acp-agent.ts#L5664),
[Claude auth status extension](https://github.com/agentclientprotocol/claude-agent-acp/blob/v0.75.1/src/auth-status.ts))

## Terminal authentication and TTY

The client advertises `clientCapabilities.auth.terminal:true`. It then runs a
separate interactive process using its configured program and base arguments,
**appends** the method's `args`, and applies method `env` overrides. Exit zero
means terminal-flow success, after which the client reconnects and initializes.
The descriptor has no `command`; do not send terminal methods to `authenticate`.
([Normative terminal auth](https://agentclientprotocol.com/protocol/v1/authentication))

Claude 0.75.1 actually advertised these local-environment descriptors:

```json
{"id":"claude-ai-login","name":"Claude Subscription","type":"terminal","args":["--cli","auth","login","--claudeai"]}
{"id":"console-login","name":"Anthropic Console","type":"terminal","args":["--cli","auth","login","--console"]}
```

Neither descriptor supplied `env`. Source uses `claude-login` with `args:["--cli"]`
for remote conditions including `NO_BROWSER`, SSH, and `CLAUDE_CODE_REMOTE`.
The legacy `_meta["terminal-auth"]` capability is also recognized; its response
metadata carries `command:process.execPath` and the adapter argv. Such a legacy
command can bypass an outer launcher, so metadata preservation alone cannot
establish managed-login coverage.
([Claude initialization](https://github.com/agentclientprotocol/claude-agent-acp/blob/v0.75.1/src/acp-agent.ts#L1964))

Claude's `--cli` branch precedes version handling, removes that flag, and spawns
the native CLI with inherited stdio. A harmless executable supplied through
`CLAUDE_CODE_EXECUTABLE` confirmed the real adapter forwarded
`["auth","login","--claudeai"]`; stdin/stdout/stderr were all non-TTY without
Docker `-it`, and all TTY with `-it`. The probe deliberately exited 23, and the
adapter returned 23 in both cases. This tests delegation, **not actual login**.
([CLI entry point](https://github.com/agentclientprotocol/claude-agent-acp/blob/v0.75.1/src/index.ts),
[Executable resolution](https://github.com/agentclientprotocol/claude-agent-acp/blob/v0.75.1/src/acp-agent.ts))

Codex 1.10.0 does not advertise terminal auth. Its method list includes `api-key`,
normally `chat-gpt`, capability-gated `chat-gpt-device-code`, and optionally
`gateway`. For protocol-driven methods the wire request is
`{"jsonrpc":"2.0","id":5,"method":"authenticate","params":{"methodId":"chat-gpt"}}`.
Codex also has separate `login` and `cli` command branches; they are not evidence
of an advertised terminal method.
([Auth method selection](https://github.com/agentclientprotocol/codex-acp/blob/v1.10.0/src/CodexAuthMethod.ts),
[Codex CLI dispatch](https://github.com/agentclientprotocol/codex-acp/blob/v1.10.0/src/index.ts))

## Editor configuration, documentation verified only

Zed custom agents use `agent_servers.<id>` with `type:"custom"`, `command`, an
argument array, and `env`. JetBrains AI Assistant uses
`~/.jetbrains/acp.json`, also with `agent_servers`, `command`, `args`, and `env`;
its documented entry has no Zed `type` requirement. Use absolute executable
paths. Neither editor was launched or reconfigured during this work.
([Zed custom agents](https://zed.dev/docs/ai/external-agents#custom-agents),
[JetBrains custom agents](https://www.jetbrains.com/help/ai-assistant/acp.html#add-a-custom-agent))

Native direct-agent entry bodies, before applying a ViberMate wrapper:

```json
{"type":"custom","command":"/absolute/prefix/node_modules/.bin/codex-acp","args":[],"env":{}}
{"command":"/absolute/prefix/node_modules/.bin/claude-agent-acp","args":[],"env":{}}
```

Zed's inspected first-class terminal-auth branch is `AcpBetaFeatureFlag`-gated;
custom command builders append auth arguments. Its legacy branch can instead
use the method metadata command. This is source verification, not a promise for
every installed Zed build. JetBrains documentation establishes command/env
configuration but does not expose equivalent implementation evidence here.
([Zed auth dispatch](https://github.com/zed-industries/zed/blob/1870e269ad88802147f2baec3086abb67d17260a/crates/agent_servers/src/acp.rs#L1852),
[Zed command construction](https://github.com/zed-industries/zed/blob/1870e269ad88802147f2baec3086abb67d17260a/crates/project/src/agent_server_store.rs#L1475))

## Reproduce the bounded smoke test

Install into a newly created task directory; this does not mount an account
directory or inherit host provider environment variables:

```sh
acp_acceptance_dir="$(mktemp -d)"
acp_acceptance_image='node@sha256:8a34c4ab3ea2c5cd194f07e317b2a8f09461d3c8b05c4e34c8ccd56d56024c4d'
docker run --rm --cap-drop=ALL --security-opt no-new-privileges \
  --pids-limit=256 --mount "type=bind,src=$acp_acceptance_dir,dst=/work" \
  --workdir /work "$acp_acceptance_image" \
  npm install --prefix /work/adapters --save-exact --ignore-scripts \
  --no-audit --no-fund @agentclientprotocol/codex-acp@1.10.0 \
  @agentclientprotocol/claude-agent-acp@0.75.1 \
  @openai/codex@0.153.3 @agentclientprotocol/sdk@1.4.0
```

Run the following for each adapter; switch the argument after `-` to `codex-acp`,
or remove `terminal` for the empty-capability Claude case. The driver sends only
initialize and new, closes stdin after the new response, and has a timeout.

```sh
docker run --rm -i --network=none --cap-drop=ALL \
  --security-opt no-new-privileges --pids-limit=256 \
  --mount "type=bind,src=$acp_acceptance_dir,dst=/work,readonly" \
  --workdir /work "$acp_acceptance_image" \
  node --input-type=module - claude-agent-acp terminal <<'JS'
import { spawn } from 'node:child_process';
import { mkdirSync } from 'node:fs';
mkdirSync('/workspace');
const name = process.argv[2];
const caps = process.argv[3] === 'terminal' ? {auth:{terminal:true}} : {};
const child = spawn(`/work/adapters/node_modules/.bin/${name}`, [], {
  cwd:'/workspace', stdio:['pipe','pipe','pipe']
});
child.stderr.pipe(process.stderr);
const send = (id, method, params) => child.stdin.write(
  JSON.stringify({jsonrpc:'2.0', id, method, params}) + '\n'
);
let buffer = '';
const timer = setTimeout(() => { child.kill('SIGKILL'); process.exitCode = 1; }, 20000);
child.stdout.on('data', bytes => {
  buffer += bytes;
  for (;;) {
    const end = buffer.indexOf('\n');
    if (end < 0) break;
    const line = buffer.slice(0, end); buffer = buffer.slice(end + 1);
    if (!line.trim()) continue;
    console.log(line);
    const message = JSON.parse(line);
    if (message.id === 1) send(2, 'session/new', {cwd:'/workspace', mcpServers:[]});
    if (message.id === 2) child.stdin.end();
  }
});
child.on('error', error => { console.error(error); process.exitCode = 1; });
child.on('exit', (code, signal) => { clearTimeout(timer); console.error({code, signal}); });
send(1, 'initialize', {protocolVersion:1, clientCapabilities:caps,
  clientInfo:{name:'vibermate-isolated-acceptance', version:'0.0.0'}});
JS
```

The executed, more defensive driver is `acceptance-sources-smoke.mjs` in the
recorded temporary directory. The TTY probe there is `acceptance-tty-probe.mjs`.
To recreate the probe, save the following as an executable file in the task
directory:

```js
#!/usr/bin/env node
console.log(JSON.stringify({args:process.argv.slice(2),
  stdinTTY:!!process.stdin.isTTY, stdoutTTY:!!process.stdout.isTTY,
  stderrTTY:!!process.stderr.isTTY}));
process.exit(23);
```

Set only the container's
`CLAUDE_CODE_EXECUTABLE=/work/acceptance-tty-probe.mjs` and launch
`claude-agent-acp --cli auth login --claudeai`, once with and once without `-it`.

Remaining acceptance work: run the same direct/wrapped comparison through the
finished ViberMate runtime; test login in actual editor builds with an explicitly
authorized account; test prompts, permission rejection, cancellation, load/replay,
and shutdown; assess HTTP capture independently. The present evidence certifies
none of those unexecuted paths.

## Registered-wrapper follow-up (2026-09-07)

After the launcher, Runtime persistence and management projection were composed,
`TestACPPublishedAdaptersThroughRegisteredWrapper` was cross-compiled for Linux
ARM64 and run with the same pinned adapters in the image above. The container
used `--init --network none`, a read-only mount of the task-owned adapter/test
directory, and fresh temporary homes without provider credentials. Both adapters
passed through the actual `runlauncher.Launcher.RunACP` and DesktopHost:

- Codex ACP 1.10.0: wire-v1 initialization, unauthenticated `session/new` error
  -32000, editor EOF, exit zero and persisted final Agent version.
- Claude ACP 0.75.1: wire-v1 initialization with terminal-auth capability, native
  session creation, editor EOF, exit zero and persisted session/version.

The test is opt-in via `VIBERMATE_ACP_ACCEPTANCE_ADAPTERS`; it never issues a
prompt to these real adapters. Separate actual-child fixtures exercise the
prompt/permission loop and metadata/full recording through both local and remote
Runtime compositions. This advances wrapper acceptance, not live provider or GUI
editor certification. See the [current milestone](../plans/2026-09-07-acp-integration.md)
for the remaining explicit acceptance boundary.
