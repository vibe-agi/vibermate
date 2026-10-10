# Managed VLESS Kernel Packaging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development and test-driven-development. Preserve the reviewed producer and existing WIP. Root assigns one source/local-test owner and reviews each complete deliverable.

**Goal:** Put the reviewed fixed-source kernel and its source/license materials into the real macOS and Linux distributions, with matching provenance and closed executable inventories, so Host integration can use a deterministic packaged helper.

**Architecture:** A small Python packaging Module consumes actual outputs of `build_kernel`, verifies their byte/target/source identities, assembles a thin or macOS Universal helper, and collects the associated source/license materials. Existing application builders call the fixed producer and this Module, then retain their current signing and distribution verifiers. Host discovery is the next separate integration task; no runtime downloader or arbitrary node-selected executable is introduced.

**Tech Stack:** Existing Python3.9 standard library, fixed Go1.26.9 producer, macOS lipo/codesign and current Bash/Node22 distribution tooling.

**Spec:** `../specs/2026-10-09-vless-udp-functional-design.md`, parent Task4 in `2026-10-09-vless-udp-functional.md`. Producer contract is `2026-10-10-vless-kernel-producer.md`; its Task1 and fix1 are independently Approved. This plan implements the already approved managed-distribution requirement, not a new architecture or an additional approval gate.

## Global Constraints

- One product source/build/test owner. Preserve existing WIP with before-copies of touched files. Handwritten edits use apply_patch. No staging/commits, broad cleanup, user App/9666/accounts/trust/system proxy changes, signing credentials or release operations by workers.
- Fixed upstream protocol source `d2758a023cd7f4174a5a5fa4ff66e487d4342ba0`, dependency patch and Go1.26.9 remain unchanged. Consume checked-in `tool/vless-kernel/kernel-lock.json` and assets; no alternate lock, floating binary download, `go get`/tidy or main-module update.
- Runtime never downloads/builds a kernel. Node URI and public profile data cannot select an executable. Helper filename is `vibermate-xray` (Windows producer output is `vibermate-xray.exe`, but Windows installer integration is outside this macOS/Linux package unit).
- No unconfigured Direct fallback, permissive TLS bypass, new global trust, invented signatures or fake scan/native-pass fields. Artifact assembly/metadata tests are not native network or final release qualification.
- Preserve the exact existing protected signing/notary/source-inventory gates and preview isolation. Extend closed inventories explicitly; do not turn them into wildcard acceptance or skipped checks.
- Before any new broad build, run the affected contract tests. Reuse the already qualified real producer outputs for assembly proof; do not rerun unchanged six compiler builds or network/interop scans just to assemble their bytes.

## File structure and package contract

- `tool/vless-kernel/package_kernel.py` and `test_package_kernel.py`: packaging Interface, validation, actual materials, thin/Universal assembly and focused tests.
- Existing `tool/vless-kernel/README.md`: builder usage and pre-sign versus signed-byte distinction.
- Existing macOS local/distribution builders, verifier, desktop build manifest and protected release inventories/tests: consume the new helper explicitly.
- Existing Linux build/verify scripts: include and check the actual target-matched helper/materials.
- `THIRD_PARTY_LICENSES.md`: explain the bundled engine, dependency notices and exact modified-source access, without replacing existing notices.

Final application layout:

```text
macOS: ViberMate.app/Contents/MacOS/vibermate-xray
       ViberMate.app/Contents/Resources/vless-kernel/
Linux: <bundle>/vibermate-xray
       <bundle>/vless-kernel/
```

The materials directory includes `package-manifest.json`, per-target original `kernel-manifest.json`, the exact locked source archive, dependency patch, original public commit object, source lock, pristine upstream license, patched go.mod/go.sum, `SOURCE-ACCESS.txt`, Go runtime license, and verbatim linked-module license/notice files with a machine-readable index. No `.git`, `.work`, command logs, private keys or inherited environments are shipped.

`package-manifest.json` schema is `vibermate.vless-kernel-package/v1`. It records target, executable name/size/SHA256 at **pre-sign assembly**, actual input target/manifest/binary digests, upstream/patch identities, and material relative-path/size/SHA256 entries. It explicitly labels the executable digest stage `pre-sign`; later ad-hoc/Developer ID signing changes the helper bytes and is bound by the existing Desktop nested-code manifest/signing transformation, not misrepresented as matching the raw producer digest. The package manifest is written last; failed assembly has no success manifest.

## Task 1: Assemble verified kernel bytes and complete source/notice materials

**Files:** Create `tool/vless-kernel/package_kernel.py`, `tool/vless-kernel/test_package_kernel.py`; modify `tool/vless-kernel/README.md`. Do not touch application builders or the reviewed producer in this task. If a concrete producer seam is necessary, report it first; no duplicated alternative compiler/source verifier.

**Interface:**

