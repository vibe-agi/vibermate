# Existing upstream account: sign in again

Status: proposed from the user's request to plan this behavior; no implementation approval or product changes yet. This complements the already implemented server URL/Web reload fixes and must not silently expand that release's scope.

## Why it is unavailable today

- `provider_account_editor.dart` hides browser login when replacing an existing account; replacement currently offers imported authorization data.
- `workbench_controller.dart:startCodexLogin` always allocates a new `account.codex.<uuid>`.
- `desktopcontrol/codex_logins.go:startCodexLogin` explicitly rejects an already existing account ID.
- `productruntime/codex_oauth.go:persistCodexLogin` always calls `accounts.Create`, while `provideraccount.Manager.ReplaceSecret` already supports versioned credential replacement without replacing the account object.

Therefore this is a missing end-to-end reauthorization mode, not simply an expired-token error message or a missing button.

## Proposed user experience

1. Keep the existing account row visible. When refresh is permanently rejected, show “登录已失效” and a primary “重新登录” action. Also allow intentional re-login from the account's action menu, including before expiry.
2. Start the usual browser authorization for that exact existing account. Desktop keeps the existing local callback experience; remote Web keeps its existing callback-completion flow.
3. On success, update the original row and show “已重新登录”. Do not create a duplicate row, delete the old one, or ask the user to relink traffic profiles.
4. Cancellation, browser denial, network failure, or persistence failure leaves the original account and associations intact. Show a plain retryable message; technical details are secondary.

## Identity and preservation boundaries

- Re-login means renew access to the same upstream identity, not silently change the underlying person or ChatGPT workspace. Compare the upstream account/workspace identity and known user identity; do not rely on email alone. A different identity must be rejected with “登录的不是原账号，请切换后重试”, without modifying the original account.
- Keep ViberMate account ID, display name, notes, linked services, traffic-profile references, network exit selection, automatic-refresh preference, enabled/disabled state, configured request-header policy, and historical evidence. Only credential material and its revision/state change.
- Reuse current secret-store/account authority for atomic replacement; coordinate with background refresh and concurrent re-login so stale operations cannot overwrite a newer credential. Account deletion or a genuine conflicting credential update during authorization must not resurrect or overwrite that account.
- Bind the pending OAuth transaction to the authenticated owner/session and exact existing destination before opening the browser. A callback cannot choose a different account or endpoint. Retain PKCE/state checks, replay handling, cancellation and secret clearing.
- Existing-account authorization is an account operation, not a routing-service creation. Fix its authority from the existing account's credential origin/driver/realm; do not force the user to create a new routing service or rewrite service links merely to sign in again. New-account login keeps its current selected-service validation.
- Authorization-code exchange for an existing account must follow that account's selected network exit; preserve the approved account/profile/global precedence when integrated with the global-egress work. It must not fall back to direct on proxy failure. External browser networking remains the browser's responsibility.
- No password/token/code output in logs, public account DTOs, or retained conversation evidence. No database reset or migration is expected for this bounded feature; pending login state is already ephemeral.

## Implementation slices after design approval

1. Extend the existing login target with explicit create versus reauthorize intent and frozen destination/version/identity information; keep the new-account flow unchanged. Control API must authorize and validate the existing OAuth account rather than treating any existing ID as a conflict.
2. On token exchange success, validate the intended upstream identity and perform a credential-only update through the account authority. Preserve existing request headers and settings; clear the refresh-failure state through the existing credential-preparer invalidation path. Keep completion idempotent and non-destructive.
3. Add the existing-row “重新登录” action and reuse CodexOAuthLoginPanel with an existing-account target. Refresh that row and invalidate its current quota observations after success; preserve recorded history and all unrelated user choices.
4. TDD coverage: expired/permanently unrefreshable account → same-ID ready credential; all fields/links/history/header policy preserved; wrong person/workspace rejected; cancel/network/denial/save failure preserve old state; concurrent refresh/re-login/deletion and duplicate callbacks do not overwrite newer data; account-selected proxy is used; Desktop and remote Web callback modes remain supported; new-account OAuth regression remains green.

## Separate from server login

This proposal concerns an upstream ChatGPT/OAuth account inside a Runtime. CLI `vibermate login --server A` and `--server B` already persist independent Runtime logins in `LoginStore`, keyed by canonical scheme+host+port. Reauthorizing an upstream account must not modify those CLI Runtime logins or the user's original Codex login files.
