import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import {
  chmod,
  link,
  lstat,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  realpath,
  rm,
  symlink,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { flutterDesktopBuildConfigurationNames } from "../../ui/flutter_app/tool/desktop_build_manifest.mjs";
import {
  parseR0EvidenceArguments,
  prepareR0ReleaseEvidence,
} from "./prepare-r0-release-evidence.mjs";

const testDirectory = dirname(fileURLToPath(import.meta.url));
const repositoryUnderTest = resolve(testDirectory, "../..");
const commitDate = "2026-08-03T04:34:56Z";
const syftRootSPDXID =
  "SPDXRef-DocumentRoot-Directory-vibermate-unsigned-payload";

function sha256(payload) {
  return createHash("sha256").update(payload).digest("hex");
}

function trustedSyftSPDXDocument({ ledger, version }) {
  return {
    SPDXID: "SPDXRef-DOCUMENT",
    spdxVersion: "SPDX-2.3",
    dataLicense: "CC0-1.0",
    name: "unsigned-payload",
    documentNamespace: "https://fixture.invalid/syft/v1.44.0/unsigned-payload",
    creationInfo: {
      created: commitDate,
      creators: ["Organization: Anchore, Inc", "Tool: syft-1.44.0"],
    },
    packages: [
      {
        SPDXID: syftRootSPDXID,
        name: "vibermate-unsigned-payload",
        versionInfo: version,
        primaryPackagePurpose: "FILE",
        downloadLocation: "NOASSERTION",
        filesAnalyzed: false,
      },
      {
        SPDXID: "SPDXRef-Package-beta-library",
        name: "beta-library",
        versionInfo: "2.0.0",
      },
      {
        SPDXID: "SPDXRef-Package-alpha-library",
        name: "alpha-library",
        versionInfo: "1.2.3",
      },
      {
        SPDXID: "SPDXRef-Package-version-unknown-library",
        name: "version-unknown-library",
      },
      {
        SPDXID: "SPDXRef-Package-vibermate",
        name: "vibermate",
        versionInfo: version,
      },
    ],
    files: ledger.entries.map((entry, index) => ({
      SPDXID: `SPDXRef-File-${index}`,
      fileName: entry.path === "." ? "" : entry.path,
      fileTypes: [entry.type === "directory" ? "OTHER" : "BINARY"],
      checksums: [
        entry.type === "directory"
          ? { algorithm: "SHA1", checksumValue: "0".repeat(40) }
          : { algorithm: "SHA256", checksumValue: entry.sha256 },
      ],
    })),
    relationships: [
      {
        spdxElementId: "SPDXRef-DOCUMENT",
        relatedSpdxElement: syftRootSPDXID,
        relationshipType: "DESCRIBES",
      },
    ],
  };
}

function trustedSyftGenerator(mutate = () => {}) {
  return (context) => {
    const document = trustedSyftSPDXDocument(context);
    mutate(document, context);
    return Buffer.from(`${JSON.stringify(document)}\n`, "utf8");
  };
}

function rawSyftGenerator(payload) {
  const raw = Buffer.isBuffer(payload) ? payload : Buffer.from(payload, "utf8");
  return () => Buffer.from(raw);
}

function preparationDependencies(overrides = {}) {
  return {
    generateSyftSPDX: trustedSyftGenerator(),
    verifyUniversalBinary() {},
    ...overrides,
  };
}

function runGit(root, ...arguments_) {
  return execFileSync("git", ["-C", root, ...arguments_], {
    encoding: "utf8",
    env: {
      ...process.env,
      GIT_AUTHOR_DATE: commitDate,
      GIT_COMMITTER_DATE: commitDate,
    },
  }).trim();
}

async function writeFixtureFile(path, payload, mode = 0o644) {
  await mkdir(dirname(path), { recursive: true, mode: 0o755 });
  await writeFile(path, payload, { mode });
  await chmod(path, mode);
}

async function fixture(t) {
  const temporaryAlias = await mkdtemp(join(tmpdir(), "vibermate-r0-evidence-"));
  const temporaryRoot = await realpath(temporaryAlias);
  t.after(async () => {
    await rm(temporaryRoot, { recursive: true, force: true });
  });
  const repositoryRoot = join(temporaryRoot, "source");
  await mkdir(repositoryRoot, { mode: 0o755 });
  await chmod(repositoryRoot, 0o755);
  await writeFixtureFile(
    join(repositoryRoot, ".gitignore"),
    ["ui/flutter_app/build/", ""].join("\n"),
  );
  await writeFixtureFile(
    join(repositoryRoot, "ui/flutter_app/pubspec.yaml"),
    "name: vibermate_app\nversion: 0.1.2+3\nenvironment:\n  sdk: ^3.11.3\n",
  );
  await writeFixtureFile(
    join(repositoryRoot, "LICENSE"),
    "Apache License fixture\n",
  );
  const configurationPayloads = {
    "go.mod": "module example.invalid/vibermate\n\ngo 1.25.12\n",
    "go.sum": "example.invalid/module v1.0.0 h1:fixture\n",
    "ui/flutter_app/.metadata": "version:\n  revision: fixture\nproject_type: app\n",
    "ui/flutter_app/pubspec.lock": "packages: {}\n",
    "ui/flutter_app/tool/flutter-sdk.env":
      "VIBERMATE_FLUTTER_VERSION=3.41.5\nVIBERMATE_FLUTTER_REVISION=2c9eb20739dfec95e2c74bd3dfa4601b0a8a36aa\n",
    "ui/flutter_app/macos/Runner.xcodeproj/project.pbxproj": "// fixture project\n",
    "ui/flutter_app/macos/Runner/Configs/AppInfo.xcconfig":
      "PRODUCT_BUNDLE_IDENTIFIER = io.vibermate.desktop\n",
    "ui/flutter_app/macos/Runner/Configs/Release.xcconfig":
      "#include ../../Flutter/Flutter-Release.xcconfig\n",
    "ui/flutter_app/macos/Runner/Info.plist": "<plist><dict/></plist>\n",
    "ui/flutter_app/macos/Runner/Release.entitlements":
      "<plist><dict/></plist>\n",
  };
  for (const [relativePath, payload] of Object.entries(configurationPayloads)) {
    await writeFixtureFile(join(repositoryRoot, relativePath), payload);
  }
  runGit(repositoryRoot, "init", "-q");
  runGit(repositoryRoot, "config", "user.email", "r0-test@example.invalid");
  runGit(repositoryRoot, "config", "user.name", "R0 Test");
  runGit(repositoryRoot, "add", ".");
  runGit(repositoryRoot, "commit", "-q", "-m", "fixture source");
  const revision = runGit(repositoryRoot, "rev-parse", "HEAD");

  const mainPath = join(
    repositoryRoot,
    "ui/flutter_app/build/distribution/universal-apple-darwin/release/bundle/macos/ViberMate.app/Contents/MacOS/vibermate-desktop",
  );
  const launcherPath = join(
    repositoryRoot,
    "ui/flutter_app/build/distribution/universal-apple-darwin/release/bundle/macos/ViberMate.app/Contents/MacOS/vibermate",
  );
  const daemonPath = join(
    repositoryRoot,
    "ui/flutter_app/build/distribution/universal-apple-darwin/release/bundle/macos/ViberMate.app/Contents/MacOS/vibermated",
  );
  const buildManifestPath = join(
    repositoryRoot,
    "ui/flutter_app/build/distribution/universal-apple-darwin/release/bundle/macos/ViberMate.app/Contents/Resources/vibermate-build-manifest.json",
  );
  const distPath = join(
    repositoryRoot,
    "ui/flutter_app/build/distribution/universal-apple-darwin/release/r0-dist",
  );
  const mainPayload = Buffer.from("universal desktop main\n");
  const launcherPayload = Buffer.from("universal launcher\n");
  const daemonPayload = Buffer.from("universal daemon\n");
  await writeFixtureFile(mainPath, mainPayload, 0o755);
  await writeFixtureFile(launcherPath, launcherPayload, 0o755);
  await writeFixtureFile(daemonPath, daemonPayload, 0o755);
  const appFrameworkPayload = Buffer.from("universal App framework\n");
  const flutterFrameworkPayload = Buffer.from("universal FlutterMacOS framework\n");
  await writeFixtureFile(
    join(distPath, "App.framework/App"),
    appFrameworkPayload,
    0o755,
  );
  await writeFixtureFile(
    join(distPath, "App.framework/Resources/flutter_assets/AssetManifest.bin"),
    "fixture assets\n",
  );
  await writeFixtureFile(
    join(distPath, "FlutterMacOS.framework/FlutterMacOS"),
    flutterFrameworkPayload,
    0o755,
  );
  await writeFixtureFile(
    join(distPath, "FlutterMacOS.framework/Resources/icudtl.dat"),
    "fixture ICU\n",
  );

  const configurationSHA256 = {};
  for (const name of flutterDesktopBuildConfigurationNames) {
    configurationSHA256[name] = sha256(await readFile(join(repositoryRoot, name)));
  }
  const buildManifest = {
    schema: "vibermate.desktop-build/v3",
    source: {
      vcs: "git",
      revision,
      commitTime: commitDate,
      dirty: false,
    },
    profiles: {
      desktop: "release",
      sidecars: "release",
      target: "universal-apple-darwin",
      toolkit: "flutter",
    },
    toolchains: {
      go: "go version go1.25.12 darwin/arm64",
      flutter:
        "Flutter 3.41.5 (2c9eb20739dfec95e2c74bd3dfa4601b0a8a36aa)",
      dart: "Dart 3.11.3",
      xcode: "Xcode 16.2\nBuild version 16C5032a",
    },
    configurationSHA256,
    nestedCodeSHA256: {
      "app-framework": sha256(appFrameworkPayload),
      "flutter-macos-framework": sha256(flutterFrameworkPayload),
      vibermate: sha256(launcherPayload),
      vibermated: sha256(daemonPayload),
    },
  };
  await writeFixtureFile(
    buildManifestPath,
    `${JSON.stringify(buildManifest, null, 2)}\n`,
    0o600,
  );

  return {
    artifactRoot: join(temporaryRoot, "artifacts"),
    base: temporaryRoot,
    buildManifest,
    buildManifestPath,
    daemonPath,
    distPath,
    launcherPath,
    mainPath,
    repositoryRoot,
    revision,
  };
}

function preparationOptions(value, artifactRoot = value.artifactRoot) {
  return {
    artifactRoot,
    expectedRevision: value.revision,
    repositoryRoot: value.repositoryRoot,
  };
}

async function externalInputRoot(value) {
  const root = join(value.base, "r0-input");
  await mkdir(root, { mode: 0o700 });
  for (const [source, destination, mode] of [
    [value.mainPath, "vibermate-desktop", 0o755],
    [value.launcherPath, "vibermate", 0o755],
    [value.daemonPath, "vibermated", 0o755],
    [value.buildManifestPath, "vibermate-build-manifest.json", 0o600],
    [join(value.repositoryRoot, "LICENSE"), "LICENSE", 0o644],
  ]) {
    await writeFixtureFile(join(root, destination), await readFile(source), mode);
  }
  await writeFixtureFile(
    join(root, "dist/App.framework/App"),
    await readFile(join(value.distPath, "App.framework/App")),
    0o755,
  );
  await writeFixtureFile(
    join(root, "dist/App.framework/Resources/flutter_assets/AssetManifest.bin"),
    await readFile(
      join(value.distPath, "App.framework/Resources/flutter_assets/AssetManifest.bin"),
    ),
  );
  await writeFixtureFile(
    join(root, "dist/FlutterMacOS.framework/FlutterMacOS"),
    await readFile(join(value.distPath, "FlutterMacOS.framework/FlutterMacOS")),
    0o755,
  );
  await writeFixtureFile(
    join(root, "dist/FlutterMacOS.framework/Resources/icudtl.dat"),
    await readFile(
      join(value.distPath, "FlutterMacOS.framework/Resources/icudtl.dat"),
    ),
  );
  return root;
}

async function artifactSnapshot(root) {
  const snapshot = [];
  async function visit(path, relativePath) {
    const info = await lstat(path);
    if (info.isDirectory()) {
      snapshot.push({ path: relativePath, mode: info.mode & 0o7777, type: "directory" });
      const entries = await readdir(path);
      entries.sort();
      for (const name of entries) {
        await visit(
          join(path, name),
          relativePath === "." ? name : `${relativePath}/${name}`,
        );
      }
      return;
    }
    const payload = await readFile(path);
    snapshot.push({
      path: relativePath,
      mode: info.mode & 0o7777,
      type: "file",
      payload: payload.toString("base64"),
    });
  }
  await visit(root, ".");
  return snapshot;
}

async function assertAbsent(path) {
  await assert.rejects(lstat(path), (error) => error?.code === "ENOENT");
}

test("source-traceability preparer (R0) stages a deterministic verifier-ready evidence set", async (t) => {
  const value = await fixture(t);
  const secondRoot = join(value.base, "artifacts-second");
  const universalChecks = [];
  const dependencies = preparationDependencies({
    verifyUniversalBinary(path, label) {
      universalChecks.push({ path: path.slice(path.lastIndexOf("/") + 1), label });
    },
  });
  const first = await prepareR0ReleaseEvidence(
    preparationOptions(value),
    dependencies,
  );
  const second = await prepareR0ReleaseEvidence(
    preparationOptions(value, secondRoot),
    dependencies,
  );
  assert.equal(first.spec, join(value.artifactRoot, "release-spec.json"));
  assert.deepEqual(
    await artifactSnapshot(value.artifactRoot),
    await artifactSnapshot(secondRoot),
  );
  assert.equal(universalChecks.length, 6);

  assert.deepEqual(
    (await readdir(join(value.artifactRoot, "unsigned-payload"))).sort(),
    [
      "LICENSE",
      "dist",
      "vibermate",
      "vibermate-build-manifest.json",
      "vibermate-desktop",
      "vibermated",
    ],
  );
  assert.deepEqual(
    await readFile(join(value.artifactRoot, "desktop-build-manifest.json")),
    await readFile(
      join(value.artifactRoot, "unsigned-payload/vibermate-build-manifest.json"),
    ),
  );
  const spec = JSON.parse(await readFile(first.spec, "utf8"));
  assert.equal(spec.channel, "nightly");
  assert.equal(spec.commit, value.revision);
  assert.equal(spec.publishedAt, commitDate);
  assert.deepEqual(
    spec.artifacts.map((artifact) => artifact.role),
    ["app-tree-ledger", "desktop-build-manifest", "sbom", "known-issues"],
  );
  assert.deepEqual(spec.evidenceScope, {
    level: "r0",
    artifactState: "unsigned-pre-sign",
    r2Reproducibility: "not-asserted",
    r3SignedPackageBinding: "not-asserted",
    releaseApproval: "not-asserted",
  });
  const sbom = JSON.parse(
    await readFile(join(value.artifactRoot, "sbom.spdx.json"), "utf8"),
  );
  assert.deepEqual(
    sbom.packages.map((pkg) => [pkg.name, pkg.versionInfo]),
    [
      ["alpha-library", "1.2.3"],
      ["beta-library", "2.0.0"],
      ["version-unknown-library", "NOASSERTION"],
      ["vibermate", "0.1.2"],
    ],
  );
  assert.equal(
    sbom.packages.filter((pkg) => pkg.name === "vibermate").length,
    1,
  );
  const knownIssues = JSON.parse(
    await readFile(join(value.artifactRoot, "known-issues.json"), "utf8"),
  );
  assert.deepEqual(
    knownIssues.issues.map((issue) => issue.id),
    [
      "r2-reproducibility-missing",
      "r3-signed-package-binding-missing",
      "packaged-conformance-current-missing",
      "release-approval-missing",
      "license-review-missing",
    ],
  );
  for (const name of [
    "app-tree.json",
    "desktop-build-manifest.json",
    "sbom.spdx.json",
    "known-issues.json",
    "release-spec.json",
  ]) {
    const info = await lstat(join(value.artifactRoot, name));
    assert.equal(info.mode & 0o7777, 0o600, `${name} mode`);
  }
  assert.equal(
    (
      await lstat(
        join(
          value.artifactRoot,
          "unsigned-payload/vibermate-build-manifest.json",
        ),
      )
    ).mode & 0o7777,
    0o600,
  );

  const outputDirectory = join(value.base, "verified-output");
  await mkdir(outputDirectory, { mode: 0o700 });
  await chmod(outputDirectory, 0o700);
  execFileSync(
    "go",
    [
      "run",
      "./cmd/vibermate-release-evidence",
      "--spec",
      first.spec,
      "--artifact-root",
      value.artifactRoot,
      "--source-root",
      value.repositoryRoot,
      "--expected-revision",
      value.revision,
      "--output",
      join(outputDirectory, "release.json"),
    ],
    { cwd: repositoryUnderTest, stdio: "pipe" },
  );
});

test(
  "source-traceability preparer (R0) admits and directly runs the pinned Syft release",
  { skip: process.env.VIBERMATE_TEST_PINNED_SYFT === undefined },
  async (t) => {
    const value = await fixture(t);
    const syftBinaryPath = await realpath(
      process.env.VIBERMATE_TEST_PINNED_SYFT,
    );
    await prepareR0ReleaseEvidence(
      {
        ...preparationOptions(value),
        syftBinaryPath,
      },
      { verifyUniversalBinary() {} },
    );
    const sbom = JSON.parse(
      await readFile(join(value.artifactRoot, "sbom.spdx.json"), "utf8"),
    );
    assert.deepEqual(sbom.creationInfo.creators, [
      "Tool: syft-1.44.0",
      "Tool: vibermate-r0-evidence-preparer",
    ]);
  },
);

test(
  "source-traceability preparer (R0) verifies real Universal Mach-O executable build versions",
  { skip: process.env.VIBERMATE_TEST_UNIVERSAL_INPUT_ROOT === undefined },
  async (t) => {
    const value = await fixture(t);
    const universalRoot = await realpath(
      process.env.VIBERMATE_TEST_UNIVERSAL_INPUT_ROOT,
    );
    const main = await readFile(join(universalRoot, "vibermate-desktop"));
    const launcher = await readFile(join(universalRoot, "vibermate"));
    const daemon = await readFile(join(universalRoot, "vibermated"));
    await writeFixtureFile(value.mainPath, main, 0o755);
    await writeFixtureFile(value.launcherPath, launcher, 0o755);
    await writeFixtureFile(value.daemonPath, daemon, 0o755);
    await writeFixtureFile(
      value.buildManifestPath,
      `${JSON.stringify(
        {
          ...value.buildManifest,
          nestedCodeSHA256: {
            ...value.buildManifest.nestedCodeSHA256,
            vibermate: sha256(launcher),
            vibermated: sha256(daemon),
          },
        },
        null,
        2,
      )}\n`,
      0o600,
    );
    await prepareR0ReleaseEvidence(
      preparationOptions(value),
      { generateSyftSPDX: trustedSyftGenerator() },
    );
  },
);

test("source-traceability preparer (R0) consumes only the closed external build-input root", async (t) => {
  const value = await fixture(t);
  const inputRoot = await externalInputRoot(value);
  await rm(join(value.repositoryRoot, "ui/flutter_app/build"), {
    recursive: true,
  });
  await prepareR0ReleaseEvidence(
    { ...preparationOptions(value), inputRoot },
    preparationDependencies(),
  );
  assert.deepEqual(
    (await readdir(join(value.artifactRoot, "unsigned-payload"))).sort(),
    [
      "LICENSE",
      "dist",
      "vibermate",
      "vibermate-build-manifest.json",
      "vibermate-desktop",
      "vibermated",
    ],
  );
});

test("source-traceability preparer (R0) closes external build-input inventory and LICENSE", async (t) => {
  await t.test("extra root entry", async (t) => {
    const value = await fixture(t);
    const inputRoot = await externalInputRoot(value);
    await writeFixtureFile(join(inputRoot, "extra"), "unexpected\n");
    await assert.rejects(
      prepareR0ReleaseEvidence(
        { ...preparationOptions(value), inputRoot },
        preparationDependencies(),
      ),
      /exceeds the entry limit|unexpected inventory/u,
    );
    await assertAbsent(value.artifactRoot);
  });
  await t.test("untracked LICENSE bytes", async (t) => {
    const value = await fixture(t);
    const inputRoot = await externalInputRoot(value);
    await writeFile(join(inputRoot, "LICENSE"), "different license\n");
    await assert.rejects(
      prepareR0ReleaseEvidence(
        { ...preparationOptions(value), inputRoot },
        preparationDependencies(),
      ),
      /does not match the tracked candidate LICENSE/u,
    );
    await assertAbsent(value.artifactRoot);
  });
});

test("source-traceability preparer CLI grammar (R0) is closed and explicit", () => {
  const parsed = parseR0EvidenceArguments([
    "--artifact-root=/private/tmp/artifacts",
    `--expected-revision=${"a".repeat(40)}`,
    "--input-root=/private/tmp/input",
    "--source-root=/private/tmp/source",
    "--syft-bin=/private/tmp/syft",
  ]);
  assert.equal(parsed.artifactRoot, "/private/tmp/artifacts");
  assert.equal(parsed.inputRoot, "/private/tmp/input");
  assert.equal(parsed.repositoryRoot, "/private/tmp/source");
  assert.equal(parsed.syftBinaryPath, "/private/tmp/syft");
  assert.throws(() => parseR0EvidenceArguments([]));
  assert.throws(() =>
    parseR0EvidenceArguments([
      "--artifact-root=relative",
      `--expected-revision=${"a".repeat(40)}`,
      "--input-root=/private/tmp/input",
      "--source-root=/private/tmp/source",
      "--syft-bin=/private/tmp/syft",
    ]),
  );
  assert.throws(() =>
    parseR0EvidenceArguments([
      "--artifact-root=/private/tmp/first",
      "--artifact-root=/private/tmp/second",
      `--expected-revision=${"a".repeat(40)}`,
      "--input-root=/private/tmp/input",
      "--source-root=/private/tmp/source",
      "--syft-bin=/private/tmp/syft",
    ]),
  );
  assert.throws(() =>
    parseR0EvidenceArguments([
      "--artifact-root=/private/tmp/artifacts",
      `--expected-revision=${"a".repeat(40)}`,
      "--input-root=/private/tmp/input",
      "--source-root=/private/tmp/source",
      "--syft-bin=/private/tmp/syft",
      "--output=/private/tmp/other",
    ]),
  );
  assert.throws(() =>
    parseR0EvidenceArguments([
      "--artifact-root=/private/tmp/artifacts",
      `--expected-revision=${"a".repeat(40)}`,
      "--input-root=/private/tmp/input",
      "--source-root=/private/tmp/source",
      "--syft-spdx=/private/tmp/syft.json",
    ]),
  );
});

test("source-traceability preparer (R0) rejects source symbolic links", async (t) => {
  const value = await fixture(t);
  await rm(value.mainPath);
  await symlink(value.launcherPath, value.mainPath);
  await assert.rejects(
    prepareR0ReleaseEvidence(
      preparationOptions(value),
      preparationDependencies(),
    ),
    /symbolic link/u,
  );
  await assertAbsent(value.artifactRoot);
});

test("source-traceability preparer (R0) rejects source special files", async (t) => {
  const value = await fixture(t);
  await rm(value.daemonPath);
  try {
    execFileSync("mkfifo", [value.daemonPath]);
  } catch {
    t.skip("mkfifo is unavailable on this platform");
    return;
  }
  await assert.rejects(
    prepareR0ReleaseEvidence(
      preparationOptions(value),
      preparationDependencies(),
    ),
    /regular file|special file/u,
  );
  await assertAbsent(value.artifactRoot);
});

test("source-traceability preparer (R0) rejects privileged source mode bits", async (t) => {
  const value = await fixture(t);
  await chmod(value.mainPath, 0o4755);
  await assert.rejects(
    prepareR0ReleaseEvidence(
      preparationOptions(value),
      preparationDependencies(),
    ),
    /privileged mode bits/u,
  );
  await assertAbsent(value.artifactRoot);
});

test("source-traceability preparer (R0) rejects source hard-link aliases", async (t) => {
  const value = await fixture(t);
  await rm(value.mainPath);
  await link(value.launcherPath, value.mainPath);
  await assert.rejects(
    prepareR0ReleaseEvidence(
      preparationOptions(value),
      preparationDependencies(),
    ),
    /hard-link aliases/u,
  );
  await assertAbsent(value.artifactRoot);
});

test("source-traceability preparer (R0) rejects missing and empty build inputs", async (t) => {
  await t.test("missing", async (t) => {
    const value = await fixture(t);
    await rm(value.buildManifestPath);
    await assert.rejects(
      prepareR0ReleaseEvidence(
        preparationOptions(value),
        preparationDependencies(),
      ),
      /ENOENT|no such file/u,
    );
    await assertAbsent(value.artifactRoot);
  });
  await t.test("empty dist", async (t) => {
    const value = await fixture(t);
    await rm(value.distPath, { recursive: true });
    await mkdir(value.distPath, { mode: 0o755 });
    await assert.rejects(
      prepareR0ReleaseEvidence(
        preparationOptions(value),
        preparationDependencies(),
      ),
      /dist source must contain at least one regular file/u,
    );
    await assertAbsent(value.artifactRoot);
  });
});

test("source-traceability preparer (R0) rejects dirty source and wrong revisions", async (t) => {
  await t.test("dirty", async (t) => {
    const value = await fixture(t);
    await writeFixtureFile(join(value.repositoryRoot, "dirty"), "dirty\n");
    await assert.rejects(
      prepareR0ReleaseEvidence(
        preparationOptions(value),
        preparationDependencies(),
      ),
      /worktree is dirty/u,
    );
    await assertAbsent(value.artifactRoot);
  });
  await t.test("assume-unchanged cannot mask a tracked change", async (t) => {
    const value = await fixture(t);
    runGit(
      value.repositoryRoot,
      "update-index",
      "--assume-unchanged",
      "go.mod",
    );
    await writeFixtureFile(
      join(value.repositoryRoot, "go.mod"),
      "module example.invalid/masked\n\ngo 1.25.12\n",
    );
    await assert.rejects(
      prepareR0ReleaseEvidence(
        preparationOptions(value),
        preparationDependencies(),
      ),
      /masked or non-ordinary tracked entry/u,
    );
    await assertAbsent(value.artifactRoot);
  });
  await t.test("skip-worktree cannot mask a tracked change", async (t) => {
    const value = await fixture(t);
    runGit(
      value.repositoryRoot,
      "update-index",
      "--skip-worktree",
      "go.mod",
    );
    await writeFixtureFile(
      join(value.repositoryRoot, "go.mod"),
      "module example.invalid/masked\n\ngo 1.25.12\n",
    );
    await assert.rejects(
      prepareR0ReleaseEvidence(
        preparationOptions(value),
        preparationDependencies(),
      ),
      /masked or non-ordinary tracked entry/u,
    );
    await assertAbsent(value.artifactRoot);
  });
  await t.test("repository-local excludes cannot mask untracked input", async (t) => {
    const value = await fixture(t);
    await writeFile(
      join(value.repositoryRoot, ".git/info/exclude"),
      "masked-input\n",
    );
    await writeFixtureFile(
      join(value.repositoryRoot, "masked-input"),
      "hidden from ordinary Git status\n",
    );
    await assert.rejects(
      prepareR0ReleaseEvidence(
        preparationOptions(value),
        preparationDependencies(),
      ),
      /exclude file contains an active pattern/u,
    );
    await assertAbsent(value.artifactRoot);
  });
  await t.test("replacement objects cannot rewrite the admitted commit", async (t) => {
    const value = await fixture(t);
    const tree = runGit(value.repositoryRoot, "rev-parse", "HEAD^{tree}");
    const replacement = execFileSync(
      "git",
      ["-C", value.repositoryRoot, "commit-tree", tree],
      {
        encoding: "utf8",
        env: {
          ...process.env,
          GIT_AUTHOR_DATE: "2030-01-02T03:04:05Z",
          GIT_COMMITTER_DATE: "2030-01-02T03:04:05Z",
        },
        input: "replacement commit\n",
      },
    ).trim();
    runGit(value.repositoryRoot, "replace", value.revision, replacement);
    assert.equal(
      runGit(value.repositoryRoot, "show", "-s", "--format=%cI", "HEAD"),
      "2030-01-02T03:04:05Z",
    );
    await prepareR0ReleaseEvidence(
      preparationOptions(value),
      preparationDependencies(),
    );
  });
  await t.test("wrong expected revision", async (t) => {
    const value = await fixture(t);
    await assert.rejects(
      prepareR0ReleaseEvidence(
        {
          ...preparationOptions(value),
          expectedRevision: "b".repeat(40),
        },
        preparationDependencies(),
      ),
      /HEAD does not match/u,
    );
    await assertAbsent(value.artifactRoot);
  });
  await t.test("wrong build-manifest revision", async (t) => {
    const value = await fixture(t);
    const malformedBinding = {
      ...value.buildManifest,
      source: { ...value.buildManifest.source, revision: "b".repeat(40) },
    };
    await writeFixtureFile(
      value.buildManifestPath,
      `${JSON.stringify(malformedBinding, null, 2)}\n`,
      0o600,
    );
    await assert.rejects(
      prepareR0ReleaseEvidence(
        preparationOptions(value),
        preparationDependencies(),
      ),
      /does not bind the exact clean Git source/u,
    );
    await assertAbsent(value.artifactRoot);
  });
});

test("source-traceability preparer (R0) binds build-manifest configuration and nested-code digests", async (t) => {
  await t.test("configuration digest", async (t) => {
    const value = await fixture(t);
    const unbound = {
      ...value.buildManifest,
      configurationSHA256: {
        ...value.buildManifest.configurationSHA256,
        "go.mod": "f".repeat(64),
      },
    };
    await writeFixtureFile(
      value.buildManifestPath,
      `${JSON.stringify(unbound, null, 2)}\n`,
      0o600,
    );
    await assert.rejects(
      prepareR0ReleaseEvidence(
        preparationOptions(value),
        preparationDependencies(),
      ),
      /configurationSHA256 does not bind go.mod/u,
    );
    await assertAbsent(value.artifactRoot);
  });
  await t.test("Go sidecar nested-code digest", async (t) => {
    const value = await fixture(t);
    await writeFixtureFile(value.launcherPath, "changed sidecar bytes\n", 0o755);
    await assert.rejects(
      prepareR0ReleaseEvidence(
        preparationOptions(value),
        preparationDependencies(),
      ),
      /sidecar bytes do not match/u,
    );
    await assertAbsent(value.artifactRoot);
  });
});

test("source-traceability preparer (R0) validates and normalizes bounded Syft JSON", async (t) => {
  await t.test("malformed", async (t) => {
    const value = await fixture(t);
    await assert.rejects(
      prepareR0ReleaseEvidence(
        preparationOptions(value),
        preparationDependencies({
          generateSyftSPDX: rawSyftGenerator("{\n"),
        }),
      ),
      /malformed JSON/u,
    );
    await assertAbsent(value.artifactRoot);
  });
  await t.test("duplicate member", async (t) => {
    const value = await fixture(t);
    const duplicateJSON =
      '{"spdxVersion":"SPDX-2.3","packages":[],"packages":[]}\n';
    await assert.rejects(
      prepareR0ReleaseEvidence(
        preparationOptions(value),
        preparationDependencies({
          generateSyftSPDX: rawSyftGenerator(duplicateJSON),
        }),
      ),
      /duplicate member/u,
    );
    await assertAbsent(value.artifactRoot);
  });
  await t.test("duplicate package is normalized once", async (t) => {
    const value = await fixture(t);
    await prepareR0ReleaseEvidence(
      preparationOptions(value),
      preparationDependencies({
        generateSyftSPDX: trustedSyftGenerator((document) => {
          document.packages.push(
            {
              SPDXID: "SPDXRef-Package-duplicate-one",
              name: "duplicate",
              versionInfo: "1.0.0",
            },
            {
              SPDXID: "SPDXRef-Package-duplicate-two",
              name: "duplicate",
              versionInfo: "1.0.0",
            },
          );
        }),
      }),
    );
    const sbom = JSON.parse(
      await readFile(join(value.artifactRoot, "sbom.spdx.json"), "utf8"),
    );
    assert.equal(
      sbom.packages.filter(
        (pkg) => pkg.name === "duplicate" && pkg.versionInfo === "1.0.0",
      ).length,
      1,
    );
  });
  await t.test("file digest must bind the payload ledger", async (t) => {
    const value = await fixture(t);
    await assert.rejects(
      prepareR0ReleaseEvidence(
        preparationOptions(value),
        preparationDependencies({
          generateSyftSPDX: trustedSyftGenerator((document) => {
            const file = document.files.find(
              (entry) => entry.checksums[0].algorithm === "SHA256",
            );
            file.checksums[0].checksumValue = "f".repeat(64);
          }),
        }),
      ),
      /file digest does not match the payload ledger/u,
    );
    await assertAbsent(value.artifactRoot);
  });
  await t.test("file path must bind the payload ledger", async (t) => {
    const value = await fixture(t);
    await assert.rejects(
      prepareR0ReleaseEvidence(
        preparationOptions(value),
        preparationDependencies({
          generateSyftSPDX: trustedSyftGenerator((document) => {
            document.files.find((entry) => entry.fileName !== "").fileName =
              "not-in-the-payload-ledger";
          }),
        }),
      ),
      /unknown or duplicate payload path/u,
    );
    await assertAbsent(value.artifactRoot);
  });
  await t.test("extra DESCRIBES is rejected", async (t) => {
    const value = await fixture(t);
    await assert.rejects(
      prepareR0ReleaseEvidence(
        preparationOptions(value),
        preparationDependencies({
          generateSyftSPDX: trustedSyftGenerator((document) => {
            document.relationships.push({
              spdxElementId: "SPDXRef-DOCUMENT",
              relatedSpdxElement: syftRootSPDXID,
              relationshipType: "DESCRIBES",
            });
          }),
        }),
      ),
      /must describe exactly one payload root/u,
    );
    await assertAbsent(value.artifactRoot);
  });
  await t.test("wrong DESCRIBES target is rejected", async (t) => {
    const value = await fixture(t);
    await assert.rejects(
      prepareR0ReleaseEvidence(
        preparationOptions(value),
        preparationDependencies({
          generateSyftSPDX: trustedSyftGenerator((document) => {
            document.relationships[0].relatedSpdxElement =
              "SPDXRef-DocumentRoot-wrong";
          }),
        }),
      ),
      /describes the wrong payload root/u,
    );
    await assertAbsent(value.artifactRoot);
  });
  await t.test("oversized", async (t) => {
    const value = await fixture(t);
    await assert.rejects(
      prepareR0ReleaseEvidence(
        preparationOptions(value),
        preparationDependencies({
          generateSyftSPDX: rawSyftGenerator(Buffer.alloc(257, 0x20)),
          limits: { syftInputBytes: 256 },
        }),
      ),
      /invalid or excessive size/u,
    );
    await assertAbsent(value.artifactRoot);
  });
});

test("source-traceability preparer (R0) refuses an existing artifact root without changing it", async (t) => {
  const value = await fixture(t);
  await mkdir(value.artifactRoot, { mode: 0o700 });
  const sentinel = join(value.artifactRoot, "keep");
  await writeFixtureFile(sentinel, "keep\n", 0o600);
  await assert.rejects(
    prepareR0ReleaseEvidence(
      preparationOptions(value),
      preparationDependencies(),
    ),
    /artifact root already exists/u,
  );
  assert.equal(await readFile(sentinel, "utf8"), "keep\n");
});
