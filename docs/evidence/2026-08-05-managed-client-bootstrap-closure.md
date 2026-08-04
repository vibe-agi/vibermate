# Managed Client Bootstrap Closure Evidence

Date: 2026-08-05

## Frozen candidate

- Source revision: `c16b2f9556fd4123adba9d5a6f72bb7fbb855905`
- Source state: clean
- Platform: macOS arm64
- Desktop profile: development
- Fixed client: Claude Code 2.1.220
- Report schema: `vibermate.m0-assembly-acceptance/v6`
- Deterministic result: 18 of 18 checks passed

The private report remains outside Git at:

`/private/tmp/vibermate-c16b2f9-acceptance.OCLvjV/deterministic-report.json`

Its mode is `0600`, its size is 8,770 bytes, and its SHA-256 digest is:

`5411f2b8e6db4680dee7270c567c04b64a247ea830baec2de7c6342fe3fb7011`

The independently supplied acceptance executable has SHA-256:

`58551733d6fa714965a4e9bb2c740ec197dd5028b4e9ae18d65a17e4737931d6`

The report binds these packaged artifacts:

- App bundle: `29ea0befd0d368d19747d0ddf493ba4982e88010665934c67627a911aed55778`
- Desktop executable: `0f9ab762969f152d19859c8053ba329d51ede832fcf34d93342da2234adcf1ed`
- daemon: `3b9e3598c98d4df18d4cee53818ba20a0da92d07f93476bcef63b6496c66787d`
- launcher: `d5ae034b317000153985b1527e1b489e0e8c74d61c26c8f56dac69b5908425a7`
- embedded build manifest: `39396a3b78e199180cc0022685dbe384ea6bab68b0221806f59e9105b284179e`
- fixed client entrypoint: `8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081`

The independent v6 verifier re-read the clean checkout, App, manifest,
sidecars, acceptance executable, fixed client, and frozen configuration. It
accepted the exact revision and rejected no required check.

## Managed launch proof

The deterministic packaged run exercises a current managed Access with a
missing provider credential. Fixed Claude Code starts from an isolated HOME
with no client login, crosses the authenticated proxy and exact endpoint MITM,
and reaches the intended `provider_credential_unavailable` boundary. This
proves the launcher supplied the local non-secret client bootstrap credential;
otherwise the fixed client exits before sending a request.

The launch grant keeps two authorities distinct:

- protected authorities may receive the local Root and enter MITM; and
- managed-credential authorities may additionally replace client-side
  provider authentication and base selection for that CaptureRun.

The current production compiler contains only managed account-backed profiles,
so these sets are equal for the current slice. That equality is not a claim
about the deferred original-passthrough profile.

## Local provider dogfood

A second run reused the same clean packaged App with an isolated HOME, SQLite
database, development file SecretStore, and literal-loopback OpenAI Chat
fixture. No real provider credential or external provider was used.

Two explicit network decisions were made during discovery: the first allowed
one request and the second established the exact host-and-port rule used by the
successful run. `vibermate run -- claude` then completed with:

- client terminal reason `completed`;
- result `current-app-ready`;
- 3 input tokens and 1 output token as reported by the fixed client;
- two succeeded semantic Exchanges visible through the authenticated Control
  API; and
- a terminal `client_semantic` provider egress attempt under Access authority,
  with outcome `completed`.

The first fixture attempt deliberately exposed a response-shape mismatch: a
non-streaming request received `text/event-stream`. Runtime failed closed with
`provider_response_invalid` at `$.http.content_type`; it did not forward the
invalid response as success. The fixture was then corrected to select JSON or
SSE from the request's `stream` bit. Production validation was not relaxed.

Client-local provider and cost labels are not treated as model-identity or
billing evidence. VibeMate's typed Access, Exchange, and EgressAttempt records
remain the authority for what route actually ran.

## Boundaries

This evidence does not prove a real provider credential, arbitrary client
versions, streaming token presentation in an interactive TUI, exact JA3/JA4 or
HTTP/2 fingerprint parity, original-passthrough behavior, system Root trust,
Keychain storage, signed/notarized distribution, LAN Server mode, plugins, or
Preview/Release readiness.
