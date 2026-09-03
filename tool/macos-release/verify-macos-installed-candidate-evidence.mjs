import { createHash } from "node:crypto";
import { createReadStream } from "node:fs";
import { lstat, readFile, readdir, realpath } from "node:fs/promises";
import { basename, dirname, relative, resolve, sep } from "node:path";
import { pathToFileURL } from "node:url";
import {
  macOSDistributionPolicy,
  parseClosedJSONObject,
  requireAppleTeamID,
  validateNotarizationEvidence,
  validateNotaryLog,
  validateNotarySubmitResult,
  validateSigningTransformationEvidence,
} from "./macos-distribution-policy.mjs";

const reportFilename = "signed-package-installation-report.json";
const checksumFilename = "signed-package-installation-report.sha256";
const reportSchema = "vibermate.macos-signed-package-installation/v1";
const maximumJSONBytes = 8 << 20;
const maximumDiskImageBytes = 4 * (1 << 30);
const sha256Pattern = /^[0-9a-f]{64}$/u;
const revisionPattern = /^[0-9a-f]{40}$/u;
const uuidPattern =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/iu;
const timestampPattern =
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{3})?Z$/u;
const expectedInputEntries = Object.freeze([
  ".vibermate-private",
  ".vibermate-private/notarization",
  ".vibermate-private/notarization/apple-notary-log.json",
  ".vibermate-private/notarization/apple-notary-submit.json",
  ".vibermate-private/notarization/notarization-evidence.json",
  ".vibermate-private/signing",
  ".vibermate-private/signing/signing-transformation.json",
  "bundle",
  "bundle/dmg",
  "bundle/dmg/ViberMate_0.1.0_universal.dmg",
]);
const forbiddenConfigurationNames = new Set([
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

function requireEqual(actual, expected, label) {
  if (actual !== expected) {
    throw new Error(`${label} is invalid`);
  }
}

function requireDigest(value, label) {
  if (!sha256Pattern.test(value ?? "")) {
    throw new Error(`${label} is not a SHA-256 digest`);
  }
}

function environmentValue(environment, name) {
  const value = environment[name];
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`${name} is required`);
  }
  return value;
}

function absoluteCleanPath(value, label) {
  if (!value.startsWith(sep) || resolve(value) !== value) {
    throw new Error(`${label} must be an absolute clean path`);
  }
  return value;
}

function runnerChild(environment, name, prefix) {
  const runnerTemp = absoluteCleanPath(
    environmentValue(environment, "RUNNER_TEMP"),
    "RUNNER_TEMP",
  );
  const value = absoluteCleanPath(environmentValue(environment, name), name);
  if (
    dirname(value) !== runnerTemp ||
    !basename(value).startsWith(prefix) ||
    basename(value).length <= prefix.length
  ) {
    throw new Error(`${name} is outside its admitted runner path`);
  }
  return value;
}

function rejectCredentials(environment) {
  for (const [name, value] of Object.entries(environment)) {
    if (
      typeof value === "string" &&
      value.length !== 0 &&
      (name.startsWith("APPLE_") ||
        name.startsWith("TAURI_SIGNING_") ||
        forbiddenConfigurationNames.has(name))
    ) {
      throw new Error(
        `Installed candidate evidence verifier received forbidden Apple distribution configuration: ${name}`,
      );
    }
  }
}

function requireRevision(value, label) {
  if (!revisionPattern.test(value ?? "")) {
    throw new Error(`${label} is not a full lowercase Git revision`);
  }
  return value;
}

