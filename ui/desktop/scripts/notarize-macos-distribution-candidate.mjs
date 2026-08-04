import { createHash, randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import {
  chmod,
  link,
  lstat,
  mkdir,
  mkdtemp,
  open,
  readFile,
  realpath,
  rm,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, relative, resolve, sep } from "node:path";
import {
  extractSubmissionIDForLog,
  macOSDistributionPolicy,
  notarizationCredentialsFromEnvironment,
  parseClosedJSONObject,
  requireCertificateSHA256,
  requireUnchangedDigest,
  validateGatekeeperAssessment,
  validateNotaryLog,
  validateNotarizationEvidence,
  validateNotarySubmitResult,
  validateStapleResults,
} from "./macos-distribution-policy.mjs";
import {
  appleToolchainEvidence,
  macOSDistributionDirectories,
  sha256File,
  verifySignedMacOSDistributionCandidate,
} from "./verify-macos-signed-candidate.mjs";

const maximumCommandOutput = 8 << 20;
const notaryWaitMilliseconds = 2 * 60 * 60 * 1000;
const notaryProcessTimeoutMilliseconds = notaryWaitMilliseconds + 5 * 60 * 1000;
const localValidationTimeoutMilliseconds = 10 * 60 * 1000;
const maximumAPIKeyBytes = 64 << 10;

function isolatedToolEnvironment() {
  return {
    DEVELOPER_DIR: macOSDistributionPolicy.developerDirectory,
    HOME: process.env.HOME,
    LANG: "C",
    LC_ALL: "C",
    PATH: "/usr/bin:/bin:/usr/sbin:/sbin",
    TMPDIR: process.env.TMPDIR ?? tmpdir(),
  };
}

function runCaptured(command, arguments_, timeoutMilliseconds) {
  return spawnSync(command, arguments_, {
    cwd: macOSDistributionDirectories.repositoryDirectory,
    encoding: "utf8",
    env: isolatedToolEnvironment(),
    maxBuffer: maximumCommandOutput,
    timeout: timeoutMilliseconds,
  });
}

function requireSuccessfulCommand(result, label) {
  if (result.error !== undefined || result.signal !== null || result.status !== 0) {
    throw new Error(`${label} failed`);
  }
  return `${result.stdout ?? ""}\n${result.stderr ?? ""}`;
}

async function requirePrivateAPIKey(credentials) {
  const metadata = await lstat(credentials.keyPath);
  if (
    metadata.isSymbolicLink() ||
    !metadata.isFile() ||
    metadata.size === 0 ||
    metadata.size > maximumAPIKeyBytes ||
    (metadata.mode & 0o077) !== 0
  ) {
    throw new Error("The App Store Connect API private key must be a private regular file");
  }
  if ((await realpath(credentials.keyPath)) !== credentials.keyPath) {
    throw new Error("The App Store Connect API private key path must not contain symlinks");
  }
  const source = await readFile(credentials.keyPath, "utf8");
  if (
    !source.startsWith("-----BEGIN PRIVATE KEY-----\n") ||
    !source.trimEnd().endsWith("-----END PRIVATE KEY-----")
  ) {
    throw new Error("The App Store Connect API private key is not PEM data");
  }
}

function notaryAuthenticationArguments(credentials) {
  return [
    "--key",
    credentials.keyPath,
    "--key-id",
    credentials.keyID,
    "--issuer",
    credentials.issuerID,
  ];
}

async function fetchNotaryExchange(candidate, credentials) {
  const submitResult = runCaptured(
    "/usr/bin/xcrun",
    [
      "notarytool",
      "submit",
      candidate.dmgPath,
      ...notaryAuthenticationArguments(credentials),
      "--wait",
      "--timeout",
      "2h",
      "--output-format",
      "json",
      "--no-progress",
    ],
    notaryProcessTimeoutMilliseconds,
  );
  const submitSource = submitResult.stdout ?? "";
  let submitValue;
  let submitDecodeError;
  try {
    submitValue = parseClosedJSONObject(submitSource, "notarytool submit result");
  } catch (error) {
    submitDecodeError = error;
  }
  let submissionID;
  let submissionIDError;
  try {
    submissionID = extractSubmissionIDForLog(submitValue);
  } catch (error) {
    submissionIDError = error;
  }

  let logResult;
  let logSource;
  const temporaryDirectory = await mkdtemp(resolve(tmpdir(), "vibermate-notary-log-"));
  try {
    if (submissionID !== undefined) {
      const path = resolve(temporaryDirectory, "apple-notary-log.json");
      logResult = runCaptured(
        "/usr/bin/xcrun",
        [
          "notarytool",
          "log",
          submissionID,
          path,
          ...notaryAuthenticationArguments(credentials),
        ],
        localValidationTimeoutMilliseconds,
      );
      try {
        const metadata = await lstat(path);
        if (
          metadata.isSymbolicLink() ||
          !metadata.isFile() ||
          metadata.size === 0 ||
          metadata.size > maximumCommandOutput
        ) {
          throw new Error("The downloaded Apple notary log is invalid");
        }
        logSource = await readFile(path);
      } catch (error) {
        if (logResult.status === 0) {
          throw error;
        }
      }
    }
  } finally {
    await rm(temporaryDirectory, { recursive: true, force: true });
  }
  return Object.freeze({
    logResult,
    logSource,
    submissionID,
    submissionIDError,
    submitDecodeError,
    submitResult,
    submitSource,
    submitValue,
  });
}

function validateNotaryExchange(exchange, expected) {
  requireSuccessfulCommand(exchange.submitResult, "Apple notary submission command");
  if (exchange.submitDecodeError !== undefined) {
    throw exchange.submitDecodeError;
  }
  if (exchange.submissionIDError !== undefined) {
    throw exchange.submissionIDError;
  }
  const submission = validateNotarySubmitResult(exchange.submitValue);
  if (exchange.logResult === undefined || exchange.logSource === undefined) {
    throw new Error("Apple notarization did not yield a developer log");
  }
  requireSuccessfulCommand(exchange.logResult, "Apple notary log download");
  const value = parseClosedJSONObject(
    exchange.logSource.toString("utf8"),
    "Apple notary log",
  );
  const verifiedLog = validateNotaryLog(value, {
    ...expected,
    submissionID: submission.id,
  });
  return Object.freeze({ submission, verifiedLog });
}

function stapleAndValidateDiskImage(dmgPath) {
  const staple = runCaptured(
    "/usr/bin/xcrun",
    ["stapler", "staple", "-v", dmgPath],
    localValidationTimeoutMilliseconds,
  );
  const validate = runCaptured(
    "/usr/bin/xcrun",
    ["stapler", "validate", "-v", dmgPath],
    localValidationTimeoutMilliseconds,
  );
  validateStapleResults(staple, validate);
}

function assessWithGatekeeper(path, type, expectedTeamID, label) {
  const arguments_ = ["--assess", "--type", type];
  if (type === "open") {
    arguments_.push("--context", "context:primary-signature");
  }
  arguments_.push("--verbose=4", path);
  const result = runCaptured(
    "/usr/sbin/spctl",
    arguments_,
    localValidationTimeoutMilliseconds,
  );
  validateGatekeeperAssessment(requireSuccessfulCommand(result, label), expectedTeamID, label);
}

async function readToolVersions() {
  const base = await appleToolchainEvidence();
  const output = (arguments_, label) =>
    requireSuccessfulCommand(
      runCaptured("/usr/bin/xcrun", arguments_, localValidationTimeoutMilliseconds),
      label,
    ).trim();
  const staplerPath = output(["--find", "stapler"], "stapler path inspection");
  const notarytoolPath = output(
    ["--find", "notarytool"],
    "notarytool path inspection",
  );
  for (const [name, path] of [
    ["notarytool", notarytoolPath],
    ["stapler", staplerPath],
  ]) {
    if (
      (await realpath(path)) !== path ||
      !path.startsWith(`${macOSDistributionPolicy.developerDirectory}/`)
    ) {
      throw new Error(`The selected ${name} path is not canonical and admitted`);
    }
  }
  return Object.freeze({
    ...base.evidence,
    notarytool: output(["notarytool", "--version"], "notarytool version inspection"),
    notarytoolPath,
    notarytoolSHA256: await sha256File(notarytoolPath),
    stapler: staplerPath,
    staplerSHA256: await sha256File(staplerPath),
  });
}

function repositoryRelativePath(path) {
  return relative(macOSDistributionDirectories.repositoryDirectory, path)
    .split(sep)
    .join("/");
}

async function writePrivateArtifact(filename, content) {
  const directory = macOSDistributionDirectories.evidenceDirectory;
  await mkdir(directory, { recursive: true, mode: 0o700 });
  const metadata = await lstat(directory);
  if (
    metadata.isSymbolicLink() ||
    !metadata.isDirectory() ||
    (await realpath(directory)) !== directory
  ) {
    throw new Error("The private evidence directory is unsafe");
  }
  await chmod(directory, 0o700);
  if (basename(filename) !== filename || filename.includes("\0")) {
    throw new Error("The private evidence filename is invalid");
  }
  const finalPath = resolve(directory, filename);
  try {
    await lstat(finalPath);
    throw new Error("Notarization evidence already exists");
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
  const temporaryPath = resolve(directory, `.${randomUUID()}.tmp`);
  let handle;
  try {
    handle = await open(temporaryPath, "wx", 0o600);
    await handle.writeFile(content);
    await handle.sync();
    await handle.close();
    handle = undefined;
    await link(temporaryPath, finalPath);
    await chmod(finalPath, 0o600);
    const directoryHandle = await open(directory, "r");
    try {
      await directoryHandle.sync();
    } finally {
      await directoryHandle.close();
    }
  } finally {
    if (handle !== undefined) {
      await handle.close();
    }
    await rm(temporaryPath, { force: true });
  }
  return finalPath;
}

async function main() {
  if (process.argv.length !== 2) {
    throw new Error("The macOS notarization candidate command accepts no arguments");
  }
  if (process.platform !== "darwin") {
    throw new Error("macOS distribution notarization requires macOS");
  }
  const credentials = notarizationCredentialsFromEnvironment(process.env);
  await requirePrivateAPIKey(credentials);
  const candidate = await verifySignedMacOSDistributionCandidate(credentials.teamID);
  const preStapleSHA256 = candidate.diskImageSHA256;

  let exchange;
  try {
    exchange = await fetchNotaryExchange(candidate, credentials);
  } finally {
    await rm(credentials.keyPath);
  }
  const submitPath = await writePrivateArtifact(
    macOSDistributionPolicy.notarySubmitFilename,
    exchange.submitSource,
  );
  let logPath;
  if (exchange.logSource !== undefined) {
    logPath = await writePrivateArtifact(
      macOSDistributionPolicy.notaryLogFilename,
      exchange.logSource,
    );
  }
  const notarization = validateNotaryExchange(exchange, {
    archiveFilename: candidate.dmgFilename,
    preStapleSHA256,
  });
  if (logPath === undefined) {
    throw new Error("The fixed Apple developer log was not persisted");
  }
  requireUnchangedDigest(
    preStapleSHA256,
    await sha256File(candidate.dmgPath),
    "The submitted DMG",
  );
  stapleAndValidateDiskImage(candidate.dmgPath);
  const finalCandidate = await verifySignedMacOSDistributionCandidate(
    credentials.teamID,
    { expectedPreStapleSHA256: preStapleSHA256 },
  );
  if (
    finalCandidate.appPath !== candidate.appPath ||
    finalCandidate.dmgPath !== candidate.dmgPath ||
    finalCandidate.applicationTreeSHA256 !== candidate.applicationTreeSHA256 ||
    finalCandidate.certificateSHA256 !== candidate.certificateSHA256 ||
    finalCandidate.manifestSHA256 !== candidate.manifestSHA256 ||
    finalCandidate.signingEvidenceSHA256 !== candidate.signingEvidenceSHA256 ||
    finalCandidate.sourceRevision !== candidate.sourceRevision ||
    finalCandidate.toolingRevision !== candidate.toolingRevision ||
    finalCandidate.teamIdentifier !== candidate.teamIdentifier ||
    finalCandidate.unsignedArchiveSHA256 !== candidate.unsignedArchiveSHA256
  ) {
    throw new Error("The signed candidate changed while it was notarized");
  }
  assessWithGatekeeper(
    finalCandidate.appPath,
    "execute",
    credentials.teamID,
    "VibeMate.app Gatekeeper assessment",
  );
  assessWithGatekeeper(
    finalCandidate.dmgPath,
    "open",
    credentials.teamID,
    "Distribution DMG Gatekeeper assessment",
  );
  const finalSHA256 = await sha256File(finalCandidate.dmgPath);
  requireCertificateSHA256(finalCandidate.certificateSHA256);
  const evidence = {
    schema: macOSDistributionPolicy.evidenceSchema,
    createdAt: new Date().toISOString(),
    candidate: {
      app: repositoryRelativePath(finalCandidate.appPath),
      applicationTreeSHA256: finalCandidate.applicationTreeSHA256,
      architectures: [...finalCandidate.architectures],
      buildManifestSHA256: finalCandidate.manifestSHA256,
      bundleIdentifier: macOSDistributionPolicy.appIdentifier,
      diskImage: repositoryRelativePath(finalCandidate.dmgPath),
      finalSHA256,
      minimumSystemVersion: macOSDistributionPolicy.minimumSystemVersion,
      preStapleSHA256,
      signingEvidenceSHA256: finalCandidate.signingEvidenceSHA256,
      sourceCommitTime: finalCandidate.sourceCommitTime,
      sourceRevision: finalCandidate.sourceRevision,
      toolingRevision: finalCandidate.toolingRevision,
      unsignedArchiveSHA256: finalCandidate.unsignedArchiveSHA256,
      version: macOSDistributionPolicy.appVersion,
    },
    codeSigning: {
      certificateSHA256: finalCandidate.certificateSHA256,
      teamIdentifier: finalCandidate.teamIdentifier,
    },
    notarization: {
      developerLogFile: basename(logPath),
      developerSubmitFile: basename(submitPath),
      deliveryArtifact: "diskImage",
      diskImageStapled: true,
      logFormatVersion: notarization.verifiedLog.logFormatVersion,
      logSHA256: await sha256File(logPath),
      outsideApplicationStapled: false,
      status: notarization.verifiedLog.status,
      statusCode: notarization.verifiedLog.statusCode,
      submissionID: notarization.submission.id,
      submitSHA256: await sha256File(submitPath),
      ticketedCodeDirectories: 6,
    },
    tools: await readToolVersions(),
  };
  validateNotarizationEvidence(evidence);
  await writePrivateArtifact(
    macOSDistributionPolicy.notaryEvidenceFilename,
    `${JSON.stringify(evidence, null, 2)}\n`,
  );
  process.stdout.write("Notarized DMG and fixed private evidence verified.\n");
}

await main();
