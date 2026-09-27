# Codex native compatibility: six-boundary repair

Status: complete — six repairs implemented, local acceptance passed, independent
test bundle signed and verified. Subsequent user-approved installation and the
post-handoff notification regression are recorded below.

## Scope and evidence

Repair the six findings from the 2026-09-27 local review. Installed Codex
`0.157.1` is the executable compatibility oracle; cached upstream source is
reference material, not a claim that it matches that release exactly.
Synthetic loopback providers and an isolated PTY reproduced the failures without
using account credentials or changing recording settings. The original failed
request has no retained response body, so its exact upstream error is unknown.

The official [Responses streaming guide](https://developers.openai.com/api/docs/guides/streaming-responses)
defines incremental semantic events and explicit terminal events. Native
same-dialect forwarding must not depend on whether the audit projection can
represent every native field. Cross-dialect translation remains validated by
the neutral model. Client-executable tool output must still pass approval.

## Safety and acceptance invariants

- Preserve existing uncommitted work. No account changes, token refreshes,
  recording/retention changes, automatic failover, or live inference calls.
- Do not turn privacy-safe diagnostics into a store of provider messages,
  opaque state, arbitrary headers, credentials, or response bodies.
- Never release executable tool calls before approval; rejection must not
  leak actionable tool completion or a successful terminal event.
- Never retry a model request after client-visible semantic output is committed.
- Do not carry opaque turn state across account/credential-owner or route
  changes. Do not infer trust from an unrecognized client-supplied token.
- List selection and detail fetching cannot alter a Capture's activity time.
  Cross-Capture native conversation grouping is a separate, unchanged concern.
- Restore only an actual terminal; do not inject escape sequences into pipes,
  JSON output, ACP transport, or redirected logs.
- Build an independent test bundle. Do not replace/restart the user's App.

## Implementation sequence and regression matrix

### 1. P1 — stream progress and semantic completion

- Trace every Stream implementation and approval caller before changing the
  shared contract. Reuse the SSE decoder and existing commit/approval ledger.
- Forward validated non-executable progress incrementally on native Responses
  paths. Hold tool-bearing events and terminal success until their approval
  boundary; do not hold the entire preceding text/progress stream.
- Stop upstream consumption on a valid semantic terminal event, close/cancel
  the body pump, and finish approval without waiting for HTTP EOF.
- Retain malformed-frame, duplicate-terminal, byte-limit, cancellation, and
  bounded buffering checks. Partial/unknown events must not bypass tool policy.
- [x] Regressions: text/progress before EOF; completed with socket left open;
  fragmented SSE; tool allow/deny; malformed/truncated stream; cancellation;
  native Codex survives a progressing stream with a short configured idle budget.

### 2. P1 — native routing state and response metadata

- Delay downstream header commitment until the upstream response is known.
  Keep response transforms, retry eligibility, and decompression headers correct.
- Preserve protocol-relevant response headers with a narrow policy (request ID,
  quota/model metadata, native turn state), never ambient auth/cookies or
  hop-by-hop/content-encoding headers invalidated by normalization.
- Bind turn-state replay to the frozen provider ownership/routing identity.
  Same binding may continue; changed/unknown binding must not replay opaque
  account-affine state. State is ephemeral and bounded, not diagnostic evidence.
- [x] Regressions: same-binding continuation/retry; account/route switch and
  unknown token isolation; header round-trip; compressed responses; transforms;
  ordinary non-Codex and cross-dialect paths unchanged.

### 3. P1 — error semantics independent of diagnostics

- Separate client-native error forwarding from the durable diagnostic code
  allowlist. Keep native error type/code/retry semantics on compatible paths,
  without writing arbitrary messages into activity or redacted diagnostics.
- Decode bounded compressed non-2xx bodies through the same logical response
  boundary as successful bodies. Retain HTTP status and meaningful client codes.
- Avoid manufacturing generic retryable server errors for native fatal errors.
- [x] Regressions: fatal policy/quota/plan errors remain fatal in installed
  Codex; rate-limit and retryable errors retain their meaning; gzip/identity
  parity (and supported encodings); durable/redacted error privacy unchanged.

### 4. P2 — terminal and launch lifecycle cleanup

- Snapshot terminal state before child launch; restore after reclaiming the
  foreground process group on normal, signaled, cancellation, and start-failure
  paths. Reuse existing terminal dependency and process-group cleanup.
- Reset child-left mouse/keyboard/paste/focus/cursor modes on an interactive
  terminal without corrupting non-TTY outputs or restoring before the child
  has stopped. Cover descendants and idempotent cleanup.
- Distinguish launch failures from runtime control/heartbeat and finalization
  failures. Preserve child exit status and give actionable, localized messages.
- [x] Regressions: isolated PTY raw/echo restoration and terminal reset bytes;
  redirected output unchanged; signals; heartbeat/finish errors no longer say
  that an already-running command could not start.

### 5. P2 — authoritative Capture activity time

- Use a per-Capture activity timestamp at the directory boundary, with explicit
  lifecycle fallback when no activity exists. Heartbeats are liveness only.
- Align backend pagination/order, control DTOs, and Flutter sorting/display.
  Never derive a row timestamp from the currently selected session's timeline.
- Preserve selection through refresh and transitions between running/history.
- [x] Regressions: two Captures sharing a native session; clicking/loading either
  cannot change its timestamp/order; heartbeat-only update; real new activity;
  pagination ties; transition to history; legacy rows and manual captures.

### 6. P2 — HTTP/2 timeout phase boundary

- Start the response-head budget after request headers/body have been written,
  matching HTTP/1 semantics. Reuse Go HTTP trace hooks and cancellation causes.
- Keep independent dial/TLS/stream-idle/caller cancellation budgets; do not
  globally lengthen all timeouts or hide a stalled peer.
- Make timer cleanup race-safe for early responses, write failures, cancellation,
  transport reuse, and HTTP/2 multiplexing.
- [x] Regressions: slow upload longer than head budget succeeds if head arrives
  promptly after upload; stalled post-upload head times out; cancellation;
  early head and concurrent requests; HTTP/1 parity; race detector.

## Release gate

- [x] Added targeted regressions for the reviewed failures and reran existing
  protocol, policy, cancellation and lifecycle checks. No historical worktree
  checkout or reversal of the user's existing changes was used for acceptance.
- [x] Focused tests first, then all Go tests and race tests on touched runtime
  packages; Flutter analyzer and relevant controller/widget/model tests, then
  full Flutter suite where practical. No skipped check described as passed.
- [x] Installed Codex direct-vs-managed synthetic protocol comparison and a
  real isolated PTY check. Neither consumes real account quota.
- [x] Independent local macOS test bundle, CLI/daemon/UI versions consistent,
  existing distribution verification passes. Report exact path and limitations.
- [x] Update this file with actual results and residual risks; mark the goal
  complete only when all six repairs and required checks are genuinely done.

## Implemented boundaries

| Area | Repair and regression anchor |
| --- | --- |
| Native stream | `ProviderStream` releases validated non-actionable progress; tool-bearing frames remain held. A semantic terminal stops consumption without waiting for EOF and confirms transport evidence. `TestProviderStreamProgressAndApprovalBarrier`, `TestManagedResponsesSemanticTerminalDoesNotWaitForEOF`. |
| Native metadata | Narrow response-header policy; bounded hash-only turn-state ownership cache (account, credential epoch, route, session, turn). Provider-affine headers prevent transport retry after commitment. `TestManagedCodexTurnStateOwnershipAndResponseMetadata`, `TestNativeStreamMetadataBlocksRetryAfterHeaderCommit`. |
| Native errors | Sealed transient error payload/event/metadata separate from the diagnostic allowlist; identity/gzip/zstd errors share the logical decoding boundary. HTTP status, native failure kind, retry guidance and native error code reach matching clients. `TestManagedResponsesCompressedRejectionPreservesNativeError`, `TestNativeHTTPErrorPreservesStatusPayloadAndRetryMetadata`. |
| Terminal lifecycle | Restore termios after reclaiming foreground ownership; reset child-left terminal-emulator modes only on the same TTY. Supervision/finalization errors have distinct localized messages. `TestLauncherRestoresAnIsolatedTerminal` and CLI error-classification tests. |
| Capture directory | Capture-scoped body-free activity projection shared by list/detail DTOs, cursors, preview, Flutter order and timestamps. Creation/observation/lifecycle timestamps are fallbacks, not heartbeat activity. `TestCaptureActivityIsScopedAndIndependentOfHeartbeats` and the Capture selection/refresh widget regression. |
| HTTP/2 timer | Response-head budget starts on successful `WroteRequest`, composed with existing trace hooks; cancellation and early-response cleanup are synchronized. `TestResponseHeadTimeoutStartsAfterUpload`. |

Additional safety checks cover normalized SSE byte amplification, reasoning-only
opaque audit output, and contradictions between held completed tool calls and
the terminal snapshot used for approval. Unknown diagnostic field types cannot
silently erase a valid native error payload.

## Acceptance results — 2026-09-27

- `go test ./...`: passed, 107 packages with tests. Log:
  `/tmp/vibermate-six-repair-go-final.log`.
- `go vet ./...`: passed. Log: `/tmp/vibermate-six-repair-vet-final.log`.
- Race detector: passed for `protocolcore`, `openairesponses`, `exchange`,
  `loopbackproxy`, `providertransport`, `runlauncher`, `runtimepersistence`,
  `desktopcontrol`, `capturerun`, `manualcapture`. Final boundary-only rerun
  also passed. Logs: `/tmp/vibermate-six-repair-race-final.log` and
  `/tmp/vibermate-six-repair-race-final-boundary.log`.
- `flutter analyze`: no issues. `flutter test`: **640 passed, 16 skipped**;
  skips require their explicitly configured live/platform environments. Logs:
  `/tmp/vibermate-six-repair-flutter-analyze.log` and
  `/tmp/vibermate-six-repair-flutter-all.log`.
- Installed Codex **0.157.1**: all **11** synthetic subcases passed, including
  direct/managed fatal-error classification and incremental progress under a
  short idle budget. Uses isolated `CODEX_HOME` and loopback fixture servers,
  never an account or real inference endpoint. Rerun with
  `VIBERMATE_CODEX_ACCEPTANCE=<absolute Codex binary> go test ./internal/loopbackproxy -run '^TestInstalledCodex' -count=1 -v`.
  Log: `/tmp/vibermate-six-repair-codex.log`.
- Isolated macOS PTY: normal exit, child SIGKILL and redirected output passed;
  termios restoration and reset routing checked. Log:
  `/tmp/vibermate-six-repair-pty.log`.
- Real H1/H2 loopback TLS: **8** slow-upload, stalled-head, early-response and
  canceled-upload subcases passed. Log:
  `/tmp/vibermate-six-repair-http-timeouts.log`.

## Local handoff and limits

- Bundle: `dist/native-compatibility-repair/ViberMate.app`, **v0.1.14**.
  Desktop/Web UI rebuilt; CLI and daemon rebuilt with the same release label
  and native secret-store build tag. Inside-out Developer ID signing uses the
  existing team **YLZ3Y4478K**; nested object hashes match the regenerated build
  manifest. `verify_macos_app.sh <absolute bundle> live` and strict signature
  verification passed. This is a local test build, not a notarized distribution.
  Log: `/tmp/vibermate-six-repair-bundle-final.log`.
- No App was launched/restarted, no account/recording/retention setting changed,
  no default bundle or installed terminal-command link replaced. Read-only CLI
  inspection correctly reports that the current installation belongs to the
  old application location. To test, quit the old App, open this bundle, and use
  this bundle's `Contents/MacOS/vibermate` for CLI launches.
- The original provider failure had no retained body. These fixes remove
  reproduced proxy divergences; they do **not** prove the exact original
  upstream reason or promise to eliminate legitimate upstream failures.
- Turn-state bindings are ephemeral, capped at 2048 with 24-hour expiry.
  Restarted/unknown/changed-owner state is not replayed. No raw tokens are
  retained in that cache or copied to redacted diagnostics.
- Capture time is derived from retained Capture-scoped activity; after that
  evidence is purged, it falls back to existing observation/lifecycle metadata.
  Cross-Capture native conversation grouping is intentionally unchanged.
- Terminal cleanup can recover a killed child, but cannot execute if the
  launcher itself is killed with SIGKILL or the host loses power.
- Arbitrary future native executable tool types are not made trustworthy by
  passthrough. Unsupported actionable shapes remain policy/decoder work, not
  a reason to bypass approval or broaden the durable diagnostic vocabulary.

## Post-handoff regression — non-terminal error notifications

After the user closed the App, the first test bundle was copied to
`dist/ViberMate.app` with the old bundle retained in
`dist/app-backup-20260927.WHMGws/`. The terminal command already points to that
App, so its target bytes were verified without changing the installation link.

The restarted runtime reproduced repeated failures at 19:48–19:50 local time.
Read-only metadata showed HTTP 200, completed transports, and
`provider_response_failed`, not a timeout. The corresponding installed Codex
logs recorded `unhandled responses event: "error"` immediately before each
`stream closed before response.completed`. Recording remained off; the actual
provider error payload and any unread subsequent events are unavailable.

A synthetic comparison with installed Codex 0.157.1 proved an additional
forwarding defect: an `error` notification followed by normal output and
`response.completed` succeeded directly but failed through the old decoder.
`ProviderStream` now forwards the non-executable notification and continues
reading. Explicit `response.failed` still terminates immediately. EOF without
a terminal still fails, preserving the bounded native error for a client-readable
failed terminal rather than replaying an ignored notification. Tool output and
successful terminal events remain behind approval; privacy-safe diagnostics
still retain only the closed code vocabulary.

Acceptance: full `go test ./...` passed; race checks passed for
`openairesponses`, `exchange`, `loopbackproxy`, and `responseschat`; focused
`go vet` and `git diff --check` passed. Installed Codex passed all **14**
synthetic subcases, including notification-then-success and notification-then-EOF.
The real pipeline pipe test covers completion without HTTP EOF and failure on
actual EOF. Logs: `/tmp/vibermate-error-notification-go.log`,
`/tmp/vibermate-error-notification-race.log`, and
`/tmp/vibermate-error-notification-build.log`.

New independent signed bundle: `dist/native-error-notification-fix/ViberMate.app`.
Only Go runtime code changed; the verified Desktop/Web UI assets were reused.
This does not yet prove that every live upstream failure is resolved. Codex also
logged a WebSocket-to-HTTP fallback (426); that existing transport difference
was observed, not changed. No account, credential, recording, or timeout setting
was changed.

After the user explicitly confirmed that the App and captured Codex had exited,
the follow-up bundle was copied to `dist/ViberMate.app`. Process checks, strict
code-signature verification, a checksum comparison of both bundles, and the
installed CLI symlink's byte comparison passed. The previous bundle remains at
`dist/app-backup-notification-20260927.SEJYKa/ViberMate.app`. Nothing was launched
automatically; the original-session live retest is pending the user.

## Follow-up: shared-daemon session ownership (2026-09-27)

The "conversation is open in another app" report is a writer-ownership
conflict, not the earlier streaming fault. Codex 0.157.1 reuses its shared
backend for an ordinary TUI, but the per-run configuration overrides required
by ViberMate select an embedded backend. Reusing the existing global daemon
would inherit its startup environment, bypassing this run's proxy isolation.
The original backend can therefore retain the original thread's writer lock
after its window closes. No user daemon was stopped and no real lock was removed.

The [official App Server lifecycle](https://learn.chatgpt.com/docs/app-server#unsubscribe-from-a-loaded-thread)
documents unsubscribe, not a safe force-transfer API: the last subscriber
leaving does not guarantee immediate unload. A "resume UUID" preflight was
prototyped, then removed: it missed names, --last, pickers, and /resume after
launch, could inspect the wrong CODEX_HOME before policy application, and
duplicated the native interaction.

Installed Codex already supplies R (retry), F (explicit native fork), and exit
in the locked view. ViberMate now explains those choices at interactive launch
and in localized CLI help. There is no command rewriting, automatic fork,
automatic polling/transfer, session-store import, or schema/version change.
An old client without the F shortcut can use native `codex fork` under the same
ViberMate run options. Fork creates a new ID from saved history; it is not an
in-place handoff and may omit in-flight unsaved work.

Verification on installed Codex 0.157.1: six isolated PTY cases, repeated three
times, passed: UUID, session name, --last, startup picker, /resume within TUI,
and R after the fixture owner releases the lock. Fork assertions require a new
ID with the correct parent, byte-identical source history, and the source lock
still owned. The /resume case sends a synthetic continuation after forking and
receives its response through the configured local proxy. Tests use disposable
homes, synthetic history and credentials, no real model service or account.

Runnable native gate:
`VIBERMATE_CODEX_ACCEPTANCE=<absolute native Codex binary> go test ./internal/runlauncher -run '^TestInstalledCodexLockedSessionChoices$' -count=3 -timeout=90s`.
The ordinary/race launcher, CLI, and localization checks passed, including
unchanged argument suffixes across eleven invocation shapes, English/Chinese
TTY guidance, and no extra guidance in piped or non-Codex launches. Focused
`go vet` and `git diff --check` passed.

Signed candidate: `dist/codex-session-compatibility/ViberMate.app`, still
v0.1.14. Existing verified Desktop/Web assets are reused; Go CLI/runtime and
the signed build manifest were rebuilt. This follow-up fixes missing
compatibility guidance and regression coverage, not Codex's ownership rule.

Installed at `dist/ViberMate.app` after two no-running-process checks. The
previous bundle is recoverable at
`dist/backup-before-codex-sessions-20260927.PGEzuk/ViberMate.app`. Strict bundle
verification, installed CLI/daemon byte comparisons, help execution through
the existing managed CLI link, and final race/vet checks passed. The App was
not started, and no original user session was resumed or forked by the agent.

## Follow-up: long-history compaction rejected before egress

The later `invalid_exchange_request` with client path `$` and no upstream
attempt is separate from session ownership and upstream stream failures.
Read-only reconstruction of the reported session's last compaction window
produced 949 input items and 260 projected extensions: 256 reasoning items,
three encrypted agent messages, and one image. The original HTTP body was not
recorded, so this is a local-history reproduction, not an exact wire replay.
The old request validator rejected it at the shared 256-extension count cap.

`Request.Validate` now applies that count cap per message instead of to all
historical messages combined. Aggregate extension bytes remain capped at
16 MiB; message/block bounds, fragment validation, and the single-response
extension cap are unchanged. No history is pruned, no encrypted state is
rewritten, and no timeout, account, recording setting, or schema is changed.

Regression checks first failed on the old code, then passed with the scoped
counter fix. Core tests cover long history, message-count and per-message
extension boundaries, aggregate byte boundaries, and the unchanged response
cap. Existing real-loopback HTTP tests now send the synthetic 260-extension
shape, verify exact native input preservation, and cover both ordinary
generation and compaction with recording on/off and request compression.
The reconstructed local window also passes decode and provider encoding with
all native input preserved; no real upstream call or session mutation was made.

Verification passed: `go test ./...`, race checks across protocolcore,
openairesponses, responseschat, protocolpath, exchange, loopbackproxy,
anthropicchat, exchangecontent, runtimepersistence, runlauncher, and CLI;
focused `go vet`; and `git diff --check`.

Rebuilt and signed candidate: `dist/compact-repair.5DI0Z5/ViberMate.app`.
Only Go CLI/runtime code changed, so verified Desktop/Web assets were reused.
After the user confirmed exit and two stopped-process checks passed, installed
at `dist/ViberMate.app`. The previous bundle remains recoverable at
`dist/backup-before-compact.a7G7Zx/ViberMate.app`. Strict signature verification,
CLI/runtime byte comparisons, and the existing CLI symlink check passed.
Version remains v0.1.14; the App was not started automatically.
