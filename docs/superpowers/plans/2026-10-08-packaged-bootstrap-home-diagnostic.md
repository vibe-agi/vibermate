# Packaged Bootstrap HOME Diagnostic Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Root uses superpowers:subagent-driven-development to implement this plan task-by-task, with independent review after each task. Steps use checkbox (`- [ ]`) syntax for tracking. Root owns review and authorization of one writer; implementers do not spawn additional agents or dispatch hosted work without that authorization.

**Goal:** Determine whether the standalone acceptance daemon's HOME policy triggers its native-release bootstrap timeout, using byte-identical packaged inputs on two fresh disposable hosted machines.

**Architecture:** Add an acceptance-only diagnostic selector whose default retains existing behavior, plus a private capability-free observation of the first standalone bootstrap. A dedicated, explicitly dispatched diagnostic workflow builds and archives the App, acceptance harness, verifier and fixed client once; two independent macOS jobs authenticate and consume that exact archive. Existing packaged/release workflows, product startup, deadlines, native backend and V7 verifier remain unchanged.

**Tech Stack:** Go acceptance harness; Bash and Node built-ins for narrowly scoped artifact checks; pinned GitHub Actions; existing macOS/Flutter build helpers.

**Spec:** `.superpowers/sdd/2026-10-07-long-session-hotfix-release/packaged-bootstrap-diagnosis.md`, supplemented by the root's 2026-10-08 instruction: one producer, two fresh consumers, default isolated behavior, <=4 KiB private observations, unchanged V7/native checks, diagnostic workflow never grants release approval. Source baseline is `0396c4d21f85c47c669272068d0f26c0a61a6d58`; implementation uses a new clean revision descended from it.

## Global Constraints

- Plan only until root authorizes implementation. No product stage hooks, native Keychain probes, local App/daemon execution, SDK changes or remote changes in this planning task.
- Default is the existing isolated HOME policy. Treatment preserves the original HOME and removes `CFFIXED_USER_HOME`; all other environment entries and explicit cache/data arguments remain unchanged.
- Preserve the existing two-minute bootstrap deadline, all existing 15-second waits, App launch/drain deadlines and overall acceptance timeout. Do not add retries, `continue-on-error`, expected-failure-to-success conversion, development secrets or credential inputs.
- Observations are a separate <=4096-byte file, mode 0600 in a mode-0700 directory. Consumer evidence is encrypted to a controller-held recipient key before its seven-day repository artifact upload; the private key never enters Git, a hosted runner or GitHub. No raw bootstrap frame/nonce, stderr, error text, environment, paths, account/reference, Keychain contents, stack or memory dump belongs in the observation. The producer's public-source App/harness/verifier/fixed public npm-client archive and closed numeric producer statuses are non-confidential; they may use normal seven-day public-repository artifact storage. A private-* name or local file mode is not an access control.
- Both consumers independently verify archive digest against the producer job output and the verifier's clean source identity, then reuse existing bundle verification and the harness's prelaunch source/fixed-client checks. The authenticated archive binds all transferred bytes; do not add a redundant transfer manifest/per-role digest schema. No consumer rebuild, resign, npm reinstall or mutation of transferred inputs.
- All native V7 checks and the unchanged fresh verifier run in both arms. Actual producer, harness and verifier exit statuses are retained; a failed baseline remains failed evidence and a failed job. A diagnostic success is not release PASS.
- Do not modify `.github/workflows/packaged-acceptance.yml`, release workflows, `cmd/vibermate-acceptance-verify`, `internal/acceptancereport`, Flutter product code or native secret code.

## Scope and file map

Three reviewable tasks, one implementation writer, task-scoped commits and reviewed fixes. This is moderate acceptance/tooling work; the only expensive validation is the explicitly authorized one-producer/two-consumer hosted experiment. No general CI/archive framework or performance work.

| File | Responsibility |
|---|---|
| Modify `cmd/vibermate-acceptance/config.go` | Two diagnostic flags and strict validation; existing default preserved |
| Create `cmd/vibermate-acceptance/bootstrap_diagnostic.go` | HOME policy, closed observation type, first-attempt claim, bounded private writer |
| Create `cmd/vibermate-acceptance/bootstrap_diagnostic_test.go` | Policy/privacy/mode/size/concurrency tests with synthetic data only |
| Modify `cmd/vibermate-acceptance/daemon.go`, `daemon_test.go` | Select environment and observe decoder/terminal milestones without changing ownership/timeouts |
| Modify `cmd/vibermate-acceptance/main.go` | Allocate optional recorder, write it after acceptance, join write failure into command error |
| Create `tool/verify-packaged-diagnostic-verifier.mjs`, `.test.mjs` | One narrow check for the verifier binary's otherwise-unbound source identity |
| Create `.github/workflows/packaged-bootstrap-home-diagnostic.yml` | Explicit dedicated-branch producer plus two fresh consumers; actual exits and private evidence |
| Create `.github/packaged-bootstrap-home-diagnostic-contract.test.mjs` | Guard single producer, pinned actions, frozen inputs, outcomes and unchanged normal gates |

