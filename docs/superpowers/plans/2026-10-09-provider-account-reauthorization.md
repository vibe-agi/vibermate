# Existing Account OAuth Reauthorization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task, with independent spec/security and quality review.

**Goal:** Let the user sign in again on the original upstream OAuth account, preserving its identity, configuration, links and history.

**Architecture:** Extend the existing short-lived PKCE login transaction with explicit create/reauthorize intent. The server freezes the existing account destination, version and network scope before opening the browser; completion validates upstream identity and replaces only credential material through the existing account/secret-store CAS boundary. The UI reuses the existing login panel on the selected row.

**Tech Stack:** Go1.26.8; pinned Flutter/Dart; existing codexoauth/provideraccount/SecretStore/control contracts. No new runtime dependency or database migration.

**Spec:** `docs/superpowers/specs/2026-10-09-provider-account-reauthorization-proposal.md` (user approved continuing on2026-10-09).

**Checkpoint2026-10-10:** backenddc71b24 andUI930f4b1 are implemented withrealRED→GREEN and independentapproval. Minor conflict-reloadfeedback corrected79fb19c, directedrereviewpending; allsourcewriters/testhandlesclosed. Candidateversion1a8272e is0.1.25+27 andmetadatareviewapproved. Releasequalification follows `2026-10-10-login-fixes-release.md`; no publication orfullGoalcompletionclaim. Detailed evidenceandcoverageboundaries areinthisplan'sSDDreports, includingoriginalfailures.

## Global Constraints

- Sole source writer/test owner; work only in `/Users/null/Code/github/vibe-agi/vibermate/.worktrees/long-session-hotfix`, starting at1cda1f6. Preserve f90d972/b29fff8, unrelated untracked VLESS docs and the other dirty worktrees.
- Same-account renewal, not silent identity replacement. Compare upstream account/workspace ID and known user ID, never email alone. No duplicate account or deletion/recreation workaround.
- Preserve account ID/name/notes/state/associations/traffic references/egress/automatic-refresh preference/header policy/history. Failed/cancelled/mismatched authorization must not alter them.
- Owner/session/PKCE/state binding remains; callback cannot select a destination. No raw secrets in errors/logs/public DTOs. No automatic resend of consumed authorization codes.
- Existing-account code exchange uses its frozen account-selected exit; no proxy-failure Direct fallback. External browser networking is unchanged. Do not copy unrelated unfinished global-egress features into this stable fix.
- No production service/data/trust/account mutation, dependency upgrades or version/release changes in implementation tasks. Synthetic endpoints/credentials and temporary storage only. Retain original RED/GREEN output.

## Public contract shared by the tasks

Continue using `POST /api/v1/codex-oauth/logins` and existing status/callback/cancel paths. Existing create requests omit mode or send `mode:"create"`, retain their existing fields and If-Match0. Reauthorization sends:

```json
{"mode":"reauthorize","accountId":"account.codex.existing","callbackMode":"manual"}
```

Its If-Match is the selected account's `credentialEpoch`, not a settings revision. It must be positive and current at admission. Reject supplied `upstreamEndpointId`/`displayName` for reauthorization: destination, name, credential-origin/driver/realm and network choice come from the existing server account. Desktop may use callbackMode loopback, Web manual. Keep the same LoginView shape; add only closed failure reasons `login_identity_mismatch` and `login_account_changed`. An ordinary persistence failure remains `login_account_save_failed`.

### Task 1: Backend same-account authorization and credential-only commit

**Files:** `internal/desktopcontrol/codex_logins.go` and its tests; `internal/codexoauth/login.go` and its tests; `internal/productruntime/codex_oauth.go`, `runtime.go`, new focused `codex_oauth_reauthorization_test.go`; narrow account-scope helper in `internal/provideraccount/settings.go` and tests only if needed to reuse existing scope selection. Keep broader provider transport unchanged unless a concrete failing account-scoped exchange proves a required change.

Confirmed implementation seam: extend `provideraccount/types.go` and `manager.go` with a narrow optional replacement precondition for the frozen account incarnation and secret reference, checked under the existing operation lock. A separate Get/check before ReplaceSecret is not atomic against delete/recreate. Existing callers' zero precondition keeps existing behavior; reauthorization supplies the complete frozen condition and still uses credential-epoch CAS. Add a focused race/identity regression; do not redesign the account repository.

**Interfaces:** Extend private `codexoauth.LoginAccount` with intent and frozen reauthorization data (existing credential reference/epoch, upstream identity, trusted `providerauth.AccountRef`, and account incarnation evidence). Keep `LoginController` Start/Status/Complete/Cancel signatures and LoginView structure. Completion persistence receives the selected target and complete Credential as today. Give `persistCodexLogin` access to the existing SecretStore so it can read the exact old material and retain its HeaderPolicy. The frontend consumes only the public contract above.

