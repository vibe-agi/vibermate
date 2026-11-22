# Claude Code 2.1.329 on OpenAI Responses: golden round trips

The tests in `../../golden_test.go` and `../../golden_stream_test.go` run the
production `anthropic-messages-to-openai-responses` path over real Claude Code
2.1.329 traffic and compare every observable result with the files here
(design section 15.3).

- **Inputs.** The ten request bodies are the S2 corpus in
  `../../../productruntime/testdata/protocolgolden/requests/`, made committable
  by `tool/wire-skeleton` from the installed-client wire dumps of 2026-11-20.
  They are read in place, so the corpus has one copy. The two
  `*.responses.sse` files are scripted upstream turns:
  - `public.responses.sse` is the public API's full event sequence: a
    reasoning item with two summary parts, a commentary message and five
    parallel function calls, each with its `added`, delta and `done` events.
  - `chatgpt-minimal.responses.sse` is
    `W/codex-models/TestWireDumpCodexRealModels_gpt-5.5-scripted-reply-0.sse`
    (only `output_item.done` events and a terminal with an empty `output`),
    extended with summary deltas over two parts and the same five calls.

  The calls are the five Claude Code made in `claude-tools.008`, under
  upstream call ids.
- **A, request.** `<fixture>.responses.json` is the exact Responses body and
  `<fixture>.notices.txt` the decode and encode notices. Each body also passes
  re-admission and the openai-go oracle.
- **B, response.** `<stream>.anthropic.sse` is the exact Anthropic stream
  Claude Code receives after approval (the minted `msg_vm_` id of the minimal
  form is normalised to zeros), and `<stream>.stream-notices.txt` the
  stream's notices. anthropic-sdk-go accumulates it into the expected message.
- **C, round trip.** B's message, replayed as Claude Code replays it with
  `claude-tools.008`'s tool results, gives
  `<stream>.round-trip.responses.json` and `.round-trip.notices.txt`.

Requests are mapped as the preset maps them (Opus to `gpt-6.2-astra`, Sonnet
to `gpt-6.2-sol`, Haiku to `gpt-6-luna`), carry the session evidence the
Exchange merges from `X-Claude-Code-Session-Id`, and are encoded for the
official API with one fixed reasoning binding.

Regenerate with

```
go test ./internal/anthropicresponses -run Golden -update
```

and review the diff. A golden changes only for a reviewed reason.