export function validateIndependentInstalledCandidateReport(value) {
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
  requireEqual(value.schema, reportSchema, "installation report schema");
  if (
    typeof value.createdAt !== "string" ||
    !timestampPattern.test(value.createdAt) ||
    Number.isNaN(Date.parse(value.createdAt))
  ) {
    throw new Error("installation report timestamp is invalid");
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
    throw new Error("installation report candidate metadata is invalid");
  }
  const exactInstallation = {
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
    installedRelativePath: "Applications/ViberMate.app",
    source: "read-only-mounted-dmg",
    stateRemovedAfterVerification: true,
  };
  const exactLaunch = {
    gracefulExit: true,
    isolatedHome: true,
    isolatedHomeRemovedAfterVerification: true,
    launches: 2,
    navigationPersistence: true,
    readiness: "launcher-discovery-and-router-mounted",
    status: "passed",
  };
  const exactLimitations = {
    appRemoval: "runner-cleanup-only",
    cliPath: "not-installed",
    systemProxy: "not-exercised",
    systemTrust: "not-exercised",
    uninstall: "not-asserted",
    updater: "not-exercised",
  };
  for (const [name, expected] of Object.entries(exactInstallation)) {
    requireEqual(value.installation[name], expected, `installation ${name}`);
  }
  for (const [name, expected] of Object.entries(exactLaunch)) {
    requireEqual(value.launch[name], expected, `launch ${name}`);
  }
  for (const [name, expected] of Object.entries(exactLimitations)) {
    requireEqual(value.limitations[name], expected, `limitation ${name}`);
  }
  requireEqual(value.notarization.status, "Accepted", "notarization status");
  requireEqual(
    value.notarization.ticketedCodeDirectories,
    6,
    "notarization ticket count",
  );
  if (!uuidPattern.test(value.notarization.submissionID ?? "")) {
    throw new Error("notarization submission ID is invalid");
  }
  return value;
}

async function canonicalFile(path, label, maximumBytes, exactMode) {
  const metadata = await lstat(path);
  if (
    metadata.isSymbolicLink() ||
    !metadata.isFile() ||
    metadata.nlink !== 1 ||
    metadata.size === 0 ||
    metadata.size > maximumBytes ||
    (exactMode !== undefined && (metadata.mode & 0o777) !== exactMode) ||
    (await realpath(path)) !== path
  ) {
    throw new Error(`${label} is not an admitted regular file`);
  }
  return metadata;
}

async function sha256File(path) {
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(path)) {
    hash.update(chunk);
  }
  return hash.digest("hex");
}

async function readJSON(path, label) {
  await canonicalFile(path, label, maximumJSONBytes);
  const source = await readFile(path);
  return Object.freeze({
    digest: createHash("sha256").update(source).digest("hex"),
    value: parseClosedJSONObject(source.toString("utf8"), label),
  });
}

async function exactInputInventory(inputDirectory) {
  const root = await lstat(inputDirectory);
  if (
    root.isSymbolicLink() ||
    !root.isDirectory() ||
    (root.mode & 0o022) !== 0 ||
    (await realpath(inputDirectory)) !== inputDirectory
  ) {
    throw new Error("notarized candidate input is not a canonical directory");
  }
  const names = [];
  const visit = async (directory) => {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      const path = resolve(directory, entry.name);
      const name = relative(inputDirectory, path).split(sep).join("/");
      const metadata = await lstat(path);
      if (metadata.isSymbolicLink()) {
        throw new Error("notarized candidate input contains a symbolic link");
      }
      names.push(name);
      if (metadata.isDirectory()) {
        if ((metadata.mode & 0o022) !== 0) {
          throw new Error(
            "notarized candidate input contains a directory writable by peers",
          );
        }
        await visit(path);
      } else if (
        !metadata.isFile() ||
        metadata.nlink !== 1 ||
        (metadata.mode & 0o022) !== 0
      ) {
        throw new Error("notarized candidate input contains unsafe metadata");
      }
    }
  };
  await visit(inputDirectory);
  names.sort();
  const expected = [...expectedInputEntries].sort();
  if (
    names.length !== expected.length ||
    names.some((name, index) => name !== expected[index])
  ) {
    throw new Error("notarized candidate input inventory is not exact");
  }
}