## Task 1: Acceptance-only policy and private first-bootstrap observation

**Interfaces:**

```go
type daemonHomePolicy string
const (
    daemonHomeIsolated daemonHomePolicy = "isolated"
    daemonHomeLogin daemonHomePolicy = "login"
)
type bootstrapDiagnosticRecorder struct {
    mu sync.Mutex
    claimed bool
    value bootstrapDiagnostic
}
// Add to config: diagnosticDaemonHome string, bootstrapDiagnosticPath string,
// bootstrapDiagnostic *bootstrapDiagnosticRecorder.
func validateBootstrapDiagnosticConfig(config config) error
func diagnosticDaemonEnvironment(base []string, dataDirectory string, policy daemonHomePolicy) ([]string, error)
func decodeDescriptorObserved(reader io.Reader, observed func(frame int)) (desktopbootstrap.Descriptor, error)
func writeBootstrapDiagnostic(path string, recorder *bootstrapDiagnosticRecorder) error
```

Use `diagnosticDaemonHome == ""` as the programmatic zero-value alias for isolated; `defaultConfig()` explicitly sets `"isolated"`. Flags are `--diagnostic-daemon-home=isolated|login` and `--bootstrap-diagnostic=<absolute-path>`. Login requires a diagnostic path and `--deterministic-only`; any diagnostic path also requires deterministic mode. Validate unknown policy, nonabsolute/unclean path, duplicate/missing/empty/nonabsolute HOME in login mode, and fail rather than guessing a login home. A standalone isolated call still invokes the existing `isolatedDaemonEnvironment` unchanged, including its private-directory creation.

- [x] First introduce compiling test seams that preserve current behavior; this setup is not RED and does not implement the treatment. Define the types above and payload below, and initially forward every policy to the old environment helper. Keep `validateBootstrapDiagnosticConfig` returning nil, `writeBootstrapDiagnostic` returning nil without writing, and `decodeDescriptorObserved` forwarding to the current decoder without invoking its callback. These temporary seams let tests demonstrate missing behavior without compiler failures; replace them in the following steps.

```go
func diagnosticDaemonEnvironment(base []string, dataDirectory string, _ daemonHomePolicy) ([]string, error) {
    return isolatedDaemonEnvironment(base, dataDirectory)
}
```

- [x] Add the behavioral regression test before implementing treatment:

```go
func TestDiagnosticDaemonHomeLoginPreservesOnlyLoginPolicy(t *testing.T) {
    base := []string{"HOME=/Users/disposable", "PATH=/usr/bin", "CFFIXED_USER_HOME=/private/fixture", "TOKEN=sentinel"}
    got, err := diagnosticDaemonEnvironment(base, filepath.Join(t.TempDir(), "data"), daemonHomeLogin)
    if err != nil { t.Fatal(err) }
    want := []string{"HOME=/Users/disposable", "PATH=/usr/bin", "TOKEN=sentinel"}
    if !slices.Equal(got, want) { t.Fatal("environment policy mismatch") }
    if len(base) != 4 || base[2] != "CFFIXED_USER_HOME=/private/fixture" { t.Fatal("mutated parent environment") }
}
```

Add table cases for default/explicit isolated equivalence, unknown policy, missing/duplicate/relative HOME, and treatment without diagnostics/deterministic mode. Retain `TestDaemonEnvironmentUsesAPrivateDataScopedHome`. Assertions must not print environment values on failure.

- [x] Run `go test ./cmd/vibermate-acceptance -run 'TestDiagnosticDaemonHome|TestDaemonEnvironmentUsesAPrivateDataScopedHome' -count=1`; require compilation success, the existing isolated test passing, and the login test failing at `environment policy mismatch` because the old forwarder changes HOME and retains CFFIXED. Invalid-config cases must fail at an expected rejection assertion. Missing symbols, compiler/import errors or fixture setup errors do not count as RED. These tests use synthetic strings/temp files and do not launch native processes.

- [x] Implement the policy helper and flag validation. For isolated return the existing helper's result. For login copy every entry except those with `CFFIXED_USER_HOME=`; retain exactly the validated original HOME. Do not filter other variables, change cache roots or add CFFIXED to the treatment. Invoke validation after flags parse and before filesystem/App input inspection. Add the diagnostic path to the existing clean absolute-path check.

