import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import {
  chmod,
  lstat,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  realpath,
  rename,
  rm,
  symlink,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const repository = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const helper = join(repository, "ui/flutter_app/tool/macos_distribution_temp.sh");
const builder = join(repository, "ui/flutter_app/tool/build_macos_distribution.sh");
const packager = join(repository, "tool/vless-kernel/package_kernel.py");

async function fixture(t) {
  const root = await realpath(await mkdtemp(join(tmpdir(), "vibermate-temp-test-")));
  t.after(() => rm(root, { recursive: true, force: true }));
  const parent = join(root, "real parent");
  const alias = join(root, "alias parent");
  await mkdir(parent);
  await symlink(parent, alias, "dir");
  const sentinel = join(parent, "adjacent-sentinel");
  await writeFile(sentinel, "keep\n");
  return { root, parent, alias, sentinel };
}

function bash(script, ...args) {
  return spawnSync("bash", ["-c", `source "$1"; ${script}`, "bash", helper, ...args], {
    encoding: "utf8",
  });
}

function created(parent) {
  const result = bash(
    'create_distribution_temp "$2" || exit; printf "%s\\n%s\\n" "$distribution_temp_parent" "$distribution_temp_directory"',
    parent,
  );
  assert.equal(result.status, 0, result.stderr);
  const [savedParent, directory] = result.stdout.trimEnd().split("\n");
  return { savedParent, directory };
}

function cleaned(savedParent, directory) {
  return bash(
    'distribution_temp_parent="$2"; distribution_temp_directory="$3"; cleanup_distribution_temp',
    savedParent,
    directory,
  );
}

test("physical parent creates one private owned directory and cleans only it", async (t) => {
  const { parent, sentinel } = await fixture(t);
  const { savedParent, directory } = created(`${parent}/`);
  assert.equal(savedParent, parent);
  assert.match(directory, /^.*\/vibermate-flutter-distribution\.[A-Za-z0-9]{6}$/u);
  assert.equal(dirname(directory), parent);
  const info = await lstat(directory);
  assert.equal(info.isDirectory(), true);
  assert.equal(info.mode & 0o777, 0o700);
  const cleanup = cleaned(savedParent, directory);
  assert.equal(cleanup.status, 0, cleanup.stderr);
  await assert.rejects(lstat(directory), { code: "ENOENT" });
  assert.equal(await readFile(sentinel, "utf8"), "keep\n");
});

test("symbolic parent is made physical before creation and package input validation", async (t) => {
  const { root, parent, alias, sentinel } = await fixture(t);
  const { savedParent, directory } = created(`${alias}/`);
  for (const architecture of ["arm64", "amd64"]) {
    await mkdir(join(directory, `kernel-${architecture}`));
  }
  const runPackage = (firstInput) => spawnSync("python3", [
    "-B", packager, "--go", "/usr/bin/false", "--target", "darwin/universal",
    "--input", firstInput,
    "--input", join(directory, "kernel-amd64"),
    "--output", join(root, "unused-package"),
  ], { encoding: "utf8", env: { ...process.env, PYTHONDONTWRITEBYTECODE: "1" } });
  const aliasInput = join(alias, basename(directory), "kernel-arm64");
  const aliasRejected = runPackage(aliasInput);
  assert.equal(aliasRejected.status, 1);
  assert.match(aliasRejected.stderr, /input: symbolic input path component/u);
  t.diagnostic(`alias packager rejection: ${aliasRejected.stderr.trim()}`);
  t.diagnostic(`helper directory: ${directory}`);
  assert.equal(savedParent, parent);
  assert.equal(dirname(directory), parent);
  for (const architecture of ["arm64", "amd64"]) {
    assert.equal(await realpath(join(directory, `kernel-${architecture}`)),
      join(directory, `kernel-${architecture}`));
  }
  const canonicalRejectedLater = runPackage(join(directory, "kernel-arm64"));
  assert.equal(canonicalRejectedLater.status, 1);
  assert.doesNotMatch(canonicalRejectedLater.stderr, /symbolic input path component/u);
  assert.match(canonicalRejectedLater.stderr,
    /^input: failed; private failure record unavailable\n$/u);
  t.diagnostic(`canonical missing-manifest rejection: ${canonicalRejectedLater.stderr.trim()}`);
  const cleanup = cleaned(savedParent, directory);
  assert.equal(cleanup.status, 0, cleanup.stderr);
  await assert.rejects(lstat(directory), { code: "ENOENT" });
  assert.equal(await readFile(sentinel, "utf8"), "keep\n");
});

test("cleanup refuses parent, outside, traversal and symlink substitution", async (t) => {
  const { root, parent, sentinel } = await fixture(t);
  const { savedParent, directory } = created(parent);
  const outside = join(root, "vibermate-flutter-distribution.outside");
  await mkdir(outside);
  for (const target of [parent, outside, `${parent}/../vibermate-flutter-distribution.outside`]) {
    const rejected = cleaned(savedParent, target);
    assert.notEqual(rejected.status, 0);
    assert.match(rejected.stderr, /refusing to clean/u);
  }
  assert.equal((await lstat(directory)).isDirectory(), true);
  assert.equal((await lstat(outside)).isDirectory(), true);
  const displaced = join(root, "displaced-owned-directory");
  await rename(directory, displaced);
  await symlink(outside, directory, "dir");
  const substituted = cleaned(savedParent, directory);
  assert.notEqual(substituted.status, 0);
  assert.equal((await lstat(directory)).isSymbolicLink(), true);
  assert.equal((await lstat(outside)).isDirectory(), true);
  assert.equal(await readFile(sentinel, "utf8"), "keep\n");
});

test("cleanup refuses a substituted symbolic parent", async (t) => {
  const { root, parent } = await fixture(t);
  const { savedParent, directory } = created(parent);
  const displacedParent = join(root, "displaced-parent");
  const outsideParent = join(root, "outside-parent");
  await mkdir(outsideParent);
  const outsideChild = join(outsideParent, basename(directory));
  await mkdir(outsideChild);
  await writeFile(join(outsideChild, "sentinel"), "outside\n");
  await rename(parent, displacedParent);
  await symlink(outsideParent, parent, "dir");
  const rejected = cleaned(savedParent, directory);
  assert.notEqual(rejected.status, 0);
  assert.match(rejected.stderr, /refusing to clean/u);
  assert.equal(await readFile(join(outsideChild, "sentinel"), "utf8"), "outside\n");
  assert.equal((await lstat(join(displacedParent, basename(directory)))).isDirectory(), true);
});

test("distribution caller reports cleanup failure after otherwise successful work", async (t) => {
  const { root, parent } = await fixture(t);
  const { savedParent, directory } = created(parent);
  const outside = join(root, "outside");
  await mkdir(outside);
  await rename(directory, join(root, "displaced"));
  await symlink(outside, directory, "dir");
  const trap = (await readFile(builder, "utf8")).match(/^trap .+ EXIT$/mu)?.[0];
  assert.ok(trap, "distribution caller must install an EXIT cleanup trap");
  const result = bash(
    `distribution_temp_parent="$2"; distribution_temp_directory="$3"; ${trap}; true`,
    savedParent,
    directory,
  );
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /refusing to clean/u);
  assert.equal((await lstat(directory)).isSymbolicLink(), true);
});

test("missing or unusable parent leaves no owned cleanup target", async (t) => {
  const { root, parent } = await fixture(t);
  const file = join(root, "not-a-directory");
  await writeFile(file, "x");
  for (const invalid of [join(root, "missing"), file]) {
    const result = bash(
      'distribution_temp_parent=; distribution_temp_directory=; if create_distribution_temp "$2"; then exit 20; fi; printf "parent=%s\\ndirectory=%s\\n" "$distribution_temp_parent" "$distribution_temp_directory"',
      invalid,
    );
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stdout, "parent=\ndirectory=\n");
  }
  await chmod(parent, 0o500);
  try {
    const failedMktemp = bash(
      'distribution_temp_parent=; distribution_temp_directory=; if create_distribution_temp "$2"; then exit 20; fi; printf "parent=%s\\ndirectory=%s\\n" "$distribution_temp_parent" "$distribution_temp_directory"',
      parent,
    );
    assert.equal(failedMktemp.status, 0, failedMktemp.stderr);
    assert.equal(failedMktemp.stdout, "parent=\ndirectory=\n");
  } finally {
    await chmod(parent, 0o700);
  }
  assert.deepEqual(await readdir(parent), ["adjacent-sentinel"]);
});
