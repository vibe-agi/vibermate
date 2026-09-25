# ACP editor setup (current source)

ViberMate v0.1.13 does not contain this command; use matching App/Server and
CLI bytes from the current source until a later release includes it. This is
ACP observation, **not HTTP traffic
capture or routing**: no account overwrite, model mapping, script, network exit,
or tool policy from `vibermate run` is applied. The editor still owns login,
permissions, tools, native history, and provider traffic.

## Local App

Build with `make build-flutter-app`, open `dist/ViberMate.app`, and use the CLI
inside that same bundle while testing. Do not leave a released App running and
assume it supports the current-source CLI. No ViberMate account is required locally.

In Settings -> Access & launch -> Connect an ACP editor, choose the editor and
Agent, enter absolute executable paths, and copy the configuration. Merge only
the new entry; preserve existing editor settings and environment values.

Install one adapter separately. The current isolated acceptance versions are:

```sh
npm install -g @agentclientprotocol/codex-acp@1.13.1
# OR (requires Node 22 or newer)
npm install -g @agentclientprotocol/claude-agent-acp@0.81.2
```

Do not wrap plain `codex` or `claude` and expect them to become ACP Agents. These
adapters perform the actual protocol adaptation; ViberMate does not duplicate it.
Cursor CLI is already an Agent with `agent acp`. This does not establish that the
Cursor desktop editor supports hosting arbitrary external ACP Agents.

Example Zed custom Agent entry:

```json
{
  "agent_servers": {
    "vibermate-codex": {
      "type": "custom",
      "command": "/absolute/path/to/ViberMate.app/Contents/MacOS/vibermate",
      "args": ["acp", "--", "/absolute/path/to/codex-acp"],
      "env": {"PATH": "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"}
    }
  }
}
```

For JetBrains, place the `agent_servers` entry in `~/.jetbrains/acp.json` and omit
`type`. Use your actual Node location in `env.PATH`: npm executables can rely on
`/usr/bin/env node` even when the adapter path itself is absolute. No shell is
inserted, so `~`, `$PATH`, aliases and shell command strings are not expanded.
Keep credentials in the editor/Agent's own supported secret handling rather than
pasting them into a shared example. ViberMate passes the editor's effective env
unchanged; it does not update the user's shell profile or global commands.

Claude uses the same entry with `claude-agent-acp` as the final executable. Cursor
CLI uses `"args": ["acp", "--", "/absolute/path/to/agent", "acp"]`.

For VS Code, install an ACP client that supports custom stdio Agents. The
accepted combination is VS Code 1.139.0 with `formulahendry.acp-client` 0.2.0;
put the same entry under the `acp.agents` setting instead of `agent_servers`.
The ViberMate setup guide generates this shape directly. Client extensions are
separate projects with their own update and trust boundary.

## Remote Runtime

An owner creates a Runtime User using existing User management. On the editor
machine, log in once:

```sh
vibermate login --server https://your-runtime:9666
```

Then use `"args": ["acp", "--server", "https://your-runtime:9666", "--",
"/absolute/path/to/codex-acp"]`. The URL must match the saved login. HTTP is also
supported for an explicitly trusted private network; it does not encrypt account
credentials or observations in transit. Prefer HTTPS. Workspace changes do not
need a new Runtime User. Logout, disablement and session expiry revoke admission.
The owner can inspect ACP evidence in the App/Web workbench; member accounts do
not gain owner access to other Captures.

## Content and meaning of the display

By default the App shows ACP session IDs, claimed directories, self-reported Agent
version, prompt outcomes and tool-call notification counts without user/assistant
text. Enable **Save user and assistant text** in the setup guide, or add
`--record-content` before `--`, then start a new editor connection. This freezes
recording against the Server policy; reconnecting does not alter old evidence.

The observer retains at most 64 sessions, 256 prompts, 128 KiB of visible text,
and a 768 KiB encoded snapshot per connection. Individual observed frames are
bounded to 1 MiB. Limits mark the projection incomplete; forwarding has no frame
size limit. History replay is not a new prompt. Tool inputs/results, thought
chunks, images/resources, auth and permission payloads, raw errors, stderr and
unknown extension contents are forwarded but not stored. The Agent may keep its
own history independently of ViberMate's content setting.

“Agent returned” means a prompt RPC returned, not that every model request
succeeded. ACP counts are not HTTP turn or token counts. Sessions are scoped to
the connection, even when a native ID is reused after a reconnect. Agent version
is self-reported and is not a release-signature assertion.

## Troubleshooting and verified scope

- No connection: open the matching App or log in to the matching remote Server.
  Both the command and Runtime must be from the ACP build.
- For a locally ad-hoc-signed build, start the App normally before running
  direct daemon acceptance tests. This Mac reproduced a Keychain startup failure
  in `flutter_tester` that was absent in the actual App launch path. Do not
  delete credentials or weaken Keychain access to work around a test failure.
- Executable not found: use an absolute path and give npm adapters a Node PATH.
- Agent login required: complete the Agent's auth in the editor. This is separate
  from ViberMate login. Terminal auth preserves a real TTY and appended args/env.
- No Capture when typing the command directly in Terminal: any real-TTY stdin
  invocation is passed through without registration or observation. Configure
  the ACP editor to launch the command with pipes; this also keeps the editor's
  separate interactive authentication process usable.
- No text: metadata-only is the default; loaded history is not newly recorded.
- No HTTP traffic policies: intentional in ACP-only mode. `--env` is rejected so
  a configured policy cannot appear to be enforced when it is not.

The fixed VS Code Extension Host completed synthetic login, new session,
message, tool permission, cancellation, EOF and reconnect through the real
Runtime. Codex ACP 1.13.1 and Claude Agent ACP 0.81.2 also passed their isolated
login/session boundaries without provider credentials. A real paid provider
prompt, Zed/JetBrains human-click login, and arbitrary editor versions remain
outside this evidence. See the [current editor acceptance](evidence/2026-09-26-acp-editor-acceptance.md),
[earlier adapter research](research/2026-09-07-acp-acceptance.md), and
[implementation evidence](plans/2026-09-07-acp-integration.md).
