# Long-session Hotfix Release Preparation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the independently reviewed long-session hotfix through exact-source packaged validation, signing, notarization, GitHub and Homebrew.

**Architecture:** Keep the existing V7 acceptance assembly/verifier and protected macOS candidate chain. Correct their concrete toolchain/runner/version inputs; do not build an alternate harness or import unfinished product features. Complete all product work before final version/source freeze, and bind every final artifact/report to that source.

**Tech Stack:** Go1.26.8, Node22.23.1, Flutter3.41.5 at2c9eb20739dfec95e2c74bd3dfa4601b0a8a36aa, Dart3.11.3, macOS15 ARM64, Xcode16.2/build16C5032a/SDK15.2, fixed Claude2.1.220.

**Spec:** `docs/superpowers/specs/2026-10-06-long-session-hotfix-design.md`. Prerequisite product plan: `2026-10-06-long-session-coupled-admission.md`, including its actual Runtime/client/resource/data gates and independent reviews. This release is an intermediate delivery; the full production-readiness Goal remains active afterward.

## Global Constraints

- Preparation plan only until the current Store owner and coupled product tasks complete. One source implementer and one compiler/test job at a time; fresh task reviews and whole-hotfix review remain required. Reuse completed evidence only for its actual unchanged scope.
- No MCP/schema2/global-egress import, live-user database/accounts/Captures/trust/system-proxy change or automatic App restart. Use isolated synthetic test data and disposable hosted runner state.
- Preserve schemaV7, manifestV3, all deterministic acceptance checks/artifact roles, exact native-secret build tags, independent revision/client/artifact expectations and missing/failed-evidence rejection. Installed smoke does not replace V7 or long-session acceptance.
- Do not change environment reviewers/branch protections or bypass approval. On2026-10-07, GitHub GETs showed registered self-hosted runners0; macos-signing/macos-notarization/public-release retained required reviewer885569, protected branches and can_admins_bypass=false. Recheck at release, not as a substitute for the actual protected runs.
- Final builds require a clean genuine checkout with a `.git` directory; linked-worktree VCS stamping is not accepted as provenance. Verify actual binary VCS revision and vcs.modified=false.
- Candidate version is not allocated yet. The concrete next-version proposal is0.1.24+26 only if fresh remote main/tag/release/tap checks still permit it; otherwise root records the new exact identity before Task3 runs. No stale reports or overwritten published assets.
- Preserve original failures, source hashes, commands, sessions/exits, private artifact producers/digests and unfinished boundaries. Missing resources or rejected tools fail a gate; they do not turn into skipped success.

## Task interfaces and file map

Task1 changes only the common exact Go pin and its positive/negative fixtures. Task2 changes hosted inputs and workflow regression coverage, leaving V7 implementation intact. Task3 updates one coherent release identity. Final gate execution consumes the committed reviewed result of all three plus completed coupled product work; no subsequent source edit may borrow that result's provenance.

### Task 1: Align the exact V7 Go contract

**Files:** Modify `internal/acceptancereport/schema.go`; test `internal/acceptancereport/verifier_test.go`, `cmd/vibermate-acceptance/provenance_test.go`, `cmd/vibermate-acceptance-verify/main_test.go`. Update positive toolchain fixtures in `ui/flutter_app/tool/desktop_build_manifest.test.mjs` and `tool/macos-release/macos-distribution-policy.test.mjs` where they describe the selected compiler.

**Interfaces:** Preserve `VerifyFile`, `Expectations`, schemaV7 and all host/acceptance/daemon/launcher checks. `ExpectedGoVersion` is the single authoritative literal, never inferred from a submitted report.

- [ ] Add the exact module/pin regression before changing production:

```go
func TestPinnedGoToolchainMatchesModule(t *testing.T) {
    content, err := os.ReadFile("../../go.mod")
    if err != nil { t.Fatal(err) }
    if ExpectedGoVersion != "go1.26.8" || !strings.Contains(string(content), "\ntoolchain "+ExpectedGoVersion+"\n") {
        t.Fatalf("acceptance pin %q disagrees with module", ExpectedGoVersion)
    }
}
```

