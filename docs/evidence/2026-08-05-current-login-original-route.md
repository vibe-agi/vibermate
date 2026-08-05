# Current-Login Original Route Evidence

Date: 2026-08-05
Implementation candidate: `6803c07925927d84b8ddbbe4b7906715aa69b49c`
Source state during full gates: clean
Platform: macOS 26.5.2 (25F84), arm64
Go toolchain: 1.25.12
Client: Codex CLI 0.146.0, existing ChatGPT login

## Exercised path

The opt-in development-Host test created the smallest executable Access: one
exact OpenAI Responses AgentEndpoint, the Core-owned
`original_passthrough` Profile, and no provider target, account binding,
SecretRef, or managed credential. The launcher preserved the child's existing
`HOME` and optional `CODEX_HOME`, received the explicit recognized-client Root
decision, and started the real Codex executable through the same CaptureRun
and loopback proxy path used by the product.

The observed first-party origin for a Codex process signed in with ChatGPT was
`https://chatgpt.com:443`, not `https://api.openai.com:443`. The Access admitted
only that exact origin. Unsupported model-discovery, MCP, and WebSocket
operations received local typed 422 responses. Codex then used its own bounded
WebSocket-to-HTTPS fallback, completed a Responses request, printed the proof
marker `ready`, and left a completed `provider_attempt` whose target was the
exact admitted origin.

The final run was:

```text
VIBERMATE_LIVE_AGENT=codex-current-login \
  go test -run '^TestARealResponsesClientUsesItsOwnLoginThroughOriginalRoute$' \
  -count=1 -v ./internal/desktophost
```

It passed in 21.93 seconds. The test may consume a small amount of the
operator's existing Codex allowance, so it remains explicit and is skipped by
ordinary test runs. It accepts no provider key and does not print or persist
the client's login material. The child continues to own its existing private
login store; the proxy handles the live origin-bound Auth only in the bounded
request path and does not place its raw value in evidence.

## Product correction

The Access UI now asks which login origin the Codex process already uses:

- **ChatGPT sign-in** selects `https://chatgpt.com` and is the default for a
  new Codex Access;
- **OpenAI API key** selects `https://api.openai.com`;
- these choices identify the exact client origin to decrypt; they do not pick
  a managed provider route or smuggle credentials into routing.

Both exact origins can be configured as separate Accesses. The UI chooses the
first unconfigured origin when adding another Codex Access and never guesses
between two already configured origins.

The same change set replaced the web-dashboard visual scale with a compact
desktop-workbench scale: 44-pixel top chrome, a 196-pixel full sidebar,
30-pixel controls, neutral panel colors, reduced radii, and rail-based selected
states. Visual checks covered 1440 by 900 and 390 by 844 viewports. The pinned
frontend run passed 386 React tests and all 24 Playwright scenarios.

## Gates

- `make check` on the clean implementation candidate;
- pinned Node 22.23.1 frontend check and build: 386 tests;
- pinned Node 22.23.1 Playwright: 24 of 24 scenarios;
- the opt-in real current-login test above;
- `git diff --check` and the public-repository forbidden-term scan.

## What this does not prove

- A packaged, signed, notarized, or installed App uses the current-login
  original route successfully at this candidate.
- Successful Responses WebSocket semantics; the exercised client fell back to
  HTTPS after the proxy rejected the unsupported upgrade locally.
- That every Codex release or every OpenAI login method uses the same origin.
- The OpenAI-API-key origin, Claude current-login path, system Root trust,
  Keychain-backed secrets, Server mode, Preview, or Release readiness.
- Exact ClientHello, JA3/JA4, HTTP/2 SETTINGS, or header-order parity after
  MITM. Those remain separate upstream-presentation evidence boundaries.