async function exactOutputEvidence(outputDirectory) {
  const root = await lstat(outputDirectory);
  if (
    root.isSymbolicLink() ||
    !root.isDirectory() ||
    (root.mode & 0o777) !== 0o700 ||
    (await realpath(outputDirectory)) !== outputDirectory
  ) {
    throw new Error("installation evidence directory is not private and canonical");
  }
  const entries = await readdir(outputDirectory, { withFileTypes: true });
  const names = entries.map((entry) => entry.name).sort();
  if (
    entries.some((entry) => !entry.isFile()) ||
    JSON.stringify(names) !==
      JSON.stringify([checksumFilename, reportFilename].sort())
  ) {
    throw new Error("installation evidence output inventory is not exact");
  }
  const reportPath = resolve(outputDirectory, reportFilename);
  const checksumPath = resolve(outputDirectory, checksumFilename);
  await canonicalFile(reportPath, "installation report", maximumJSONBytes, 0o600);
  await canonicalFile(checksumPath, "installation report checksum", 256, 0o600);
  const reportSource = await readFile(reportPath);
  const reportDigest = createHash("sha256").update(reportSource).digest("hex");
  const checksumSource = await readFile(checksumPath, "utf8");
  requireEqual(
    checksumSource,
    `${reportDigest}  ${reportFilename}\n`,
    "installation report checksum",
  );
  const report = parseClosedJSONObject(
    reportSource.toString("utf8"),
    "signed-package installation report",
  );
  validateIndependentInstalledCandidateReport(report);
  return Object.freeze({ report, reportDigest });
}

async function requireAbsent(path, label) {
  try {
    await lstat(path);
  } catch (error) {
    if (error?.code === "ENOENT") {
      return;
    }
    throw error;
  }
  throw new Error(`${label} still exists after installed-candidate verification`);
}