- [x] Implement the recorder with a mutex, one `claimed` boolean and the following closed payload; do not add arbitrary string/map/error fields:

```go
type bootstrapDiagnostic struct {
    Schema string `json:"schema"` // fixed: vibermate.bootstrap-home-diagnostic/v1
    Policy daemonHomePolicy `json:"policy"`
    Started bool `json:"started"`
    HomeMatchesParent bool `json:"homeMatchesParent"`
    ParentCFFixedPresent bool `json:"parentCFFixedPresent"`
    ChildCFFixedPresent bool `json:"childCFFixedPresent"`
    ProgressValidated bool `json:"progressValidated"`
    SecondFrameComplete bool `json:"secondFrameComplete"`
    Outcome string `json:"outcome"`
    ElapsedMillis int64 `json:"elapsedMillis"`
    StderrBytes int `json:"stderrBytes"`
    StderrTruncated bool `json:"stderrTruncated"`
}
```

Allowed outcomes are exactly `not_started`, `start_error`, `decode_error`, `child_exit`, `context_done`, `bootstrap_deadline`, `exchange_error`, `ready`. An enum validation function must reject all other values before encoding. Schema/policy/outcome lengths are bounded by their closed vocabulary. Record no child arguments, descriptor fields, error `.Error()` values or stderr bytes. Record HOME equality and CFFIXED presence from in-memory strings without serializing them. The source identity and process exits are independently bound by Task 2/3, so the observation needs neither paths nor revision data.

- [x] In `main.go`, allocate the recorder only when the diagnostic path is nonempty; call `runAcceptance` unchanged, then `writeBootstrapDiagnostic`, joining any write failure with `runErr` before existing report-writing/error handling. No new V7 schema or check IDs. Do not use a defer that `os.Exit` would bypass.

- [x] In `startDaemon`, claim the optional recorder once (later restarts do not overwrite first-start evidence), select the helper's policy and set existing branch outcomes. Defer a locked terminal snapshot after cleanup, using `stderr.Len()` and only the overflow boolean returned by `snapshot()`; immediately discard the returned bytes. Keep fd 0/fd 3, parent writer ownership, both existing frames, 64-KiB stderr buffer and all deadlines unchanged. On success mark `ready` only after existing session exchange succeeds.

- [x] Keep `decodeDescriptor(reader)` as a compatibility wrapper around `decodeDescriptorObserved(reader, nil)`. Invoke `observed(1)` only after validated first progress; invoke `observed(2)` when a complete second newline-delimited frame is read, before schema validation. The callback takes an integer only; the decoder continues to enforce existing frame/schema/size rules.

- [x] Before implementing the writer/callback seams, add the synthetic observation/privacy tests below and run `go test ./cmd/vibermate-acceptance -run 'TestBootstrapDiagnostic' -count=1`. Require compilation and a behavioral failure: no diagnostic file was produced, or the valid progress callback was not observed. Do not introduce deliberately leaking code merely to manufacture RED. Then implement writer and callback against these tests.

- [x] Write diagnostics with `json.Marshal` plus one newline; reject `len(encoded)+1 > 4096`. Reuse the existing `privateAuditPath(filepath.Dir(path), true, 0o700) (os.FileInfo, error)` from `activity_audit.go:155` for the preexisting parent check; it rejects a final symlink, wrong type/mode and ownership. It already delegates to `requirePrivateAuditOwnership` in `activity_audit_unix.go` (UID and hard-link checks) and `activity_audit_windows.go` (explicit unsupported error). Do not invent a cross-platform ownership API or use `privateDirectory`, which creates/chmods paths and follows symlinks. Open the new file with `O_WRONLY|O_CREATE|O_EXCL`, mode 0600, write/close and propagate errors. Never overwrite an existing target or follow a final symlink. The workflow supplies a fresh private directory. Unix success tests run on macOS/Linux; Windows asserts the existing unsupported-ownership result. Keep the helper local to this command; do not create a general artifact writer.

- [x] Add synthetic privacy tests: decode progress plus a descriptor containing a sentinel nonce, finish with stderr containing a sentinel token, write observation, then assert neither sentinel nor HOME path appears, file mode is 0600, size <=4096, schema/outcome are exact. Assert existing target/symlink and unsafe parent modes fail without modifying targets. Race-test concurrent decoder observation versus snapshot; assert first-attempt claim wins only once. Example serializer check:

