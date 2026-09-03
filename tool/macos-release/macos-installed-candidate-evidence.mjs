import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { Buffer } from "node:buffer";
import {
  chmod,
  lstat,
  mkdir,
  open,
  readFile,
  readdir,
  readlink,
  realpath,
  rename,
  rm,
  rmdir,
} from "node:fs/promises";
import { basename, dirname, relative, resolve, sep } from "node:path";
import {
  macOSDistributionPolicy,
  parseClosedJSONObject,
  requireAppleTeamID,
  validateGatekeeperAssessment,
  validateMountedDMGTopLevel,
  validateNotarizationEvidence,
  validateNotaryLog,
  validateNotarySubmitResult,
  validateSigningTransformationEvidence,
  validateTreeLedgerEquality,
} from "./macos-distribution-policy.mjs";
import {
  appleToolchainEvidence,
  applicationTreeLedger,
  inspectSignedMacOSApplicationAtPath,
  inspectSignedMacOSDiskImageAtPath,
  sha256File,
} from "./verify-macos-signed-candidate.mjs";

const maximumCommandOutput = 1 << 20;
const maximumEvidenceBytes = 8 << 20;
const maximumDiskImageBytes = 4 * (1 << 30);
const commandTimeoutMilliseconds = 10 * 60 * 1000;
const smokeTimeoutMilliseconds = 4 * 60 * 1000;
const sha256Pattern = /^[0-9a-f]{64}$/u;
const revisionPattern = /^[0-9a-f]{40}$/u;
const uuidPattern =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/iu;
const timestampPattern =
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{3})?Z$/u;

export const macOSInstalledEvidencePolicy = Object.freeze({
  checksumFilename: "signed-package-installation-report.sha256",
  installedApplicationRelativePath: "Applications/ViberMate.app",
  reportFilename: "signed-package-installation-report.json",
  schema: "vibermate.macos-signed-package-installation/v1",
  smokeFilename: "desktop-smoke.json",
  smokeSchema: "vibermate.macos-installed-desktop-smoke/v1",
});

const exactArtifactEntries = Object.freeze([
  ".vibermate-private",
  ".vibermate-private/notarization",
  ".vibermate-private/notarization/apple-notary-log.json",
  ".vibermate-private/notarization/apple-notary-submit.json",
  ".vibermate-private/notarization/notarization-evidence.json",
  ".vibermate-private/signing",
  ".vibermate-private/signing/signing-transformation.json",
  "bundle",
  "bundle/dmg",
  `bundle/dmg/${macOSDistributionPolicy.diskImageFilename}`,
]);

const forbiddenAppleDistributionNames = new Set([
  "API_PRIVATE_KEYS_DIR",
  "CODE_SIGN_IDENTITY",
  "CODESIGN_ALLOCATE",
  "DEVELOPMENT_TEAM",
  "EXPANDED_CODE_SIGN_IDENTITY",
  "OTHER_CODE_SIGN_FLAGS",
  "PROVISIONING_PROFILE",
  "PROVISIONING_PROFILE_SPECIFIER",
  "SIGNING_KEYCHAIN_PATH",
]);

function requireExactKeys(value, expected, label) {
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
    actual.some((key, index) => key !== wanted[index])
  ) {
    throw new Error(`${label} has an unexpected shape`);
  }
}

function requireFixedValue(actual, expected, label) {
  if (actual !== expected) {
    throw new Error(`${label} is invalid`);
  }
}

function requireDigest(value, label) {
  if (!sha256Pattern.test(value ?? "")) {
    throw new Error(`${label} is not a SHA-256 digest`);
  }
  return value;
}

function requireRevision(value, label) {
  if (!revisionPattern.test(value ?? "")) {
    throw new Error(`${label} is not a full lowercase Git revision`);
  }
  return value;
}

function requireAbsoluteCleanPath(value, label) {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    !value.startsWith(sep) ||
    resolve(value) !== value
  ) {
    throw new Error(`${label} must be an absolute clean path`);
  }
  return value;
}

function directRunnerChild(runnerTemp, path, prefix) {
  return (
    dirname(path) === runnerTemp &&
    basename(path).startsWith(prefix) &&
    basename(path).length > prefix.length
  );
}

function requireRunnerChild(runnerTemp, path, prefix, label) {
  requireAbsoluteCleanPath(path, label);
  if (!directRunnerChild(runnerTemp, path, prefix)) {
    throw new Error(`${label} is outside its admitted runner path`);
  }
  return path;
}

function requireEnvironmentValue(environment, name) {
  const value = environment[name];
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`${name} is required`);
  }
  return value;
}

export function rejectAppleDistributionCredentials(environment) {
  for (const [name, value] of Object.entries(environment)) {
    if (
      typeof value === "string" &&
      value.length !== 0 &&
      (name.startsWith("APPLE_") ||
        name.startsWith("TAURI_SIGNING_") ||
        forbiddenAppleDistributionNames.has(name))
    ) {
      throw new Error(
        `Installed candidate verification received forbidden Apple distribution configuration: ${name}`,
      );
    }
  }
}

