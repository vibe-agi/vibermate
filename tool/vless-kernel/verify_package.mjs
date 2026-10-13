import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { lstat, open, readdir } from "node:fs/promises";
import { constants } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { parseClosedJSONObject } from "../macos-release/macos-distribution-policy.mjs";
import { validateFlutterDesktopBuildManifest } from "../../ui/flutter_app/tool/desktop_build_manifest.mjs";

const directory = dirname(fileURLToPath(import.meta.url));
const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");
const digest = /^[0-9a-f]{64}$/u;
const targets = ["darwin/arm64", "darwin/amd64", "darwin/universal", "linux/amd64", "linux/arm64"];
function requireValue(condition, message) {
  if (!condition) throw new Error(`Kernel package: ${message}`);
}
function same(left, right, message) {
  try { assert.deepEqual(left, right); } catch { throw new Error(`Kernel package: ${message}`); }
}
function keys(value, names, message) {
  requireValue(value !== null && typeof value === "object" && !Array.isArray(value), message);
  same(Object.keys(value).sort(), [...names].sort(), message);
}
function portable(path) {
  requireValue(typeof path === "string" && /^[A-Za-z0-9._/-]+$/u.test(path) &&
    !path.split("/").some((part) => !part || part === "." || part === ".."), "unsafe material path");
}
async function regular(path, maximumBytes = 128 << 20) {
  const before = await lstat(path);
  requireValue(before.isFile() && before.nlink === 1 && before.size > 0 &&
    before.size <= maximumBytes && (before.mode & 0o7000) === 0, `${path} must be an ordinary regular file`);
  const handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const opened = await handle.stat();
    const bytes = await handle.readFile();
    const after = await lstat(path);
    for (const field of ["dev", "ino", "size", "mode", "mtimeMs", "ctimeMs"]) {
      requireValue(before[field] === opened[field] && before[field] === after[field], "file changed during verification");
    }
    requireValue(bytes.length === before.size, "file size changed");
    return bytes;
  } finally { await handle.close(); }
}
async function json(path) {
  return parseClosedJSONObject(new TextDecoder("utf-8", { fatal: true }).decode(await regular(path, 2 << 20)), "kernel JSON");
}
function binaryRecord(value, preSign = false) {
  keys(value, preSign ? ["name", "size", "SHA256", "digestStage"] : ["name", "size", "SHA256"], "binary inventory shape");
  requireValue(value.name === "vibermate-xray" && Number.isSafeInteger(value.size) && value.size > 0 &&
    value.size <= (128 << 20) && digest.test(value.SHA256) && (!preSign || value.digestStage === "pre-sign"), "binary identity");
}

// Offline immutable materials remain verifiable after macOS rewrites code signatures.
export async function verifyKernelMaterialDigests(materialsPath, target) {
  requireValue(targets.includes(target), "unsupported target");
  const root = await lstat(materialsPath);
  requireValue(root.isDirectory() && !root.isSymbolicLink(), "symbolic materials directory");
  const manifest = await json(join(materialsPath, "package-manifest.json"));
  keys(manifest, ["schema", "target", "binary", "inputs", "upstream", "patchSHA256", "moduleSHA256", "materials"], "package manifest shape");
  requireValue(manifest.schema === "vibermate.vless-kernel-package/v1" && manifest.target === target, "package target or identity mismatch");
  binaryRecord(manifest.binary, true);
  keys(manifest.materials, Object.keys(manifest.materials ?? {}), "material map shape");
  const observed = [];
  async function visit(path, prefix = "") {
    const before = await lstat(path);
    requireValue(before.isDirectory() && !before.isSymbolicLink(), "symbolic material directory");
    for (const entry of await readdir(path, { withFileTypes: true })) {
      const name = `${prefix}${entry.name}`;
      portable(name);
      if (entry.isDirectory()) await visit(join(path, entry.name), `${name}/`);
      else {
        requireValue(entry.isFile(), "non-regular or symbolic material");
        observed.push(name);
        const bytes = await regular(join(materialsPath, name));
        if (name !== "package-manifest.json") {
          const item = manifest.materials[name];
          keys(item, ["size", "SHA256"], "unexpected material or digest shape");
          requireValue(item.size === bytes.length && digest.test(item.SHA256) && item.SHA256 === sha256(bytes), `material digest mismatch: ${name}`);
        }
      }
    }
    const after = await lstat(path);
    for (const field of ["dev", "ino", "mtimeMs", "ctimeMs"]) requireValue(before[field] === after[field], "material directory changed");
  }
  await visit(materialsPath);
  same(observed.sort(), [...Object.keys(manifest.materials), "package-manifest.json"].sort(), "closed material inventory mismatch");
  return manifest;
}

