import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import test from "node:test";
import {
  bundleProfileFromEnvironment,
  cargoPackageVersion,
  cargoRustVersion,
  desktopBuildManifestSchema,
  desktopDistributionSidecarTargetAliases,
  desktopBundleTarget,
  desktopBundleProfileEnvironment,
  desktopUnsignedDistributionBundleEnvironment,
  goModuleVersion,
  parseSidecarProfile,
  pnpmPackageManagerVersion,
  requireBundleSigningBoundary,
  requireDesktopBundleTarget,
  requireReleaseSource,
  requireStableRevision,
  rustToolchainVersion,
  sidecarBuildTags,
  validateDesktopVersions,
  validatePreparedBundle,
  validateReleaseToolchains,
} from "./desktop-build-policy.mjs";

test("selected brand assets stay pinned, local, and attributed", () => {
  const iconDirectory = new URL("../src/assets/brand-icons/", import.meta.url);
  assert.deepEqual(readdirSync(iconDirectory).sort(), [
    "README.md",
    "anthropic.svg",
    "claude-code.svg",
    "codex.svg",
    "openai.svg",
  ]);

  const sourceNotice = readFileSync(new URL("README.md", iconDirectory), "utf8");
  assert.match(sourceNotice, /f07e9be35aef452ce735f95ea8204a14ecc513f7/u);
  assert.match(sourceNotice, /Compatible and custom services deliberately use/u);

  for (const filename of readdirSync(iconDirectory).filter((name) =>
    name.endsWith(".svg"),
  )) {
    const svg = readFileSync(new URL(filename, iconDirectory), "utf8");
    assert.match(svg, /^<svg\b/u);
    assert.match(svg, /viewBox="0 0 24 24"/u);
    assert.doesNotMatch(svg, /<script\b|\bhref\s*=|\bon\w+\s*=/iu);
  }

  const license = readFileSync(
    new URL("../public/licenses/lobe-icons.txt", import.meta.url),
    "utf8",
  );
  assert.match(license, /^MIT License/u);
  assert.match(license, /Copyright \(c\) 2023 LobeHub/u);
});

test("sidecar profiles are explicit and non-development builds use native secrets", () => {
  assert.equal(parseSidecarProfile(["--profile=development"]), "development");
  assert.equal(parseSidecarProfile(["--profile=release"]), "release");
  assert.equal(
    parseSidecarProfile(["--profile=distribution"]),
    "distribution",
  );
  assert.deepEqual(sidecarBuildTags("development"), []);
  assert.deepEqual(sidecarBuildTags("release"), ["vibermate_native_secrets"]);
  assert.deepEqual(sidecarBuildTags("distribution"), [
    "vibermate_native_secrets",
  ]);
  assert.throws(() => parseSidecarProfile([]));
  assert.throws(() => parseSidecarProfile(["--profile=debug"]));
  assert.throws(() => parseSidecarProfile(["--profile=release", "extra"]));
});

test("release source policy rejects every dirty worktree signal", () => {
  assert.doesNotThrow(() => requireReleaseSource("release", "\n"));
  assert.throws(() => requireReleaseSource("release", " M package.json\n"));
  assert.throws(() =>
    requireReleaseSource("distribution", " M package.json\n"),
  );
  assert.doesNotThrow(() =>
    requireReleaseSource("development", " M package.json\n"),
  );
});

test("Tauri bundle profile is mandatory and explicit", () => {
  assert.equal(
    bundleProfileFromEnvironment({
      [desktopBundleProfileEnvironment]: "development",
    }),
    "development",
  );
  assert.equal(
    bundleProfileFromEnvironment({
      [desktopBundleProfileEnvironment]: "release",
    }),
    "release",
  );
  assert.equal(
    bundleProfileFromEnvironment({
      [desktopBundleProfileEnvironment]: "distribution",
    }),
    "distribution",
  );
  assert.throws(() => bundleProfileFromEnvironment({}));
  assert.throws(() =>
    bundleProfileFromEnvironment({
      [desktopBundleProfileEnvironment]: "debug",
    }),
  );
});

