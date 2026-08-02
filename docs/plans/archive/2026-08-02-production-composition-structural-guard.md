# Production Composition Structural Guard

Status: complete
Created: 2026-08-02
Implementation baseline: `6a88a00`
Implementation candidate: `1af205ca559adb6aac5cc05ceaf2ada7466a4b60`
Predecessor: `docs/plans/archive/2026-08-02-signed-client-identity-hardening.md`

## Initial finding

The Desktop production chain was factually unique:

```text
cmd/vibermated
  -> desktopdaemon.ProductionOptions / desktopdaemon.Run
  -> desktophost.Start
  -> productruntime.Start
  -> productionBuilders()
```

That was not yet a machine-enforced property. Nothing prevented a second
production caller, a test builder, a placeholder, or a development dependency
from entering the release path.

Historical packaged evidence was strong but stale: the deterministic 17/17 and
credentialed 25/25 reports were bound to
`c19cca4eb2842aa00d8e8fc17160b342a111f0b6`, not the then-current source.

## Scope

This slice added only a source-shape guard. It did not build an App, run packaged
acceptance, implement a report verifier, exercise a recognized client, or modify
the design repository.

## Enforced invariants

1. `cmd/vibermated` reaches the product only through
   `desktopdaemon.ProductionOptions` and `desktopdaemon.Run`.
2. The Desktop main package cannot import or call `desktophost` or
   `productruntime` directly.
3. `desktopdaemon.Run` calls `desktophost.Start` in the owning function body.
4. `desktophost.Start` calls `productruntime.Start` in the owning function body.
5. `productruntime.Start` selects `productionBuilders()` in the owning function
   body.
6. Every production caller is in a per-edge allowlist. A second Host or entrypoint
   must be added explicitly.
7. Import aliases cannot bypass a check; dot imports of guarded packages are
   rejected because they erase the qualifier the analysis relies on.
8. A function-value reference is not accepted as a call, and a dead decoy call
   elsewhere in the package cannot satisfy the owning-function obligation.

Every rule has known-good and injected-bad repositories exercised through the
public `repositorycheck.Check` entrypoint. The hardening mutations cover decoy
calls in all three owning packages, import aliases, function-value references,
second callers, and dot imports.

## Verification

The implementation candidate passed formatting, unit tests, touched-package race
tests, `go vet`, `repositorycheck`, release-tag builds including Windows/Linux
cross-builds, pinned frontend checks, Rust formatting/tests, and
`git diff --check`. The worktree was clean and was not pushed.

## Completion statement

> The source shape of the Desktop production composition is guarded by CI. This
> does not prove that a packaged artifact from the current commit runs.

The successor discovery is archived in
`docs/plans/archive/2026-08-02-packaged-deterministic-discovery.md`.
