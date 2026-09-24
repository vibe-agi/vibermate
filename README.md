# ViberMate

[English](README.md) · [简体中文](README.zh-CN.md) · [Website](https://vibe-agi.github.io/products/vibermate/)

**See and control the network boundary around Claude Code and Codex CLI.**

ViberMate captures agent conversations, routes requests, applies small
JavaScript rules, and keeps an auditable record. It does not replace your agent
or AI provider.

## License

This project is available under the GNU Affero General Public License v3.0
(AGPLv3). Commercial licensing is also available for organizations that need
to use the software outside the terms of the AGPLv3.

- [AGPLv3 license text](LICENSE)
- [Commercial licensing and professional services](COMMERCIAL.md)

## Commercial Support

The project author provides architecture consulting, customization,
integration, deployment, and production support. See
[COMMERCIAL.md](COMMERCIAL.md) for details.

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
or share it, go to **Settings → User management** and choose **Create owner**.
Then copy the Web workbench address from **Settings → Access & launch**.
The first account is the owner; later accounts are members.

## Standalone Server + Web (with or without Docker)

Native and container deployments use the same Web workbench and account model.
Choose **this-computer access** or **access from other devices** first; installing
Docker does not change which certificates or accounts you need. See the
[deployment guide](docs/deployment.md) and [Docker configurations](docs/docker.md).

Download the `linux_x86_64` or `linux_arm64` archive from the
[latest release](https://github.com/vibe-agi/vibermate/releases/latest), verify
it with `SHA256SUMS-linux`, and extract it. The archive contains `vibermated`,
`vibermate`, and the adjacent `vibermate-web` UI.

For personal use on this computer, no domain or certificate is needed:

```sh
./vibermated server
```

Open **http://127.0.0.1:9666**. The default listens only on this computer.
If the port is occupied, add `--listen 127.0.0.1:9667` and use that port in the
browser and CLI. In another terminal on the Server machine, read the one-use
setup/recovery key:

```sh
./vibermated server recovery-key
```

Enter that key in the browser and create your personal owner username and
password. If you start the Server with `--data-dir`, pass that same absolute
directory to the `recovery-key` command. Then connect the CLI explicitly:

```sh
vibermate login --server http://127.0.0.1:9666
vibermate run --server http://127.0.0.1:9666 -- codex
```

Even a native Web Server on this computer needs `--server`; a bare local
`vibermate run` connects to the App. The System Transparent policy does not
retain conversation bodies; publish a recording policy and select it with
`--env` when you need content capture.

For other devices (including a personal remote Server), choose one explicit
HTTPS path:

- no public domain: ViberMate private CA with a hosts-file DNS name or IP;
- public domain: embedded automatic issuance and renewal;
- existing public/enterprise certificate files.

For an existing certificate:

```sh
./vibermated server \
  --listen 0.0.0.0:9666 \
  --access-address runtime.example.com:9666 \
  --transport tls_files \
  --tls-cert /absolute/path/fullchain.pem \
  --tls-key /absolute/path/private-key.pem
```

In **Settings → User management**, the owner creates an account for each person. The
same account works in the browser and CLI. On each developer machine, sign in
once:

```sh
vibermate login --server https://your-server.example:9666
vibermate run --server https://your-server.example:9666 -- claude
# or: vibermate run --server https://your-server.example:9666 -- codex
```

Replace the example address with the exact HTTPS address opened in the browser.
The CLI uses normal system PKI when available, so public certificate renewal
does not change server identity. Private CA deployments export the public CA
locally with `vibermated server ca-certificate`; verify its fingerprint out of
band before installing it. A legacy exact-leaf pin can be deliberately migrated
with `vibermate trust --server <URL> --system-roots`. See the
[deployment guide](docs/deployment.md) for the complete commands and trust model.

Each person can change their own password from the browser account menu. The
owner can reset a member password. The local App can also reset its owner's
password under **Settings → User management**. For a headless Server, run
`vibermated server recovery-key` locally and use **Forgot owner password?**;
the recovery key rotates after use.

![ViberMate team insights](https://vibe-agi.github.io/images/vibermate/team-insights-2400.webp)

## Certificates, without guesswork

- Managed Claude and Codex processes receive ViberMate's local Root directly
  for that process. Linux does not need a system-wide CA installation.
- On macOS, install the Root from **Settings → Safety & data → Local Root
  Certificate** only for other clients that depend on macOS system trust.
- Public/enterprise Server HTTPS is separate from AI traffic inspection. The
  opt-in private-CA Server mode intentionally uses the Runtime CA too, and must
  be trusted only on managed devices.
- Root replacement is disabled while captures are running. The UI shows the
  exact SHA-256 fingerprint for install, replacement, and removal.

## What you can control

- Inspect conversations, requests, responses, tool activity, token evidence,
  and network decisions.
- Keep an agent's original destination or route it through another upstream
  service and account.
- Preview, edit, and test built-in JavaScript transforms before publishing.
- Select an upstream account from the authenticated ViberMate login name.

![ViberMate script library](https://vibe-agi.github.io/images/vibermate/script-library-2400.webp)

## Data and current boundaries

- AI traffic still goes to the provider or upstream service you choose.
- The evidence database is not encrypted by ViberMate; protect the host account
  and filesystem. Recording and retention are configurable.
- Provider credentials are kept out of policy snapshots and evidence, but text
  deliberately placed in a prompt remains prompt content.
- [Outbound evidence limits](docs/egress-visibility.md) explain what inspected,
  uninspected, and direct traffic can and cannot prove.
- Transform JavaScript has no network, file, clock, or random access. A failure
  stops the request instead of silently bypassing the rule.
- This is an early `0.x` release. A hardened public-Internet deployment,
  automatic updates, plugins, and arbitrary-client compatibility are not yet
  claimed.

Run `vibermate doctor` when setup fails. For implementation details, see the
[runtime module map](docs/module-map.md) and [architecture decisions](docs/adr).
Report suspected vulnerabilities through [SECURITY.md](SECURITY.md).
