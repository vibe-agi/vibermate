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

- **See** every conversation turn by turn: what was asked, what the model
  answered, which tools it used, and the raw HTTP when you need it.
- **Steer** each request to the upstream service and account you choose, and
  switch without editing config files.
- **Approve** before your agent reaches a site your policy hasn't decided.
- **Measure** requests, tokens and estimated cost by project, branch, model,
  account or person.
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
- [Security policy](SECURITY.md) · [Contributing](CONTRIBUTING.md)

Run `vibermate doctor` if setup fails.

## License

ViberMate is licensed under the [GNU AGPLv3](LICENSE). A commercial license,
consulting and support are available; see [COMMERCIAL.md](COMMERCIAL.md).
