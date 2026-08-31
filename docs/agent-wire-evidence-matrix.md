# Agent Wire Evidence Matrix

This matrix records what ViberMate has actually observed from the installed
clients. It is an acceptance contract, not a claim that every client version
or provider always emits every field.

Observed on 2026-08-13 and re-run on 2026-08-14:

- Claude Code `2.1.228` and `2.1.232`, using the existing first-party OAuth
  login;
- Codex CLI `0.147.0`, using its existing ChatGPT login;
- a real ViberMate Host, exact-origin MITM, local MCP stdio server, and the
  clients' built-in Agent tools;
- no API key injected by the test and no reusable credential retained in the
  fixture or log.

## Compiled managed-route compatibility

The Agent-facing protocol (A) and upstream protocol (B) are separate. The
production catalog contains exactly these managed-route codec pairs:

| Client | A | B | Status |
| --- | --- | --- | --- |
| Claude Code | Anthropic Messages | Anthropic Messages | Supported |
| Claude Code | Anthropic Messages | OpenAI Chat | Supported |
| Claude Code | Anthropic Messages | OpenAI Responses | Unsupported |
| Codex CLI | OpenAI Responses | OpenAI Responses | Supported |
| Codex CLI | OpenAI Responses | OpenAI Chat | Supported |
| Codex CLI | OpenAI Responses | Anthropic Messages | Unsupported |

OpenAI Chat is an upstream edge, not an Agent-facing client entry. An Endpoint
declaring a protocol says which wire contract it implements; ViberMate does not
infer that fact from its name, origin, Account, or model ID.

`TestProductionProtocolCompatibilityMatrixIsExact` freezes the six outcomes
above. The supported paths are also covered below the catalog seam:

- Anthropic Messages passthrough: later-turn cited text, thinking history,
  tool caller/result history, streaming text, streaming thinking, and the tool
  approval barrier in `internal/anthropicchat/messages_path_test.go`;
- Anthropic Messages to OpenAI Chat: complete and streaming text/tool
  translation plus explicit cross-dialect loss/refusal in
  `internal/anthropicchat/codec_test.go`;
- OpenAI Responses passthrough: portable conversation/reasoning history and
  incremental Responses events in `internal/openairesponses` and
  `internal/responseschat` tests;
- OpenAI Responses to OpenAI Chat: complete and streaming custom-tool history
  in `internal/responseschat` tests;
- native Claude/Codex session grouping and resume across Capture boundaries in
  `internal/agentconversation` and `internal/capturecontrol` tests.

These are ViberMate codec guarantees. They do not claim that an arbitrary
Endpoint implements any protocol correctly; the selected Endpoint and Account
remain the real upstream authority.

## Claude Code / Anthropic Messages

| Wire evidence | Retained normalized evidence | Thread signal | UI label and rule |
| --- | --- | --- | --- |
| Ordinary `user` / `assistant` message content | message role plus ordered text/tool blocks | main thread when no stronger subagent assertion is present | User / Assistant |
| Assistant text immediately before a tool call, such as “Let me read the full diff” | ordinary assistant text block | same thread as its Exchange | Assistant text. Never call this hidden thinking. |
| `thinking` content with a provider signature | provider extension fingerprint, byte count, availability/status; plaintext only when actually emitted and retained | same thread | Thinking summary when text exists; otherwise “Thinking · content unavailable” |
| `tool_use` named `Agent` in the parent request history | tool call with ID, name, and arguments | proves a delegation event, not a child identity by itself | Agent task |
| `X-Claude-Code-Session-Id` | exact native session ID | groups main and subagent traffic only within that session | Session ID; copyable for resume/diagnosis |
| `X-Claude-Code-Agent-Id` | exact native actor ID | groups repeated Exchanges from the same Claude actor | observed actor name when available, otherwise `subagent` |
| `X-Claude-Code-Parent-Agent-Id` | exact native parent actor ID | creates a parent edge only when the parent actor exists in the same client session | tree indentation under the exact parent; missing parent remains a root |
| system material containing `cc_is_subagent=true` | client-asserted subagent flag on that Exchange | separates that Exchange into a subagent thread | Subagent 1, Subagent 2, …, ordered by first observation when no name exists |
| two `Agent` calls in one parent message | two retained Agent tool calls in the same message | proves parallel dispatch intent | two independent subagent threads; no inferred parent edge required |
| MCP `tools/list`, tool call, and tool result | namespaced tool call/result blocks | same Agent thread as the containing Exchange | MCP · `<server>/<tool>` |
| provider status `429` or `529` | original provider status plus safe ViberMate reason | same thread | Provider rejected · HTTP `<status>` |

