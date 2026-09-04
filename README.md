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

## macOS App: first capture

Install and open ViberMate:

```sh
brew install --cask vibe-agi/tap/vibermate
```

In **Settings → General → Terminal command**, choose **Set up command**. Then,
from your project directory:

```sh
vibermate run -- claude
# or
vibermate run -- codex
```

Return to the App to inspect the new Capture. You can start without configuring
a Traffic Policy; transparent capture preserves the agent's existing provider,
account, and model.

The App also exposes its management UI to a browser on the same Mac. Copy the
address shown under **Settings → Team access → Web & client access**.

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

The first JSON line contains the browser address, TLS fingerprint, and
`adminAccessKeyPath`. Open the address, read the owner key from that file, and
use it to enter the Web workbench. A browser will warn about the self-signed
server certificate; check the displayed fingerprint before continuing.

For a shared production network, provide a certificate already trusted by your
users instead:

```sh
./vibermated server \
  --listen 0.0.0.0:9666 \
  --transport tls_files \
  --tls-cert /absolute/path/fullchain.pem \
  --tls-key /absolute/path/private-key.pem
```

In **Settings → Team access**, create a Runtime User for each person or device.
On each developer machine, use the matching username and password once:

```sh
vibermate login --server 192.0.2.10:9666
vibermate run --server 192.0.2.10:9666 -- claude
# or: vibermate run --server 192.0.2.10:9666 -- codex
```

The owner key is for browser administration. Runtime User passwords are for
the `vibermate` command; do not share the owner key with agent users.

![ViberMate team insights](https://vibe-agi.github.io/images/vibermate/team-insights-2400.webp)

## Certificates, without guesswork

- Managed Claude and Codex processes receive ViberMate's local Root directly
  for that process. Linux does not need a system-wide CA installation.
- On macOS, install the Root from **Settings → General → Local Root
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