test("Tauri targets are fixed by the repository-owned bundle profile", () => {
  const target = {
    TAURI_ENV_TARGET_TRIPLE: "aarch64-apple-darwin",
    TAURI_ENV_ARCH: "aarch64",
    TAURI_ENV_PLATFORM: "darwin",
    TAURI_ENV_DEBUG: "false",
  };
  assert.equal(desktopBundleTarget("release"), "aarch64-apple-darwin");
  assert.equal(desktopBundleTarget("distribution"), "universal-apple-darwin");
  assert.deepEqual(desktopDistributionSidecarTargetAliases, [
    "aarch64-apple-darwin",
    "x86_64-apple-darwin",
  ]);
  assert.equal(Object.isFrozen(desktopDistributionSidecarTargetAliases), true);
  assert.doesNotThrow(() => requireDesktopBundleTarget("release", target));
  assert.doesNotThrow(() =>
    requireDesktopBundleTarget("development", {
      ...target,
      TAURI_ENV_DEBUG: undefined,
    }),
  );
  for (const [name, value] of [
    ["TAURI_ENV_TARGET_TRIPLE", "x86_64-apple-darwin"],
    ["TAURI_ENV_ARCH", "x86_64"],
    ["TAURI_ENV_PLATFORM", "linux"],
    ["TAURI_ENV_DEBUG", "true"],
  ]) {
    assert.throws(() =>
      requireDesktopBundleTarget("release", { ...target, [name]: value }),
    );
  }
  assert.doesNotThrow(() =>
    requireDesktopBundleTarget("distribution", {
      ...target,
      TAURI_ENV_ARCH: "universal",
      TAURI_ENV_TARGET_TRIPLE: "universal-apple-darwin",
    }),
  );
  assert.throws(() => requireDesktopBundleTarget("distribution", target));
});

test("build, signing, and notarization credentials stay in separate stages", () => {
  assert.doesNotThrow(() =>
    requireBundleSigningBoundary("development", "build", {}),
  );
  assert.doesNotThrow(() =>
    requireBundleSigningBoundary("development", "bundle", {
      APPLE_SIGNING_IDENTITY: " ",
    }),
  );
  assert.throws(() =>
    requireBundleSigningBoundary("development", "bundle", {
      APPLE_SIGNING_IDENTITY: "Developer ID Application: Example",
    }),
  );
  assert.throws(() =>
    requireBundleSigningBoundary("development", "build", {
      APPLE_API_KEY_PATH: "/private/release/AuthKey.p8",
    }),
  );
  assert.throws(() =>
    requireBundleSigningBoundary("development", "build", {
      APPLE_FUTURE_CREDENTIAL: "secret",
    }),
  );
  assert.throws(() =>
    requireBundleSigningBoundary("release", "bundle", {
      APPLE_SIGNING_IDENTITY: "Developer ID Application: Example",
    }),
  );
  assert.throws(() =>
    requireBundleSigningBoundary("release", "bundle", {
      TAURI_SKIP_SIDECAR_SIGNATURE_CHECK: "true",
    }),
  );
  assert.doesNotThrow(() =>
    requireBundleSigningBoundary("distribution", "build", {}),
  );
  assert.throws(() =>
    requireBundleSigningBoundary("distribution", "build", {
      APPLE_SIGNING_IDENTITY: "a".repeat(40),
    }),
  );
  assert.throws(() =>
    requireBundleSigningBoundary("distribution", "build", {
      DEVELOPMENT_TEAM: "A1B2C3D4E5",
    }),
  );
  const signing = {
    APPLE_SIGNING_IDENTITY: "a".repeat(40),
    VIBERMATE_APPLE_TEAM_ID: "A1B2C3D4E5",
  };
  assert.doesNotThrow(() =>
    requireBundleSigningBoundary("distribution", "bundle", signing),
  );
  assert.doesNotThrow(() =>
    requireBundleSigningBoundary("distribution", "bundle", {
      [desktopUnsignedDistributionBundleEnvironment]: "1",
    }),
  );
  assert.throws(() =>
    requireBundleSigningBoundary("distribution", "bundle", {
      [desktopUnsignedDistributionBundleEnvironment]: "true",
    }),
  );
  assert.throws(() =>
    requireBundleSigningBoundary("distribution", "bundle", {
      [desktopUnsignedDistributionBundleEnvironment]: "1",
      APPLE_SIGNING_IDENTITY: "a".repeat(40),
    }),
  );
  assert.throws(() =>
    requireBundleSigningBoundary("distribution", "bundle", {
      ...signing,
      APPLE_SIGNING_IDENTITY: "-",
    }),
  );
  assert.throws(() =>
    requireBundleSigningBoundary("distribution", "bundle", {
      ...signing,
      APPLE_API_KEY: "notary-key",
    }),
  );
  assert.throws(() =>
    requireBundleSigningBoundary("distribution", "bundle", {
      ...signing,
      APPLE_CERTIFICATE: "base64-secret",
    }),
  );
  assert.throws(() =>
    requireBundleSigningBoundary("distribution", "bundle", {
      ...signing,
      APPLE_FUTURE_NOTARY_SECRET: "secret",
    }),
  );
  assert.throws(() =>
    requireBundleSigningBoundary("distribution", "bundle", {
      ...signing,
      OTHER_CODE_SIGN_FLAGS: "--preserve-metadata=entitlements",
    }),
  );
  assert.throws(() =>
    requireBundleSigningBoundary("distribution", "notary", signing),
  );
});

