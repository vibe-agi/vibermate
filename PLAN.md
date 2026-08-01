# M1.0-C0k A Failure You Can Diagnose

Status: active
Created: 2026-08-02
Implementation baseline: `7d18e6e`
Branch: `m1/root-leaf-foundation`
Predecessor: `docs/plans/archive/2026-08-02-m1.0-c0j-streaming-end-to-end.md`
Defers: `docs/plans/deferred/2026-08-01-m1.0-c-macos-trust-observation.md`

## Objective

Both defects that stopped a real client were found by patching the runtime to
print an error. Nothing a user could see said more than
`invalid_exchange_request`, and nothing stored afterwards said more either.
"My client cannot connect" is the report this product will receive most often,
and today the only way to answer it is to rebuild the runtime.

A failure has to carry enough structure to be diagnosed and no content at all.

## Read-only design authority

- `docs/design/06-security.md` §4.1 on what a record may not contain;
- `docs/design/07-protocol-translation.md` §2.3 and §3.2;
- `docs/design/15-local-control-api.md`.

## Required invariants

1. A failure names where it happened in the request's structure, and never
   what was there. A path is field names and indices; a value is content.
2. The reason stays one stable code. Diagnostic facts travel as their own
   typed fields rather than being concatenated into it.
3. A field this dialect does not model is still nameable. A closed enum could
   not name `defer_loading`, which is exactly why the failure was opaque.
4. Nothing added here can carry a credential, a body byte, or provider text.
5. What is stored is what is shown: a person reads the same facts in the
   window that the record holds.

## Non-goals

- provider-supplied error text, which is not ours to render;
- a general request inspector;
- retry or repair suggestions.

## Bottom-up implementation

- [ ] Carry the protocol failure path on the Exchange failure.
- [ ] Give the Activity record typed diagnostic fields instead of one
      concatenated reason.
- [ ] Show them through the control API and in the window.
- [ ] Prove no value, credential, or provider text can reach any of them.

## Gates

`gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`,
`go test -race -count=1 ./...`, `go run ./cmd/repositorycheck`,
`go mod tidy -diff`, `go mod verify`, `pnpm --dir ui/desktop run check`,
`git diff --check`, clean tree.

## Completion statement

> A failed request says which reason, which field, and where in the request's
> shape, and a person can read all three without rebuilding anything.
