# ViberMate Flutter desktop

This is ViberMate's sole Desktop client. The retired React/Tauri shell was
removed after the Flutter control, runtime, test, and release-tooling paths
were migrated.

The app has two explicit modes:

- `live` launches the packaged `vibermated` child, exchanges the one-time
  bootstrap capability for an in-memory desktop session, and uses the loopback
  Control API with the `vibermate://desktop` origin. The bundle also contains
  the exact `vibermate` CLI that Settings can install as a user-owned Terminal
  symlink. Both Go executables are built with `vibermate_native_secrets`, so a
  live App uses the macOS Keychain rather than the development file store.
- `preview` uses deterministic fixtures and always displays a visible Preview
  badge. It never presents fixtures as runtime evidence.

## Build runnable apps

From the implementation repository:

```sh
ui/flutter_app/tool/build_macos_app.sh live
ui/flutter_app/tool/build_macos_app.sh preview
```

Outputs:

- `dist/ViberMate.app`
- `dist/ViberMate-Preview.app`

These local development builds are ad-hoc signed. They are not Developer ID
signed or notarized release candidates.

The compiler is fixed by `tool/flutter-sdk.env`; builds reject any Flutter tag
or immutable revision other than `3.41.5@2c9eb20739dfec95e2c74bd3dfa4601b0a8a36aa`.
The App, daemon discovery, Keychain service and installed CLI share the single
application identity `io.vibermate.desktop`.

First launch uses a centered 1280 x 800 logical-point reference content area.
The full window stays within 85% of the visible screen width and 80% of its
height, including window chrome. A height limit does not also squeeze the width;
a narrow screen still reduces the reference height. Later launches preserve
the user's saved size and position, correcting
only frames that no longer fit a connected screen. **Window > Restore Recommended
Size** explicitly returns an existing window to the recommended size; there is
no aspect-ratio lock on manual resizing.

## Verify

```sh
cd ui/flutter_app
flutter analyze
flutter test
bash tool/test_window_geometry.sh # macOS geometry only; does not launch the App

VIBERMATE_LIVE_TEST_DAEMON="$PWD/build/vibermated" \
VIBERMATE_LIVE_TEST_COMMAND="$PWD/build/vibermate" \
  flutter test test/live_runtime_test.dart

# macOS-only: tests Swift storage, builds the App, exercises its exact bundled
# daemon/CLI, and launches/quits the real App twice.
cd ../..
make check-flutter-macos
```

The live test uses temporary runtime directories, performs the real
daemon/bootstrap/session/Control API handshake, and closes the child before it
removes those temporary files.

## Information hierarchy

Page headings identify the task; actions say what happens next. Avoid a second
sentence that repeats either one. `PageHeading` and `ContextHelpHeading` keep
background explanations behind a consistent, clickable information button.
`ContextHelpButton` opens scrollable, selectable help, works by keyboard and
touch, and returns focus when dismissed. Help must not depend on hover.

Keep errors, validation, missing prerequisites, security warnings, recording
scope, stale data and destructive consequences visible beside the relevant
control. `CompactLabeledControl.detail` is still visible; only explicitly
designated `help` is disclosed on demand. Never move all notices into help.
Descriptions that distinguish choices (script examples, destinations, credential
types) also remain visible. Both English and Chinese follow these rules.

This applies across Captures, Connections, Usage, Traffic policies, Upstream
services, Upstream accounts, Script library, all Settings tabs and shared editors.
Narrow page headers place actions below the title instead of squeezing the
title and help button. Tests cover 390 px, 200% text, both locales and themes.

Upstream accounts have a quick note action beside their names. Notes appear in
the account list and service-link picker, and both searches include them. Empty
text clears a note; annotations are limited to 256 Unicode code points on one
line. They are management metadata only, never forwarded to providers. Their
own revision protects concurrent edits without rotating credentials or changing
the identity revision pinned by traffic policies. Saving failures keep the
editor and draft open.

## Capability ledger

Each row names the authority exercised by the native client. “Migrated” means
the production path and local evidence exist; it is not a Preview or Release
readiness claim.

| Capability | Flutter state | Authority / evidence |
| --- | --- | --- |
| Capture directory and detail | migrated | Running/History separation, managed/manual source evidence, exact Capture assignment |
| Manual Capture lifecycle | migrated | real create/rotate/revoke; one-time credential delivery; revoke retains evidence |
| Conversations and Exchanges | migrated | CaptureRun grouping, Exchange-only manual boundary, paged Turn timeline and bounded map |
| Environments | migrated | draft/impact/publish CAS, multiple upstream Endpoints, recording and policy settings |
| Upstream services and accounts | migrated | credentials have a separate account registry; explicit compatible service links reuse credentials, with guarded unlink/delete and independent credential rotation |
| Network governance | migrated | global pending-approval attention, confirm-before-decision, connections, attempts and atomic rules |
| Offline hold | migrated | exact runtime revisions, review, safe-to-disconnect evidence and resume probing |
| Terminal command | migrated | exact packaged CLI, closed operations, bounded process output, ownership-safe install/refresh/remove confirmations |
| Desktop runtime lifecycle | migrated | packaged daemon bootstrap/session renewal plus visible unexpected-exit retry boundary |
| Navigation and preference restoration | migrated | closed non-secret workbench schema, private 0600 atomic file, exact historical revision restore, termination flush/fence, two-launch packaged App acceptance |
| Release distribution | migrated; protected execution pending | pinned Flutter universal build, manifest, CI, bundle verifier, deterministic packaged acceptance, Developer ID and notarization tooling now target Flutter; no protected signing/notarization run is claimed from this working tree |

The old Extensions and Quality pages are deferred placeholders without backend
authority. Flutter does not reproduce them as fake capabilities. They should be
introduced only with real plugin and relay-quality contracts.

Managed runs intentionally start in Terminal rather than from a GUI-spawned
interactive child. `vibermate run -- ...` preserves the Agent's TTY, current
working directory and launcher-derived workspace evidence; the Settings page
provides copyable Claude and Codex commands after the owned Terminal entry is
current.

## Current authority model

Provider Accounts live in a separate upstream-account registry. A service
profile explicitly links compatible existing accounts; linking reuses the
same secret and credential epoch rather than copying it. An Environment route
may select only an account linked to its referenced Upstream Endpoint. The
link picker also shows already-linked and incompatible accounts, with reasons.

The add-account dialog separates **OAuth login**, **Import authorization file**, and
**Manual entry**. Codex browser login uses short-lived, session-bound PKCE
transactions: the local App receives a loopback callback, while remote Web
users can paste the complete localhost callback URL. Codes and token grants
remain in the runtime, not the account response or diagnostic history.
Import uses an explicit file picker or paste action; it never silently reads
or rewrites the user's Codex auth.json. Both OAuth paths feed the same managed
credential store, automatic refresh, and paired Authorization/Chatgpt-Account-Id
replacement. Manual Bearer credentials remain static.

Synthetic protocol, widget, and packaged-daemon tests do not claim that a user
completed a real OpenAI authorization; that final browser step is interactive.