```go
raw, err := os.ReadFile(path)
if err != nil { t.Fatal(err) }
if len(raw) > 4096 || bytes.Contains(raw, []byte("SENTINEL_SECRET")) || bytes.Contains(raw, []byte("/Users/disposable")) {
    t.Fatal("private diagnostic contract violated")
}
info, err := os.Stat(path)
if err != nil || info.Mode().Perm() != 0o600 { t.Fatal("diagnostic permissions") }
```

- [x] Run the focused RED command again, then `go test -race ./cmd/vibermate-acceptance -run 'TestDiagnosticDaemonHome|TestBootstrapDiagnostic|TestDecodeDescriptor|TestDaemonInvocation' -count=1`. Review diff to confirm no product/deadline/report-schema changes. Commit only Task 1 files as `test: add private packaged bootstrap HOME diagnostics`.

Task 1 implementation evidence (2026-10-08): both compiling behavioral RED cycles were observed; final focused normal and race commands passed. Affected non-live acceptance/verifier/report tests passed once with `-skip 'TestDesktopApplicationGuardian|TestPackaged.*Live'`; this excludes the two JXA guardian tests and two opt-in live App tests, and is not a full package/live acceptance PASS. Original logs, final source snapshots and SHA-256 inventories are preserved under `.superpowers/sdd/2026-10-08-packaged-bootstrap-home-diagnostic/task-1-evidence/`; the sibling `task-1-report.md` records commands, terminal handles, exclusions and limits. Independent root-dispatched review remains pending; Tasks 2–3 are not authorized by this completion.

## Task 2: Check only the verifier's otherwise-unbound source identity

**Files:** Create `tool/verify-packaged-diagnostic-verifier.mjs` and `tool/verify-packaged-diagnostic-verifier.test.mjs`.

**Evidence boundary:** `collectAcceptanceProvenance` in `provenance.go:175–217` already reads Go source identity for acceptance/daemon/launcher and validates the App manifest. `runner.go:54–78` checks that and fixed-client identity before any App launch. The App manifest uses the real `SourceProvenance` fields `vcs`, `revision`, `commitTime`, `dirty` (`internal/acceptancereport/schema.go:60`); reuse its validator, do not parse it again. `cmd/vibermate-acceptance-verify/main.go` validates the report/artifacts but does not inspect its own executable's VCS identity. Only that verifier role needs this additional check. Archive digest + producer artifact ID bind all App/harness/verifier/client bytes, including framework symlinks, so no transfer schema, per-role hash map or duplicate client validator is necessary.

**Interface:** `node tool/verify-packaged-diagnostic-verifier.mjs <verifier-binary> <expected-revision>`; export `validateVerifierBuildInfo(text, expectedRevision)` for synthetic tests. CLI requires a nonsymlink regular executable, calls only `go version -m` via `execFileSync` with `timeout: 10000`, `maxBuffer: 65536`, and validates stdout. No binary execution, file writes, App/client parsing or generic manifest API. Print only a fixed success/failure label.

- [ ] Start with an importable `validateVerifierBuildInfo` that returns without validation (a compiling missing-check seam). Add the test below and missing/duplicate setting cases. Run `node --test tool/verify-packaged-diagnostic-verifier.test.mjs`; require the dirty/different-source rejection assertion to fail, not module-loading or syntax errors.

```js
test('verifier source rejects dirty or different builds', () => {
  const revision = '0'.repeat(40);
  const good = `binary: go1.26.8\n\tbuild\tvcs=git\n\tbuild\tvcs.revision=${revision}\n\tbuild\tvcs.modified=false\n`;
  assert.doesNotThrow(() => validateVerifierBuildInfo(good, revision));
  assert.throws(() => validateVerifierBuildInfo(good.replace('modified=false', 'modified=true'), revision));
  assert.throws(() => validateVerifierBuildInfo(good, '1'.repeat(40)));
});
```

- [ ] Implement this narrow parser and CLI guard; no new manifest or dependency:

```js
export function validateVerifierBuildInfo(text, expectedRevision) {
  if (!/^[0-9a-f]{40}$/.test(expectedRevision)) throw new Error('invalid source identity');
  const expected = { vcs: 'git', 'vcs.revision': expectedRevision, 'vcs.modified': 'false' };
  for (const [key, value] of Object.entries(expected)) {
    const settings = [...text.matchAll(/^\s*build\s+(vcs(?:\.revision|\.modified)?)=(\S+)\s*$/gm)]
      .filter(match => match[1] === key);
    if (settings.length !== 1 || settings[0][2] !== value) throw new Error('verifier source mismatch');
  }
}
```

- [ ] Run the Node tests to GREEN. Commit only these two files as `test: verify diagnostic verifier source identity`.

## Task 3: One producer, two fresh hosted consumers, honest outcome retention