Real topology results:

- one MCP + subagent run: 6 Exchanges, 1 Agent call, 6 opaque thinking
  signatures, two selectable Conversations, and exact native session/actor
  identity where emitted;
- two parallel subagents: 4 Exchanges, 2 `cc_is_subagent` Exchanges, and 2
  Agent calls in one parent message;
- Claude Code subagents cannot create other subagents. This is a documented
  client limit, so ViberMate must not advertise nested Claude subagents.

## Codex CLI / OpenAI Responses

| Wire evidence | Retained normalized evidence | Thread signal | UI label and rule |
| --- | --- | --- | --- |
| input/output message item | message role plus ordered content blocks | actor path when present, otherwise main thread | User / Assistant |
| visible `agent_message` text | assistant text block with Agent context | `author` and/or `recipient` actor path | Assistant text under the named actor thread |
| reasoning item with encrypted/opaque content | provider extension fingerprint, byte count, and availability/status | same actor thread | Reasoning · encrypted/opaque |
| reasoning summary text, when emitted | reasoning-summary block | same actor thread | Reasoning summary. The live matrix observed none, so absence is normal. |
| collaboration item / Agent context | Agent context containing `author` and `recipient` | each actor path is a stable observed thread identity | actor leaf as title; full actor path as secondary technical identity |
| MCP namespaced tool call/result | namespaced tool call/result blocks with call identity | same actor thread | MCP · `<server>/<tool>` |
| function/program tool call and result | distinct call/result blocks; call IDs retained | same actor thread | Tool call / Tool result |

Real topology results:

- one MCP + subagent run: explicit `/root` and one generated child actor, plus
  directed author/recipient evidence;
- two concurrent first-level Agents plus one nested Agent: 12 Exchanges and
  four actors: `/root`, `/root/branch`, `/root/sibling`, and
  `/root/branch/leaf`;
- six directed author/recipient edges were retained, including the nested
  branch/leaf exchange;
- 15 opaque reasoning items were retained, but no reasoning-summary text was
  emitted in this run.

## Projection rules established by the matrix

1. Split content by explicit native session and actor identity when it exists.
2. Retain an explicit native parent actor ID as a directed edge. Render the
   edge only when both endpoints belong to the same client session; never infer
   an edge from timestamps, titles, prompts, or workspace.
3. Otherwise, a Claude `cc_is_subagent=true` Exchange gets its own stable
   capture-local thread ordered by first observation. Do not infer its name or
   parent.
4. Main content and each distinguishable subagent are selectable separately.
5. The directory renders a stable, multi-level tree for exact parent edges.
   Siblings remain ordered by first observation and do not move when new Turns
   arrive. An unresolved parent leaves the Conversation at the root.
6. The default timeline never interleaves multiple Agent chats. A merged audit
   view is secondary and must label every row with its thread.
7. Visible assistant narration, reasoning summary, hidden/opaque reasoning,
   signature, tool call, and tool result remain different kinds.

## Automated gates

`internal/desktophost/live_agent_deep_test.go` provides opt-in real-client
scenarios:

- `claude-baseline`
- `claude-mcp-subagent`
- `claude-parallel-subagents`
- `codex-baseline`
- `codex-mcp-subagent`
- `codex-parallel-nested`

Ordinary test runs compile and skip these scenarios. A scenario runs only when
`VIBERMATE_LIVE_AGENT_DEEP` explicitly selects it, because it uses the local
client's existing allowance.

The 2026-08-14 regression run additionally proved all four Raw HTTP layers for
6 Claude MCP/subagent Exchanges, 4 Claude parallel-subagent Exchanges, 7 Codex
MCP/subagent Exchanges, and 11 Codex parallel/nested Exchanges. The nested
Codex run retained `/root`, `/root/branch`, `/root/branch/leaf`, and
`/root/sibling` as four Conversations with six directed actor edges.
