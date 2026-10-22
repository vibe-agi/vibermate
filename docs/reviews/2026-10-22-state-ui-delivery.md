# State, UI, integrations, and delivery review — 2026-10-22

Reviewed product base: `b03ebd8be040087d623f7a1b12c340e99994f56f` (v0.1.35). PR 66 head: `6995ea291a13505fa14a68666cbfee07bf3accb9`. All four findings below exist in the base; none is introduced by PR 66. The proposed severities are one P1 release-security item and three P2 reliability/UI items. The recording maintenance and diagnostic findings share a trigger but require different fixes.

This was a read-only production-code audit. Synthetic Go fixtures were supplied through a Go overlay; the Flutter fixture lives outside the repository. No installed App, real Runtime data, account, permission, Keychain, provider traffic, dependency, CI run, release, or PR was changed. Evidence is retained in `/Users/null/Downloads/vibermate-codebase-review-20261022`.

## Findings

### SD-1 — P1: replace the vulnerable Go toolchain across the complete release chain

`go.mod:5` pins Go 1.26.9. The exact PR 66 [vulnerability job](https://github.com/vibe-agi/vibermate/actions/runs/39614335458/job/119271734315) and [native-release-build job](https://github.com/vibe-agi/vibermate/actions/runs/39614335458/job/119271734201) both failed on `GO-2026-6809`, with `crypto/x509@go1.26.9`, fixed in 1.26.10. Native `go build`, `go vet`, Windows cross-build, and Linux cross-build completed before the scan failed. This is a newly reported dependency security gate, not a native compilation regression caused by the UI PR.

The [immutable official advisory](https://github.com/golang/vulndb/blob/e0bad56276e98711a598adbab976de7cd914013a/data/osv/GO-2026-6809.json) identifies CVE-2026-111943, published 2026-10-21 00:27:29 UTC. Its affected ranges end at Go 1.26.10 and 1.27.3; Go 1.27 prereleases through 1.27.2 are also affected. The issue concerns known compromised intermediate certificates when the applicable trust store has not distrusted them. The patch filters chains containing any of eight distrusted public-key hashes. This explains why older green CI evidence does not imply a current clean vulnerability result.

Reachability and limits:

- The scan's concrete call path is [`desktoptrust.ClientCAStatus` → `Certificate.Verify`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/desktoptrust/client_ca.go#L36). This particular method checks a public Runtime Proxy CA; a reachable symbol alone does not demonstrate that an attacker can inject a compromised chain there.
- Network-facing production transport/probe code also creates verified TLS clients: [`providertransport/transport.go:166`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/providertransport/transport.go#L166), `providertransport/probe.go:68`, and `originaltransport/probe.go:72`. Exploitation would still require a usable malicious certificate chain and an affected trust configuration. No real attack or provider traffic was attempted.
- The advisory contains no platform exclusion. The [patched source](https://github.com/golang/go/blob/4e8c85f5319d999bfd7617fdc191631e5634e3c5/src/crypto/x509/verify.go#L560) returns native verification results on the macOS/iOS/Windows system-pool path before the new Go-chain filter. Those paths depend on platform distrust updates; explicit/fallback roots and the Linux Go verifier have different exposure. The two CI failures do not prove the released macOS App is exploitable.
- The logs separately mention one advisory in a required module without apparent vulnerable-symbol calls. That aggregate is not treated as a second proven reachable vulnerability here.

Updating only `go.mod` leaves contradictory release requirements. The production pins include [CI's exact compiler assertion](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/.github/workflows/ci.yml#L261), [`acceptancereport.ExpectedGoVersion`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/acceptancereport/schema.go#L16), and the independently built VLESS kernel: [`kernel-lock.json:20`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/tool/vless-kernel/kernel-lock.json#L20), `build_kernel.py:175,190`, `package_kernel.py:242`, and `tool/linux-release/verify-linux-distributions.sh:113`. Their receipts, compiler identity, artifact hashes, and corresponding fixtures need coherent regeneration. Existing released binaries are unaffected by editing these pins; a new verified release is required.

Validation: saved the exact GitHub job logs as `pr66-vulnerability.log` and `pr66-native-release-build.log`, the official advisory as `GO-2026-6809.json`, and patched Go source as `x509-verify-go1.26.10.go`. No scan was bypassed or retried. A fix should pass both tagged and ordinary govulncheck runs and rebuild/verify the native and kernel artifacts with matching provenance.

### SD-2 — P2: routine expiry rewrites all archives even when no records expire

[`requestRoutineRecordingCleanup`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/productruntime/recording_cleanup.go#L158) unconditionally publishes a cleanup obligation once a minute. [`maintain`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/productruntime/recording_maintenance.go#L81) pauses observation admission, seals the recorder and raw offers, drains writers/readers, then calls the cleanup routine. After deletion, [`executeRecordingCleanup:109–118`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/productruntime/recording_cleanup.go#L109) compacts every current and legacy source without checking whether that source changed. [`CompactRecordingSource:226`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/runtimepersistence/recording_cleanup.go#L226) runs full `VACUUM`, checkpoint, integrity and foreign-key checks; certification hashes source files again.

Trigger: any running Runtime with retained history, even when every record remains within retention and nobody requested deletion. It creates recurring full-database I/O and a period when new optional observations are rejected. The budget deliberately drops those offers rather than delaying Agent execution (`recording.go:112–123`). The cost grows with retained archive bytes; it is not bounded by the number of expired rows.

The isolated `TestAudit20261022RoutineNoopRewritesArchive` seeds a temporary revision-1 database with one unexpired synthetic 16 MiB ACP snapshot, starts the real storage cutover/runtime, and invokes the real routine scheduler twice at successive due times. Each completed certificate reports zero removed records. The 17,514,496-byte legacy archive's SQLite schema cookie increases with each pass, proving repeated `VACUUM` rewrites:

| Pass | Removed records | Archive bytes | Schema cookie | Elapsed on this host |
| --- | ---: | ---: | --- | ---: |
| 1 | 0 | 17,514,496 | 109 → 110 | 684 ms |
| 2 | 0 | 17,514,496 | 110 → 111 | 627 ms |

These are small local synthetic measurements, not end-to-end latency or a hardware-independent performance claim. The fixture does not claim Agent execution was blocked. It verifies work performed while recording is sealed.

Recommended change: distinguish routine expiry/no-work passes from physical-removal obligations, retain per-source proof of actual cleanup work, and avoid full compaction of untouched sources. Preserve the durable visibility/denial publication, late-writer fencing, approval/one-use authority, and physical-removal certification for sources that need it. A regression should prove a no-expiry pass does not rewrite a retained archive while a real deletion still removes its bytes and certifies completion.

### SD-3 — P2: cleanup failure loses the cause while recording stays disabled until restart

[`recording_maintenance.go:101`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/productruntime/recording_maintenance.go#L101) reduces `executeRecordingCleanup`'s error to a boolean. The failure branch sets `unsafeMaintenance`, closes inspection admission, keeps the recording seals, and reports only `RecordingDegraded` (`136–147`). The [periodic worker](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/productruntime/recording_storage.go#L222) subsequently skips cleanup while the flag remains set. The normal structured `RecordingFailure` channel is never given the original cause or cleanup phase.

Trigger: a failed cleanup receipt write, purge, checkpoint, compaction, integrity check, or certificate step. The synthetic reproduction holds an external SQLite read transaction long enough for the routine pass's truncate-checkpoint to fail, then releases it. After the next worker tick:

```text
runtime=initialized storage=healthy recording=degraded
specific_failure=<nil> cleanup_pending=true
new_record_offer=DroppedUnavailable control_create=ok
```

The runtime's required control operation still succeeds, confirming the intended execution/recording separation. However, the operator cannot distinguish the persistent state from an ordinary temporary recording gap using the detailed failure panel, which is rendered only when [`recordingFailure` exists](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/workbench_shell.dart#L114). The existing restart-recovery test documents that reopening and recovery are needed to retry the retained claim.

The latch itself is conservative privacy behavior and must not simply be removed: a partially mutated source cannot safely re-admit readers/writers. Retain a redacted, typed cleanup failure with phase/recovery state, and communicate that recording requires verified recovery/restart. If implementing an in-process retry, it must use the same durable claim and full recovery/certification path. Do not reopen the catalog merely because the original syscall error is transient.

Validation: `TestAudit20261022RoutineBusyFailureLosesDiagnostic` passes by reproducing the lost diagnostic and retained latch using only temporary storage. Existing `TestRuntimeCleanupRetainedWALFailureRecoversWithoutPrematureCompletion` was source-reviewed for the supported restart path; it was not rerun locally in this audit.

### SD-4 — P2: a late allowlist GET overwrites an acknowledged save in the UI

[`_loadServerIpAllowlist:2423–2424`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/workbench_controller.dart#L2423) publishes any completed read if the controller is alive. It has no generation/revision check against [`saveServerIpAllowlist`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/workbench_controller.dart#L2442). A read started before a save can therefore replace the newer PATCH result at line 2447. Quiet server-management refreshes also do not acquire the loading flag, so a loading flag alone does not order these operations.

Reproduction using the real controller's public methods and an in-memory API double:

1. Display revision 1, an empty allowlist; start `refreshServerManagement(quiet: true)` and hold its allowlist GET response.
2. Save `203.0.113.0/24`; the API acknowledges revision 2 and the controller displays revision 2.
3. Deliver the old GET response. The server double remains at revision 2 with the saved CIDR, while the controller returns to revision 1 with an empty list.

```text
authoritative_revision=2 authoritative_ranges=[203.0.113.0/24]
displayed_revision=1 displayed_ranges=[]
Expected: <2>  Actual: <1>
```

This is incorrect security-setting display and can provoke confusing subsequent edit conflicts. It does not undo the server policy, bypass its CAS, or grant access. The delayed-response fixture intentionally asserts the desired invariant and fails against the audited base. It uses no real server or App.

Recommended change: invalidate pre-mutation loads when a save succeeds, or use a monotonic revision/generation fence when publishing responses; keep conflict reconciliation. Test old GET after successful save, two quiet reads finishing out of order, conflict refresh, and disposal. Other runtime-user refresh/mutation paths have a similar shape, but this report counts only the reproduced allowlist case.

## Coverage and preserved contracts

The review used `CONTEXT.md`, `docs/module-map.md`, ADRs 0009, 0020, 0021 and 0023, backup/restore documentation, and storage/read-performance documentation. There is no `.codegraph/` directory in this checkout, so no index was created. Source/tests were followed through representative deep paths; this is not a claim that every line or every platform was exercised.

| Area | Reviewed boundary and evidence | Result/limit |
| --- | --- | --- |
| Recording budget/lifecycle | `recording/budget.go`, recorder offer/enqueue/settle/flush, raw/content/native/usage publication and existing blocked-writer tests | Shared non-waiting admission and reservations remain owned until actual settlement. The reproduced problem is unnecessary maintenance work and failure disclosure, not a demonstrated Agent-execution dependency. |
| Required vs optional state | `runtimepersistence/storage_roles.go`, control extraction, `productruntime/recording_storage.go`, ADR 0023 | Separate role owners, files and handles; required control remains usable in the cleanup-failure reproduction. No suggestion to make accounts/configuration/permissions lossy. |
| Deletion/privacy/retention | Control archive epoch and capture fences; denial chain and writer coverage; cleanup source selection, usage retention caps, late-observation tests | Removal first restricts visibility durably, and dependent inspection fails closed if denial proof is missing. Native approvals and one-use facts remain required authority. No new bypass reproduced. |
| Migration/cutover | `storage_cutover.go`, schema upgrade/snapshot, `control_extract.go`, exact schema validation and failure fixtures | Supported old roles are extracted into new control with receipts and archive provenance. Ambiguous/missing control does not silently create a fresh authoritative store. The no-op fixture traverses an actual v1 cutover. |
| Backup/restore/move | `move.go`, `backup.go`, `backup_roles.go`, role inventory, optional absence, WAL/SHM handling, directory guards | New destination only; source and host locks; hash/schema/integrity checks; pending cleanup refuses export; public backup omits root provider secret stores. Manifest is integrity evidence, not encryption or an attacker-proof signature. No broad backup suite rerun locally. |
| Flutter storage/evidence | Cleanup statistics generations, cache invalidation, native reader epochs and content-generation keys, desktop storage pending selection | These reviewed paths already contain stale-read/lifecycle fencing. The server allowlist path does not. |
| Web sessions/control | `platform_runtime_web.dart`, session store, `HttpControlApi` request lifecycle | Origin-bound tab storage, verified current principal, exact-record cleanup, retired-session handling and bounded request body/response paths reviewed. No auth/token values accessed. |
| Paseo/Claude/Codex | Production `agent.session_open` hook, persisted binding, selection timeout/cancel/commit, CLI catalog bounds and shell wrappers | Explicit Environment and Runtime Server retained per binding; history-only opens skipped; missing selections refuse launch; argv/cwd preserved. No live Paseo/editor acceptance performed. |
| Delivery/dependencies | Exact PR CI logs, SHA-pinned Actions, workflow permission boundaries, Linux distribution validation, trusted macOS tooling/signing jobs, Go/kernel pins, Flutter/Node/Paseo lock manifests | Current Go advisory is actionable. Existing green source/fixture checks do not establish a new signed/notarized release or live current-client/provider compatibility. |

PR 66 changes only `app_copy.dart`, `egress_profile_editor.dart`, `settings_view.dart`, `egress_default_settings_test.dart`, and one golden. Its active-network-exit versus saved-revision wording/controls are a known UX repair and are excluded from the new findings. The controller, maintenance, dependencies and workflows above are unchanged by that PR.

## Executed validation

The exact pinned compiler was used directly after discovering that the system `/usr/local/go/bin/go` is 1.25.8 when toolchain switching is disabled. The initial `GOTOOLCHAIN=local go test` exited before compiling because the module requires Go 1.26; that is an audit invocation issue, not a product failure. The subsequent command completed both synthetic reproductions:

```sh
GOTOOLCHAIN=local GOMAXPROCS=4 DEVELOPER_DIR=/Library/Developer/CommandLineTools \
  /Users/null/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.9.darwin-arm64/bin/go test \
  -p=2 -overlay /Users/null/Downloads/vibermate-codebase-review-20261022/audit-recording-overlay.json \
  ./internal/productruntime -run '^TestAudit20261022' -count=1 -timeout=90s -v
```

Result: 2/2 reproduction tests passed, package time 4.540 seconds. Source: `audit_recording_20261022_test.go`; output: `audit-recording-repro.log`.

From `ui/flutter_app`:

```sh
flutter test --no-pub \
  /Users/null/Downloads/vibermate-codebase-review-20261022/audit_allowlist_20261022_test.dart \
  --reporter expanded
```

Result: the one desired-behavior assertion failed as expected with displayed revision 1 versus authoritative revision 2. Output: `audit-allowlist-repro.log`. This confirms an existing bug; no fix or passing regression suite is claimed.

No broad Go/race/Flutter rerun, real provider call, installed client operation, backup of user data, trust change, or artifact publication was performed for this subreview. The exact CI logs supply current remote validation evidence; the synthetic tests supply causal local evidence for the newly investigated behaviors.
