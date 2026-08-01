# M1.0-C0h-1 Connection Policy: Rules and Deny

Status: active
Created: 2026-08-02
Implementation baseline: `0080687`
Branch: `m1/root-leaf-foundation`
Predecessor: `docs/plans/archive/2026-08-02-m1.0-c0f-ingress-observation.md`
Defers: `docs/plans/deferred/2026-08-01-m1.0-c-macos-trust-observation.md`

## Objective

The product's central claim is that a user decides, before a dial, which hosts
a captured program may reach. That decision does not exist: the proxy writes a
fixed allow with a literal rule identifier for anything that matched an
AgentEndpoint, and blind forwarding allows everything else. `DecisionAsk` and
the asked phase are unreachable.

This is the first of three slices. It builds the rule set and the decision, and
wires `allow` and `deny`. The `ask` path and remembered rules follow, because
`ask` blocks a dial on a person and must not be shipped half-built: a
half-built blocking decision fails open, which is the one outcome worse than
not having it.

## Read-only design authority

- `docs/design/06-security.md` §4.1, including `INV-FIREWALL-NO-WILDCARD`;
- `docs/design/02-architecture.md` §5.5 ordering;
- `docs/adr/0002-blind-tunnel-firewall-and-connection-events.md`.

## Required invariants

1. The decision happens before any dial, DNS resolution, or certificate
   issuance, for every proxied connection including a blind tunnel.
2. Rules are ordered and the first match wins. Evaluation is deterministic:
   the same rule set and the same connection always produce the same decision
   and name the same rule.
3. The shipped default is not a wildcard allow. Design 06 makes that an
   invariant, because a default that allows everything makes the firewall the
   one control that never fires.
4. A decision names the rule that produced it, and the connection record
   carries that name rather than a literal.
5. `ask` is not reachable in this slice. A rule set that asks is refused at
   construction rather than silently downgraded, so nothing depends on a
   half-built blocking decision.

## Non-goals

- the ask path, the approval integration, and remembered rules, which are the
  next slice;
- a control API or UI for editing rules;
- per-ingress scoping beyond what the record already carries.

## Bottom-up implementation

- [ ] Add an ordered rule set with deterministic first-match evaluation.
- [ ] Prove the default is not a wildcard allow and that ask is refused.
- [ ] Evaluate before dial for both the MITM and blind paths.
- [ ] Record the matched rule rather than a literal.

## Gates

`make check`, `gofmt -l .`, `go test -count=1 ./...`,
`go test -race -count=1 ./...`, `go vet ./...`, `go mod tidy -diff`,
`go mod verify`, pinned `govulncheck`, `git diff --check`, clean tree.

## Completion statement

> Every proxied connection is decided by an ordered rule set before any dial,
> the decision names its rule, and the shipped default is not a wildcard allow.

It does not implement ask, remembered rules, or rule editing.