export function installedCandidatePathsFromEnvironment(environment) {
  const runnerTemp = requireAbsoluteCleanPath(
    requireEnvironmentValue(environment, "RUNNER_TEMP"),
    "RUNNER_TEMP",
  );
  const inputDirectory = requireRunnerChild(
    runnerTemp,
    requireEnvironmentValue(
      environment,
      "VIBERMATE_NOTARIZED_DOWNLOAD_DIRECTORY",
    ),
    "vibermate-notarized-download-",
    "notarized candidate download",
  );
  const installRoot = requireRunnerChild(
    runnerTemp,
    requireEnvironmentValue(environment, "VIBERMATE_INSTALL_ROOT"),
    "vibermate-install-root-",
    "isolated install root",
  );
  const mountDirectory = requireRunnerChild(
    runnerTemp,
    requireEnvironmentValue(
      environment,
      "VIBERMATE_INSTALL_MOUNT_DIRECTORY",
    ),
    "vibermate-install-mount-",
    "read-only installation mount",
  );
  const homeDirectory = requireRunnerChild(
    runnerTemp,
    requireEnvironmentValue(environment, "VIBERMATE_INSTALL_HOME"),
    "vibermate-install-home-",
    "isolated installed-App home",
  );
  const stateDirectory = requireRunnerChild(
    runnerTemp,
    requireEnvironmentValue(
      environment,
      "VIBERMATE_INSTALL_STATE_DIRECTORY",
    ),
    "vibermate-install-state-",
    "installed-App smoke state",
  );
  const outputDirectory = requireRunnerChild(
    runnerTemp,
    requireEnvironmentValue(
      environment,
      "VIBERMATE_INSTALL_EVIDENCE_DIRECTORY",
    ),
    "vibermate-installed-evidence-",
    "installed candidate evidence",
  );
  const smokeBinary = requireRunnerChild(
    runnerTemp,
    requireEnvironmentValue(environment, "VIBERMATE_INSTALL_SMOKE_BINARY"),
    "vibermate-installed-smoke-",
    "trusted installed-App smoke binary",
  );
  const outputStagingDirectory = `${outputDirectory}.staging`;
  const uniquePaths = new Set([
    inputDirectory,
    installRoot,
    mountDirectory,
    homeDirectory,
    stateDirectory,
    outputDirectory,
    outputStagingDirectory,
    smokeBinary,
  ]);
  if (uniquePaths.size !== 8) {
    throw new Error("Installed candidate paths must be distinct");
  }
  return Object.freeze({
    diskImagePath: resolve(
      inputDirectory,
      "bundle",
      "dmg",
      macOSDistributionPolicy.diskImageFilename,
    ),
    homeDirectory,
    inputDirectory,
    installRoot,
    installedAppPath: resolve(
      installRoot,
      macOSInstalledEvidencePolicy.installedApplicationRelativePath,
    ),
    mountDirectory,
    notaryEvidencePath: resolve(
      inputDirectory,
      ".vibermate-private",
      "notarization",
      macOSDistributionPolicy.notaryEvidenceFilename,
    ),
    notaryLogPath: resolve(
      inputDirectory,
      ".vibermate-private",
      "notarization",
      macOSDistributionPolicy.notaryLogFilename,
    ),
    notarySubmitPath: resolve(
      inputDirectory,
      ".vibermate-private",
      "notarization",
      macOSDistributionPolicy.notarySubmitFilename,
    ),
    outputDirectory,
    outputStagingDirectory,
    reportPath: resolve(
      outputDirectory,
      macOSInstalledEvidencePolicy.reportFilename,
    ),
    runnerTemp,
    signingEvidencePath: resolve(
      inputDirectory,
      ".vibermate-private",
      "signing",
      macOSDistributionPolicy.signingEvidenceFilename,
    ),
    smokeBinary,
    smokeReportPath: resolve(
      stateDirectory,
      macOSInstalledEvidencePolicy.smokeFilename,
    ),
    stateDirectory,
  });
}

export function readOnlyAttachArguments(diskImagePath, mountDirectory) {
  return Object.freeze([
    "attach",
    diskImagePath,
    "-readonly",
    "-nobrowse",
    "-noautoopen",
    "-mountpoint",
    mountDirectory,
    "-plist",
  ]);
}

export function exactApplicationCopyArguments(source, destination) {
  return Object.freeze([
    "--norsrc",
    "--noextattr",
    "--noacl",
    "--noqtn",
    "-X",
    source,
    destination,
  ]);
}

function sanitizedToolEnvironment(environment) {
  const admittedNames = [
    "CI",
    "ImageOS",
    "ImageVersion",
    "LOGNAME",
    "RUNNER_ARCH",
    "RUNNER_OS",
    "RUNNER_TEMP",
    "SHELL",
    "TERM",
    "USER",
    "XPC_FLAGS",
    "XPC_SERVICE_NAME",
    "__CF_USER_TEXT_ENCODING",
  ];
  const result = {
    DEVELOPER_DIR: macOSDistributionPolicy.developerDirectory,
    HOME: environment.RUNNER_TEMP,
    LANG: "C",
    LC_ALL: "C",
    PATH: "/usr/bin:/bin:/usr/sbin:/sbin",
    TMPDIR: environment.RUNNER_TEMP,
  };
  for (const name of admittedNames) {
    if (typeof environment[name] === "string" && environment[name] !== "") {
      result[name] = environment[name];
    }
  }
  return result;
}

