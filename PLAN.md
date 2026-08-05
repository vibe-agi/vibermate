# Reversible Access Lifecycle Closure

Status: in_progress
Created: 2026-08-05
Implementation baseline: `ce80b657a37afc240861ce27b2271cfda35bc5da`
Design authority: `vibermate-design` at `e856138a0e04761120319ec76f21204a92c0e119`

## Goal

Close the already-designed reversible Access lifecycle through the real
SQLite, projection, Control API, Desktop UI, and Activity paths. A person must
be able to stop new traffic without deleting configuration or history, then
restore only future admissions through one typed CAS transition.

## Product acceptance

1. `PATCH /api/v1/accesses/{accessId}` accepts only enabled-to-disabled and
   disabled-to-enabled transitions under aggregate-local `If-Match` and an
   idempotency key. The browser sends only the target status, never the full
   aggregate or a credential reference.
2. Disable commits the durable next revision and synchronously withdraws the
   active projection before success. New endpoint, leaf, route, and request
   admissions fail closed; already pinned immutable snapshots may finish.
3. Re-enable recompiles the durable aggregate and publishes its next immutable
   snapshot before success. It affects only subsequent admissions.
4. Failed pre-commit work does not change durable or active state. A known
   commit followed by projection failure is reported as unavailable and never
   shown as success.
5. Activity records distinct enabled and disabled management events without
   prompt, header, credential, or aggregate payload data.
6. Desktop presents one compact reversible control, one concise disable impact
   confirmation, and equivalent English and Simplified Chinese behavior.
7. Integration and race evidence covers CAS conflict, withdrawal, re-enable,
   restart recovery, old-snapshot completion, and shutdown boundaries.

## Explicitly deferred

- Safe deletion. It needs one authority that proves disabled, no pinned
  request, drained runtime work, and no workspace or other configuration
  reference while retaining historical Activity identity.
- Renaming and arbitrary aggregate editing through this PATCH route.
- Packaged original-route acceptance, Keychain, system Root installation,
  Server/LAN mode, plugins, Language Bridge, transparent application capture,
  and Release readiness.

## Completion statement

This statement will be filled only after the implementation and evidence gates
are complete.
