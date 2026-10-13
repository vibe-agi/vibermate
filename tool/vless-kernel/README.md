# Locked VLESS kernel producer

This tool builds the exact pinned Xray source with the reviewed dependency patch.
It requires Python 3.9 or newer (standard library only), local Git, and an absolute
Go 1.26.9 executable. It does not install Go or modify global Git/Go settings.

Create an owned temporary parent, then build into a new child:

```sh
kernel_fixture_dir="$(mktemp -d /tmp/vibermate-kernel-producer.XXXXXX)"
python3 tool/vless-kernel/build_kernel.py \
  --go /absolute/path/to/go1.26.9/bin/go \
  --target darwin/arm64 \
  --archive /absolute/path/to/locked-upstream-source.tar.gz \
  --output "${kernel_fixture_dir}/native-a"
```

Supported compile targets are `darwin/arm64`, `darwin/amd64`, `linux/amd64`,
`linux/arm64`, and `windows/amd64`. The executable is `vibermate-xray`, or
`vibermate-xray.exe` for Windows. Omit `--archive` to download only the locked
HTTPS codeload URL using ordinary proxy/TLS transport settings. Local and network
archives must match the same exact byte count and SHA256 before extraction.
Output must be a fresh child beneath an existing real directory; existing paths
and symbolic-link destinations are rejected. No alternate lock CLI is provided.

The Python interface is:

```python
build_kernel(*, go_binary: str, target: str, output_dir: str,
             archive_path: str | None = None) -> dict
```

The return value is exactly the emitted `kernel-manifest.json` data. That manifest
is published atomically as the final success marker after static build metadata
and binary byte checks. Failed outputs retain safe `.work/commands.log`
diagnostics and any rejected binary under a `.failed` name in the owned work
directory; no completed executable or success manifest is exposed. Compiler
validation happens before output creation. Work/source directories are retained
for inspection and belong to the caller.

`kernel-lock.json` binds the original source archive, full Git tree and raw commit,
pristine/patched module files, patch and upstream license. `upstream.commit`
preserves the existing public signature bytes; these content checks do not claim
independent GPG trust verification. Local Git uses isolated configuration and
hooks, indexes tracked-but-ignored files, establishes exact detached shallow HEAD
without a tag, and verifies clean source before applying `dependency.patch`.
Only the locked module patch is permitted. `vcs.modified=true` honestly records it.

The build uses `./main`, `-buildvcs=true`, `-trimpath`, and the exact locked source
marker. Modules are read-only; Go uses the public proxy/checksum service and empty
private exemptions. Ambient compiler flags, experiments, architecture levels,
workspace/toolchain switches and compiler overrides are neutralized locally.
Ordinary environment and proxy transport are preserved without recording secrets.
The manifest records actual compiler identity, build arguments, controlled
environment, actual build metadata/dependencies, and the output's size/hash.

Each successful output carries the exact `LICENSE.upstream`, `dependency.patch`,
`kernel-lock.json`, `upstream.commit`, and `SOURCE-ACCESS.txt`. The source-access
note identifies the exact archive to which the patch applies. The root upstream
license alone is not a complete notice/license audit of linked modules.

## Verified package assembly

`package_kernel.py` consumes completed outputs from this fixed producer. It
requires the same absolute Go 1.26.9 compiler and verifies its byte identity,
the adjacent lock/assets, producer manifests, source module files, executable
bytes, and actual Go build metadata/dependencies before claiming success.
Inputs are read-only real directories; the fresh output must be outside them.
Existing outputs, duplicate architectures, symlinks, duplicate JSON keys,
changed/replaced evidence, and missing linked-module notice material fail with a
bounded `KernelError`. There is no alternate lock or bypass flag.

For a Universal macOS kernel, first produce `darwin/arm64` and `darwin/amd64`
outputs, then assemble them with Apple's `/usr/bin/lipo`:

```sh
python3 tool/vless-kernel/package_kernel.py \
  --go /absolute/path/to/go1.26.9/bin/go \
  --target darwin/universal \
  --input /absolute/producer-arm64 \
  --input /absolute/producer-amd64 \
  --output /absolute/existing-parent/new-universal-package
```

Thin assembly copies the verified executable without changing its bytes:

```sh
python3 tool/vless-kernel/package_kernel.py \
  --go /absolute/path/to/go1.26.9/bin/go \
  --target linux/amd64 --input /absolute/producer-linux-amd64 \
  --output /absolute/existing-parent/new-linux-amd64-package
python3 tool/vless-kernel/package_kernel.py \
  --go /absolute/path/to/go1.26.9/bin/go \
  --target linux/arm64 --input /absolute/producer-linux-arm64 \
  --output /absolute/existing-parent/new-linux-arm64-package
```

