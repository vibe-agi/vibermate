# Capture supervision after laptop sleep — VIBERMATE-47

## Confirmed code path

The CLI message is produced by ViberMate stopping the supervised child, not by
Codex reporting a provider timeout. The launcher sends a heartbeat every 30s,
with a 5s per-call budget. Desktop grants a 90s proxy lease (the Capture manager
default is 2m). Previously:

1. Laptop suspension outlasted that wall-clock lease.
2. Heartbeat only accepted an unexpired `attached` row; listing/recovery could
   additionally change it to `expired`.
3. Any heartbeat error canceled the child context and stopped its process group.
   ACP duplicated the same one-error termination policy.
4. The control handler also mislabeled temporary persistence/deadline failures
   as `403 run_capability_rejected`.

This reproduces the reported path without suspending the user's machine. It is
not evidence that an in-flight provider TCP/SSE stream survives sleep.

## Fix and security constraints

- Reuse one run/ACP heartbeat loop. Transport failures, incomplete responses,
  HTTP 408/429/5xx retry with bounded calls, at most every 2s during recovery.
  No elapsed-outage timer kills the child after hours of suspension.
- Announce interruption and restoration once per outage. Diagnostics contain
  no URL, capability or server response body. Non-file stderr writers serialize
  child output with these messages; ACP keeps native inherited files.
- `expired` means an inactive proxy lease, not revocation. Only the original
  supervisor capability of an already attached process can renew it. The exact
  Runtime login/user is revalidated on every renewal. Run ID, PID, frozen
  configuration, history and proxy token remain unchanged.
- Proxy authorization remains denied throughout expiry. The proxy token cannot
  renew a lease. Finished, revoked, deleted or never-attached runs cannot resume.
- Shutdown revokes expired runs too. Finishing an expired run is permitted as
  cleanup, and is irreversible. Renewal enters the existing archive barrier,
  preventing re-admission from racing destructive maintenance.
- Temporary control failures return 503, not fabricated revocation. Actual
  authorization rejection still ends supervision; there is no fallback direct
  connection, new login, new Capture, grant widening or disabled TLS check.
- ACP starts heartbeats only after PID attachment. Its faster observation tick
  revalidates a rejected lease once before retrying publication, so wake-up order
  cannot incorrectly terminate the process before the heartbeat runs.

No new schema, migration, dependency, platform sleep hook or Codex-only path.
An explicitly stopped/restarted Runtime is not silently assigned a replacement
Capture. This change does not make an Agent continue computing while asleep or
replay provider requests on its behalf.

## Checks

- A 12-hour fake-clock jump originally failed renewal; now succeeds before or
  after catalog expiry reconciliation. The same test rejects wrong supervisor,
  finished, shutdown, deleted and never-attached runs; expired proxy use fails.
- A real shell child stays alive through 503, request timeout and 429; after
  recovery it reads input and returns its original exit code. Create/attach each
  occur once. A subsequent 403 stops that same child. Repeated race runs pass.
- Local ACP protocol exchange continues after a 3-hour clock jump when the
  observation tick, deliberately ahead of heartbeat, performs renewal.
- Remote logout, disable and password reset reject renewal of expired runs.
- Lifecycle HTTP tests distinguish unavailable storage from forbidden authority.
- `go test ./...` passed; runlauncher/capturerun/capturecontrol race suites passed;
  focused Desktop/remote ACP/revocation/wake race checks passed three runs.
  Targeted `go vet` and `git diff --check` passed.

Independent candidate built and verified:
`dist/candidates/local-kueeBmxJ/ViberMate.app`.
Packaged daemon SHA-256:
`b3a3751960ef73ca33243120d04508b076973f309ed32aa0a28d09b55fc7f890`;
packaged CLI:
`e3c87af7c8521d71242333ac714f7cb1c4efc4654ad54339947a4ae7457b5376`.
This is the current dirty-source, ad-hoc signed local candidate, not a release.
It includes the mainline v1 schema work and must not be pointed at old data
before the explicit offline conversion is ready.

The installed App and real data remain untouched. `/Applications/ViberMate.app`
and `dist/ViberMate.app` still have daemon hash
`b2d337e74aec550aa5e3b5acdbb0ff1eb398a06b18fea8be5c67285ad7e25eb2`.
Packaging is separate from installation, real-sleep acceptance and release.
