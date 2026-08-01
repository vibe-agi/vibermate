# M1.0-C0g Generic ApprovalCenter

Status: active
Created: 2026-08-02
Implementation baseline: `12d40c6`
Branch: `m1/root-leaf-foundation`
Predecessor: `docs/plans/archive/2026-08-02-m1.0-c0e-ingress-surface.md`
Defers: `docs/plans/deferred/2026-08-01-m1.0-c-macos-trust-observation.md`

## Objective

The design has one ApprovalCenter serving network `ask`, tool intent, plugin
permission, authorized outbound, and high-risk configuration, with identical
pending items merged into one entry rather than one prompt per event. The
durable record today is tool intent and nothing else: the kind, risk, copy
keys, and choices are all constants, the subject fields are tool call IDs and
tool names, and an Access plan binding is mandatory.

That last constraint is the blocking one. A network `ask` is decided before
any Access is resolved, so it cannot supply a plan binding at all. Connection
policy cannot be built on this record, and adding a second approval kind would
mean forking it.

## Read-only design authority

- `docs/design/04-ux.md` §4.1 and §4.6;
- `docs/design/06-security.md` §4.1;
- `docs/design/03-ui.md` §3.2.

## Required invariants

1. A record declares its `Kind`. Risk, copy keys, and available choices are
   derived from the kind rather than assumed.
2. Identical pending items merge on a stable `AggregateKey`, and the record
   counts how many requests and how many waiters that entry represents. A
   burst of the same question is one entry, not one prompt per event.
3. The Access plan binding is optional, because a decision taken before Access
   resolution has none. When present it is still frozen and still checked, so
   a stale plan cannot resolve a pending item.
4. A subject is carried as redacted identifiers and safe display labels only.
   No record holds a path, header, body, argument value, or credential.
5. Fail-closed behaviour is unchanged: expiry, cancellation, shutdown, and a
   full queue all deny, and a decision remains compare-and-swap and
   idempotency-keyed.
6. The existing tool-intent behaviour is preserved exactly, including its
   blocking barrier and its stable i18n keys.

## Non-goals

- connection policy itself, which consumes this;
- remembered rules beyond recording the chosen scope;
- plugin permissions and authorized outbound, which have no runtime yet;
- UI beyond what the control contract needs.

## Bottom-up implementation

- [x] Generalize the durable record: kind, aggregate key, subject refs and
      labels, request and waiter counts, optional plan binding.
- [x] Derive risk, copy keys, and choices from the kind.
- [x] Migrate the schema and preserve existing rows as tool intent, each with
      its own identity as its aggregate key so nothing merges retroactively.
- [x] Merge identical pending questions onto one entry at runtime: one
      decision releases every waiter, a cancelled waiter does not answer the
      others, and the entry is freed only when its last waiter leaves.
- [x] Prove counting, optional binding, and unchanged fail-closed behaviour.

## A harness bound, not a product contract

The runtime test harness allowed five seconds to start, which the whole suite
under race instrumentation began to exceed once a fourteenth migration existed.
Migrations run in well under a second in isolation; the pure-Go SQLite driver
with the suite running in parallel makes that time a function of machine
contention. The bound was raised rather than the migration trimmed, because the
product has no five-second startup contract and trimming would have hidden
nothing real.

## Gates

`make check`, `gofmt -l .`, `go test -count=1 ./...`,
`go test -race -count=1 ./...`, `go vet ./...`, `go mod tidy -diff`,
`go mod verify`, pinned `govulncheck`, `git diff --check`, clean tree.

## Completion statement

> One durable approval record serves more than one kind, merges identical
> pending items with counts, and does not require an Access plan binding for a
> decision taken before Access resolution.

It does not implement connection policy, remembered rules, or plugin
permissions.