**Files:** Create `.github/workflows/packaged-bootstrap-home-diagnostic.yml` and `.github/packaged-bootstrap-home-diagnostic-contract.test.mjs`.

**Interfaces:** Producer outputs `artifact_id`, `archive_sha256`, `revision`; one matrix consumer job uses `policy: [isolated, login]`, `fail-fast: false`, and `needs: producer`. Each matrix entry is a fresh GitHub-hosted `macos-15` job. Artifacts are named with run ID, run attempt, full revision and policy; input archive retention is seven days. No workflow-call API, arbitrary revision input or remote URL input. The only initial trigger is a root-authorized push of the reviewed frozen SHA to the exact dedicated branch `diagnostic/packaged-bootstrap-home-20261008`; ordinary candidate pushes, PRs and default-branch pushes must not trigger this experiment. This is an explicit one-off dispatch, not a scheduled or automatic release gate.

**Dispatch correction (2026-10-08):** GitHub requires a workflow to exist on the default branch before `workflow_dispatch` can run; the new diagnostic workflow is not registered there. Instead of merging untested diagnostic tooling into protected main or changing an existing release gate, use the exact dedicated-branch push trigger below. Root verified that this remote branch does not yet exist and remains the sole dispatcher after review. No force-push, default-branch mutation, release approval or additional native action is authorized. Primary source: https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow .

**Hosted-boundary correction after integrated review:** `packaged-acceptance` is an existing unprotected test environment (no approval or deployment-branch rules). The exact root-authorized dedicated branch push is the diagnostic dispatch control; do not describe the environment name as proof of protection. Signing, notarization and public-release human approval protections remain separate and unchanged. Do not reconfigure cloud environments. Before dispatch, implement the confidentiality correction in the final integration-fix section below.

- [ ] Add failing contract tests using existing `.github/packaged-acceptance-contract.test.mjs` style. Assert the exact dedicated-branch trigger with no PR, schedule, arbitrary branch or manual-input trigger, protected `packaged-acceptance` environment, exactly one App/harness/verifier build producer, matrix `fail-fast: false`, same `needs.producer.outputs.artifact_id` download, hash check before extraction, no build/npm/signing in consumers, both actual exit checks, seven-day retention and no `continue-on-error`. Pin every reused action. Also assert the diagnostic selector never appears in the existing normal packaged workflow. For the critical exit-status and archive-integrity shell steps, execute their actual extracted shell against synthetic temporary inputs: both zero exits succeed; nonzero/missing/malformed status fails; mismatched archive digest fails before extraction. Do not launch native inputs, install tooling or emulate the entire CI runner. These behavioral checks complement the workflow wiring checks rather than merely checking source spelling.

```js
test('normal gate does not opt into diagnostic behavior', () => {
  const normal = readFileSync(new URL('./workflows/packaged-acceptance.yml', import.meta.url), 'utf8');
  assert.doesNotMatch(normal, /diagnostic-daemon-home|bootstrap-diagnostic/);
});
```

- [ ] Start from a syntactically valid diagnostic workflow file with only its producer job and a harmless source-check step; then run `node --test .github/packaged-bootstrap-home-diagnostic-contract.test.mjs`. Require a contract assertion failure for the missing fresh-consumer/shared-artifact behavior; a missing file or parse error is setup failure, not RED.

- [ ] Add the workflow skeleton below. Use the existing workflow's exact pinned checkout/setup-Go/setup-Node/upload actions and the repository's pinned download action. No reusable workflow refactor:

```yaml
name: diagnostic only - packaged bootstrap HOME A/B
on:
  push:
    branches:
      - diagnostic/packaged-bootstrap-home-20261008
permissions:
  contents: read
concurrency:
  group: vibermate-packaged-bootstrap-home-diagnostic
  cancel-in-progress: false
jobs:
  producer:
    environment: packaged-acceptance
    runs-on: macos-15
    timeout-minutes: 30
    outputs:
      artifact_id: ${{ steps.upload.outputs.artifact-id }}
      archive_sha256: ${{ steps.archive.outputs.sha256 }}
      revision: ${{ steps.source.outputs.revision }}
  consumer:
    needs: producer
    environment: packaged-acceptance
    runs-on: macos-15
    timeout-minutes: 30
    strategy:
      fail-fast: false
      matrix:
        policy: [isolated, login]
```

The job `steps` are supplied by the next two explicit steps; do not leave this skeleton as the implementation. Start every shell step that creates task files with `umask 077`; initialize status files before their first append. Validate GitHub output revision/digest strings before using them as expected values.

