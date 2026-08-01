# Live provider run

Date: 2026-08-02
Implementation: `6a2ad19` plus the egress-identity fix in this commit
Test: `internal/productruntime.TestALiveProviderAnswersThroughTheWholePipeline`

## What ran

A client request in Anthropic Messages went through the whole ProductRuntime
to a real OpenAI-compatible backend and came back in Anthropic Messages.

```
VIBERMATE_LIVE_PROVIDER_ORIGIN=http://127.0.0.1:23333/v1 \
VIBERMATE_LIVE_PROVIDER_KEY=<redacted> \
VIBERMATE_LIVE_PROVIDER_MODEL=dashscope:glm-5 \
go test ./internal/productruntime/ -run TestALiveProvider -count=1 -v
```

The run passed in 1.22s. The model answered `"ready"`.

## What it proves

- the frozen Access plan resolves and the model policy substitutes the backend
  model for the client's alias;
- the credential is stored behind a SecretRef in the development file store,
  resolved at the boundary, and injected as a provider header;
- the request is translated Anthropic Messages → OpenAI chat completions, and
  the answer is translated back, with usage carried through;
- the outbound is recorded as a completed provider attempt against the real
  destination, with a non-zero received byte count;
- the credential does not appear in the audit record.

## The same thing, through the proxy

`TestALiveProviderAnswersThroughTheProxy` runs the same backend the way a
client reaches it: an ordinary `http.Client` with a proxy address, the local
root in its trust store, and no other knowledge of vibermate. It POSTs to
`https://api.anthropic.com/v1/messages`, the proxy authorizes the CONNECT
against a CaptureRun capability, decides the connection against the stored
rules, terminates TLS with a leaf issued for that exact host, and the answer
comes back. The connection is on the record as decrypted rather than as an
opaque tunnel.

The run passed in 3.12s. The model answered `"ready"`.

Note that the client sent its own `x-api-key`. It is not forwarded: the
provider credential comes from the SecretRef in the plan.

## Streaming, the way a client asks for it

`TestALiveProviderStreamsThroughTheProxy` sends `"stream": true` through the
same proxy and reads the response as a client would. It came back as
`text/event-stream` carrying Anthropic Messages events — `message_start`,
`content_block_delta`, `message_delta`, `message_stop` — with the text
assembled from the deltas and usage present (`input 10, output 686` on the
recorded run). The provider attempt reached a terminal with a non-zero
received byte count.

The run took 17.8s, which is the model's latency rather than the product's.

## The other client dialect

`TestALiveProviderAnswersAResponsesClient` runs an OpenAI Responses request
against the same backend, which speaks OpenAI chat completions. The answer
comes back as a Responses object with output items and usage, not as a chat
completion relabelled. It goes through the Exchange rather than the proxy
because the Codex binary on this machine is a release the catalog has no
evidence for; the translation is what is under test, not the launch.

The run passed in 3.5s. The model answered `"ready"`.

## What neither run proves

The backend is a local OpenAI-compatible service over loopback cleartext. A
strict-TLS provider origin exercises a different transport path.

Codex is not driven end to end; see the deferred note on the client catalog.
Claude Code is, in `internal/desktophost`.

No run exercises a stream that ends early, a mid-stream failover, or the
Responses WebSocket path.

## What they found

Every provider request had been failing. `internal/exchange` passed one
generated identity as both the upstream attempt ID and the outbound's ID, so
the outbound could not be recorded — ADR-0015 §10 refuses an identity that
encodes its parent — and the Exchange failed with `provider_transport_failed`.

No test caught it, because every other test in the suite substitutes a fake
provider that never reaches the audit. This is the first request that actually
went out.

The proxy run found a second one. Every connection record was storing the
CONNECT authority in `RequestedHost`, a field that sits beside a separate
`Port`, so the record stated the port twice and any reader joining the two
rendered `api.anthropic.com:443:443`. The host field now refuses a value
carrying a port, and the proxy writes the host alone — while still recording
an authority the client sent that is not a usable name, because a refusal
nobody can see is the case most worth seeing.

## A real agent client

`internal/desktophost.TestARealAgentClientReachesAModelThroughVibermate`
launches the installed Claude Code binary through the product's own launcher —
the same path `vibermate run --` takes — and asks it one question.

```
VIBERMATE_LIVE_PROVIDER_ORIGIN=http://127.0.0.1:23333/v1 \
VIBERMATE_LIVE_PROVIDER_KEY=<redacted> \
VIBERMATE_LIVE_PROVIDER_MODEL=dashscope:glm-5 \
VIBERMATE_LIVE_AGENT=claude \
go test ./internal/desktophost/ -run TestARealAgentClient -count=1 -v
```

Claude Code 2.1.220 printed `ready`. The launcher gave it the proxy address,
the CaptureRun credential, and the local root; it knew nothing else about
vibermate. The connection is recorded as decrypted, and a provider attempt
reached the live backend.

### What it took

Two defects stood between the client and the model, and neither was reachable
from a synthetic request:

- Claude Code sends `system` messages inside the message list, not only as the
  top-level `system` parameter. The decoder accepted only user and assistant
  roles, so the client's first request was refused. Design 07 §3.2 normalizes
  roles to system | developer | user | assistant | tool and requires the
  original position to survive, which is what an instruction placed
  mid-conversation means.
- Claude Code sends `defer_loading`, a request option this dialect does not
  model. Strict decoding refused the whole request over it. Clients add fields
  faster than any translator learns them; design 07 §2.3 requires the wire
  layer to keep unknown fields rather than let a typed unmarshal make them
  disappear, so the field is now declared as not forwarded instead of fatal.
  Only the request object is tolerant: an unknown field inside a tool
  definition or a content block still fails closed, because there it changes
  what the structure means.
