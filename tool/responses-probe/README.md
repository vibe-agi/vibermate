# responses-probe: the S14 live probe gate

`responses-probe` answers probes Q1-Q11 of the Claude Code on OpenAI Responses
design (section 18.1) with one real OpenAI API key, and writes the data step
S14 records: a verdict per probe with the profile switch to flip when it
fails, the forms of `context_length_exceeded` and `invalid_encrypted_content`,
the real `input_tokens` of every corpus body for the `tokenestimate`
calibration fixture, and which seed model ids the key can use.

Every request body is built by ViberMate's own code. The tool decodes the
sanitized Claude Code 2.1.329 bodies committed in
`internal/productruntime/testdata/protocolgolden/requests/` with the registered
`anthropic-messages-to-openai-responses` path and encodes them for the official
API with `PublicResponsesProfile()`. It then runs re-admission and the cyber
finalizer exactly as the Exchange does for a managed API-key account. A test
proves that, with the goldens' mapping, the bodies are byte for byte the
committed goldens in `internal/anthropicresponses/testdata/claude-2.1.329/`. A
probe changes only what its question needs: an output cap, an effort, a
profile switch, a synthetic filler or prompt, a tampered seal. Each body file
lists those edits. Nothing but the repository's sanitized corpus and the
tool's own synthetic text is ever sent.

## Two modes

