import { createHash, randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import {
  chmod,
  lstat,
  mkdir,
  open,
  readFile,
  realpath,
  rename,
  rm,
} from "node:fs/promises";
import { dirname, isAbsolute, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const toolDirectory = dirname(fileURLToPath(import.meta.url));
const flutterDirectory = resolve(toolDirectory, "..");
const defaultRepositoryRoot = resolve(flutterDirectory, "../..");
const digestPattern = /^[0-9a-f]{64}$/u;
const revisionPattern = /^[0-9a-f]{40}$/u;
const maximumManifestBytes = 128 << 10;

export const flutterDesktopBuildManifestSchema =
  "vibermate.desktop-build/v3";

export const flutterDesktopBuildConfigurationNames = Object.freeze([
  "go.mod",
  "go.sum",
  "ui/flutter_app/.metadata",
  "ui/flutter_app/pubspec.yaml",
  "ui/flutter_app/pubspec.lock",
  "ui/flutter_app/tool/flutter-sdk.env",
  "ui/flutter_app/macos/Runner.xcodeproj/project.pbxproj",
  "ui/flutter_app/macos/Runner/Configs/AppInfo.xcconfig",
  "ui/flutter_app/macos/Runner/Configs/Release.xcconfig",
  "ui/flutter_app/macos/Runner/Info.plist",
  "ui/flutter_app/macos/Runner/Release.entitlements",
]);

export const flutterDesktopNestedCode = Object.freeze({
  "app-framework":
    "Contents/Frameworks/App.framework/Versions/A/App",
  "flutter-macos-framework":
    "Contents/Frameworks/FlutterMacOS.framework/Versions/A/FlutterMacOS",
  vibermate: "Contents/MacOS/vibermate",
  vibermated: "Contents/MacOS/vibermated",
});

function exactKeys(value, expected, label) {
  if (
    value === null ||
    typeof value !== "object" ||
    Array.isArray(value) ||
    Object.getPrototypeOf(value) !== Object.prototype
  ) {
    throw new Error(`${label} must be an object`);
  }
  const actual = Object.keys(value).sort();
  const wanted = [...expected].sort();
  if (
    actual.length !== wanted.length ||
    actual.some((name, index) => name !== wanted[index])
  ) {
    throw new Error(`${label} has an unexpected shape`);
  }
}

function nonemptyString(value, label, maximumBytes = 16 << 10) {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    Buffer.byteLength(value, "utf8") > maximumBytes ||
    value.trim() !== value ||
    value.includes("\0")
  ) {
    throw new Error(`${label} is invalid`);
  }
  return value;
}

function digestMap(value, expectedNames, label) {
  exactKeys(value, expectedNames, label);
  for (const name of expectedNames) {
    if (!digestPattern.test(value[name] ?? "")) {
      throw new Error(`${label} contains an invalid digest for ${name}`);
    }
  }
}

export function validateFlutterDesktopBuildManifest(
  value,
  { expectedRevision, expectedTarget, requireClean = true } = {},
) {
  exactKeys(
    value,
    [
      "configurationSHA256",
      "nestedCodeSHA256",
      "profiles",
      "schema",
      "source",
      "toolchains",
    ],
    "Flutter Desktop build manifest",
  );
  exactKeys(
    value.source,
    ["commitTime", "dirty", "revision", "vcs"],
    "Flutter Desktop source",
  );
  exactKeys(
    value.profiles,
    ["desktop", "sidecars", "target", "toolkit"],
    "Flutter Desktop profiles",
  );
  exactKeys(
    value.toolchains,
    ["dart", "flutter", "go", "xcode"],
    "Flutter Desktop toolchains",
  );
  digestMap(
    value.configurationSHA256,
    flutterDesktopBuildConfigurationNames,
    "Flutter Desktop configuration digests",
  );
  digestMap(
    value.nestedCodeSHA256,
    Object.keys(flutterDesktopNestedCode),
    "Flutter Desktop nested-code digests",
  );
  if (
    value.schema !== flutterDesktopBuildManifestSchema ||
    value.source.vcs !== "git" ||
    !revisionPattern.test(value.source.revision ?? "") ||
    (expectedRevision !== undefined &&
      value.source.revision !== expectedRevision) ||
    typeof value.source.dirty !== "boolean" ||
    (requireClean && value.source.dirty) ||
    typeof value.source.commitTime !== "string" ||
    Number.isNaN(Date.parse(value.source.commitTime)) ||
    value.profiles.desktop !== "release" ||
    value.profiles.sidecars !== "release" ||
    value.profiles.toolkit !== "flutter" ||
    ![
      "aarch64-apple-darwin",
      "universal-apple-darwin",
      "x86_64-apple-darwin",
    ].includes(
      value.profiles.target,
    ) ||
    (expectedTarget !== undefined &&
      value.profiles.target !== expectedTarget)
  ) {
    throw new Error("Flutter Desktop build provenance is inconsistent");
  }
  for (const [name, version] of Object.entries(value.toolchains)) {
    nonemptyString(version, `Flutter Desktop ${name} toolchain`);
  }
  return Object.freeze({
    commitTime: value.source.commitTime,
    nestedCodeSHA256: Object.freeze({ ...value.nestedCodeSHA256 }),
    revision: value.source.revision,
  });
}

function run(command, arguments_, { cwd = defaultRepositoryRoot } = {}) {
  const result = spawnSync(command, arguments_, {
    cwd,
    encoding: "utf8",
    env: { ...process.env, LANG: "C", LC_ALL: "C" },
    maxBuffer: 4 << 20,
    timeout: 30_000,
  });
  if (
    result.error !== undefined ||
    result.signal !== null ||
    result.status !== 0
  ) {
    throw new Error(`Could not run ${command}`);
  }
  return (result.stdout || result.stderr).trim();
}

async function sha256File(path) {
  const metadata = await lstat(path);
  if (!metadata.isFile() || metadata.isSymbolicLink() || metadata.size <= 0) {
    throw new Error(`Build input is not a direct nonempty file: ${path}`);
  }
  const payload = await readFile(path);
  if (payload.length !== metadata.size) {
    throw new Error(`Build input changed while it was read: ${path}`);
  }
  return createHash("sha256").update(payload).digest("hex");
}

export function normalizeFlutterToolchains(
  machine,
  { expectedVersion, expectedRevision },
) {
  if (
    machine === null ||
    typeof machine !== "object" ||
    Array.isArray(machine) ||
    machine.frameworkVersion !== expectedVersion ||
    machine.frameworkRevision !== expectedRevision ||
    typeof machine.dartSdkVersion !== "string" ||
    machine.dartSdkVersion.length === 0
  ) {
    throw new Error("Flutter SDK differs from the repository authority");
  }
  return Object.freeze({
    dart: `Dart ${machine.dartSdkVersion}`,
    flutter: `Flutter ${expectedVersion} (${expectedRevision})`,
  });
}

function normalizedFlutterToolchains() {
  const source = run("flutter", ["--version", "--machine"], {
    cwd: flutterDirectory,
  });
  let machine;
  try {
    machine = JSON.parse(source);
  } catch {
    throw new Error("Flutter returned malformed machine-readable version data");
  }
  const authoritySource = run("/bin/sh", [
    "-c",
    ". ./tool/flutter-sdk.env && printf '%s\\n%s\\n' \"$VIBERMATE_FLUTTER_VERSION\" \"$VIBERMATE_FLUTTER_REVISION\"",
  ], { cwd: flutterDirectory });
  const [expectedVersion, expectedRevision, ...extra] =
    authoritySource.split(/\r?\n/u);
  if (extra.length !== 0) {
    throw new Error("Flutter SDK differs from the repository authority");
  }
  return normalizeFlutterToolchains(machine, {
    expectedVersion,
    expectedRevision,
  });
}

function canonicalCommitTime(repositoryRoot, revision) {
  const source = run(
    "git",
    ["show", "-s", "--format=%cI", revision],
    { cwd: repositoryRoot },
  );
  const parsed = new Date(source);
  if (Number.isNaN(parsed.valueOf())) {
    throw new Error("Git commit time is invalid");
  }
  return parsed.toISOString().replace(".000Z", "Z");
}

async function requireCanonicalDirectory(path, label) {
  if (!isAbsolute(path) || resolve(path) !== path) {
    throw new Error(`${label} path is not a clean absolute path`);
  }
  const metadata = await lstat(path);
  if (metadata.isSymbolicLink() || !metadata.isDirectory()) {
    throw new Error(`${label} is not a direct directory`);
  }
  if ((await realpath(path)) !== path) {
    throw new Error(`${label} path contains a symbolic directory`);
  }
}

export async function writeFlutterDesktopBuildManifest({
  appPath,
  repositoryRoot = defaultRepositoryRoot,
  requireClean = false,
  target,
}) {
  await requireCanonicalDirectory(repositoryRoot, "Repository root");
  await requireCanonicalDirectory(appPath, "Flutter App bundle");
  if (
    ![
      "aarch64-apple-darwin",
      "universal-apple-darwin",
      "x86_64-apple-darwin",
    ].includes(target)
  ) {
    throw new Error("Flutter Desktop target is invalid");
  }

  const initialStatus = run(
    "git",
    ["status", "--porcelain=v1", "--untracked-files=all"],
    { cwd: repositoryRoot },
  );
  if (requireClean && initialStatus !== "") {
    throw new Error("A distribution manifest requires a clean Git worktree");
  }
  const revision = run("git", ["rev-parse", "HEAD"], {
    cwd: repositoryRoot,
  });
  if (!revisionPattern.test(revision)) {
    throw new Error("Git did not return a full source revision");
  }

  const configurationSHA256 = {};
  for (const name of flutterDesktopBuildConfigurationNames) {
    configurationSHA256[name] = await sha256File(
      resolve(repositoryRoot, name),
    );
  }
  const nestedCodeSHA256 = {};
  for (const [name, relativePath] of Object.entries(flutterDesktopNestedCode)) {
    nestedCodeSHA256[name] = await sha256File(resolve(appPath, relativePath));
  }

  const flutterToolchains = normalizedFlutterToolchains();
  const manifest = {
    schema: flutterDesktopBuildManifestSchema,
    source: {
      vcs: "git",
      revision,
      commitTime: canonicalCommitTime(repositoryRoot, revision),
      dirty: initialStatus !== "",
    },
    profiles: {
      desktop: "release",
      sidecars: "release",
      target,
      toolkit: "flutter",
    },
    toolchains: {
      go: run("go", ["version"], { cwd: repositoryRoot }),
      flutter: flutterToolchains.flutter,
      dart: flutterToolchains.dart,
      xcode: run("/usr/bin/xcodebuild", ["-version"], {
        cwd: repositoryRoot,
      }),
    },
    configurationSHA256,
    nestedCodeSHA256,
  };
  validateFlutterDesktopBuildManifest(manifest, {
    expectedRevision: revision,
    expectedTarget: target,
    requireClean,
  });

  const finalRevision = run("git", ["rev-parse", "HEAD"], {
    cwd: repositoryRoot,
  });
  const finalStatus = run(
    "git",
    ["status", "--porcelain=v1", "--untracked-files=all"],
    { cwd: repositoryRoot },
  );
  if (finalRevision !== revision || finalStatus !== initialStatus) {
    throw new Error("Git source changed while the manifest was assembled");
  }

  const resourcesDirectory = resolve(appPath, "Contents", "Resources");
  await mkdir(resourcesDirectory, { recursive: true, mode: 0o755 });
  const finalPath = resolve(
    resourcesDirectory,
    "vibermate-build-manifest.json",
  );
  const temporaryPath = resolve(
    resourcesDirectory,
    `.vibermate-build-manifest.${randomUUID()}.tmp`,
  );
  const payload = Buffer.from(`${JSON.stringify(manifest, null, 2)}\n`, "utf8");
  if (payload.length === 0 || payload.length > maximumManifestBytes) {
    throw new Error("Flutter Desktop build manifest exceeds its size bound");
  }
  let handle;
  try {
    handle = await open(temporaryPath, "wx", 0o644);
    await handle.writeFile(payload);
    await handle.sync();
    await handle.close();
    handle = undefined;
    await chmod(temporaryPath, 0o644);
    await rename(temporaryPath, finalPath);
    const directory = await open(resourcesDirectory, "r");
    try {
      await directory.sync();
    } finally {
      await directory.close();
    }
  } finally {
    if (handle !== undefined) {
      await handle.close();
    }
    await rm(temporaryPath, { force: true });
  }
  return Object.freeze({ manifest: Object.freeze(manifest), path: finalPath });
}

function parseCLI(arguments_) {
  const values = new Map();
  for (const argument of arguments_) {
    const separator = argument.indexOf("=");
    if (separator <= 2 || !argument.startsWith("--")) {
      throw new Error("Manifest arguments must use --name=value");
    }
    const name = argument.slice(2, separator);
    if (!new Set(["app", "repository-root", "target"]).has(name) || values.has(name)) {
      throw new Error("Manifest argument is unknown or duplicated");
    }
    values.set(name, argument.slice(separator + 1));
  }
  if (!values.has("app") || !values.has("target")) {
    throw new Error("Manifest generation requires --app and --target");
  }
  return {
    appPath: resolve(values.get("app")),
    repositoryRoot: values.has("repository-root")
      ? resolve(values.get("repository-root"))
      : defaultRepositoryRoot,
    requireClean: process.env.VIBERMATE_RELEASE_REQUIRE_CLEAN === "1",
    target: values.get("target"),
  };
}

if (
  typeof process.argv[1] === "string" &&
  import.meta.url === pathToFileURL(resolve(process.argv[1])).href
) {
  const result = await writeFlutterDesktopBuildManifest(
    parseCLI(process.argv.slice(2)),
  );
  process.stdout.write(`${result.path}\n`);
}
