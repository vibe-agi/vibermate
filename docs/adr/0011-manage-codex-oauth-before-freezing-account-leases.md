# Manage Codex OAuth before freezing Account leases

Status: accepted

A Codex OAuth Account is one versioned credential snapshot containing the access token, refresh token, ID token, ChatGPT account ID, and FedRAMP routing flag. ViberMate refreshes and commits that snapshot before `ProviderAccount` freezes an attempt-scoped lease. The provider authenticator then reads exactly the frozen secret revision and writes `Authorization`, `ChatGPT-Account-Id`, and `X-OpenAI-FedRAMP` atomically after ordinary Header policy; users and scripts cannot set or delete those fields for this driver.

Phase 1 accepts an explicit one-time Codex `auth.json` import. Import transfers refresh ownership to ViberMate: the source file is neither watched nor updated, and the same rotating refresh-token set must not remain active in another Codex installation. Account APIs return only derived display metadata and lifecycle state. Decoded JWT claims are unverified hints and never grant ViberMate authority.

Refresh uses the pinned Codex adapter contract, the Runtime's strict egress path, Offline Hold, body-free audit, per-credential single-flight, and SecretStore compare-and-swap. A response that omits an ID or refresh token preserves the prior value; an account-identity change fails closed. Permanent refresh failures become `reconnect_required`. A transient proactive failure may retain an access token only while its known expiry remains in the future. Token-endpoint bodies and every managed OAuth secret are excluded from account responses, Raw HTTP evidence, diagnostics, and Reveal controls.

The reviewed public Codex client ID and endpoint behavior are compatibility evidence, not proof that a third-party release may use that registered client identity. This slice remains experimental until the supported integration model is confirmed. Browser PKCE, device-code login, provider revocation, and a bounded 401 refresh/retry are separate decisions; adding them must preserve the same pre-lease revision and evidence boundary rather than rotating credentials inside a frozen attempt.
