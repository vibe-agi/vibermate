# M1.0-C0i What Went Out, Visible

Status: active
Created: 2026-08-02
Implementation baseline: `98ed776`
Branch: `m1/root-leaf-foundation`
Predecessor: `docs/plans/archive/2026-08-02-m1.0-c0h-5-the-window-that-answers.md`
Defers: `docs/plans/deferred/2026-08-01-m1.0-c-macos-trust-observation.md`

## Objective

Design 06 §4.1 promises that vibermate can audit where an agent connected and
how much it sent without decrypting anything. The runtime records all of it.
Nobody can see any of it.

`GET /api/v1/egress-attempts` returns one empty object per attempt: the
attempt's fields are unexported and it has no wire contract, so the endpoint
that answers "what went out" answers nothing at all. And the window has no
screen for either connections or outbound attempts, so even the records that
do serialize are invisible.

## Read-only design authority

- `docs/design/06-security.md` §4.1, including what a blind-tunnel record may
  not contain;
- `docs/design/15-local-control-api.md` §; the connection and egress reads;
- `docs/design/03-ui.md`.

## Required invariants

1. An outbound attempt serializes as what it is. Every fact the record holds
   reaches the reader, and a fact it does not hold is absent rather than
   present and empty.
2. A blind record stays blind. No URL path, header, or body byte appears in
   any view, and no view invents a field the record does not carry.
3. The window renders the runtime's own shapes, proven against generated
   samples rather than hand-typed ones.
4. A view distinguishes what was decrypted from what was forwarded blind, and
   an allowed connection from a refused one, because that distinction is the
   whole point of the record.
5. Nothing here is a decision surface. These screens read.

## Non-goals

- filtering and search beyond what the API already accepts;
- the single-connection detail route, which needs a screen concept first;
- rule editing in the window.

## Bottom-up implementation

- [ ] Give the outbound attempt an explicit wire contract, and prove the
      endpoint carries its fields.
- [ ] Generate samples for a connection record and an outbound attempt.
- [ ] Show connections: source, destination, decision, decryption, bytes.
- [ ] Show outbound attempts: purpose, target, outcome, bytes.
- [ ] Prove no path, header, or body reaches either view.

## Gates

`gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`,
`go test -race -count=1 ./...`, `go run ./cmd/repositorycheck`,
`go mod tidy -diff`, `go mod verify`, `pnpm --dir ui/desktop run check`,
`git diff --check`, clean tree.

## Completion statement

> A person can see which connections were made, whether each was decrypted or
> forwarded blind, and what went out on them, without any of it carrying a
> path, header, or body.
