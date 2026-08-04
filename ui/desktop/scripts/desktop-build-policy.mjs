const supportedProfiles = new Set(["development", "release", "distribution"]);
const semanticVersion =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/u;
const fullGitRevision = /^[0-9a-f]{40}$/u;
const sha256 = /^[0-9a-f]{64}$/u;
const macOSSigningConfigurationEnvironment = [
  "CODE_SIGN_IDENTITY",
  "DEVELOPMENT_TEAM",
  "EXPANDED_CODE_SIGN_IDENTITY",
  "OTHER_CODE_SIGN_FLAGS",
  "PROVISIONING_PROFILE",
  "PROVISIONING_PROFILE_SPECIFIER",
];
const distributionTeamEnvironment = "VIBERMATE_APPLE_TEAM_ID";
const unsignedDistributionBundleEnvironment =
  "VIBERMATE_DESKTOP_UNSIGNED_DISTRIBUTION_BUNDLE";
const certificateSHA1 = /^[0-9a-fA-F]{40}$/u;
const appleTeamID = /^[A-Z0-9]{10}$/u;

export const desktopBuildManifestSchema = "vibermate.desktop-build/v2";
export const desktopDistributionSidecarTargetAliases = Object.freeze([
  "aarch64-apple-darwin",
  "x86_64-apple-darwin",
]);

const desktopBuildConfigurationNames = [
  "go.mod",
  "go.sum",
  "rust-toolchain.toml",
  "ui/desktop/package.json",
  "ui/desktop/pnpm-lock.yaml",
  "ui/desktop/src-tauri/Cargo.toml",
  "ui/desktop/src-tauri/Cargo.lock",
  "ui/desktop/src-tauri/tauri.conf.json",
];

export const desktopBundleProfileEnvironment =
  "VIBERMATE_DESKTOP_BUNDLE_PROFILE";

export const desktopDistributionTeamEnvironment =
  distributionTeamEnvironment;

export const desktopUnsignedDistributionBundleEnvironment =
  unsignedDistributionBundleEnvironment;

function requireProfile(profile, message) {
  if (!supportedProfiles.has(profile)) {
    throw new Error(message);
  }
  return profile;
}

function distributionCredentialNames(environment) {
  return Object.keys(environment).filter(
    (name) =>
      name.startsWith("APPLE_") ||
      name.startsWith("TAURI_SIGNING_") ||
      name === "API_PRIVATE_KEYS_DIR" ||
      macOSSigningConfigurationEnvironment.includes(name),
  );
}

