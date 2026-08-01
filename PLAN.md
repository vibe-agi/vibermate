# M1.0-C0h-2 Connection Policy: The Ask Path

Status: active
Created: 2026-08-02
Implementation baseline: `0918d6b`
Branch: `m1/root-leaf-foundation`
Predecessor: `docs/plans/archive/2026-08-02-m1.0-c0h-1-policy-rules-and-deny.md`
Defers: `docs/plans/deferred/2026-08-01-m1.0-c-macos-trust-observation.md`

## Objective

`ask` is the decision the product is actually for: a connection waits on a
person before it is dialled. The rule set refuses to construct one today,
deliberately, because a blocking decision that cannot block is an allow wearing
a different name.

This slice makes it real. It is the highest-risk work in the foundation,
because it is a blocking wait in front of a dial: every way it can fail must
fail closed, and it must not deadlock the proxy while it waits.

## Read-only design authority

- `docs/design/06-security.md` §4.1, including the aggregation key and the
  fail-closed rules;
- `docs/design/04-ux.md` §4.1 and §4.6;
- `docs/design/03-ui.md` §3.2.

## Required invariants

1. An `ask` blocks before the dial. Nothing is resolved, connected, or issued
   while the question is open.
2. Every failure denies. Timeout, cancellation, shutdown, a full queue, and an
   unavailable approval authority all produce deny, never allow.
3. Identical questions merge. Design 06 keys aggregation on the kind, the
   ingress, the host, and the port, so a burst of connections to one host is
   one question answered once for all of them.
4. A caller that goes away does not answer for the others. Its waiter leaves
   and the question stays open while anyone still waits.
5. The record carries the host and port as its subject, and no path, header,
   body, or credential. A blind connection has none of those to begin with,
   and the record must not acquire them.
6. Waiting is bounded. The proxy does not hold a connection open indefinitely
   because nobody is looking at the approval queue.

## Non-goals

- remembered rules and rule scopes beyond recording the chosen one;
- rule editing through a control API or UI;
- renaming the approval package, which now serves more than tool intent and is
  misnamed. That is mechanical churn and is tracked separately.

## Bottom-up implementation

- [x] Add a network-ask entry point to the approval authority.
- [x] Key aggregation on kind, ingress, host, and port.
- [x] Allow the rule set to construct an ask.
- [x] Block the proxy on it before the dial, and deny on every failure.
- [x] Prove merging, independent cancellation, bounded waiting, and that no
      failure produces allow.
- [x] Make the approval record storable without a plan binding, which the
      tool-intent schema required and a network ask cannot supply.
- [x] Make the merged count true, so one question reports how many connections
      are actually waiting on it.

## What this slice did not turn on

The shipped policy still carries `interim.allow-unmatched-pending-ask` rather
than asking about an unknown host. `ask` now works end to end and is answerable
through `POST /api/v1/approvals/{id}/actions/decide`, but nothing remembers an
answer yet, so a default of `ask` would ask again for every connection to a
host that was just allowed. Turning the default on belongs with remembered
decisions in the next slice, and the interim rule stays named in every
connection record until then.

## Gates

`make check`, `gofmt -l .`, `go test -count=1 ./...`,
`go test -race -count=1 ./...`, repeated race stress for `toolapproval`,
`loopbackproxy`, `connectionpolicy`, `go vet ./...`, `go mod tidy -diff`,
`go mod verify`, pinned `govulncheck`, `git diff --check`, clean tree.

## Completion statement

> A connection whose rule asks waits on a person before it is dialled,
> identical questions are one question, and every way the wait can fail denies.

It does not implement remembered rules or rule editing.
