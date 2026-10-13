# Source-Bound Patched VLESS Kernel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development and verification-before-completion. This is the next managed-kernel input for approved parent Task4; only dispatch after the current UI owner releases the local source/build/test slot.

**Goal:** Produce and verify one native managed-kernel candidate from the already-selected Xray source with current security patches, then use its actual build results as the input to packaging integration.

**Architecture:** Keep the existing managed-process Module and upstream protocol implementation. Build exact Xray commit `d2758a023cd7f4174a5a5fa4ff66e487d4342ba0` in an owned temporary checkout with Go1.26.9 and a recorded minimal dependency-security patch. Preserve source, patch, commands, dependencies, licenses and binary hashes together; do not bundle the old Go1.26.1 binaries or alter the main product toolchain in this unit.

**Tech Stack:** Existing verified `/tmp/vibermate-go-security.JcRyfL/go/bin/go` (Go1.26.9, executable SHA256 `318dee99db233501f6c419fbb4616e5a345ed0b831dbd6c7669b24e1ff8e9e2e`); official GitHub XTLS/Xray-core source; official Go module proxy/checksum database; existing govulncheck v1.6.0 pin. Native target darwin/arm64 first, no platform execution claims by cross compilation.

**Spec:** `../specs/2026-10-09-vless-udp-functional-design.md` and parent `2026-10-09-vless-udp-functional.md` Task4/5. Concrete inputs: parent SDD `kernel-release-security-facts-2026-10-10.md`, `kernel-binary-metadata-2026-10-10.md`, `tls-ca-integration-facts.md` (root has read all three).

## Global Constraints

- One local source/build/test owner, including temporary upstream builds. No user App/9666/accounts/trust/proxy changes; no protected signing/notary approval, release/tag/push or global Go installation/environment changes.
- No main product Go.mod/go.sum, Flutter, Runtime, ingress, private protocol or lifecycle edits in this unit. Build only in one `mktemp -d` directory; keep artifacts/evidence for the following packaging task, do not erase failed attempts.
- Keep exact upstream protocol source. Initial security patch is x/net v0.60.0 and its required minimum-version-selection changes; further dependency changes require an actual scanner/compiler finding and a primary-source fixed version, recorded as a minimal patch. Do not run an all-dependency latest upgrade or weaken TLS, REALITY, UDP authentication or checksum verification.
- No source/date bypass to revive allowInsecure. No geodata/Wintun/DLL bundling, external node connections, arbitrary Xray configuration from users or protocol fork.
- Source/reachability scan outcomes, compiler metadata and real handshake tests are different evidence. A module-only report is not proof of exploitability; a clean binary scan is not native platform/whole-product proof. Keep actual failures and scope limits.
- The existing official darwin/arm64 engine remains an old functional fixture. Do not overwrite it, pretend it received patches, or relabel an old successful run as evidence for the new binary.

## Task 1: Build and qualify the native patched input

**Files:** owned temporary exact upstream checkout and build artifacts; report, raw logs, source/module patch and hashes under this plan's `.superpowers/sdd/2026-10-10-vless-patched-kernel/`. No product source edits. Existing product tests may execute unchanged with the new explicit binary only while this task owns the test slot.

**Produces:** one exact native executable and an accompanying manifest containing: upstream repository/ref/full commit/tree; clean-source and patched go.mod/go.sum hashes; complete dependency patch hash; Go executable/version/hash; build argv/environment allowlist; binary path/size/hash/build-info; scanner version/results; exact native interop commands/results; retained upstream LICENSE/source pointer. All computed fields contain actual observed values, not invented defaults. This is an input candidate, not an installed or published kernel.

- [ ] **Step 1: Reconfirm inputs and observe the security baseline.** Verify the existing Go executable hash and `go version`. Read the static binary report and its raw metadata; use the retained official darwin/arm64 binary at `/tmp/vibermate-xray-metadata.NywgPC/darwin-arm64/xray` without executing it. Scan it in binary mode with the existing v1.6.0 tool pin and save exact output/exit. Expected affected old compiler/dependency findings are security baseline evidence, not a manufactured application regression.

