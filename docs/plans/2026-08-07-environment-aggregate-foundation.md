# Environment-first Production Vertical

Status: implemented in the current working tree; final repository and packaged
evidence gates remain in progress. Architecture authority is ADR-0021 in the
design repository.

## Implemented scope

- Direct replacement of the retired Access/Profile runtime with typed
  Environment, ClientEndpoint, ProtocolPlan, UpstreamRoute and account-policy
  authority.
- Core-owned `system_transparent`, which blind-forwards and audits without
  MITM, Root delivery, credential rewrite, plugins or semantic processing.
- Private Environment draft, bounded impact preview, durable CAS publish and
  atomic immutable snapshot publication.
- Durable typed managed/manual Capture identities and Capture → Environment
  assignment with launch-authority freezing.
- Hot switch, affected-connection reconnect, and fail-closed
  `restart_required` when authority would widen.
- Environment-first local CA admission, proxy, Exchange, provider transport,
  Activity, approvals, Offline Hold and SQLite recovery.
- `vibermate run [--env ID] -- command` plus Environment-bound Manual Capture.
- Desktop Environment/Capture/Request/policy surfaces and responsive bilingual
  Playwright coverage.
- Environment-first packaged acceptance producer/verifier contracts.

## Completion evidence required before freezing

- `make check`;
- non-cached full Go tests and race tests;
- `go vet`, tidy drift and module verification;
- Desktop component/build and Playwright checks;
- structural checker and forbidden-vocabulary scan;
- clean diff and generated-artifact checks;
- one fresh deterministic packaged run from a clean candidate before any
  Preview claim.

## Explicitly deferred

- Server Host and remote enrollment;
- account connectors and account-management UI;
- plugin execution, QualityRun and Language Bridge;
- system trust mutation and native secret storage;
- signed distribution, notarization and installer evidence.

This plan does not introduce compatibility aliases, dual writes, old schema
readers or versioned product names. The product has not shipped, so the only
supported model is the Environment-first model above.
