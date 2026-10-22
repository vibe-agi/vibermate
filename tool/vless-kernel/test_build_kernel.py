"""Behavioral contracts for the locked producer."""
import tempfile
import unittest
import hashlib
import http.server
import io
import json
import os
import subprocess
import sys
import tarfile
import threading
import urllib.error
from unittest.mock import patch
from pathlib import Path

import build_kernel as producer


class Contracts(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def fixture(self, entries=None):
        assets = self.root / "assets"
        assets.mkdir()
        tree_dir = self.root / "original"
        tree_dir.mkdir()
        files = {".gitignore": b"*.dat\n", "ignored.dat": b"tracked\n",
                 "go.mod": b"module fixture\n", "go.sum": b"old\n",
                 "run": b"#!/bin/sh\n"}
        for name, value in files.items():
            (tree_dir / name).write_bytes(value)
        (tree_dir / "run").chmod(0o755)
        env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull)
        def git(*argv, input=None):
            return subprocess.check_output(["git", *argv], cwd=tree_dir, env=env,
                                           input=input, stderr=subprocess.DEVNULL)
        git("init", "--template=")
        git("add", "--force", "--all")
        tree = git("write-tree").decode().strip()
        raw = (f"tree {tree}\nauthor Fixture <fixture@example.invalid> 1 +0000\n"
               "committer Fixture <fixture@example.invalid> 1 +0000\n\nfixture\n").encode()
        commit = git("hash-object", "-t", "commit", "--stdin", input=raw).decode().strip()
        (assets / "upstream.commit").write_bytes(raw)
        (assets / "LICENSE.upstream").write_bytes(b"fixture license\n")
        (tree_dir / "go.sum").write_bytes(b"new\n")
        delta = git("diff", "--", "go.sum")
        (assets / "dependency.patch").write_bytes(delta)
        archive = self.root / "archive.tar.gz"
        with tarfile.open(archive, "w:gz") as tar:
            for name, value in (entries if entries is not None else files.items()):
                member = tarfile.TarInfo("Xray-core-" + commit + "/" + name)
                if isinstance(value, tarfile.TarInfo):
                    member = value
                    if not member.name.startswith("/"):
                        member.name = "Xray-core-" + commit + "/" + name
                    tar.addfile(member)
                else:
                    member.size = len(value)
                    member.mode = 0o755 if name == "run" else 0o644
                    tar.addfile(member, io.BytesIO(value))
        digest = lambda data: hashlib.sha256(data).hexdigest()
        lock = {"commit": commit, "tree": tree, "archiveSize": archive.stat().st_size,
                "archiveSHA256": digest(archive.read_bytes()), "rawCommitSHA256": digest(raw),
                "patchSHA256": digest(delta), "licenseSHA256": digest(b"fixture license\n"),
                "pristineModuleSHA256": {"goMod": digest(files["go.mod"]), "goSum": digest(b"old\n")},
                "moduleSHA256": {"goMod": digest(files["go.mod"]), "goSum": digest(b"new\n")}}
        lock.update(repository="https://example.invalid/fixture.git", ref="fixture", archiveURL="https://example.invalid/fixture.tar.gz", sourceMarker="d2758a0-vibermate-security1")
        (assets / "kernel-lock.json").write_text(json.dumps(lock))
        source = self.root / "source"
        source.mkdir()
        return archive, source, lock, assets

    def prepare(self, fixture):
        archive, source, lock, assets = fixture
        return producer._prepare_source(archive, source, lock, assets)

    def test_exact_tree_includes_ignored_tracked_file_and_preserves_mode(self):
        f = self.fixture()
        self.prepare(f)
        self.assertEqual((f[1] / "ignored.dat").read_bytes(), b"tracked\n")
        self.assertEqual((f[1] / "run").stat().st_mode & 0o777, 0o755)
        self.assertEqual((f[1] / "go.sum").read_bytes(), b"new\n")
        self.assertEqual(subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=f[1]).decode().strip(), f[2]["commit"])

    def test_bad_archive_size_and_hash_are_rejected_before_extraction(self):
        f = self.fixture()
        for field, value in [("archiveSize", 1), ("archiveSHA256", "0" * 64)]:
            with self.subTest(field=field):
                lock = dict(f[2], **{field: value})
                with self.assertRaises(producer.KernelError):
                    self.prepare((f[0], f[1], lock, f[3]))
                self.assertEqual(list(f[1].iterdir()), [])

    def test_bad_pinned_assets_are_rejected_before_extraction(self):
        f = self.fixture()
        for field in ["rawCommitSHA256", "patchSHA256", "licenseSHA256"]:
            with self.subTest(field=field):
                with self.assertRaises(producer.KernelError):
                    self.prepare((f[0], f[1], dict(f[2], **{field: "0" * 64}), f[3]))
                self.assertEqual(list(f[1].iterdir()), [])

    def test_tree_mismatch_catches_omitted_ignored_file(self):
        f = self.fixture(entries=[("go.mod", b"module fixture\n"), ("go.sum", b"old\n")])
        with self.assertRaisesRegex(producer.KernelError, "tree"):
            self.prepare(f)

    def test_bad_module_hash_is_rejected(self):
        f = self.fixture()
        f[2]["pristineModuleSHA256"]["goMod"] = "0" * 64
        with self.assertRaisesRegex(producer.KernelError, "module"):
            self.prepare(f)

    def test_bad_patched_module_hash_is_rejected(self):
        f = self.fixture()
        f[2]["moduleSHA256"]["goSum"] = "0" * 64
        with self.assertRaisesRegex(producer.KernelError, "module"):
            self.prepare(f)

    def test_raw_commit_hash_alone_cannot_replace_git_identity(self):
        f = self.fixture()
        raw = (f[3] / "upstream.commit").read_bytes().replace(b"fixture\n", b"tamper\n")
        (f[3] / "upstream.commit").write_bytes(raw)
        f[2]["rawCommitSHA256"] = hashlib.sha256(raw).hexdigest()
        with self.assertRaisesRegex(producer.KernelError, "commit"):
            self.prepare(f)
        self.assertEqual(list(f[1].iterdir()), [])

    def test_invalid_patch_is_not_applied(self):
        f = self.fixture()
        delta = (f[3] / "dependency.patch").read_bytes().replace(b"-old", b"-absent")
        (f[3] / "dependency.patch").write_bytes(delta)
        f[2]["patchSHA256"] = hashlib.sha256(delta).hexdigest()
        with self.assertRaises(producer.KernelError):
            self.prepare(f)
        self.assertEqual((f[1] / "go.sum").read_bytes(), b"old\n")

    def test_unsafe_members_are_rejected(self):
        cases = ["../escape", "/absolute", "bad\\name", ".git/config", "dup"]
        for name in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as parent:
                old_root, self.root = self.root, Path(parent)
                try:
                    entries = [(name, b"a"), (name, b"b")] if name == "dup" else [(name, b"a")]
                    if name == "/absolute":
                        member = tarfile.TarInfo("/absolute")
                        entries = [(name, member)]
                    f = self.fixture(entries)
                    with self.assertRaises(producer.KernelError):
                        self.prepare(f)
                    self.assertFalse((Path(parent) / "escape").exists())
                finally:
                    self.root = old_root

    def test_link_member_is_rejected(self):
        member = tarfile.TarInfo("Xray-core-fake/link")
        member.type = tarfile.SYMTYPE
        member.linkname = "/tmp"
        f = self.fixture([("link", member)])
        with self.assertRaises(producer.KernelError):
            self.prepare(f)
        self.assertEqual(list(f[1].iterdir()), [])

    def test_special_member_is_rejected(self):
        member = tarfile.TarInfo("fifo")
        member.type = tarfile.FIFOTYPE
        f = self.fixture([("fifo", member)])
        with self.assertRaises(producer.KernelError):
            self.prepare(f)
        self.assertEqual(list(f[1].iterdir()), [])

    def test_wrong_compiler_is_rejected_without_success_marker(self):
        executable = self.root / "go"
        executable.write_text("#!/bin/sh\nprintf 'go version go1.20 darwin/arm64\\n'\n")
        executable.chmod(0o755)
        output = self.root / "out"
        with self.assertRaisesRegex(producer.KernelError, "compiler"):
            producer.build_kernel(go_binary=str(executable), target="darwin/arm64", output_dir=str(output))
        self.assertFalse((output / "kernel-manifest.json").exists())

    def test_existing_and_symlink_outputs_are_rejected(self):
        existing = self.root / "exists"
        existing.mkdir()
        link = self.root / "link"
        link.symlink_to(existing, target_is_directory=True)
        for output in [existing, link]:
            with self.subTest(output=output), self.assertRaises(producer.KernelError):
                producer.build_kernel(go_binary="/missing/go", target="darwin/arm64", output_dir=str(output))
        self.assertEqual(list(existing.iterdir()), [])

    def test_failed_build_leaves_no_binary_or_manifest(self):
        go = self.root / "go"
        go.write_text("#!/bin/sh\ncase \"$1\" in\nversion) printf 'go version go1.26.10 darwin/arm64\\n';;\nenv) printf 'go1.26.10\\n';;\nbuild) exit 3;;\nesac\n")
        go.chmod(0o755)
        f = self.fixture()
        output = self.root / "out"
        with patch.object(producer, "_load_inputs", return_value=(f[2], f[3])), self.assertRaisesRegex(producer.KernelError, "build"):
            producer.build_kernel(go_binary=str(go), target="darwin/arm64", output_dir=str(output), archive_path=str(f[0]))
        self.assertFalse((output / "kernel-manifest.json").exists())
        self.assertFalse((output / "vibermate-xray").exists())

    def test_wrong_output_info_rejects_binary_and_exercises_exact_build_command(self):
        # This command double only produces rejected outputs; success is proven
        # by two actual native builds, never by fabricated successful metadata.
        f = self.fixture()
        go = self.root / "go"
        trace = self.root / "trace.json"
        go.write_text("#!/usr/bin/env python3\nimport json,sys\nfrom pathlib import Path\n"
                      "args=sys.argv[1:]\n"
                      "if args==['version']: print('go version go1.26.10 darwin/arm64')\n"
                      "elif args==['env','GOVERSION']: print('go1.26.10')\n"
                      "elif args[0]=='build':\n"
                      f" Path({str(trace)!r}).write_text(json.dumps(args))\n"
                      " Path(args[args.index('-o')+1]).write_bytes(b'rejected output')\n"
                      "else: print(json.dumps({'GoVersion':'go1.20','Settings':[],'Deps':[]}))\n")
        go.chmod(0o755)
        output = self.root / "out"
        with patch.object(producer, "_load_inputs", return_value=(f[2], f[3])), self.assertRaisesRegex(producer.KernelError, "build info"):
            producer.build_kernel(go_binary=str(go), target="windows/amd64", output_dir=str(output), archive_path=str(f[0]))
        argv = json.loads(trace.read_text())
        self.assertEqual(argv[:4], ['build', '-buildvcs=true', '-trimpath', '-ldflags=-X github.com/xtls/xray-core/core.build=d2758a0-vibermate-security1'])
        self.assertEqual(argv[-1], './main')
        self.assertEqual(Path(argv[-2]).name, "vibermate-xray.exe")
        self.assertFalse((output / "kernel-manifest.json").exists())
        self.assertFalse((output / "vibermate-xray.exe").exists())
        self.assertEqual((output / ".work/vibermate-xray.exe.failed").read_bytes(), b"rejected output")

    def test_target_environment_cancels_ambient_compiler_overrides(self):
        with patch.dict(os.environ, {"GOFLAGS": "-overlay=secret.json", "GOROOT": "/bad",
                                     "GOEXPERIMENT": "bad", "GODEBUG": "bad", "GIT_DIR": "/bad",
                                     "HTTPS_PROXY": "https://username:password@example.invalid"}):
            for target in ["darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64", "windows/amd64"]:
                env, recorded = producer._build_environment(target)
                self.assertEqual(env["GOOS"] + "/" + env["GOARCH"], target)
                self.assertEqual(env["GOFLAGS"], "-mod=readonly")
                self.assertEqual(env["GOEXPERIMENT"], "")
                self.assertNotIn("GOROOT", env)
                self.assertNotIn("GODEBUG", env)
                self.assertNotIn("GIT_DIR", env)
                self.assertIn("HTTPS_PROXY", env)
                self.assertNotIn("HTTPS_PROXY", recorded)

    def test_diagnostics_redact_credential_urls(self):
        safe = producer._safe_output(b'failed https://user:password@example.invalid/path?token=x\n')
        self.assertNotIn("password", safe)
        self.assertNotIn("token=x", safe)

    def test_bad_lock_has_bounded_error_without_output(self):
        output = self.root / "out"
        with patch.object(producer, "_load_inputs", side_effect=ValueError("broken lock")):
            with self.assertRaisesRegex(producer.KernelError, "lock"):
                producer.build_kernel(go_binary="/missing/go", target="darwin/arm64", output_dir=str(output))
        self.assertFalse(output.exists())

    def test_network_does_not_follow_redirect_to_an_unlocked_url(self):
        requested = []
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                requested.append(self.path)
                if self.path == "/redirect":
                    self.send_response(302)
                    self.send_header("Location", "/unlocked")
                else:
                    self.send_response(200)
                self.end_headers()
            def log_message(self, *args):
                pass
        server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        try:
            with self.assertRaises(urllib.error.HTTPError):
                producer._open_url(f"http://127.0.0.1:{server.server_port}/redirect")
            self.assertEqual(requested, ["/redirect"])
        finally:
            server.shutdown()
            thread.join()
            server.server_close()

    def test_unsupported_target_is_rejected_before_output_creation(self):
        with tempfile.TemporaryDirectory() as parent:
            output = Path(parent) / "result"
            with self.assertRaises(producer.KernelError):
                producer.build_kernel(go_binary="/missing/go", target="plan9/arm64",
                                      output_dir=str(output))
            self.assertFalse(output.exists())