export function installedSmokeEnvironment(environment, isolatedHome) {
  const loginHome = requireAbsoluteCleanPath(
    requireEnvironmentValue(environment, "HOME"),
    "login HOME",
  );
  requireAbsoluteCleanPath(isolatedHome, "isolated App home");
  if (
    loginHome === isolatedHome ||
    loginHome.startsWith(`${isolatedHome}${sep}`)
  ) {
    throw new Error("login HOME must remain outside the isolated App home");
  }
  return Object.freeze({
    ...sanitizedToolEnvironment(environment),
    HOME: loginHome,
    TMPDIR: resolve(isolatedHome, "tmp"),
  });
}

function runTool(command, arguments_, label, options = {}) {
  const result = spawnSync(command, arguments_, {
    cwd: options.cwd,
    encoding: "utf8",
    env: options.environment,
    input: options.input,
    maxBuffer: maximumCommandOutput,
    timeout: options.timeoutMilliseconds ?? commandTimeoutMilliseconds,
  });
  if (result.error !== undefined || result.signal !== null || result.status !== 0) {
    throw new Error(`${label} failed`);
  }
  return Object.freeze({
    stderr: result.stderr ?? "",
    stdout: result.stdout ?? "",
  });
}

function commandOutput(result) {
  return `${result.stdout}\n${result.stderr}`.trim();
}

async function requireCanonicalObject(path, type, label) {
  const metadata = await lstat(path);
  if (
    metadata.isSymbolicLink() ||
    (type === "directory" && !metadata.isDirectory()) ||
    (type === "file" && !metadata.isFile()) ||
    (await realpath(path)) !== path
  ) {
    throw new Error(`${label} is not a canonical ${type}`);
  }
  return metadata;
}

async function inspectExactDownloadedArtifact(paths) {
  const root = await requireCanonicalObject(
    paths.inputDirectory,
    "directory",
    "notarized candidate download",
  );
  if ((root.mode & 0o022) !== 0) {
    throw new Error("The notarized candidate download directory is writable by peers");
  }
  const actualEntries = [];
  const visit = async (directory) => {
    const entries = await readdir(directory, { withFileTypes: true });
    for (const entry of entries) {
      const path = resolve(directory, entry.name);
      const name = relative(paths.inputDirectory, path).split(sep).join("/");
      const metadata = await lstat(path);
      if (metadata.isSymbolicLink()) {
        throw new Error("The notarized candidate artifact contains a symbolic link");
      }
      actualEntries.push(name);
      if (metadata.isDirectory()) {
        if ((metadata.mode & 0o022) !== 0) {
          throw new Error(
            "The notarized candidate artifact has a directory writable by peers",
          );
        }
        await visit(path);
      } else if (metadata.isFile()) {
        if (metadata.nlink !== 1 || (metadata.mode & 0o022) !== 0) {
          throw new Error("The notarized candidate artifact has unsafe file metadata");
        }
        const limit = name.endsWith(".dmg")
          ? maximumDiskImageBytes
          : maximumEvidenceBytes;
        if (metadata.size === 0 || metadata.size > limit) {
          throw new Error("The notarized candidate artifact has an invalid file size");
        }
      } else {
        throw new Error("The notarized candidate artifact contains a special file");
      }
    }
  };
  await visit(paths.inputDirectory);
  actualEntries.sort();
  const expectedEntries = [...exactArtifactEntries].sort();
  if (
    actualEntries.length !== expectedEntries.length ||
    actualEntries.some((entry, index) => entry !== expectedEntries[index])
  ) {
    throw new Error("The notarized candidate artifact inventory is not exact");
  }
}

async function readBoundedJSON(path, label) {
  const metadata = await requireCanonicalObject(path, "file", label);
  if (
    metadata.size === 0 ||
    metadata.size > maximumEvidenceBytes ||
    metadata.nlink !== 1
  ) {
    throw new Error(`${label} is not a bounded regular file`);
  }
  const source = await readFile(path);
  return Object.freeze({
    digest: createHash("sha256").update(source).digest("hex"),
    source,
    value: parseClosedJSONObject(source.toString("utf8"), label),
  });
}

async function readAndValidateProducerEvidence(paths, revisions, expectedTeamID) {
  const [signing, notarization, submit, log] = await Promise.all([
    readBoundedJSON(paths.signingEvidencePath, "signing transformation evidence"),
    readBoundedJSON(paths.notaryEvidencePath, "notarization evidence"),
    readBoundedJSON(paths.notarySubmitPath, "notary submit result"),
    readBoundedJSON(paths.notaryLogPath, "notary developer log"),
  ]);
  validateSigningTransformationEvidence(signing.value);
  validateNotarizationEvidence(notarization.value);
  const submission = validateNotarySubmitResult(submit.value);
  validateNotaryLog(log.value, {
    archiveFilename: macOSDistributionPolicy.diskImageFilename,
    preStapleSHA256: notarization.value.candidate.preStapleSHA256,
    submissionID: submission.id,
  });
  if (
    notarization.value.candidate.sourceRevision !== revisions.candidateRevision ||
    notarization.value.candidate.toolingRevision !== revisions.toolingRevision ||
    notarization.value.codeSigning.teamIdentifier !== expectedTeamID ||
    notarization.value.candidate.signingEvidenceSHA256 !== signing.digest ||
    notarization.value.notarization.submitSHA256 !== submit.digest ||
    notarization.value.notarization.logSHA256 !== log.digest ||
    notarization.value.notarization.submissionID.toLowerCase() !== submission.id ||
    signing.value.candidate.sourceRevision !== revisions.candidateRevision ||
    signing.value.candidate.toolingRevision !== revisions.toolingRevision ||
    signing.value.codeSigning.teamIdentifier !== expectedTeamID ||
    signing.value.codeSigning.certificateSHA256 !==
      notarization.value.codeSigning.certificateSHA256 ||
    signing.value.candidate.signedApplicationTreeSHA256 !==
      notarization.value.candidate.applicationTreeSHA256 ||
    signing.value.candidate.buildManifestSHA256 !==
      notarization.value.candidate.buildManifestSHA256 ||
    signing.value.candidate.diskImageSHA256 !==
      notarization.value.candidate.preStapleSHA256
  ) {
    throw new Error("The notarization evidence chain is inconsistent");
  }
  return Object.freeze({ log, notarization, signing, submission, submit });
}