test("Desktop sidecar builds retain one full Git revision", () => {
  const revision = "1".repeat(40);
  assert.doesNotThrow(() => requireStableRevision(revision, revision));
  assert.throws(() => requireStableRevision("1".repeat(39), "1".repeat(39)));
  assert.throws(() => requireStableRevision(revision, "2".repeat(40)));
});

test("desktop versions are semantic, synchronized, and non-placeholder", () => {
  const versions = {
    packageVersion: "0.1.0",
    tauriVersion: "0.1.0",
    cargoVersion: "0.1.0",
  };
  assert.equal(validateDesktopVersions("release", versions), "0.1.0");
  assert.equal(validateDesktopVersions("distribution", versions), "0.1.0");
  assert.throws(() =>
    validateDesktopVersions("release", {
      ...versions,
      cargoVersion: "0.1.1",
    }),
  );
  assert.throws(() =>
    validateDesktopVersions("release", {
      packageVersion: "0.0.0",
      tauriVersion: "0.0.0",
      cargoVersion: "0.0.0",
    }),
  );
  assert.equal(
    validateDesktopVersions("development", {
      packageVersion: "0.0.0",
      tauriVersion: "0.0.0",
      cargoVersion: "0.0.0",
    }),
    "0.0.0",
  );
});

test("Cargo package version is read only from the package table", () => {
  assert.equal(
    cargoPackageVersion('[workspace]\nresolver = "2"\n\n[package]\nversion = "0.1.0"\n\n[dependencies]\nthing = "9.9.9"\n'),
    "0.1.0",
  );
  assert.throws(() => cargoPackageVersion("[package]\nname = \"missing\"\n"));
});

test("release toolchain pins are derived from exact repository declarations", () => {
  assert.equal(
    cargoRustVersion('[package]\nrust-version = "1.88"\n'),
    "1.88.0",
  );
  assert.equal(goModuleVersion("module example.test\n\ngo 1.25.12\n"), "1.25.12");
  assert.equal(pnpmPackageManagerVersion("pnpm@10.33.2"), "10.33.2");
  assert.equal(
    rustToolchainVersion('[toolchain]\nchannel = "1.88.0"\nprofile = "minimal"\n'),
    "1.88.0",
  );
  assert.throws(() => goModuleVersion("module example.test\n\ngo 1.25\n"));
  assert.throws(() => pnpmPackageManagerVersion("pnpm@latest"));
  assert.throws(() =>
    rustToolchainVersion('[toolchain]\nchannel = "stable"\n'),
  );
});

