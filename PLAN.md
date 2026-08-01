# M1.0-C0h-3 Connection Policy: Rules a Person Owns

Status: active
Created: 2026-08-02
Implementation baseline: `b352fee`
Branch: `m1/root-leaf-foundation`
Predecessor: `docs/plans/archive/2026-08-02-m1.0-c0h-2-the-ask-path.md`
Defers: `docs/plans/deferred/2026-08-01-m1.0-c-macos-trust-observation.md`

## Objective

The rule set is a constant compiled into the runtime. Nobody can add a host,
remove one, or see what is in force, and every start reconstructs the same
placeholder. Until rules are durable and editable, `ask` cannot become the
shipped default: a person would answer the same question forever, because
there is nowhere for an answer to live.

This slice gives the rules an owner. They are stored, they survive a restart,
they can be read and changed while the runtime is running, and a change takes
effect on the next connection rather than the next launch.

## Read-only design authority

- `docs/design/06-security.md` §4.1, including `INV-FIREWALL-NO-WILDCARD`;
- `docs/design/02-architecture.md` §12, the `[[proxy.firewall.rules]]` shape;
- `docs/design/15-local-control-api.md`.

## Required invariants

1. A stored rule set is still a rule set. Everything the constructor refuses
   today — a wildcard allow default, a missing default, a pattern language —
   it refuses when the rules come from storage.
2. Order is explicit and stable. A stored set has no insertion order a person
   can see, so precedence is a declared property of a rule and ties resolve
   the same way on every start.
3. A change takes effect on the next connection. A connection already decided
   is not revisited; a connection not yet decided sees the new set whole,
   never half of one revision and half of another.
4. A rejected change changes nothing. A rule set that would not construct is
   refused before it is stored, so the runtime cannot be left holding rules it
   would not have accepted.
5. The shipped set is still not a wildcard allow. Seeding writes the same
   named interim rule, and a permissive set remains something a person has to
   write on purpose.

## Non-goals

- remembered decisions, which are the next slice: a choice that writes a rule
  must commit with the decision it came from, per `INV-APPROVAL-TERMINAL-ATOMIC`;
- flipping the shipped default to `ask`, which waits on remembering;
- the `policy_scope` component of the aggregation key in design 06 §4.1, which
  has no values until scopes exist;
- a configuration file. Rules live in the runtime store in this slice; the
  file layering in design 02 §12 is a separate source.

## Bottom-up implementation

- [ ] Give a rule an explicit precedence and a deterministic tie-break.
- [ ] Store rules durably, with the default as a stored rule rather than a
      constructor argument.
- [ ] Seed the shipped set exactly once, and never seed a wildcard allow.
- [ ] Hold the live set behind a revision the proxy reads per connection.
- [ ] Read and change rules through the control API, with CAS on the revision.
- [ ] Prove: a refused set leaves the old one in force, a change is visible to
      the next connection and not to one already decided, and a restart brings
      back exactly what was stored.

## Gates

`gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`,
`go test -race -count=1 ./...`, repeated race stress for `connectionpolicy`,
`loopbackproxy`, `runtimepersistence`, `go run ./cmd/repositorycheck`,
`go mod tidy -diff`, `go mod verify`, `git diff --check`, clean tree.

## Completion statement

> Connection rules are stored, ordered, editable while the runtime runs, and
> refused as a whole when they would not construct.

It does not implement remembered decisions or turn `ask` on by default.
