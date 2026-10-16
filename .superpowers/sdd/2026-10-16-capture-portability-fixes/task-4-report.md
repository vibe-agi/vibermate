# Task 4 report: actionable launch failures

Base: `f727753d69e5532ddeb22e123cc021647f431642` on `codex/capture-portability-fixes`, isolated worktree `/Users/null/.codex/worktrees/1c22/vibermate`.

## Changes

- Actual net/http TCP dial timeouts with a live Capture context now classify as Runtime connection failures. Actual Capture preparation deadlines and caller cancellation keep their existing classifications.
- Remote setup and create connection failures identify the Runtime Server. Missing login, preflight401, and create401 after successful authenticated preflight request remote login.
- CLI stderr prints localized rejection details containing only a bounded numeric HTTP status, a locally chosen stage (`session_check`, `capture_create`, or `control`), and a fixed allowlisted reason (otherwise `unknown`). Titles, response bodies, unknown codes, headers, credentials and raw errors never enter this output. Existing Environment/client-target message keys remain intact.
- Rejected malformed, oversized and trailing-JSON responses retain their HTTP status without retaining arbitrary content. Interrupted response reads preserve operation cancellation/deadline before considering rejection status.
- Remote inspection preserves underlying typed HTTP/network/context causes. No login document format or session/state behavior changed.
- Both English and Chinese timeout messages now refer to the selected Runtime.
- ACP uses the same remote connection/create classification. Child supervision and child exit handling were not changed.

## Vertical RED/GREEN evidence

1. `go test ./internal/runlauncher -run 'TestDialTimeoutWithLiveCaptureContextIsConnectionFailure' -count=1`
   - RED: real expired `net.Dialer` returned `dial tcp ... i/o timeout` with live request context; classifier returned `ErrCapturePreparationTimedOut`.
   - Minimal fix: examine dial establishment failure before the shared deadline sentinel.
   - GREEN: focused dial/classifier/stale-discovery/timeout/cancellation selection passed (`0.873s`).
2. `go test ./cmd/vibermate -run 'TestRemote(RunWithout|CreateRejected|ConnectionFailure)' -count=1`
   - RED: missing remote login returned `cli.error.runtimeUnavailable`; successful-preflight then create401 returned `cli.error.launchFailed`; create connection close returned the local runtime key.
   - Minimal fix: choose remote availability for remote connection errors and remote login for remote create401, retaining cancellation priority.
   - GREEN: all three CLI interface cases passed (`0.966s`).
3. `go test ./cmd/vibermate -run TestRemoteCreateRejectionsExposeOnlySafeDetails -count=1`
   - RED: seven cases lacked safe status/reason/stage details: known create reason, unknown code, HTML, oversized body, trailing JSON, Environment missing, invalid client target.
   - Minimal fix: allowlisted bounded summary, fixed operation stages, localized CLI stderr detail, and strict complete error-document decoding while preserving status for invalid payloads.
   - GREEN: all seven cases passed (`1.462s`); assertions reject synthetic body/header/token/unknown-code sentinels, stdout output and unbounded diagnostics.
4. `go test ./cmd/vibermate -run TestRemotePreflightFailuresPreserveSafeAuthenticationStage -count=1`
   and `go test ./internal/runlauncher -run 'TestLegacyRemoteSessionAuthenticatedBinding/(unavailable|revoked|timeout|canceled)$' -count=1`
   - RED: preflight401 lost HTTP details; preflight503 was labeled `control`; inspection401/503 lost `ControlFailure`, and inspection timeout lost `context.DeadlineExceeded`.
   - Minimal fix: select the actual current-session route stage and preserve error chains using `errors.Join`/`%w`.
   - GREEN: CLI preflight cases (`3.733s`) and inspection cases (`0.951s`) passed. Failed preflight never issues create. Cancellation also remains observable.
5. `go test ./locales -run TestCapturePreparationTimeoutSupportsLocalAndRemoteRuntime -count=1`
   - RED: rendered English timeout directed users to the Desktop App, without referring to the selected Runtime.
   - Minimal fix: update the matching English and Chinese messages.
   - GREEN: complete locale suite passed (`0.398s`).
6. `go test ./cmd/vibermate -run TestRemoteCreateCancellation -count=1 -timeout=20s`
   - RED: cancellation while reading a stalled401 response body returned login-required with rejection details, rather than interrupt outcome130.
   - Minimal fix: preserve the operation context cause when response reading is interrupted before decoding HTTP rejection.
   - GREEN: ordinary create cancellation and canceled rejection-body read both passed (`1.434s`).

An initial full CLI check was interrupted with SIGQUIT after an added cancellation fixture waited indefinitely for its server request context during httptest cleanup. The stack identified only that fixture; it now uses an explicit deferred release channel. The corrected complete CLI run and the focused race run passed. No product fix was inferred from that fixture hang.

## Final verification (normal checks completed before race checks)

- `go test ./cmd/vibermate ./locales -count=1 -timeout=90s`: PASS (`6.590s`, `0.676s`).
- `go test ./internal/runlauncher -run 'Test(DialTimeout|ClassifyCreateFailure|HeartbeatRetries|LegacyRemoteSessionAuthenticatedBinding|RemoteIdentityRecoveryRequiresLoginBeforeCapture|RemoteCaptureAcceptsValidDirectoryDisplayLabels|DefaultRemoteLoginMigratesCompatibleHomeDocument|InspectRemote|Launcher(Bounds|Cancels|Classifies|KeepsChild|Supervises)|TheCreateBudget|TheControlTransport|OnlyCreate)' -count=1 -timeout=90s`: PASS (`12.223s`). Includes configured create timeout, caller cancellation, exact child lifecycle/exit behavior, temporary control outage, binding recovery and compatibility migration coverage.
- `go test -race ./internal/runlauncher ./cmd/vibermate ./locales -run 'Test(DialTimeout|ClassifyCreateFailure|HeartbeatRetries|LegacyRemoteSessionAuthenticatedBinding|RemoteRunWithout|RemoteCreate|RemoteConnectionFailure|RemotePreflightFailures|CapturePreparationTimeout)' -count=1 -timeout=90s`: PASS (all three packages, no race reports).
- `git diff --check`: PASS.

The entire runlauncher suite was intentionally not represented as green: its known Git snapshot failure belongs to Task 6. All fixtures used temporary state, synthetic credentials and loopback peers. No real credentials/services, installed applications or OS settings were changed.
