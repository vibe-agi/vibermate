# M1.0-C0e Complete Ingress Protocol Surface

Status: active
Created: 2026-08-02
Implementation baseline: `8f5f81c`
Branch: `m1/root-leaf-foundation`
Predecessor: `docs/plans/archive/2026-08-02-m1.0-c0d-catalogued-dispatch.md`
Defers: `docs/plans/deferred/2026-08-01-m1.0-c-macos-trust-observation.md`

## Objective

Everything that reaches the listener must be tunnelled, forwarded, or refused
with an audit record. Two shapes still fall outside that:

1. A cleartext `http://` request is answered 405 without any handling. An Agent
   that reaches an `http://` host through the exported proxy simply fails.
2. A WebSocket upgrade on an uncatalogued path inside a MITM connection is
   silently degraded to an ordinary GET, so the client believes it negotiated
   a protocol the proxy never spoke.

## Read-only design authority

- `docs/design/02-architecture.md` §5.4 and §10.3;
- `docs/design/06-security.md` §4.1;
- `docs/design/07-protocol-translation.md` §6.5.

## Required invariants

1. A cleartext forward-proxy request is either forwarded to its origin or
   refused, and either way it leaves a connection record and an
   `EgressAttempt`. It never enters a model pipeline and never carries a
   provider credential.
2. Design 06 is explicit that a proxy necessarily sees a cleartext request
   line, so the record states honestly that this connection was not encrypted
   rather than implying blindness it does not have. It still records no body.
3. An upgrade the proxy cannot serve is refused explicitly. It is never
   answered as though it were an ordinary request.
4. Nothing here widens what may be decrypted, and no cleartext path may carry
   a `SecretRef` or a provider credential.

## Non-goals

- WebSocket message semantics, framing, or plugins;
- connection policy;
- HTTP/2.

## Bottom-up implementation

- [x] Classify a cleartext forward-proxy request and give it a typed decision.
- [x] Forward it through the gated egress boundary with both records; an
      origin-form request and an unauthenticated forward are still refused,
      and both leave an audit record.
- [x] Refuse an unsupported upgrade explicitly instead of degrading it.
- [x] Prove no body, credential, or model pipeline is reachable from either.
- [x] Prove a real captured child process reaches a non-model host through the
      real launcher and the real proxy over real sockets.

## A test that proved nothing

The captured-child test first fetched a loopback origin with an ordinary
`http.Get`. It passed, and it proved nothing: Go unconditionally skips a proxy
for a loopback target, so the child had connected directly with the proxy
uninvolved. The test now asserts the proxy recorded the outbound, and the child
builds its transport from the exported variables explicitly. What it proves is
what belongs to vibermate: the launcher exported a usable proxy address and
credential, and the proxy forwarded a request to a host that is not a model
API. Go's own automatic proxy selection is not under test.

## Gates

`make check`, `gofmt -l .`, `go test -count=1 ./...`,
`go test -race -count=1 ./...`, `go vet ./...`, `go mod tidy -diff`,
`go mod verify`, pinned `govulncheck`, `git diff --check`, clean tree.

## Completion statement

> Every shape that reaches the listener is tunnelled, forwarded, or explicitly
> refused, and each leaves connection and per-egress evidence.

It does not implement WebSocket semantics, connection policy, or HTTP/2.
