# ACP editor acceptance — 2026-09-26

This acceptance used only isolated synthetic data. It did not read an existing
editor profile, provider credential, or project, and it sent no model request.

## Fixed clients

- Visual Studio Code 1.139.0, macOS arm64.
- `formulahendry/vscode-acp` 0.2.0 at commit
  `e7371659e3ac100db842b419b1361205a193032e`, built from source and loaded into
  an isolated VS Code Extension Host.
- `@agentclientprotocol/typescript-sdk` 1.5.0 for the synthetic Agent.
- `@agentclientprotocol/codex-acp` 1.13.1.
- `@agentclientprotocol/claude-agent-acp` 0.81.2.

The VS Code run used the extension's production `AgentManager`,
`ConnectionManager`, `PermissionHandler`, and `SessionUpdateHandler`; it was not
a copied ACP client. The permission handler was deliberately configured to
`allowAll` for the synthetic no-op tool, so this proves the reverse permission
request and selected outcome, not a human approval click.

## Observed sequence

Through `vibermate acp --server http://127.0.0.1:<isolated-port> -- <agent>` the
real Extension Host completed:

1. wire-v1 initialization and advertised synthetic authentication;
2. an initial `session/new` rejection with ACP `-32000`;
3. `authenticate`, then a successful new native session;
4. a prompt with assistant chunks, a tool-call notification, reverse
   `session/request_permission`, selected allow outcome, tool completion, and
   `end_turn`;
5. a second prompt, editor cancellation, and `cancelled` result;
6. editor EOF, clean process exit, a fresh process, reauthentication, a new
   session, and a second clean EOF.

The same real editor host then initialized `codex-acp` 1.13.1 through ViberMate,
observed exact `api-key` and `chat-gpt` auth methods, and received the expected
authentication-required boundary from an empty isolated `CODEX_HOME`. No login
was submitted. Separate isolated stdio checks initialized
`claude-agent-acp` 0.81.2 with terminal-auth capability, observed
`claude-ai-login` and `console-login`, created a session, and closed cleanly.

## Runtime and UI evidence

The native Runtime Server retained independent finished ACP Captures. Owner API
reads showed:

- full recording frozen at connection start;
- the self-reported Agent name/version and one native session;
- `completed` and `cancelled` prompts, one tool notification, and only the
  opted-in user/assistant text;
- authentication-required state for both real `codex-acp` observations; and
- no HTTP Exchange, token count, provider success, or local-path inference.

Chrome 153 Playwright logged in through the real Web workbench at 390 × 760,
resized to 1280 × 800, selected the retained prompt Capture, and observed the
ACP boundary, two prompt cards, tool-call count, cancelled outcome, and absence
of a fabricated Raw HTTP section. Flutter widget tests separately cover the
editor setup generator and ACP evidence view in English, Simplified Chinese,
desktop, and 390 px layouts.

The packaged CLI and daemon also passed both content modes through the real
Flutter controller using an acceptance-only Keychain service selected at link
time. The ordinary production Keychain namespace was not modified. The
LaunchServices-exclusive native-shell check was not counted on this host because
another ViberMate application identity was already running; production App
shell/release acceptance remains a separate release gate.

## Boundary

This proves ViberMate's editor-facing stdio transport, authorization boundary,
bounded observation, App controller, Server/Web, and remote CLI integration for
the fixed versions above. It does not prove a paid provider response, arbitrary
editor/adapter versions, HTTP interception of ACP traffic, account replacement,
model mapping, or tool-policy enforcement. Those remain separate authorities.
