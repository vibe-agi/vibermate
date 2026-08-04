# Packaged Deterministic Freshness Gate

Status: in progress
Created: 2026-08-02
Implementation baseline: `1b1a1b5fe43e1e4d89243006b10ff9c67ef0ea28`
Predecessor: `docs/plans/archive/2026-08-02-packaged-deterministic-discovery.md`

## Scope coordinates

- Product construction scope: initial Desktop milestone (M0), covering one
  macOS arm64 vertical slice with one fixed verified client.
- Implementation Goal: packaged deterministic freshness gate.
- Evidence maturity: current-candidate production-wiring evidence for the
  architecture gate (G0), with fixed-client compatibility remaining a
  continuous version-regression obligation (G3).
- Release claim: contributes to `GATE-RUNTIME-ASSEMBLY` before Preview; it does
  not by itself close Preview, the public-preview recovery gate (G1), the
  stable-release platform gate (G2), signing, notarization, or Release gates.

## Current fact

A clean packaged deterministic run passed 17 of 17 checks at the historical
baseline. That private v5 report binds the full source revision, clean state,
fixed client, App members, build manifest, configuration digests, and pinned
toolchains.

The repository now emits the current v6 schema and contains a strict public
verifier and CLI that consume either current v6 or historical v5 reports with
an explicit expected schema, revision, and fixed client coordinates. Schema
selection also selects the exact frozen check set: v6 requires packaged
main-window cold restore, while v5 remains frozen at its original contract.
Current v6 additionally requires caller-owned source, App, acceptance-binary,
and client coordinates, an actual v2 build manifest, a clean matching Git
checkout, and independent artifact and configuration re-hashing. The historical
clean 17-of-17 report passes only under the explicit v5 contract, and the same
report fails when the expected revision changes.

The repository now contains a manual macOS arm64 packaged workflow. It runs
only on the protected `vibermate-acceptance` self-hosted runner, takes the
pre-provisioned Claude Code 2.1.220 path from the protected environment, never
downloads a client, and relies on the runtime compound catalog to reject any
artifact drift. It builds the selected SHA, runs deterministic-only acceptance,
and always invokes the strict verifier against that same SHA and client.

That workflow has not run for this dirty implementation worktree. No fresh
report exists for the eventual candidate, so the gate remains open.

## Adjacent Web UI progress (not gate completion)

The Desktop Web UI now uses pinned TanStack Router and history packages for
eight real top-level views and all 14 frozen ICM hash-route skeletons:
`#overview`, `#access`, `#access/{accessId}/routing`,
`#activity/requests/{exchangeId}`, `#extensions/discover`,
`#extensions/installed`, `#extensions/detail/{extensionId}`,
`#quality/sites`, `#dashboards/system`, `#activity/requests`,
`#policies/approvals`, `#settings/recovery`, `#extensions/develop`, and
`#dashboards/extensions/{dashboardId}`. External hashes use the design's exact
no-leading-slash spelling (`#overview`); legacy `#/…`, `/policy`, `/policies`,
`/approvals`, and `/system` inputs replace into their canonical routes. Policy
approval links carry one validated `selected` locator, unknown routes fail
closed to Overview, and the nested main-content scroller restores browser
history positions. `#activity/requests` now reads the canonical, cursor-paged
four-field Exchange summaries, and `#activity/requests/{exchangeId}` reads one
closed, redacted detail projection joined from durable Activity and
EgressAttempt evidence. The detail exposes only the Exchange/Access/status,
stable result code, ordered upstream-attempt IDs, optional egress-proxy ID, and
plugin-run IDs; a missing Exchange is a typed 404 and an incomplete projection
fails closed. Other deep tasks whose authoritative control contract does not
yet exist preserve the precise locator and show an unavailable boundary instead
of a fixture or fallback object. Dynamic IDs and leaf search state are bounded
and fail closed without reflecting an arbitrary URL value into the document.

The current macOS main-window path now has host-owned navigation restart
persistence. An explicit launch hash takes precedence; only when it is absent
does startup load a validated locator before Router construction and install it
with history replacement. The UI uses neither `localStorage` nor
`sessionStorage`. Tauri stores only a versioned canonical locator, bounded to
2 KiB inside a 4 KiB app-data file, behind main-window-only commands. On macOS,
the store requires an owned `0700` directory and owned regular `0600` file,
refuses symlink traversal, and commits through an atomic synchronized replace.
It does not persist capabilities, secrets, business objects, or Query cache;
the authenticated control reads and Query snapshots are rebuilt after restart.

