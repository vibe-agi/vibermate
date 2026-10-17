# Control and inspection reliability implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Remove the demonstrated full-history Raw lookup, tolerate a busy initial process attachment, and retain useful bounded timeout diagnostics while recording isolation is implemented.

**Architecture:** Keep the current public interfaces and byte-integrity checks. Separate the initial control budget from routine supervision, preserve typed error causes, and abort individual UI requests when their deadline expires. These changes are a supporting sub-project of the recording-isolation spec, not proof of full recording isolation or a release candidate on their own.

**Tech Stack:** Go 1.26.9, modernc SQLite, Dart/Flutter, package:http 1.6.0.

**Spec:** `docs/superpowers/specs/2026-10-17-recording-isolation-design.md`, observed defects and acceptance 1, 2, 11.

## Global Constraints

- Keep the user's running App, actual database, credentials and original main worktree intact. Work only in `/Users/null/.codex/worktrees/1c22/vibermate`.
- Actual forwarded bytes are not droppable. Authority, replay and revocation checks remain reliable.
- Initial attachment and ordinary UI control reads allow 30 seconds while honoring caller cancellation and explicit shorter launcher control budgets.
- Diagnostics contain only closed local stage/cause values and already-sanitized protocol status/reason; no raw URL, token, header, response body or arbitrary error text.
- Use behavioral RED before new production changes. Existing accepted RED/GREEN evidence may be reused for already-written changes; do not pretend an initially passing test was RED.
- One source implementer and one heavy test process at a time. Run affected checks, not the hour-long persistence race suite locally. Reviewers use the implementer's exact test evidence instead of repeating it.
- Do not publish this sub-project as completion of recording isolation. No user App restart or real data migration.

---

### Task 1: Qualify indexed Raw reads and the initial attachment budget

**Files:**
- Modify: `internal/runtimepersistence/evidence_body.go`, `internal/runlauncher/launcher.go`, `internal/runlauncher/acp_unix.go`.
- Test: `internal/runtimepersistence/evidence_body_test.go`, `internal/runlauncher/attachment_budget_test.go`, `internal/runlauncher/create_budget_internal_test.go`.
- Read for affected coverage: `internal/runtimepersistence/raw_evidence_repository_test.go`, `internal/runtimepersistence/native_retention_test.go`.

**Interfaces:**
- Preserve `loadEvidenceChunks` and public Raw/native content reader interfaces.
- Private launcher `initialControlTimeout` defaults to 30 seconds; routine `controlTimeout` remains 5 seconds. Explicit positive `Options.ControlTimeout` bounds both; `CreateTimeout` alone does not shorten attachment.
- SQL compares the stored BLOB primary key without applying a function to it:

```sql
FROM json_each(?) AS wanted
JOIN runtime_evidence_chunks AS chunks
  ON chunks.digest = unhex(wanted.value)
```

- [x] **Step 1: Inspect the existing changes and accepted RED evidence.** The root already wrote the regression tests and minimal fixes before the user expanded the architecture scope. Evidence root is `/Users/null/Downloads/vibermate-timeout-fix-20261017-a6aswkq0`. `raw-evidence-scale-red.log` records an old query taking 4.804 seconds beyond its 2-second context; `attachment-red.log` records both actual-child cases stopping under the old 5-second attachment bound. The earlier `raw-evidence-red.log` passed and is not accepted RED. Read the implementation and verify the existing tests assert exact bytes, context budget, real child output and exit code.

```go
// Existing regression's essential behavioral checks:
records, err := repository.ListExchange(ctx, exchangeID)
if err != nil || ctx.Err() != nil { t.Fatalf("read exceeded budget: %v / %v", err, ctx.Err()) }
// Compare retained body byte-for-byte; do not assert a query-plan string.
// The real shell child receives input only after a 6-second attach and exits 7.
```

- [x] **Step 2: Qualify the narrow implementation.** Preserve digest/decompression/manifest-size checks and both run/ACP attachment contexts. Verify cancellation and explicit shorter control budgets still work. Any additional behavior fix requires its own observed failing test before production edits. Do not change schemas, frozen DDL, Raw queue or recording contracts in this task.

- [x] **Step 3: Run affected checks once.** Use the pinned Go toolchain `/Users/null/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.9.darwin-arm64/bin/go`. Run `go test -mod=readonly -p=1 ./internal/runlauncher -count=1 -timeout=10m`. For persistence, derive a fully anchored alternation from the actual `func Test...` declarations in `evidence_body_test.go`, `raw_evidence_repository_test.go`, and `native_retention_test.go`; run those tests with `-count=1 -timeout=10m`. Then run a scoped race check of the new real-child attachment test and the existing explicit-control-budget test cases. Record exact commands, package output and logs; one test process at a time.

- [x] **Step 4: Self-review and commit only these six source/test files.** Leave the controller's uncommitted spec/ADR/plan/glossary files alone. Commit message: `fix: keep initial capture attachment and evidence reads responsive`. Report accepted RED/GREEN logs and affected checks. Do not claim the broader isolation is complete.

### Task 2: Preserve safe control failure stage and transport cause

**Files:**
- Create: `internal/runlauncher/control_failure.go`, `internal/runlauncher/control_failure_test.go`.
- Modify: `internal/runlauncher/control.go`, `cmd/vibermate/main.go`.
- Test: `internal/runlauncher/control_internal_test.go` if present (otherwise keep new focused tests in `control_failure_test.go`), `cmd/vibermate/main_test.go`.

