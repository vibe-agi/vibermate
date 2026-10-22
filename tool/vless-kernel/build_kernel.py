"""Produce a content-bound Xray kernel using only the adjacent locked inputs."""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import subprocess
import tarfile
import urllib.request

TARGETS = ("darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64", "windows/amd64")
ASSETS = Path(__file__).resolve().parent


class KernelError(RuntimeError):
    """A bounded, non-secret build phase failure."""


def _sha(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _require(condition, phase, detail):
    if not condition:
        raise KernelError(f"{phase}: {detail}")


def _safe_output(data):
    # Tool diagnostics may contain credential-bearing proxy/module URLs.
    return re.sub(r"https?://[^\s]+", "[URL]", data.decode("utf-8", "replace"))


def _run(argv, *, cwd, env, phase, input=None, timeout=120):
    try:
        result = subprocess.run(argv, cwd=cwd, env=env, input=input,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                timeout=timeout, check=False)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise KernelError(f"{phase}: command unavailable or timed out") from error
    log = Path(cwd).parent / "commands.log"
    with log.open("a", encoding="utf-8") as stream:
        stream.write(f"phase={phase} exit={result.returncode}\n")
        stream.write(_safe_output(result.stdout) + _safe_output(result.stderr))
    _require(result.returncode == 0, phase, "command failed; see owned commands.log")
    return result.stdout


def _git_environment():
    env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    env.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
               GIT_TERMINAL_PROMPT="0", GIT_ATTR_NOSYSTEM="1")
    return env


def _git(source, *args, input=None):
    return _run(["git", "-c", "core.hooksPath=" + os.devnull,
                 "-c", "core.autocrlf=false", "-c", "core.filemode=true",
                 "-c", "core.ignorecase=false", *args], cwd=source,
                env=_git_environment(), phase="source Git", input=input)


def _load_inputs():
    return json.loads((ASSETS / "kernel-lock.json").read_bytes()), ASSETS


def _check_file(path, digest, phase, size=None):
    _require(path.is_file() and not path.is_symlink(), phase, "expected regular file")
    if size is not None:
        _require(path.stat().st_size == size, phase, "size mismatch")
    _require(_sha(path) == digest, phase, "SHA256 mismatch")


def _extract(archive, source, commit):
    prefix = "Xray-core-" + commit
    with tarfile.open(archive, "r:gz") as tar:
        entries = tar.getmembers()
        seen = set()
        for member in entries:
            name = member.name
            parts = PurePosixPath(name).parts
            _require(bool(parts) and parts[0] == prefix and not name.startswith("/")
                     and ".." not in parts and "\\" not in name and ".git" not in parts,
                     "archive", "unsafe member path")
            relative = tuple(parts[1:])
            _require(relative not in seen, "archive", "duplicate member")
            seen.add(relative)
            _require(member.isdir() or member.isfile(), "archive", "link or special member")
            _require(0 <= member.size <= 20_000_000, "archive", "member size exceeds limit")
            _require(bool(relative) or member.isdir(), "archive", "invalid prefix entry")
        # Validate every member before creating any extracted files.
        for member in entries:
            relative = PurePosixPath(member.name).parts[1:]
            if not relative:
                continue
            dest = source.joinpath(*relative)
            if member.isdir():
                dest.mkdir(parents=True, exist_ok=True)
            else:
                dest.parent.mkdir(parents=True, exist_ok=True)
                with tar.extractfile(member) as stream, dest.open("xb") as output:
                    shutil.copyfileobj(stream, output)
                _require(dest.stat().st_size == member.size, "archive", "truncated member")
                dest.chmod(0o755 if member.mode & 0o111 else 0o644)


def _modules(source, expected):
    for filename, field in [("go.mod", "goMod"), ("go.sum", "goSum")]:
        _check_file(source / filename, expected[field], "module")


