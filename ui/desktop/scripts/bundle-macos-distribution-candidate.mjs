import { randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import {
  chmod,
  link,
  lstat,
  mkdir,
  mkdtemp,
  open,
  readdir,
  realpath,
  rm,
  symlink,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, relative, resolve, sep } from "node:path";
import { pathToFileURL } from "node:url";
import {
  macOSDistributionPolicy,
  releaseRevisionsFromEnvironment,
  requireAppleTeamID,
  validateSigningTransformationEvidence,
  validateTreeLedgerEquality,
} from "./macos-distribution-policy.mjs";
import {
  applicationTreeLedger,
  inspectSignedMacOSDistributionCandidateCore,
  inspectUnsignedMacOSDistributionCandidate,
  macOSDistributionDirectories,
  verifySignedMacOSDistributionCandidate,
} from "./verify-macos-signed-candidate.mjs";

const sha1Pattern = /^[0-9a-f]{40}$/u;
const sha256Pattern = /^[0-9a-f]{64}$/u;
const maximumCommandOutput = 8 << 20;
const localTimeoutMilliseconds = 20 * 60 * 1000;
const credentialNames = new Set([
  "API_PRIVATE_KEYS_DIR",
  "APPLE_API_ISSUER",
  "APPLE_API_KEY",
  "APPLE_API_KEY_PATH",
  "APPLE_API_PRIVATE_KEY",
  "APPLE_CERTIFICATE",
  "APPLE_CERTIFICATE_PASSWORD",
  "APPLE_ID",
  "APPLE_PASSWORD",
  "CODE_SIGN_IDENTITY",
  "CODESIGN_ALLOCATE",
  "DEVELOPMENT_TEAM",
  "EXPANDED_CODE_SIGN_IDENTITY",
  "OTHER_CODE_SIGN_FLAGS",
  "PROVISIONING_PROFILE",
  "PROVISIONING_PROFILE_SPECIFIER",
]);

function signingEnvironment() {
  return {
    DEVELOPER_DIR: macOSDistributionPolicy.developerDirectory,
    HOME: process.env.HOME,
    LANG: "C",
    LC_ALL: "C",
    PATH: "/usr/bin:/bin:/usr/sbin:/sbin",
    TMPDIR: process.env.TMPDIR ?? tmpdir(),
  };
}

function runTool(command, arguments_, label) {
  const result = spawnSync(command, arguments_, {
    cwd: macOSDistributionDirectories.repositoryDirectory,
    encoding: "utf8",
    env: signingEnvironment(),
    maxBuffer: maximumCommandOutput,
    timeout: localTimeoutMilliseconds,
  });
  if (result.error !== undefined || result.signal !== null || result.status !== 0) {
    throw new Error(`${label} failed`);
  }
}

function rejectAmbientCredentials(environment) {
  for (const [name, value] of Object.entries(environment)) {
    if (
      typeof value === "string" &&
      value.trim() !== "" &&
      (credentialNames.has(name) ||
        name.startsWith("DYLD_") ||
        name.startsWith("TAURI_SIGNING_") ||
        (name.startsWith("APPLE_") && name !== "APPLE_SIGNING_IDENTITY"))
    ) {
      throw new Error(`The trusted signer refuses ambient variable ${name}`);
    }
  }
}

async function signingInputs() {
  rejectAmbientCredentials(process.env);
  const revisions = releaseRevisionsFromEnvironment(process.env);
  const identity = process.env.APPLE_SIGNING_IDENTITY?.trim();
  const keychainPath = process.env.SIGNING_KEYCHAIN_PATH?.trim();
  const runnerTemp = process.env.RUNNER_TEMP?.trim();
  const teamID = requireAppleTeamID(process.env.VIBERMATE_APPLE_TEAM_ID?.trim());
  const unsignedArchiveSHA256 = process.env.VIBERMATE_UNSIGNED_ARCHIVE_SHA256?.trim();
  const unsignedApplicationTreeSHA256 =
    process.env.VIBERMATE_UNSIGNED_APPLICATION_TREE_SHA256?.trim();
  if (!sha1Pattern.test(identity ?? "")) {
    throw new Error("APPLE_SIGNING_IDENTITY must be one lowercase SHA-1 selector");
  }
  if (!sha256Pattern.test(unsignedArchiveSHA256 ?? "")) {
    throw new Error("VIBERMATE_UNSIGNED_ARCHIVE_SHA256 is invalid");
  }
  if (!sha256Pattern.test(unsignedApplicationTreeSHA256 ?? "")) {
    throw new Error("VIBERMATE_UNSIGNED_APPLICATION_TREE_SHA256 is invalid");
  }
  if (
    typeof keychainPath !== "string" ||
    typeof runnerTemp !== "string" ||
    (await realpath(runnerTemp)) !== runnerTemp ||
    !keychainPath.startsWith(`${runnerTemp}/`) ||
    resolve(keychainPath) !== keychainPath ||
    (await realpath(keychainPath)) !== keychainPath
  ) {
    throw new Error("SIGNING_KEYCHAIN_PATH is not a canonical runner-temporary path");
  }
  const metadata = await lstat(keychainPath);
  if (metadata.isSymbolicLink() || !metadata.isFile()) {
    throw new Error("SIGNING_KEYCHAIN_PATH is not a regular keychain file");
  }
  return Object.freeze({
    identity,
    keychainPath,
    revisions,
    teamID,
    unsignedApplicationTreeSHA256,
    unsignedArchiveSHA256,
  });
}

export function signingCommandArguments(kind, identity, keychainPath, path) {
  const common = [
    "--force",
    "--sign",
    identity,
    "--keychain",
    keychainPath,
    "--timestamp",
  ];
  if (kind === "application") {
    return [...common, "--options", "runtime", path];
  }
  if (kind === "diskImage") {
    return [
      ...common,
      "--identifier",
      macOSDistributionPolicy.diskImageIdentifier,
      path,
    ];
  }
  if (
    !["vibermate", "vibermated"].includes(kind) ||
    macOSDistributionPolicy.executableIdentifiers[kind] === undefined
  ) {
    throw new Error("The requested nested signing object is not admitted");
  }
  return [
    ...common,
    "--options",
    "runtime",
    "--identifier",
    macOSDistributionPolicy.executableIdentifiers[kind],
    path,
  ];
}

export function diskImageCreationArguments(stagingDirectory, dmgPath) {
  return [
    "create",
    "-srcfolder",
    stagingDirectory,
    "-volname",
    macOSDistributionPolicy.volumeName,
    "-fs",
    "HFS+",
    "-layout",
    "GPTSPUD",
    "-format",
    "UDZO",
    "-imagekey",
    "zlib-level=9",
    "-nospotlight",
    dmgPath,
  ];
}

function repositoryRelativePath(path) {
  return relative(macOSDistributionDirectories.repositoryDirectory, path)
    .split(sep)
    .join("/");
}

async function prepareDiskImage(appPath, dmgPath) {
  const temporaryDirectory = await mkdtemp(resolve(tmpdir(), "vibermate-dmg-source-"));
  const stagingDirectory = resolve(temporaryDirectory, "root");
  try {
    await mkdir(stagingDirectory, { mode: 0o700 });
    const stagedAppPath = resolve(stagingDirectory, macOSDistributionPolicy.appBundleName);
    runTool(
      "/usr/bin/ditto",
      [
        "--norsrc",
        "--noextattr",
        "--noacl",
        "--noqtn",
        "-X",
        appPath,
        stagedAppPath,
      ],
      "Signed application staging",
    );
    await symlink("/Applications", resolve(stagingDirectory, "Applications"));
    const names = (await readdir(stagingDirectory)).sort();
    if (JSON.stringify(names) !== JSON.stringify(["Applications", "VibeMate.app"])) {
      throw new Error("The DMG staging root is not the fixed minimal inventory");
    }
    validateTreeLedgerEquality(
      await applicationTreeLedger(appPath),
      await applicationTreeLedger(stagedAppPath),
    );
    runTool(
      "/usr/bin/hdiutil",
      diskImageCreationArguments(stagingDirectory, dmgPath),
      "Read-only disk-image creation",
    );
  } finally {
    await rm(temporaryDirectory, { recursive: true, force: true });
  }
}

async function writeSigningEvidence(evidence) {
  const finalPath = macOSDistributionDirectories.signingEvidencePath;
  const directory = dirname(finalPath);
  await mkdir(directory, { recursive: true, mode: 0o700 });
  if ((await realpath(directory)) !== directory) {
    throw new Error("The signing evidence directory is not canonical");
  }
  await chmod(directory, 0o700);
  try {
    await lstat(finalPath);
    throw new Error("The signing evidence already exists");
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
  const temporaryPath = resolve(directory, `.${randomUUID()}.tmp`);
  let handle;
  try {
    handle = await open(temporaryPath, "wx", 0o600);
    await handle.writeFile(`${JSON.stringify(evidence, null, 2)}\n`);
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
}

async function main() {
  if (process.argv.length !== 2) {
    throw new Error("The signed macOS candidate bundler accepts no arguments");
  }
  if (process.platform !== "darwin") {
    throw new Error("macOS Developer ID signing requires macOS");
  }
  const inputs = await signingInputs();
  const unsigned = await inspectUnsignedMacOSDistributionCandidate();
  if (
    unsigned.sourceRevision !== inputs.revisions.candidateRevision ||
    unsigned.toolingRevision !== inputs.revisions.toolingRevision ||
    unsigned.applicationTreeSHA256 !== inputs.unsignedApplicationTreeSHA256
  ) {
    throw new Error("The unsigned candidate or archive-bound tree changed before signing");
  }

  for (const name of ["vibermate", "vibermated"]) {
    runTool(
      "/usr/bin/codesign",
      signingCommandArguments(
        name,
        inputs.identity,
        inputs.keychainPath,
        unsigned.executablePaths[name],
      ),
      `${name} Developer ID signing`,
    );
  }
  runTool(
    "/usr/bin/codesign",
    signingCommandArguments(
      "application",
      inputs.identity,
      inputs.keychainPath,
      unsigned.appPath,
    ),
    "VibeMate.app Developer ID signing",
  );

  await mkdir(macOSDistributionDirectories.dmgDirectory, {
    recursive: true,
    mode: 0o700,
  });
  if ((await readdir(macOSDistributionDirectories.dmgDirectory)).length !== 0) {
    throw new Error("The fixed DMG output directory must start empty");
  }
  const dmgPath = resolve(
    macOSDistributionDirectories.dmgDirectory,
    macOSDistributionPolicy.diskImageFilename,
  );
  await prepareDiskImage(unsigned.appPath, dmgPath);
  runTool(
    "/usr/bin/codesign",
    signingCommandArguments(
      "diskImage",
      inputs.identity,
      inputs.keychainPath,
      dmgPath,
    ),
    "Distribution DMG Developer ID signing",
  );

  const signed = await inspectSignedMacOSDistributionCandidateCore(inputs.teamID);
  if (JSON.stringify(unsigned.tools) !== JSON.stringify(signed.tools)) {
    throw new Error("The admitted toolchain changed during signing");
  }
  const evidence = {
    schema: macOSDistributionPolicy.signingEvidenceSchema,
    createdAt: new Date().toISOString(),
    candidate: {
      app: repositoryRelativePath(signed.appPath),
      buildManifestSHA256: signed.manifestSHA256,
      diskImage: repositoryRelativePath(signed.dmgPath),
      diskImageSHA256: signed.diskImageSHA256,
      signedApplicationTreeSHA256: signed.applicationTreeSHA256,
      signedExecutableSHA256: signed.executableSHA256,
      sourceRevision: signed.sourceRevision,
      toolingRevision: signed.toolingRevision,
      unsignedApplicationTreeSHA256: unsigned.applicationTreeSHA256,
      unsignedArchiveFilename: macOSDistributionPolicy.unsignedAppArchiveFilename,
      unsignedArchiveSHA256: inputs.unsignedArchiveSHA256,
      unsignedExecutableSHA256: unsigned.executableSHA256,
      unsignedSidecarSHA256: unsigned.sidecarSHA256,
    },
    codeSigning: {
      certificateSHA256: signed.certificateSHA256,
      teamIdentifier: signed.teamIdentifier,
    },
    tools: signed.tools,
    unsignedApplicationLedger: unsigned.applicationLedger,
  };
  validateSigningTransformationEvidence(evidence);
  await writeSigningEvidence(evidence);
  await verifySignedMacOSDistributionCandidate(inputs.teamID);
  process.stdout.write("Trusted Developer ID signing transformation verified.\n");
}

function isMainModule() {
  return (
    typeof process.argv[1] === "string" &&
    import.meta.url === pathToFileURL(resolve(process.argv[1])).href
  );
}

if (isMainModule()) {
  await main();
}
