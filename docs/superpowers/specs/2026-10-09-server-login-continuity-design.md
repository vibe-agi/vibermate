# Server login continuity fixes

Status: the user approved proceeding after the confirmed diagnosis and proposed default-port/session-restoration behavior. This is a bounded correction of the existing CLI and Web login flows, not a replacement authentication system.

## User-visible contract

- `--server https://runtime.example.test` and the explicit `:443` form select the same server, trust state, and saved login. HTTP without a port similarly means port 80. Explicit non-default ports remain unchanged. A bare hostname without a scheme still requires a port; HTTPS never downgrades to HTTP.
- Reloading the same Web tab restores an unexpired, still-authorized Web session. The server remains authoritative for current identity and permissions. Passwords and recovery keys are never stored. Explicit logout, expiry, and revocation remove the saved session.
- This change does not extend session lifetime, remember login after closing a browser, or persist the server's in-memory sessions across server restarts.

## Implementation boundary

CLI normalization belongs in `serverconnection.ParseTarget`, before the existing strict `ParseAddress`. Preserve one canonical explicit-port origin so login/trust keys do not fork. Do not relax empty/zero/invalid ports, credentials, unsupported schemes, URL paths, query strings, fragments, or malformed IPv6.

Web uses a dedicated, origin-bound, tab-scoped `sessionStorage` record for the existing bearer session response, separate from presentation preferences. Bootstrap decodes/bounds/checks expiry and then validates the read capability using `GET /api/v1/server/web-sessions/current` before constructing the workbench. Use the server-returned identity, not stored role claims. Preserve the existing owner/member authorization boundary and no-redirect behavior.

Login/setup/recovery and password-change success replace the saved session. Logout and definitive invalidation clear it. Network failures and server 5xx during restoration must not erase a still-potentially-valid session or misreport them as bad credentials. Unavailable browser storage must not prevent ordinary in-memory sign-in; reload continuity then cannot be promised. Do not log stored credentials or raw storage contents.

## Verification

Reproduce missing default-port behavior before the CLI change, then prove canonical equivalence, explicit-port preservation, malformed URL rejection, shared login/trust identity, and command parsers.

Reproduce Web reload using the real Web connector, then verify valid restoration, owner/member scoping, no saved passwords, invalid/expired/corrupt/foreign-origin records, transient verification failure, logout, password rotation, and API-triggered invalidation. Use a real Chrome sessionStorage check and an isolated browser page-reload smoke where feasible; do not access production data, users, trust, or listeners.