The native host now also owns the post-readiness sidecar boundary. A retained
attempt identity closes both the ready-but-undelivered and delivered generation
when the process exits, so the command waiting across the Ready/delivery seam
cannot silently spawn or consume a replacement. Rust emits only the closed
`daemon_exited` lifecycle event to `main`; React closes the old client, aborts
active and future control requests, shows synchronized localized recovery copy,
and waits for an explicit Restart action before invoking a new generation.
Intentional bounded App shutdown transitions to stopping first and emits no
crash event. Exit status, stderr, paths, argv, environment, and capabilities do
not cross the event. The design-required retained minimal fault record and
user-driven diagnostic export remain separate open operations work; their
storage/retention/export schema is not frozen by the current design.

The browser suite covers the 14-route ICM matrix, exact physical hashes,
direct entry, reload, legacy replace, back/forward navigation,
selected-approval focus across polling, invalid and missing locators,
nested-scroll restoration, the skip link, and a narrow viewport. Permanent
boundary cases exercise maximum-size identities in both locales, all five
Policy actions at phone width, cyclic cursor refusal, honest initial metric
loading, first-source failure, and missing Exchange retry/back recovery without
horizontal document overflow. The production bundle excludes the development
preview client/control/data path and retains the strict CSP. Preview
deliberately skips the native navigation load/save commands, so its reload
coverage is not packaged macOS cold-start restoration acceptance. The v6
packaged command owns that proof: in an isolated HOME it seeds a safe
noncanonical locator, requires an atomic Router rewrite, checks the exit flush,
and repeats the observation in a second cold launch. No fresh v6 report exists
for this dirty worktree.

The same slice now uses a session-scoped TanStack Query client rather than a
parallel Dashboard cache. Seven loopback sources poll and fail independently;
Activity polls its head and loads older cursor pages only on explicit request.
The sources consume Query cancellation signals, retain explicitly stale
last-success evidence on partial failure, and continue while the browser
reports offline.
One pending source cannot hide another source's failure, and an empty stale
snapshot is not rendered as authoritative. Control writes are non-retrying
mutations with post-success targeted background invalidation, so a committed
write is not reversed by a failed or hanging follow-up read. Access plans and
credential metadata also use the Query cache, while credential values and
complete Access-apply inputs stay outside both Query and Mutation cache. The
native one-shot session is consumed once under StrictMode and reused across a
failed first loopback inspection and UI retry.

SQLite and the Access Manager now expose a defensive, lifecycle-bounded
aggregate read by Access ID, including disabled Accesses that are absent from
the active projection. Root Access GET/PATCH and editable UI hydration remain
open because the canonical OpenAPI has no path or DTO for them and currently
conflicts with the design prose on ETag/If-Match representation; this slice
does not introduce an implementation-local wire contract in their place.
The existing transitional apply route remains enabled-only and rejects every
other status before a durable write. Its closed commit receipt distinguishes an
exact published candidate (`active` plus its frozen plan hash) from a known
commit whose process-local projection failed (`unavailable`, without a hash),
so neither the UI nor the acceptance consumer can misreport the latter as
active or reconstruct a receipt by racing a follow-up read.

The embedded SQLite migration set is now revision 26. Store startup derives the
binary revision from the ordered embedded sources and refuses a newer Goose
history before applying any migration, including a future rolled-back history.
Before constructing transports, ProductRuntime reconstructs every durable
EgressAttempt through the domain validators, atomically rejects corrupt or
partial terminal evidence, and marks only valid prior-generation nonterminals
`failed(daemon_restarted)` without moving completion time before start. Runtime
terminal writes ignore request cancellation but share lifecycle and shutdown
deadlines; construction or persistence failure latches storage unavailable,
cancels the runtime owner, and is included in bounded shutdown. Revision 26
also aligns SQLite with the typed `plugin_catalog_sync` and
`plugin_artifact_fetch` runtime egress purposes while preserving existing rows,
sequences, and indexes.

This is still a bounded control-plane slice. Only the frozen ICM routes are
registered; wider panel/tab families and the unfrozen
filter/time-range/inspector URL grammar remain open. Multi-window navigation
restore/synchronization is not implemented, and native commands currently
reject every Webview except `main`. Windows owner/DACL and reparse-point checks
are not implemented, so the non-Unix navigation store fails closed. The UI
event/WebSocket invalidation and recovery contract remains unfrozen and the
current reads continue polling. This work does not change the freshness gate's
in-progress status.

## Implemented

- one shared current v6 wire schema used by the report generator and verifier,
  with the original v5 check contract retained only for historical reports;
- strict JSON, source, client, configuration, check-set, artifact, toolchain,
  path, ownership, permission, size, and file-identity validation, including
  independent re-hashing of current v6 source/configuration and artifact bytes;
- public `vibermate-acceptance-verify` CLI with explicit
  schema/revision/client flags plus trusted source, App, acceptance-executable,
  and client coordinates for current v6 evidence;
- public-entrypoint mutation, fixture, unsafe-file, concurrency, race, and CLI
  tests, including substituted-byte, dirty-checkout, coordinate-swap, manifest,
  and configuration-drift rejection, included in the normal contract job;