function requireExactKeys(value, expected, label) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${label} must be an object`);
  }
  const actual = Object.keys(value).sort();
  const wanted = [...expected].sort();
  if (
    actual.length !== wanted.length ||
    actual.some((key, index) => key !== wanted[index])
  ) {
    throw new Error(`${label} has an unexpected shape`);
  }
}

function requireDigestMap(actual, expected, label) {
  requireExactKeys(actual, Object.keys(expected), label);
  for (const [name, digest] of Object.entries(expected)) {
    if (!sha256.test(digest) || actual[name] !== digest) {
      throw new Error(`${label} does not match ${name}`);
    }
  }
}

function requireStringMap(actual, expected, label) {
  requireExactKeys(actual, Object.keys(expected), label);
  for (const [name, value] of Object.entries(expected)) {
    if (
      typeof value !== "string" ||
      value.length === 0 ||
      actual[name] !== value
    ) {
      throw new Error(`${label} does not match ${name}`);
    }
  }
}

export function parseSidecarProfile(arguments_) {
  if (arguments_.length !== 1 || !arguments_[0].startsWith("--profile=")) {
    throw new Error(
      "Sidecar build requires exactly one --profile=development|release|distribution argument",
    );
  }
  const profile = arguments_[0].slice("--profile=".length);
  if (!supportedProfiles.has(profile)) {
    throw new Error(
      "Sidecar build profile must be development, release, or distribution",
    );
  }
  return profile;
}

export function bundleProfileFromEnvironment(environment) {
  return requireProfile(
    environment[desktopBundleProfileEnvironment],
    "Desktop Tauri builds must use an explicit repository-owned bundle command",
  );
}

export function desktopBundleTarget(profile) {
  requireProfile(profile, "Unknown Desktop bundle profile");
  return profile === "distribution"
    ? "universal-apple-darwin"
    : "aarch64-apple-darwin";
}

export function requireDesktopBundleTarget(profile, environment) {
  const expectedTarget = desktopBundleTarget(profile);
  const expectedArch = profile === "distribution" ? "universal" : "aarch64";
  if (
    environment.TAURI_ENV_TARGET_TRIPLE !== expectedTarget ||
    environment.TAURI_ENV_ARCH !== expectedArch ||
    environment.TAURI_ENV_PLATFORM !== "darwin" ||
    (environment.TAURI_ENV_DEBUG !== undefined &&
      environment.TAURI_ENV_DEBUG !== "false")
  ) {
    throw new Error(
      `Desktop ${profile} bundles require the ${expectedTarget} Tauri release target`,
    );
  }
}

function present(environment, name) {
  return (
    typeof environment[name] === "string" &&
    environment[name].trim().length !== 0
  );
}

export function requireBundleSigningBoundary(profile, phase, environment) {
  requireProfile(profile, "Unknown Desktop bundle profile");
  if (phase !== "build" && phase !== "bundle") {
    throw new Error("Unknown Desktop bundle credential phase");
  }
  if (
    profile !== "development" &&
    typeof environment.TAURI_SKIP_SIDECAR_SIGNATURE_CHECK === "string" &&
    environment.TAURI_SKIP_SIDECAR_SIGNATURE_CHECK.trim().length !== 0
  ) {
    throw new Error("Release Desktop bundles cannot skip sidecar signatures");
  }
  if (
    profile !== "distribution" &&
    distributionCredentialNames(environment).some((name) =>
      present(environment, name),
    )
  ) {
    throw new Error(
      "Non-distribution Desktop bundles refuse Apple signing or notarization configuration",
    );
  }
  if (profile !== "distribution") {
    return;
  }
  if (phase === "build") {
    if (
      distributionCredentialNames(environment).some((name) =>
        present(environment, name),
      ) ||
      present(environment, distributionTeamEnvironment)
    ) {
      throw new Error(
        "Unsigned distribution builds refuse Apple credentials and signing configuration",
      );
    }
    return;
  }
  const unsignedDistributionBundle =
    environment[unsignedDistributionBundleEnvironment] === "1";
  if (
    environment[unsignedDistributionBundleEnvironment] !== undefined &&
    !unsignedDistributionBundle
  ) {
    throw new Error(
      "The unsigned distribution bundle marker must be exactly 1",
    );
  }
  if (unsignedDistributionBundle) {
    if (
      distributionCredentialNames(environment).some((name) =>
        present(environment, name),
      ) ||
      present(environment, distributionTeamEnvironment)
    ) {
      throw new Error(
        "Unsigned distribution App bundling refuses Apple credentials and signing configuration",
      );
    }
    return;
  }
  if (
    !certificateSHA1.test(environment.APPLE_SIGNING_IDENTITY ?? "") ||
    !appleTeamID.test(environment[distributionTeamEnvironment] ?? "")
  ) {
    throw new Error(
      "Distribution bundling requires one certificate SHA-1 and Apple Team ID",
    );
  }
  if (
    distributionCredentialNames(environment).some(
      (name) =>
        name !== "APPLE_SIGNING_IDENTITY" && present(environment, name),
    )
  ) {
    throw new Error(
      "Distribution bundling must not receive certificate material, notarization credentials, or updater keys",
    );
  }
}

export function sidecarBuildTags(profile) {
  requireProfile(profile, "Unknown sidecar build profile");
  return profile === "development" ? [] : ["vibermate_native_secrets"];
}

export function requireReleaseSource(profile, porcelainStatus) {
  requireProfile(profile, "Unknown sidecar build profile");
  if (profile !== "development" && porcelainStatus.trim().length !== 0) {
    throw new Error("Release sidecar build requires a clean Git worktree");
  }
}

export function requireStableRevision(before, after) {
  if (!fullGitRevision.test(before) || before !== after) {
    throw new Error("Git revision changed during the Desktop sidecar build");
  }
}

function cargoPackageField(source, field) {
  let inPackage = false;
  for (const line of source.split(/\r?\n/u)) {
    if (/^\s*\[[^\]]+\]\s*$/u.test(line)) {
      if (inPackage) {
        break;
      }
      inPackage = /^\s*\[package\]\s*$/u.test(line);
      continue;
    }
    if (!inPackage) {
      continue;
    }
    const match = line.match(
      new RegExp(`^\\s*${field}\\s*=\\s*"([^"]+)"\\s*$`, "u"),
    );
    if (match !== null) {
      return match[1];
    }
  }
  throw new Error(`Cargo package ${field} is missing`);
}

export function cargoPackageVersion(source) {
  return cargoPackageField(source, "version");
}

export function cargoRustVersion(source) {
  const value = cargoPackageField(source, "rust-version");
  return /^\d+\.\d+$/u.test(value) ? `${value}.0` : value;
}

export function goModuleVersion(source) {
  const match = source.match(/^go\s+(\d+\.\d+\.\d+)\s*$/mu);
  if (match === null) {
    throw new Error("go.mod must pin an exact Go patch version");
  }
  return match[1];
}

