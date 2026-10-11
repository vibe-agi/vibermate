# Shared Account Quota Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to execute the bounded task and independent review. One source/test owner; root owns Git commits.

**Goal:** Keep account and flow quota/credit displays consistent using the existing cache, automatically refresh every five minutes, and coalesce manual/automatic reads.

**Architecture:** Extend the existing WorkbenchController-owned account-facts cache and lifecycle, not a new backend/cache subsystem. Both actual views already consume that cache through the WorkbenchShell controller subscription. Add bounded scheduling and local freshness metadata; render existing credit facts in the compact flow card. The user approved this approach and selected five minutes; no new architecture approval is needed.

**Tech Stack:** Existing Flutter3.41.5/Dart3.11.3 and current locked dependencies; no dependency, HTTP schema, Go, database or credential change.

**Spec:** User-approved Goal scope10 in the current Goal attachment, and `2026-10-09-production-continuation.md` section “新增的用户可见闭环”. Concrete source-backed diagnosis is retained at `.superpowers/sdd/2026-10-11-stable-production-integration/quota-oauth-runtime-diagnosis.md`. StageA merge is `b5d5dbe191a4e62096e57b1b3eba599374e57c01`; do not repeat it.

## Global Constraints

- Exactly five-minute automatic freshness interval; explicit manual refresh bypasses freshness but joins a same-scope in-flight request. No page-specific timer, facts store, direct upstream HTTP or second observation cache.
- Scope remains accountID plus credential epoch, origin and settings revision. Late old-scope/invalidation/dispose completions cannot repopulate cache or restart work. Reuse existing request identity guard and four-at-a-time account batching.
- Keep all quota reads through ControlApi.accountFacts and existing Runtime authority/egress. History reads remain explicit, reset redemption remains explicit and confirmed, OAuth AutomaticRefresh setting is not changed or reinterpreted. Do not automatically sign in, rotate settings, redeem a credit, or contact real upstreams in tests.
- Automatic reads apply only to currently usable supported ChatGPT-origin accounts the current owner UI can query; Web member context must not start owner-only quota polling. Missing/failed data is not zero. Preserve last valid same-scope facts on failure and show the existing truthful stale/failure state.
- One controller lifecycle-owned timer; hidden desktop windows do not disable the quota schedule while the process runs. OS/browser suspension cannot guarantee execution: on resume perform one due check, not a catch-up burst. Fresh manual results must not be immediately fetched again by a pending periodic/resume tick.
- Cancel timer and stop future batches at disposal/sign-out; repeated initialization cannot install duplicate timers. Keep dashboard/evidence timers' existing independent behavior.
- Credit is the existing upstream balance, not quota percent or reset coupons. Reuse accountCreditsLabel/EN+zh terminology; no invented balance, lost precision, currency conversion or extra reset-details request from rendering. Narrow layouts retain readable text/action controls.
- Work only in the existing production-readiness worktree; preserve original workspace, running App, real accounts/trust/data and generated cache. No new release/version or broad test/kernel rebuild in this unit.

## Task 1: Shared scheduler, joined reads and compact credit presentation

**Files:**
- Modify `ui/flutter_app/lib/features/workbench/workbench_controller.dart`: existing quota methods/maps, existing `_clock`, initialize/dispose/resume lifecycle only.
- Modify `ui/flutter_app/lib/features/workbench/provider_account_facts.dart`: `ProviderAccountQuotaMini` credit rendering from supplied AccountFacts.
- Modify `ui/flutter_app/lib/features/workbench/provider_accounts_view.dart` and `environments_view.dart` only if needed to share eligibility/refresh/stale rendering; preserve current cache subscription and selection/order.
- Modify `ui/flutter_app/lib/core/i18n/app_copy.dart` only for new user-facing text, both languages.
- Tests: extend `ui/flutter_app/test/provider_account_settings_test.dart`, `environment_account_quota_test.dart`, `provider_accounts_view_test.dart`; add `ui/flutter_app/test/provider_account_quota_refresh_test.dart` for timer/clock lifecycle if the existing files are not a clear owner.

**Interfaces:** Existing `providerAccountQuota`, `providerAccountQuotaLoading`, `providerAccountQuotaFailed`, `invalidateProviderAccountQuota`, `refreshProviderAccountQuota`, `ensureProviderAccountQuotas` retain their call contracts. Same-scope concurrent refresh futures now resolve to the shared result instead of a second caller immediately returning old/null facts. Existing optional constructor clock supplies deterministic freshness tests. No public scheduler/testing API.

- [ ] **Step 1 — record real REDs.** First add a coalesced-result test in provider_account_settings_test.dart using its actual `_import` and `_DelayedFactsApi` fixtures:

```dart
test('concurrent quota refreshes join the same fresh result', () async {
  final preview = PreviewControlApi(seedCaptures: false);
  final account = await _import(preview);
  final api = _DelayedFactsApi(preview);
  final controller = WorkbenchController(
    api: api,
    terminalCommands: PreviewTerminalCommandService(),
    previewMode: true,
    closeRuntime: preview.close,
  );
  addTearDown(controller.dispose);
  controller.data = await preview.loadDashboard();
  final first = controller.refreshProviderAccountQuota(account);
  final second = controller.refreshProviderAccountQuota(account);
  expect(api.pending, hasLength(1));
  final facts = await preview.accountFacts(account.id);
  api.pending.single.complete(facts);
  expect(await first, same(facts));
  expect(await second, same(facts));
});
```