- [ ] Change the named known-good harness/CLI/verifier fixtures' host string to literal `go version go1.26.8 darwin/arm64`. Extend existing drift tables to reject host and each binary's1.25.13,1.26.0,1.26.9 values. Keep the existing runtime/build/Flutter/tag mismatch controls.
- [ ] Record actual RED with `go test -count=1 -run '^TestPinnedGoToolchainMatchesModule$' ./internal/acceptancereport` and `go test -count=1 -run '^TestToolchainValidationRequiresPinnedBuildAndHostVersions$' ./cmd/vibermate-acceptance`. A compiler/setup failure is not this RED.
- [ ] Set only the production literal `ExpectedGoVersion = "go1.26.8"`; do not admit a version range. Run `go test -count=1 ./internal/acceptancereport ./cmd/vibermate-acceptance ./cmd/vibermate-acceptance-verify` and the affected Node fixture files. Commit owned passing paths, preserve logs/hashes and obtain independent spec/quality review.

### Task 2: Supply disposable hosted inputs to unchanged packaged V7

**Files:** Modify `.github/workflows/packaged-acceptance.yml`, `.github/workflows/ci.yml`, `Makefile`; create `.github/packaged-acceptance-contract.test.mjs`. Do not modify the acceptance runner/verifier or native SecretStore to accommodate missing hosted capabilities.

**Interfaces:** Retain DESKTOP_APP, ACCEPTANCE_ROOT, ACCEPTANCE_BIN, VERIFY_BIN, REPORT_PATH, FIXED_CLAUDE_PATH and independent GITHUB_SHA. Fixed client is an isolated native platform package under ACCEPTANCE_ROOT, not a machine-global variable/install.

- [ ] Add this regression and extend its required list to all existing build/verification/report/cleanup gates before editing the workflow:

```javascript
import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';
test('packaged V7 retains exact gates on disposable inputs', () => {
  const w = readFileSync(new URL('./workflows/packaged-acceptance.yml', import.meta.url), 'utf8');
  assert.match(w, /runs-on: macos-15/u);
  assert.doesNotMatch(w, /self-hosted|VIBERMATE_CLAUDE_2_1_220_PATH|continue-on-error/u);
  for (const value of ['environment: packaged-acceptance', '--expected-mode deterministic', '--expected-schema vibermate.m0-assembly-acceptance/v7', '--expected-revision "${GITHUB_SHA}"', '--expected-client-id claude-code', '--expected-client-version 2.1.220', '--source-root "${GITHUB_WORKSPACE}"', '--desktop-app "${DESKTOP_APP}"', '--acceptance-executable "${ACCEPTANCE_BIN}"', '--client-entrypoint "${FIXED_CLAUDE_PATH}"', 'build_macos_app.sh live', 'verify_macos_app.sh "${DESKTOP_APP}" live', 'if-no-files-found: error']) assert.ok(w.includes(value), value);
});
test('workflow checks include hosted contract and native release code', () => {
  const ci = readFileSync(new URL('./workflows/ci.yml', import.meta.url), 'utf8');
  const make = readFileSync(new URL('../Makefile', import.meta.url), 'utf8');
  const check = 'node --test .github/packaged-acceptance-contract.test.mjs';
  assert.ok(ci.includes(check));
  assert.ok(make.includes(check));
  assert.ok(ci.includes('make check-release-build'));
  assert.ok(ci.includes('govulncheck@v1.6.0 -tags vibermate_native_secrets ./...'));
});
```

- [ ] Run `node --test .github/packaged-acceptance-contract.test.mjs` and retain the current self-hosted-input RED. Then set `runs-on: macos-15`, retain packaged-acceptance environment and exact DEVELOPER_DIR, remove the machine-variable client dependency, and create the private0700 acceptance root before installation.
- [ ] Install with the existing pinned Node and per-command isolated cache:

```bash
npm install --ignore-scripts --no-audit --no-fund --prefix "${ACCEPTANCE_ROOT}/client" --cache "${ACCEPTANCE_ROOT}/npm-cache" @anthropic-ai/claude-code-darwin-arm64@2.1.220
fixed_claude="${ACCEPTANCE_ROOT}/client/node_modules/@anthropic-ai/claude-code-darwin-arm64/claude"
test -f "${fixed_claude}" && test ! -L "${fixed_claude}" && test -x "${fixed_claude}"
test "$(shasum -a 256 "${fixed_claude}" | awk '{print $1}')" = 8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081
printf 'FIXED_CLAUDE_PATH=%s\n' "${fixed_claude}" >> "${GITHUB_ENV}"
```