export async function verifyMacOSInstalledCandidateEvidence(
  environment = process.env,
) {
  if (process.platform !== "darwin" || process.arch !== "arm64") {
    throw new Error("Installed macOS candidate evidence verification requires macOS arm64");
  }
  rejectCredentials(environment);
  const runnerTemp = absoluteCleanPath(
    environmentValue(environment, "RUNNER_TEMP"),
    "RUNNER_TEMP",
  );
  if ((await realpath(runnerTemp)) !== runnerTemp) {
    throw new Error("RUNNER_TEMP must be canonical");
  }
  const inputDirectory = runnerChild(
    environment,
    "VIBERMATE_NOTARIZED_DOWNLOAD_DIRECTORY",
    "vibermate-notarized-download-",
  );
  const outputDirectory = runnerChild(
    environment,
    "VIBERMATE_INSTALL_EVIDENCE_DIRECTORY",
    "vibermate-installed-evidence-",
  );
  if (inputDirectory === outputDirectory) {
    throw new Error("installation evidence input and output must be distinct");
  }
  const transientDirectories = [
    [
      runnerChild(
        environment,
        "VIBERMATE_INSTALL_MOUNT_DIRECTORY",
        "vibermate-install-mount-",
      ),
      "installation mount",
    ],
    [
      runnerChild(
        environment,
        "VIBERMATE_INSTALL_ROOT",
        "vibermate-install-root-",
      ),
      "isolated install root",
    ],
    [
      runnerChild(
        environment,
        "VIBERMATE_INSTALL_HOME",
        "vibermate-install-home-",
      ),
      "isolated installed-App home",
    ],
    [
      runnerChild(
        environment,
        "VIBERMATE_INSTALL_STATE_DIRECTORY",
        "vibermate-install-state-",
      ),
      "installed-App smoke state",
    ],
  ];
  const sourceRevision = requireRevision(
    environmentValue(environment, "VIBERMATE_CANDIDATE_REVISION"),
    "candidate revision",
  );
  const toolingRevision = requireRevision(
    environmentValue(environment, "VIBERMATE_TOOLING_REVISION"),
    "tooling revision",
  );
  const expectedTeamID = requireAppleTeamID(
    environmentValue(environment, "VIBERMATE_APPLE_TEAM_ID"),
  );
  await Promise.all(
    [
      ...transientDirectories,
      ...transientDirectories.slice(1).map(([path, label]) => [
        `${path}.cleanup`,
        `${label} cleanup tombstone`,
      ]),
      [`${outputDirectory}.staging`, "installation evidence staging"],
    ].map(([path, label]) => requireAbsent(path, label)),
  );
  await exactInputInventory(inputDirectory);
  const output = await exactOutputEvidence(outputDirectory);
  const signingPath = resolve(
    inputDirectory,
    ".vibermate-private/signing/signing-transformation.json",
  );
  const notarizationPath = resolve(
    inputDirectory,
    ".vibermate-private/notarization/notarization-evidence.json",
  );
  const submitPath = resolve(
    inputDirectory,
    ".vibermate-private/notarization/apple-notary-submit.json",
  );
  const logPath = resolve(
    inputDirectory,
    ".vibermate-private/notarization/apple-notary-log.json",
  );
  const diskImagePath = resolve(
    inputDirectory,
    "bundle/dmg/ViberMate_0.1.0_universal.dmg",
  );
  const [signing, notarization, submit, log] = await Promise.all([
    readJSON(signingPath, "signing transformation evidence"),
    readJSON(notarizationPath, "notarization evidence"),
    readJSON(submitPath, "notary submit result"),
    readJSON(logPath, "notary developer log"),
  ]);
  validateSigningTransformationEvidence(signing.value);
  validateNotarizationEvidence(notarization.value);
  const submission = validateNotarySubmitResult(submit.value);
  validateNotaryLog(log.value, {
    archiveFilename: macOSDistributionPolicy.diskImageFilename,
    preStapleSHA256: notarization.value.candidate.preStapleSHA256,
    submissionID: submission.id,
  });
  await canonicalFile(
    diskImagePath,
    "stapled disk image",
    maximumDiskImageBytes,
  );
  const diskImageSHA256 = await sha256File(diskImagePath);
  const report = output.report;
  if (
    report.candidate.sourceRevision !== sourceRevision ||
    report.candidate.toolingRevision !== toolingRevision ||
    report.candidate.teamIdentifier !== expectedTeamID ||
    report.candidate.diskImageSHA256 !== diskImageSHA256 ||
    report.candidate.diskImageSHA256 !==
      notarization.value.candidate.finalSHA256 ||
    report.candidate.applicationTreeSHA256 !==
      notarization.value.candidate.applicationTreeSHA256 ||
    report.candidate.buildManifestSHA256 !==
      notarization.value.candidate.buildManifestSHA256 ||
    report.candidate.certificateSHA256 !==
      notarization.value.codeSigning.certificateSHA256 ||
    report.candidate.signingEvidenceSHA256 !== signing.digest ||
    report.candidate.notarizationEvidenceSHA256 !== notarization.digest ||
    report.notarization.submissionID.toLowerCase() !== submission.id ||
    notarization.value.candidate.sourceRevision !== sourceRevision ||
    notarization.value.candidate.toolingRevision !== toolingRevision ||
    notarization.value.codeSigning.teamIdentifier !== expectedTeamID ||
    notarization.value.notarization.submissionID.toLowerCase() !==
      submission.id ||
    notarization.value.notarization.submitSHA256 !== submit.digest ||
    notarization.value.notarization.logSHA256 !== log.digest ||
    notarization.value.candidate.signingEvidenceSHA256 !== signing.digest ||
    signing.value.candidate.diskImageSHA256 !==
      notarization.value.candidate.preStapleSHA256 ||
    signing.value.candidate.signedApplicationTreeSHA256 !==
      report.candidate.applicationTreeSHA256 ||
    signing.value.candidate.buildManifestSHA256 !==
      report.candidate.buildManifestSHA256 ||
    signing.value.codeSigning.certificateSHA256 !==
      report.candidate.certificateSHA256 ||
    signing.value.codeSigning.teamIdentifier !== expectedTeamID ||
    signing.value.candidate.sourceRevision !== sourceRevision ||
    signing.value.candidate.toolingRevision !== toolingRevision
  ) {
    throw new Error(
      "The installation report is not bound to the exact notarized candidate",
    );
  }
  return output;
}

function isMainModule() {
  return (
    typeof process.argv[1] === "string" &&
    import.meta.url === pathToFileURL(resolve(process.argv[1])).href
  );
}

if (isMainModule()) {
  if (process.argv.length !== 2) {
    throw new Error(
      "The installed macOS candidate evidence verifier accepts no arguments",
    );
  }
  await verifyMacOSInstalledCandidateEvidence();
  process.stdout.write("Installed macOS candidate evidence verified.\n");
}
