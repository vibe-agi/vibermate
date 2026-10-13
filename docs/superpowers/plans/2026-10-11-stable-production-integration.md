# Stable Fixes and Production Source Integration Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development for source resolution and independent review. Use resolving-merge-conflicts if Git reports conflicts. Root owns Git checkpoint/merge operations; one source/local-test owner at a time.

**Goal:** Integrate the already released long-session and login fixes with the reviewed VLESS/UDP/global-egress production work without dropping either line's behavior or preserved data.

**Architecture:** Freeze and checkpoint the current exact source, then perform a normal local Git merge of the released-equivalent stable lineage. Resolve only actual conflicts, first obtain a compiling combined tree, then validate the shared functional paths on that combined source. Do not redesign subsystems or repeat old artifact production to avoid resolving a conflict.

**Tech Stack:** Existing Git, qualified Go1.26.9, pinned Flutter3.41.5/Node22.23.1, existing regression/acceptance tests and signed release tooling.

**Spec:** Approved full production Goal item9 and `2026-10-09-production-continuation.md`; source behavior from the referenced long-session/login release plans, global-default design and `../specs/2026-10-09-vless-udp-functional-design.md`. This is the already required conscious integration, not a new feature approval or a claim of final production readiness.

## Global Constraints

- Preserve both completed lines and all uncommitted work. No reset --hard, checkout-overwrite, stash/clean, silent ours/theirs whole-file selection, data reset, account/trust changes or user App/9666 restart.
- Current production branch is `fix/production-readiness-20261003`; existing stable local commit `999d7b0876b1c9c570e1d2192f9cc7e5a97805ee` has product tree `89be12a87e5c5958e0686987e60803e00867c0d9`, matching the independently verified published0.1.25 source. Public merge object4e89f628 is not locally available; its exact public fetch failed CONNECT407. Do not change user proxy/credentials or invent that object. The local stable lineage is an available actual integration input, not a new release or altered public tag.
- One source/local-test owner. Handwritten conflict resolutions use apply_patch. Root controls staging/commits/merge; implementers preserve before/during/after evidence and never push/publish/sign or approve a protected environment.
- Before changing Git source state, verify the index is empty and freeze the source list/snapshots. Stage only explicit reviewed Goal source/test/docs paths, excluding generated `tool/vless-kernel/__pycache__/` and all ignored evidence/build/data. Preserve excluded files; do not delete them.
- Preserve complete history/long-session resource behavior, web same-tab restore, canonical multi-server login/default ports, original-account OAuth identity/settings, global egress precedence/no-Direct-fallback, VLESS secret/TLS/domain policy, owned SOCKS authentication/UDP lifecycle and reviewed packet portability.
- Stable Go/security pins must survive together with current fixed-kernel producer requirements; no unrelated dependency upgrade. Keep current four-helper package inventories, source/license materials, protected signature/notary/install requirements and16tickets. Do not choose a final version or overwrite released tags in this plan.
- Prior tests prove their exact prior source only. Combined-source regression is required; compile-only, short or skipped runs must not be called complete production acceptance. Full original Goal, native/platform/client/data/soak and final signed/public qualification remain open afterward.

## Task 1: Root-owned source checkpoint and actual merge boundary

**Files:** Exact generated manifest of current tracked modifications and untracked Goal source; the recorded Python cache is excluded. Evidence under this plan's ignored `.superpowers/sdd/2026-10-11-stable-production-integration/`. No product source edits. A concrete staged-check observation requires `.gitattributes` entries only for the two byte-locked `tool/vless-kernel/dependency.patch` and `upstream.commit` materials: `-text -whitespace` prevents normalization and recognizes their required patch/Git-object whitespace, while all other source checking remains unchanged. Verify both staged/worktree SHA values remain those fixed by the reviewed producer; never trim these materials to satisfy a text-format check.