test("release rejects drift from pinned toolchains while development records it", () => {
  const expected = {
    go: "1.25.12",
    node: "22.23.1",
    rustc: "1.88.0",
    cargo: "1.88.0",
    pnpm: "10.33.2",
    tauri: "2.11.4",
  };
  assert.doesNotThrow(() =>
    validateReleaseToolchains("release", { expected, actual: expected }),
  );
  const drifted = { ...expected, node: "25.8.1" };
  assert.throws(() =>
    validateReleaseToolchains("release", { expected, actual: drifted }),
  );
  assert.throws(() =>
    validateReleaseToolchains("distribution", { expected, actual: drifted }),
  );
  assert.doesNotThrow(() =>
    validateReleaseToolchains("development", {
      expected,
      actual: drifted,
    }),
  );
});

test("pre-bundle verification binds profile, source, inputs, and sidecar bytes", () => {
  const revision = "1".repeat(40);
  const configurationSHA256 = {
    "go.mod": "a".repeat(64),
    "go.sum": "b".repeat(64),
    "rust-toolchain.toml": "e".repeat(64),
    "ui/desktop/package.json": "f".repeat(64),
    "ui/desktop/pnpm-lock.yaml": "1".repeat(64),
    "ui/desktop/src-tauri/Cargo.toml": "2".repeat(64),
    "ui/desktop/src-tauri/Cargo.lock": "3".repeat(64),
    "ui/desktop/src-tauri/tauri.conf.json": "4".repeat(64),
  };
  const sidecarSHA256 = {
    vibermated: "c".repeat(64),
    vibermate: "d".repeat(64),
  };
  const manifest = {
    schema: desktopBuildManifestSchema,
    source: {
      vcs: "git",
      revision,
      commitTime: "2026-08-03T00:00:00Z",
      dirty: false,
    },
    profiles: {
      desktop: "release",
      sidecars: "release",
      target: "aarch64-apple-darwin",
    },
    toolchains: {
      go: "go version go1.25.12 darwin/arm64",
      node: "v22.23.1",
      rustc: "rustc 1.88.0\nhost: aarch64-apple-darwin",
      cargo: "cargo 1.88.0",
      pnpm: "10.33.2",
      tauri: "tauri-cli 2.11.4",
    },
    configurationSHA256,
    sidecarSHA256,
  };
  const evidence = {
    manifest,
    currentRevision: revision,
    currentCommitTime: "2026-08-03T00:00:00Z",
    porcelainStatus: "",
    currentToolchains: manifest.toolchains,
    currentConfigurationSHA256: configurationSHA256,
    currentSidecarSHA256: sidecarSHA256,
  };
  assert.doesNotThrow(() => validatePreparedBundle("release", evidence));
  assert.doesNotThrow(() =>
    validatePreparedBundle("distribution", {
      ...evidence,
      manifest: {
        ...manifest,
        profiles: {
          ...manifest.profiles,
          sidecars: "distribution",
          target: "universal-apple-darwin",
        },
      },
    }),
  );
  assert.throws(() =>
    validatePreparedBundle("development", evidence),
  );
  assert.throws(() =>
    validatePreparedBundle("release", {
      ...evidence,
      currentSidecarSHA256: {
        ...sidecarSHA256,
        vibermate: "e".repeat(64),
      },
    }),
  );
  assert.throws(() =>
    validatePreparedBundle("release", {
      ...evidence,
      porcelainStatus: " M ui/desktop/package.json\n",
    }),
  );
  assert.throws(() =>
    validatePreparedBundle("release", {
      ...evidence,
      manifest: { ...manifest, unexpected: true },
    }),
  );
  const configurationWithoutRust = { ...configurationSHA256 };
  delete configurationWithoutRust["rust-toolchain.toml"];
  assert.throws(() =>
    validatePreparedBundle("release", {
      ...evidence,
      manifest: {
        ...manifest,
        configurationSHA256: configurationWithoutRust,
      },
      currentConfigurationSHA256: configurationWithoutRust,
    }),
  );
  assert.throws(() =>
    validatePreparedBundle("release", {
      ...evidence,
      manifest: {
        ...manifest,
        schema: "vibermate.desktop-build/v1",
      },
    }),
  );
});
