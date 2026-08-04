# Remote Enrollment Authority Foundation

Status: in_progress
Created: 2026-08-04
Implementation baseline: `06bda4fca779d18ab3e20793926a4e8e6cb3ab86`
Design authority: `vibermate-design` ADR-0019 at `6fd70d49fc563e6ca8a95e35e6806ec80d0fe922`

## Goal

Establish the durable Core authority behind remote `vibermate login`: an
administrator-scoped ProxyClientBinding can mint one expiring, one-time
enrollment; consuming it atomically creates one MachineRegistration and one
digest-only enrolled ControlPrincipal credential. Every later authentication
rechecks the active principal, machine and binding rather than trusting network
location, MachineID, or a previously decoded claim.

## Invariants

1. Enrollment, enrolled-control and proxy-grant credentials are disjoint typed
   namespaces. None can authenticate as another.
2. Raw enrollment and control credentials are returned once and never stored;
   SQLite stores only domain-separated SHA-256 digests.
3. Enrollment completion consumes exactly one active, unexpired enrollment and
   creates the machine and principal in the same SQLite transaction. Concurrent
   consumers have at most one winner.
4. An enrollment is bound to the exact active ProxyClientBinding generation
   observed when it was created. Binding change or revocation makes it fail
   closed.
5. Authentication rereads durable state and succeeds only while the principal,
   MachineRegistration and ProxyClientBinding are all active and still bound to
   the same internal generation.
6. MachineID and display name are public attribution metadata, never
   credentials, proxy selectors, routes, Access IDs, Profile IDs or model
   account selectors.
7. Allowed grant kinds come only from the durable binding/principal policy and
   are returned as the existing immutable `controlprincipal.Principal`.
8. Binding revocation immediately rejects new enrollment completion and new
   control authentication without rotating provider or ManualCapture secrets.
9. The unreleased database remains one clean baseline migration; no synthetic
   compatibility migration or product-facing numeric revision is introduced.
10. Cancellation, shutdown admission and ambiguous SQLite commit outcomes are
    bounded and fail closed; a reconciled exact durable commit may still return
    its one-time credential.

## Deliverables

- typed ProxyClientBinding, MachineRegistration, ClientEnrollmentGrant and
  enrolled-principal records with immutable public views;
- a lifecycle-owned authority for binding creation/revocation, one-time
  enrollment issuance/completion and durable control authentication;
- SQLite tables and one transactional repository behind the existing operation
  admission/drain fence;
- concurrency, expiry, revocation, restart, cancellation, digest-only storage,
  namespace separation and commit-reconciliation tests;
- RuntimeStore exposure for the next Server composition slice, without wiring
  a listener or declaring Server readiness;
- synchronized README and module map with exact proof boundaries.

## Explicitly out of scope

- Server HTTP/TLS listener, Web admin session, OpenAPI handlers and UI;
- `vibermate login`, ConnectionProfile files or client-side credential storage;
- public Root delivery and remote proxy address delivery;
- remote CaptureRun or ManualCapture issuance and quota enforcement;
- binding policy editing, principal credential rotation or machine re-enrollment;
- Access/Profile/route/provider/plugin changes;
- packaged Preview or Release evidence.

## Completion statement

> VibeMate has a durable, one-time remote enrollment authority that can produce
> and authenticate a binding-scoped enrolled ControlPrincipal while rechecking
> the active binding and machine on every request. No Server listener, login
> command, remote proxy, Root delivery or Preview-ready product is claimed.