async function mountedTopLevel(directory) {
  const result = [];
  for (const name of await readdir(directory)) {
    const path = resolve(directory, name);
    const metadata = await lstat(path);
    if (metadata.isSymbolicLink()) {
      result.push({ name, target: await readlink(path), type: "symlink" });
    } else if (metadata.isDirectory()) {
      result.push({ name, type: "directory" });
    } else if (metadata.isFile()) {
      result.push({ name, type: "file" });
    } else {
      throw new Error("The mounted disk image contains a special top-level entry");
    }
  }
  return result;
}

function assessWithGatekeeper(path, type, expectedTeamID, environment) {
  const arguments_ = ["--assess", "--type", type];
  if (type === "open") {
    arguments_.push("--context", "context:primary-signature");
  }
  arguments_.push("--verbose=4", path);
  const result = runTool(
    "/usr/sbin/spctl",
    arguments_,
    `${type} Gatekeeper assessment`,
    { environment },
  );
  validateGatekeeperAssessment(
    commandOutput(result),
    expectedTeamID,
    `${type} Gatekeeper assessment`,
  );
}

export function validateMountedDiskImageReadOnlyFlags(value) {
  requireExactKeys(
    value,
    ["Writable", "WritableMedia", "WritableVolume"],
    "mounted disk-image write flags",
  );
  for (const [name, flag] of Object.entries(value)) {
    requireFixedValue(flag, "false", `mounted disk-image ${name}`);
  }
  return value;
}

function requireReadOnlyMount(mountDirectory, environment) {
  const information = runTool(
    "/usr/sbin/diskutil",
    ["info", "-plist", mountDirectory],
    "mounted disk-image inspection",
    { environment },
  );
  validateMountedDiskImageReadOnlyFlags(
    Object.fromEntries(
      ["Writable", "WritableMedia", "WritableVolume"].map((name) => [
        name,
        runTool(
          "/usr/bin/plutil",
          ["-extract", name, "raw", "-o", "-", "-"],
          `mounted disk-image ${name} inspection`,
          { environment, input: information.stdout },
        ).stdout.trim(),
      ]),
    ),
  );
}

function compareApplicationToNotarization(application, evidence, revisions) {
  const notarization = evidence.notarization.value;
  if (
    application.applicationTreeSHA256 !==
      notarization.candidate.applicationTreeSHA256 ||
    application.manifestSHA256 !== notarization.candidate.buildManifestSHA256 ||
    application.sourceRevision !== revisions.candidateRevision ||
    application.teamIdentifier !== notarization.codeSigning.teamIdentifier ||
    application.certificateSHA256 !==
      notarization.codeSigning.certificateSHA256
  ) {
    throw new Error("The installed application does not match notarization evidence");
  }
}

async function createPrivateDirectory(path, label) {
  await mkdir(path, { mode: 0o700 });
  try {
    await chmod(path, 0o700);
    const metadata = await lstat(path);
    if (
      !metadata.isDirectory() ||
      metadata.isSymbolicLink() ||
      (metadata.mode & 0o777) !== 0o700
    ) {
      throw new Error(`${label} is not a private directory`);
    }
    return Object.freeze({ dev: metadata.dev, ino: metadata.ino });
  } catch (error) {
    await rm(path, { force: true, recursive: true });
    throw error;
  }
}

async function requireTrustedSmokeBinary(path) {
  const metadata = await requireCanonicalObject(
    path,
    "file",
    "trusted installed-App smoke binary",
  );
  if (
    metadata.nlink !== 1 ||
    (metadata.mode & 0o111) === 0 ||
    (metadata.mode & 0o022) !== 0
  ) {
    throw new Error("The trusted installed-App smoke binary metadata is unsafe");
  }
}

export function validateDesktopSmokeEvidence(value) {
  requireExactKeys(
    value,
    [
      "gracefulExit",
      "isolatedHome",
      "launches",
      "navigationPersistence",
      "readiness",
      "schema",
      "status",
    ],
    "installed Desktop smoke evidence",
  );
  requireFixedValue(
    value.schema,
    macOSInstalledEvidencePolicy.smokeSchema,
    "installed Desktop smoke schema",
  );
  requireFixedValue(value.status, "passed", "installed Desktop smoke status");
  requireFixedValue(value.launches, 2, "installed Desktop smoke launch count");
  requireFixedValue(
    value.readiness,
    "launcher-discovery-and-router-mounted",
    "installed Desktop smoke readiness",
  );
  for (const name of [
    "navigationPersistence",
    "gracefulExit",
    "isolatedHome",
  ]) {
    requireFixedValue(value[name], true, `installed Desktop smoke ${name}`);
  }
  return value;
}

