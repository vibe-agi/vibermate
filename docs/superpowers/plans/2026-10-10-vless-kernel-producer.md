# Locked VLESS Kernel Producer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development, test-driven-development and verification-before-completion. Implement after the native-input review closes; keep product packaging/Host changes as the following integration unit.

**Goal:** Turn the verified native-input procedure into a checked-in deterministic producer that emits a platform kernel and an honest source/patch/build manifest for the existing package builders.

**Architecture:** Reuse the already-run standard-library archive/Git verification procedure, now parameterized behind one small build-tool Interface. Commit the exact source lock, original public Git commit object, dependency patch and upstream license. The producer obtains only that locked archive (or an explicitly supplied identical local copy), verifies it, applies the exact patch, invokes the selected Go1.26.9 and writes fresh output plus provenance. Runtime never downloads or builds a kernel.

**Tech Stack:** Python3 standard library for the existing safe tar/Git procedure, Git local object operations, existing Go1.26.9 compiler. No new Python package, dependency resolver, protocol implementation or application dependency.

**Spec:** `../specs/2026-10-09-vless-udp-functional-design.md`, parent Task4 and `2026-10-10-vless-patched-kernel.md`. The final actual-input report/manifest and dependency.patch in that plan's SDD are authoritative inputs, not values to rediscover.

## Global Constraints

- One product source/build/test owner. Preserve all existing WIP; before-copy any existing file actually touched. Handwritten edits use apply_patch; exact provided assets may be mechanically copied without alteration. No commits/staging, app/9666/accounts/trust/proxy/global Go settings or release/signing actions.
- Do not change main go.mod/go.sum, Runtime, Flutter, package inventories, source admission or signature policy in this unit. Only add this producer and its tests/assets. No binaries committed into Git.
- Exact upstream protocol source and security patch stay fixed. No go get/tidy/update is run by the producer; build with `-mod=readonly`, normal official checksum verification and the exact patched module bytes. No fallback to old official binaries, floating main/latest, Direct connection or disabled TLS verification.
- Isolate each attempt in a newly owned work directory and refuse existing output targets. Preserve failed work/logs rather than overwrite or recursively clean a broad directory. Do not log inherited proxy credentials or complete environments.
- Distinguish reproducible artifact bytes, provenance verification and native behavior. Other-platform compilation is not a native runtime/platform/installation claim. Current two unimported module-only scanner findings remain explicit; no new full scan campaign on unchanged exact inputs.

## Task 1: Checked-in producer, behavioral verification and real reproduction

**Files:** create `tool/vless-kernel/build_kernel.py`, `test_build_kernel.py`, `kernel-lock.json`, `upstream.commit`, `dependency.patch`, `LICENSE.upstream`, `README.md`. All code/CLI lives here; do not add a speculative repository-wide artifact framework.

**Inputs to reuse verbatim:**

| Locked field | Exact value |
| --- | --- |
| repository/ref | https://github.com/XTLS/Xray-core.git / v26.3.27 |
| commit | d2758a023cd7f4174a5a5fa4ff66e487d4342ba0 |
| tree | dbcb1d31713edfcf5860cf35feee0632e9117893 |
| archive URL | https://codeload.github.com/XTLS/Xray-core/tar.gz/d2758a023cd7f4174a5a5fa4ff66e487d4342ba0 |
| archive bytes/SHA256 | 822765 / 14fa566ee0a801d3d51144c67018b449f5dcf462ddfacadd032da069787e61f9 |
| original raw commit SHA256 | 8af835cea3015efd4442433d745d4a33dec51acb802b528ca3cb95ee6f1ff71c |
| dependency.patch SHA256 | 5e73b85a73c3659f6357b1776f5ced0711dddd6247842e39256eab3f32324a47 |
| pristine go.mod / go.sum SHA256 | 59b6bcc54671552945b07780021177a137a1e85d4c32b374e9a49a68ae018749 / 73b6160af7e1cc8769d5a5911fb931e8f0659e967314e2748c83fe315c922d14 |
| patched go.mod / go.sum SHA256 | 60e103fc90c37672198b63a7c45c9040b62f1146b4d7e6ec9bedc03611ccdc77 / 43b0206e7eda56da8a120182b5ba4ec9cea4267d3896791fdf182a9b10e487f1 |
| upstream license SHA256 | 1f256ecad192880510e84ad60474eab7589218784b9a50bc7ceee34c2b91f1d5 |
| compiler/source marker | go1.26.9 / d2758a0-vibermate-security1 |

