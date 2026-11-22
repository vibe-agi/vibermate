# Protocol characterization goldens

`TestProtocolGolden` (`../../protocol_golden_test.go`) runs the four existing
protocol paths over these inputs and compares every observable result with
the files under `golden/`.

- `requests/` holds request bodies Claude Code 2.1.329 and Codex 0.171.0 sent
  in the installed-client wire dumps of 2026-11-20, made committable by
  `tool/wire-skeleton`: prose longer than 160 bytes is a placeholder of the
  same length, identifiers and paths are replaced, base64 media are tiny valid
  files, and every other value and the member order are unchanged. Request
  heads are not kept.
- `replies/` holds the scripted provider replies of the same dumps, passed
  through the same tool. The test also writes its own Chat, Anthropic and
  Responses replies inline.
- `golden/<codec pair>/<fixture>.golden.json` records the client decode
  report, the provider request head and notices, re-admission, each scripted
  stream (live bytes per event, the approval release, intents, notices) and
  each unary reply. `<fixture>.provider.json` is the exact provider body.

A file name gives its source: `claude-tools.008.json` is request 008 of the
`claude-tools` dump directory, and `replies/codex-models.reply-0.sse` is that
directory's scripted reply 0.

Three inputs are variants of a committed request, named
`<source>-<variant>.json`, so that the Chat paths, which refuse the
originals, also have their handling of tool history pinned. Each is the
source file with a few exact byte edits; every other byte is unchanged.

- `claude-tools.008-text-results.json`: the ToolSearch result's
  `tool_reference` list as one text block naming the same tools, the image
  result as one text block, and the PDF result without its `document` part.
  The assistant turn keeps its `thinking` and `redacted_thinking` blocks.
- `claude-tools.008-chat-history.json`: the same edits, and the assistant
  turn without its thinking blocks, which is what Claude Code sends back
  after a Chat upstream answered (it returns none).
- `codex-models.005-chat-history.json`: without the `reasoning` item (a Chat
  upstream returns none to Codex), the `status` members of the assistant
  message and the function call, the message's `phase`, the `tool_search`
  tool and the web search tool's `search_content_types`.

Regenerate the inputs from a dump directory `W` with

```
go run ./tool/wire-skeleton -redact <local user name> \
  W/claude-tools/claude-008-TestWireDumpClaudeToolRoundTrip.body.json \
  internal/productruntime/testdata/protocolgolden/requests/claude-tools.008.json
```

and the goldens with

```
go test ./internal/productruntime -run TestProtocolGolden -update
```

The goldens were recorded with the `thinking.display` fix (#128) in place
and before any shared protocol code changed. A golden may change only for a
reviewed reason; the review reads the diff.
