# M1.0-C0f Ingress Identity and Observation

Status: active
Created: 2026-08-02
Implementation baseline: `9afec27`
Branch: `m1/root-leaf-foundation`
Predecessor: `docs/plans/archive/2026-08-02-m1.0-c0g-generic-approvals.md`
Defers: `docs/plans/deferred/2026-08-01-m1.0-c-macos-trust-observation.md`

## Objective

Two ingress facts are currently asserted rather than observed.

A CaptureRun reports the same state whether traffic arrived through it or not.
Design 02 is explicit that a run created but never used is
`waiting_for_traffic`, not captured: a program that ignores proxy variables,
clears its environment, dials a socket directly, or uses QUIC produces exactly
this shape, and telling the user it was captured is the difference between a
working setup and a silent one.

Separately, the connection record uses the CaptureRun identity as its ingress
identity. They are different objects: a CaptureRun is one short-lived source,
while an IngressProfile is the stable thing connection rules, network egress
rules, and per-ingress statistics are scoped by. Substituting one for the other
leaves a connection with no ingress identity at all once an ingress is not a
CaptureRun.

## Read-only design authority

- `docs/design/02-architecture.md` §4.2 and the IngressProfile terminology;
- `docs/design/06-security.md` §4.1 and §4.3;
- `docs/adr/0003-capture-run-and-agent-session.md`.

## Required invariants

1. A CaptureRun distinguishes created, observed, and finished. Observation is
   recorded from real authenticated proxy traffic, never inferred from the
   fact that a child was launched.
2. A run that never carried traffic is reported honestly. The product does not
   claim capture it did not observe.
3. Observation is monotonic and idempotent: the first authenticated connection
   marks it, later ones do not rewrite it, and a finished run does not become
   observed afterwards.
4. Nothing here infers attribution from a fingerprint, user agent, loopback
   source port, or connection reuse. Those signals are shared between
   processes and are not identity.

## Non-goals

- IngressProfile as a control-plane object with its own routes and UI, which
  belongs with connection policy that consumes it;
- system and application proxy pointing;
- session resolution.

## Bottom-up implementation

- [x] Add a typed observation state to the CaptureRun record and persist it.
      The state is required rather than defaulted, so a writer that forgot the
      field cannot look like one that observed nothing.
- [x] Mark it from the first authenticated proxy connection only, in the one
      place that can honestly know.
- [x] Prove monotonicity, idempotence, and that a finished run cannot become
      observed.
- [x] Prove a launched-but-unused run reports honestly.

## A precision bug the end-to-end test caught

The first authorization returned an untruncated observation time while the
stored value was truncated to milliseconds, so the same fact differed between
the first read and every later one. The value returned to a caller is now the
value that was stored.

## Gates

`make check`, `gofmt -l .`, `go test -count=1 ./...`,
`go test -race -count=1 ./...`, `go vet ./...`, `go mod tidy -diff`,
`go mod verify`, pinned `govulncheck`, `git diff --check`, clean tree.

## Completion statement

> A CaptureRun reports whether traffic was actually observed through it, marked
> only from real authenticated proxy traffic and never inferred.

It does not implement IngressProfile as a control-plane object, proxy
pointing, or session resolution.