export function pnpmPackageManagerVersion(value) {
  const match = value?.match(/^pnpm@(\d+\.\d+\.\d+)$/u);
  if (match === null || match === undefined) {
    throw new Error("packageManager must pin an exact pnpm version");
  }
  return match[1];
}

export function rustToolchainVersion(source) {
  let inToolchain = false;
  for (const line of source.split(/\r?\n/u)) {
    if (/^\s*\[[^\]]+\]\s*$/u.test(line)) {
      if (inToolchain) {
        break;
      }
      inToolchain = /^\s*\[toolchain\]\s*$/u.test(line);
      continue;
    }
    if (!inToolchain) {
      continue;
    }
    const match = line.match(
      /^\s*channel\s*=\s*"(\d+\.\d+\.\d+)"\s*$/u,
    );
    if (match !== null) {
      return match[1];
    }
  }
  throw new Error("rust-toolchain.toml must pin an exact Rust patch version");
}

export function validateDesktopVersions(profile, versions) {
  requireProfile(profile, "Unknown sidecar build profile");
  const values = [
    versions.packageVersion,
    versions.tauriVersion,
    versions.cargoVersion,
  ];
  if (values.some((value) => !semanticVersion.test(value))) {
    throw new Error("Desktop versions must be valid semantic versions");
  }
  if (!values.every((value) => value === values[0])) {
    throw new Error("package.json, Tauri, and Cargo versions must match");
  }
  if (profile !== "development" && values[0] === "0.0.0") {
    throw new Error("Release sidecar build refuses the placeholder 0.0.0 version");
  }
  return values[0];
}

export function validateReleaseToolchains(profile, versions) {
  requireProfile(profile, "Unknown Desktop bundle profile");
  if (profile === "development") {
    return;
  }
  requireExactKeys(
    versions.expected,
    ["go", "node", "rustc", "cargo", "pnpm", "tauri"],
    "Expected Desktop toolchains",
  );
  requireExactKeys(
    versions.actual,
    ["go", "node", "rustc", "cargo", "pnpm", "tauri"],
    "Actual Desktop toolchains",
  );
  for (const name of Object.keys(versions.expected)) {
    if (
      !semanticVersion.test(versions.expected[name]) ||
      versions.actual[name] !== versions.expected[name]
    ) {
      throw new Error(
        `Release Desktop ${name} toolchain must be ${versions.expected[name]}`,
      );
    }
  }
}

export function validatePreparedBundle(profile, evidence) {
  requireProfile(profile, "Unknown Desktop bundle profile");
  const { manifest } = evidence;
  requireExactKeys(
    manifest,
    [
      "schema",
      "source",
      "profiles",
      "toolchains",
      "configurationSHA256",
      "sidecarSHA256",
    ],
    "Desktop build manifest",
  );
  requireExactKeys(
    manifest.source,
    ["vcs", "revision", "commitTime", "dirty"],
    "Desktop build manifest source",
  );
  requireExactKeys(
    manifest.profiles,
    ["desktop", "sidecars", "target"],
    "Desktop build manifest profiles",
  );
  requireExactKeys(
    manifest.toolchains,
    ["go", "node", "rustc", "cargo", "pnpm", "tauri"],
    "Desktop build manifest toolchains",
  );

  requireExactKeys(
    evidence.currentConfigurationSHA256,
    desktopBuildConfigurationNames,
    "Current Desktop build configuration digests",
  );
  requireReleaseSource(profile, evidence.porcelainStatus);
  requireStableRevision(manifest.source.revision, evidence.currentRevision);
  const dirty = evidence.porcelainStatus.trim().length !== 0;
  const expectedTarget = desktopBundleTarget(profile);
  if (
    manifest.schema !== desktopBuildManifestSchema ||
    manifest.source.vcs !== "git" ||
    manifest.source.commitTime !== evidence.currentCommitTime ||
    manifest.source.dirty !== dirty
  ) {
    throw new Error("Desktop build manifest source is inconsistent");
  }
  if (
    manifest.profiles.desktop !== "release" ||
    manifest.profiles.sidecars !== profile ||
    manifest.profiles.target !== expectedTarget
  ) {
    throw new Error("Desktop build manifest profiles are inconsistent");
  }
  requireStringMap(
    manifest.toolchains,
    evidence.currentToolchains,
    "Desktop build toolchains",
  );
  requireDigestMap(
    manifest.configurationSHA256,
    evidence.currentConfigurationSHA256,
    "Desktop build configuration digests",
  );
  requireDigestMap(
    manifest.sidecarSHA256,
    evidence.currentSidecarSHA256,
    "Desktop sidecar digests",
  );
}