- [ ] Check installed package version2.1.220 and package-lock entry `packages["node_modules/@anthropic-ai/claude-code-darwin-arm64"].integrity` equals `sha512-rmtd41Bf+n+YnhjSjtQ8WG5qy8KKogUp3YRfQrkLsTgPUD0H3j869rBInBJT3SHrKQ0hLghQLGM73CC1C+USLQ==` before executing the client. Run existing native-catalog check with nonempty verified `VIBERMATE_OFFICIAL_CLAUDE` and `go test -count=1 -run '^TestOfficialInstalledClientPackagesMatchTheBuiltInCatalog$/^Claude_Code$' ./internal/clientadapter`; fail rather than permit an optional-client skip.
- [ ] Assert actual arm64/macOS15/build24, Xcode16.2/build16C5032a, SDK15.2, Go1.26.8, Node22.23.1, Flutter3.41.5/exact revision/Dart3.11.3, clean checked-out GITHUB_SHA, and retain the locked CocoaPods installer. Missing GUI/JXA/unlocked login Keychain or tool admission remains a failing prerequisite. No local-Xcode substitution or fake provenance.
- [ ] Keep `--deterministic-only --timeout 10m`, fresh source-bound harness/verifier builds, always-run verification after successful build, all four artifact-coordinate arguments, private report retention7days and exact-root cleanup on failure. Preserve existing action pins: checkout11d5960a326750d5838078e36cf38b85af677262; setup-go924ae3a1cded613372ab5595356fb5720e22ba16; setup-nodea0853c24544627f65ddf259abe73b1d18a591444; upload-artifactea165f8d65b6e75b540449e92b4886f43607fa02.
- [ ] Add the Node regression to Makefile check-workflows and CI generated-structural. Add one macos-15 native-build CI job using those checkout/setup-go pins and DEVELOPER_DIR, running existing `make check-release-build` and `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 -tags vibermate_native_secrets ./...`. Do not substitute untagged coverage for native release paths.

```yaml
  native-release-build:
    needs: generated-structural
    runs-on: macos-15
    env:
      DEVELOPER_DIR: /Applications/Xcode_16.2.app/Contents/Developer
      GOFLAGS: -mod=readonly
    steps:
      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262
      - uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16
        with:
          go-version-file: go.mod
          check-latest: false
          cache: true
      - run: make check-release-build
      - run: go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 -tags vibermate_native_secrets ./...
```

- [ ] Run `node --test .github/packaged-acceptance-contract.test.mjs` and `make check-workflows check-release-tooling`; preserve exact results, commit owned paths and independently review. The actual hosted native-build/V7 runs remain final gates; local workflow/unit-test success is preparation, not actual hosted acceptance.

### Task 3: Freeze one coherent release identity

**Files:** Modify `ui/flutter_app/pubspec.yaml`, `tool/macos-release/macos-distribution-policy.mjs`, `.github/workflows/macos-developer-id-candidate.yml`; affected Node fixtures in `tool/macos-release/`; update current-version capability copy and add the exact new release note under `docs/releases/`. Preserve historical release notes.

**Interfaces:** pubspec version/build and policy appVersion/appBuildNumber/diskImageFilename must agree with three candidate-workflow DMG literals, Go product-version injection, R0/SBOM and final binaries. Product capabilities/rollback claims derive from actual hotfix acceptance, not future full-Goal features.