- [ ] Implement producer steps in this order: checkout exact `${{ github.sha }}` using `11d5960a326750d5838078e36cf38b85af677262`; require clean checkout and matching HEAD; mode-0700 task root in RUNNER_TEMP; setup Go `924ae3a1cded613372ab5595356fb5720e22ba16`, Node `a0853c24544627f65ddf259abe73b1d18a591444` with Node 22.23.1; copy the normal packaged workflow's fixed Claude installation/checks verbatim; run existing pinned Flutter installer and locked CocoaPods installer; assert arm64; retain existing official Claude catalogue test; call `build_macos_app.sh live`, `verify_macos_app.sh "$DESKTOP_APP" live`, and build harness/verifier with `-buildvcs=true -trimpath`. Set `DEVELOPER_DIR=/Applications/Xcode_16.2.app/Contents/Developer` exactly. Record each build/check's actual exit through the following local shell pattern and fail immediately after recording a nonzero result:

```bash
set +e
ui/flutter_app/tool/build_macos_app.sh live
build_exit=$?
set -e
printf 'app_build=%d\n' "$build_exit" >> "$PRODUCER_EXITS"
test "$build_exit" -eq 0
```

Use literal labels `client_catalog`, `app_build`, `app_verify`, `harness_build`, `verifier_build`, `verifier_source`, `archive`; append only completed numeric statuses and explicitly define absent as not-run. A failing command's status must be written before shell exit. Do not fabricate an exit for skipped steps. An `always()` upload retains this small producer-status file even when no archive exists.

- [ ] Stage payload outside checkout using existing `ditto --norsrc --noextattr --noacl --noqtn -X` convention for the App/client and `install -m 0755` for harness/verifier. Payload contains exactly `ViberMate.app`, `vibermate-acceptance`, `vibermate-acceptance-verify`, and `client` (the npm prefix with package metadata/lock and native executable, excluding its separate npm cache). Verify the staged App again and run Task 2's verifier-source check against the producer revision. Archive only this payload root with `/usr/bin/tar -czf "$INPUT_ARCHIVE" -C "$PAYLOAD_ROOT" .` and `COPYFILE_DISABLE=1`; record SHA-256 via `shasum -a 256`, export only the validated 64-hex digest as the job output. Archive preserves executable modes and framework symlinks. Upload archive using `actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02`, `if-no-files-found: error`; the returned artifact ID is the sole download selector. Producer does not run the App or acceptance: no Keychain state is transferred.

- [ ] Each consumer checks out the same clean `${{ github.sha }}`, verifies it equals producer revision, installs the same Go/Node/pinned Flutter runtime tooling (Flutter is required by existing provenance collection), and sets the same DEVELOPER_DIR. It does not install CocoaPods or rebuild product inputs. Create fresh 0700 download/extract/evidence directories under its own RUNNER_TEMP. Download the producer artifact by `artifact-ids` using `actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093`; require exactly the named archive as a nonsymlink regular file. Hash against `needs.producer.outputs.archive_sha256` before extraction; reject any mismatch. Extract only that authenticated same-run archive into an empty private directory. Run `node tool/verify-packaged-diagnostic-verifier.mjs "$PAYLOAD_ROOT/vibermate-acceptance-verify" "$EXPECTED_REVISION"` and existing `verify_macos_app.sh ... live` before any App launch. Full acceptance then performs the existing source/native-profile/fixed-client checks before launching either App. Never chmod/re-sign/repair transferred executables after verification.

- [ ] Run full acceptance with original environment unchanged in the consumer shell; only pass the policy as the acceptance flag. Both consumers use the same canonical payload-relative paths, `--deterministic-only`, `--timeout 10m` and distinct fresh report/observation files. Capture harness status without short-circuiting verifier or overwriting it:

```bash
set +e
"$PAYLOAD_ROOT/vibermate-acceptance" \
  --desktop-app "$PAYLOAD_ROOT/ViberMate.app" \
  --claude "$PAYLOAD_ROOT/client/node_modules/@anthropic-ai/claude-code-darwin-arm64/claude" \
  --deterministic-only --timeout 10m \
  --report "$EVIDENCE_ROOT/deterministic-v7.json" \
  --diagnostic-daemon-home "$DIAGNOSTIC_POLICY" \
  --bootstrap-diagnostic "$EVIDENCE_ROOT/bootstrap.json"
harness_exit=$?
set -e
printf 'harness=%d\n' "$harness_exit" >> "$EVIDENCE_ROOT/exits.txt"
```

Pass `DIAGNOSTIC_POLICY` from the closed matrix via `env`, never shell interpolation of untrusted input. This capture step deliberately completes so the subsequent verifier and upload run; it is followed by a mandatory final failure step below.

