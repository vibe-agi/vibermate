# Codex OAuth account management for ViberMate phase 1

Date: 2026-09-21. This report records the source-level research and target integration
direction. The narrower implementation slice accepted for this repository is recorded in
ADR 0011; this document does not by itself claim OAuth client registration or provider support.

## Recommendation

Make ViberMate the credential authority for every Codex OAuth account that ViberMate routes.
An account must be a first-class server-side object whose access token, refresh token, ID token,
ChatGPT account/workspace ID, and FedRAMP routing bit are updated and consumed as one versioned
credential snapshot. A traffic policy should refer to that account object, not contain copied
`Authorization` or `ChatGPT-Account-ID` header values.

For the complete managed-login product:

- use browser + loopback PKCE for the native App, falling back to device-code login when the
  registered loopback ports are unavailable;
- use device-code login for native Web, Docker Web, remote servers, and team deployments;
- manage refresh-token rotation, proactive refresh, and one 401 recovery centrally;
- keep OAuth secrets in ViberMate's secret store and expose only derived account metadata;
- allow explicit fixed-account selection, but defer round-robin, quota balancing, and automatic
  failover;
- treat importing Codex `auth.json` as advanced one-time bootstrap input, not as a live shared file;
- do not depend on Codex's experimental `chatgptAuthTokens` app-server API.

This is an architectural recommendation inferred from the reviewed source, not a statement that
OpenAI supports third-party products using Codex's published OAuth client ID. The OAuth endpoints,
client ID, scopes, redirect URIs, and response shapes are upstream implementation details. Keep
them isolated behind a versioned Codex OAuth adapter, add contract tests, and validate product and
service-policy authorization before release.

## Reviewed source revisions