- [ ] After coupled product tasks and Tasks1–2 review close, GET remote main, latest release, matching proposed tag and actual tap cask. For the currently proposed0.1.24+26, run `gh api repos/vibe-agi/vibermate/releases/latest` and `gh api repos/vibe-agi/vibermate/git/matching-refs/tags/v0.1.24` with inherited token overrides unset. Confirm App build monotonicity against remote main. If occupied or stale, root chooses and records a new exact version/build before test literals are written; do not overwrite a release.
- [ ] First add a literal frozen-identity test (use root's verified exact identity; current proposal shown):

```javascript
// Add readFileSync from node:fs to the existing test imports.
test('frozen release identity is coherent', () => {
  assert.equal(macOSDistributionPolicy.appVersion, '0.1.24');
  assert.equal(macOSDistributionPolicy.appBuildNumber, '26');
  assert.equal(macOSDistributionPolicy.diskImageFilename, 'ViberMate_0.1.24_universal.dmg');
  const pubspec = readFileSync(new URL('../../ui/flutter_app/pubspec.yaml', import.meta.url), 'utf8');
  const workflow = readFileSync(new URL('../../.github/workflows/macos-developer-id-candidate.yml', import.meta.url), 'utf8');
  assert.match(pubspec, /^version: 0\.1\.24\+26$/mu);
  assert.equal([...workflow.matchAll(/ViberMate_0\.1\.24_universal\.dmg/gu)].length, 3);
  assert.doesNotMatch(workflow, /ViberMate_0\.1\.23_universal\.dmg/u);
});
```

- [ ] Also check pubspec and all three workflow transfer paths against the same literal, retain mismatch tests, and record RED with `node --test tool/macos-release/macos-distribution-policy.test.mjs`. Then update policy/pubspec/three paths/positive fixtures together. Do not mechanically replace historical-data versions.
- [ ] Write hotfix notes only for proven behavior: complete long-session continuation, actual supported clients, retained data and explicit old-binary/backup restore limits. Include stop-active-Captures/backup/upgrade guidance; do not claim MCP/default-egress/terminal-display fixes from the frozen development branch.
- [ ] Run `make check-release-tooling check-workflows`, review source/version consistency, commit owned GREEN paths and independently review. Root records one full40hex final SHA, version/build and exact prerequisite evidence. Any later source edit invalidates that freeze.

## Final source gates and authorized delivery

These steps consume the completed coupled plan and reviewed Tasks1–3; they do not replace product implementation with tooling success.

- [ ] From a clean genuine checkout of the frozen SHA, inspect `go version -m` and product-version output of actual binaries. Run all applicable final CI jobs: generation/module/format/repository/workflow checks, complete Go tests/vet/race-short plus named full-size affected races/contracts, untagged/tagged vulnerability/native-secret/cross builds, locked Paseo, Flutter/analyze/tests/full Chrome/native desktop and Linux distribution gates. Retain exact source/results and no skipped required gate.
- [ ] Complete exact-source official Codex same-native-session long-history continuation, final-default4111/longer/full-tail/scripts/transforms/original-managed credentials, independent response-envelope/2-and4-active-plus-waiting/control deadlines and safe old-schema backup/upgrade/reopen/restore evidence required by the coupled plan. The later full-Goal46MCP matrix and60minute new-feature soak are not claimed by this scoped release.
- [ ] Run final whole-hotfix independent review over the complete stable-base range, including deferred minors/rulings and actual capacity/data/format proofs. Fix release-blocking findings and re-verify affected/final-source gates; never waive them merely to ship.
- [ ] Integrate only the reviewed hotfix through protected main. Bind all final evidence to the actual merged candidate SHA; if integration changes it, verify that actual source and produce corresponding fresh binaries/reports. Dispatch packaged-acceptance from that exact committed source and independently verify its V7 report against independently supplied source/App/harness/client coordinates. Retain private report before7day expiry.
- [ ] Recheck existing approval protections. Dispatch macos-developer-id-candidate from main with `candidate_revision` equal to the exact admitted merged SHA. Observe the actual unsigned→R0/SBOM→sign→notarize→installed-evidence chain and required approvals; no self-bypass or protection change. Preserve producer-run/artifact IDs/digests, Apple transformation/staple verification and installed report/cleanup. Save3day/7day private evidence promptly.
- [ ] Recheck version/tag availability immediately before publishing. Create the release/tag at the verified merged SHA and publish only the verified DMG, macOS SPDX SBOM and checksums. The candidate workflow does not publish. Use the existing linux-release workflow with the exact release_tag only after that Release exists; it checks tag/pubspec/HEAD, builds/verifies archives and uploads without clobbering assets.
- [ ] Independently download public macOS/Linux assets; verify hashes, actual version/build/revision, signature/Gatekeeper/notary ticket and relevant package metadata. Distinguish Linux ARM64 archive verification from actual execution if the runner is x86-64.
- [ ] Update the Homebrew tap's ViberMate cask only after verified publication, using the final post-staple DMG SHA. Preserve unrelated tap edits and its PR/CI/audit/install/uninstall requirements. Independently query actual brew metadata and download/hash. Do not install over the user's running App.
- [ ] Report release URL, exact version/build/source, tap commit/checksum, what this hotfix proves and remaining full-Goal work. Keep the full Goal active until its separate complete scope is genuinely finished.

## Self-review and execution boundary

All seven hotfix acceptance/delivery requirements map to coupled product Tasks1–7 plus these exact-source/release steps. Toolchain, hosted runner and version corrections each have their own RED/GREEN and review boundary; none changes product authorization or weakens a gate. Version availability and hosted/Apple resources are checked at execution, not assumed from old successful releases. Subagent-driven execution is already the user's selected mode; no new general permission question is needed. Source implementation of this release plan has not started.
