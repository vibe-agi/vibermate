# Durable ManualCapture Authority Foundation

Status: complete
Created: 2026-08-04
Implementation baseline: `4a18508`
Design authority: `vibermate-design` ADR-0019 at `ff822a58cf8ee8c08ee840f9e70676ec08a856c5`

## Goal

Create one durable, route-neutral ManualCapture authority beneath future
Desktop and Server adapters. A capture can be created, observed through its
opaque proxy credential, rotated, revoked, expired, listed within its owner
scope, and recovered after restart without retaining a raw credential.

This slice does not expose the authority through HTTP, CLI, UI, or the proxy.
Those surfaces must consume this same authority in the next slice.

## Clean unreleased database baseline

VibeMate has not shipped a database format. The repository's accumulated
development migrations are therefore not a product compatibility contract.
This slice replaces them with one complete schema baseline at revision 1 and
rejects databases created from the pre-baseline development schema. No
best-effort compatibility aliases or migration paths are retained.

ManualCapture `CredentialRevision` remains. It is live security state, not
schema history: rotation must identify the credential that was closed, prevent
a stale control action, and keep evidence from an old password out of a new
grant.

## Invariants

1. SQLite stores a domain-separated SHA-256 credential digest, never a raw
   proxy password or secret-bearing URI.
2. `manual-capture/<capture-id>` is mechanically derived and is not a stored
   second authority.
3. A capture contains no Access, Profile, route, account, model, plugin,
   provider credential, process, adapter, machine, or workspace selection.
4. Ownership is either this local installation or one exact future
   ProxyClientBinding. Local control-principal rotation does not orphan a
   capture, and Server owners cannot read or mutate each other.
5. Create and rotate disclose one raw credential once. Rotate atomically
   replaces the digest and advances its credential revision; the old value is
   immediately unusable.
6. Revoke is idempotent only at the current credential revision. Stale
   revisions fail closed.
7. Temporary expiry, authorization observation, rotation, and revocation are
   SQLite transactions. Startup recovery expires elapsed captures and restores
   active until-revoked or unexpired captures without recovering plaintext.
8. Only an authenticated proxy operation can move observation to
   `observed`; create, copy, list, and get cannot.
9. Manager shutdown closes admission and drains owned work but does not revoke
   active ManualCaptures. Their declared lifetime or explicit control action
   owns validity.
10. Every repository operation uses the existing SQLite operation gate, so
    store shutdown cancels and drains it before closing the database.

## Deliverables

- typed immutable ManualCapture ID, owner, lifecycle, view, one-time grant,
  redacted credential, evidence, commands, repository, and manager;
- one clean complete SQLite schema baseline with a fixed identity;
- durable create/CAS rotate/idempotent revoke/authorize/get/list/recover;
- owner isolation, exact expiry, restart, credential redaction, concurrent CAS,
  lifecycle cancellation, race, full repository, and cross-build evidence;
- implementation map and README updated with exact proof and non-proof scope.

## Explicitly out of scope

- HTTP routes, OpenAPI, local discovery, CLI, UI, or Server Web adapters;
- parsing Proxy-Authorization or composing a shared CaptureAdmission;
- closing already-established proxy connections after rotate/revoke;
- verification projection from ConnectionEvent/TLS/Exchange evidence;
- ProxyClientBinding quota and remote enrollment;
- packaged Preview or Release evidence.

## Completion statement

When complete, the repository may claim only:

> VibeMate has one durable, owner-scoped ManualCapture authority whose opaque
> proxy credentials can be created, atomically rotated, revoked, expired,
> observed, and recovered without persisting plaintext. It is not yet exposed
> through a product control or data-plane surface, and it is not Preview or
> Release evidence.

## Frozen result

- the unreleased SQLite format is one complete revision-1 baseline with a
  fixed schema identity; pre-baseline development databases fail closed;
- ManualCapture create, rotate, revoke, expire, authorize, list, recover, and
  owner isolation use real SQLite through the shared operation gate;
- raw proxy credentials exist only in the one-time create/rotate return value;
  persistence and diagnostic formatting retain no plaintext;
- targeted and full ordinary/race tests, vet, module checks, generated-source
  checks, repository structural checks, native-tag builds, and cross-builds
  pass;
- no proxy parser, product composition, HTTP route, CLI, or UI consumes this
  authority yet.