Raw commit is `/tmp/vibermate-xray-patched.1BB4oX/output/source-raw-commit.bin`; patch/license and source verification procedure are preserved under `.superpowers/sdd/2026-10-10-vless-patched-kernel/task-1-evidence/`. Read the existing `verify-source-archive.py` and final report before adapting it. Keep public signature bytes exactly; do not reconstruct new identities or claim independent GPG trust verification. The fixed archive/commit/tree hashes bind content.

**Interface:**

Expose `build_kernel(*, go_binary: str, target: str, output_dir: str, archive_path: str | None = None) -> dict`. It returns exactly the emitted build-manifest data after byte/metadata checks. The steps below define its implementation and validation behavior; callers need no source/Git/process internals.

The function and CLI use the checked-in lock/assets adjacent to the module, not a caller-provided alternative lock. `target` is one of `darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`, `windows/amd64`. `go_binary` is an absolute existing executable, with actual `go version`/`go env GOVERSION` exactly go1.26.9; record its hash. `output_dir` must be a fresh not-yet-existing child directory beneath an existing real parent, and cannot be a repository/root/home directory. The optional local archive must still match exact size/hash. Network mode uses only the locked HTTPS URL, preserves existing proxy/TLS settings and verifies exact size/hash before extraction.

```sh
kernel_fixture_dir="$(mktemp -d /tmp/vibermate-kernel-producer.XXXXXX)"
python3 tool/vless-kernel/build_kernel.py \
  --go /tmp/vibermate-go-security.JcRyfL/go/bin/go \
  --target darwin/arm64 \
  --archive /tmp/vibermate-xray-patched.1BB4oX/output/upstream-source.tar.gz \
  --output "${kernel_fixture_dir}/native-a"
```

The example's parent is created once as an owned temporary fixture, never assumed pre-existing or reused blindly. Actual test paths/commands go in the report. Windows output is `vibermate-xray.exe`; other targets use `vibermate-xray`. Success also emits `kernel-manifest.json`, exact upstream license/patch/source-lock/raw-commit material and a source-access note beside the binary. Failure must not emit a success manifest or leave a partial binary presented as complete.

Manifest schema is `vibermate.vless-kernel-build/v1`; fields include `schema`, `target`, `upstream` (repository/ref/commit/tree/archiveSHA256), `patchSHA256`, `moduleSHA256` (goMod/goSum), `compiler` (version/host/hash), `build` (argv and non-secret controlled environment), `binary` (name/size/SHA256), `buildInfo` (GoVersion/GOOS/GOARCH/vcs revision/modified/dependencies), and `licenseSHA256`. Use actual observations; do not embed fake scan/native-pass fields or a fixed expected hash for other platforms. Expected `vcs.modified=true` is explained by the exact dependency patch, never disguised as pristine.