- **plan** (the default) sends nothing. It builds every body, including the
  conditional ones, and prints each probe's ID, question, model, method, path,
  body size and ViberMate's own input-token estimate. It writes the bodies to
  the output directory for review. A body that depends on an earlier reply
  (Q4 turn two, Q9's later turns, the Q10 tampered seal) is built from a
  stand-in reply and marked `~`.
- **live** needs the explicit flag `--live` and a key. It sends the plan once,
  in order, with no retries, and writes the report. Each switch a refused
  default flips stays in force for the probes after it, as the fix would.

## Run it on the owner's Mac

You need Go at least the version in `go.mod` (`go 1.26.3`; the toolchain
line asks for `go1.26.11`, which the `go` command fetches by itself). Install
Go from https://go.dev/dl/ or with `brew install go`. If the module proxy is
slow or blocked from your network, add `GOPROXY=https://goproxy.cn,direct` in
front of the `go run` commands below. If `api.openai.com` is reachable only
through a proxy, set `HTTPS_PROXY` (for example
`export HTTPS_PROXY=http://127.0.0.1:7890`); the tool honours it.

Run every command below from the root of a checkout that contains
`tool/responses-probe`.

### 1. Store the key without it reaching your shell history

In Terminal (zsh, the macOS default), type the commands below as shown. The
key is typed at a hidden prompt, so it never appears on the command line, in
`~/.zsh_history` or in `ps`:

```sh
mkdir -p ~/.config/vibermate && chmod 700 ~/.config/vibermate
(umask 077; read -rs "key?OpenAI API key: "; echo; printf '%s\n' "$key" > ~/.config/vibermate/openai-probe.key; unset key)
ls -l ~/.config/vibermate/openai-probe.key   # must show -rw-------
```

In bash, use `read -rsp "OpenAI API key: " key` instead of `read -rs "key?OpenAI API key: "`.

The tool refuses a key file that its group or other users can read (any mode
looser than `0600`). Instead of a file, it also takes the key from
`OPENAI_API_KEY`.

### 2. Review the plan (no network)

```sh
go run ./tool/responses-probe --out ~/vibermate-probe/plan
```

### 3. Run it

```sh
go run ./tool/responses-probe --live \
  --key-file ~/.config/vibermate/openai-probe.key \
  --out ~/vibermate-probe/live
```

To include the two expensive probes as well, Q8 (long reasoning at `xhigh`
and `max`) and Q9 (a 10-turn cache session plus 3 turns with
`prompt_cache_breakpoint`), run:

```sh
go run ./tool/responses-probe --live --include-slow --max-requests 60 \
  --key-file ~/.config/vibermate/openai-probe.key \
  --out ~/vibermate-probe/live-slow
```

Q6 needs a model without reasoning, and the design names none. Pick one from
the `public_gpt_ids` that Q11 lists in `report.json`, then run Q6 alone (Q11
always runs first and is free):

```sh
go run ./tool/responses-probe --live --only Q6 --non-reasoning-model <id> \
  --key-file ~/.config/vibermate/openai-probe.key \
  --out ~/vibermate-probe/q6
```

When the run is done, delete the key file
(`rm ~/.config/vibermate/openai-probe.key`). If the key was made only for the
probe, revoke it.

## Cost

`plan` prints the request count and the estimated input tokens before
anything is sent. Live mode refuses to start when the plan, conditional
probes included, needs more requests than `--max-requests` (default 40). It
also stops sending at that number.

The default run sends 29 requests, plus up to 4 more that run only when a
default is refused. Its bodies come to about 0.6 M input tokens, and many of
them are cache hits. Every probe caps its output at `--max-output-tokens`
(default 64). Two kinds of probe get larger caps:

- Q4's first turn gets `--turn-output-tokens` (2048), so that it ends with a
  reasoning item followed by text.
- Q8 gets `--slow-output-tokens` (24000).

The two Q10 overflow bodies (about 1.5 M tokens of filler each) are meant to
be refused before billing. The tool stops reading either one as soon as it is
answered, so if the API takes one, only its input is billed.

`--include-slow` adds 15 requests:

- Q8: 2 reasoning streams of up to 24000 output tokens each, which can take
  several minutes.
- Q9: 13 short turns of about 12 k input tokens each.

## What a run writes

Each run writes these files to `--out` (by default a new directory under the
system temp directory, printed at the end):

| File | Content |
|---|---|
| `report.md` | the verdict table, every probe's status, timing and usage, any accepted probe whose stream the registered path could not translate, the Q10 error forms, the calibration table and the Q11 seed table |
| `report.json` | the same with every field: per probe the status, time to the first header and the first event, the longest gap between events, the error type, code and param, whether the message has numbers (the message itself truncated to 300 characters, and not kept at all on 401 and 403), usage (`input_tokens`, `cached_tokens`, `output_tokens`, `reasoning_tokens`), what ViberMate's classifier and Anthropic renderer make of a refusal (what Claude Code would see), and whether the real stream translates |
| `tokenestimate-calibration.json` | the real `input_tokens` per corpus body, keyed by fixture, beside the estimate at recording time: the proposed fixture for S16's calibration test (`internal/tokenestimate/testdata/claude-2.1.329-input-tokens.json`) |
| `bodies/NN-<probe>.json` | each body exactly as sent |
| `bodies/NN-<probe>.notes.txt` | the probe's edits, profile, and the decode and encode notices |
| `plan.json` | plan mode only: the plan with its counts |

S14 then takes these results to the places the design names:

- the verdicts and error forms go into the PR and
  `docs/agent-wire-evidence-matrix.md`;
- the context windows go into `openai_context_windows.dart` (when
  `/v1/models` lists none, as the report then says, take them from OpenAI's
  model pages for the API);
- the seed ids that exist go into `claude_model_defaults.dart`;
- the calibration file goes into the `tokenestimate` fixture;
- every `fail` becomes the named profile switch, with a test, in its own PR.

## Secrets

- The key is read only in live mode, from the 0600 file or the variable. It
  is sent only in the `Authorization` header, and plain `http` is refused
  except to a loopback test server.
- Every byte printed or written passes a filter that removes the key, its
  last characters and anything shaped like a provider key.
- Headers are kept only from an allowlist that leaves out the organization
  and project. Q11 keeps only the seed ids and public `gpt-` ids, never
  fine-tuned ids.
- `--verbose` logs each request and response head with `Authorization:
  [redacted]`.
- The tests run with a canary key against a fake upstream that echoes the key
  in error messages, headers and stream events, and check that it appears
  nowhere in stdout, stderr or the output directory.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--live` | off | send the probes (required for live mode) |
| `--key-file` | none | key file, mode 0600 or stricter; else `OPENAI_API_KEY` |
| `--base-url` | `https://api.openai.com` | API origin (tests point it at a fake) |
| `--models` | the seven candidates of design 11.2 | upstream models; Q1 runs on each, the preset rows map the corpus onto them |
| `--non-reasoning-model` | none | model for Q6 |
| `--max-requests` | 40 | request budget |
| `--max-output-tokens` | 64 | output cap of the acceptance probes |
| `--turn-output-tokens` | 2048 | output cap of replayed turns (Q4 turn one, Q9) |
| `--slow-output-tokens` | 24000 | output cap of Q8 |
| `--overflow-tokens` | 1500000 | filler in the Q10 oversized context (raised above a window `/v1/models` lists) |
| `--include-slow` | off | add Q8 and Q9 |
| `--only` | all | run only these questions, e.g. `Q6,Q10` |
| `--out` | temp directory | output directory |
| `--repo` | found above the working directory | repository root |
| `--timeout` | 30m | per-request limit |
| `--verbose` | off | log request and response heads to stderr |

The exit code is 0 when the run completed, whatever the verdicts. It is 1 when
the run could not complete, for example when the key was refused, and 2 for a
usage error or a refused key file.

## Tests

```sh
go test ./tool/responses-probe -count=1
```

The tests run every probe end to end against an `httptest` Responses server
(streamed SSE, JSON errors, an in-band `response.failed`). They also cover the
key-file mode rule, the `--live` requirement, the budget, the redaction
canary, plan mode's output, and byte equality with the registered path's
goldens.