def _prepare_source(archive, source, lock, assets):
    """Internal fixture seam; production callers always use adjacent lock/assets."""
    _require(source.is_dir() and not source.is_symlink() and not any(source.iterdir()),
             "source", "requires empty owned source directory")
    _check_file(archive, lock["archiveSHA256"], "archive", lock["archiveSize"])
    for filename, field in [("upstream.commit", "rawCommitSHA256"),
                            ("dependency.patch", "patchSHA256"),
                            ("LICENSE.upstream", "licenseSHA256")]:
        _check_file(assets / filename, lock[field], "asset")
    raw = (assets / "upstream.commit").read_bytes()
    object_id = hashlib.sha1(b"commit " + str(len(raw)).encode() + b"\0" + raw).hexdigest()
    _require(object_id == lock["commit"], "commit", "Git object identity mismatch")
    _extract(archive, source, lock["commit"])
    _git(source, "init", "--template=")
    _git(source, "add", "--force", "--all")
    tree = _git(source, "write-tree").decode().strip()
    _require(tree == lock["tree"], "tree", "full source tree mismatch")
    commit = _git(source, "hash-object", "-w", "-t", "commit", "--stdin", input=raw).decode().strip()
    _require(commit == lock["commit"], "commit", "Git object identity mismatch")
    (source / ".git" / "shallow").write_text(commit + "\n", encoding="ascii")
    _git(source, "checkout", "--detach", commit)
    _require(_git(source, "status", "--porcelain=v1") == b"", "source", "not clean before patch")
    _modules(source, lock["pristineModuleSHA256"])
    _git(source, "apply", "--check", str(assets / "dependency.patch"))
    _git(source, "apply", str(assets / "dependency.patch"))
    _modules(source, lock["moduleSHA256"])
    _patched_source(source, lock)


def _patched_source(source, lock):
    expected = lock.get("patchedFiles")
    _require(isinstance(expected, dict) and bool(expected), "patch", "missing closed file inventory")
    changed = set(_git(source, "diff", "HEAD", "--name-only").decode().splitlines())
    added = set(_git(source, "ls-files", "--others").decode().splitlines())
    _require(changed | added == set(expected), "patch", "unexpected source modifications")
    for name, record in expected.items():
        path = PurePosixPath(name)
        _require(isinstance(name, str) and not path.is_absolute() and ".." not in path.parts
                 and ".git" not in path.parts and "\\" not in name and str(path) == name,
                 "patch", "unsafe file inventory")
        _require(isinstance(record, dict) and set(record) == {"sha256", "mode"}
                 and record["mode"] in ("100644", "100755"), "patch", "invalid file record")
        item = source / name
        _check_file(item, record["sha256"], "patch")
        _require(oct(item.stat().st_mode & 0o777)[2:] == record["mode"][3:],
                 "patch", "file mode mismatch")
    if added:
        _git(source, "add", "--intent-to-add", "--force", "--", *sorted(added))


def _build_environment(target):
    goos, goarch = target.split("/")
    controlled = {
        "GOTOOLCHAIN": "local", "GOWORK": "off", "GOENV": "off",
        "GOFLAGS": "-mod=readonly", "GOPROXY": "https://proxy.golang.org",
        "GOSUMDB": "sum.golang.org", "GONOSUMDB": "", "GONOPROXY": "",
        "GOPRIVATE": "", "CGO_ENABLED": "0", "GOOS": goos, "GOARCH": goarch,
        "GOAMD64": "v1", "GOARM64": "v8.0", "GOEXPERIMENT": "",
        "GO111MODULE": "on", "GOFIPS140": "off",
    }
    env = _git_environment()
    for name in ("GOROOT", "GOTOOLDIR", "GODEBUG", "GOCACHEPROG", "GO_EXTLINK_ENABLED"):
        env.pop(name, None)
    env.update(controlled)
    return env, controlled


