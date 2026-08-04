# Local Preview Usability Closure

Status: in_progress
Created: 2026-08-04
Implementation baseline: `8656bdcf922342c1a1b45aca252f4c9bd9d77e37`
Design authority: `vibermate-design` at `6fd70d49fc563e6ca8a95e35e6806ec80d0fe922`

## Goal

Turn the existing foundations into one honest, usable local macOS product loop:
launch the packaged Desktop App, create one Access and provider credential with
the development file SecretStore, start a supported terminal client through the
packaged CLI, preserve its streaming response, and inspect the resulting run,
connection, egress, and activity evidence in the App.

This goal closes composition and usability gaps. It does not expand provider,
plugin, Server, Language Bridge, Keychain, or system-wide application capture
scope.

## Product acceptance

1. A fresh development installation starts from one clean unreleased SQLite
   baseline. A database from a different development baseline fails with a
   stable, user-actionable reason instead of being partially accepted.
2. Settings exposes the exact packaged `vibermate` command and its terminal
   discoverability state. A user can copy an absolute command immediately; a
   managed terminal link is installed or removed only through the existing
   receipt-owned `cliinstall` authority and never edits a shell profile.
3. The Access screen creates one complete enabled Access, pass-through Profile,
   provider account, direct egress rule, and SecretRef without exposing the
   saved API key again.
4. `vibermate run -- <client>` reaches the running Desktop generation, creates
   one CaptureRun, injects only the capture proxy/Root environment needed for
   that child, and does not leak its control credential to the child.
5. A semantic request traverses CONNECT/SNI admission, the immutable Access
   snapshot, codec conversion, provider transport, streaming response, and
   durable ConnectionEvent/EgressAttempt/Activity evidence.
6. Manual capture remains available for applications that accept explicit HTTP
   proxy settings. It uses the same listener and Access authority and does not
   become a second routing system.
7. The current clean source commit has packaged deterministic evidence. Any
   credentialed dogfood evidence is separate, private, opt-in, and never
   described as a general client/provider guarantee.

## Implementation order

1. Freeze the clean schema identity and add regression fixtures for foreign
   unreleased baselines.
2. Connect `internal/cliinstall` to the packaged CLI and Desktop Settings with
   typed status/install/remove operations and English/Chinese copy.
3. Exercise a fresh App through Access creation and development-secret storage;
   fix only blockers found on that real path.
4. Exercise one deterministic local provider fixture through the packaged CLI,
   then one explicitly authorized credentialed client path if the environment
   supports it.
5. Run repository, Go, race, Rust, React, Playwright, cross-build, vulnerability,
   packaging, and freshness gates; archive exact proof and non-proof boundaries.

## Invariants

1. The App Webview never receives a provider secret, Root private key, local
   control credential, or arbitrary filesystem mutation primitive.
2. Terminal-command installation never overwrites an existing object, edits a
   shell profile, or removes a link whose filesystem identity no longer matches
   its private receipt.
3. Access configuration remains the sole routing authority. CaptureRun,
   workspace metadata, client signer identity, proxy username/password, and UI
   selection cannot directly choose an account or provider target.
4. The development file SecretStore is an explicit Preview limitation, not a
   Keychain-equivalent security claim.
5. No compatibility migration is added before a database format is released.
   Development-baseline replacement is explicit and fail closed.
6. Tests and fixtures use local or reserved example origins. No private dogfood
   provider address or credential enters source, history, logs, or evidence.

## Explicitly out of scope

- Keychain and production secret-store hardening;
- system Root installation or removal and system-wide application capture;
- Network Extension, TUN, transparent capture, or App-specific proxy injection;
- Server listener, LAN sharing, enrollment UX, and remote proxy grants;
- advanced multi-candidate policy editing beyond the existing ordered candidate
  flow, workspace route switching, plugins, Market, Language Bridge, and new
  provider dialects;
- Preview or Release claims beyond the exact local acceptance recorded here.

## Completion statement

> A local macOS user can launch VibeMate, configure one Access and development
> credential, run a supported terminal client through the packaged command, keep
> the client streaming behavior, and inspect the resulting traffic evidence.
> Keychain, system-wide App capture, LAN Server mode, plugins, and general
> Preview/Release readiness remain unclaimed.