export async function verifyKernelMaterials(materialsPath, target) {
  const manifest = await verifyKernelMaterialDigests(materialsPath, target);
  const lock = await json(join(directory, "kernel-lock.json"));
  const upstream = Object.fromEntries(["repository", "ref", "commit", "tree", "archiveSHA256"].map((name) => [name, lock[name]]));
  same(manifest.upstream, upstream, "upstream identity mismatch");
  same(manifest.moduleSHA256, lock.moduleSHA256, "patched module identity mismatch");
  requireValue(manifest.patchSHA256 === lock.patchSHA256, "patch identity mismatch");
  const fixed = {
    "upstream-source.tar.gz": lock.archiveSHA256,
    "dependency.patch": lock.patchSHA256,
    "upstream.commit": lock.rawCommitSHA256,
    "LICENSE.upstream": lock.licenseSHA256,
    "kernel-lock.json": sha256(await regular(join(directory, "kernel-lock.json"))),
    "source/go.mod": lock.moduleSHA256.goMod,
    "source/go.sum": lock.moduleSHA256.goSum,
    "licenses/go/LICENSE": "911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad",
  };
  for (const [name, hash] of Object.entries(fixed)) requireValue(manifest.materials[name]?.SHA256 === hash, `locked material mismatch: ${name}`);
  requireValue(manifest.materials["upstream-source.tar.gz"].size === lock.archiveSize, "locked archive size mismatch");
  const expectedTargets = target === "darwin/universal" ? ["darwin/amd64", "darwin/arm64"] : [target];
  requireValue(Array.isArray(manifest.inputs), "input inventory missing");
  same(manifest.inputs.map((item) => item.target).sort(), expectedTargets, "producer target inventory mismatch");
  const linked = new Map();
  const allowed = new Set([...Object.keys(fixed), "SOURCE-ACCESS.txt", "licenses.json", "licenses/go/LICENSE"]);
  for (const input of manifest.inputs) {
    keys(input, ["target", "manifestSHA256", "binary"], "producer input shape");
    binaryRecord(input.binary);
    const name = `inputs/${input.target.replace("/", "-")}/kernel-manifest.json`;
    allowed.add(name);
    requireValue(manifest.materials[name]?.SHA256 === input.manifestSHA256, "producer manifest digest mismatch");
    const producer = await json(join(materialsPath, name));
    requireValue(producer.schema === "vibermate.vless-kernel-build/v1" && producer.target === input.target &&
      producer.compiler?.version === lock.goVersion && producer.patchSHA256 === lock.patchSHA256 &&
      producer.licenseSHA256 === lock.licenseSHA256, "producer identity mismatch");
    same(producer.upstream, upstream, "producer upstream mismatch");
    same(producer.moduleSHA256, lock.moduleSHA256, "producer modules mismatch");
    same(producer.binary, input.binary, "producer binary mismatch");
    const info = producer.buildInfo;
    requireValue(info?.GoVersion === lock.goVersion && `${info.GOOS}/${info.GOARCH}` === input.target &&
      info["vcs.revision"] === lock.commit && info["vcs.modified"] === true &&
      info.path === "github.com/xtls/xray-core/main" && info.main?.Path === "github.com/xtls/xray-core", "producer metadata identity mismatch");
    requireValue(Array.isArray(info.dependencies) && info.dependencies.length > 0, "producer dependencies missing");
    for (const dependency of info.dependencies) {
      requireValue(!dependency.Replace && typeof dependency.Path === "string" && typeof dependency.Version === "string" &&
        typeof dependency.Sum === "string" && dependency.Sum.startsWith("h1:"), "dependency identity mismatch");
      const id = `${dependency.Path}@${dependency.Version}`;
      if (linked.has(id)) same(linked.get(id), dependency, "conflicting linked dependency");
      linked.set(id, dependency);
    }
  }
  if (target !== "darwin/universal") same(manifest.binary, { ...manifest.inputs[0].binary, digestStage: "pre-sign" }, "thin binary differs from producer");
  const licenses = await json(join(materialsPath, "licenses.json"));
  requireValue(licenses.schema === "vibermate.vless-kernel-licenses/v1" && Array.isArray(licenses.modules), "licenses inventory missing");
  requireValue(licenses.go?.version === lock.goVersion && licenses.go.sourceURL === `https://go.dev/dl/${lock.goVersion}.src.tar.gz`, "Go notice identity mismatch");
  same(licenses.go.files.map(({ path, size, SHA256 }) => ({ path, size, SHA256 })), [{ path: "licenses/go/LICENSE", ...manifest.materials["licenses/go/LICENSE"] }], "Go notice digest disagreement");
  const moduleIDs = [];
  for (const module of licenses.modules) {
    const id = `${module.Path}@${module.Version}`;
    moduleIDs.push(id);
    const dependency = linked.get(id);
    requireValue(dependency?.Sum === module.Sum && module.materialKey === sha256(Buffer.from(id)) &&
      Array.isArray(module.files) && module.files.length > 0, "linked-module notice identity mismatch");
    for (const file of module.files) {
      portable(file.path);
      requireValue(file.path.startsWith(`licenses/modules/${module.materialKey}/`), "notice outside module inventory");
      requireValue(!allowed.has(file.path), "duplicate notice path");
      allowed.add(file.path);
      same(manifest.materials[file.path], { size: file.size, SHA256: file.SHA256 }, "notice digest disagreement");
    }
  }
  same(moduleIDs.sort(), [...linked.keys()].sort(), "linked-module notices omitted or duplicated");
  same([...allowed].sort(), Object.keys(manifest.materials).sort(), "material outside producer/notice allowlist");
  return manifest;
}

