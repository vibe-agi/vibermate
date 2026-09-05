# ViberMate

[English](README.md) · [简体中文](README.zh-CN.md) · [Website](https://vibe-agi.github.io/products/vibermate/)

**See and control the network boundary around Claude Code and Codex CLI.**

ViberMate captures agent conversations, routes requests, applies small
JavaScript rules, and keeps an auditable record. It does not replace your agent
or AI provider.

![ViberMate capture timeline](https://vibe-agi.github.io/images/vibermate/capture-timeline-2400.webp)

## Choose how to run it

| Experience | Best for | Runs on |
| --- | --- | --- |
| **macOS App** | A complete local workbench with the Runtime included | macOS 14+ (Apple silicon and Intel) |
| **Runtime Server + Web** | A browser-managed Runtime used by one or many people | Linux x86-64 and ARM64 |
| **`vibermate` command** | Starting Claude or Codex through either Runtime | macOS and Linux |

There is no separate “team edition.” One Runtime already supports multiple
Runtime Users, isolated login sessions, per-user captures, and a shared admin
view. Use it alone or create an account for each person or device.

| Who is using it | Sign-in experience |
| --- | --- |
| Local macOS App | No sign-in; the App controls its own local Runtime |
| Server owner in a browser | Personal username and password; full workbench |
| Team member in a browser | Personal username and password; own usage and password |
| Claude or Codex from Terminal | The same personal username and password, entered once |

ViberMate has no shared or default `admin/admin` credential. Login tokens are
short-lived implementation details and are not something people need to copy.

## macOS App: first capture

Install and open ViberMate:

```sh
brew install --cask vibe-agi/tap/vibermate
```

In **Settings → Access & launch → Terminal command**, choose **Set up command**. Then,
from your project directory:

```sh
vibermate run -- claude
# or
vibermate run -- codex
```

Return to the App to inspect the new Capture. You can start without configuring
a Traffic Policy; transparent capture preserves the agent's existing provider,
account, and model.

You do not need an account for normal App use. To open this Runtime in a browser
or share it, go to **Settings → Access & launch**, choose **Create owner**, then copy
the Web workbench address. The first account is the owner; later accounts are
members.

## Linux Server + Web

Download the `linux_x86_64` or `linux_arm64` archive from the
[latest release](https://github.com/vibe-agi/vibermate/releases/latest), verify
it with `SHA256SUMS-linux`, and extract it. The archive contains `vibermated`,
`vibermate`, and the adjacent `vibermate-web` UI.

Start an encrypted Runtime on your network:

```sh
./vibermated server \
  --listen 0.0.0.0:9666 \
  --transport self_signed_tls
```

The first JSON line contains the browser address and TLS fingerprint. On the
Server machine, print the one-use setup/recovery key:

```sh
./vibermated server recovery-key
```

Open the browser address, enter that key, and create your personal owner
username and password. A browser will warn about the self-signed server
certificate; check the displayed fingerprint before continuing. If you start
the Server with `--data-dir`, pass that same absolute directory to the
`recovery-key` command.

For a shared production network, provide a certificate already trusted by your
users instead:

```sh
./vibermated server \
  --listen 0.0.0.0:9666 \
  --transport tls_files \
  --tls-cert /absolute/path/fullchain.pem \
  --tls-key /absolute/path/private-key.pem
```

In **Settings → Access & launch**, the owner creates an account for each person. The
same account works in the browser and CLI. On each developer machine, sign in
once:

```sh
vibermate login --server https://your-server.example:9666
vibermate run --server https://your-server.example:9666 -- claude
# or: vibermate run --server https://your-server.example:9666 -- codex
```

Replace the example address with the HTTPS address you opened in the browser.

Each person can change their own password from the browser account menu. The
owner can reset a member password. The local App can also reset its owner's
password under **Settings → Access & launch**. For a headless Server, run
`vibermated server recovery-key` locally and use **Forgot owner password?**;
the recovery key rotates after use.

![ViberMate team insights](https://vibe-agi.github.io/images/vibermate/team-insights-2400.webp)

## Certificates, without guesswork

- Managed Claude and Codex processes receive ViberMate's local Root directly
  for that process. Linux does not need a system-wide CA installation.
- On macOS, install the Root from **Settings → Safety & data → Local Root
  Certificate** only for other clients that depend on macOS system trust.
- The Runtime Root used to inspect agent traffic is separate from the TLS
  certificate used to open a remote Server in a browser.
- Root replacement is disabled while captures are running. The UI shows the
  exact SHA-256 fingerprint for install, replacement, and removal.

## What you can control

- Inspect conversations, requests, responses, tool activity, token evidence,
  and network decisions.
- Keep an agent's original destination or route it through another upstream
  service and account.
- Preview, edit, and test built-in JavaScript transforms before publishing.
- Select an upstream account from the authenticated ViberMate login name.
- Hold new external work before disconnecting a machine or Runtime.

![ViberMate script library](https://vibe-agi.github.io/images/vibermate/script-library-2400.webp)

## Data and current boundaries

- AI traffic still goes to the provider or upstream service you choose.
- The evidence database is not encrypted by ViberMate; protect the host account
  and filesystem. Recording and retention are configurable.
- Provider credentials are kept out of policy snapshots and evidence, but text
  deliberately placed in a prompt remains prompt content.
- Transform JavaScript has no network, file, clock, or random access. A failure
  stops the request instead of silently bypassing the rule.
- This is an early `0.x` release. A hardened public-Internet deployment,
  automatic updates, plugins, and arbitrary-client compatibility are not yet
  claimed.

Run `vibermate doctor` when setup fails. For implementation details, see the
[runtime module map](docs/module-map.md) and [architecture decisions](docs/adr).
Report suspected vulnerabilities through [SECURITY.md](SECURITY.md).

Apache-2.0 licensed.
