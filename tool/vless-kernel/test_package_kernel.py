"""Packaging contract tests using real files and bounded command doubles."""
import copy
import contextlib
import errno
import hashlib
import io
import json
import shutil
import tempfile
import subprocess
import stat
import urllib.error
from pathlib import Path
import unittest
from unittest.mock import patch

import build_kernel as producer
import package_kernel as packaging
from package_kernel import KernelError, package_kernel


class PackageKernelTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.assets = self.root / "assets"
        self.assets.mkdir()
        for name in ("LICENSE.upstream", "dependency.patch", "upstream.commit", "kernel-lock.json"):
            shutil.copyfile(producer.ASSETS / name, self.assets / name)
        self.lock = json.loads((self.assets / "kernel-lock.json").read_bytes())
        self.lock["moduleSHA256"] = {"goMod": hashlib.sha256(b"patched module\n").hexdigest(),
                                    "goSum": hashlib.sha256(b"patched sums\n").hexdigest()}
        self.archive = b"synthetic content-bound archive\n"
        self.lock["archiveSHA256"] = hashlib.sha256(self.archive).hexdigest()
        self.lock["archiveSize"] = len(self.archive)
        (self.assets / "kernel-lock.json").write_text(json.dumps(self.lock))
        self.go = self.root / "go"
        self.go.write_bytes(b"synthetic compiler command boundary\n")
        self.go.chmod(0o755)
        self.goroot = self.root / "goroot"
        self.goroot.mkdir()
        (self.goroot / "LICENSE").write_bytes(b"Verbatim Go license\n")
        self.dependencies = [
            {"Path": "golang.org/x/net", "Version": "v0.60.0", "Sum": "h1:net-fixture"},
            {"Path": "google.golang.org/grpc", "Version": "v1.83.2", "Sum": "h1:grpc-fixture"}]
        self.module_dirs = {}
        for index, dependency in enumerate(self.dependencies):
            directory = self.root / ("module-" + str(index))
            directory.mkdir()
            (directory / "LICENSE").write_bytes(b"Verbatim linked license\r\n")
            (directory / "third_party" / "example").mkdir(parents=True)
            (directory / "third_party" / "example" / "NOTICE.txt").write_bytes(b"Nested third-party notice\n")
            self.module_dirs[dependency["Path"]] = directory
        self.infos = {}
        self.manifests = {}
        self.arm64 = self.make_input("darwin/arm64", "arm64")
        self.amd64 = self.make_input("darwin/amd64", "amd64")
        self.output = self.root / "out"
        self.private_checks = []
        def private_work(*args, **kwargs):
            directory = self.root / ("private-check-" + str(len(self.private_checks)))
            directory.mkdir(mode=0o700)
            self.private_checks.append(directory)
            return str(directory)
        self.addCleanup(patch.stopall)
        patch.object(producer, "_load_inputs", return_value=(self.lock, self.assets)).start()
        self.real_run = producer._run
        patch.object(producer, "_run", side_effect=self.run_command).start()
        patch.object(packaging.tempfile, "mkdtemp", side_effect=private_work).start()

    def source_note(self):
        lock = self.lock
        return (f"Upstream: {lock['repository']}\nRef: {lock['ref']}\nCommit: {lock['commit']}\n"
                f"Source archive: {lock['archiveURL']}\nArchive SHA256: {lock['archiveSHA256']}\n"
                "Apply the adjacent dependency.patch to that exact source.\n"
                "upstream.commit preserves the public Git identity; no independent GPG trust certification is claimed.\n"
                "vcs.modified=true records the exact dependency patch. LICENSE.upstream covers upstream;\n"
                "complete linked-module notices and native/package qualification remain separate requirements.\n")

    def make_input(self, target, name):
        directory = self.root / name
        source = directory / ".work" / "source"
        source.mkdir(parents=True)
        (source / "go.mod").write_bytes(b"patched module\n")
        (source / "go.sum").write_bytes(b"patched sums\n")
        (directory / ".work" / "upstream-source.tar.gz").write_bytes(self.archive)
        for filename in ("LICENSE.upstream", "dependency.patch", "upstream.commit", "kernel-lock.json"):
            shutil.copyfile(self.assets / filename, directory / filename)
        (directory / "SOURCE-ACCESS.txt").write_text(self.source_note())
        binary = directory / "vibermate-xray"
        binary.write_bytes(("binary for " + target).encode())
        binary.chmod(0o755)
        settings = {"GOOS": target.split("/")[0], "GOARCH": target.split("/")[1],
                    "vcs.revision": self.lock["commit"], "vcs.modified": "true",
                    "CGO_ENABLED": "0", "-trimpath": "true"}
        info = {"GoVersion": "go1.26.9", "Deps": copy.deepcopy(self.dependencies),
                "Settings": [{"Key": key, "Value": value} for key, value in settings.items()],
                "Main": {"Path": "github.com/xtls/xray-core", "Version": "v0.0.0-20260327174540-d2758a023cd7+dirty"},
                "Path": "github.com/xtls/xray-core/main"}
        self.infos[str(binary)] = info
        env, controlled = producer._build_environment(target)
        manifest = {
            "schema": "vibermate.vless-kernel-build/v1", "target": target,
            "upstream": {key: self.lock[key] for key in ("repository", "ref", "commit", "tree", "archiveSHA256")},
            "patchSHA256": self.lock["patchSHA256"], "moduleSHA256": self.lock["moduleSHA256"],
            "compiler": {"version": "go1.26.9", "host": "darwin/arm64", "SHA256": hashlib.sha256(self.go.read_bytes()).hexdigest()},
            "build": {"environment": controlled, "argv": [str(self.go), "build", "-buildvcs=true", "-trimpath",
                "-ldflags=-X github.com/xtls/xray-core/core.build=" + self.lock["sourceMarker"],
                "-o", str(directory / ".work" / "vibermate-xray"), "./main"]},
            "binary": {"name": "vibermate-xray", "size": binary.stat().st_size,
                       "SHA256": hashlib.sha256(binary.read_bytes()).hexdigest()},
            "licenseSHA256": self.lock["licenseSHA256"],
            "buildInfo": {"GoVersion": "go1.26.9", "GOOS": settings["GOOS"], "GOARCH": settings["GOARCH"],
                "vcs.revision": self.lock["commit"], "vcs.modified": True, "dependencies": info["Deps"],
                "settings": settings, "main": info["Main"], "path": info["Path"]}}
        self.manifests[str(directory)] = manifest
        self.write_manifest(directory)
        return directory

    def write_manifest(self, directory):
        (directory / "kernel-manifest.json").write_text(json.dumps(self.manifests[str(directory)]))

    def run_command(self, argv, **kwargs):
        if argv[0] == "/usr/bin/lipo":
            raise KernelError("lipo: command failed")
        if argv[1:] == ["version"]:
            return b"go version go1.26.9 darwin/arm64\n"
        if argv[1:3] == ["env", "GOVERSION"]:
            return b"go1.26.9\n"
        if argv[1:3] == ["env", "GOROOT"]:
            return str(self.goroot).encode() + b"\n"
        if argv[1:4] == ["version", "-m", "-json"]:
            return json.dumps(self.infos[str(Path(argv[4]).resolve())]).encode()
        if argv[1:4] == ["mod", "download", "-json"]:
            dependency = next(item for item in self.dependencies
                              if item["Path"] + "@" + item["Version"] == argv[4])
            return json.dumps({**dependency, "Dir": str(self.module_dirs[dependency["Path"]])}).encode()
        if argv[1:] == ["mod", "verify"]:
            return b"all modules verified\n"
        self.fail("Unexpected command: " + repr(argv))

    def call(self, inputs=None, target="darwin/arm64"):
        return package_kernel(go_binary=str(self.go), target=target,
                              build_directories=[str(path) for path in (inputs or [self.arm64])],
                              output_dir=str(self.output))

    def reject(self, pattern, inputs=None, target="darwin/arm64"):
        with self.assertRaisesRegex(KernelError, pattern):
            self.call(inputs, target)
        self.assertFalse((self.output / "vless-kernel" / "package-manifest.json").exists())

    def test_duplicate_universal_architecture_is_rejected_without_success(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            arm64 = root / "arm64"
            arm64.mkdir()
            (arm64 / "kernel-manifest.json").write_text('{"target":"darwin/arm64"}')
            with self.assertRaisesRegex(KernelError, "target"):
                package_kernel(go_binary="/unused/go", target="darwin/universal",
                               build_directories=[str(arm64), str(arm64)],
                               output_dir=str(root / "out"))
            self.assertFalse((root / "out" / "vless-kernel" /
                              "package-manifest.json").exists())

    def test_target_cardinality_and_wrong_architecture(self):
        for inputs, target in (([self.arm64], "darwin/universal"),
                               ([self.arm64, self.amd64], "darwin/arm64"),
                               ([self.amd64], "darwin/arm64")):
            with self.subTest(target=target, inputs=inputs):
                self.reject("target", inputs, target)

    def test_altered_binary_rejected(self):
        (self.arm64 / "vibermate-xray").write_bytes(b"altered")
        self.reject("binary")

    def test_altered_material_rejected(self):
        (self.arm64 / "dependency.patch").write_bytes(b"altered")
        self.reject("material")

    def test_altered_patched_module_rejected(self):
        (self.arm64 / ".work" / "source" / "go.mod").write_bytes(b"altered")
        self.reject("module")

    def test_lock_mismatch_rejected(self):
        self.manifests[str(self.arm64)]["upstream"]["commit"] = "wrong"
        self.write_manifest(self.arm64)
        self.reject("manifest")

    def test_changed_manifest_dependency_evidence_rejected(self):
        self.manifests[str(self.arm64)]["buildInfo"]["dependencies"] = []
        self.write_manifest(self.arm64)
        self.reject("build info")

    def test_binary_metadata_replacement_rejected(self):
        self.infos[str(self.arm64 / "vibermate-xray")]["Deps"][0]["Replace"] = {"Path": "evil"}
        self.reject("build info")

    def test_duplicate_json_keys_rejected(self):
        manifest = self.arm64 / "kernel-manifest.json"
        manifest.write_text(manifest.read_text().replace('{', '{"target":"darwin/arm64",', 1))
        self.reject("JSON")

    def test_symbolic_input_rejected(self):
        symbolic = self.root / "symbolic"
        symbolic.symlink_to(self.arm64, target_is_directory=True)
        self.reject("input", [symbolic])

    def test_symbolic_material_rejected(self):
        material = self.arm64 / "dependency.patch"
        material.unlink()
        material.symlink_to(self.assets / "dependency.patch")
        self.reject("material")

    def test_existing_output_preserves_unowned_sentinel(self):
        self.output.mkdir()
        sentinel = self.output / "sentinel"
        sentinel.write_bytes(b"leave untouched")
        self.reject("output")
        self.assertEqual(sentinel.read_bytes(), b"leave untouched")

    def test_competing_output_preserves_unowned_sentinel(self):
        mkdir = Path.mkdir
        def race(path, *args, **kwargs):
            if path == self.output:
                mkdir(path)
                (path / "sentinel").write_bytes(b"competitor")
            return mkdir(path, *args, **kwargs)
        with patch.object(Path, "mkdir", race):
            self.reject("output")
        self.assertEqual((self.output / "sentinel").read_bytes(), b"competitor")

    def test_missing_notice_names_module(self):
        shutil.rmtree(self.module_dirs["golang.org/x/net"])
        self.module_dirs["golang.org/x/net"].mkdir()
        self.reject("license.*golang.org/x/net")

    def test_module_sum_mismatch_rejected(self):
        original = self.run_command
        def mismatch(argv, **kwargs):
            data = original(argv, **kwargs)
            if argv[1:4] == ["mod", "download", "-json"]:
                value = json.loads(data)
                value["Sum"] = "h1:wrong"
                return json.dumps(value).encode()
            return data
        with patch.object(producer, "_run", side_effect=mismatch):
            self.reject("module.*sum")

    def test_failing_lipo_has_no_success_manifest(self):
        self.reject("lipo", [self.arm64, self.amd64], "darwin/universal")

    def test_verbatim_nested_notices_and_inventory(self):
        returned = self.call()
        materials = self.output / "vless-kernel"
        inventory = json.loads((materials / "licenses.json").read_bytes())
        modules = inventory["modules"]
        self.assertEqual(len(modules), 2)
        for module in modules:
            self.assertEqual({item["sourcePath"] for item in module["files"]},
                             {"LICENSE", "third_party/example/NOTICE.txt"})
            for item in module["files"]:
                expected = b"Verbatim linked license\r\n" if item["sourcePath"] == "LICENSE" else b"Nested third-party notice\n"
                actual = (materials / item["path"]).read_bytes()
                self.assertEqual(actual, expected)
                self.assertEqual(item["SHA256"], hashlib.sha256(expected).hexdigest())
        manifest = json.loads((materials / "package-manifest.json").read_bytes())
        self.assertEqual(returned, manifest)
        self.assertEqual(manifest["binary"]["SHA256"], hashlib.sha256(b"binary for darwin/arm64").hexdigest())
        self.assertEqual(manifest["binary"]["digestStage"], "pre-sign")
        self.assertNotIn("package-manifest.json", manifest["materials"])
        self.assertFalse(any(".work" in key or ".git" in key for key in manifest["materials"]))
        self.assertFalse(any("failure" in key for key in manifest["materials"]))
        self.assertFalse((self.private_checks[-1] / "failure.json").exists())

    def cli_failure(self, error):
        (self.arm64 / ".work" / "upstream-source.tar.gz").unlink()
        stderr, stdout = io.StringIO(), io.StringIO()
        argv = ["package_kernel.py", "--go", str(self.go), "--target", "darwin/arm64",
                "--input", str(self.arm64), "--output", str(self.output)]
        with patch("sys.argv", argv), patch.object(producer, "_download", side_effect=error), \
             contextlib.redirect_stderr(stderr), contextlib.redirect_stdout(stdout):
            with self.assertRaises(SystemExit) as caught:
                packaging.main()
        self.assertEqual(caught.exception.code, 1)
        self.assertEqual(stdout.getvalue(), "")
        self.assertFalse(self.output.exists())
        message = stderr.getvalue()
        self.assertTrue(message.startswith("archive: failed;"))
        self.assertNotIn("credential-secret", message)
        self.assertNotIn("https://", message)
        self.assertNotIn("Traceback", message)
        self.assertEqual(len(message.splitlines()), 1)
        return message

    def test_cli_http_failure_retains_private_safe_status(self):
        error = urllib.error.HTTPError("https://user:credential-secret@example.invalid", 502,
                                       "credential-secret raw message", {"Authorization": "credential-secret"}, None)
        message = self.cli_failure(error)
        record = self.private_checks[-1] / "failure.json"
        self.assertTrue(record.is_file(), "typed failure facts were not retained")
        self.assertIn(str(record), message)
        self.assertEqual(stat.S_IMODE(record.parent.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE(record.stat().st_mode), 0o600)
        data = record.read_text()
        self.assertEqual(json.loads(data), {"phase": "archive", "exceptionType": "HTTPError", "httpStatus": 502})
        self.assertNotIn("credential-secret", data)
        self.assertNotIn("https://", data)

    def test_cli_wrapped_os_failure_retains_only_reason_type_and_errno(self):
        error = urllib.error.URLError(OSError(errno.ECONNRESET, "credential-secret https://user:credential-secret@example.invalid"))
        message = self.cli_failure(error)
        record = self.private_checks[-1] / "failure.json"
        self.assertTrue(record.is_file(), "wrapped errno was not retained")
        self.assertIn(str(record), message)
        data = record.read_text()
        self.assertEqual(json.loads(data), {"phase": "archive", "exceptionType": "URLError",
                                           "reasonType": "ConnectionResetError", "reasonErrno": errno.ECONNRESET})
        self.assertNotIn("credential-secret", data)

    def test_failure_before_private_work_has_no_diagnostic_claim(self):
        with patch.object(producer, "_fresh_output", side_effect=OSError(errno.EACCES, "credential-secret")):
            with self.assertRaises(KernelError) as caught:
                self.call()
        message = str(caught.exception)
        self.assertTrue(message.startswith("output: failed;"))
        self.assertIn("failure record unavailable", message)
        self.assertNotIn("credential-secret", message)
        self.assertEqual(self.private_checks, [])
        self.assertFalse(self.output.exists())

    def test_failure_record_write_error_preserves_original_phase(self):
        original = packaging.os.open
        def reject_record(path, *args, **kwargs):
            if Path(path).name == "failure.json":
                raise OSError(errno.ENOSPC, "credential-secret failed record")
            return original(path, *args, **kwargs)
        with patch.object(packaging.os, "open", side_effect=reject_record):
            message = self.cli_failure(OSError(errno.EIO, "credential-secret original archive failure"))
        self.assertIn("failure record unavailable", message)
        self.assertNotIn("failure.json", message)
        self.assertFalse((self.private_checks[-1] / "failure.json").exists())

    def test_missing_binary_digest_is_rejected(self):
        del self.manifests[str(self.arm64)]["binary"]["SHA256"]
        self.write_manifest(self.arm64)
        self.reject("binary")

    def test_input_through_symbolic_parent_is_rejected(self):
        alias = self.root / "alias"
        alias.symlink_to(self.root, target_is_directory=True)
        self.reject("input", [alias / "arm64"])

    def test_missing_input_manifest_is_bounded(self):
        (self.arm64 / "kernel-manifest.json").unlink()
        self.reject("input")

    def test_missing_patched_module_is_bounded(self):
        (self.arm64 / ".work" / "source" / "go.mod").unlink()
        self.reject("module")

    def test_symbolic_archive_is_rejected(self):
        archive = self.arm64 / ".work" / "upstream-source.tar.gz"
        archive.unlink()
        archive.symlink_to(self.root / "missing-archive")
        with patch.object(producer, "_download", side_effect=AssertionError("must reject symbolic archive")):
            self.reject("archive.*regular")

    def test_wrong_compiler_version_has_no_owned_output(self):
        original = self.run_command
        def wrong_version(argv, **kwargs):
            return b"go version go1.26.8 darwin/arm64\n" if argv[1:] == ["version"] else original(argv, **kwargs)
        with patch.object(producer, "_run", side_effect=wrong_version):
            self.reject("compiler.*go1.26.9")
        self.assertFalse(self.output.exists())

    def test_output_beneath_input_cannot_modify_producer(self):
        self.output = self.arm64 / "package-child"
        self.reject("output.*input")
        self.assertFalse(self.output.exists())

    def test_binary_go_target_mismatch_is_rejected(self):
        info = self.infos[str(self.arm64 / "vibermate-xray")]
        next(item for item in info["Settings"] if item["Key"] == "GOARCH")["Value"] = "amd64"
        self.reject("build info")

    def test_patch_manifest_mismatch_rejected(self):
        self.manifests[str(self.arm64)]["patchSHA256"] = "0" * 64
        self.write_manifest(self.arm64)
        self.reject("manifest")

    def test_replaced_same_byte_binary_is_rejected_before_publication(self):
        original = packaging._assemble
        def replace(*args, **kwargs):
            original(*args, **kwargs)
            binary = self.arm64 / "vibermate-xray"
            replacement = self.arm64 / "replacement"
            replacement.write_bytes(binary.read_bytes())
            replacement.chmod(0o755)
            replacement.replace(binary)
        with patch.object(packaging, "_assemble", side_effect=replace):
            self.reject("publication.*changed")

    def test_unexpected_material_cannot_enter_inventory(self):
        original = packaging._assemble
        def extra(*args, **kwargs):
            original(*args, **kwargs)
            (self.output / "vless-kernel" / "unexpected-private-key").write_bytes(b"unallowed")
        with patch.object(packaging, "_assemble", side_effect=extra):
            self.reject("publication.*allowlist")

    def test_failed_command_is_bounded_and_retains_sanitized_diagnostics(self):
        # Exercise the real producer subprocess boundary, with a failing command result.
        failed = subprocess.CompletedProcess([], 1, b"https://user:password@example.invalid\n", b"failure\n")
        with patch.object(producer, "_run", side_effect=self.real_run), \
             patch.object(subprocess, "run", return_value=failed):
            with self.assertRaises(KernelError) as caught:
                self.call()
        self.assertNotIn("password", str(caught.exception))
        self.assertIn("compiler", str(caught.exception))
        self.assertFalse(self.output.exists())
        log = (self.private_checks[-1] / "commands.log").read_text()
        self.assertIn("exit=1", log)
        self.assertNotIn("password", log)

    def test_lipo_architecture_and_slice_bytes_are_checked(self):
        for corrupt in ("architectures", "slice"):
            with self.subTest(corrupt=corrupt):
                self.output = self.root / ("out-" + corrupt)
                original = self.run_command
                def lipo(argv, **kwargs):
                    if argv[0] != "/usr/bin/lipo":
                        return original(argv, **kwargs)
                    if argv[1] == "-create":
                        Path(argv[-1]).write_bytes(b"synthetic assembled output")
                        return b""
                    if argv[1] == "-archs":
                        return b"arm64" if corrupt == "architectures" else b"arm64 x86_64"
                    Path(argv[-1]).write_bytes(b"incorrect slice")
                    return b""
                with patch.object(producer, "_run", side_effect=lipo):
                    self.reject("lipo", [self.arm64, self.amd64], "darwin/universal")


if __name__ == "__main__":
    unittest.main()
