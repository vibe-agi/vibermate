# Interactive Reconnect Diagnostics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development for the bounded fix and independent review. Run after the current quota task releases the sole source/test slot.

**Goal:** Keep transient Runtime reconnect messages out of an interactive client's terminal input while retaining noninteractive diagnostics and real supervision failures.

**Architecture:** The existing heartbeat state machine already keeps the child alive and emits one interrupt/restore per outage, but both messages write to the same stderr inherited by the foreground child. Change only diagnostic presentation at that point: a terminal owned by the child receives no asynchronous supervisor status text; redirected/nonterminal stderr retains the same diagnostic lines. Real rejection/cancellation and terminal restoration remain unchanged.

**Tech Stack:** Current pinned Go1.26.9, existing golang.org/x/term and isolated PTY/test fixtures; no new dependency, backend status framework, client protocol, or retry policy.

**Spec:** Approved Goal scope10 and `2026-10-09-production-continuation.md` user-visible follow-up section. Read-only diagnosis established direct shared-stderr writes; it did not establish the cause of any real Runtime outage. This plan repairs the displayed-input interference, not a claim that all network outages are fixed.

**Status (2026-10-12):** Implemented, verified and independently reviewed Approved with no findings. Real script-PTY RED showed heartbeat text next to child input; terminal/file/pipe/buffer/revoke GREEN, focused4.565s/full-package30.207s/race5.334s passed. Linux/Windows amd64 test-binary and CLI compilation passed. Three frozen files only; source/hash/report/review evidence is in this plan's SDD. Native behavioral coverage is Darwin arm64 with synthetic child/control, not installed Codex/Claude or native foreign OS qualification. No release or Runtime-outage-cause claim.

## Global Constraints

- Never send input or redraw keystrokes to Codex/Claude, clear a user's screen, restart the client, replay requests, or extend/ignore a Runtime lease. Permanent rejection still follows the actual failure channel and stops supervision/child as currently defined.
- Gate only the two asynchronous heartbeat interrupt/restore diagnostics. Keep startup guidance, actual fatal errors after terminal release, child stderr bytes, capture create/attach/finish ownership, retry timing and signal/terminal cleanup unchanged.
- Check actual stream terminal identity with existing x/term, not TERM strings, executable names or a Codex-only heuristic. A redirected writer retains exactly one interrupt/restore pair for each actual recovery, as before.
- No log file, telemetry registry, public flag or API is needed for this bounded request. Existing noninteractive diagnostic output stays available; no credentials, real commands or user history enter new test output.
- Use isolated synthetic child/control server and PTY. Do not touch running user terminals/App/9666, user login, trust, settings or proxy.
- Shared workspace has one source/test owner. Root controls Git; preserve all unrelated changes and current quota work. No full unrelated Go/Flutter/kernel campaign.

## Task 1: Respect the child's terminal during transient recovery

**Files:**
- Modify `internal/runlauncher/launcher.go`: the two heartbeat Fprintln calls only, routed through a private diagnostic helper.
- Create `internal/runlauncher/heartbeat_diagnostics.go` if keeping the stream test out of the large launcher.go is clearer; no public configuration.
- Extend `internal/runlauncher/launcher_integration_test.go` nonterminal assertions as needed; add `internal/runlauncher/heartbeat_terminal_darwin_test.go` for actual local PTY integration. Keep its package consistent with the existing controlFixture helper.
- Use existing `terminal_restore_darwin_test.go`, `process_unix_internal_test.go` and native Codex PTY fixtures as references; no unrelated edits to them.

**Interfaces:** Launcher.Config/Stdin/Stdout/Stderr and heartbeat failure channel remain unchanged. The private helper may be:

```go
func (launcher *Launcher) writeHeartbeatDiagnostic(message string) {
    if output, ok := launcher.config.Stderr.(*os.File); ok && term.IsTerminal(int(output.Fd())) {
        return // The active child inherits this terminal; it owns its screen.
    }
    _, _ = fmt.Fprintln(launcher.config.Stderr, message)
}
```

Imports are fmt, os and the existing golang.org/x/term. Normal New already supplies a nonnil stderr and keeps *os.File unwrapped; other writers use the existing lockedWriter. The heartbeat helper is called only by the active child-supervision loop, not a new global suppression policy.

- [x] **Step1 — real terminal RED.** Adapt the existing isolated `/usr/bin/script` subprocess pattern to run a synthetic foreground child with actual TTY stdin/stderr. Exercise the same temporary503/bounded timeout/429/recovery sequence as TestLauncherKeepsChildThroughTemporaryControlFailure. Keep a literal input sentinel queued for the child and prove it receives it exactly once, one capture/create/attach/finish, no client restart. Assert terminal output contains the child's prompt/sentinel but no asynchronous Runtime interrupted/restored text. Current code must fail the text assertion for the real TTY; missing fixture setup or no heartbeat recovery does not count as RED. Retain exact command/output.
- [x] **Step2 — minimal presentation fix.** Add the private helper and replace only the two heartbeat diagnostic calls. Keep reconnecting boolean, retryableControlFailure, timer reset, bounded requests, failure channel and child exit/cleanup byte-for-byte where possible. Do not suppress a fatal supervision error or change the child's assigned streams.
- [x] **Step3 — identity and lifecycle controls.** Run the real PTY test; retain existing nonterminal buffer test proving one interrupt/restored pair and same child lifetime. Cover stderr redirected to a real file, pipe/buffer output, and permanent revocation with actual failed supervision/child stop. Verify existing terminal mode restoration and admitted input remain correct. No source-text grep as behavioral evidence and no live provider traffic.
- [x] **Step4 — scoped final verification.** From worktree root run:

```sh
/tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/runlauncher -run '^(TestLauncherKeepsChildThroughTemporaryControlFailure|TestLauncherHeartbeatTerminal|TestLauncherRestoresAnIsolatedTerminal|TestInteractiveChildOwnsTheTerminalForeground)' -count=1 -v
/tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/runlauncher -count=1
/tmp/vibermate-go-security.JcRyfL/go/bin/go test -race ./internal/runlauncher -run '^(TestLauncherKeepsChildThroughTemporaryControlFailure|TestLauncherHeartbeatTerminal)' -count=1
```

The new actual terminal test should use the named TestLauncherHeartbeatTerminal prefix so the exact selection executes it. Compile this affected package/CLI for existing Linux/Windows amd64 targets into a new owned temporary directory; do not execute foreign binaries or claim native tests on those OSes. Use existing opt-in/native test boundaries honestly. No CodeGraph index exists in this worktree, so use normal rg inspection.
- [x] **Step5 — freeze/report/review.** Record exact source BASE, owned files/snapshots/hashes, behavioral RED, final commands/exit/output and platform/opt-in limits. Root independently reviews the fix-only diff and actual results, then commits. This closes terminal interference only, not Runtime outage diagnosis, entire client matrix, final Goal or public release.

## Self-review

| Contract | Check |
| --- | --- |
| heartbeat → shared terminal | Only supervisor's transient lines are gated; child output and real errors remain |
| reconnect → lease/capture | No policy or lifetime mutation; existing create/attach/finish and revoke tests survive |
| stream destination → evidence | Real TTY RED plus real redirected/nonterminal controls, no environment-name guessing |
| task timing → quota task | Starts after sole writer release; no concurrent Go/Flutter suites |

User intent is uninterrupted input, not removal of diagnostic evidence or retry protection. Existing visual/terminal semantics provide the boundary; no redesign of agent terminals is required.