def _compiler(go_binary, env, cwd):
    go = Path(go_binary)
    _require(go.is_absolute() and go.is_file() and os.access(go, os.X_OK),
             "compiler", "requires absolute existing executable")
    version = _run([str(go), "version"], cwd=cwd, env=env, phase="compiler").decode().strip()
    parts = version.split()
    _require(len(parts) == 4 and parts[:3] == ["go", "version", "go1.26.10"]
             and re.fullmatch(r"[a-z0-9]+/[a-z0-9]+", parts[3]) is not None,
             "compiler", "requires go1.26.10")
    goversion = _run([str(go), "env", "GOVERSION"], cwd=cwd, env=env, phase="compiler").decode().strip()
    _require(goversion == "go1.26.10", "compiler", "GOVERSION mismatch")
    return {"version": goversion, "host": parts[3], "SHA256": _sha(go)}


def _read_build_info(go, binary, source, env, target, lock):
    data = _run([go, "version", "-m", "-json", str(binary)], cwd=source,
                env=env, phase="build info")
    info = json.loads(data)
    settings = {item["Key"]: item["Value"] for item in info.get("Settings", [])}
    deps = info.get("Deps", [])
    versions = {item["Path"]: item["Version"] for item in deps}
    _require(info.get("GoVersion") == "go1.26.10" and
             settings.get("GOOS", "") + "/" + settings.get("GOARCH", "") == target and
             settings.get("vcs.revision") == lock["commit"] and
             settings.get("vcs.modified") == "true" and
             settings.get("CGO_ENABLED") == "0" and settings.get("-trimpath") == "true" and
             versions.get("golang.org/x/net") == "v0.60.0" and
             versions.get("google.golang.org/grpc") == "v1.83.2" and
             not any(item.get("Replace") for item in deps),
             "build info", "compiler/target/VCS/dependency mismatch")
    return {"GoVersion": info["GoVersion"], "GOOS": settings["GOOS"],
            "GOARCH": settings["GOARCH"], "vcs.revision": settings["vcs.revision"],
            "vcs.modified": True, "dependencies": deps, "settings": settings,
            "main": info.get("Main"), "path": info.get("Path")}


def _fresh_output(output_dir):
    output = Path(output_dir).absolute()
    _require(not output.exists() and not output.is_symlink(), "output", "refuses existing path")
    _require(output.parent.is_dir() and not output.parent.is_symlink(),
             "output", "requires existing real parent directory")
    output = output.parent.resolve() / output.name
    _require(output != Path.home().resolve() and output != Path(output.anchor)
             and not (output / ".git").exists(), "output", "unsafe destination")
    return output


def _download(lock, archive):
    url = lock["archiveURL"]
    _require(url.startswith("https://codeload.github.com/"), "archive", "requires locked HTTPS URL")
    # urllib uses ordinary environment proxies and the system TLS verifier.
    with _open_url(url) as response, archive.open("xb") as stream:
        _require(response.url == url, "archive", "unexpected redirect")
        remaining = lock["archiveSize"] + 1
        while remaining:
            chunk = response.read(min(remaining, 1024 * 1024))
            if not chunk:
                break
            stream.write(chunk)
            remaining -= len(chunk)


def _open_url(url):
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, request, response, code, message, headers, new_url):
            return None
    return urllib.request.build_opener(NoRedirect()).open(url, timeout=60)