- manual protected macOS arm64 build/run/verify workflow with a seven-day,
  uncommitted private report artifact and fail-closed missing-report handling.

## Still required

- provision and protect the runner label plus
  `VIBERMATE_CLAUDE_2_1_220_PATH` without `latest` or hidden client
  authentication/network setup;
- produce a new clean private report bound to the implementation candidate and
  run all release gates from a clean worktree.

## Goal

Turn deterministic packaged evidence freshness into a fail-closed machine gate
without committing private reports or introducing credentials.

## Required work

1. Define one strict typed verifier for the current report schema. It accepts an
   explicit expected full Git revision and expected deterministic client; it
   never silently derives or accepts a nearby revision.
2. Reject unknown fields, wrong schema, dirty source, stale revision, non-passed
   report/checks, duplicate/missing/extra required check IDs, non-deterministic
   configuration, malformed or duplicate artifact roles/digests, client drift,
   and unsafe report file shape or permissions.
3. Exercise every rejection through the verifier's public entrypoint with
   known-good and injected-bad fixtures. Mutating only an internal helper is not
   sufficient evidence that the gate is wired.
4. Add one macOS arm64 packaged workflow that builds from the selected clean
   revision with the frozen toolchains, runs deterministic acceptance with the
   fixed verified client, and immediately verifies the produced report against
   that same revision and artifact set.
5. A missing, stale, blocked, failed, or unreadable report must fail the gate.
   It must never be treated as a skipped success.
6. Keep credentialed evidence separate and opt-in. Do not read or inject a real
   provider credential in this Goal.
7. Keep recognized-client packaged acceptance separate. This Goal uses the
   existing fixed verified-client path and does not broaden Root delivery.

The workflow must first freeze how the exact client binary is provisioned. It
must not download an unreviewed latest version or add authentication/network
side effects merely to make CI convenient.

## Completion evidence

- verifier unit, fixture, mutation, and race tests;
- a clean packaged deterministic report bound to the implementation candidate;
- proof that the same report is rejected when the expected revision changes;
- proof that a report mutation, check deletion/duplication, dirty flag, client
  mismatch, or unsafe file is rejected;
- full repository gates and a clean worktree;
- no push and no design-repository change.

## Completion statement

> The current clean macOS arm64 packaged deterministic assembly has passing
> evidence, and the gate rejects evidence that is missing, malformed, failed, or
> bound to any other source revision. Credentialed, recognized-client, signing,
> notarization, system trust, Preview, and Release claims remain outside scope.

The repository calls the unsigned native-secret build
`bundle:packaged-acceptance` and deliberately exposes no publish or
`bundle:release` command. The build hooks reject missing profiles, wrong
targets, dirty release-profile source, toolchain drift, stale sidecars, and
manifest/config digest drift. A separate manually dispatched workflow now
builds a Universal candidate without Apple distribution credentials and
produces source-traceability evidence (R0) on a fresh trusted runner. R0 binds
the selected commit, dependency lock files, exact toolchain, SPDX SBOM, build
manifest, and staged artifact digests; isolated Developer ID and DMG-only
notarization environments are gated on that evidence. After notarization,
another fresh `macos-15` arm64 job has no GitHub Environment and no Apple
distribution credentials. Candidate source remains inert except for ancestry
proof; trusted tooling read-only mounts the exact stapled DMG, copies the App
to an isolated stable `$RUNNER_TEMP/.../Applications` root, verifies
signature/Team ID, Universal slices, minimum OS, bundle inventory, tree
equality and Gatekeeper, and reuses packaged acceptance for bounded
launch/readiness/persistence/exit.
The only Apple identity input is the non-secret repository variable
`VIBERMATE_APPLE_TEAM_ID`; missing or malformed input fails closed.
It emits a closed `signed-package-installation-report.json` plus checksum only
after exact mount/App/home/state cleanup, and an independent verifier binds the
report back to raw notarization evidence. This is runner-isolated installation
evidence only: it does not assert real `/Applications`, CLI-path, updater,
system-trust/proxy, or uninstall behavior, and the workflow does not publish.

Distribution remains blocked until the GitHub environments are independently
confirmed to enforce reviewers, no self-review, protected default-branch
deployment, no administrator bypass, and environment-only Apple secrets; a
real Apple-credentialed signing/notary/installed-evidence run succeeds; and
current packaged conformance, R2 reproducibility, R3 signed-package binding,
release approval, and update/uninstall evidence are complete. The 2026-08-03
external audit found zero GitHub Environments in this private repository,
whose default branch is `main`. Branch-protection and rulesets APIs both return
403 under the current plan (“Upgrade to GitHub Pro or make public”), so
`github.ref_protected` is currently false and the workflow intentionally fails
closed; that guard must not be weakened. The PAT also received 403 while
enumerating repository secret/variable names, leaving those names unverified.
