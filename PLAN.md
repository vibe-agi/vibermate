# ManualCapture Authenticated Control and CLI

Status: active
Created: 2026-08-04
Implementation baseline: `81c4853`
Design authority: `vibermate-design` ADR-0019 at `6fd70d49fc563e6ca8a95e35e6806ec80d0fe922`

## Goal

Give the already-composed durable ManualCapture authority its first real,
authenticated product surface. The local CLI and Desktop App must call one
shared Host adapter with an explicitly authenticated `ControlPrincipal`; the
request body must never declare owner, route, Access, Profile, account, model,
plugin, machine, workspace, runtime generation, Root epoch, or internal
credential epoch.

This is an unreleased clean contract. It retains no launcher-era route aliases,
no numeric product-facing state versions, and no compatibility schema chain.

## Invariants

1. The Host authenticates first and passes an immutable principal explicitly
   to the ManualCapture handler. The handler never reconstructs ownership from
   the body, proxy credential, display label, or browser metadata.
2. Creation is a two-step UX contract: read a review context, show listener,
   Root DER identity and lifetime, then return one opaque confirmation token.
   Any bound fact changing makes the token fail closed without exposing an
   internal runtime or Root epoch.
3. Create and rotate return the raw proxy password exactly once with
   `Cache-Control: no-store`. List and detail never return it.
4. Product views contain stable identity, lifecycle and observation only.
   Internal credential epochs remain Core-only; mutations use opaque
   `ETag`/`If-Match` and stale state returns a typed conflict.
5. The local CLI reads only the owner-private discovery record. It does not
   accept a control origin, token, route or account from argv or environment.
6. An interactive create defaults to no. Non-terminal use requires explicit
   `--yes`; secret output stays on stdout while review and diagnostics stay on
   stderr. A lost mutation response is ambiguous and is never retried.
7. Desktop App and local CLI use the same `ManualHandler`, manager and grant
   issuer. Their authentication transports remain disjoint and neither can
   replay the other's capability.
8. The generated proxy credential remains route-neutral and enters the one
   existing proxy listener and `CaptureAdmission` pipeline.

## Deliverables

- shared ManualCapture HTTP adapter for context, create, list, detail, rotate
  and revoke;
- opaque confirmation token and ETag contracts with negative tests;
- bounded local CLI client and `vibermate capture create` human/shell output;
- Desktop control routing through the same handler and Desktop principal;
- owner isolation, stale-context, stale-ETag, one-time-secret, no-retry,
  lifecycle and race tests;
- exact implementation docs and repository structural checks.

## Explicitly out of scope

- Desktop creation wizard and verification ladder UI;
- remote enrollment, Server listener and ProxyClientBinding quota;
- full CLI list/show/verify/rotate/revoke and additional output formats;
- Keychain or system Root trust mutation;
- Access/Profile/route/provider changes;
- packaged Preview or Release evidence.

## Completion statement

When complete, the repository may claim only:

> An authenticated local CLI or Desktop App can create and manage the same
> durable, route-neutral ManualCapture through one shared control adapter, and
> `vibermate capture create` can deliver a one-time standard proxy credential.
> The Desktop wizard, remote enrollment, verification projection and packaged
> evidence remain incomplete, so the product is not Preview or Release ready.