```python
def package_kernel(*, go_binary: str, target: str,
                   build_directories: list[str], output_dir: str) -> dict:
    """Verify producer outputs; emit a fresh executable + vless-kernel materials.

    target: darwin/arm64, darwin/amd64, darwin/universal,
            linux/amd64, linux/arm64.
    build_directories: one completed matching producer output, or exactly
        darwin/arm64 and darwin/amd64 for Universal. Inputs are read-only.
    output_dir: fresh owned child beneath a real existing parent.
    Returns exactly the final package-manifest data; raises bounded KernelError.
    """
```

CLI: `python3 tool/vless-kernel/package_kernel.py --go /absolute/go --target darwin/universal --input /absolute/arm64 --input /absolute/amd64 --output /absolute/new-output`. No alternative lock or success-bypass flag. The input path is a build-time tooling argument, not a Runtime/user-node setting. Production builders in Task2 generate those inputs using the reviewed producer in the same build.

- [ ] **Step 1 — behavioral RED through the Interface.** Start with a running minimal Interface and a rejection test; demonstrate wrong target/duplicate architecture or altered binary acceptance before fixing it, not only missing imports. Use `unittest` and real temporary files. Synthetic command doubles may exercise rejection; actual success claims must come from the existing real outputs.

```python
with self.assertRaisesRegex(KernelError, "target"):
    package_kernel(go_binary=self.go, target="darwin/universal",
                   build_directories=[self.arm64, self.arm64],
                   output_dir=str(self.root / "out"))
self.assertFalse((self.root / "out" / "vless-kernel" /
                  "package-manifest.json").exists())
```

- [ ] **Step 2 — validate, then own output.** Validate the exact target/input set, actual fixed producer manifest identity, regular files, byte hashes/sizes, source materials and actual Go build metadata. Compare manifest dependencies with actual binary metadata rather than trusting a caller-supplied list. Verify the provided compiler is Go1.26.9. Reuse the producer's existing verification helpers where applicable. Reject symbolic/missing/tampered inputs, replacements, wrong architectures, lock/patch mismatches and existing/competing output without touching unowned contents. Strict JSON rejects duplicate keys. Command failures and filesystem errors stay bounded by phase without dumping credentials.

- [ ] **Step 3 — collect actual notices and source.** Reuse the preserved producer `.work/source` and exact archive (local-archive producer inputs may not store the archive under `.work`; locate and hash the original path recorded by the producer inputs or fetch the fixed locked HTTPS archive using the existing verified downloader). Carry the original archive plus exact dependency patch and patched module files so recipients can reconstruct the modified source. For every actual linked dependency `Path/Version/Sum`, query exact verified Go module metadata under the producer's controlled environment (no version solving/upgrades), require matching module sum, and copy verbatim regular license/copying/notice files from that module, including nested third-party notices. Include Go's license from the verified compiler's GOROOT. Use stable path-derived material keys; a missing license is an explicit bounded failure naming the module, not a blank/generated substitute or silently omitted dependency. Capture source URLs/versions and actual file hashes in `licenses.json`; do not reinterpret license text or claim legal certification.

- [ ] **Step 4 — assemble bytes.** Thin targets copy the verified binary unchanged. Universal uses `/usr/bin/lipo -create` with the two actual inputs, verifies exactly x86_64+arm64, extracts each slice into an owned temporary location and compares its bytes with the source slice. Preserve filenames and executable modes. Package only allowlisted materials, never entire producer work directories. Write actual pre-sign size/hash and material map, atomically publish the final manifest last. Keep failed owned work diagnosable without broad deletion.

- [ ] **Step 5 — targeted tests and actual assembly proof.** Run `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tool/vless-kernel -p 'test_package_kernel.py' -v`. Cover target cardinality, altered binary/material/manifest/dependency evidence, duplicate JSON, symlink input, existing/competing output, missing notice and failing lipo; assert no success manifest and unchanged unowned sentinel. Test notice collection with real synthetic module directories and exact copied text/hash, not just method-call counts. Then assemble actual Universal from `/private/tmp/vibermate-kernel-producer.3FnjWV/native-a` + `darwin-amd64`, and thin Linux from `linux-amd64` + `linux-arm64` separately, using `/tmp/vibermate-go-security.JcRyfL/go/bin/go`. These outputs already exist and their producer proof is retained; do not rebuild them. Native thin assembly can additionally compare exact dbcd4d37503782af9d218d4f2519e59fd1ea7142fe18b94a63f3a2cc1ab5d25f. Record actual inventory/module counts and byte hashes. Do not execute Linux binaries on macOS.

- [ ] **Step 6 — freeze.** Retain before-based diff, source hashes, commands/first failures/exits, actual output manifests and all live-handle terminal states. README documents source reconstruction, exact packaging commands, file layout and later signing/Host limits. Release source/test slot for one independent task review; no commits or subagents from implementer.

## Task 2: Real builders and all existing inventory consumers

