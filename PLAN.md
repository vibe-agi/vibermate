# M1.0-C0h-4 Connection Policy: Remembering an Answer

Status: active
Created: 2026-08-02
Implementation baseline: `747d057`
Branch: `m1/root-leaf-foundation`
Predecessor: `docs/plans/archive/2026-08-02-m1.0-c0h-3-rules-a-person-owns.md`
Defers: `docs/plans/deferred/2026-08-01-m1.0-c-macos-trust-observation.md`

## Objective

A person can answer a connection question, and a person can write a rule, but
the two are unrelated: answering the same question about the same host forever
is the only thing on offer. Remembering is what makes `ask` a usable default
rather than an interrogation.

## Read-only design authority

- `docs/design/06-security.md` §4.1 and `INV-APPROVAL-TERMINAL-ATOMIC`;
- `docs/design/04-ux.md` §4.1 and §4.6;
- `docs/design/02-architecture.md` §10.1.

## Required invariants

1. The answer and the rule it creates commit together. A remembered decision
   that survived without its rule would ask again; a rule that survived
   without its decision would answer a question nobody was asked.
2. A remembered answer is exactly as wide as what was asked. Allowing a host
   and port does not allow the host on another port, and never allows anything
   else. A wider rule stays something a person writes on purpose.
3. The rules in force follow the commit. A remembered answer that only reached
   storage would ask again on the next connection until a restart.
4. An unremembered answer changes no rules at all.
5. A person is shown what remembering will do before they choose it.

## Non-goals

- the `policy_scope` component of the design 06 aggregation key, which needs
  policy scopes rather than the decision scopes this slice adds;
- flipping the shipped default to `ask`, which is decided at the end of this
  slice on whether a person can actually see and answer a question in time;
- a rule simulator.

## Bottom-up implementation

- [x] Add remembered decision scopes, refused for kinds that cannot mean them.
- [x] Present remembering as a distinct choice with its own subject.
- [x] Write the rule and the terminal decision in one commit.
- [x] Put the new rules in force as soon as that commit lands.
- [x] Prove: the pair commits or neither does, the width is exactly the
      question, an unremembered answer changes nothing, and the next
      connection is decided without asking again.

## The flip, and why it is still not here

Remembering was the missing half of `ask`. The other half is that a person has
to see the question in time to answer it. Today that means the control API and
a capability token: there is no window and no command that shows a waiting
question. A shipped default of `ask` would hang every first connection for the
decision timeout in front of anyone who has not written their own client.

So the flip moves to the next slice, which gives it a surface: a person can
list what is waiting and answer it, and only then does the shipped default
become the one design 06 asks for.

## Gates

`gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`,
`go test -race -count=1 ./...`, race stress for `toolapproval`,
`connectionpolicy`, `loopbackproxy`, `runtimepersistence`,
`go run ./cmd/repositorycheck`, `go mod tidy -diff`, `go mod verify`,
`git diff --check`, clean tree.

## Completion statement

> Answering "allow this host" writes exactly that rule in the same commit as
> the answer, and the next connection to that host is decided without asking.
