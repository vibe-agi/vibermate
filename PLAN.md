# Observable Original Route Closure

Status: completed
Created: 2026-08-05
Implementation baseline: `665390c21b40a9ece552687fa556c542da6e12a8`
Design authority: `vibermate-design` at `70240511bae006a58c526c93c677a0d4eca13534`
Implementation candidate: `efb62989e584fb7810f18af83dc6b3bd35dfa3b1`
Evidence archive: `docs/evidence/2026-08-05-observable-original-route-closure.md`

## Goal

Make a newly created Access useful before the user configures a VibeMate-managed
provider account. Core owns one invisible original route that preserves the
tool's current login and exact origin while keeping the same immutable Access,
workspace routing, egress admission, Hold, and audit authorities as managed
routes.

## Product acceptance

1. Core derives exactly one account-free `original_passthrough` Profile and
   exact-origin target for every Access. Callers cannot submit, retarget,
   delete, or use the system Profile as automatic fallback.
2. A new Access can contain zero managed Profiles, accounts, credentials, or
   provider routes. Its default route is the Core-owned original Profile.
3. Original semantic traffic preserves method, path, query, bounded end-to-end
   headers, body, client authentication, dialect, model, streaming, and the
   downstream H1/H2 protocol to the exact ClientOrigin. It runs no model map,
   AuthDriver, plugin, Language Bridge, or semantic retry.
4. Upstream presentation defaults to `follow-client`. Named product
   presentation is an explicit advanced choice and cannot be inferred from the
   provider, model, account, dialect, or client classification. H1/H2 is not a
   user setting.
5. Original and managed Profiles can coexist as explicit workspace choices.
   Switching between client-owned and VibeMate-managed authentication fails
   with a restart-required conflict while the workspace has an active
   CaptureRun. The guard and route CAS share one SQLite write transaction.
6. CaptureRun creation precedes route-aware bootstrap-authority resolution, so
   a launch racing a cross-auth route update has one deterministic winner.
7. The Desktop UI recommends current login, keeps managed provider setup
   advanced, explains restart-required switches, and provides the same English
   and Simplified Chinese contract.
8. Both credential modes reject upstream redirects without returning a
   Location value to the client.

## Explicitly deferred

- Anthropic `count_tokens` forwarding on the original route. It remains a
  typed local 422 until a separate ClientOperationRun/`profile_operation`
  path exists; the generic original-origin arm cannot carry its payload.
- Packaged credentialed proof for the original route.
- Keychain, system Root installation, Server/LAN mode, plugins, Language
  Bridge, transparent application capture, and Release readiness.

## Completion statement

> A user can create an Access without choosing a provider or saving a second
> credential, run a supported CLI with its existing login, observe its supported
> semantic traffic, and later select a managed route per workspace. The default
> upstream wire policy follows the current client; product emulation is
> opt-in. Payload-bearing auxiliary operations and packaged original-route
> acceptance remain explicitly unclaimed.
