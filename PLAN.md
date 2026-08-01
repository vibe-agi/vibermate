# M1.0-C0c Network Blind Tunnel Data Plane

Status: active
Created: 2026-08-02
Implementation baseline: `8737ab3`
Branch: `m1/root-leaf-foundation`
Predecessor: `docs/plans/archive/2026-08-02-m1.0-c0b-egress-identity-and-audit.md`
Defers: `docs/plans/deferred/2026-08-01-m1.0-c-macos-trust-observation.md`

## Objective

The proxy refuses every CONNECT whose authority is not an enabled
AgentEndpoint. The launcher exports `HTTP_PROXY` to the whole child process
tree, so today an Agent's every non-model host is refused with 403: package
installs, update checks, MCP servers, and the Agent's own fetch tools. The
product is unusable for a real Agent until an unmatched CONNECT is tunnelled
without decryption.

## Read-only design authority

- `docs/adr/0002-blind-tunnel-firewall-and-connection-events.md`;
- `docs/adr/0015-egress-purpose-authority-and-audit-cardinality.md` §3, §9;
- `docs/design/02-architecture.md` §10.3;
- `docs/design/06-security.md` §4.1 and §4.3.

## Required invariants

1. An unmatched CONNECT is forwarded byte-for-byte without terminating TLS.
   No certificate is issued, no ClientHello is interpreted beyond what the
   connection record already captures, and no content enters any pipeline.
2. A blind connection records one `ConnectionEvent` with client-side facts and
   one `EgressAttempt` with purpose `blind_tunnel`, payload class
   `opaque_tunnel`, and a blind-connection parent.
3. A blind tunnel never records a URL path, header, request body, response
   body, or any tunnelled byte. Only counts, duration, and outcome.
4. Bidirectional copying is bounded, cancellable, and drains within the
   owner's shutdown deadline. A half-closed peer does not leak the other half.
5. This Goal does not decide whether a connection is allowed. Admission stays
   as it is until connection policy lands; blind forwarding is the transport,
   not the decision.

## Non-goals

- `allow/deny/ask` and the ApprovalCenter, which are separate Goals;
- cleartext HTTP forward proxying;
- WebSocket semantics beyond opaque byte forwarding;
- egress policy selection and upstream proxies.

## Bottom-up implementation

- [ ] Add a bounded bidirectional tunnel with owner-context cancellation and
      byte counting on both directions.
- [ ] Dial the unmatched authority through the gated egress boundary so a
      blind tunnel cannot bypass planned offline hold.
- [ ] Emit the blind `EgressAttempt` and complete it on close.
- [ ] Prove no path, header, or payload reaches any record.
- [ ] Prove shutdown drains an active tunnel and a half-closed peer.

## Gates

`make check`, `gofmt -l .`, `go test -count=1 ./...`,
`go test -race -count=1 ./...`, repeated race stress for `loopbackproxy`,
`connectionevent`, `egressaudit`, `go vet ./...`, `go mod tidy -diff`,
`go mod verify`, pinned `govulncheck`, `git diff --check`, clean tree.

## Completion statement

> A connection whose authority is not an enabled AgentEndpoint is forwarded
> without decryption and leaves both a connection record and a per-egress
> record, neither of which contains any tunnelled byte.

It does not prove connection policy, cleartext forwarding, or release
readiness.