- [ ] **Step1 — regression before product edits.** Extend the real control fixture in `codex_logins_test.go` after its original account is created. It should accept reauthorization for that same account without allocating a new ID. The existing helper can construct this explicit request:
  ```go
  r := httptest.NewRequest(http.MethodPost, desktopcontrol.CodexLoginPath,
      strings.NewReader(`{"mode":"reauthorize","accountId":"account.codex.login","callbackMode":"manual"}`))
  r = r.WithContext(desktopcontrol.WithOAuthSession(r.Context(), "session-one"))
  r.Header.Set("Content-Type", "application/json")
  r.Header.Set("If-Match", "1")
  r.Header.Set("Idempotency-Key", "reauthorize-existing-once")
  w := httptest.NewRecorder()
  app.ServeHTTP(w, r)
  if w.Code != http.StatusCreated { t.Fatalf("reauthorize start status=%d", w.Code) }
  ```
  Also exercise the actual production persistence adapter with a real temporary account/secret store, old expired/reconnect credential and fresh same-identity credential. Assert same account object (including headers/settings/associations) and advanced credential epoch, only one account, and cleared permanent refresh state. Do not implement replacement in a fake Persist closure and call that a production test.
- [ ] **Step2 — RED.** Run focused new tests with the pinned Go executable, retain expected422/409/new-account-only failure. Compile setup errors are not RED evidence.
- [ ] **Step3 — admission/freeze.** In create mode retain existing endpoint validation/account-absence behavior. In reauthorize mode validate existing OAuth account + expected epoch, inspect its stored identity without forcing a refresh, and freeze trusted metadata/scope. Do not require a newly created/active routing service just to renew an existing account. A stable-line account with no explicit exit uses the existing credential-scope fallback; an explicit selected exit is retained. Never copy an expired access token into the authorization-code request.
- [ ] **Step4 — exchange/commit.** Pass the frozen AccountRef to the existing token HTTP client on reauthorization (creation retains its current empty scope). Validate the returned profile against the original account/workspace and known user identity. Re-read/verify the destination account and exact credential version before writing. Read old Material at that revision, keep HeaderPolicy, construct Material with the new credential, then call the existing account replacement/CAS authority; destroy/clear all temporary values. Preserve current account fields, and use existing preparer Forget after the successful commit. Deleted/recreated or concurrently updated accounts must not be overwritten; no account creation fallback.
- [ ] **Step5 — focused negatives.** Test wrong identity, stale epoch, deleted/recreated account, duplicate callback (one token exchange), cancellation/denial, storage failure, concurrent refresh/update, owner/session mismatch, read-only capability denial, and selected-account egress scope. Prove preservation by actual store/account comparisons; use a closed transport fixture so wrong/direct routing cannot pass accidentally. Keep new-account OAuth regression.
- [ ] **Step6 — normal verification/self-review/commit.** Run affected packages once after focused GREEN, format/check diff; write exact command/results/source/coverage limits to task1 report and commit only owned paths. Return public contract deviations before UI task begins. This backend-only intermediate commit is not a release candidate without Task2.

### Task 2: Existing account “Sign in again” UI

**Files:** `ui/flutter_app/lib/core/api/control_api.dart`, `control_models.dart`; `features/workbench/workbench_controller.dart`, `provider_account_editor.dart`, `provider_accounts_view.dart`, `codex_oauth_login_panel.dart`; `preview/preview_control_api.dart`; `core/i18n/app_copy.dart`; focused API/editor/row/Chrome tests.

**Interfaces:** Consume Task1's mode/If-Match/failure reasons. Add `startCodexReauthorization({required ProviderAccount account, required String callbackMode})` to the ControlApi implementations and WorkbenchController; it uses the existing ID/credentialEpoch and never calls UUID allocation. Extend CodexOAuthLoginPanel with optional existing-account target while preserving existing create callers.

- [ ] **Step1 — RED.** Add a widget/API regression for an existing reconnect_required row: click “Sign in again”, start authorization, and assert the outgoing request targets the original account/epoch and no account-create request occurs. Complete via a controlled LoginView and assert the original row, links/settings and account count remain stable. Existing replacement import stays available.
- [ ] **Step2 — API contract.** Implement the exact body from Public contract with If-Match account.credentialEpoch, add the two closed reason values to CodexLogin validation, and keep existing create behavior unchanged. HTTP fixtures must reject duplicate/create ID allocation rather than accepting any request.
- [ ] **Step3 — UI.** Expose “Sign in again”/“重新登录” as the primary recovery action for reconnect_required and a normal account action for OAuth accounts. Reuse the panel with a clearly named existing-account heading and locked target. On success refresh the original row and current quota observations; show “Signed in again”/“已重新登录”. No advanced credential-version terminology in the primary UI.
- [ ] **Step4 — failures/cancel.** Wrong identity explains that the browser signed into a different account; retry/cancel does not erase or mutate the original. Account-changed conflict reloads current state rather than overwriting it. Polling/disposal/cancellation must stop updating closed dialogs. Preserve existing callback modes, busy guards, import, header editing and existing errors.
- [ ] **Step5 — verify/self-review/commit.** Run affected Dart tests, a focused real-Chrome UI/API regression, format/analyze and Web build. Verify English/Chinese compact layout using existing fixtures; no unrelated redesign. Commit only owned files and write task2 evidence/limits. Request scoped independent review through root.

## Final integration

- [ ] Independent spec/security + quality review for each task and combined stable candidate; resolve actual blocking findings.
- [ ] Verify stable-line endpoint/Web fixes remain present, preserve the original full-suite failure logs, and use serial/controlled candidate validation rather than repeating competing full builds. Code review approval is not release qualification.
- [ ] Publish only after the candidate's actual applicable checks, provenance/signing/notarization/install and public distribution checks; re-read remote version at freeze. Keep the larger production Goal and VLESS continuation queued.