| Project | Revision reviewed | Why it is authoritative here |
| --- | --- | --- |
| OpenAI Codex | [`ebc05da3bdb76f25861e7cb418bd06d28cadc609`](https://github.com/openai/codex/tree/ebc05da3bdb76f25861e7cb418bd06d28cadc609) | Current first-party implementation of Codex login, storage, refresh, and request authentication at research time. |
| router-for-me/CLIProxyAPI | [`a5ab69521f7b4e0f244836d0419da8fcd89408ea`](https://github.com/router-for-me/CLIProxyAPI/tree/a5ab69521f7b4e0f244836d0419da8fcd89408ea) | Current implementation of multi-credential scheduling and Codex token refresh at research time. It is useful prior art, not the protocol authority. |

Codex is Apache-2.0 and CLIProxyAPI is MIT at these revisions. Source copying must retain the
applicable notices; those code licenses do not by themselves grant use of an OAuth service or
registered client identity. [Codex license](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/LICENSE),
[CLIProxyAPI license](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/LICENSE).

## Verified Codex credential contract

### `auth.json` schema and storage behavior

The user's sample is a valid representation of managed ChatGPT auth, but it is not the complete
current schema. Codex's `AuthDotJson` currently contains optional `auth_mode`, the JSON key
`OPENAI_API_KEY`, optional `tokens`, and optional `last_refresh`; the same container now also has
agent identity, personal-access-token, and Bedrock fields. The ChatGPT `TokenData` contains an ID
token, access token, refresh token, and optional account ID.
[AuthDotJson](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/storage.rs#L39-L65),
[TokenData](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/token_data.rs#L10-L25),
[auth-mode wire names](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/protocol/src/auth.rs#L6-L38).

The relevant on-disk shape is therefore:

```json
{
  "auth_mode": "chatgpt",
  "OPENAI_API_KEY": null,
  "tokens": {
    "id_token": "<raw JWT>",
    "access_token": "<raw JWT>",
    "refresh_token": "<opaque secret>",
    "account_id": "<ChatGPT workspace/account id>"
  },
  "last_refresh": "<RFC 3339 timestamp>"
}
```

Codex's in-memory ID-token representation derives email, plan type, ChatGPT user ID, ChatGPT
account/workspace ID, and the FedRAMP flag. The claims are read from the top-level email and the
`https://api.openai.com/profile` and `https://api.openai.com/auth` namespaced claims.
[derived fields](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/token_data.rs#L27-L41),
[claim names](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/token_data.rs#L71-L99),
[claim mapping](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/token_data.rs#L174-L198).

Codex only Base64-decodes the JWT payload in this path; it does not verify the signature there.
That is reasonable for display/scheduling metadata obtained immediately from a successful issuer
exchange, but it is not independent identity proof. ViberMate must not treat claims from an
arbitrarily uploaded `auth.json` as authorization facts until the credential has been validated
against the provider. JWT `exp` may still be used as a scheduling hint.
[payload decoder and `exp`](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/token_data.rs#L129-L147).

Codex supports four storage modes: file (the default), keyring, automatic keyring-with-file
fallback, and process-memory-only. File storage writes `$CODEX_HOME/auth.json` with Unix mode
`0600`; direct keyring storage serializes the whole auth object, and automatic mode falls back to
the file on keyring failure.
[storage modes](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/config/src/types.rs#L111-L124),
[file path and mode](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/storage.rs#L154-L220),
[keyring write](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/storage.rs#L251-L322),
[automatic fallback](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/storage.rs#L408-L456),
[ephemeral backend selection](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/storage.rs#L459-L528).

ViberMate should not watch or mutate Codex's `auth.json`. Two processes refreshing a rotating
refresh token can invalidate one another, and a keyring-backed Codex installation may not have a
file at all. ViberMate should obtain its own OAuth grant and own the resulting lifecycle.

### Browser PKCE login

Current Codex browser login uses issuer `https://auth.openai.com`, loopback port `1455` with
registered fallback `1457`, a `127.0.0.1` listener, and redirect
`http://localhost:<port>/auth/callback`.
[defaults](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/server.rs#L75-L125),
[login server and redirect](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/server.rs#L176-L205),
[loopback bind](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/server.rs#L635-L692).

It generates a random 64-byte PKCE verifier and S256 challenge plus a random 32-byte state, then
uses authorization-code response type and validates state before exchanging the code.
[PKCE generation](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/oauth/pkce.rs#L8-L28),
[authorization URL fields](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/oauth/authorization.rs#L21-L47),
[state validation](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/server.rs#L360-L425).

The current authorization request uses these scopes:

```text
openid profile email offline_access api.connectors.read api.connectors.invoke
```

It also sends `id_token_add_organizations=true`, `codex_cli_simplified_flow=true`, an
`originator`, and optionally `allowed_workspace_id`.
[authorization construction](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/server.rs#L584-L615).

The code exchange is a form-encoded POST to `https://auth.openai.com/oauth/token` with
`grant_type=authorization_code`, `client_id`, `code`, `redirect_uri`, and `code_verifier`. Codex
requires `id_token`, `access_token`, and `refresh_token` in the response, extracts the account ID
from the ID token, and persists `last_refresh`.
[grant fields](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/oauth/client.rs#L58-L74),
[exchange endpoint/encoding](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/server.rs#L695-L827),
[initial persistence](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/server.rs#L830-L873).

The App can offer this as the shortest native flow. It should never expose the verifier, code,
state, or callback URL in ordinary logs. If both registered ports are occupied, fall back to device
login; do not send cancellation traffic to an unknown process merely because it owns a port.

### Device-code login

Current Codex device login:

1. POSTs JSON containing `client_id` to
   `https://auth.openai.com/api/accounts/deviceauth/usercode`;
2. shows `https://auth.openai.com/codex/device` and the returned one-time code;
3. polls `https://auth.openai.com/api/accounts/deviceauth/token` for up to 15 minutes;
4. receives an authorization code plus PKCE verifier/challenge;
5. exchanges them through the same token endpoint with redirect
   `https://auth.openai.com/deviceauth/callback`;
6. persists the same ID/access/refresh-token bundle.

[user-code request](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/device_code_auth.rs#L62-L97),
[polling behavior](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/device_code_auth.rs#L99-L147),
[verification URL](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/device_code_auth.rs#L165-L179),
[exchange and persistence](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/device_code_auth.rs#L181-L237).

This is the correct primary flow for server deployments: the user's browser does not need to
reach a callback listener on the server, and an arbitrary customer domain is not assumed to be a
registered OAuth redirect URI. The UI should retain Codex's anti-phishing warning: continue only
when the user initiated the displayed device login. Login transactions must be independent and
TTL-bound so concurrent users cannot overwrite one global pending code.

### Refresh endpoint, rotation, expiry, and 401 handling

Current Codex uses:

| Item | Current source behavior |
| --- | --- |
| Token endpoint | `https://auth.openai.com/oauth/token` |
| Public client ID | `app_EMoamEEZ73f0CkXaXp7hrann` |
| Grant | `grant_type=refresh_token`, `client_id`, `refresh_token` |
| Encoding | JSON for refresh (authorization-code exchange is form-encoded) |
| Client secret | none in the reviewed implementation |
| Proactive window | access-token JWT expires in at most 5 minutes |
| Fallback when `exp` is unavailable | `last_refresh` older than 8 days |

[endpoint/window constants](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs#L203-L216),
[refresh request and encoding](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs#L1609-L1657),
[grant fields](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/oauth/client.rs#L76-L110),
[client ID](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs#L1690-L1710),
[proactive decision](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs#L2955-L2977).

The refresh response fields are optional. Codex replaces only fields actually returned, so a
rotated refresh token replaces the old one while an omitted refresh token preserves it. The whole
result is saved before the in-memory cache is reloaded.
[partial update semantics](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs#L1583-L1607),
[optional response and persist/reload](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs#L1690-L1695),
[refresh commit](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs#L3041-L3059).

Codex classifies `refresh_token_expired`, `refresh_token_reused`, and
`refresh_token_invalidated` as permanent; a 401 or bad-request `invalid_grant` is also permanent.
Transport and malformed-response errors are transient. On a proactive failure Codex can continue
returning the cached auth, and its 401 recovery reloads a matching account first, then performs one
refresh attempt before returning the 401.
[failure classification](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs#L1634-L1687),
[cached auth on proactive failure](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs#L2355-L2373),
[401 state-machine contract](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs#L1830-L1848),
[serialized guarded refresh](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs#L2795-L2890).

The rotation semantics make concurrency correctness a product requirement, not an optimization.
ViberMate must single-flight refresh per account and commit with an expected credential revision.
If another request already replaced the failed access token, a 401 handler should reuse that newer
snapshot instead of consuming the refresh token again.

### Account ID and outbound request binding

For token-backed ChatGPT auth, Codex resolves the account ID from the same token state that
supplies the bearer token. Its bearer provider adds:

```text
Authorization: Bearer <access_token>
ChatGPT-Account-ID: <account_id>
X-OpenAI-Fedramp: true              # only for a FedRAMP account
```

[account-ID lookup](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs#L598-L620),
[one auth snapshot](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/model-provider/src/auth.rs#L306-L324),
[header injection](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/model-provider/src/bearer_auth_provider.rs#L31-L46).

ChatGPT-backed modes default to `https://chatgpt.com/backend-api/codex`; API-key mode defaults to
`https://api.openai.com/v1`.
[ChatGPT base](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/model-provider-info/src/lib.rs#L74-L78),
[mode-specific destination](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/model-provider-info/src/lib.rs#L412-L431).

Therefore a ViberMate “Codex OAuth account” is not merely a bearer token. Switching accounts must
atomically substitute the bearer token and ChatGPT account ID, and must carry the FedRAMP routing
property when present. Client-supplied or custom-overwrite values for these identity headers must
be stripped and replaced after ordinary custom headers are applied.

### Logout and revocation

Codex performs best-effort revocation at `https://auth.openai.com/oauth/revoke`: it prefers the
refresh token, falls back to the access token, sends a JSON `token` plus `token_type_hint`, includes
the client ID for a refresh token, uses a ten-second timeout, and still permits local deletion if
revocation fails.
[revocation policy and request](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/revoke.rs#L1-L65),
[token selection](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/revoke.rs#L68-L95),
[HTTP request](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/revoke.rs#L97-L145).

ViberMate's Delete action should use the same best-effort lifecycle: attempt revocation, then erase
the local secret even when the network is unavailable. The UI should report “removed locally;
revocation could not be confirmed” rather than retain a secret indefinitely.

### External-token mode is not a stable shortcut

Codex has an external `chatgptAuthTokens` mode that accepts only an access token, account ID, and
optional plan. The app-server protocol explicitly labels it unstable and for OpenAI internal use;
the external host owns refresh. Codex stores that mode only in process memory and does not run its
normal managed refresh path for it.
[unstable login contract](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/app-server-protocol/src/protocol/v2/account.rs#L60-L103),
[refresh callback shape](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/app-server-protocol/src/protocol/v2/account.rs#L270-L308),
[external host responsibility](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/app-server-protocol/src/protocol/v2/account.rs#L541-L552),
[ephemeral construction](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs#L1712-L1771),
[no managed refresh](https://github.com/openai/codex/blob/ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs#L2848-L2883).

This accurately describes ViberMate's current manual token substitution problem, but it is not a
stable integration dependency. ViberMate can adopt the lifecycle semantics while keeping its
network-routing boundary independent of this API.

## What CLIProxyAPI demonstrates

CLIProxyAPI is valuable evidence for multi-account operation, but its OAuth wire behavior must not
override the current first-party Codex source.

### Patterns worth adopting

- It represents each credential as a stable auth record with provider, label, lifecycle status,
  disabled/unavailable state, timestamps, error state, and mutable credential metadata.
  [auth record](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/sdk/cliproxy/auth/types.go#L47-L106)
- Its file store enumerates multiple JSON records and turns each into an independently selectable
  auth object. ViberMate should take the one-record-per-account semantic, but use its database and
  secret store rather than copy the file layout.
  [multi-file load](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/sdk/auth/filestore.go#L171-L200),
  [record construction](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/sdk/auth/filestore.go#L230-L355)
- It has per-account refresh and persistence locks, lets a concurrent 401 reuse a token another
  request already refreshed, preserves a still-valid access token after transient refresh failure,
  and uses generation/registration epochs to reject stale writes.
  [lock ownership](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/sdk/cliproxy/auth/conductor.go#L192-L201),
  [401/single-flight refresh](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/sdk/cliproxy/auth/conductor_refresh.go#L471-L540),
  [transient-failure behavior](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/sdk/cliproxy/auth/conductor_refresh.go#L542-L623),
  [generation-aware merge](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/sdk/cliproxy/auth/conductor_lifecycle.go#L143-L280)
- It keeps account selection separate from token lifecycle and implements round-robin, weighted
  round-robin, and fill-first policies. That separation is useful even though automatic pool
  scheduling is out of phase-1 scope.
  [selector types](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/sdk/cliproxy/auth/selector.go#L27-L72),
  [stable-ID round robin](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/sdk/cliproxy/auth/selector.go#L613-L652)

### Details not to copy

CLIProxyAPI currently refreshes with form encoding and an explicit
`scope=openid profile email`, while current Codex refreshes with JSON and no scope. Its login scope
also omits Codex's current connector scopes. Follow the pinned first-party adapter contract, not
this older wire representation.
[CLIProxy endpoints/scopes](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/internal/auth/codex/openai_auth.go#L23-L86),
[CLIProxy refresh wire](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/internal/auth/codex/openai_auth.go#L187-L280).

Do not copy these behaviors:

- returning the raw failed token-endpoint response body in errors, because it can place provider
  details or secrets in logs;
  [raw error body](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/internal/auth/codex/openai_auth.go#L239-L246)
- treating only `refresh_token_reused` as non-retryable; current Codex recognizes expired,
  invalidated, unauthorized, and `invalid_grant` permanent failures as well;
  [CLIProxy retry classifier](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/internal/auth/codex/openai_auth.go#L299-L337)
- unconditionally overwriting the stored refresh token with an empty response value; preserve the
  prior refresh token when the field is omitted;
  [unconditional storage update](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/internal/auth/codex/openai_auth.go#L339-L348)
- plaintext JSON as ViberMate's primary secret store. CLIProxyAPI creates the directory as `0700`
  but uses `os.Create` for the file, rather than explicitly enforcing Codex's `0600` behavior;
  [file write](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/internal/auth/codex/token.go#L47-L83)
- filenames containing email and plan information. ViberMate should use opaque generated IDs and
  keep PII in access-controlled metadata;
  [filename scheme](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/internal/auth/codex/filename.go#L9-L31)
- using decoded-but-unverified JWT claims as authorization. CLIProxyAPI also documents that its
  parser does no signature verification.
  [JWT parser](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/internal/auth/codex/jwt_parser.go#L54-L75)

CLIProxyAPI inserts bearer and account-ID headers from one auth record, but then applies custom
headers. ViberMate should apply managed identity headers last so a user overwrite cannot split the
token from its account ID.
[CLIProxy request headers](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/internal/runtime/executor/codex_executor_request.go#L321-L369).

## Proposed ViberMate phase-1 model

### Public metadata and separate secret material

Use one durable `CodexOAuthAccount` per exact human/workspace credential. Do not use email as the
identity because one user can belong to multiple ChatGPT workspaces and email can change.

Suggested non-secret fields:

| Field | Purpose |
| --- | --- |
| `id` | opaque ViberMate-generated ID; referenced by traffic-policy revisions |
| `scope` / `owner_id` | personal or team ownership and authorization boundary |
| `display_name` | user-editable label, independent of provider email |
| `issuer` | pinned provider identity, initially `https://auth.openai.com` |
| `chatgpt_user_id` | derived display/deduplication hint, not a ViberMate authorization principal |
| `chatgpt_account_id` | workspace routing ID bound to the secret bundle |
| `email`, `plan_type` | optional display metadata |
| `is_fedramp` | outbound routing property bound to the account |
| `status` | `active`, `degraded`, `reauth_required`, `disabled`, or `revoking` |
| `access_expires_at` | refresh scheduling hint |
| `last_refreshed_at`, `next_refresh_at` | operator-visible lifecycle state |
| `credential_revision` | monotonic compare-and-swap version |
| `last_error_code` | reviewed stable code only; never raw provider body |
| audit timestamps/actor | who added, disabled, reauthorized, or removed it |

Keep `access_token`, `refresh_token`, and raw `id_token` in a separate encrypted/OS-backed secret
record referenced by `secret_ref`. APIs and UI serializers must make those fields structurally
unrepresentable, not merely CSS-hidden. A managed OAuth `Authorization` value must never be
available through the ordinary evidence “Reveal” control.

Suggested uniqueness is `(owner scope, issuer, chatgpt_user_id, chatgpt_account_id)` when all
claims are available. Do not silently overwrite an existing account on collision; offer
“reauthorize existing account” or “keep as separate connection.”

### Lifecycle

```text
connecting -> active
active -> refreshing -> active                         successful rotation
active -> degraded -> refreshing                       transient failure, access token still valid
active/degraded -> reauth_required                     expired/reused/revoked/invalid grant
active/degraded/reauth_required -> disabled            explicit operator action
* -> revoking -> removed                               best-effort revoke, then local erasure
```

`refreshing` can be an internal operation rather than a persisted UI state. Never leave an account
unusable just because a proactive refresh transiently failed while its access token is still
valid. Conversely, do not keep retrying a known-expired, reused, revoked, or invalid refresh token;
show a Reauthorize action.

### Login UX by deployment

| Deployment | Default flow | User experience |
| --- | --- | --- |
| Native App, local user | Browser PKCE loopback | “Connect Codex account” opens the browser; App completes automatically. Fall back to device code if the registered port is unavailable. |
| Native Web on the same machine | Device code | UI shows OpenAI verification URL and one-time code, polls status, and can cancel. No assumption that the browser can reach a special loopback callback owned by Runtime. |
| Docker/local Web | Device code | Same flow; no host networking or callback-port instructions. |
| Remote personal Web | Device code | Same flow; require authenticated HTTPS for the management UI. |
| Team Web | Device code | Only an authorized owner/admin can create a shared account; personal accounts remain visible/selectable only to their owner. |

The success screen should show label, masked email, plan, workspace/account suffix, and status, not
tokens. Provide Rename, Test connection, Reauthorize, Disable/Enable, and Remove. Shared-account
creation must explicitly state that the Runtime server will hold a renewable credential and which
team members/policies may use it.

Remote non-loopback HTTP must not offer secret import or account administration. Device login does
avoid transporting the refresh token through the browser, but the management session, account PII,
and authorization actions still require the server HTTPS work already planned elsewhere.

### Login transaction boundary

Each pending login needs an opaque transaction ID, requesting user/team, mode, created/expiry
times, status, and encrypted ephemeral PKCE/state/device material. It must expire and be erased on
success, cancellation, or timeout. Never store it as a process-global singleton. A completion may
create or reauthorize only the owner/scope captured when the transaction began.

For native PKCE, accept callbacks only on loopback, validate state before reading the code, and
consume a code once. For device flow, respect the provider polling interval and 15-minute limit;
do not let a browser choose arbitrary issuer/token URLs.

### Refresh algorithm

For every request or scheduled refresh:

1. load account metadata plus one immutable secret snapshot and its `credential_revision`;
2. if another worker has already replaced the access token that failed, use the newer snapshot;
3. acquire a per-account single-flight lock; in a multi-process deployment also acquire a short
   database lease or use a conditional update that prevents two refresh grants;
4. POST the current refresh grant through the pinned Codex OAuth adapter;
5. construct a complete candidate bundle: preserve any omitted token field, parse new display
   metadata, and reject an unexpected workspace/account identity change;
6. atomically commit only when the stored revision still equals the loaded revision;
7. erase the old secret value after the new bundle is durable;
8. on transient failure, schedule bounded jittered retry without discarding an unexpired access
   token; on permanent failure, set `reauth_required`;
9. on an upstream 401, perform at most one refresh/retry for that request.

In-memory locking alone is insufficient if team Web can run multiple server processes. Revision
comparison is also needed to prevent an older refresh response from overwriting a newer
reauthorization or Disable/Remove action.

### Request-routing boundary

At exchange start, freeze the selected traffic-policy revision and OAuth account ID. Immediately
before the outbound provider request:

1. remove inbound `Authorization`, `ChatGPT-Account-ID`, and `X-OpenAI-Fedramp` when the route uses
   a managed account;
2. apply ordinary non-identity header transformations;
3. resolve one current versioned OAuth credential snapshot;
4. inject its bearer token, exact account ID, and FedRAMP flag;
5. record only the ViberMate account ID and credential revision in frozen evidence.

This keeps account selection auditable without persisting secrets. A refresh during a stream must
not splice a new token/account ID into the same attempt; the next attempt receives the new
snapshot.

### `auth.json` import

Import is useful as explicit bootstrap input and for diagnostics, but should not be the eventual
main onboarding flow:

- native App may read a user-selected local file;
- Web may accept an explicit upload only over authenticated HTTPS; a browser-provided server file
  path is not meaningful or safe;
- validate `auth_mode=chatgpt`, required non-empty token fields, and syntactic claim consistency;
- copy into ViberMate's secret store and immediately discard upload bytes;
- make clear that this is a copy, not synchronization;
- never write refreshed values back into Codex's file;
- warn that continuing to refresh the same imported rotating token in both programs can force
  reauthentication; prefer a fresh ViberMate device login.

### Security and observability rules

- Never log, trace, persist in captures, return through APIs, or include in copied diagnostics:
  access tokens, refresh tokens, ID tokens, authorization codes, PKCE verifiers, OAuth state, or
  device authorization IDs.
- Redaction is irreversible at the evidence boundary. An administrator's “Reveal” affordance may
  reveal ordinary retained headers, but never a managed OAuth credential.
- Do not put email, plan, account ID, or token hashes in secret filenames. Use opaque IDs.
- Encrypt server-side secrets at rest with a deployment-owned key; native App should prefer the OS
  secret store. File permissions are defense in depth, not encryption.
- Do not expose raw token-endpoint bodies. Map reviewed provider codes to stable internal errors and
  retain only status, category, correlation ID if safe, and timestamps.
- Decoded JWT claims are display hints. ViberMate authorization comes from its own user/team ACL,
  never from an email or role string inside the token.
- Account-management APIs require owner/admin authorization, CSRF protection for browser sessions,
  and an audit event for connect, reauthorize, disable, enable, policy assignment, and removal.
- Rate-limit login starts and polling. A user code is short-lived but still must not enter ordinary
  logs or diagnostics.
- Test outbound OAuth calls through the intended server egress policy; do not inherit an arbitrary
  captured client's proxy or headers.

## Accepted first implementation slice

ADR 0011 deliberately narrows the first slice while the supported OAuth client-registration
model remains unresolved. It establishes managed credential ownership and refresh correctness
without presenting source-level compatibility as a supported OpenAI login integration.

### Include

- one Codex OAuth provider adapter pinned to the reviewed upstream contract;
- explicit one-time `auth.json` bootstrap with refresh-ownership warnings;
- metadata list/detail without secrets;
- the existing deployment-selected ViberMate SecretStore;
- fixed account selection from a traffic policy;
- proactive refresh and single-flight/CAS rotation before an attempt freezes its credential;
- permanent-failure `reconnect_required` state and explicit credential replacement;
- audit and redaction guarantees.

### Defer

- native PKCE and server/device-code login;
- a bounded 401 refresh/retry;
- provider revocation and provider-confirmed connection tests;
- automatic round-robin, weighted distribution, quota-aware failover, or “burn one account first”;
- generic OAuth-provider abstraction exposed in the UI;
- background synchronization with Codex keyring or `auth.json`;
- account sharing by exporting refresh tokens;
- accepting arbitrary OAuth issuer, client ID, token endpoint, redirect URI, or scopes from a
  normal user;
- reliance on Codex's unstable external-token app-server API;
- using provider email/JWT claims as ViberMate authentication or team membership;
- exposing any managed OAuth secret through Raw HTTP reveal.

## Target managed-login release gates and test matrix

Before the later browser/device managed-login product is called complete, these cases should pass
with a fake issuer plus controlled integration validation. ADR 0011's import-and-refresh slice
uses the applicable refresh, identity, persistence, and redaction subset today:

1. browser PKCE success, state mismatch, cancellation, occupied ports, and device fallback;
2. device authorization pending, denial, expiry, cancellation, concurrent users, and polling
   interval enforcement;
3. initial metadata extraction for personal and workspace accounts, including optional/missing
   claims and FedRAMP;
4. refresh response with all tokens, rotated refresh token, omitted refresh token, omitted ID token,
   malformed response, timeout, and server error;
5. permanent errors: expired, reused, invalidated, unauthorized, and `invalid_grant`;
6. two simultaneous proactive refreshes and many simultaneous 401s consume one refresh grant;
7. stale refresh cannot overwrite reauthorization, disable, or removal;
8. transient refresh failure keeps a valid access token usable, but an expired account becomes
   unavailable with a clear Reauthorize action;
9. managed request strips client identity headers, then atomically injects the selected token,
   account ID, and FedRAMP header;
10. original-destination and API-key routes remain unchanged;
11. no token/code/verifier appears in application logs, audit events, database metadata, Raw HTTP
    evidence, diagnostics, browser storage, crash output, or account APIs;
12. restart and, for supported team topology, competing server workers preserve rotation safely;
13. best-effort revoke success, revoke timeout/failure followed by local erase, and deletion during
    an in-flight refresh;
14. `auth.json` import rejects wrong mode/incomplete data and never mutates the source file;
15. a pinned upstream-contract test detects changes in endpoint, encoding, scopes, client ID, or
    response semantics before a release.

## Unresolved release decision

The reviewed source proves how official Codex currently authenticates; it does not prove that a
third-party application may ship using the same registered OAuth client identity. Before enabling
the feature for users, confirm the permitted integration model. If that cannot be confirmed, keep
the implementation experimental and prefer an explicitly supported provider integration rather
than presenting source-level compatibility as a stable OpenAI API contract.
