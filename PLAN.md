# Desktop Manual App Capture

Status: complete
Created: 2026-08-04
Implementation baseline: `a51a4576acdb7a6770b37172d8e808a9bf96b00f`
Implementation candidate: `cfb3ecbff4bc8aab0db8e6cceaedc53b51152ba7`
Design authority: `vibermate-design` ADR-0019 at `6fd70d49fc563e6ca8a95e35e6806ec80d0fe922`

## Goal

Turn the existing authenticated ManualCapture control contract into one honest
Desktop task for applications started outside `vibermate run`: review the
local proxy and Root delivery, create one route-neutral login, save its
password once, and then observe, rotate, or revoke it without seeing internal
version counters or pretending that traffic observation proves app identity.

## Invariants

1. The Desktop sends only display name, application class, lifetime, and the
   opaque review confirmation. It cannot submit Access, Profile, route,
   account, model, plugin, owner, machine, workspace, or version coordinates.
2. Create and rotate use the existing shared authenticated handler and are
   never retried. A lost one-time response remains an explicit ambiguous
   outcome.
3. The raw proxy password exists only in the immediate component-local
   delivery state. It never enters React Query, Web Storage, a log, an error,
   the durable list projection, or a later detail response.
4. Once the person dismisses the delivery ticket, the Desktop cannot display
   the password again. Recovery means explicit credential rotation.
5. Product concurrency is an opaque `ETag`/`If-Match` contract. No numeric
   runtime, schema, aggregate, Root, credential, catalog, or plan counter is
   shown or accepted by this task.
6. `waiting_for_traffic` and `observed` describe only whether the generated
   login reached VibeMate. They do not identify the application or choose an
   upstream route.
7. All visible copy comes from the canonical English and Simplified Chinese
   catalogs. Desktop and narrow layouts remain usable without horizontal
   overflow.

## Deliverables

- strict Desktop wire validation for ManualCapture context, list, detail,
  create, rotate and revoke, including no-store and opaque state headers;
- review-first creation form and explicit route-neutral boundary copy;
- one-time proxy URL and shell setup delivery with explicit copy actions;
- secret-free observation cards with bounded traffic state, expiry, rotate and
  revoke;
- deterministic preview adapter, unit tests, Playwright interaction tests, and
  desktop/mobile visual inspection in both locales;
- synchronized implementation README and module map.

## Explicitly out of scope

- remote enrollment, Server listener and shared-team administration;
- application identity verification beyond current traffic observation;
- automatic application proxy configuration or OS network extension;
- Keychain or system Root trust mutation;
- Access/Profile/route/provider changes;
- packaged Preview or Release evidence.

## Completion statement

> The authenticated Desktop App can review, create, observe, rotate and revoke
> a route-neutral ManualCapture, while delivering each proxy password exactly
> once and retaining no secret in its query or browser-storage planes. This
> does not prove application identity, remote access, or Preview readiness.

## Frozen result

The implementation satisfies this Goal. The UI follows a three-state handoff:
review ticket, one-time credential-delivery ticket, and permanent secret-free
observation card. Both supported locales and the 390-pixel layout were
inspected through a real Chromium session. The product remains pre-Preview.
