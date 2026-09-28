# Long-running Runtime CONNECT 503 hotfix

## Evidence and failure path

- Affected installed build: 0.1.15 (16), source `23e8fb72099292ec0a1b153f1d1e3a14c001ddc8`.
- Local CONNECT returned `503 / proxy_stopping` before upstream transport or authentication. The daemon was still listening. Database `quick_check` passed; disk space was available.
- Conversation-directory SQL used a correlated completion lookup that did not satisfy SQLite's partial-index predicate, and applied capture scope after the lifecycle union.
- Against the incident database, read-only reproduction using the production SQLite driver took 12.1768 s. A concurrent operation on its single connection exceeded a 5 s deadline.
- Egress terminal persistence has a 5 s lifecycle bound. Any terminal persistence error latches storage unhealthy and cancels the Runtime owner, shutting down proxy admission and active connections. The exact first live write error was not available; the contention and shutdown mechanism were independently reproduced.

## Scoped fix

- Reuse the indexed lifecycle-winner query for conversation grouping, with capture scope applied before grouping and the partial-index predicate explicit.
- Share a small read-only connection pool across activity and usage queries; keep writes on the existing serialized writer. Report reads no longer perform retention DELETEs; expiry predicates, write-side cleanup and explicit cleanup retain their responsibilities.
- Preserve core audit failure policy. This fix removes query/writer contention, not the intentional dependency between durable egress auditing and proxy availability. Optional observation failures and security-critical enforcement remain distinct concerns.
- No database structure change, migration, history deletion, account reset, TCP timeout change, or credential-backend change.

## Verification

- New regression checks failed on the original implementation and passed after the fix: directory reads while the writer connection is occupied; grouping 12,000 lifecycle rows (4,000 exchanges).
- Fixed query against the same incident database returned the same 25 conversations in 47.95 ms, read-only.
- Independent hotfix `go test ./... -count=1`: passed.
- Main-worktree affected-package tests and targeted persistence/runtime race tests: passed.
- Developer-ID-signed candidate: all 7 real Flutter Runtime / packaged CLI / ACP integration tests passed. An earlier ad-hoc-signed candidate failed Keychain access and was not installed.
- Standalone native HTTP smoke: setup/authentication, bundled Web assets, CA export, restart persistence passed.
- Chrome Web UI with an isolated candidate Runtime: login, 7/365-day report navigation, manual refresh, return to captures, global refresh passed. This UI fixture had no user history; large-history verification was the read-only query and regression tests above.

## Local delivery

- Installed 0.1.15 (17) in `/Applications/ViberMate.app` and repository `dist/ViberMate.app`; existing Developer ID, signatures verified. Not automatically started.
- Prior bundles preserved under `dist/backups/20260928-connect-503-before-build17/` as `Applications-ViberMate.app` and `dist-ViberMate.app`.
- Installed/candidate daemon SHA-256: `b2d337e74aec550aa5e3b5acdbb0ff1eb398a06b18fea8be5c67285ad7e25eb2`.
- Independent source worktree: `/tmp/vibermate-connect-diagnostic.oi0p6i/hotfix`. The functional fix also exists in the main worktree, whose unrelated unfinished schema/report work was not shipped.
- Local hotfix only: no public release, new notarization, Git push, or Homebrew update in this incident task. End-user workload retest and sustained-run validation remain to be observed.

## Main-source follow-up — VIBERMATE-46

Implemented and verified after local build 17 delivery; these changes are not in the installed App or a public release.

- Optional activity, conversation identity, usage, response-content and raw-evidence recording failures latch a recording warning. They do not revoke Runtime readiness or cancel the proxy owner. Existing bounded writes remain synchronous; this is failure-policy separation, not complete I/O independence.
- Security-critical egress-audit failure still stops proxy admission. The public status records the first operation, closed reason code and UTC occurrence time for the current Runtime incarnation; later failures and successful health reads cannot overwrite it. Raw errors, SQL, paths and credentials are excluded. Restart begins a new incarnation; missing records are not backfilled.
- App/Web show distinct recording-warning and proxy-stopped states. A valid status survives inventory-load failure, including first load; refresh does not fabricate healthy status or empty inventory.
- The separately issued local CLI credential can read only the exact `GET /api/v1/status` route under existing transport/host guards. `status`/`doctor` report the same diagnosis; unhealthy Runtime no longer receives a misleading all-clear or launch-next instruction. Other CLI reads/writes, browser-origin use and malformed credentials remain rejected.
- Reused the existing status route, observers and UI notices; no queue, retry service, dependency or runtime compatibility path was added.

### Verification

- `go test ./... -count=1`: passed.
- `go test -race ./internal/productruntime ./internal/desktophost ./internal/desktopcontrol ./internal/runtimepersistence ./internal/runlauncher ./cmd/vibermate -count=1`: passed.
- `flutter analyze --no-pub`: no issues. Full Flutter suite: 665 passed, 16 skipped under existing platform/integration conditions; skips are not acceptance evidence.
- Flutter Web release build and fresh main-source daemon build: passed. Widget checks cover English/Chinese and 640/1440 widths, recording-only degradation, inventory failure, cold start, refresh and recovery.
- Real Chrome Web acceptance against a fresh isolated daemon: a synthetic SQLite terminal-write failure during an actual loopback HTTP proxy request produced `storageFailure = egress_complete / write_failed`; `/api/v1/status` remained readable and subsequent forwarding returned 503. The public payload excluded the injected raw error. This follow-up exercised HTTP forwarding, not a new CONNECT reproduction.
- Browser checks confirmed the first-cause banner and stopped state in English and Chinese, unchanged after manual refresh and reload/re-login. Only the dedicated test tab was used and closed; the temporary daemon was stopped. No real account credentials or user database were used.

The main-source follow-up has not been packaged, installed, pushed or released. Full account/report/schema work and its native candidate/release gates remain open. A new 12-hour sustained workload was not run.
