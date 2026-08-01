# M1.0-C0j Streaming, End to End

Status: active
Created: 2026-08-02
Implementation baseline: `b61e1f6`
Branch: `m1/root-leaf-foundation`
Predecessor: `docs/plans/archive/2026-08-02-m1.0-c0i-what-went-out-visible.md`
Defers: `docs/plans/deferred/2026-08-01-m1.0-c-macos-trust-observation.md`

## Objective

Two live runs now reach a real model and come back, but both ask for a whole
answer at once. Every agent client streams. A streamed answer is a different
path through the whole product: a different response mode, a different set of
wire events, incremental usage, and a commit ledger that has to stay honest
about what the client actually received when a stream ends early.

Running one real streamed request is the fastest way to find what the
non-streaming runs could not.

## Read-only design authority

- `docs/design/02-architecture.md` §4.3 and §10 on the CommitLedger's two axes;
- `docs/design/07-protocol-translation.md`;
- `docs/design/10-client-compatibility.md`.

## Required invariants

1. A client that asked for a stream gets a stream, in its own dialect's event
   grammar, not a whole answer relabelled.
2. Usage reaches the client. A streamed answer that drops the token counts
   makes every downstream cost view wrong.
3. The two ledger axes stay separate: what the upstream was charged for is not
   the same fact as what the client understood.
4. A stream that ends early is recorded as what it was, and its outbound
   attempt still reaches a terminal.
5. Nothing in the streamed path carries a credential or a body byte into a
   record.

## Non-goals

- the Responses WebSocket path;
- multi-attempt failover mid-stream;
- the packaged acceptance run with a real agent client.

## Bottom-up implementation

- [ ] Drive one real streamed request through the Exchange and read the events.
- [ ] Drive the same request through the proxy as a client would.
- [ ] Prove usage, the ledger axes, and the outbound terminal.
- [ ] Fix what those runs find.

## Gates

`gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`,
`go test -race -count=1 ./...`, `go run ./cmd/repositorycheck`,
`go mod tidy -diff`, `go mod verify`, `pnpm --dir ui/desktop run check`,
`git diff --check`, clean tree.

## Completion statement

> A real client streaming through vibermate receives its own dialect's events,
> with usage, and the records afterwards say what actually happened.
