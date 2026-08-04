# ManualCapture Shared Admission Foundation

Status: complete
Created: 2026-08-04
Implementation baseline: `b90aa86`
Design authority: `vibermate-design` ADR-0019 at `ff822a58cf8ee8c08ee840f9e70676ec08a856c5`

## Goal

Compose the durable ManualCapture authority into ProductRuntime and the one
existing proxy admission boundary. CaptureRun and ManualCapture credentials
must share the listener and pipeline while remaining disjoint capability
namespaces. Neither credential may carry route, Access, Profile, account,
model, plugin, machine, or workspace selection.

This slice stops below HTTP control routes, CLI rendering, and UI. Those
surfaces must consume the runtime-owned controller and shared grant issuer
after this composition is frozen.

## Invariants

1. CaptureRun and ManualCapture proxy credentials have explicit, disjoint
   type tags plus 256 bits of random capability material. A dispatcher chooses
   an authority only from that closed type tag and never probes multiple
   credential stores.
2. The fixed proxy username remains a protocol label only. No credential field
   contains business routing or attribution metadata.
3. ProductRuntime owns one ManualCapture manager built from the same SQLite
   store, clock, and security random source as the existing capture runtime.
4. Startup recovery completes before the proxy is built or any Host can
   publish discovery/readiness.
5. The shared admission authorizer maps a valid credential to exactly one
   immutable route-neutral CaptureAdmission before policy, DNS, dial,
   certificate issuance, endpoint lookup, or body read.
6. Manual admission is configured evidence only. It cannot assert adapter,
   process, machine, workspace, Access, Profile, or route evidence.
7. Shutdown closes proxy admission first, then both capture admissions, drains
   in-flight work, and preserves active ManualCaptures while revoking active
   CaptureRuns according to their distinct lifecycle contracts.
8. ProductRuntime exposes the ManualCapture controller for a later
   authenticated Host adapter, but no package may bypass the shared issuer or
   pass a caller-declared owner into a data-plane request.

## Deliverables

- disjoint managed/manual proxy credential shapes and redaction tests;
- one closed composite CaptureAdmission authorizer;
- ManualCapture production builder, startup recovery, Runtime ownership,
  cleanup ordering, and controller accessor;
- integration tests proving both credential kinds reach the same proxy and
  preserve their distinct evidence;
- structural and full repository validation plus exact implementation docs.

## Explicitly out of scope

- ManualCapture HTTP/OpenAPI routes, CLI/TUI output, or Desktop UI;
- verification projection from ConnectionEvent/TLS/Exchange evidence;
- remote enrollment, ProxyClientBinding quota, or Server composition;
- route/Profile/Access changes;
- packaged Preview or Release evidence.

## Completion statement

When complete, the repository may claim only:

> ProductRuntime owns both durable CaptureRun and ManualCapture authorities,
> and one listener authenticates their disjoint opaque proxy credentials into
> the same route-neutral CaptureAdmission and downstream pipeline.
> ManualCapture still has no product control surface and the result is not
> Preview or Release evidence.

## Frozen result

- `run_…` and `manual_…` are disjoint 256-bit bearer namespaces; their tag
  selects one authentication authority and carries no business metadata;
- ProductRuntime restores both durable authorities before proxy construction,
  then exposes one route-neutral admission boundary to the listener;
- a real ManualCapture credential traverses the production proxy, records
  configured-only attribution and observation, and cannot claim managed-run
  machine, workspace, process, adapter, Access, Profile, or route evidence;
- shutdown closes proxy admission first, drains both capture authorities, and
  preserves ManualCapture lifetime while revoking active CaptureRuns;
- the unreleased database remains one complete baseline file with the stable
  `vibermate-runtime` identity; this slice added no compatibility chain or
  product schema history;
- full ordinary and race tests, vet, module checks, structural checks,
  generated-source checks, native/cross builds, Desktop checks, and source
  hygiene gates pass;
- no ManualCapture HTTP/OpenAPI route, CLI/TUI command, or Desktop UI exists,
  so the capability is not yet usable by a person.