**Produces:** `checkpoint-files.txt`, private current snapshot path with hashes, exact checkpoint commit, actual merge output, and (if any) `conflicted-paths.txt` plus Git index stage entries. These generated artifacts are Task2's exact scope, not a guessed conflict list.

- [x] **Step 1 — verify/freeze the actual inputs.** Confirm no product writer/test process remains, `git diff --cached --name-only` is empty, current branch/HEAD, stable commit/tree and the reviewed task after-snapshots. Retain a new private `mktemp -d` snapshot containing HEAD, binary tracked patch, explicit untracked source archive and hashes. The earlier snapshot `/private/tmp/vibermate-integration-snapshot.lNiS4P` remains historical and must not be overwritten. Inspect the exact source list; unknown/unrelated paths are preserved and excluded, not silently committed.

```sh
git status --short
git diff --cached --name-only
git rev-parse HEAD
git rev-parse 999d7b0876b1c9c570e1d2192f9cc7e5a97805ee^{tree}
git diff --name-only HEAD
git ls-files --others --exclude-standard
```

- [x] **Step 2 — checkpoint explicitly selected source.** Stage using `git add --pathspec-from-file=.superpowers/sdd/2026-10-11-stable-production-integration/checkpoint-files.txt`, inspect staged names/stat and `git diff --cached --check`, and commit the exact preserved work as a local implementation checkpoint. This commit is not a publish/production qualification claim. Record full commit/tree and remaining untracked cache; do not include that cache or ignored evidence.

- [x] **Step 3 — perform normal merge and retain its exact result.** Root runs the command below once; a conflict exit is an expected source-resolution handoff, not a reason to restart or overwrite source. Record all actual conflicted paths and stages. Do not abort or retry the merge just because resolution takes time.

```sh
git merge --no-ff --no-commit --no-edit 999d7b0876b1c9c570e1d2192f9cc7e5a97805ee
git diff --name-only --diff-filter=U
git ls-files --unmerged
git status --short
```

- [x] **Step 4 — hand off actual evidence.** If conflicts exist, give Task2 the exact path/stage manifest and the checkpoint/stable revisions. If none, Task2 still verifies combined semantics and compilation. No merge commit before independent source review and the applicable checks below.

## Task 2: Resolve the combined source and validate affected behavior

**Files:** Actual conflict manifest from Task1 plus narrowly demonstrated compile/contract consumers. No unrelated refactor or blanket version replacement. Preserve the pre-resolution conflict state and derive a review diff against both parents, with the resolved hunks separately identified.

**Consumes:** The complete published stable lineage, current reviewed checkpoint and exact Git merge stages. **Produces:** A resolved compiling tree, functional regression evidence, before/after hashes, all remaining qualification limits and a source-resolution report for independent review.

- [x] **Step 1 — resolve from evidence.** Read each conflicted function with its base, current and stable alternatives. Preserve each side's required behavior. Use the actual versions, fixtures and consumer contracts named in the two parent changes; do not copy one full side over the other merely to compile. Source edits use apply_patch. Record the rationale per conflict in the report. Preserve observed compiler failures as integration evidence, not a reason to invent new product behavior.

```sh
git show :1:<conflicted-path>
git show :2:<conflicted-path>
git show :3:<conflicted-path>
```

These Git stage commands consume each actual path from `conflicted-paths.txt`; they are not literal filenames. If an add/delete case lacks a stage, record that absence and compare its real parent path.

- [x] **Step 2 — check combined compilation before heavy testing.** Worker checks the resolved working files with qualified `go test ./... -run '^$'` and Flutter analysis in the pinned environment, retaining original unmerged index stages for the three-way review. After the worker freezes its report, root verifies/stages only the exact resolutions and demonstrated consumer fixes. Compilation/analysis is not full test acceptance. Resolve only actual errors caused or exposed by this merge and report any significant expansion before editing additional files.

```sh
/tmp/vibermate-go-security.JcRyfL/go/bin/go test ./... -run '^$'
/opt/homebrew/bin/flutter analyze --no-pub
```

