# Observable Original Route Closure Evidence

Date: 2026-08-05
Implementation candidate: `efb62989e584fb7810f18af83dc6b3bd35dfa3b1`
Design authority: `vibermate-design@70240511bae006a58c526c93c677a0d4eca13534`
Source state during final gates: clean
Platform: macOS 26.5.2 (25F84), arm64
Go toolchain: 1.25.12

## Implemented boundary

- Every Access contains one Core-derived `original_passthrough` Profile and
  exact ClientOrigin target without an account, SecretRef, or AuthDriver.
- A new current-login Access requires no provider origin, model, route name, or
  API key. Zero or more managed Profiles may be added later.
- Original and managed Profiles share the default RouteSet as explicit
  workspace choices; the original Profile is never an automatic fallback.
- The original route preserves the client-owned request envelope and
  authentication only for the exact frozen origin. It uses the same protocol,
  streaming response path, Offline Hold lease, Activity, and EgressAttempt
  lifecycle as the managed path.
- The default upstream presentation is `follow-client` for both H1 and H2.
  Named product presentation requires an explicit user selection; provider,
  model, account, dialect, and client classification cannot select it.
- Route changes across credential sources use a guarded SQLite CAS. The
  repository tests prove that an active CaptureRun rejects the switch and that
  finishing the run permits it. CaptureRun creation is ordered before
  route-aware launch authority resolution.
- Managed and client-passthrough transports reject all upstream 3xx responses,
  close the body, record `redirect_denied`, and expose no Location value.

## User-facing evidence

The Access form opens in current-login mode and the first valid save needs only
the tool, exact client origin, and Access name. Managed-provider fields remain
behind the explicit managed mode. Workspace cards expose current client login
alongside configured VibeMate accounts. While tools are running, choices that
would change the credential source are disabled with a stop-select-restart
instruction; managed-to-managed selection remains available for later
requests.

The browser suite exercised 24 scenarios, including three terminals grouped by
stable workspace identity, route switching, both locales, narrow layouts,
keyboard navigation, and manual application capture. The React suite passed
386 tests.

## Final gates

- `make check`;
- uncached `go test -count=1 ./...`;
- uncached `go test -race -count=1 ./...`;
- `go vet ./...`, `go mod tidy -diff`, and `go mod verify`;
- repository structure and immutable workflow-pin checks;
- native-secret builds plus Windows and Linux cross-builds;
- React/TypeScript build, 386 tests, and Playwright 24/24;
- Rust format, clippy, and 31 tests;
- fixed `govulncheck@v1.6.0`: zero reachable vulnerabilities;
- production UI audit: no known vulnerability;
- Rust audit: the existing 16 allowed unmaintained warnings, with
  RUSTSEC-2024-0429 excluded only after the repository's reachability guard;
- forbidden private-project term and private-origin scan: no match.

## What this evidence does not prove

- Raw ClientHello passthrough, exact JA3/JA4 parity, or observed H2
  SETTINGS/header ordering. MITM creates a fresh upstream TLS connection.
- Original-route `count_tokens`; payload-bearing auxiliary requests still
  receive the local typed 422 and never use the original-origin arm.
- A packaged fixed-client or real-provider run through the original route.
- Arbitrary client versions, operating-system Root trust, production secret
  protection, Server mode, plugins, Language Bridge, Preview, or Release
  readiness.
