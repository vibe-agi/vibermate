# Packaged Deterministic Freshness Gate

Status: planned
Created: 2026-08-02
Implementation baseline: `1b1a1b5fe43e1e4d89243006b10ff9c67ef0ea28`
Predecessor: `docs/plans/archive/2026-08-02-packaged-deterministic-discovery.md`

## Current fact

A clean packaged deterministic run now passes 17 of 17 checks at the baseline.
The private v5 report binds the full source revision, clean state, fixed client,
App members, build manifest, configuration digests, and pinned toolchains.

No machine gate consumes that report. A later commit can still be described
using stale evidence unless a reviewer manually notices the revision mismatch.

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