Thin `darwin/arm64` and `darwin/amd64` are also supported, each with one matching
input. Universal requires exactly one input of each architecture, verifies
exactly `arm64` and `x86_64`, and checks each extracted slice's size/hash against
the original. Executable filenames and modes are preserved. The Python interface
returns exactly the final package manifest:

```python
package_kernel(*, go_binary: str, target: str,
               build_directories: list[str], output_dir: str) -> dict
```

The package child contains `vibermate-xray`, `vless-kernel/`, and private `.work/`
diagnostics. Only the first two belong in application distributions. The
materials directory contains:

- `package-manifest.json`: the final atomically published success marker.
- `inputs/<os>-<arch>/kernel-manifest.json`: each original producer manifest.
- `upstream-source.tar.gz`, `dependency.patch`, `upstream.commit`,
  `kernel-lock.json`, `LICENSE.upstream`, and verbatim `SOURCE-ACCESS.txt`.
- `source/go.mod` and `source/go.sum`: the exact patched module files.
- `licenses/go/LICENSE`: Go's verbatim license from the verified compiler.
- `licenses/modules/<sha256-of-module-path-and-version>/...`: verbatim regular
  license/copying/notice/copyright files, including nested third-party notices.
- `licenses.json`: actual linked module paths, exact versions/sums, fixed-version
  source URLs, original notice paths, and every copied notice's size/hash.

Module metadata comes from exact `go mod download -json path@version` queries
under the producer's controlled environment; returned sums must match the actual
binary. `go mod verify` also checks downloaded module content. These commands use
private copies of the patched module files, public proxy/checksum verification,
and ordinary TLS/transport settings; they do not upgrade dependencies or modify
global configuration. Notice collection preserves text exactly and does not
certify license obligations. A missing regular license/notice fails and names
the module. Material keys are portable path-derived hashes, while original
module identities and notice paths remain in `licenses.json`.

The original archive is reused from the producer's retained `.work` when present.
Local-archive producer manifests do not record that input path; when the archive
is absent, the existing verified downloader fetches the fixed locked HTTPS URL
once per assembly and checks its exact size/hash. No whole producer source/work
directory, Git metadata, inherited environment, or command log enters the
material inventory. Failed private checks remain under
`vibermate-kernel-package-check-*` in the system temporary directory; owned output
failures retain diagnostics without removing unrelated paths.
Non-command failures after private-work creation retain a fresh 0600
`failure.json` in that 0700 private directory; the bounded CLI error names its
location. The record contains only phase, exception type and available numeric
HTTP status/errno (including a wrapped URL error's safe reason type/errno).
It excludes raw messages, URLs, headers, response bodies and environment values.
If private work is not yet owned or the record cannot be written, the error
preserves the original phase and reports that the failure record is unavailable.

To reconstruct the modified source, extract `upstream-source.tar.gz`, enter
`Xray-core-<kernel-lock.json commit>`, and apply the supplied `dependency.patch`
with `git apply /absolute/path/to/dependency.patch`. Compare the resulting
`go.mod` and `go.sum` to the supplied `source/` copies and their locked SHA256s.
The archive, raw commit, exact patch, module files, and fixed-version dependency
source URLs provide account-free source access; reconstruction does not claim
independent GPG certification.

`package-manifest.json` uses `vibermate.vless-kernel-package/v1`, records each
input's manifest/binary digests, upstream/patch identities, the closed material
file map, and actual executable size/SHA256 with `digestStage: pre-sign`. Its file
map excludes the package manifest itself. Later ad-hoc or Developer ID signing
changes executable bytes; the pre-sign hash is not the final signed hash.
Desktop consumers must bind those transformations in their nested manifests.
Host discovery, runtime helper selection, final application inventory/signing,
installation, trust qualification, and Linux native execution remain downstream
work. A packaging input is a build-time tooling argument, not a user-node or
Runtime setting.

Run just the packaging contract tests with:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s tool/vless-kernel -p 'test_package_kernel.py' -v
```

Run the targeted tests with:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tool/vless-kernel -v
```

Two native darwin/arm64 builds have reproduced the separately qualified input
byte-for-byte. Other target compilation and static metadata checks do not establish
native execution, security qualification, or App support on those systems. Package
builders still need to deliver notices/source access, incorporate and inventory the
kernel, sign/verify nested macOS code where applicable, and integrate trusted Host
helper discovery. Native System-positive trust, cross-platform runtime tests,
installation and final release qualification remain separate work.
