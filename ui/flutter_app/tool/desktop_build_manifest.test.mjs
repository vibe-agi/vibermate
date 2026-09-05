import assert from "node:assert/strict";
import test from "node:test";
import { validateMacOSBuildVersions } from "./verify_macos_build_versions.mjs";
import {
  flutterDesktopBuildConfigurationNames,
  flutterDesktopBuildManifestSchema,
  flutterDesktopNestedCode,
  normalizeFlutterToolchains,
  validateFlutterDesktopBuildManifest,
} from "./desktop_build_manifest.mjs";

const digest = "a".repeat(64);
const revision = "b".repeat(40);

test("every Mach-O slice supports macOS 14 regardless of the build SDK", () => {
  const slice = (minimum, platform = "MACOS") =>
    `  cmd LC_BUILD_VERSION\n platform ${platform}\n    minos ${minimum}\n      sdk 27.0\n`;
  assert.doesNotThrow(() => validateMacOSBuildVersions(slice("14.0"), 1, "daemon"));
  assert.doesNotThrow(() => validateMacOSBuildVersions(slice("10.15") + slice("11.0"), 2, "Flutter"));
  for (const source of [slice("27.0"), slice("26.0"), slice("14.1"), slice("14.0.1"), slice("14.0", "IOS"), ""]) {
    assert.throws(() => validateMacOSBuildVersions(source, 1, "daemon"));
  }
  assert.throws(() => validateMacOSBuildVersions(slice("14.0") + slice("27.0"), 2, "universal"));
  assert.throws(() => validateMacOSBuildVersions(slice("14.0"), 2, "missing slice"));
});

function manifest(overrides = {}) {
  return {
    schema: flutterDesktopBuildManifestSchema,
    source: {
      vcs: "git",
      revision,
      commitTime: "2026-08-11T00:00:00Z",
      dirty: false,
    },
    profiles: {
      desktop: "release",
      sidecars: "release",
      target: "universal-apple-darwin",
      toolkit: "flutter",
    },
    toolchains: {
      dart: "Dart 3.11.3",
      flutter: `Flutter 3.41.5 (${"c".repeat(40)})`,
      go: "go version go1.25.13 darwin/arm64",
      xcode: "Xcode 16.2\nBuild version 16C5032a",
    },
    configurationSHA256: Object.fromEntries(
      flutterDesktopBuildConfigurationNames.map((name) => [name, digest]),
    ),
    nestedCodeSHA256: Object.fromEntries(
      Object.keys(flutterDesktopNestedCode).map((name) => [name, digest]),
    ),
    ...overrides,
  };
}

test("v3 is a closed Flutter-only build contract", () => {
  assert.equal(flutterDesktopBuildManifestSchema, "vibermate.desktop-build/v3");
  assert.ok(
    flutterDesktopBuildConfigurationNames.every(
      (name) => !name.includes("ui/desktop") && !name.includes("rust"),
    ),
  );
  assert.deepEqual(Object.keys(flutterDesktopNestedCode).sort(), [
    "app-framework",
    "flutter-macos-framework",
    "vibermate",
    "vibermated",
  ]);
  assert.doesNotThrow(() =>
    validateFlutterDesktopBuildManifest(manifest(), {
      expectedRevision: revision,
      expectedTarget: "universal-apple-darwin",
    }),
  );
});

test("manifest refuses stale, dirty, non-Flutter, and open shapes", () => {
  assert.throws(() =>
    validateFlutterDesktopBuildManifest(
      manifest({ source: { ...manifest().source, dirty: true } }),
    ),
  );
  assert.doesNotThrow(() =>
    validateFlutterDesktopBuildManifest(
      manifest({ source: { ...manifest().source, dirty: true } }),
      { requireClean: false },
    ),
  );
  assert.throws(() =>
    validateFlutterDesktopBuildManifest(
      manifest({ profiles: { ...manifest().profiles, toolkit: "tauri" } }),
    ),
  );
  assert.throws(() =>
    validateFlutterDesktopBuildManifest(
      { ...manifest(), unexpected: true },
      { requireClean: false },
    ),
  );
  assert.throws(() =>
    validateFlutterDesktopBuildManifest(manifest(), {
      expectedRevision: "d".repeat(40),
    }),
  );
});

test("manifest requires every Flutter configuration and nested code digest", () => {
  const missingConfiguration = manifest();
  delete missingConfiguration.configurationSHA256["ui/flutter_app/pubspec.lock"];
  assert.throws(() =>
    validateFlutterDesktopBuildManifest(missingConfiguration),
  );

  const missingFramework = manifest();
  delete missingFramework.nestedCodeSHA256["app-framework"];
  assert.throws(() =>
    validateFlutterDesktopBuildManifest(missingFramework),
  );

  const invalidDigest = manifest();
  invalidDigest.nestedCodeSHA256.vibermate = "not-a-digest";
  assert.throws(() => validateFlutterDesktopBuildManifest(invalidDigest));
});

test("exact Flutter revision is authoritative across installation channels", () => {
  const expectedVersion = "3.41.5";
  const expectedRevision = "c".repeat(40);
  const machine = {
    frameworkVersion: expectedVersion,
    frameworkRevision: expectedRevision,
    dartSdkVersion: "3.11.3",
    channel: "[user-branch]",
  };

  assert.deepEqual(
    normalizeFlutterToolchains(machine, {
      expectedVersion,
      expectedRevision,
    }),
    {
      dart: "Dart 3.11.3",
      flutter: `Flutter ${expectedVersion} (${expectedRevision})`,
    },
  );
  assert.throws(() =>
    normalizeFlutterToolchains(
      { ...machine, frameworkRevision: "d".repeat(40) },
      { expectedVersion, expectedRevision },
    ),
  );
});