- [ ] **Step 1: Tests and behavioral RED.** Add tests through the real producer/preparation Interface using small synthetic tar/commit fixtures and real temporary files. First demonstrate a violated contract (for example accepting a tampered archive or ignoring target/compiler validation) on a minimal running implementation, not only a missing symbol/compile error. The CLI's production lock stays fixed; synthetic locks are an internal test seam, never a runtime flag. Retain RED and command.
- [ ] **Step 2: Extract/verify through the actual procedure.** Hash/size-check the locked archive and checked-in assets before extracting. Retain the existing member safety checks: correct single prefix; no absolute, parent, backslash, `.git`, duplicate or special/link entries; preserve executable bits. Extract only into the newly owned directory. Use local Git with isolated hooks/config so user filters cannot silently change input; `git add --force --all` must include upstream's tracked-but-ignored files. Verify full tree and original commit hashes, establish detached exact HEAD/shallow boundary without adding a new tag (matching the proven input's VCS version), and require clean source before patch. Do not perform network Git fetch or reconstruct the signature at every build; the verified raw commit is now a pinned asset.
- [ ] **Step 3: Patch and build deterministically.** Require pristine module hashes, `git apply --check` the exact locked patch, apply it, require final module hashes and only go.mod/go.sum tracked modifications. Invoke the exact proven build flags (`-buildvcs=true -trimpath`, fixed source-marker ldflags, `./main`), target GOOS/GOARCH, CGO_ENABLED=0, GOTOOLCHAIN=local/GOWORK=off/GOENV=off/GOFLAGS=-mod=readonly and normal public GOPROXY/GOSUMDB/private-exemption values from the input manifest. Neutralize ambient compiler override/overlay/experiment values that would change the admitted bytes; never alter global config. Preserve necessary ordinary environment and proxy transport without serializing secrets.
- [ ] **Step 4: Verify output and expose honest errors.** Statically read actual build info; require exact compiler/target/commit/dirty-patch state and final x/net0.60/gRPC1.83.2 inputs. Check regular output file and size/hash, copy license/source-access materials, then atomically publish the manifest as the last success marker. Refuse overwrite rather than reuse unknown output. Use checked argument arrays, no shell interpolation of input. Bounded error strings describe the failed phase without dumping environment/credential-bearing URLs; retain detailed safe diagnostic output inside owned work paths.
- [ ] **Step 5: Targeted unit/integration coverage.** Cover bad archive size/hash, bad raw commit/patch/module hash, traversal/link/duplicate member, omitted tracked ignored file/tree mismatch, wrong compiler and unsupported target, pre-existing/symlink output, failed build and wrong output build info without success manifest, exact source marker/target mapping, and success manifest bound to produced bytes. Do not test only text presence or mock a claimed successful real build; command fakes may exercise rejection, while the actual production Interface gets the real build proof below.
- [ ] **Step 6: Real native reproduction.** Run the checked-in CLI twice into two fresh owned output directories with the already-verified archive and compiler, sequentially. Require equal actual binary hashes and compare with the qualified input hash `dbcd4d37503782af9d218d4f2519e59fd1ea7142fe18b94a63f3a2cc1ab5d25f`. If the bytes differ, retain both results and identify the concrete command/environment/metadata difference before claiming reproduction; do not just update the expected hash. When bytes match, reuse that exact input's scan/interop evidence rather than rerun the four unchanged tests. Build the remaining target outputs only after native reproduction works; record compilation/build-info checks separately from native execution. No whole Go/Flutter/race/soak campaign.
- [ ] **Step 7: Freeze the tool and hand off.** Run the Python unit tests and CLI help/argument checks, source diff/format checks appropriate to these files, retain commands/first failures/results, source hashes, complete task-only diff and actual output manifests. Describe network-mode testing separately from local-archive mode and any unavailable target. Release the source/test slot for independent review. README documents requirements, exact CLI, source/patch/license access, read-only module/build behavior and later package/native limits; do not claim this alone makes the downloaded App support VLESS.

## Self-review and subsequent consumers

| Producer → consumer | Check |
| --- | --- |
| fixed source lock/assets → actual source tree | exact content binding and preserved public Git identity, no live API/research dependency |
| native procedure → reusable tool | same compiler/module patch/flags; two real builds compare bytes |
| target kernel → macOS/Linux package builders | fixed filename + source/build manifest; package scripts still must incorporate/sign/verify it |
| output metadata → Host resolver | Host consumes a trusted package-relative executable, never a URI-supplied path or automatic runtime download |

The following unit extends existing macOS closed code-object/architecture/signature/notary inventories and Linux tar checks, then connects trusted helper discovery and actual SOCKS/ManualCapture address delivery. Full notices for actually linked modules, native System-positive trust, cross-platform runtime and final install/release remain explicit requirements; copied root LICENSE alone is not that audit.