class SetupFailures(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.output = self.root / "out"

    def compiler(self, malformed=None, competing=False):
        go = self.root / "go"
        go.write_text(
            f"#!{sys.executable}\nimport sys\nfrom pathlib import Path\n"
            "args=sys.argv[1:]\n"
            f"bad={malformed!r}\n"
            "if args==['version']:\n"
            " sys.stdout.buffer.write(b'\\xffhttps://user:secret@example.invalid\\n' if bad=='version' else b'go version go1.26.10 darwin/arm64\\n')\n"
            "elif args==['env','GOVERSION']:\n"
            + (f" Path({str(self.output)!r}).mkdir()\n Path({str(self.output / 'sentinel')!r}).write_bytes(b'competitor-owned')\n" if competing else "")
            + " sys.stdout.buffer.write(b'\\xffhttps://user:secret@example.invalid\\n' if bad=='goversion' else b'go1.26.10\\n')\n"
        )
        go.chmod(0o755)
        return go

    def build(self, go):
        return producer.build_kernel(go_binary=str(go), target="darwin/arm64", output_dir=str(self.output))

    def no_success(self):
        self.assertFalse((self.output / "kernel-manifest.json").exists())
        self.assertFalse((self.output / "vibermate-xray").exists())

    def test_malformed_compiler_version_is_bounded(self):
        with self.assertRaisesRegex(producer.KernelError, "^compiler:"):
            self.build(self.compiler(malformed="version"))
        self.assertFalse(self.output.exists())

    def test_malformed_compiler_goversion_is_bounded(self):
        with self.assertRaisesRegex(producer.KernelError, "^compiler:"):
            self.build(self.compiler(malformed="goversion"))
        self.assertFalse(self.output.exists())

    def test_denied_output_creation_is_bounded(self):
        go = self.compiler()
        original = Path.mkdir
        def deny(path, *args, **kwargs):
            if path == self.output:
                raise PermissionError("denied https://user:secret@example.invalid")
            return original(path, *args, **kwargs)
        with patch.object(Path, "mkdir", deny), self.assertRaisesRegex(producer.KernelError, "^output:"):
            self.build(go)
        self.assertFalse(self.output.exists())

    def test_competing_creation_keeps_unowned_sentinel(self):
        with self.assertRaisesRegex(producer.KernelError, "^output:"):
            self.build(self.compiler(competing=True))
        self.assertEqual((self.output / "sentinel").read_bytes(), b"competitor-owned")
        self.assertEqual([p.name for p in self.output.iterdir()], ["sentinel"])
        self.no_success()

    def test_partial_owned_setup_has_bounded_error_and_no_success(self):
        go = self.compiler()
        original = Path.mkdir
        for stage in (".work", ".work/source"):
            output = self.root / stage.replace("/", "-")
            denied = output / stage
            def deny(path, *args, **kwargs):
                if path == denied:
                    raise PermissionError("setup denied")
                return original(path, *args, **kwargs)
            with self.subTest(stage=stage), patch.object(Path, "mkdir", deny):
                with self.assertRaisesRegex(producer.KernelError, "^output:"):
                    producer.build_kernel(go_binary=str(go), target="darwin/arm64", output_dir=str(output))
                self.assertTrue(output.is_dir())
                self.assertFalse((output / "kernel-manifest.json").exists())
                self.assertFalse((output / "vibermate-xray").exists())

    def cli(self, go):
        return subprocess.run([sys.executable, str(producer.ASSETS / "build_kernel.py"),
                               "--go", str(go), "--target", "darwin/arm64",
                               "--output", str(self.output)], capture_output=True, text=True)

    def bounded_cli(self, result, phase):
        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stdout, "")
        self.assertTrue(result.stderr.startswith(phase + ":"), result.stderr)
        self.assertLess(len(result.stderr), 160)
        self.assertNotIn("Traceback", result.stderr)
        self.assertNotIn("secret", result.stderr)
        self.assertNotIn("https://", result.stderr)
        self.no_success()

    def test_cli_malformed_version_is_bounded(self):
        self.bounded_cli(self.cli(self.compiler(malformed="version")), "compiler")
        self.assertFalse(self.output.exists())

    def test_cli_malformed_goversion_is_bounded(self):
        self.bounded_cli(self.cli(self.compiler(malformed="goversion")), "compiler")
        self.assertFalse(self.output.exists())

    def test_cli_competing_creation_keeps_unowned_sentinel(self):
        self.bounded_cli(self.cli(self.compiler(competing=True)), "output")
        self.assertEqual((self.output / "sentinel").read_bytes(), b"competitor-owned")
        self.assertEqual([p.name for p in self.output.iterdir()], ["sentinel"])

    def test_cli_denied_parent_is_bounded(self):
        go = self.compiler()
        self.root.chmod(0o500)
        try:
            self.bounded_cli(self.cli(go), "output")
            self.assertFalse(self.output.exists())
        finally:
            self.root.chmod(0o700)


if __name__ == "__main__":
    unittest.main()
