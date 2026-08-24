import assert from "node:assert/strict";
import test from "node:test";
import {
  flutterDesktopBuildConfigurationNames,
  flutterDesktopBuildManifestSchema,
  flutterDesktopNestedCode,
  validateFlutterDesktopBuildManifest,
} from "./desktop_build_manifest.mjs";

const digest = "a".repeat(64);
const revision = "b".repeat(40);

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
