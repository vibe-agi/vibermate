# Measured Packaged Acceptance Repair Implementation Plan

> **For agentic workers:** Root uses superpowers:subagent-driven-development for one bounded acceptance-repair task, with behavioral TDD, independent task/integration review and actual hosted verification. One source/compiler writer; implementers never dispatch subagents or hosted work.

**Goal:** Remove the two measured acceptance-client blockers so the unchanged normal packaged V7 gate can progress on the hotfix candidate.

**Architecture:** Align normal standalone acceptance launch with the existing working Desktop sidecar environment; retain an explicitly selected isolated diagnostic arm. Correct the acceptance Capture-list client to use the unified API contract and collect complete paginated snapshots, without changing the server or user-visible limits.

**Tech Stack:** Existing Go acceptance harness and httptest fixtures, pinned offline Go1.26.8; unchanged normal hosted macOS native-release workflow.

**Spec/evidence:** `.superpowers/sdd/2026-10-08-packaged-bootstrap-home-diagnostic/hosted-f009579-result.md` and `capture-assignment-diagnosis.md`. Both refer to actual common-artifact run37805923405 at sourcef009579a2301a5aacadfb1c2ddf8d429f6495024. Isolated HOME timed out after120025ms; login HOME reached ready in77ms then its initial `limit=200` Capture-list request failed because the server maximum is199. The read fails before launcher execution. No normal V7/release PASS exists yet.

## Global Constraints

- Only acceptance command files listed below may change. No product runtime, server request limit, native credential code, V7 schema/verifier, Flutter product code, workflow, dependency, timeout or release protection changes.
- Preserve explicit private cache/data directories, fd0/fd3 ownership, both bootstrap frames, two-minute deadline, existing15-second waits, stderr cap, decoder drain and bounded confidential diagnostics. Do not replace native secrets or run App/daemon/JXA/Keychain locally.
- Normal/default acceptance must preserve exactly one original absolute nonempty HOME and remove only CFFIXED_USER_HOME, matching Desktop sidecar behavior; never modify the parent environment or invent another login home. This is acceptance launch repair, not a claim that a specific native call was measured.
- Explicit diagnostic policy selection still requires the bounded diagnostic output and deterministic mode. The normal zero/default configuration requires neither diagnostic output nor a new flag. Existing isolated behavior is available only as explicit diagnostic selection.
- Capture pagination must not truncate successful evidence or change user conversation limits. Both existing before/after callers receive complete typed CaptureResponse values, including entries after the first page. Do not raise the server's199 limit or alter its limit+1 lookahead.
- Preserve actual first failures, raw logs, final source hashes and command handles. Only one local compiler/test job at a time; root may observe already-running external CI concurrently. Preserve unrelated VLESS docs, original worktrees, user data, live App9666 and real diagnostic recipient key.

## Task 1: Repair the normal acceptance client

**Files:** modify `cmd/vibermate-acceptance/config.go`, `bootstrap_diagnostic.go`, `bootstrap_diagnostic_test.go`, `control.go`, `control_test.go`. `config_test.go` or `daemon_test.go` may receive focused tests if needed; do not change `daemon.go` startup ownership or runner probe semantics merely to make tests easier.

### A. Normal launch environment

- [ ] Add behavioral tests before changing defaults. `defaultConfig()` and programmatic zero policy must both route a synthetic parent `[HOME=/Users/disposable, PATH=/usr/bin, CFFIXED_USER_HOME=/private/app, TOKEN=sentinel]` to a copy with unchanged HOME/PATH/TOKEN and no CFFIXED. Assert the input remains unchanged and no replacement acceptance-home directory is created. Avoid printing environment values on failure. Current default/zero behavior must fail these assertions while explicit login behavior already passes.
- [ ] Make `defaultConfig().diagnosticDaemonHome` empty (normal, not a diagnostic selection). In the helper, empty policy aliases login, not isolated:

```go
switch policy {
case "", daemonHomeLogin:
    // Existing validated login copy/filter body, unchanged.
case daemonHomeIsolated:
    return isolatedDaemonEnvironment(base, dataDirectory)
default:
    return nil, errors.New("unknown diagnostic daemon HOME policy")
}
```

Keep this mapping consistent in initial diagnostic metadata: empty policy records login. Preserve the current named login helper validation and default environment construction; do not duplicate the copy/filter implementation.
- [ ] Validation distinguishes normal empty policy from explicit diagnostic selection. Reject unknown values; require a diagnostic path for either nonempty selector, and require deterministic mode whenever a diagnostic path is supplied. Validate original HOME for normal or login modes. The original output collision/private-path checks remain unchanged. Table tests cover normal no-output acceptance, both explicit policies with valid diagnostics, explicit selector without output, non-deterministic diagnostics, and missing/empty/relative/duplicate original HOME. Existing explicit-isolated equivalence/private-home tests remain; remove only their obsolete zero-policy-is-isolated expectation.
- [ ] Run focused environment/config/diagnostic tests to GREEN and race once on final repaired harness. No native child is required to prove environment/config behavior; actual normal V7 is a later hosted gate.