The Flutter command runs from `ui/flutter_app`; Go runs at the worktree root. Do not run them concurrently.

- [x] **Step 3 — run focused combined regressions.** Sequentially run the existing Go long-session/storage/runtime tests and login/grant/network consumers. Use the exact already packaged unchanged helper for the native end-to-end test; do not build it again merely because source integration occurred.

```sh
/tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/exchangecontent ./internal/runtimepersistence ./internal/productruntime -count=1
/tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/serverconnection ./internal/codexoauth ./internal/provideraccount ./internal/capturegrant ./internal/capturecontrol ./internal/manualcaptureclient ./internal/desktopcontrol ./internal/desktophost ./internal/serverhost ./internal/socksproxy ./cmd/vibermate ./cmd/vibermated -count=1
VIBERMATE_TEST_XRAY_BINARY="$PWD/dist/candidates/local-o7uoeCnw/ViberMate.app/Contents/MacOS/vibermate-xray" /tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/serverhost -run '^TestServerManualSOCKSREALITYDelivery$' -count=1 -v
```

Run the actual existing Flutter parser/API, original-account reauthorization, SOCKS/global/VLESS editor tests on VM; same-tab restore and SOCKS flows on Chrome after the existing asset preparer. These filenames are supplied by the stable/current parents and must exist after resolution.

```sh
/opt/homebrew/bin/flutter test --no-pub test/control_models_test.dart test/control_api_http_test.dart test/codex_reauthorization_test.dart test/socks_delivery_test.dart test/egress_default_settings_test.dart test/vless_egress_editor_test.dart test/vless_tls_editor_test.dart
/opt/homebrew/bin/dart run tool/prepare_chrome_test_assets.dart
/opt/homebrew/bin/flutter test --no-pub --platform chrome test/platform_runtime_web_test.dart test/socks_delivery_test.dart
```

Run affected Node release-policy/source-manifest tests where actual merge hunks touch those consumers, using the explicit pinned Node22 binary. The report lists exact selected filenames/commands and links the corresponding hunks. Preserve all first failures and any skip/platform/font limits; do not inflate local passes into final CI/native/source qualification.

- [x] **Step 4 — independent resolution review and merge checkpoint.** Freeze source and report exact checkpoint/stable inputs, resolved conflict list, source hashes, native/functional results and remaining limits. Independent review covers resolution semantics and cross-consumer contracts. Root commits the merge only after findings and applicable checks are resolved. Then proceed to remaining functional Goal work and appropriate phased candidate/release qualification; do not mark the full Goal complete.

**Closure evidence:** Task2 independent review initially found duplicate/pre-admission Raw scope ownership and three missing compiler test fixtures. Fix1 preserved the actual HTTP1/HTTP2 Raw-scope12 RED, then passed five covering tests and the full loopbackproxy normal suite; independent scoped review closed I1/I2 with no new findings. Other combined evidence includes final Go compile,3+12 Go packages, Flutter analysis/VM185/Chrome52, actual packaged-helper SOCKS/REALITY TCP+UDP and Node96PASS/2existing opt-inSKIP. Exact source hashes, parents, logs and reviews remain under this plan's SDD directory. This is a local source integration checkpoint, not final CI/race/platform/signature/public-release qualification.

## Self-review

| Shared Interface / task | Agreement |
| --- | --- |
| Task1 → Task2 | Exact frozen source and actual Git stages define work, not a hypothetical conflict map |
| Stable fixes → current features | Keep both behavior sets; no entire-side overwrite, history trimming or credential reset |
| Combined tree → regression | Compile first, then known affected Go/Flutter/native paths; old results not reused as combined proof |
| Source → release pipeline | Preserve current kernel inventory and stable security pins; final version/sign/notary remain later |
| Task1 self | Explicit inspected file list excludes generated cache, backups retained, no destructive cleanup |
| Task2 self | Actual conflict resolutions and checks reviewed before merge commit; no new architecture scope |
