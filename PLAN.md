# Safe Access Retirement Closure

Status: implementation_complete_evidence_pending
Created: 2026-08-05
Implementation baseline: `e3f5ffae88957692d376390d3749f707c80b003f`

## Goal

Close permanent Access retirement through the existing SQLite, immutable
projection, request data plane, SecretStore, authenticated Desktop control
plane, and compact UI. The operation must be explicit, revision-bound,
retryable, and safe under concurrent requests; it must not turn a missing UI
row into permission to reuse the same durable identity.

## Invariants

1. Only a disabled Access can be retired. Deletion never acts as an implicit
   disable operation.
2. Preview is evidence, not authority. Execution re-reads the aggregate,
   workspace revisions, active CaptureRun IDs, ProxyClientBinding Profile
   references, and cross-Access secret ownership and compares one exact impact
   token.
3. A per-Access request-admission cut closes before drain. Every MITM request
   holds its Access-use lease through its complete downstream response.
4. Active CaptureRuns block deletion. Durable workspace assignments require a
   separate explicit confirmation and are removed in the SQLite transaction.
   A ProxyClientBinding policy that names any owned Profile blocks deletion;
   it is never rewritten or swept implicitly.
5. A secret referenced by another Access is preserved. Only exclusive
   references are passed to the host SecretStore, whose missing result is
   idempotent and whose errors never contain the reference.
6. Durable deletion writes an immutable tombstone before removing the Access
   aggregate in the same transaction. The `AccessID` cannot be created again.
7. A lost success response can be retried without repeating secret cleanup or
   recording another deletion Activity event. Commit ambiguity is reconciled
   from SQLite; an unresolvable result fails closed.
8. Activity, ConnectionEvent, and EgressAttempt history is retained. Deletion
   never claims to erase historical evidence or force-cancel an Agent process.

## Product acceptance

- The Desktop shows `Delete` only for a disabled Access.
- The confirmation is compact and names only bounded counts: workspace
  assignments, active captures, remote-client policy references, exclusive
  credentials, and preserved shared credentials.
- Workspace retirement requires a checkbox; active captures leave the action
  disabled and expose Refresh.
- A successful deletion removes the Access from the directory. A later create
  using the retired identity returns `access_retired`.
- Activity and traffic views remain ten-row paginated tables and stay operable
  with at least eight captured Agents and dozens of connection/attempt rows.

## Required evidence

- Request admission close/drain/release through the real proxy handler.
- SQLite impact CAS, workspace retirement, ProxyClientBinding reference fence,
  tombstone reopen, identity non-reuse, commit-then-error and rollback-then-error
  reconciliation.
- Exclusive/shared secret classification, cleanup failure/retry, and changed
  ownership between preview and execution.
- Authenticated control preview/DELETE, capability separation, idempotency,
  single Activity event, strict closed response validation, and UI workflow.
- Full Go, race-touched packages, vet, structural/cross-platform builds,
  TypeScript, React, and Playwright checks.

## Explicitly deferred

- Force-terminating running CaptureRuns or requests.
- Erasing retained Activity, ConnectionEvent, or EgressAttempt history.
- Native release secret storage, Keychain, system Root installation,
  Server/LAN composition, plugins, Language Bridge, transparent application
  capture, and Preview/Release readiness.