```sh
env GOTOOLCHAIN=local GOWORK=off GOFLAGS= \
  GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org \
  GONOSUMDB= GONOPROXY= GOPRIVATE= \
  /tmp/vibermate-go-security.JcRyfL/go/bin/go run \
  golang.org/x/vuln/cmd/govulncheck@v1.6.0 -mode=binary \
  /tmp/vibermate-xray-metadata.NywgPC/darwin-arm64/xray
```

Use `set -o pipefail` for logging pipelines. Do not infer that every flagged symbol is exercised by the selected Runtime configuration.

- [ ] **Step 2: Obtain exact official source.** Create one owned temporary directory with `mktemp -d /tmp/vibermate-xray-patched.XXXXXX`; initialize/fetch from `https://github.com/XTLS/Xray-core.git` and checkout only full commit `d2758a023cd7f4174a5a5fa4ff66e487d4342ba0`. Record `git rev-parse HEAD`, tree and clean status before patching, and verify equality with the required commit. Do not use floating main/latest or change the product worktree. Preserve pristine go.mod/go.sum/LICENSE and source URL in evidence. Check for submodule/build requirements from this exact source, without running unrelated upstream scripts.

```sh
kernel_task_dir="$(mktemp -d /tmp/vibermate-xray-patched.XXXXXX)"
kernel_source_dir="${kernel_task_dir}/source"
kernel_output_dir="${kernel_task_dir}/output"
mkdir -p "${kernel_source_dir}" "${kernel_output_dir}"
git -C "${kernel_source_dir}" init
git -C "${kernel_source_dir}" remote add origin https://github.com/XTLS/Xray-core.git
git -C "${kernel_source_dir}" fetch --depth=1 origin d2758a023cd7f4174a5a5fa4ff66e487d4342ba0
git -C "${kernel_source_dir}" checkout --detach FETCH_HEAD
git -C "${kernel_source_dir}" rev-parse HEAD 'HEAD^{tree}'
git -C "${kernel_source_dir}" status --porcelain=v1
```

Use the actual generated paths consistently and record them in the report. A resumed tool call must reuse that already-created directory; it must not silently create another checkout or resolve to the user's workspace or a broad directory.

Observed transport amendment2026-10-10: exact Git fetch and scoped HTTP/1.1 retry returned CONNECT407 through the inherited proxy; no Git proxy override exists, and official HTTPS/API succeeds without changing configuration. Use official Git commit API + exact-commit codeload archive as the bounded alternative. Its verification.payload/signature may reconstruct the original public commit object only if `git hash-object -t commit` equals exact `d2758a023cd7f4174a5a5fa4ff66e487d4342ba0`; safely extracted full source/index must produce `git write-tree` exactly `dbcb1d31713edfcf5860cf35feee0632e9117893`. Only after both checks may the verified object, detached HEAD and required shallow boundary be installed in this owned checkout. Preserve archive/API/object/tree hashes and original407, confirm a clean tree, and clearly record API/codeload transport rather than claim clone succeeded or an independent GPG trust verification. No further proxy investigation, global settings, Direct/TLS bypass or repeated failedfetch is needed.

- [ ] **Step 3: Apply the minimal recorded dependency patch and build.** In the owned upstream checkout, use the verified Go1.26.9 with GOTOOLCHAIN=local/GOWORK=off and normal checksum verification to select `golang.org/x/net@v0.60.0`; retain the actual Go module diff and MVS changes. Dependency tooling may generate module files; any handwritten correction uses apply_patch. Do not modify protocol source. Build `./main` with CGO disabled and the source marker retained in the banner:

```sh
env GOTOOLCHAIN=local GOWORK=off GOENV=off GOFLAGS= \
  GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org \
  GONOSUMDB= GONOPROXY= GOPRIVATE= \
  /tmp/vibermate-go-security.JcRyfL/go/bin/go get golang.org/x/net@v0.60.0

env GOTOOLCHAIN=local GOWORK=off GOENV=off GOFLAGS=-mod=readonly \
  CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
  /tmp/vibermate-go-security.JcRyfL/go/bin/go build -buildvcs=true -trimpath \
  -ldflags='-X github.com/xtls/xray-core/core.build=d2758a0-vibermate-security1' \
  -o "${kernel_output_dir}/vibermate-xray" ./main
```

