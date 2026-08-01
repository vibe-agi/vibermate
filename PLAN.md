# M1.0-C0b Egress Identity and Audit Foundation

Status: active
Created: 2026-08-02
Implementation baseline: `319047c`
Branch: `m1/root-leaf-foundation`
Predecessor: `docs/plans/archive/2026-08-02-m1.0-c0a-payload-failclosed.md`
Defers: `docs/plans/deferred/2026-08-01-m1.0-c-macos-trust-observation.md`

## Objective

`ConnectionEvent` currently answers two different questions with one mutable
record, and one identity is built by string concatenation. Both are closed by
`ADR-0015`, which this Goal implements.

Today a persistent MITM connection carrying several requests overwrites its
`RouteHost` and `CredentialBindingID` with whichever semantic Exchange ran
last, and an opaque request contributes nothing at all. A reader therefore
cannot tell where a connection's traffic actually went. Separately,
`exchangeID = run.RunID + "/" + exchangeID` encodes containment in a string,
which `ADR-0015` §10 forbids outright.

## Read-only design authority

- `docs/adr/0015-egress-purpose-authority-and-audit-cardinality.md` §7–§11;
- `docs/design/02-architecture.md` §10.3;
- `docs/adr/0002-blind-tunnel-firewall-and-connection-events.md`.

## Required invariants

1. `CaptureRunID`, `ConnectionID`, `ExchangeID`, `UpstreamAttemptID`, and
   `EgressAttemptID` are generated independently. No identity encodes
   containment of another as a substring, prefix, or delimiter-joined value.
   Association is expressed only by typed references.
2. `ConnectionEvent` describes one client-side connection: ingress, source
   confidence, requested authority, observed SNI, ConnectionPolicy decision,
   MITM or blind mode, aggregate downstream bytes, duration, and terminal
   state. Request-level provider target, credential decision, egress rule,
   proxy, and route host are never written back onto it.
3. Every real outbound attempt produces one immutable `EgressAttempt` freezing
   its ID, purpose, policy authority and revision, authoritative target,
   egress decision, selected proxy, DNS mode, payload class, typed parent,
   start and terminal state, and byte counts. It holds no secret, header,
   body, or tunnelled bytes.
4. Typed parents are exhaustive and validated at construction:
   `provider_attempt` requires an `UpstreamAttempt` parent with an explicit
   `ExchangeID`; `profile_operation` requires a client-operation parent with no
   `ExchangeID`; `original_origin` and `agent_probe` require the original
   request plus a connection; `blind_tunnel` requires a connection; runtime
   purposes require a runtime action and may have no connection. Invalid
   combinations are refused rather than encoded as empty strings.
5. Connection-pool reuse still writes one record per logical attempt, marked
   `reused_transport`. It never overwrites an earlier record.
6. `SourceConfidence` is derived, not hardcoded. A CaptureRun that carries
   verified compound-release adapter evidence reports `verified`; a run without
   it reports `configured`; a connection with no accepted run reports
   `unknown`. If the design intends adapter evidence to remain `configured`,
   the enum value that cannot be produced is removed instead of being left
   unreachable.
7. Storage, control API, and any reader present the two records separately.
   A record predating this schema is labelled legacy rather than being
   synthesised into per-request facts.

## Non-goals

- blind tunnelling itself and connection `allow/deny/ask`, which are C0c;
- `RuntimeEgressPolicy`, `AccessEgressPolicy` rule evaluation, and
  `UpstreamProxyProfile`, which need the egress-policy Goal;
- `profile_operation` and Language Bridge;
- Root, trust store, system proxy;
- UI beyond whatever the control contract requires to stay honest.

## Bottom-up implementation

### 1. Independent typed identity

- [ ] Give `exchange.ClientRequest` typed `CaptureRunRef` and `ConnectionRef`
  correlation instead of a concatenated Exchange ID.
- [ ] Generate the Exchange ID independently in the proxy and pass the run and
  connection as references.
- [ ] Prove no production identity contains another identity as a substring.
- [ ] Add a structural rule rejecting delimiter-joined identity construction.

### 2. Narrow ConnectionEvent

- [ ] Remove request-level fields from the connected phase; keep client-side
  connection facts only.
- [ ] Migrate the schema and mark pre-migration rows legacy.
- [ ] Prove a persistent connection carrying several requests keeps one stable
  connection record.

### 3. EgressAttempt

- [ ] Promote the design-repository prototype shape into a production package
  with constructor validation for purpose, authority, payload class, and typed
  parent.
- [ ] Persist through `runtimepersistence` with a forward migration.
- [ ] Emit from the provider transport and the original-origin transport.
- [ ] Prove one attempt per real outbound, pool reuse marked, and no secret,
  header, or body anywhere in the record.

### 4. Source confidence

- [ ] Derive from CaptureRun adapter evidence, or delete the unreachable enum
  value with a recorded reason.

### 5. Read path

- [ ] Expose both records through the authenticated control slice so the two
  decisions can be read separately.

## Gates

`make check`, `gofmt -l .`, `go test -count=1 ./...`,
`go test -race -count=1 ./...`, repeated race stress for `loopbackproxy`,
`connectionevent`, `runtimepersistence`, `providertransport`,
`originaltransport`, `go vet ./...`, `go mod tidy -diff`, `go mod verify`,
pinned `govulncheck`, `git diff --check`, clean tree.

## Completion statement

> Connection facts and per-egress facts are separate immutable records with
> independently generated identities and typed references, so a persistent
> connection can no longer misreport where its traffic went.

It does not prove blind tunnelling, egress policy evaluation, upstream proxy
support, or release readiness.
