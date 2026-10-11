# Server Login Continuity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Fix omitted default CLI ports and same-tab Web refresh logout on the stable release line.

**Architecture:** Normalize HTTP(S) defaults at the existing CLI Target boundary. Restore existing Web bearer capabilities from a dedicated tab-scoped store only after validating them against the server; retain all existing authorization and expiry rules.

**Tech Stack:** Go 1.26.8; pinned Flutter/Dart; existing `http` and `web` packages; Chrome.

**Spec:** `docs/superpowers/specs/2026-10-09-server-login-continuity-design.md`

**Current status:** implementation commits f90d972 and b29fff8 passed scoped/combined review, affected tests, analyzer, Web build, and the isolated real-backend/actual-browser-reload probe. The subsequent full Go/Flutter run was not fully green: two unchanged Go test families and one unchanged Flutter timer test failed; all three then passed isolated reruns with no source/timeout changes. Preserve this distinction. Release qualification is still open; no new version has been published.

## Global Constraints

- Work only in `/Users/null/Code/github/vibe-agi/vibermate/.worktrees/long-session-hotfix`, starting at `dbf9745b5391723103967889939a5ab4c7fbb7bc`; preserve unrelated untracked VLESS requirement documents and the production-readiness worktree.
- One source writer/test owner at a time; retain actual RED/GREEN evidence. Only temporary/synthetic credentials and isolated listeners. No production service restarts or user data/trust changes.
- No dependency upgrades, database migrations, session lifetime changes, cookie-auth redesign, automatic direct/network downgrade, or release/version changes in these tasks.
- Never store passwords/recovery keys; never expose tokens in diagnostics. Saved Web capabilities stay origin-bound and tab-scoped. The server decides identity and permissions.

### Task 1: Normalize CLI default ports

**Files:** Modify `internal/serverconnection/target.go`; test `target_test.go`, `login_store_test.go`, and `cmd/vibermate/main_test.go`. Preserve the existing `ParseTarget(string) (Target, error)` API.

**Interfaces:** All CLI command parsers already consume `ParseTarget`. `Target.Origin()` remains an explicit-port canonical string, consumed by login persistence and trust/transport.

- [x] Add literal table cases and login-store/command regressions before implementation:
  ```go
  // Every pair must produce the same comparable Target and saved-login key.
  // https://runtime.example.test -> https://runtime.example.test:443
  // http://runtime.example.test -> http://runtime.example.test:80
  // https://[::1] -> https://[::1]:443
  // https://runtime.example.test:9443 -> unchanged
  // Reject https://host:, https://host:0, malformed IPv6, userinfo,
  // credentials, paths, queries/fragments, and scheme-less host.
  ```
- [x] Run focused new tests and retain the expected missing-port RED (not a compile failure).
- [x] Implement only explicit-scheme defaulting, using `net.JoinHostPort(parsed.Hostname(), defaultPort)` when no port is present. Reject syntactically explicit empty ports and unbracketed IPv6 before defaulting. Continue using strict `ParseAddress` for the result.
- [x] Run `go test -count=1 ./internal/serverconnection ./cmd/vibermate ./internal/servertransport`; self-review and commit only task files. Record exact command/results and commit in the task report.

### Task 2: Restore Web sessions across reload

**Files:** Modify `ui/flutter_app/lib/core/bootstrap/platform_runtime_web.dart`; add focused Web session store/support under the same directory only if needed. Add tests under `ui/flutter_app/test` and a real-browser smoke under `ui/flutter_app/tool` or `tool/server` if needed. Existing `connectPlatformRuntime({RuntimeLoginAttempt? login, String? daemonPath})` callers must keep working.

**Interfaces:** Consume existing `GET /api/v1/server/web-sessions/current` (returns `id`, `username`, `role`), existing `DesktopSession`, and `HttpControlApi.connect(inspectSession: false, selfScoped: !principal.owner)`. No new server endpoint needed. A transport/store seam may be introduced for deterministic tests without test-only lifecycle methods.

- [x] Add a regression using the real Web connector and synthetic HTTP boundary, with hand-derived complete session fixtures:
  ```dart
  // Sign in once, dispose the page connection without logout, reconstruct
  // the connector with no credentials, validate /web-sessions/current,
  // and assert the returned principal and authenticated API access.
  // No new login POST, password, or recovery key may be needed or stored.
  ```
- [x] Observe and retain RED: the second bootstrap currently throws `credentials_required`.
- [x] Implement origin-bound tab persistence and restoration, bounded decode/expiry validation, server identity confirmation, successful login/password-change replacement, and clearing on logout or confirmed session invalidation. Keep a transient restore failure distinct from expired/rejected credentials. Storage failures may degrade to in-memory login, not block sign-in.
- [x] Add/execute regressions for valid owner/member restore, corrupt/expired/foreign-origin records, server 401, network/5xx retention, logout, password replacement, API invalidation, and unavailable storage. Use real browser sessionStorage for browser-specific behavior. Do not weaken tests to assert source strings.
- [x] Run affected Flutter tests, Chrome test(s), formatting/analyzer, and a Web build/smoke covering an actual page reload if available. Self-review and commit task files only; report test evidence and uncovered boundaries.

### Completion

- [x] Independently review each task for spec/security and quality; resolve concrete blocking findings.
- [x] Review the combined stable-line diff, preserving all unrelated work. Record targeted validation and browser smoke results in the ledger.
- [ ] Report exactly what is implemented/verified and what has not been released; do not mark the full production Goal complete or call a local commit a published version.
