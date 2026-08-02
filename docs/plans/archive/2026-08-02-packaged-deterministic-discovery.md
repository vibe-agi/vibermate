# Packaged Deterministic Discovery

Status: complete
Created: 2026-08-02
Starting source: `1af205ca559adb6aac5cc05ceaf2ada7466a4b60`
Passing source: `1b1a1b5fe43e1e4d89243006b10ff9c67ef0ea28`
Predecessor: `docs/plans/archive/2026-08-02-production-composition-structural-guard.md`

## Purpose and constraints

This was a discovery run, not a freshness-verifier implementation. It built a
development-profile macOS arm64 App, both packaged sidecars, and the acceptance
runner from standalone clean checkouts. It selected fixed Claude Code 2.1.220,
used a run-local missing `SecretRef`, did not supply a credential, and did not
exercise a real provider.

Each first failure was retained before any correction. Environment-only retry
reused the same source and artifact digests. Every evidence directory is mode
`0700`; every report is mode `0600`.

## Discovery chronology

### 1. Host toolchain was not pinned in the first process

The first run from `1af205c` failed at `build-provenance` because the acceptance
process saw Node outside the pinned 22.23.1 environment. The original report is:

- `/private/tmp/vibermate-deterministic-1af205c.cyjJzd/evidence/deterministic-report-first-1af205c.json`
- SHA-256 `a1f454c858da15fa9a5f7c6c59e79651ef4ed1c531e9603d6aabb5108fe8b88e`

An environment-only retry used the identical source and App members. It passed
provenance and exposed the first product-era harness drift:

- `/private/tmp/vibermate-deterministic-1af205c.cyjJzd/evidence/deterministic-report-env-retry-1af205c.json`
- SHA-256 `9ff5d9677fafb803b4ebd4e84d551059a543ae824e58440c858d32b63e3d6754`

The fixed client was waiting on the shipped `default.ask` connection policy
until approval expiry. The runtime was fail-closed as designed; the old harness
had assumed an implicit allow that no longer exists.

### 2. The harness now owns one exact connection rule

Commit `64bd40f4521d6b92c2b09bd17b8cb973a0672ed3` made the harness read and replace
the real connection-policy resource, adding one exact host-and-port allow for the
selected fixed client while proving that the default remains `ask`. It neither
changes nor bypasses the production default.

The newly built `64bd40f` artifact reached the intentional missing-credential
boundary, then exposed a second stale assertion:

- `/private/tmp/vibermate-deterministic-64bd40f.xrpdMH/evidence/deterministic-report-64bd40f.json`
- SHA-256 `14d45e299d66af7857c1a32b5c5bb8b02f32e9a736853f1c0a7abae82ed6d86b`

### 3. ConnectionEvent was incorrectly treated as provider-attempt evidence

The acceptance helper still required `configured` source confidence, the former
implicit rule ID, a provider route, and a credential-binding ID on a
ConnectionEvent. Current production correctly records the client-side
connection only; request-level provider facts belong to Activity and
EgressAttempt evidence.

Commit `006e487baf643cf1353dd85f70fc232625fcab3b` changed the check to require the
exact four-phase client timeline, verified source evidence, the explicit rule,
observed SNI, MITM mode, positive terminal byte counts, and the absence of
request-level provider facts. Its first packaged run passed that check and then
found the same stale assumptions in active held-connection discovery:

- `/private/tmp/vibermate-deterministic-006e487.OfRQgV/evidence/deterministic-report-006e487.json`
- SHA-256 `e4c7e7ce782b3dee222ef8c0fcf8f5445588b4b835abdca3f4d82fc0b359630c`

### 4. Active held-connection discovery used the old host shape

The active helper compared the host-only ConnectionEvent field with a joined
`host:port` authority and still required `configured` confidence. Commit
`1b1a1b5fe43e1e4d89243006b10ff9c67ef0ea28` made terminal and active checks share
one strict client-side evidence validator. Tests reject the old confidence, the
joined host shape, the wrong rule, and provider-field contamination.

## Passing packaged evidence

- Standalone clean source:
  `/private/tmp/vibermate-deterministic-1b1a1b5.W4h4LV/source`
- App:
  `/private/tmp/vibermate-deterministic-1b1a1b5.W4h4LV/source/ui/desktop/src-tauri/target/release/bundle/macos/VibeMate.app`
- Report:
  `/private/tmp/vibermate-deterministic-1b1a1b5.W4h4LV/evidence/deterministic-report-1b1a1b5.json`
- Report schema: `vibermate.m0-assembly-acceptance/v5`
- Report SHA-256:
  `40f2d774078e74a533bbf30c956f7b1f8bc0ef07acfb900ced18092fb7df580c`
- Result: 17 of 17 checks passed; report mode `0600`; source dirty flag `false`.
- Acceptance runner SHA-256:
  `ede4f8deacb4dccbea8b8767d093bd7ae6f48aee9f98df1ba90ea5c50dd3fcd1`
- App-bundle SHA-256:
  `b9f33c1e00abc355b9db6e6c2b2b10a78bccb1b30764fe10bede45779cbd1d81`
- Desktop executable SHA-256:
  `f8da3b0d633ef25bf3dbb54195988f90aa68108012e10279087970dd06bd530a`
- Packaged daemon SHA-256:
  `7b193cc84190be353ef2b57dc81dc26b20aacff26dee9c81d4091e74346e3505`
- Packaged launcher SHA-256:
  `dabe8939b6ec9a28da2891593007c5ac9dc51e0e5a6827415ceff9745d6ea785`
- Embedded build-manifest SHA-256:
  `4e71718b956f774208ad7993a8356eb2aa3572294f2b6f058425f5e5f9b66b86`
- Fixed Claude Code entrypoint SHA-256:
  `8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081`
- Toolchains: Go 1.25.12, Node 22.23.1, pnpm 10.33.2, and Rust/Cargo 1.88.0.

A structural scan found no credential-like value, prompt marker, response body,
tool argument, or authorization material in the report. The standalone source
remained clean after build and acceptance.

## What this proves

One fixed Claude Code 2.1.220 build traversed the clean packaged App built from
the recorded passing source, explicit connection policy, authenticated proxy,
exact AgentEndpoint MITM,
missing-credential fail-closed boundary, body-free client ConnectionEvent,
Offline Hold, graceful shutdown, force-kill, SQLite reopen, and interrupted
connection recovery paths.

## What this does not prove

- no current-HEAD credentialed provider path;
- no recognized-client packaged path;
- no Codex behavior at the passing source;
- no live system-Keychain trust operation;
- no physical network-loss, sleep, power-loss, signing, notarization, or
  distribution behavior;
- no Preview or Release readiness;
- no machine consumer yet rejects a stale report for a later commit.

The last point is the scope of the successor `PLAN.md`.