- [ ] Run the transferred verifier in a separate `if: always()` step after harness invocation (gate on successful transfer verification, not harness status). Capture the real exit using the same pattern, write `verifier=<numeric>` to `exits.txt`, and pass every existing normal verifier argument: report; mode deterministic; schema `vibermate.m0-assembly-acceptance/v7`; expected producer revision; Claude client ID/version `claude-code`/`2.1.220`; clean source root; transferred App/harness/client paths. Missing/failed report must produce a verifier failure, not an expected-success override. Validate observation mode/size and retain only `bootstrap.json`, `deterministic-v7.json`, `exits.txt`, plus a small identity file containing producer revision, artifact ID and archive digest. No raw logs or private runtime data uploaded.

- [ ] Add `always()` evidence upload using the pinned upload action, seven-day retention and policy-specific name. Then final outcome step reads the two numeric exits, rejects missing/not-run values and exits nonzero if either is nonzero:

```bash
test "$harness_exit" -eq 0
test "$verifier_exit" -eq 0
```

Read these variables from the closed status file in this final step; do not rely on shell state across steps. This intentionally leaves expected baseline timeout as a failed matrix job while `fail-fast: false` allows treatment to complete. Workflow summaries use “diagnostic only” and actual numbers, never “expected failure passed.” A producer failure leaves consumers skipped and producer failure preserved. Cleanup runs `always()`, validates exact RUNNER_TEMP-owned roots, then removes only those roots; do not touch login HOME/Keychain.

- [ ] Run `node --test .github/packaged-bootstrap-home-diagnostic-contract.test.mjs .github/packaged-acceptance-contract.test.mjs tool/verify-packaged-diagnostic-verifier.test.mjs`; run `actionlint .github/workflows/packaged-bootstrap-home-diagnostic.yml`; rerun focused Task 1 tests only if Task 3 changed harness code. Check `git diff --check` and inspect final diff against the file map. Commit Task 3 as `ci: add isolated packaged bootstrap HOME experiment`.

## Review and hosted execution handoff

- [ ] Root reviews the three commits, exact artifact/exit chain and static/unit evidence before authorizing dispatch. Do not dispatch this plan automatically.
- [ ] Once authorized, dispatch the dedicated workflow exactly once by pushing its reviewed clean final revision to the previously absent exact dedicated branch. Record producer/consumer job IDs, input artifact ID/digest, each harness and verifier exit, both observation files, and both actual V7 reports. Verify both consumer identity files agree with producer outputs before interpreting A/B. Existing generic validation may also run on that new branch; retain one current-source run and cancel only verified duplicate validation runs, never the experiment or a failed run presented as success.
- [ ] Interpret without overclaiming: isolated reproduces + login bootstrap succeeds supports the environment policy as an operational trigger; it does not identify a specific native call. If parent CFFIXED was absent, treatment effectively changed only HOME. Both succeed means nonreproduction; both fail means policy alone did not resolve the reproduced failure. A treatment bootstrap success followed by later failed V7 is only bootstrap success.
- [ ] No normal/release gate is replaced by this diagnostic run. Any behavioral correction requires the measured results, separate review and ordinary current-source gate evidence. Product stage hooks remain out of scope unless the A/B remains inconclusive and root authorizes a narrower follow-up.

## Plan self-review

The plan covers default compatibility, treatment environment, capability privacy, existing deadlines, full V7, exact common artifacts, fresh machines, independent source/hash checks, all real exits, negative baseline status and unchanged release gates. The helper interfaces and CLI names are consistent across tasks. No test/build/network mutation was run while writing this plan; only source/skill reads and this plan file were performed. The existing App exit timeout is 3 seconds and remains untouched; “15-second waits” refers to existing acceptance waits, not a proposed change to App shutdown.

## Final integration fix: actual hosted confidentiality and truthful dispatch controls

The integrated review found two real hosted-boundary gaps; do not dispatch before the following one fix wave and scoped re-review. This section supersedes earlier Task 3 wording that calls the test environment or plaintext consumer artifacts protected/private.

**Owned files:** workflow and its contract test; create `tool/seal-packaged-bootstrap-evidence.mjs` and `.test.mjs`; include controller-generated public-only `tool/packaged-bootstrap-diagnostic-recipient.pem`. Root owns the plan and the ignored private recipient key. No product, V7, native code, normal workflow, environment configuration or release protection changes.

The controller generates a fresh RSA-3072 key pair locally, retaining the private key mode 0600 in this plan's ignored evidence workspace and committing only the SPKI public key. Its only purpose is to receive this experiment's diagnostic evidence. No CA trust, account credential or signing identity is created or installed.

