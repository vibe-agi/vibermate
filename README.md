# ViberMate

[English](README.md) · [简体中文](README.zh-CN.md) · [Website](https://vibe-agi.github.io/products/vibermate/)

**See and steer your AI coding agent.**

Claude Code and Codex talk to an AI service over the network, and normally you
cannot see those messages or decide where they go. ViberMate sits in the
middle, on your Mac or on a server you run. You keep using your agent exactly
as before; ViberMate records each conversation and lets you decide what
happens to it.

![A Claude Code session in ViberMate, turn by turn](https://vibe-agi.github.io/images/vibermate/conversation-en-2400.webp)

## What it does

- **See** user input and Agent replies first. Open tool calls, system context
  or raw HTTP beside the relevant message without losing your reading position.
- **Steer** each request to the upstream service and account you choose, and
  switch without editing config files.
- **Approve** before your agent reaches a site your policy hasn't decided.
- **Measure** requests, tokens and estimated cost by project, branch, model,
  account or person, with compact request-level details inside conversations.
- **Share** one server with your team. Everyone signs in with their own
  account, and the owner decides who may use which policies.

![Usage overview with requests, tokens and estimated cost](https://vibe-agi.github.io/images/vibermate/usage-en-2400.webp)

The [website](https://vibe-agi.github.io/products/vibermate/) walks through
each of these with screenshots.

## Get started

**macOS 14+ (Apple silicon and Intel)**

```sh
brew install --cask vibe-agi/tap/vibermate
```

Open ViberMate, go to **Settings → Access & launch → Terminal command** and set
up the `vibermate` command. Then, in your project:

```sh
vibermate run -- claude    # or: vibermate run -- codex
```

To use an Environment's model-service account without a native client login,
opt in for each launch. The same option supports Codex and Claude:

```sh
vibermate run --env work -- codex                  # ordinary native login
vibermate run --env work --managed-auth -- codex   # no OpenAI login required
vibermate run --env work --managed-auth -- claude
```

`--managed-auth` requires an explicit Environment with a managed Client Flow
for the supported client. Original-only capture cannot supply authentication.
For Claude, the prepared client destination must remain in the Environment's
managed scope. A custom-base-only destination can be rejected if normal launch
preparation returns Claude to its default service; use a managed
`api.anthropic.com` flow in that case.

Codex uses an internal `vibermate` provider while keeping the same `CODEX_HOME`
and native history. Resume sessions created with this option using the same
option; address an older `openai` session directly by its UUID:

```sh
vibermate run --env work --managed-auth -- codex resume
vibermate run --env work --managed-auth -- codex resume <uuid>
vibermate run --env work --managed-auth -- codex resume <uuid> --model <model>
```

The native picker filters by provider, so older `openai` sessions may not appear;
`--all` only removes the working-directory filter. Use explicit `--model` when
you want a particular resumed model. Codex owns active-session access: wait for
release and press **R** to retry, **F** to fork saved history, or **Esc/Q** to exit.
A fork creates a new ID and may omit unsaved work. If **F** is unavailable, exit
and use `codex fork` with the same ViberMate run options. ViberMate does not
migrate stored history or release another application's session lock. Codex may
update a resumed thread's active settings. Managed model-service authentication
does not provide Codex Cloud or connector OAuth access.

**Linux server (x86-64 and ARM64)**

Download the archive for your machine from the
[latest release](https://github.com/vibe-agi/vibermate/releases/latest),
extract it, and start the server:

```sh
./vibermated server
```

Open <http://127.0.0.1:9666> and create the owner account with the key from
`./vibermated server recovery-key`. To reach the server from other devices,
follow the [deployment guide](docs/deployment.md).

**Have your own public domain?** ViberMate obtains, renews and hot-loads HTTPS
certificates without a separate Caddy installation. Open **Settings → Access &
launch → Deploy with your own domain** for native commands or Docker settings,
or follow the [public HTTPS](docs/deployment.md#automatic-public-https) and
[Docker examples](docs/docker.md#public-domain-automatic-https). Point DNS at
the server and provide direct TCP 443 or layer-4 passthrough; an ordinary HTTP
reverse proxy cannot replace the Agent CONNECT path.

## Learn more

- [Website](https://vibe-agi.github.io/products/vibermate/): what ViberMate
  does, step by step
- [Deployment and HTTPS](docs/deployment.md), [Docker](docs/docker.md),
  [backup and restore](docs/backup-and-restore.md)
- [What is supported today](docs/capability-support.md)
- [Paseo integration](integrations/paseo/README.md): choose a traffic profile per conversation
- [Security policy](SECURITY.md) · [Contributing](CONTRIBUTING.md)

Run `vibermate doctor` if setup fails. For a remote Runtime, use
`vibermate doctor --server <same-server> [--env <environment-id>]` after login.
It verifies control/session/machine and the credential-free CONNECT entry rejection;
optional `--env` checks the authorized active catalog. It creates no Capture and
leaves authorized forwarding, native client login/startup and model inference
explicitly NOT CHECKED. Local doctor checks runtime/storage/root trust where
supported and states the same limits.

## License

ViberMate is licensed under the [GNU AGPLv3](LICENSE). A commercial license,
consulting and support are available; see [COMMERCIAL.md](COMMERCIAL.md).
