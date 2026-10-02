# ViberMate for Paseo

Choose a ViberMate traffic profile for each Claude or Codex conversation in Paseo.
The same React Native picker runs in desktop, web, iOS and Android clients.
This open-source plugin lives in the ViberMate repository; no marketplace submission is required.

The choice is saved before the Agent starts and is keyed by Paseo `agentId`.
Conversations can use different profiles and resume or refresh without another choice.
A binding retains its ViberMate server: connection-setting changes affect new bindings.
"Profile" here means a ViberMate Environment; routes, accounts and model mappings remain in ViberMate.

Blank composer drafts have no provider session. The picker opens when a draft starts its
first interactive session, usually on the first submitted prompt. Existing conversations
without a binding are asked once on their next opening. History-only reads do not ask.

## Requirements

- Paseo `0.11.0-beta.3`; declared range `>=0.11.0-beta.3 <0.12.0`.
- A macOS or Linux daemon host with ViberMate CLI and Claude Code or Codex installed.
- ViberMate 0.1.23 or newer, with `vibermate profiles --json` and
  `GET /api/v1/capture-environments`.
- For a local Mac connection, open ViberMate. For a remote server, sign in on the **Paseo host**:

  ```sh
  vibermate login --server https://runtime.example.com:443
  vibermate profiles --server https://runtime.example.com:443 --json
  ```

Members see only active, published Environments they may launch. The plugin does not need
an owner token or send login/proxy credentials to phone clients.

## Install

For source testing, build the CLI:

```sh
go build -o /absolute/path/to/vibermate ./cmd/vibermate
```

Enable plugins in **Settings → Plugins** on the target host. Install the directory through
**Plugin source** or from its terminal:

```sh
paseo plugin install /absolute/path/to/vibermate/integrations/paseo
```

Users can also install from GitHub:

```sh
paseo plugin install github:vibe-agi/vibermate:integrations/paseo
```

The manifest prepares two executable launchers. No runtime npm dependency or additional
native phone application is needed.

## Configure

Find the installed directory on the daemon host in `plugins.vibermate.path` in
`$PASEO_HOME/config.json`. A directory install uses your source directory. In the same file,
merge these command fields into the existing `agents.providers` settings:

```json
{
  "agents": {
    "providers": {
      "claude": { "command": ["/absolute/plugin/directory/bin/claude"] },
      "codex": { "command": ["/absolute/plugin/directory/bin/codex"] }
    }
  }
}
```

Preserve existing provider models, options and environment; run `paseo reload` after editing.
If a real CLI is not on PATH, set `VIBERMATE_PASEO_CLAUDE_BINARY` or
`VIBERMATE_PASEO_CODEX_BINARY` in that provider's `env` to its real absolute executable.
Never put the wrappers themselves on PATH as `claude` or `codex`, or use shell aliases.

Open **Settings → ViberMate** on any connected desktop or phone:

1. Set the ViberMate CLI path, preferably absolute for a GUI-hosted daemon.
2. Leave **Server** empty for the local Mac app, or enter the origin used for remote CLI login.
3. Optionally set the binding directory. It defaults to
   `$PASEO_HOME/plugin-data/vibermate/bindings`, falling back to `~/.paseo`.
   Use distinct directories for multiple daemons when `PASEO_HOME` is not exposed to plugins.
4. Enable conversation profile selection, then **Save and check**.

Each Agent process invokes `vibermate run --env <selected-id> -- <real-cli> ...`.
Its cwd comes from `PASEO_AGENT_CWD`. Existing ViberMate client verification, CA trust,
proxy, Git attribution, runtime login, remote relay and supervision continue to apply.
Version/availability/auth probes without a conversation ID pass through directly.

## Current boundaries

- Paseo bounds before hooks at 30 seconds. Selection allows 20 seconds, reserving time for
  catalog I/O and cleanup. Cancel, timeout, aborted hooks or failed storage stop startup;
  no default profile is selected. Retry after a timeout. Longer waits need a Paseo API change.
- Pending selections are visible to connected clients on the host. Paseo's hook does not
  identify the initiating device. The first confirmation wins; another client cannot replace it.
  Concurrent conversations have independent decisions.
- Built-in Claude and Codex session families are covered. ViberMate's separate compatibility
  contract still governs native client versions, platforms and install shapes. An unrecognized
  binary does not acquire CA trust just because this plugin launched it. Utility generations
  without a conversation identity are outside the per-conversation capture boundary.
- A disabled/deleted Environment stops future launches without choosing another profile.
  Switching the profile of a live conversation is not offered.
- Bindings store only agent ID, Environment ID and server origin in atomic host-local files.
  Keep the directory through reinstalls; archived conversations retain their choice for resume.
- When disabling/removing the plugin, restore the original provider commands. Wrappers reject
  interactive sessions lacking a choice rather than allowing uncaptured task traffic.
- Deterministic tests and SDK checks are separate from live desktop or physical iOS/Android
  acceptance. Source compatibility does not claim a completed device acceptance run.

## Development

```sh
cd integrations/paseo
npm ci --ignore-scripts
npm run typecheck
npm test
```

Tests use synthetic catalogs and fake CLIs. They cover concurrent isolation, persistence/restart,
cancel/timeout, competing decisions, failed storage, production resume hooks, argv/cwd preservation,
catalog validation, mobile entry navigation and cleanup. Typechecking uses the published
`@getpaseo/plugin@0.11.0-beta.3` SDK. Both entries also compile with that release's actual
Paseo plugin compiler, including its runtime-boundary checks and Hermes async transform.
The package has its own version and AGPLv3 license.