Use Node built-in crypto for a small authenticated envelope: random 32-byte AES key, random 12-byte nonce, AES-256-GCM with the fixed schema string as AAD, and RSA-OAEP with SHA-256 wrapping the AES key. Closed envelope fields are `schema`, `wrappedKey`, `nonce`, `tag`, `ciphertext`; schema is exactly `vibermate.private-bootstrap-evidence/v1`, binary fields are base64. Only an RSA public key of at least 3072 bits is admitted. No private key or arbitrary metadata is serialized. Export `sealDiagnosticEvidence(plaintext, publicKey)` solely for focused synthetic testing; CLI is `node tool/seal-packaged-bootstrap-evidence.mjs <plaintext-archive> <encrypted-output> <public-key>`. Validate regular nonsymlink input/public-key files, bound plaintext archive to 1 MiB and PEM to 16 KiB, create output exclusively with mode 0600, print fixed success/failure text and never print raw crypto/filesystem errors. This is one bounded diagnostic envelope, not a general backup/encryption framework.

- [ ] Add behavioral RED with a no-op forwarding seam: real generated ephemeral test key pair, encrypted output differs from plaintext and excludes a sentinel, decrypting with the intended private key restores exact bytes. Missing crypto behavior must fail an assertion, not parsing/import setup. Add tamper/wrong-recipient rejection and invalid key/size/CLI overwrite cases; tests must exercise real crypto and filesystem boundaries.
- [ ] Implement the envelope and prove GREEN with synthetic input only. Never use the retained real private recipient key in tests or output its bytes.
- [ ] Keep the existing four-file local consumer staging allowlist and private modes. Archive only that staging directory to a task-owned plaintext archive outside the upload target, invoke the sealer with the committed public recipient, and upload only the single encrypted output path. Never upload the staging directory or plaintext archive, including on failure. A seal failure must leave the step/job failed; the subsequent final actual-exit gate remains unchanged and cannot erase earlier failure.
- [ ] Name producer artifacts as non-confidential diagnostic inputs/status, and consumer artifacts as encrypted diagnostic evidence. Correct misleading environment/test names; retain the named environment and exact dedicated branch trigger without adding cloud configuration or human approval requirements to this already-authorized synthetic diagnostic.
- [ ] Extend actual extracted shell tests so a synthetic staged fixture yields only the encrypted upload file and a failed sealer cannot yield an uploadable plaintext artifact. Keep all previous archive/exit behavior tests and original raw failure logs. Run the named combined Node tests plus new crypto tests, actionlint and diff checks once on final code. No local native launch or cloud action.
- [ ] Root reads the report and obtains the one scoped integration re-review for the two findings and new fix diff. Only then may it push the exact reviewed SHA to the dedicated branch. Downloaded consumer artifacts must be decrypted locally with the retained key, authenticated before parsing/extraction, and their source/artifact identities checked before interpretation. Keep both encrypted and decrypted diagnostic evidence locally; do not print private key material.

## Hosted build correction: contain private umask outside public App build

The first actual producer run37801469020 at b971248 built the App but failed the unchanged verifier's closed executable-layout check: only vibermate/vibermated (explicitly chmod755 by the existing builder) were listed, not the GUI executable. The existing builder already required the GUI executable to be executable and generated its binary manifest; the verifier selects files with all three execute bits (`find -perm -111`). Unlike the previously successful normal workflow, the new producer invoked the entire build with inherited umask077. No consumer ran, so this is an experiment-setup failure, not evidence about HOME behavior.

Ruling: keep private status/evidence creation under077, but run the unchanged public-source App builder in a subshell with022, matching ordinary distributable-bundle permissions; do not chmod or repair a built/transferred bundle and do not weaken the verifier. This supersedes the overly broad instruction to carry077 into every child build. Cost if wrong: another visible producer failure; no bootstrap/native/release success is inferred.

Owned fix files: only `.github/workflows/packaged-bootstrap-home-diagnostic.yml` and its contract test. Replace only the App build invocation with `(umask 022; ui/flutter_app/tool/build_macos_app.sh live)` while retaining the existing real exit capture in the parent077 shell. Add a behavioral test executing that exact invocation against a controlled synthetic build probe: builder observes0022, creates0755-style executable permission, parent still observes0077 and creates0600 status output. The original invocation under077 must fail this observable assertion before the fix. This does not run Flutter/Xcode or native App code and does not assert only source spelling. Run the existing combined20 tests plus the added case and actionlint, preserve raw first failure, commit/review only this small range. No module/product/normal gate/encryption/privacy change. Root alone may explicitly start a new measured experiment on the corrected frozen SHA after review; do not rerun the same failed SHA blindly.