export async function verifyMacOSKernelPackage(appPath, target) {
  requireValue(target.startsWith("darwin/"), "macOS target mismatch");
  const binary = join(appPath, "Contents/MacOS/vibermate-xray");
  const info = await lstat(binary);
  requireValue(info.isFile() && info.nlink === 1 && (info.mode & 0o111) !== 0 && (info.mode & 0o7000) === 0, "macOS helper must be a regular executable");
  // Signed bytes are bound by Desktop nestedCodeSHA256 and codesign/transformation.
  return verifyKernelMaterials(join(appPath, "Contents/Resources/vless-kernel"), target);
}

export async function verifyLocalMacOSKernelPackage(appPath, target) {
  const manifest = await verifyMacOSKernelPackage(appPath, target);
  const desktop = await json(join(appPath, "Contents/Resources/vibermate-build-manifest.json"));
  const desktopTargets = { "darwin/arm64": "aarch64-apple-darwin", "darwin/amd64": "x86_64-apple-darwin", "darwin/universal": "universal-apple-darwin" };
  validateFlutterDesktopBuildManifest(desktop, { requireClean: false, expectedTarget: desktopTargets[target] });
  requireValue(sha256(await regular(join(appPath, "Contents/MacOS/vibermate-xray"))) === desktop.nestedCodeSHA256["vibermate-xray"], "local signed helper differs from Desktop nested provenance");
  return manifest;
}

export async function verifyLinuxKernelPackage(bundlePath, target) {
  requireValue(target.startsWith("linux/"), "Linux target mismatch");
  const manifest = await verifyKernelMaterials(join(bundlePath, "vless-kernel"), target);
  const binary = join(bundlePath, "vibermate-xray");
  const bytes = await regular(binary);
  const metadata = await lstat(binary);
  requireValue((metadata.mode & 0o111) !== 0 && bytes.length === manifest.binary.size && sha256(bytes) === manifest.binary.SHA256, "Linux helper differs from pre-sign bytes");
  const result = spawnSync("go", ["version", "-m", "-json", binary], {
    encoding: "utf8", timeout: 30_000, maxBuffer: 2 << 20,
    env: { ...process.env, GOTOOLCHAIN: "local", GOENV: "off", GOWORK: "off" },
  });
  requireValue(!result.error && result.signal === null && result.status === 0, "Linux actual build metadata unavailable");
  const info = parseClosedJSONObject(result.stdout, "kernel actual build metadata");
  const settings = Object.fromEntries(info.Settings.map((item) => [item.Key, item.Value]));
  const expected = await json(join(bundlePath, "vless-kernel/inputs", target.replace("/", "-"), "kernel-manifest.json"));
  same({ GoVersion: info.GoVersion, GOOS: settings.GOOS, GOARCH: settings.GOARCH,
    "vcs.revision": settings["vcs.revision"], "vcs.modified": settings["vcs.modified"] === "true",
    dependencies: info.Deps, settings, main: info.Main, path: info.Path }, expected.buildInfo, "Linux actual kernel metadata mismatch");
  return manifest;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const [platform, root, target, ...extra] = process.argv.slice(2);
  requireValue(extra.length === 0 && root && target, "usage: verify_package.mjs <macos|linux> <bundle> <target>");
  if (platform === "macos") await verifyLocalMacOSKernelPackage(root, target);
  else if (platform === "linux") await verifyLinuxKernelPackage(root, target);
  else throw new Error("Kernel package: unknown layout");
  console.log(`Verified ${target} kernel package`);
}
