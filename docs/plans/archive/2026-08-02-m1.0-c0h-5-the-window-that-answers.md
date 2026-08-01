# M1.0-C0h-5 The Window That Answers a Connection

Status: active
Created: 2026-08-02
Implementation baseline: `f21b81d`
Branch: `m1/root-leaf-foundation`
Predecessor: `docs/plans/archive/2026-08-02-m1.0-c0h-4-remembering-an-answer.md`
Defers: `docs/plans/deferred/2026-08-01-m1.0-c-macos-trust-observation.md`

## Objective

`ask` blocks a connection on a person, and an answer can be remembered as a
rule. Neither is reachable from the window. The ApprovalCenter still describes
every question as a tool call, offers two choices it hard-coded, and reads
fields the runtime stopped sending. A question about a connection arrives with
no subject and no way to remember the answer.

This slice makes the window able to answer the questions the runtime actually
asks. It is the last thing between the product and the shipped default that
design 06 requires.

## Read-only design authority

- `docs/design/03-ui.md` §3.2, the ApprovalCenter;
- `docs/design/04-ux.md` §4.1 and §4.6;
- `docs/design/06-security.md` §4.1.

## Required invariants

1. The window offers exactly the choices the runtime declared, in the order it
   declared them. A hard-coded choice can offer something the runtime will
   refuse, or hide something it allows.
2. Every choice says what it does before it is taken, including whether it
   will be remembered.
3. A question names its subject in the terms it is about: a host and port for
   a connection, tool names for a tool call.
4. A merged question says how many callers are waiting on it, so one prompt is
   visibly one answer for all of them.
5. Answering is revision-checked, and a stale answer is reported rather than
   retried into a different question.

## Non-goals

- rule editing in the window, which has an API but no screen yet;
- the connection and egress views.

## Bottom-up implementation

- [x] Bring the approval types level with what the runtime sends.
- [x] Render the declared choices, with their own copy, instead of two fixed
      buttons.
- [x] Show the subject and the waiting count.
- [x] Report a stale or conflicting answer as itself.
- [x] Prove it against the runtime's own shapes rather than hand-written ones.

## Gates

`gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`,
`go test -race -count=1 ./...`, `go run ./cmd/repositorycheck`,
`go mod tidy -diff`, `go mod verify`, `pnpm --dir ui/desktop run check`,
`git diff --check`, clean tree.

## The flip

The shipped default is now `ask`, and nothing is allowed in advance. The five
invariants above are what made that defensible: the window shows a waiting
question within its polling interval, names the connection, offers the
choices the runtime declared, and can remember an answer, and the decision
budget is five minutes rather than seconds.

Two consequences are deliberate and visible in the tests. An unattended run
reaches nothing until somebody says what it may reach, which is what
INV-UNATTENDED-NO-IMPLICIT-ALLOW asks for. And no model host is allowed in
advance: what an agent may reach is a decision about that installation, and
shipping an allow list would be this product making it for everyone.

Migration 19 withdraws the placeholder rule by the exact name it shipped
under. Rules a person wrote are untouched.

## Completion statement

> A person can see a connection question in the window, understand what each
> answer will do, and remember an answer, using only the choices the runtime
> declared.