async function runInstalledSmoke(paths, environment) {
  const result = runTool(
    paths.smokeBinary,
    [
      "installed-smoke",
      "--desktop-app",
      paths.installedAppPath,
      "--home",
      paths.homeDirectory,
      "--report",
      paths.smokeReportPath,
      "--timeout",
      "2m",
    ],
    "installed Desktop launch and readiness smoke",
    {
      environment,
      timeoutMilliseconds: smokeTimeoutMilliseconds,
    },
  );
  requireFixedValue(
    result.stdout,
    "Installed macOS Desktop smoke passed\n",
    "installed Desktop smoke output",
  );
  requireFixedValue(result.stderr, "", "installed Desktop smoke error output");
  const smoke = await readBoundedJSON(
    paths.smokeReportPath,
    "installed Desktop smoke evidence",
  );
  validateDesktopSmokeEvidence(smoke.value);
  return smoke.value;
}

async function removeCreatedDirectory(path, identity, runnerTemp, label) {
  const metadata = await lstat(path);
  if (
    metadata.isSymbolicLink() ||
    !metadata.isDirectory() ||
    metadata.dev !== identity.dev ||
    metadata.ino !== identity.ino ||
    dirname(path) !== runnerTemp
  ) {
    throw new Error(`${label} identity changed before cleanup`);
  }
  const tombstone = `${path}.cleanup`;
  if (dirname(tombstone) !== runnerTemp) {
    throw new Error(`${label} cleanup tombstone is outside RUNNER_TEMP`);
  }
  try {
    await lstat(tombstone);
    throw new Error(`${label} cleanup tombstone already exists`);
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
  await rename(path, tombstone);
  const renamed = await lstat(tombstone);
  if (renamed.dev !== identity.dev || renamed.ino !== identity.ino) {
    throw new Error(`${label} identity changed during cleanup`);
  }
  await rm(tombstone, { force: false, recursive: true });
}

function makeReport(evidence, smoke, diskImage, revisions, expectedTeamID) {
  const notarization = evidence.notarization.value;
  return {
    schema: macOSInstalledEvidencePolicy.schema,
    createdAt: new Date().toISOString(),
    candidate: {
      applicationTreeSHA256: notarization.candidate.applicationTreeSHA256,
      architectures: [...macOSDistributionPolicy.architectures],
      buildManifestSHA256: notarization.candidate.buildManifestSHA256,
      bundleIdentifier: macOSDistributionPolicy.appIdentifier,
      certificateSHA256: notarization.codeSigning.certificateSHA256,
      diskImageFilename: macOSDistributionPolicy.diskImageFilename,
      diskImageSHA256: diskImage.diskImageSHA256,
      minimumSystemVersion: macOSDistributionPolicy.minimumSystemVersion,
      notarizationEvidenceSHA256: evidence.notarization.digest,
      signingEvidenceSHA256: evidence.signing.digest,
      sourceRevision: revisions.candidateRevision,
      teamIdentifier: expectedTeamID,
      toolingRevision: revisions.toolingRevision,
      version: macOSDistributionPolicy.appVersion,
    },
    installation: {
      appRemovedAfterVerification: true,
      bundleInventoryVerified: true,
      codeSignatureVerified: true,
      diskImageGatekeeperAssessment: "accepted-notarized-developer-id",
      diskImageReadOnly: true,
      exactTreeCopy: true,
      installRootRemovedAfterVerification: true,
      installShape: "isolated-runner-applications",
      installedApplicationGatekeeperAssessment:
        "accepted-notarized-developer-id",
      installedRelativePath:
        macOSInstalledEvidencePolicy.installedApplicationRelativePath,
      source: "read-only-mounted-dmg",
      stateRemovedAfterVerification: true,
    },
    launch: {
      gracefulExit: smoke.gracefulExit,
      isolatedHome: smoke.isolatedHome,
      isolatedHomeRemovedAfterVerification: true,
      launches: smoke.launches,
      navigationPersistence: smoke.navigationPersistence,
      readiness: smoke.readiness,
      status: smoke.status,
    },
    notarization: {
      status: notarization.notarization.status,
      submissionID: notarization.notarization.submissionID,
      ticketedCodeDirectories:
        notarization.notarization.ticketedCodeDirectories,
    },
    limitations: {
      appRemoval: "runner-cleanup-only",
      cliPath: "not-installed",
      systemProxy: "not-exercised",
      systemTrust: "not-exercised",
      uninstall: "not-asserted",
      updater: "not-exercised",
    },
  };
}

export function validateInstalledCandidateReport(value, expected = {}) {
  requireExactKeys(
    value,
    [
      "candidate",
      "createdAt",
      "installation",
      "launch",
      "limitations",
      "notarization",
      "schema",
    ],
    "signed-package installation report",
  );
  requireExactKeys(
    value.candidate,
    [
      "applicationTreeSHA256",
      "architectures",
      "buildManifestSHA256",
      "bundleIdentifier",
      "certificateSHA256",
      "diskImageFilename",
      "diskImageSHA256",
      "minimumSystemVersion",
      "notarizationEvidenceSHA256",
      "signingEvidenceSHA256",
      "sourceRevision",
      "teamIdentifier",
      "toolingRevision",
      "version",
    ],
    "signed-package installation candidate",
  );
  requireExactKeys(
    value.installation,
    [
      "appRemovedAfterVerification",
      "bundleInventoryVerified",
      "codeSignatureVerified",
      "diskImageGatekeeperAssessment",
      "diskImageReadOnly",
      "exactTreeCopy",
      "installRootRemovedAfterVerification",
      "installShape",
      "installedApplicationGatekeeperAssessment",
      "installedRelativePath",
      "source",
      "stateRemovedAfterVerification",
    ],
    "signed-package installation result",
  );
  requireExactKeys(
    value.launch,
    [
      "gracefulExit",
      "isolatedHome",
      "isolatedHomeRemovedAfterVerification",
      "launches",
      "navigationPersistence",
      "readiness",
      "status",
    ],
    "signed-package installed launch result",
  );
  requireExactKeys(
    value.notarization,
    ["status", "submissionID", "ticketedCodeDirectories"],
    "signed-package notarization result",
  );
  requireExactKeys(
    value.limitations,
    ["appRemoval", "cliPath", "systemProxy", "systemTrust", "uninstall", "updater"],
    "signed-package installation limitations",
  );
  requireFixedValue(
    value.schema,
    macOSInstalledEvidencePolicy.schema,
    "signed-package installation schema",
  );
  if (
    typeof value.createdAt !== "string" ||
    !timestampPattern.test(value.createdAt) ||
    Number.isNaN(Date.parse(value.createdAt))
  ) {
    throw new Error("signed-package installation timestamp is invalid");
  }
  for (const name of [
    "applicationTreeSHA256",
    "buildManifestSHA256",
    "certificateSHA256",
    "diskImageSHA256",
    "notarizationEvidenceSHA256",
    "signingEvidenceSHA256",
  ]) {
    requireDigest(value.candidate[name], `candidate ${name}`);
  }
  requireRevision(value.candidate.sourceRevision, "candidate source revision");
  requireRevision(value.candidate.toolingRevision, "candidate tooling revision");
  requireAppleTeamID(value.candidate.teamIdentifier);
  if (
    JSON.stringify(value.candidate.architectures) !==
      JSON.stringify(macOSDistributionPolicy.architectures) ||
    value.candidate.bundleIdentifier !== macOSDistributionPolicy.appIdentifier ||
    value.candidate.version !== macOSDistributionPolicy.appVersion ||
    value.candidate.minimumSystemVersion !==
      macOSDistributionPolicy.minimumSystemVersion ||
    value.candidate.diskImageFilename !==
      macOSDistributionPolicy.diskImageFilename
  ) {
    throw new Error("signed-package installation candidate metadata is invalid");
  }
  const fixedInstallationValues = {
    appRemovedAfterVerification: true,
    bundleInventoryVerified: true,
    codeSignatureVerified: true,
    diskImageGatekeeperAssessment: "accepted-notarized-developer-id",
    diskImageReadOnly: true,
    exactTreeCopy: true,
    installRootRemovedAfterVerification: true,
    installShape: "isolated-runner-applications",
    installedApplicationGatekeeperAssessment:
      "accepted-notarized-developer-id",
    installedRelativePath:
      macOSInstalledEvidencePolicy.installedApplicationRelativePath,
    source: "read-only-mounted-dmg",
    stateRemovedAfterVerification: true,
  };
  for (const [name, expectedValue] of Object.entries(fixedInstallationValues)) {
    requireFixedValue(
      value.installation[name],
      expectedValue,
      `installation ${name}`,
    );
  }
  const fixedLaunchValues = {
    gracefulExit: true,
    isolatedHome: true,
    isolatedHomeRemovedAfterVerification: true,
    launches: 2,
    navigationPersistence: true,
    readiness: "launcher-discovery-and-router-mounted",
    status: "passed",
  };
  for (const [name, expectedValue] of Object.entries(fixedLaunchValues)) {
    requireFixedValue(value.launch[name], expectedValue, `launch ${name}`);
  }
  requireFixedValue(
    value.notarization.status,
    "Accepted",
    "notarization status",
  );
  if (!uuidPattern.test(value.notarization.submissionID ?? "")) {
    throw new Error("notarization submission ID is invalid");
  }
  requireFixedValue(
    value.notarization.ticketedCodeDirectories,
    macOSDistributionPolicy.notaryTicketedCodeDirectoryCount,
    "notarization ticket count",
  );
  const fixedLimitations = {
    appRemoval: "runner-cleanup-only",
    cliPath: "not-installed",
    systemProxy: "not-exercised",
    systemTrust: "not-exercised",
    uninstall: "not-asserted",
    updater: "not-exercised",
  };
  for (const [name, expectedValue] of Object.entries(fixedLimitations)) {
    requireFixedValue(value.limitations[name], expectedValue, `limitation ${name}`);
  }
  for (const [name, expectedValue] of Object.entries(expected)) {
    if (expectedValue !== undefined) {
      requireFixedValue(value.candidate[name], expectedValue, `candidate ${name}`);
    }
  }
  return value;
}

async function writePrivateFile(path, source) {
  const handle = await open(path, "wx", 0o600);
  try {
    await handle.writeFile(source);
    await handle.sync();
  } finally {
    await handle.close();
  }
}

async function writeClosedEvidence(paths, report) {
  validateInstalledCandidateReport(report);
  const reportSource = `${JSON.stringify(report, null, 2)}\n`;
  if (Buffer.byteLength(reportSource, "utf8") > maximumEvidenceBytes) {
    throw new Error("signed-package installation report is oversized");
  }
  const reportDigest = createHash("sha256").update(reportSource).digest("hex");
  const staging = paths.outputStagingDirectory;
  try {
    await mkdir(staging, { mode: 0o700 });
    await chmod(staging, 0o700);
    await writePrivateFile(
      resolve(staging, macOSInstalledEvidencePolicy.reportFilename),
      reportSource,
    );
    await writePrivateFile(
      resolve(staging, macOSInstalledEvidencePolicy.checksumFilename),
      `${reportDigest}  ${macOSInstalledEvidencePolicy.reportFilename}\n`,
    );
    const directory = await open(staging, "r");
    try {
      await directory.sync();
    } finally {
      await directory.close();
    }
    await rename(staging, paths.outputDirectory);
  } catch (error) {
    await rm(staging, { force: true, recursive: true });
    throw error;
  }
  return Object.freeze({ reportDigest, reportPath: paths.reportPath });
}

function releaseRevisions(environment) {
  return Object.freeze({
    candidateRevision: requireRevision(
      requireEnvironmentValue(environment, "VIBERMATE_CANDIDATE_REVISION"),
      "candidate revision",
    ),
    toolingRevision: requireRevision(
      requireEnvironmentValue(environment, "VIBERMATE_TOOLING_REVISION"),
      "tooling revision",
    ),
  });
}

export async function createMacOSInstalledCandidateEvidence(
  environment = process.env,
) {
  if (process.platform !== "darwin" || process.arch !== "arm64") {
    throw new Error("Installed macOS candidate evidence requires macOS arm64");
  }
  rejectAppleDistributionCredentials(environment);
  const revisions = releaseRevisions(environment);
  const expectedTeamID = requireAppleTeamID(
    requireEnvironmentValue(environment, "VIBERMATE_APPLE_TEAM_ID"),
  );
  const paths = installedCandidatePathsFromEnvironment(environment);
  if ((await realpath(paths.runnerTemp)) !== paths.runnerTemp) {
    throw new Error("RUNNER_TEMP must be canonical");
  }
  await inspectExactDownloadedArtifact(paths);
  await requireTrustedSmokeBinary(paths.smokeBinary);
  for (const path of [
    paths.mountDirectory,
    paths.installRoot,
    paths.homeDirectory,
    paths.stateDirectory,
    `${paths.installRoot}.cleanup`,
    `${paths.homeDirectory}.cleanup`,
    `${paths.stateDirectory}.cleanup`,
    paths.outputDirectory,
    paths.outputStagingDirectory,
  ]) {
    try {
      await lstat(path);
    } catch (error) {
      if (error?.code === "ENOENT") {
        continue;
      }
      throw error;
    }
    throw new Error("An installed candidate output path already exists");
  }

  const evidence = await readAndValidateProducerEvidence(
    paths,
    revisions,
    expectedTeamID,
  );
  const diskImage = await inspectSignedMacOSDiskImageAtPath(
    paths.diskImagePath,
    expectedTeamID,
  );
  if (
    diskImage.diskImageSHA256 !==
      evidence.notarization.value.candidate.finalSHA256 ||
    diskImage.certificateSHA256 !==
      evidence.notarization.value.codeSigning.certificateSHA256 ||
    diskImage.dmgFilename !== macOSDistributionPolicy.diskImageFilename
  ) {
    throw new Error("The stapled disk image does not match notarization evidence");
  }

  const environmentForTools = sanitizedToolEnvironment(environment);
  assessWithGatekeeper(
    paths.diskImagePath,
    "open",
    expectedTeamID,
    environmentForTools,
  );
  const toolchain = await appleToolchainEvidence();
  const mountIdentity = await createPrivateDirectory(
    paths.mountDirectory,
    "read-only installation mount",
  );
  let mountAttached = false;
  let installIdentity;
  let homeIdentity;
  let stateIdentity;
  let smoke;
  let operationError;
  try {
    runTool(
      "/usr/bin/hdiutil",
      readOnlyAttachArguments(paths.diskImagePath, paths.mountDirectory),
      "read-only disk-image attachment",
      { environment: environmentForTools },
    );
    mountAttached = true;
    requireReadOnlyMount(paths.mountDirectory, environmentForTools);
    validateMountedDMGTopLevel(await mountedTopLevel(paths.mountDirectory));
    const mountedAppPath = resolve(
      paths.mountDirectory,
      macOSDistributionPolicy.appBundleName,
    );
    const mountedApplication = await inspectSignedMacOSApplicationAtPath(
      mountedAppPath,
      expectedTeamID,
      { expectedRevision: revisions.candidateRevision, toolchain },
    );
    compareApplicationToNotarization(
      mountedApplication,
      evidence,
      revisions,
    );

    installIdentity = await createPrivateDirectory(
      paths.installRoot,
      "isolated install root",
    );
    const applicationsDirectory = resolve(paths.installRoot, "Applications");
    const stagingRoot = resolve(paths.installRoot, ".staging");
    const stagingApplications = resolve(stagingRoot, "Applications");
    await mkdir(applicationsDirectory, { mode: 0o700 });
    await mkdir(stagingRoot, { mode: 0o700 });
    await mkdir(stagingApplications, { mode: 0o700 });
    const stagingAppPath = resolve(
      stagingApplications,
      macOSDistributionPolicy.appBundleName,
    );
    runTool(
      "/usr/bin/ditto",
      exactApplicationCopyArguments(mountedAppPath, stagingAppPath),
      "exact application copy from mounted disk image",
      { environment: environmentForTools },
    );
    const stagedApplication = await inspectSignedMacOSApplicationAtPath(
      stagingAppPath,
      expectedTeamID,
      { expectedRevision: revisions.candidateRevision, toolchain },
    );
    compareApplicationToNotarization(stagedApplication, evidence, revisions);
    validateTreeLedgerEquality(
      mountedApplication.applicationLedger,
      stagedApplication.applicationLedger,
    );

    runTool(
      "/usr/bin/hdiutil",
      ["detach", paths.mountDirectory],
      "read-only disk-image detachment",
      { environment: environmentForTools },
    );
    mountAttached = false;
    await rmdir(paths.mountDirectory);
    await rename(stagingAppPath, paths.installedAppPath);
    await rmdir(stagingApplications);
    await rmdir(stagingRoot);

    const installedApplication = await inspectSignedMacOSApplicationAtPath(
      paths.installedAppPath,
      expectedTeamID,
      { expectedRevision: revisions.candidateRevision, toolchain },
    );
    compareApplicationToNotarization(installedApplication, evidence, revisions);
    validateTreeLedgerEquality(
      mountedApplication.applicationLedger,
      installedApplication.applicationLedger,
    );
    assessWithGatekeeper(
      paths.installedAppPath,
      "execute",
      expectedTeamID,
      environmentForTools,
    );

    homeIdentity = await createPrivateDirectory(
      paths.homeDirectory,
      "isolated installed-App home",
    );
    await mkdir(resolve(paths.homeDirectory, "tmp"), { mode: 0o700 });
    stateIdentity = await createPrivateDirectory(
      paths.stateDirectory,
      "installed-App smoke state",
    );
    smoke = await runInstalledSmoke(
      paths,
      installedSmokeEnvironment(environment, paths.homeDirectory),
    );
    const afterLaunch = await inspectSignedMacOSApplicationAtPath(
      paths.installedAppPath,
      expectedTeamID,
      { expectedRevision: revisions.candidateRevision, toolchain },
    );
    compareApplicationToNotarization(afterLaunch, evidence, revisions);
    validateTreeLedgerEquality(
      installedApplication.applicationLedger,
      afterLaunch.applicationLedger,
    );
    validateTreeLedgerEquality(
      installedApplication.applicationLedger,
      await applicationTreeLedger(paths.installedAppPath),
    );
  } catch (error) {
    operationError = error;
  }

  const cleanupErrors = [];
  if (mountAttached) {
    try {
      runTool(
        "/usr/bin/hdiutil",
        ["detach", paths.mountDirectory],
        "read-only disk-image cleanup detachment",
        { environment: environmentForTools },
      );
      mountAttached = false;
    } catch (error) {
      cleanupErrors.push(error);
    }
  }
  if (!mountAttached) {
    try {
      const metadata = await lstat(paths.mountDirectory);
      if (
        metadata.isSymbolicLink() ||
        !metadata.isDirectory() ||
        metadata.dev !== mountIdentity.dev ||
        metadata.ino !== mountIdentity.ino
      ) {
        throw new Error("read-only installation mount identity changed");
      }
      await rmdir(paths.mountDirectory);
    } catch (error) {
      if (error?.code !== "ENOENT") {
        cleanupErrors.push(error);
      }
    }
  }
  for (const [path, identity, label] of [
    [paths.stateDirectory, stateIdentity, "installed-App smoke state"],
    [paths.homeDirectory, homeIdentity, "isolated installed-App home"],
    [paths.installRoot, installIdentity, "isolated install root"],
  ]) {
    if (identity !== undefined) {
      try {
        await removeCreatedDirectory(path, identity, paths.runnerTemp, label);
      } catch (error) {
        cleanupErrors.push(error);
      }
    }
  }
  const failures = [operationError, ...cleanupErrors].filter(Boolean);
  if (failures.length > 1) {
    throw new AggregateError(
      failures,
      "Installed candidate verification or cleanup failed more than once",
    );
  }
  if (failures.length === 1) {
    throw failures[0];
  }
  if (smoke === undefined) {
    throw new Error("Installed Desktop smoke did not produce evidence");
  }
  const report = makeReport(
    evidence,
    smoke,
    diskImage,
    revisions,
    expectedTeamID,
  );
  validateInstalledCandidateReport(report, {
    applicationTreeSHA256:
      evidence.notarization.value.candidate.applicationTreeSHA256,
    diskImageSHA256: diskImage.diskImageSHA256,
    notarizationEvidenceSHA256: evidence.notarization.digest,
    signingEvidenceSHA256: evidence.signing.digest,
    sourceRevision: revisions.candidateRevision,
    teamIdentifier: expectedTeamID,
    toolingRevision: revisions.toolingRevision,
  });
  return writeClosedEvidence(paths, report);
}
