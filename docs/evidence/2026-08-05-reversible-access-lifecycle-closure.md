# Reversible Access Lifecycle Closure Evidence

Date: 2026-08-05
Implementation candidate: `b12b6a4fa2d759a0c522f4c934656ee2fe76953f`
Desktop profile: development
Target: macOS arm64

## What was exercised

The status-only Desktop control route was exercised through the real SQLite
Access repository, `access.Manager`, immutable projection, authenticated
Control API, React control client, and Desktop workflow.

The integration path committed revision 1 as enabled, rejected the legacy
full-aggregate attempt to apply a disabled Access, disabled through
`PATCH /api/v1/accesses/{accessId}` at revision 2, and proved that the active
resolver then returned `access_not_configured`. The same durable aggregate was
read back as disabled and re-enabled at revision 3. The republished plan
returned its frozen plan hash and became visible only to later resolution.

Existing Access manager tests independently cover a disabled aggregate across
close/reopen recovery, a retained old immutable snapshot handle, concurrent
CAS ownership, projection failure after commit, cancellation, shutdown, and
race detection. The control route records distinct redacted
`access.disabled` and `access.enabled` Activity facts.

The browser workflow used the exact status-only request. It confirmed the
impact before disable, retained the configured route, rendered the inactive
state, and re-enabled the same Access. The control client contract asserts that
the request body is exactly `{ "status": "disabled" }` and contains no Profile
or secret material.

## UI review

The Desktop browser preview was visually inspected at 1280 by 820. The
lifecycle control stays beside the status badge, the confirmation is one
compact inline boundary, and the route action is the compact `+ Add` form. The
same suite exercised 390-pixel English and 920-pixel Simplified Chinese layout
boundaries, keyboard navigation, eight captured agents, and high-volume
Activity tables.

## Clean packaged startup

A development App was rebuilt from the clean implementation candidate. Its
embedded `vibermate.desktop-build/v2` manifest records:

- source revision `b12b6a4fa2d759a0c522f4c934656ee2fe76953f`;
- `dirty: false`;
- target `aarch64-apple-darwin`;
- development sidecars.

The resulting App started against the existing private development data
directory. Both the native process and packaged daemon remained live, and the
daemon published a fresh private loopback control descriptor. The startup did
not enter the previous local-runtime failure boundary. This is local
development-profile startup evidence, not a signed/notarized installed-App or
packaged lifecycle interaction claim.

## Gates

The following completed successfully from the clean candidate:

- `make check`;
- `go test -count=1 ./...`;
- `go vet ./...`;
- `go mod verify`;
- fixed `govulncheck@v1.6.0` with zero reachable vulnerabilities;
- ordinary and race tests for Access, Activity, runtime persistence, Desktop
  control, and ProductRuntime;
- 394 Desktop TypeScript tests;
- 26 Playwright workflows;
- 31 Rust/Tauri tests;
- development App bundle construction and embedded-manifest verification.

Node was 25.8.1 while the repository requests 22.23.1. The package manager
reported that mismatch; checks still passed. This run therefore does not replace
the pinned-toolchain release gate.

## What this does not prove

This evidence does not prove safe Access deletion, forced cancellation of work
already holding an immutable snapshot, Keychain protection, system Root trust,
Server/LAN composition, signed/notarized installation, provider success, or
Preview/Release readiness. A known durable commit followed by projection
failure remains an unavailable state that requires recovery; it is not reported
as a successful lifecycle change.
