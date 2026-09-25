# Observe ACP without inferring HTTP authority

Status: accepted

`vibermate acp` supervises an existing ACP executable through byte-preserving stdio and the authenticated Capture Run lifecycle, preserving editor-owned invocation, authentication and permissions; native Sessions and Prompts remain distinct from HTTP Exchanges, workspace authority and billing usage. The initial mode is bounded ACP observation with metadata-only defaults and frozen content/retention opt-in, no HTTP policy or credential injection, and direct unobserved passthrough for real-TTY stdin. Storage is a digest-checked additive extension of the released baseline, publication requires live run and user authority, and management reads retain existing owner authorization; see the [implementation boundaries and acceptance evidence](../plans/2026-09-07-acp-integration.md).