### B. Complete Capture-list snapshots

- [ ] Extend existing `testControlClient` real HTTP fixture before implementation. Reject any explicit limit above199 with the real closed422 problem document. A clean empty catalog must succeed, whereas current `limit=200` must fail. Then serve a multi-page catalog with managed/manual typed keys and preserved evidence; the later page must be required for the expected full result, making current single-page behavior fail separately.
- [ ] Keep `controlClient.captures(ctx) (desktopcontrol.CaptureListResponse, error)` and existing callers. Omit `limit` to consume the server's default50 instead of copying a second maximum constant. Relay each nonempty opaque cursor with `url.Values`/QueryEscape:

```go
query := url.Values{}
if cursor != "" { query.Set("cursor", cursor) }
path := "/api/v1/captures"
if encoded := query.Encode(); encoded != "" { path += "?" + encoded }
```

Append every full CaptureResponse in received order and return success only when the terminal page has empty NextCursor; aggregate NextCursor is empty. Both existing snapshots then include all pages without runner changes.
- [ ] Fail closed on context cancellation, later HTTP/problem/JSON failure, invalid typed key or key/kind/ID disagreement, duplicate typed key, repeated/cyclic cursor, empty nonterminal page, malformed/oversized cursor, or a page exceeding the documented default50. Use existing captureidentity parsing, not a custom key format. Error returns contain no successful partial snapshot and do not print capture data or cursors.
- [ ] Use the existing acceptance evidence-reader precedent of32 pages as an **isolated acceptance-fixture resource bound**, not a product/user history or message limit. A terminal page at32 succeeds; a nonterminal page32 returns an explicit incomplete-evidence error and no partial result. This bounds a misbehaving test runtime without silently discarding history. No new timeout is introduced; callers retain their existing context.
- [ ] Tests cover >199 total records across legal pages, same raw ID under two distinct capture kinds, exact preserved payload/order, URL-escaped cursor relay, empty terminal catalog, final full page, later-page failure, duplicate/cyclic continuation, duplicate typed keys, malformed page/cursor, canceled context, exact32-page terminal success and nonterminal exhaustion error. Fixtures derive expectations by hand, not by the implementation helper. Do not run the native launcher to test pagination; preserve existing exact-one-new-managed-run and assignment checks by returning the complete catalog to their unchanged callers.

### Verification and handoff

- [ ] Preserve both real behavioral REDs and GREEN commands/logs. Use pinned offline Go1.26.8, GOFLAGS=-mod=readonly/GOTOOLCHAIN=local/GOPROXY=off/GOSUMDB=off, DEVELOPER_DIR=/Library/Developer/CommandLineTools.
- [ ] On final code run focused normal/race environment and capture-client tests, then affected non-live acceptance/verifier/report tests once with the four explicit JXA/live exclusions already documented in the diagnostic plan. Do not call that exclusion run full native/package PASS. Run go mod tidy -diff and git diff --check; no dependency change is expected.
- [ ] Self-review exact file scope, unchanged deadlines/native/V7/product/server boundaries and complete evidence semantics; commit only owned files. Full report and raw evidence go in this plan's own SDD workspace. Return terminal handles, commit, hashes, results and remaining limits. Root independently reviews before any hosted run.
- [ ] Root then runs the unchanged normal packaged deterministic workflow on the reviewed source, with no diagnostic override, and its fresh V7 verifier. Keep failure exits honest, preserve encrypted/secure evidence requirements, signing/notary/release approval protections and actual source identity. The old diagnostic result only justifies this repair, never substitutes for that normal gate.

## Root preflight/self-review

Both fixes are acceptance-client corrections in one delivered harness; one source writer and one review cover the common final artifact. A touches config/environment tests; B touches control/pagination tests, with no conflicting source ownership. Default normal and explicit diagnostic modes are intentionally distinct; recorder metadata must use the same effective policy as the environment helper. Capture server maximum/lookahead stay unchanged; bounded traversal errors are not successful partial snapshots and the32-page bound is not exposed to users. The data/cache/fd/native/report interfaces remain unchanged. Existing normal workflow is consumed, not edited. No private recipient key is part of this task. Actual hosted failure evidence, not performance targets, drives both changes.
