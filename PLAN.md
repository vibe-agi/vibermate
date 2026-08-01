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

- [ ] Classify a cleartext forward-proxy request and give it a typed decision.
- [ ] Forward or refuse it through the gated egress boundary with both records.
- [ ] Refuse an unsupported upgrade explicitly instead of degrading it.
- [ ] Prove no body, credential, or model pipeline is reachable from either.

## Gates

`make check`, `gofmt -l .`, `go test -count=1 ./...`,
`go test -race -count=1 ./...`, `go vet ./...`, `go mod tidy -diff`,
`go mod verify`, pinned `govulncheck`, `git diff --check`, clean tree.

## Completion statement

> Every shape that reaches the listener is tunnelled, forwarded, or explicitly
> refused, and each leaves connection and per-egress evidence.

It does not implement WebSocket semantics, connection policy, or HTTP/2.
