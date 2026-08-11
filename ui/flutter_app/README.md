# ViberMate Flutter desktop

This is the in-progress native macOS replacement for `ui/desktop`. The existing
React/Tauri client remains in the repository until capability parity is proven.

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

## Verify

```sh
cd ui/flutter_app
flutter analyze
flutter test

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

## Migration and removal ledger

`ui/desktop` is not removable merely because the Flutter shell renders. Each
row below names the authority that must exist in the replacement and the
remaining deletion boundary.

| Capability | Flutter state | Authority / evidence |
| --- | --- | --- |
| Capture directory and detail | migrated | Running/History separation, managed/manual source evidence, exact Capture assignment |
| Manual Capture lifecycle | migrated | real create/rotate/revoke; one-time credential delivery; revoke retains evidence |
| Conversations and Exchanges | migrated | CaptureRun grouping, Exchange-only manual boundary, paged Turn timeline and bounded map |
| Environments | migrated | draft/impact/publish CAS, multiple upstream Endpoints, recording and policy settings |
| Endpoints and Accounts | migrated | every Account has exactly one Endpoint; create/rotate/delete uses real authority and reference checks |
| Network governance | migrated | global pending-approval attention, confirm-before-decision, connections, attempts and atomic rules |
| Offline hold | migrated | exact runtime revisions, review, safe-to-disconnect evidence and resume probing |
| Workspace default | migrated | real GET/PUT/DELETE CAS; independent from the current Capture assignment |
| Terminal command | migrated | exact packaged CLI, closed operations, bounded process output, ownership-safe install/refresh/remove confirmations |
| Desktop runtime lifecycle | migrated | packaged daemon bootstrap/session renewal plus visible unexpected-exit retry boundary |
| Navigation and preference restoration | migrated | closed non-secret workbench schema, private 0600 atomic file, exact historical revision restore, termination flush/fence, two-launch packaged App acceptance |
| Release distribution | in progress | pinned Flutter build, CI, bundle verifier and deterministic packaged acceptance use Flutter; Developer ID signing/notarization workflow remains to migrate before old shell removal |

The old Extensions and Quality pages are deferred placeholders without backend
authority. Flutter does not reproduce them as fake capabilities. They should be
introduced only with real plugin and relay-quality contracts.

Managed runs intentionally start in Terminal rather than from a GUI-spawned
interactive child. `vibermate run -- ...` preserves the Agent's TTY, current
working directory and launcher-derived workspace evidence; the Settings page
provides copyable Claude and Codex commands after the owned Terminal entry is
current.

## Current authority model

Every Provider Account belongs to exactly one global Upstream Endpoint through
`upstreamEndpointId`. An Environment may reference multiple Upstream Endpoints;
each route may only select accounts owned by its referenced Endpoint. The UI
groups accounts under that authority and surfaces any mismatched route evidence
as an error instead of silently offering an invalid account.