**Files:** `ui/flutter_app/tool/build_macos_app.sh`, `build_macos_distribution.sh`, `verify_macos_app.sh`, `desktop_build_manifest.mjs`, `desktop_build_manifest.test.mjs`; `tool/macos-release/macos-distribution-policy.mjs`, `verify-macos-signed-candidate.mjs`, `prepare-r0-release-evidence.mjs`, `r0-build-input-transfer.mjs` and their existing focused tests; `tool/linux-release/build-linux-distributions.sh`, `verify-linux-distributions.sh`; `THIRD_PARTY_LICENSES.md`. The protected workflow `macos-developer-id-candidate.yml` is touched only if its explicit R0 copy inventory needs the new member; no credential/approval changes. Before-copies are mandatory for existing WIP.

**Consumes:** Task1 `package_kernel` layout and immutable actual producer outputs. **Produces:** a fixed sibling helper at the package paths above, included in the actual signed code inventory and offline source/notice resources. Subsequent Host discovery consumes only that package-relative helper (or the already explicit developer override), not a build/cache path.

- [ ] **Step 1 — affected contract RED.** Extend the existing desktop manifest/policy/R0 tests with the new expected helper and a missing/wrong kernel rejection. Use actual verifier Interfaces and fixture bundles, not only source-string matching. Keep preview expected members unchanged. Run the exact affected tests and retain the failing assertions before changing implementation.
- [ ] **Step 2 — invoke the reviewed producer and assembler.** In fresh builder-owned paths, resolve the actual Go executable and call `build_kernel.py` per required target, then `package_kernel.py`. Local live/candidate builds embed the host-matching thin helper; Universal distribution embeds both-slice output. Preview remains without authority-bearing sidecars/materials. Linux builds embed the corresponding architecture output. Do not add a release-time arbitrary-prebuilt override. Source/network acquisition is the fixed producer's job, not copied shell download logic.

```sh
python3 "${repository_root}/tool/vless-kernel/build_kernel.py" \
  --go "${kernel_go}" --target "${kernel_target}" \
  --output "${kernel_build}"
python3 "${repository_root}/tool/vless-kernel/package_kernel.py" \
  --go "${kernel_go}" --target "${kernel_target}" \
  --input "${kernel_build}" --output "${kernel_package}"
```

- [ ] **Step 3 — extend, do not loosen, macOS proof.** Embed helper before nested ad-hoc signing, then write the Desktop manifest after signing. Add nested key `vibermate-xray` and its exact path; add producer lock/patch/tool input hashes to configuration provenance. Add code object/signing identity `io.vibermate.desktop.vibermate-xray`, fixed executable inventory, exact Universal checks and two additional architecture tickets (14→16 code-directory tickets). Update signing transformation and installed/archive fixtures using the same real policy. R0/SBOM transfer must carry the executable and materials with digest agreement, not omit them because they live outside current three members. Preserve minOS14, protected environment checks and absence of arbitrary executable acceptance.
- [ ] **Step 4 — Linux and notices.** Copy executable/materials to the exact bundle layout. Verify regular/non-symlink target and actual kernel/source/package identities before archiving; verification checks presence/materials and host-native kernel version while keeping cross-arch checks static. Preserve existing Runtime/Web native startup and cleanup. Add human-readable root attribution pointing to bundled verbatim notices and modified-source materials; do not delete other notices.
- [ ] **Step 5 — affected checks and one owned assembly.** Run Bash syntax checks and the touched Node22 contract suites (`desktop_build_manifest`, distribution policy, signed-candidate, R0 evidence/transfer, archive/installed evidence, workflow) as needed by their changed consumers. Build one local candidate bundle with explicit Go1.26.9 PATH using the existing candidate mode; do not install or launch the user's App. Inspect actual helper/resources, nested metadata/signature/architecture and run unchanged app verifier. Full clean-source protected Universal/Linux native distribution runs belong to the later frozen candidate; preserve platform/build limits, do not force them green on dirty developer source.
- [ ] **Step 6 — freeze and hand off to Host.** Retain exact diff/hashes, actual command evidence and no remaining owned processes. Independent review covers both spec and quality. Report any concrete original-package failure separately; no full project refactor, version bump or public release in this task.

## Self-review / next consumer

| Producer → consumer | Decision |
| --- | --- |
| fixed producer → Task1 | fixed source/compiler and actual-byte validation; no alternative build implementation |
| Task1 → Task2 | one fixed executable plus `vless-kernel/`; pre-sign digest explicitly distinct from later signed bytes |
| Task2 → protected distribution | explicit fourth helper in closed code/signature/notary/R0 inventories; no wildcard or skipped gate |
| package → Host | deterministic sibling helper; trusted override stays host-only, never profile controlled |
| Task1 self | existing actual artifacts support real packaging proof; notice omissions fail visibly |
| Task2 self | local candidate proof does not pretend dirty source is a signed release or other OS ran natively |

After these tasks, wire package-relative daemon discovery, Desktop loopback SOCKS startup, host-owned authenticated status and separate ManualCapture SOCKS descriptor, then demonstrate the assembled REALITY/Vision TCP/UDP path. This plan does not replace those remaining parent requirements, client/CA/MCP/soak work, or final same-source release qualification.