**Interfaces:**
- Preserve exported `ControlFailure` and `retryableControlFailure` semantics for HTTP 408, 429 and 5xx.
- Add a distinct error wrapper for transport/read/decode failure, preserving the underlying cause with `Unwrap`; do not represent network errors as `ControlFailure{Status:0}`.

```go
type ControlCallFailure struct {
    stage string
    reason string
    cause error
}
func (failure *ControlCallFailure) Unwrap() error { return failure.cause }
func (failure *ControlCallFailure) Error() string { return failure.SafeSummary() }
func (failure *ControlCallFailure) SafeSummary() string {
    return "stage=" + safeControlStage(failure.stage) + " reason=" + safeControlCause(failure.reason)
}
```

Stages are closed local values: `control`, `session_check`, `capture_create`, `process_attach`, `capture_heartbeat`, `capture_finish`, `acp_start`, `acp_publish`. Derive from locally constructed method/path; never include a run ID. Closed causes: `timeout`, `canceled`, `dns`, `connection_refused`, `connection_reset`, `tls`, `response_read`, `invalid_response`, `network`. Classify using `errors.Is/As`, not substring matching of raw errors. Allowlisted HTTP metadata keeps the same stage vocabulary.

- [ ] **Step 1: Add failing behavioral tests.** A local control HTTP fixture times out during post-start attach; CLI stderr must include `stage=process_attach reason=timeout` as well as the existing supervision summary. A wrapped temporary network error must remain retryable; a definitive 403 remains non-retryable. An error containing a fake URL/credential sentinel must not print that sentinel. Exercise body read timeout and invalid JSON separately so stage/cause is not lost after response headers.

```go
var call *ControlCallFailure
if !errors.As(err, &call) || !errors.Is(err, context.DeadlineExceeded) {
    t.Fatalf("missing typed cause: %v", err)
}
if got := call.SafeSummary(); got != "stage=process_attach reason=timeout" {
    t.Fatalf("summary=%q", got)
}
```

- [ ] **Step 2: Run the new tests and record the expected missing-stage/cause failure.** Use focused `go test -mod=readonly -p=1 ./internal/runlauncher ./cmd/vibermate -run 'Control.*Failure|.*Supervision.*Detail' -count=1`; adjust the regex to the actual added names and record it.
- [ ] **Step 3: Wrap failures at `controlClient.jsonRequest` with the local stage, and render `SafeSummary` from the CLI using the existing localized detail template.** Keep `errors.Is/As` working through all wrappers and joins. Preserve cancellation exit 130 and existing child lifecycle behavior. HTTP response bodies remain bounded. No automatic retry of start or command execution is introduced.
- [ ] **Step 4: Run affected launcher/CLI packages, self-review and commit.** Include transient heartbeat retry and HTTP rejection regression evidence. Commit message: `fix: explain capture control failures with safe stage details`.

### Task 3: Give UI reads a 30-second deadline and cancel the timed-out request

**Files:**
- Modify: `ui/flutter_app/lib/core/api/control_api.dart`, `ui/flutter_app/lib/features/workbench/workbench_controller.dart`, `ui/flutter_app/lib/core/i18n/app_copy.dart`.
- Test: `ui/flutter_app/test/control_api_http_test.dart`, `ui/flutter_app/test/control_api_session_test.dart`.
- Create focused test: `ui/flutter_app/test/control_api_timeout_test.dart`.

**Interfaces:**
- Keep all `ControlApi` methods and response byte limits.
- `_requestTimeout = const Duration(seconds: 30)`; each `_send` attempt has one deadline covering headers and body, not a fresh 30 seconds per chunk.
- Add a typed safe `ControlRequestTimeout` containing only duration/closed operation information if the UI needs a distinct error. Workbench translates it through AppCopy; never show raw `Future not completed` or a token-bearing URI.
- Use the already-pinned package:http `AbortableRequest` and an individual `Completer<void>` trigger. Do not close the shared client to cancel one request.

```dart
final abort = Completer<void>();
final request = http.AbortableRequest(method, destination,
  abortTrigger: abort.future);
// One owned timer/deadline covers sending and bounded body consumption.
// On timeout, complete abort once, report the typed timeout, and cancel timer.
// Closing HttpControlApi cancels every outstanding owned request.
```

- [ ] **Step 1: Add failing async tests through `HttpControlApi`.** Use a controlled BaseClient that observes `Abortable.abortTrigger`, fake time where available, and a real local HTTP server for one cancellation check. Verify a response after 10 but before 30 seconds succeeds; a stalled headers/body attempt times out at 30 seconds and aborts; one timed-out request does not abort a concurrent request; slow trickle does not reset the total deadline; a late response cannot publish stale work. Continue to enforce existing maximum-body checks and session renewal behavior. Add a controller-level check for actionable localized timeout text.
- [ ] **Step 2: Record RED, then implement the per-request owner.** Inspect the pinned `http-1.6.0` code in the local pub cache before using cancellation APIs. Bound/cancel the response subscription on failure, including declared-length rejection. Dispose cancels all owned requests and the renewal timer. Retired read retry remains at most once and mutating calls are not retried.
- [ ] **Step 3: Run the new timeout tests, existing control API HTTP/session tests, and relevant workbench tests once; run Dart formatting and targeted analyzer.** Use the repository's pinned Flutter invocation after reading its tool configuration. Record exact commands/results. Do not build or install the App.
- [ ] **Step 4: Self-review and commit.** Commit message: `fix: cancel timed out workbench requests and explain recovery`. No new dependencies.

## Completion boundary

After all three task reviews pass, retain the worktree and continue the recording-isolation architecture. These changes do not split database ownership or remove synchronous logging gates. The final full-branch review and release qualification occur only after that work is implemented and tested.