def build_kernel(*, go_binary: str, target: str, output_dir: str,
                 archive_path: str | None = None) -> dict:
    """Build into a new child directory and return its exact success manifest."""
    _require(target in TARGETS, "target", "unsupported target")
    try:
        output = _fresh_output(output_dir)
    except (OSError, ValueError) as error:
        raise KernelError("output: cannot validate destination") from error
    try:
        lock, assets = _load_inputs()
    except (OSError, ValueError) as error:
        raise KernelError("lock: cannot read adjacent source lock") from error
    env, controlled = _build_environment(target)
    # Compiler checks precede output ownership. Diagnostics use a fresh private
    # directory rather than writing beside an unowned caller path.
    import tempfile
    try:
        with tempfile.TemporaryDirectory(prefix="vibermate-kernel-compiler-") as temp:
            cwd = Path(temp) / "check"
            cwd.mkdir()
            compiler = _compiler(go_binary, env, cwd)
    except (OSError, ValueError) as error:
        raise KernelError("compiler: validation failed") from error
    owned_output = False
    work = None
    binary = None
    name = "vibermate-xray.exe" if target.startswith("windows/") else "vibermate-xray"
    phase = "output"
    try:
        output.mkdir(mode=0o700)
        owned_output = True
        work = output / ".work"
        work.mkdir()
        source = work / "source"
        source.mkdir()
        binary = work / name
        phase = "archive"
        archive = Path(archive_path).resolve() if archive_path else work / "upstream-source.tar.gz"
        if archive_path is None:
            _download(lock, archive)
        phase = "source preparation"
        _prepare_source(archive, source, lock, assets)
        phase = "build"
        argv = [go_binary, "build", "-buildvcs=true", "-trimpath",
                "-ldflags=-X github.com/xtls/xray-core/core.build=" + lock["sourceMarker"],
                "-o", str(binary), "./main"]
        _run(argv, cwd=source, env=env, phase="build", timeout=900)
        _modules(source, lock["moduleSHA256"])
        _patched_source(source, lock)
        phase = "build info"
        _require(binary.exists() and stat.S_ISREG(binary.lstat().st_mode)
                 and binary.stat().st_size > 0, "binary", "missing regular output")
        info = _read_build_info(go_binary, binary, source, env, target, lock)
        phase = "publication"
        manifest = {
            "schema": "vibermate.vless-kernel-build/v1", "target": target,
            "upstream": {key: lock[key] for key in ("repository", "ref", "commit", "tree", "archiveSHA256")},
            "patchSHA256": lock["patchSHA256"], "moduleSHA256": lock["moduleSHA256"],
            "compiler": compiler, "build": {"argv": argv, "environment": controlled},
            "binary": {"name": name, "size": binary.stat().st_size, "SHA256": _sha(binary)},
            "buildInfo": info, "licenseSHA256": lock["licenseSHA256"],
        }
        for filename in ("LICENSE.upstream", "dependency.patch", "upstream.commit", "kernel-lock.json"):
            shutil.copyfile(assets / filename, output / filename)
        (output / "SOURCE-ACCESS.txt").write_text(
            f"Upstream: {lock['repository']}\nRef: {lock['ref']}\nCommit: {lock['commit']}\n"
            f"Source archive: {lock['archiveURL']}\nArchive SHA256: {lock['archiveSHA256']}\n"
            "Apply the adjacent dependency.patch to that exact source.\n"
            "upstream.commit preserves the public Git identity; no independent GPG trust certification is claimed.\n"
            "vcs.modified=true records the exact dependency and connection-lifetime patch. LICENSE.upstream covers upstream;\n"
            "complete linked-module notices and native/package qualification remain separate requirements.\n",
            encoding="utf-8")
        binary.rename(output / name)
        manifest_temp = output / ".kernel-manifest.json.tmp"
        manifest_temp.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
        manifest_temp.rename(output / "kernel-manifest.json")
        return manifest
    except Exception as error:
        # Setup refusal must never touch a path created by a competing caller.
        # Failed owned setup may not yet have a work directory or binary path.
        if owned_output and binary is not None:
            try:
                if (output / name).exists():
                    (output / name).rename(work / (name + ".failed"))
                if binary.exists():
                    binary.rename(work / (name + ".failed"))
            except OSError:
                raise KernelError(f"{phase}: failed; rejected output retention failed") from error
        if isinstance(error, KernelError):
            raise
        if phase == "output":
            raise KernelError("output: cannot create owned output directories") from error
        raise KernelError(f"{phase}: failed; inspect owned .work diagnostics") from error


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", required=True, dest="go_binary", help="absolute Go 1.26.10 executable")
    parser.add_argument("--target", required=True, choices=TARGETS)
    parser.add_argument("--output", required=True, dest="output_dir", help="fresh child of an existing real parent")
    parser.add_argument("--archive", dest="archive_path", help="optional exact locked source archive")
    args = parser.parse_args()
    try:
        manifest = build_kernel(**vars(args))
    except KernelError as error:
        parser.exit(1, str(error) + "\n")
    print(json.dumps(manifest, indent=2))


if __name__ == "__main__":
    main()
