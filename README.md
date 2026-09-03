# ViberMate

[English](README.md) | [简体中文](README.zh-CN.md)

**See and control how Claude Code and Codex CLI connect to AI services.**

ViberMate is a local macOS app for people who use AI coding agents. Start an
agent through ViberMate and you can see its requests, choose where they go,
apply small JavaScript rules, and keep an auditable local record.

> **Release status:** the source is public, but the first Developer ID-signed
> and notarized download has not been published yet. Until it appears on the
> [Releases page](https://github.com/vibe-agi/vibermate/releases), builds from
> this repository are developer previews and should not be redistributed.

## What can I do with it?

- Run Claude Code or Codex CLI normally while seeing each captured conversation.
- Keep the original provider connection, or route a request to a configured
  endpoint and account.
- Hide local usernames and paths before a request leaves your Mac.
- Use built-in JavaScript examples, edit them, and test them against a sample
  Turn before publishing.
- Select an endpoint account with a JavaScript rule, including the name of the
  ViberMate user who logged in.
- Pause new network work with Offline Hold when you need to disconnect safely.

ViberMate is not an AI provider and does not replace Claude Code or Codex. The
agent CLI must already be installed and able to sign in to its provider.

## First run

### 1. Install ViberMate

A signed installer is still being prepared. After the first public release,
the intended Homebrew command is:

```sh
brew install --cask vibe-agi/tap/vibermate
```

Do not expect that command to work until the Releases page and this section
announce a signed version. Developers can [build from source](#build-from-source)
in the meantime.

### 2. Add the Terminal command

1. Open ViberMate.
2. Open **Settings → General → Terminal command**.
3. Choose **Set up command**.

ViberMate creates `~/.local/bin/vibermate`. It does not edit your shell profile
or replace a command it cannot prove it owns.

If your shell says `command not found`, run the same examples with
`~/.local/bin/vibermate` or add `~/.local/bin` to your shell's `PATH`.

### 3. Start an agent

Open a Terminal in your project and run one of these commands:

```sh
vibermate run -- claude
```

```sh
vibermate run -- codex
```

You do not need to create an Environment first. The built-in transparent mode
keeps the agent's original destination, account, credentials, and model.

### 4. Return to ViberMate

The new run appears as a Capture. Open it to inspect conversations, requests,
responses, tool activity, usage, and connection results. Stop the agent as
usual with `Ctrl+C`.

When you are ready to apply your own routing or JavaScript rules, create an
Environment and launch it explicitly:

```sh
vibermate run --env work -- claude
```

## Do I need to install a certificate?

Usually, no. Claude Code and Codex processes started with `vibermate run`
receive the ViberMate certificate directly for that process.

Install the certificate from **Settings → General → Local Root Certificate**
only when another application relies on the macOS trust store. ViberMate shows
the exact SHA-256 fingerprint and installs trust only for the current macOS
user. The private key stays in ViberMate's local data directory.

Certificate replacement is disabled while a Capture is running. Removal and
recovery instructions always identify the exact ViberMate certificate rather
than asking you to delete an unrelated certificate.

## Five words used in the app

| Word | Plain meaning |
| --- | --- |
| **Capture** | One managed agent run, or one manually connected application. |
| **Environment** | A reusable set of routing, account, network, and JavaScript rules. |
| **Endpoint** | The upstream AI service address. |
| **Account** | One credential belonging to one Endpoint. |
| **Message transform** | JavaScript that changes a request before upload or a response before display. |

An Environment is frozen when a Capture starts. Editing or publishing it
affects future Captures, not a request that is already in progress.

## Data and privacy

- ViberMate runs locally, but your AI requests still go to the provider or
  endpoint you selected.
- The local SQLite evidence database is **not encrypted** by ViberMate. It
  relies on your macOS account and local file permissions.
- New Environments default to redacted full-content evidence retained for 30
  days. You can choose metadata-only recording or turn content recording off.
- Provider credentials are kept outside Environment snapshots and evidence.
  Text you type into a prompt is still content, so do not paste a secret unless
  you intend to send it to the selected provider.
- Message-transform JavaScript has no network, file, clock, or random access.
  A bounded execution failure stops the request instead of silently bypassing
  the rule.

## Build from source

This path is for contributors. It requires macOS 14 or later, Xcode, Go
1.25.13, and the repository-pinned Flutter 3.41.5 SDK.

```sh
git clone https://github.com/vibe-agi/vibermate.git
cd vibermate
make build-flutter-app
open dist/ViberMate.app
```

The local App is ad-hoc signed and is not a public distribution build. Run the
main validation gates with:

```sh
make check
make check-flutter-macos
```

Implementation details live in the [runtime module map](docs/module-map.md),
and the most important design decisions live in [docs/adr](docs/adr).

## Current boundaries

- The current release target is macOS 14 or later.
- Built-in semantic inspection covers Anthropic Messages and OpenAI Responses
  traffic used by the supported Claude and Codex paths.
- A hardened public-Internet Server, automatic updates, plugins, and broad
  arbitrary-client compatibility are not claimed yet.
- A passing source build is not the same as a Developer ID-signed, notarized,
  Gatekeeper-approved release.

ViberMate is licensed under the [Apache License 2.0](LICENSE).
