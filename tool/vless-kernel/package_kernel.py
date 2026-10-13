"""Assemble verified locked-producer bytes and verbatim source/notice materials."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import sys
import tempfile
import urllib.error

# The CLI runs inside a source tree whose cleanliness is release evidence.
# Suppress adjacent import caches before loading the producer; importing this
# module as a library retains the caller's normal bytecode policy.
if __name__ == "__main__":
    sys.dont_write_bytecode = True

import build_kernel as producer
from build_kernel import KernelError

TARGETS = ("darwin/arm64", "darwin/amd64", "darwin/universal", "linux/amd64", "linux/arm64")
ASSET_NAMES = ("LICENSE.upstream", "dependency.patch", "upstream.commit", "kernel-lock.json")


def _json(data):
    def pairs(items):
        result = {}
        for key, value in items:
            producer._require(key not in result, "JSON", "duplicate key")
            result[key] = value
        return result
    try:
        return json.loads(data, object_pairs_hook=pairs,
                          parse_constant=lambda value: (_ for _ in ()).throw(ValueError()))
    except (ValueError, UnicodeError) as error:
        raise KernelError("JSON: invalid document") from error


def _directory(path, phase):
    producer._require(stat.S_ISDIR(path.lstat().st_mode), phase, "requires real directory")
    return (path.stat().st_dev, path.stat().st_ino)


def _snapshot(path, phase):
    before = path.lstat()
    producer._require(stat.S_ISREG(before.st_mode), phase, "expected regular file")
    digest = producer._sha(path)
    after = path.lstat()
    identity = lambda value: (value.st_dev, value.st_ino, value.st_mode, value.st_size,
                              value.st_mtime_ns, value.st_ctime_ns)
    producer._require(identity(before) == identity(after), phase, "file changed during verification")
    return {"identity": identity(after), "size": after.st_size, "SHA256": digest}


def _remember(path, snapshots, phase, digest=None, size=None):
    observed = _snapshot(path, phase)
    producer._require(digest is None or observed["SHA256"] == digest, phase, "SHA256 mismatch")
    producer._require(size is None or observed["size"] == size, phase, "size mismatch")
    if path in snapshots:
        producer._require(snapshots[path] == observed, phase, "input replaced or changed")
    snapshots[path] = observed
    return observed


def _copy(path, destination, snapshots):
    observed = _remember(path, snapshots, "material copy")
    destination.parent.mkdir(parents=True, exist_ok=True)
    with path.open("rb") as source, destination.open("xb") as output:
        shutil.copyfileobj(source, output)
    producer._check_file(destination, observed["SHA256"], "material copy", observed["size"])
    _remember(path, snapshots, "material copy")


def _source_note(lock):
    # The fixed producer's emitted note is content checked, then carried verbatim.
    return (f"Upstream: {lock['repository']}\nRef: {lock['ref']}\nCommit: {lock['commit']}\n"
            f"Source archive: {lock['archiveURL']}\nArchive SHA256: {lock['archiveSHA256']}\n"
            "Apply the adjacent dependency.patch to that exact source.\n"
            "upstream.commit preserves the public Git identity; no independent GPG trust certification is claimed.\n"
            "vcs.modified=true records the exact dependency patch. LICENSE.upstream covers upstream;\n"
            "complete linked-module notices and native/package qualification remain separate requirements.\n")


def _fixed_inputs(snapshots):
    lock, assets = producer._load_inputs()
    _remember(assets / "kernel-lock.json", snapshots, "lock")
    producer._require(_json((assets / "kernel-lock.json").read_bytes()) == lock,
                      "lock", "adjacent lock mismatch")
    for name, field in (("LICENSE.upstream", "licenseSHA256"),
                        ("dependency.patch", "patchSHA256"), ("upstream.commit", "rawCommitSHA256")):
        _remember(assets / name, snapshots, "lock", lock[field])
    raw = (assets / "upstream.commit").read_bytes()
    producer._require(hashlib.sha1(b"commit " + str(len(raw)).encode() + b"\0" + raw).hexdigest()
                      == lock["commit"], "lock", "commit identity mismatch")
    return lock, assets


def _input(path, lock, assets, snapshots, directories):
    directories[path] = _directory(path, "input")
    for child in (path / ".work", path / ".work" / "source"):
        directories[child] = _directory(child, "input")
    manifest_path = path / "kernel-manifest.json"
    _remember(manifest_path, snapshots, "manifest")
    manifest = _json(manifest_path.read_bytes())
    producer._require(isinstance(manifest, dict), "manifest", "requires object")
    target = manifest.get("target")
    producer._require(target in TARGETS and target != "darwin/universal", "target", "invalid producer target")
    upstream = {key: lock[key] for key in ("repository", "ref", "commit", "tree", "archiveSHA256")}
    producer._require(manifest.get("schema") == "vibermate.vless-kernel-build/v1"
                      and manifest.get("upstream") == upstream
                      and manifest.get("patchSHA256") == lock["patchSHA256"]
                      and manifest.get("moduleSHA256") == lock["moduleSHA256"]
                      and manifest.get("licenseSHA256") == lock["licenseSHA256"],
                      "manifest", "fixed producer identity mismatch")
    for name in ASSET_NAMES:
        _remember(path / name, snapshots, "material", snapshots[assets / name]["SHA256"])
    note_digest = hashlib.sha256(_source_note(lock).encode()).hexdigest()
    _remember(path / "SOURCE-ACCESS.txt", snapshots, "material", note_digest)
    source = path / ".work" / "source"
    producer._modules(source, lock["moduleSHA256"])
    for name, field in (("go.mod", "goMod"), ("go.sum", "goSum")):
        _remember(source / name, snapshots, "module", lock["moduleSHA256"][field])
    binary_data = manifest.get("binary", {})
    producer._require(binary_data.get("name") == "vibermate-xray"
                      and type(binary_data.get("size")) is int and binary_data["size"] > 0
                      and isinstance(binary_data.get("SHA256"), str)
                      and re.fullmatch(r"[0-9a-f]{64}", binary_data["SHA256"]),
                      "binary", "invalid name, size or SHA256")
    binary = path / "vibermate-xray"
    _remember(binary, snapshots, "binary", binary_data.get("SHA256"), binary_data["size"])
    mode = binary.stat().st_mode
    producer._require(mode & stat.S_IXUSR and not mode & (stat.S_ISUID | stat.S_ISGID),
                      "binary", "requires ordinary executable mode")
    return {"path": path, "source": source, "manifest": manifest, "target": target, "binary": binary}


def _verify_metadata(item, go, compiler, lock, check):
    env, controlled = producer._build_environment(item["target"])
    manifest = item["manifest"]
    producer._require(manifest.get("compiler") == compiler, "compiler", "producer compiler identity mismatch")
    argv = manifest.get("build", {}).get("argv", [])
    expected = ["build", "-buildvcs=true", "-trimpath",
                "-ldflags=-X github.com/xtls/xray-core/core.build=" + lock["sourceMarker"], "-o"]
    producer._require(manifest.get("build", {}).get("environment") == controlled
                      and len(argv) == 8 and argv[1:6] == expected and argv[7] == "./main"
                      and Path(argv[0]).is_absolute() and Path(argv[6]).is_absolute()
                      and Path(argv[6]).name == "vibermate-xray", "manifest", "build invocation mismatch")
    info = producer._read_build_info(go, item["binary"], check, env, item["target"], lock)
    producer._require(info == manifest.get("buildInfo"), "build info", "actual metadata differs from producer manifest")
    producer._require(info.get("path") == "github.com/xtls/xray-core/main"
                      and info.get("main", {}).get("Path") == "github.com/xtls/xray-core",
                      "build info", "unexpected main module")
    return info


def _dependencies(infos):
    dependencies = {}
    for info in infos:
        seen = set()
        for dependency in info["dependencies"]:
            path, version, checksum = (dependency.get(key, "") for key in ("Path", "Version", "Sum"))
            producer._require(isinstance(path, str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._~/-]*", path)
                              and all(part not in ("", ".", "..") for part in path.split("/"))
                              and isinstance(version, str) and re.fullmatch(r"v[0-9][A-Za-z0-9.+-]*", version)
                              and isinstance(checksum, str) and checksum.startswith("h1:")
                              and not dependency.get("Replace") and path not in seen,
                              "module", "invalid or duplicate dependency metadata")
            seen.add(path)
            if path in dependencies:
                producer._require(dependencies[path] == dependency, "module", "slice dependency disagreement")
            dependencies[path] = dependency
    return [dependencies[key] for key in sorted(dependencies)]


def _notice_paths(directory, module, directories):
    result = []
    def walk_error(error):
        raise KernelError("license: cannot read notices for " + module) from error
    for root, dirs, files in os.walk(directory, followlinks=False, onerror=walk_error):
        parent = Path(root)
        directories[parent] = _directory(parent, "license")
        for child in dirs:
            _directory(parent / child, "license")
        dirs[:] = sorted(child for child in dirs if child not in (".git", ".work"))
        for name in sorted(files):
            relative = (parent / name).relative_to(directory)
            notice = re.match(r"^(licen[cs]e|copying|notice|copyright)(?:[._-]|$)", name, re.I)
            in_licenses = any(part.lower() in ("licenses", "licences") for part in relative.parts[:-1])
            if notice or in_licenses:
                producer._require(not any(part in (".git", ".work") for part in relative.parts),
                                  "license", "unsafe notice path")
                producer._require(re.fullmatch(r"[A-Za-z0-9._/-]+", relative.as_posix()),
                                  "license", "nonportable notice path for " + module)
                _snapshot(parent / name, "license")
                result.append(relative)
    producer._require(bool(result), "license", "missing regular license/notice files for " + module)
    return sorted(result)


def _proxy_escape(value):
    return "".join("!" + char.lower() if char.isupper() else char for char in value)


def _notices(go, dependencies, check, env, snapshots, directories):
    modules = []
    copies = []
    for dependency in dependencies:
        path, version = dependency["Path"], dependency["Version"]
        metadata = _json(producer._run([go, "mod", "download", "-json", path + "@" + version],
                                      cwd=check, env=env, phase="module download", timeout=300))
        producer._require(isinstance(metadata, dict) and not metadata.get("Error")
                          and not metadata.get("Replace") and metadata.get("Path") == path
                          and metadata.get("Version") == version and metadata.get("Sum") == dependency["Sum"],
                          "module", "download identity/sum mismatch for " + path)
        directory = Path(metadata["Dir"])
        producer._require(directory.is_absolute(), "module", "requires absolute module directory")
        directories[directory] = _directory(directory, "module")
        material_key = hashlib.sha256((path + "@" + version).encode()).hexdigest()
        files = []
        for relative in _notice_paths(directory, path, directories):
            source = directory / relative
            observed = _remember(source, snapshots, "license")
            destination = Path("licenses") / "modules" / material_key / relative
            files.append({"sourcePath": relative.as_posix(), "path": destination.as_posix(),
                          "size": observed["size"], "SHA256": observed["SHA256"]})
            copies.append((source, destination))
        modules.append({**dependency, "materialKey": material_key,
                        "sourceURL": "https://proxy.golang.org/" + _proxy_escape(path) + "/@v/" + _proxy_escape(version) + ".zip",
                        "files": files})
    producer._run([go, "mod", "verify"], cwd=check, env=env, phase="module verification", timeout=300)
    goroot = Path(producer._run([go, "env", "GOROOT"], cwd=check, env=env, phase="compiler").decode().strip())
    producer._require(goroot.is_absolute(), "compiler", "requires absolute GOROOT")
    directories[goroot] = _directory(goroot, "compiler")
    license_path = goroot / "LICENSE"
    observed = _remember(license_path, snapshots, "Go license")
    copies.append((license_path, Path("licenses") / "go" / "LICENSE"))
    return {"schema": "vibermate.vless-kernel-licenses/v1", "modules": modules,
            "go": {"version": "go1.26.9", "sourceURL": "https://go.dev/dl/go1.26.9.src.tar.gz",
                   "files": [{"path": "licenses/go/LICENSE", "sourcePath": "LICENSE",
                              "size": observed["size"], "SHA256": observed["SHA256"]}]},
            "notice": "Verbatim materials; no legal certification is claimed."}, copies


def _assemble(items, target, binary, work, env, snapshots):
    if target != "darwin/universal":
        _copy(items[0]["binary"], binary, snapshots)
        binary.chmod(stat.S_IMODE(items[0]["binary"].stat().st_mode))
        return
    producer._run(["/usr/bin/lipo", "-create", *[str(item["binary"]) for item in items],
                   "-output", str(binary)], cwd=work / "check", env=env, phase="lipo")
    architectures = producer._run(["/usr/bin/lipo", "-archs", str(binary)],
                                   cwd=work / "check", env=env, phase="lipo").decode().split()
    producer._require(len(architectures) == 2 and set(architectures) == {"arm64", "x86_64"},
                      "lipo", "requires exactly arm64 and x86_64")
    for item in items:
        arch = "x86_64" if item["target"] == "darwin/amd64" else "arm64"
        slice_path = work / ("slice-" + arch)
        producer._run(["/usr/bin/lipo", str(binary), "-thin", arch, "-output", str(slice_path)],
                       cwd=work / "check", env=env, phase="lipo")
        source = snapshots[item["binary"]]
        producer._check_file(slice_path, source["SHA256"], "lipo slice", source["size"])
    modes = {stat.S_IMODE(item["binary"].stat().st_mode) for item in items}
    producer._require(len(modes) == 1, "binary", "slice executable modes differ")
    binary.chmod(modes.pop())


def _failure_record(error, phase, work, identity):
    """Retain closed safe facts only; diagnostic failure never masks the cause."""
    if work is None or identity is None:
        return None
    def exception_type(value):
        name = type(value).__name__
        return name if re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]{0,79}", name) else "Exception"
    try:
        if _directory(work, "diagnostic") != identity or stat.S_IMODE(work.stat().st_mode) != 0o700:
            return None
        facts = {"phase": phase, "exceptionType": exception_type(error)}
        if type(getattr(error, "errno", None)) is int:
            facts["errno"] = error.errno
        if isinstance(error, urllib.error.HTTPError):
            if type(error.code) is int:
                facts["httpStatus"] = error.code
        elif isinstance(error, urllib.error.URLError):
            facts["reasonType"] = exception_type(error.reason)
            if type(getattr(error.reason, "errno", None)) is int:
                facts["reasonErrno"] = error.reason.errno
        record = work / "failure.json"
        descriptor = os.open(record, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
            os.fchmod(stream.fileno(), 0o600)
            stream.write(json.dumps(facts, indent=2) + "\n")
        return record
    except Exception:
        return None


def package_kernel(*, go_binary: str, target: str,
                   build_directories: list[str], output_dir: str) -> dict:
    """Verify read-only producer outputs; emit a fresh executable and materials.

    Return exactly the atomically published final package-manifest data.
    Failed private work remains diagnosable; failures raise bounded KernelError.
    """
    producer._require(target in TARGETS, "target", "unsupported target")
    expected = {"darwin/arm64", "darwin/amd64"} if target == "darwin/universal" else {target}
    producer._require(isinstance(build_directories, list) and len(build_directories) == len(expected),
                      "target", "requires exactly matching producer targets")
    snapshots, directories = {}, {}
    work = work_identity = None
    phase = "output"
    try:
        output = producer._fresh_output(output_dir)
        phase = "input"
        paths = [Path(path).absolute() for path in build_directories]
        producer._require(len(set(paths)) == len(paths), "target", "duplicate producer target")
        for path in paths:
            _directory(path, "input")
            producer._require(path == path.resolve(), "input", "symbolic input path component")
            producer._require(path != output and path not in output.parents,
                              "output", "destination must be outside read-only input")
            _snapshot(path / "kernel-manifest.json", "manifest")
        raw_manifests = [_json((path / "kernel-manifest.json").read_bytes()) for path in paths]
        producer._require(all(isinstance(item, dict) for item in raw_manifests)
                          and {item.get("target") for item in raw_manifests} == expected,
                          "target", "requires exactly matching producer targets")
        phase = "lock"
        lock, assets = _fixed_inputs(snapshots)
        phase = "input verification"
        items = [_input(path, lock, assets, snapshots, directories) for path in paths]
        items.sort(key=lambda item: item["target"])
        producer._require({item["target"] for item in items} == expected,
                          "target", "producer target changed")
        phase = "private work"
        work = Path(tempfile.mkdtemp(prefix="vibermate-kernel-package-check-"))
        work_identity = _directory(work, "private work")
        check = work / "check"
        check.mkdir()
        phase = "compiler"
        env, _ = producer._build_environment(items[0]["target"])
        compiler = producer._compiler(go_binary, env, check)
        _remember(Path(go_binary), snapshots, "compiler", compiler["SHA256"])
        phase = "build info"
        infos = [_verify_metadata(item, go_binary, compiler, lock, check) for item in items]
        dependencies = _dependencies(infos)
        for name in ("go.mod", "go.sum"):
            _copy(items[0]["source"] / name, check / name, snapshots)
        phase = "license collection"
        licenses, notice_copies = _notices(go_binary, dependencies, check, env, snapshots, directories)
        producer._modules(check, lock["moduleSHA256"])
        phase = "archive"
        archive = next((item["path"] / ".work" / "upstream-source.tar.gz" for item in items
                        if (item["path"] / ".work" / "upstream-source.tar.gz").exists()
                        or (item["path"] / ".work" / "upstream-source.tar.gz").is_symlink()), None)
        if archive is None:
            archive = work / "upstream-source.tar.gz"
            producer._download(lock, archive)
        _remember(archive, snapshots, "archive", lock["archiveSHA256"], lock["archiveSize"])
        phase = "output"
        output.mkdir(mode=0o700)
        directories[output] = _directory(output, "output")
        owned_work = output / ".work"
        materials = output / "vless-kernel"
        materials.mkdir()
        owned_work.mkdir(mode=0o700)
        (owned_work / "check").mkdir()
        if (work / "commands.log").exists():
            shutil.copyfile(work / "commands.log", owned_work / "commands.log")
        phase = "materials"
        for name in ASSET_NAMES:
            _copy(assets / name, materials / name, snapshots)
        _copy(items[0]["path"] / "SOURCE-ACCESS.txt", materials / "SOURCE-ACCESS.txt", snapshots)
        _copy(archive, materials / "upstream-source.tar.gz", snapshots)
        for name in ("go.mod", "go.sum"):
            _copy(items[0]["source"] / name, materials / "source" / name, snapshots)
        for item in items:
            _copy(item["path"] / "kernel-manifest.json",
                  materials / "inputs" / item["target"].replace("/", "-") / "kernel-manifest.json", snapshots)
        for source, relative in notice_copies:
            _copy(source, materials / relative, snapshots)
        (materials / "licenses.json").write_text(json.dumps(licenses, indent=2) + "\n", encoding="utf-8")
        phase = "assembly"
        binary = output / "vibermate-xray"
        _assemble(items, target, binary, owned_work, env, snapshots)
        observed = _snapshot(binary, "assembled binary")
        phase = "publication"
        for path, identity in directories.items():
            producer._require(_directory(path, "publication") == identity, "publication", "directory replaced")
        for path in list(snapshots):
            _remember(path, snapshots, "publication")
        inventory = {}
        allowed = set(ASSET_NAMES) | {"SOURCE-ACCESS.txt", "upstream-source.tar.gz", "source/go.mod",
                                      "source/go.sum", "licenses.json"}
        allowed.update("inputs/" + item["target"].replace("/", "-") + "/kernel-manifest.json" for item in items)
        allowed.update(relative.as_posix() for _, relative in notice_copies)
        for path in sorted(materials.rglob("*")):
            if path.is_dir():
                _directory(path, "publication")
                continue
            value = _snapshot(path, "publication")
            relative = path.relative_to(materials).as_posix()
            producer._require(relative in allowed, "publication", "material outside allowlist")
            inventory[relative] = {key: value[key] for key in ("size", "SHA256")}
        producer._require(set(inventory) == allowed, "publication", "missing allowlisted material")
        manifest = {"schema": "vibermate.vless-kernel-package/v1", "target": target,
                    "binary": {"name": "vibermate-xray", "size": observed["size"],
                               "SHA256": observed["SHA256"], "digestStage": "pre-sign"},
                    "inputs": [{"target": item["target"],
                                "manifestSHA256": snapshots[item["path"] / "kernel-manifest.json"]["SHA256"],
                                "binary": item["manifest"]["binary"]} for item in items],
                    "upstream": items[0]["manifest"]["upstream"], "patchSHA256": lock["patchSHA256"],
                    "moduleSHA256": lock["moduleSHA256"], "materials": inventory}
        temporary = materials / ".package-manifest.json.tmp"
        with temporary.open("x", encoding="utf-8") as stream:
            stream.write(json.dumps(manifest, indent=2) + "\n")
        temporary.rename(materials / "package-manifest.json")
        return manifest
    except KernelError:
        raise
    except Exception as error:
        record = _failure_record(error, phase, work, work_identity)
        detail = f"see private failure record {record}" if record is not None else "private failure record unavailable"
        raise KernelError(f"{phase}: failed; {detail}") from error


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", required=True, dest="go_binary", help="absolute Go 1.26.9 executable")
    parser.add_argument("--target", required=True, choices=TARGETS)
    parser.add_argument("--input", required=True, action="append", dest="build_directories",
                        help="completed matching locked producer output (repeat for Universal)")
    parser.add_argument("--output", required=True, dest="output_dir", help="fresh child of a real existing parent")
    try:
        manifest = package_kernel(**vars(parser.parse_args()))
    except KernelError as error:
        parser.exit(1, str(error) + "\n")
    print(json.dumps(manifest, indent=2))


if __name__ == "__main__":
    main()