Current second caller returns old/null facts, so retain that behavioral failure. Also add a compact Mini test with a full valid AccountFacts containing the existing credits schema and require the same formatted balance as the account row; current Mini omits it. Run only the named RED tests with pinned Flutter, keeping fixture/setup errors distinct.

- [ ] **Step 2 — one cache owner with local freshness metadata.** Keep the existing facts map and scope validation. Extend the current in-flight record with a completer/future; publish it before notifying listeners and issuing the request, always settle it even when invalidated/disposed, and return it to same-scope joiners. Keep failures/old values and generation guards. Attach scope-bound local check time using `_clock`, not the remote provider timestamp, so manual success suppresses automatic work for five minutes and failure is retried on the next interval without a tight loop. Invalidate its timing with its account scope. Metadata is not another facts cache.

The fixed scheduling constant is:

```dart
static const _accountQuotaRefreshInterval = Duration(minutes: 5);
```

Use a private due check and one Timer.periodic in the existing initialize/dispose lifecycle. After successful initial inventory, start one due/missing-cache check without blocking initialize on upstream quota responses; lazy page loads join the same requests. Reuse ensureProviderAccountQuotas batching and existing ControlApi route; automatic callers request only due supported accounts while manual callers retain forced refresh. Check disposed state before starting requests and before each subsequent batch. On resume re-evaluate due accounts once using the existing clock. Do not route the quota timer through `_pollingVisible`, which intentionally gates unrelated display polling.

- [ ] **Step 3 — timer and identity tests.** Use Flutter test virtual timers plus the existing injected clock; advance both deterministically, never wait five real minutes. Cover: initial missing cache; no request before five minutes; exactly one per due account at five minutes; manual refresh immediately then no duplicate at the old tick; manual+periodic+both views join one in-flight result; failed refresh preserves old facts and retries on a later interval; credential/settings/origin change and explicit invalidation reject old completion; dispose during a held first batch starts no later batches; repeated initialize installs one schedule; hidden-window tick and one resume catch-up; unsupported/disabled accounts and Web member skip owner-only reads; no automatic history/reset/settings mutation. Fixtures must implement the actual initialization/poll methods they exercise, not hide Fake missing-method failures.

- [ ] **Step 4 — compact credits and cross-view consistency.** Extend ProviderAccountQuotaMini to display credits from its supplied facts using the existing balance formatter and localized label. Handle known balance, unlimited, confirmed no credits, missing credits, and credit-only observations without quota windows. Do not let the existing windows-empty early return hide known credit. Keep the current quota windows/countdown semantics and selected account order. Render actual account page and flow group against one controller; one refresh updates both with identical windows/credit and one account-facts call. Verify English/Chinese,390px and200% text scale with no overflows or lost activate/refresh controls. Preserve error/stale display without inventing zero. Use existing visual test font practice; any substituted font evidence is layout-only, not native typography.

- [ ] **Step 5 — proportionate verification and report.** Run targeted RED then GREEN while iterating, and once at final source:

```sh
/opt/homebrew/bin/flutter analyze --no-pub
/opt/homebrew/bin/flutter test --no-pub test/provider_account_settings_test.dart test/provider_account_quota_refresh_test.dart test/environment_account_quota_test.dart test/provider_accounts_view_test.dart test/provider_account_refresh_test.dart test/workbench_controller_test.dart
/opt/homebrew/bin/dart run tool/prepare_chrome_test_assets.dart
/opt/homebrew/bin/flutter test --no-pub --platform chrome test/provider_account_quota_refresh_test.dart test/environment_account_quota_test.dart
```

Commands run from ui/flutter_app. If timer cases live in an existing listed file instead of the optional new file, omit only the nonexistent path and record their exact names; retain the same behavior union. Format only owned Dart files and run git diff --check. No unrelated Go/kernel/full global-matrix rerun. Freeze owned file list, before/after snapshots/hashes, exact first failures and final commands/results, and actual render paths in the task report. Root performs independent spec/quality review and commits the scoped unit. Do not claim automatic refresh works after App/browser termination or that this publishes the new version.

## Self-review and recovery

| Shared contract | Check |
| --- | --- |
| Existing controller cache → both views | Facts remain singular; credit omission is renderer-only; ancestor notification already exists |
| Manual refresh → automatic cadence | Same in-flight result, scope-bound local time; no stale completion can postpone a newer scope |
| Controller lifecycle → timer/batches | One timer; hidden continues while runnable; resume due check; disposal stops further work |
| Upstream facts → credit text | Reuse strict DTO/formatter; distinguish unlimited/zero/unknown and coupons |
| Tests → production code | Existing clock/timers/API, no live data or new public testing hook |

This bounded plan implements the user's confirmed five-minute/shared-cache/credit request. Terminal diagnostics and remaining global-egress/MCP/final qualification keep their own scope; this unit neither replaces nor expands them.