Record real compilation failures and fix only their demonstrated dependency/build causes. The dirty VCS marker caused by the explicit dependency patch is expected and must be recorded alongside that exact patch; do not disguise it as pristine upstream. If additional official dependency-security patches are required by actual findings, record old/new/fixed-source URLs and rerun only affected qualification. Escalate if progress requires protocol changes or a different upstream version rather than silently expanding scope.

- [ ] **Step 4: Verify the newly built bytes.** Read `go version -m` and SHA256/size. Run govulncheck v1.6.0 on the actual patched `./main` source and newly built binary; retain called/imported/module-only distinctions and complete failures. Confirm compiler and independent x/net implementation are patched; no ignore files, output filters or omitted reachable findings to produce green. Preserve the exact patch and full module list/Go sums. Collect the upstream license and list of actually linked modules for later notices; this is not a completed cross-platform license/package audit.

- [ ] **Step 5: Exercise the existing real native transport with the new binary.** With the new absolute path in `VIBERMATE_TEST_XRAY_BINARY`, run exact existing `TestCoreIntegration`, `TestTLSCoreIntegration`, `TestPrivateUDPRejectsUnassociatedSender` and `TestPrivateUDPRejectsWrongUUID` once in product `internal/vlesscore`. The product itself continues using its existing development Go in this unit. These prove REALITY+Vision/TLS+customCA TCP/UDP and the private authentication contract for the changed engine input. Preserve bytes, engine/source hashes and cleanup evidence. Do not rerun whole Runtime/Flutter/race/soak suites, change timing budgets, or call a skipped integration successful. If the previously recorded observation-channel fixture edge actually manifests, report it for a separately reviewed small test fix; do not silently alter product tests here.

```sh
env GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOPROXY=off \
  VIBERMATE_TEST_XRAY_BINARY="${kernel_output_dir}/vibermate-xray" \
  /Users/null/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.8.darwin-arm64/bin/go test \
  ./internal/vlesscore \
  -run '^(TestCoreIntegration|TestTLSCoreIntegration|TestPrivateUDPRejectsUnassociatedSender|TestPrivateUDPRejectsWrongUUID)$' \
  -count=1 -v -timeout=45s
```

The45s Go package watchdog contains the complete named group; individual product/fixture operation deadlines stay unchanged. Native System-positive trust remains a later isolated runner proof; do not install the synthetic CA into the developer machine.

- [ ] **Step 6: Freeze evidence and hand off.** Write actual commands, source/patch/compiler/binary identities, scanner outcomes, interop results, all process/session exits and remaining delivery scope to task-1-report.md. Keep one manifest and exact source/module patch alongside raw logs. No commit of compiled binaries or real secrets into the product repository, and no public release claim. The next packaging unit uses these known working inputs to implement reproducible build/distribution and native helper discovery, rather than guessing the compiler/dependency patch in advance.

## Self-review and subsequent integration

| Interface | Consistency check |
| --- | --- |
| official source → patched checkout | exact full commit/tree plus explicit minimal dependency patch |
| patched checkout → executable | actual pinned compiler and recorded command/build info; dirty marker explained, not hidden |
| executable → existing process Module | unchanged version/source marker and real TCP/UDP/auth proof |
| native input → later packaging | no premature five-platform/native System/install claim |

Reproducible packaging must subsequently include the patched kernel in macOS nested-code signing/provenance and Linux archives, supply the appropriate notices/source access, and deliver it through trusted Host startup rather than a URI-provided executable. Existing policy/scripts have exact executable inventories and must be extended and tested, not bypassed. The separate Host delivery source map identifies those consumers. This task supplies a verified native input and exact reproducible patch; it does not complete those later integration requirements.
