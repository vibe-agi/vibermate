# Current client local-fixture runs

Date: 2026-08-05
Implementation: `59b687ba1205e295e44e91e22756e61410492079`
Source state: clean
Platform: macOS 26.5.2 (25F84), arm64
Toolchain: Go 1.25.12

## Fixed observations

- Claude Code 2.1.221;
- Codex CLI 0.146.0;
- an in-process literal-loopback OpenAI Chat SSE fixture;
- the ordinary development file SecretStore;
- no real provider credential or external provider request.

Both installed clients were recognized through the macOS signer path rather
than matched to a fixed release catalog entry. The test-only decision loop
answered the same pending `client_root_ask` question exposed by the Approval
Center with one request-scoped allow decision. Production contains no automatic
approval bypass.

## Commands and results

```text
VIBERMATE_LIVE_AGENT=claude-local-fixture \
go test -count=1 \
  -run '^TestARealAgentClientReachesAModelThroughVibermate$' \
  -v ./internal/desktophost
```

Claude Code printed `ready`. The test passed in 1.71 seconds.

```text
VIBERMATE_LIVE_AGENT=codex-local-fixture \
go test -count=1 \
  -run '^TestARealResponsesClientReachesAModelThroughVibermate$' \
  -v ./internal/desktophost
```

Codex printed `ready` and reported four tokens used. The test passed in 11.74
seconds.

## What these runs prove

- `vibermate run` resolves and verifies each installed executable, waits long
  enough for the explicit Root question, and starts exactly the reviewed child;
- the recognized-client grant reaches the launcher without widening the
  fixed-release feature catalog;
- the child receives the scoped proxy credential and public Root, connects
  through the production loopback proxy, repeats the exact CONNECT/SNI origin,
  negotiates a protocol executable by the active Access, and completes MITM;
- the current Claude Messages and Codex Responses HTTP request shapes reach the
  typed Responses-to-Chat streaming path and return client-native terminal
  output;
- a provider attempt reaches the configured local fixture through the ordinary
  SecretRef and audit boundaries.

Unit and integration tests separately prove that current Responses
`instructions`, message item IDs, and the supported `web_search` declaration
are handled with explicit translation notices; unknown hosted-tool fields still
fail closed. They also prove that required integer usage details use explicit
conservative approximations when the Chat backend does not report the split.

## What these runs do not prove

- a packaged, signed, or notarized App assembly;
- a real remote provider, remote TLS, or production credential protection;
- arbitrary Claude or Codex releases, unsigned clients, or Linux recognition;
- named-product emulation, exact JA3/JA4, raw ClientHello passthrough, or HTTP/2
  SETTINGS and header-order parity;
- an H2-only provider path, Responses WebSocket semantics, TUI delta rendering,
  tool execution, compaction, or a multi-turn conversation;
- system Root installation, Keychain storage, Server mode, or Release readiness.

