import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, mkdir, writeFile, rm, symlink, readFile, cp, copyFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { verifyKernelMaterials, verifyLinuxKernelPackage, verifyMacOSKernelPackage, verifyLocalMacOSKernelPackage } from "./verify_package.mjs";
import { flutterDesktopBuildConfigurationNames, flutterDesktopNestedCode } from "../../ui/flutter_app/tool/desktop_build_manifest.mjs";

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), "vibermate-kernel-materials-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  return root;
}

test("offline kernel verification refuses a missing package success marker", async (t) => {
  const root = await fixture(t);
  await assert.rejects(() => verifyKernelMaterials(root, "linux/amd64"));
});

const actualPackage = process.env.VIBERMATE_KERNEL_TEST_PACKAGE;
const actualUniversal = process.env.VIBERMATE_KERNEL_TEST_UNIVERSAL;
test("real macOS materials remain immutable while local signed code is bound to Desktop provenance", { skip: !actualUniversal }, async (t) => {
  const root = await fixture(t);
  const app = join(root, "ViberMate.app");
  await mkdir(join(app, "Contents/MacOS"), { recursive: true });
  await mkdir(join(app, "Contents/Resources"), { recursive: true });
  await cp(join(actualUniversal, "vless-kernel"), join(app, "Contents/Resources/vless-kernel"), { recursive: true });
  const binary = join(app, "Contents/MacOS/vibermate-xray");
  await copyFile(join(actualUniversal, "vibermate-xray"), binary);
  // A signature transformation changes bytes; immutable producer materials stay valid.
  await writeFile(binary, Buffer.concat([await readFile(binary), Buffer.from("fixture signature transformation")]));
  const materials = await verifyMacOSKernelPackage(app, "darwin/universal");
  assert.equal(materials.binary.SHA256, "a5ecfb28f368f2ad7aa61f5c2739c229786b9c58d0dcaa955959ae9ccaa49c31");
  const nested = Object.fromEntries(Object.keys(flutterDesktopNestedCode).map((name) => [name, "a".repeat(64)]));
  nested["vibermate-xray"] = createHash("sha256").update(await readFile(binary)).digest("hex");
  const desktop = {
    schema: "vibermate.desktop-build/v3", source: { vcs: "git", revision: "b".repeat(40), commitTime: "2026-08-03T00:00:00Z", dirty: true },
    profiles: { desktop: "release", sidecars: "release", toolkit: "flutter", target: "universal-apple-darwin" },
    toolchains: { go: "go version go1.26.10", flutter: "fixture Flutter", dart: "fixture Dart", xcode: "fixture Xcode" },
    configurationSHA256: Object.fromEntries(flutterDesktopBuildConfigurationNames.map((name) => [name, "c".repeat(64)])),
    nestedCodeSHA256: nested,
  };
  await writeFile(join(app, "Contents/Resources/vibermate-build-manifest.json"), JSON.stringify(desktop));
  await verifyLocalMacOSKernelPackage(app, "darwin/universal");
  await writeFile(binary, "wrong post-sign kernel");
  await assert.rejects(() => verifyLocalMacOSKernelPackage(app, "darwin/universal"), /nested provenance/);
  await rm(join(app, "Contents/Resources/vless-kernel/licenses/go/LICENSE"));
  await assert.rejects(() => verifyMacOSKernelPackage(app, "darwin/universal"), /closed material inventory/);
});
test("retained real Linux package validates actual bytes and rejects changed kernel/material inventories", { skip: !actualPackage }, async (t) => {
  const root = await fixture(t);
  await cp(join(actualPackage, "vless-kernel"), join(root, "vless-kernel"), { recursive: true });
  await copyFile(join(actualPackage, "vibermate-xray"), join(root, "vibermate-xray"));
  const original = await readFile(join(root, "vibermate-xray"));
  const manifest = await verifyLinuxKernelPackage(root, "linux/amd64");
  assert.equal(manifest.binary.SHA256, "8d3b93726f40259283322eeb8fbc202d84cbf02838e3ceaf9886ba2056fc916a");
  await writeFile(join(root, "vibermate-xray"), "another kernel");
  await assert.rejects(() => verifyLinuxKernelPackage(root, "linux/amd64"), /pre-sign bytes/);
  // macOS does not mistake its raw producer digest for post-sign code bytes.
  const app = join(root, "ViberMate.app");
  await mkdir(join(app, "Contents/MacOS"), { recursive: true });
  await mkdir(join(app, "Contents/Resources"), { recursive: true });
  await cp(join(root, "vless-kernel"), join(app, "Contents/Resources/vless-kernel"), { recursive: true });
  await copyFile(join(root, "vibermate-xray"), join(app, "Contents/MacOS/vibermate-xray"));
  await assert.rejects(() => verifyMacOSKernelPackage(app, "darwin/arm64"), /target/);
  await writeFile(join(root, "vibermate-xray"), original);
  await writeFile(join(root, "vless-kernel/extra.txt"), "unlisted");
  await assert.rejects(() => verifyLinuxKernelPackage(root, "linux/amd64"), /unexpected material/);
  await rm(join(root, "vless-kernel/extra.txt"));
  const source = join(root, "vless-kernel/upstream-source.tar.gz");
  await writeFile(source, "changed archive");
  await assert.rejects(() => verifyLinuxKernelPackage(root, "linux/amd64"), /material digest/);
});

test("offline kernel verification refuses duplicate JSON and symbolic success markers", async (t) => {
  const root = await fixture(t);
  await writeFile(join(root, "package-manifest.json"), '{"schema":"bad","schema":"vibermate.vless-kernel-package/v1"}');
  await assert.rejects(() => verifyKernelMaterials(root, "linux/amd64"), /valid JSON/);
  await rm(join(root, "package-manifest.json"));
  await writeFile(join(root, "outside.json"), '{}');
  await symlink(join(root, "outside.json"), join(root, "package-manifest.json"));
  await assert.rejects(() => verifyKernelMaterials(root, "linux/amd64"), /regular|symbolic/);
});

test("offline kernel verification refuses another source or package target", async (t) => {
  const root = await fixture(t);
  await writeFile(join(root, "package-manifest.json"), JSON.stringify({
    schema: "vibermate.vless-kernel-package/v1", target: "linux/arm64",
    binary: { name: "vibermate-xray", digestStage: "pre-sign", size: 1, SHA256: "a".repeat(64) },
    inputs: [], upstream: {}, patchSHA256: "a".repeat(64), moduleSHA256: {}, materials: {},
  }));
  await assert.rejects(() => verifyKernelMaterials(root, "linux/amd64"), /target|identity/);
});
