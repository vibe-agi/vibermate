import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { Buffer } from "node:buffer";
import { createReadStream } from "node:fs";
import {
  lstat,
  mkdtemp,
  open,
  readFile,
  readdir,
  readlink,
  realpath,
  rm,
  rmdir,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, dirname, relative, resolve, sep } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import {
  macOSDistributionPolicy,
  parseClosedJSONObject,
  releaseRevisionsFromEnvironment,
  requireCertificateSHA256,
  treeLedgerSHA256,
  validateAppleToolchainEvidence,
  validateClosedEntitlements,
  validateCodesignMetadata,
  validateDiskImageFormat,
  validateEmbeddedBuildManifest,
  validateInfoPlist,
  validateLipoArchitectures,
  validateMachOInventory,
  validateMountedDMGTopLevel,
  validateSigningTransformationEvidence,
  validateTreeLedgerEquality,
} from "./macos-distribution-policy.mjs";

const scriptDirectory = dirname(fileURLToPath(import.meta.url));
const repositoryDirectory = resolve(scriptDirectory, "../..");
const flutterDirectory = resolve(repositoryDirectory, "ui", "flutter_app");
const distributionReleaseDirectory = resolve(
  flutterDirectory,
  "build",
  "distribution",
  macOSDistributionPolicy.target,
  "release",
);
const distributionBundleDirectory = resolve(distributionReleaseDirectory, "bundle");
const appDirectory = resolve(distributionBundleDirectory, "macos");
const dmgDirectory = resolve(distributionBundleDirectory, "dmg");
const expectedAppPath = resolve(appDirectory, macOSDistributionPolicy.appBundleName);
const maximumCommandOutput = 1 << 20;
const localValidationTimeoutMilliseconds = 10 * 60 * 1000;
const maximumApplicationEntries = 8192;
const maximumApplicationFileBytes = 1 << 30;
const maximumApplicationTotalBytes = 2 * (1 << 30);
const maximumApplicationPathBytes = 4 << 10;
const maximumEvidenceBytes = 8 << 20;
const machOMagic = new Set([
  "cafebabe",
  "cafebabf",
  "cefaedfe",
  "cffaedfe",
  "bebafeca",
  "bfbafeca",
  "feedface",
  "feedfacf",
]);
const portableApplicationComponentPattern = /^[A-Za-z0-9._+@(), -]+$/u;

export const macOSDistributionDirectories = Object.freeze({
  appDirectory,
  bundleDirectory: distributionBundleDirectory,
  dmgDirectory,
  evidenceDirectory: resolve(
    distributionReleaseDirectory,
    ".vibermate-private",
    "notarization",
  ),
  releaseDirectory: distributionReleaseDirectory,
  repositoryDirectory,
  signingEvidencePath: resolve(
    distributionReleaseDirectory,
    ".vibermate-private",
    "signing",
    macOSDistributionPolicy.signingEvidenceFilename,
  ),
});

function localEnvironment() {
  const environment = { ...process.env, LANG: "C", LC_ALL: "C" };
  for (const name of Object.keys(environment)) {
    if (
      name.startsWith("APPLE_") ||
      name.startsWith("TAURI_SIGNING_") ||
      name === "API_PRIVATE_KEYS_DIR" ||
      name === "CODE_SIGN_IDENTITY" ||
      name === "DEVELOPMENT_TEAM" ||
      name === "EXPANDED_CODE_SIGN_IDENTITY" ||
      name === "OTHER_CODE_SIGN_FLAGS" ||
      name === "PROVISIONING_PROFILE" ||
      name === "PROVISIONING_PROFILE_SPECIFIER" ||
      name === "SIGNING_KEYCHAIN_PATH"
    ) {
      delete environment[name];
    }
  }
  delete environment.VIBERMATE_APPLE_TEAM_ID;
  return environment;
}

function runTool(command, arguments_, label, options = {}) {
  const result = spawnSync(command, arguments_, {
    cwd: repositoryDirectory,
    encoding: "utf8",
    env: localEnvironment(),
    input: options.input,
    maxBuffer: maximumCommandOutput,
    timeout: options.timeoutMilliseconds ?? localValidationTimeoutMilliseconds,
  });
  if (result.error !== undefined || result.signal !== null || result.status !== 0) {
    throw new Error(`${label} failed`);
  }
  return Object.freeze({
    stderr: result.stderr ?? "",
    stdout: result.stdout ?? "",
  });
}

async function admittedXcrunTool(name) {
  if (process.env.DEVELOPER_DIR !== macOSDistributionPolicy.developerDirectory) {
    throw new Error("DEVELOPER_DIR is not the admitted Xcode installation");
  }
  const selectedPath = runTool(
    "/usr/bin/xcrun",
    ["--find", name],
    `${name} path inspection`,
  ).stdout.trim();
  const canonicalPath = await realpath(selectedPath);
  for (const path of [selectedPath, canonicalPath]) {
    if (!path.startsWith(`${macOSDistributionPolicy.developerDirectory}/`)) {
      throw new Error(`${name} did not resolve inside the admitted Xcode`);
    }
  }
  return canonicalPath;
}

export async function appleToolchainEvidence() {
  const clangPath = await admittedXcrunTool("clang");
  const lipoPath = await admittedXcrunTool("lipo");
  const toolPaths = Object.freeze({
    clang: clangPath,
    codesign: "/usr/bin/codesign",
    ditto: "/usr/bin/ditto",
    hdiutil: "/usr/bin/hdiutil",
    lipo: lipoPath,
    security: "/usr/bin/security",
    spctl: "/usr/sbin/spctl",
    xcrun: "/usr/bin/xcrun",
  });
  const lipoVersion = runTool(
    lipoPath,
    ["-version"],
    "Apple lipo version inspection",
  );
  const evidence = {
    clang: runTool(clangPath, ["--version"], "Apple clang version inspection")
      .stdout.trim(),
    codesign: "/usr/bin/codesign",
    developerDirectory: process.env.DEVELOPER_DIR,
    hdiutil: "/usr/bin/hdiutil",
    lipo: `${lipoVersion.stdout}\n${lipoVersion.stderr}`.trim(),
    macOS: runTool(
      "/usr/bin/sw_vers",
      ["-productVersion"],
      "macOS version inspection",
    ).stdout.trim(),
    macOSBuild: runTool(
      "/usr/bin/sw_vers",
      ["-buildVersion"],
      "macOS build inspection",
    ).stdout.trim(),
    node: process.version,
    runnerImage: `${process.env.ImageOS ?? "unknown"}/${process.env.ImageVersion ?? "unknown"}`,
    sdk: runTool(
      "/usr/bin/xcrun",
      ["--sdk", "macosx", "--show-sdk-version"],
      "macOS SDK version inspection",
    ).stdout.trim(),
    toolPaths,
    toolSHA256: Object.freeze(
      Object.fromEntries(
        await Promise.all(
          Object.entries(toolPaths).map(async ([name, path]) => [
            name,
            await sha256File(path),
          ]),
        ),
      ),
    ),
    xcode: runTool(
      "/usr/bin/xcodebuild",
      ["-version"],
      "Xcode version inspection",
    ).stdout.trim(),
  };
  validateAppleToolchainEvidence(evidence);
  return Object.freeze({
    evidence: Object.freeze(evidence),
    paths: toolPaths,
  });
}

async function requireCanonicalPath(path, expectedType, label) {
  const metadata = await lstat(path);
  if (metadata.isSymbolicLink()) {
    throw new Error(`${label} must not be a symbolic link`);
  }
  if (
    (expectedType === "directory" && !metadata.isDirectory()) ||
    (expectedType === "file" && !metadata.isFile())
  ) {
    throw new Error(`${label} has the wrong file type`);
  }
  if ((await realpath(path)) !== path) {
    throw new Error(`${label} path must not contain symbolic links`);
  }
  return metadata;
}

async function resolveUnsignedApplicationPath() {
  await requireCanonicalPath(appDirectory, "directory", "macOS app directory");
  const entries = await readdir(appDirectory, { withFileTypes: true });
  if (
    entries.length !== 1 ||
    entries[0].name !== macOSDistributionPolicy.appBundleName ||
    !entries[0].isDirectory()
  ) {
    throw new Error("The unsigned candidate must contain exactly ViberMate.app");
  }
  await requireCanonicalPath(
    expectedAppPath,
    "directory",
    "unsigned macOS distribution application",
  );
  return expectedAppPath;
}

async function resolveSignedCandidatePaths() {
  const appPath = await resolveUnsignedApplicationPath();
  await requireCanonicalPath(dmgDirectory, "directory", "macOS DMG directory");
  const entries = await readdir(dmgDirectory, { withFileTypes: true });
  if (
    entries.length !== 1 ||
    entries[0].name !== macOSDistributionPolicy.diskImageFilename ||
    !entries[0].isFile()
  ) {
    throw new Error("The distribution target must contain the one fixed DMG");
  }
  const dmgPath = resolve(dmgDirectory, macOSDistributionPolicy.diskImageFilename);
  await requireCanonicalPath(dmgPath, "file", "macOS distribution DMG");
  return Object.freeze({
    appPath,
    dmgFilename: basename(dmgPath),
    dmgPath,
  });
}

export async function sha256File(path) {
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(path)) {
    hash.update(chunk);
  }
  return hash.digest("hex");
}

export async function applicationTreeLedger(root) {
  const ledger = [];
  const portablePaths = new Set();
  let totalBytes = 0;
  const visit = async (path, relativePath) => {
    const normalizedPath = relativePath.normalize("NFC");
    const portablePath = normalizedPath.toLocaleLowerCase("en-US");
    const components = relativePath.split("/");
    if (
      (typeof relativePath.isWellFormed === "function" &&
        !relativePath.isWellFormed()) ||
      Buffer.byteLength(relativePath, "utf8") === 0 ||
      Buffer.byteLength(relativePath, "utf8") > maximumApplicationPathBytes ||
      ledger.length >= maximumApplicationEntries ||
      normalizedPath !== relativePath ||
      /[\0-\x1f\\]/u.test(relativePath) ||
      components.some(
        (component) =>
          component.length === 0 ||
          component === ".." ||
          (component !== "." &&
            (component.trim() !== component ||
              !portableApplicationComponentPattern.test(component))),
      ) ||
      portablePaths.has(portablePath)
    ) {
      throw new Error("The macOS application tree exceeds its structural bound");
    }
    portablePaths.add(portablePath);
    const metadata = await lstat(path);
    const mode = metadata.mode & 0o7777;
    if ((mode & 0o7000) !== 0) {
      throw new Error("The macOS application contains a privileged mode");
    }
    if (metadata.isSymbolicLink()) {
      const target = await readlink(path);
      if (
        macOSDistributionPolicy.allowedApplicationSymlinks[relativePath] !==
        target
      ) {
        throw new Error(
          "The macOS application contains an unadmitted symbolic link",
        );
      }
      // POSIX does not define meaningful permission bits for symbolic links.
      // Darwin reports 0755 for App bundle links while Linux commonly reports
      // 0777, so encode the one canonical archive value on every host.
      ledger.push({ mode: 0o755, path: relativePath, target, type: "symlink" });
      return;
    }
    if ((mode & 0o022) !== 0) {
      throw new Error("The macOS application contains a group/world-writable mode");
    }
    if (metadata.isDirectory()) {
      if ((mode & 0o700) !== 0o700) {
        throw new Error("The macOS application contains a non-writable owner directory");
      }
      ledger.push({ mode, path: relativePath, type: "directory" });
      const entries = await readdir(path);
      entries.sort((left, right) => left.localeCompare(right, "en"));
      for (const name of entries) {
        await visit(
          resolve(path, name),
          relativePath === "." ? name : `${relativePath}/${name}`,
        );
      }
      return;
    }
    if (metadata.isFile()) {
      if (metadata.nlink !== 1) {
        throw new Error("The macOS application contains a hard-linked file");
      }
      if (metadata.size > maximumApplicationFileBytes) {
        throw new Error("The macOS application contains an oversized file");
      }
      totalBytes += metadata.size;
      if (totalBytes > maximumApplicationTotalBytes) {
        throw new Error("The macOS application exceeds its total byte bound");
      }
      ledger.push({
        mode,
        path: relativePath,
        sha256: await sha256File(path),
        size: metadata.size,
        type: "file",
      });
      return;
    }
    throw new Error("The macOS application contains a special file");
  };
  await visit(root, ".");
  return Object.freeze(ledger.map((entry) => Object.freeze(entry)));
}

function inspectApplicationMetadata(appPath) {
  const extendedAttributes = runTool(
    "/usr/bin/xattr",
    ["-lr", appPath],
    "Application extended-attribute inspection",
  );
  if (`${extendedAttributes.stdout}${extendedAttributes.stderr}`.trim() !== "") {
    throw new Error("The macOS application must not contain extended attributes");
  }
  const accessControl = runTool(
    "/bin/ls",
    ["-leR", appPath],
    "Application ACL inspection",
  );
  if (/^[dl-][rwxStTs-]{9}\+/mu.test(accessControl.stdout)) {
    throw new Error("The macOS application must not contain access-control lists");
  }
}

async function isMachOFile(path) {
  const handle = await open(path, "r");
  try {
    const header = Buffer.alloc(4);
    const { bytesRead } = await handle.read(header, 0, header.length, 0);
    return bytesRead === header.length && machOMagic.has(header.toString("hex"));
  } finally {
    await handle.close();
  }
}

async function inspectMachOPaths(appPath, ledger) {
  const paths = [];
  for (const entry of ledger) {
    if (entry.type === "file" && (await isMachOFile(resolve(appPath, entry.path)))) {
      paths.push(entry.path);
    }
  }
  validateMachOInventory(paths);
  return Object.freeze(
    Object.fromEntries(
      Object.entries(macOSDistributionPolicy.codeObjects).map(
        ([name, codeObject]) => [
          name,
          resolve(appPath, ...codeObject.relativePath.split("/")),
        ],
      ),
    ),
  );
}

function readPlistValue(infoPlistPath, name) {
  return runTool(
    "/usr/bin/plutil",
    ["-extract", name, "raw", "-o", "-", infoPlistPath],
    `Info.plist ${name} inspection`,
  ).stdout.trim();
}

async function inspectInfoPlist(appPath) {
  const path = resolve(appPath, "Contents", "Info.plist");
  await requireCanonicalPath(path, "file", "macOS application Info.plist");
  validateInfoPlist({
    bundleIdentifier: readPlistValue(path, "CFBundleIdentifier"),
    bundleExecutable: readPlistValue(path, "CFBundleExecutable"),
    bundleVersion: readPlistValue(path, "CFBundleVersion"),
    minimumSystemVersion: readPlistValue(path, "LSMinimumSystemVersion"),
    shortVersion: readPlistValue(path, "CFBundleShortVersionString"),
  });
}

async function inspectBuildManifest(appPath, expectedRevision) {
  const embeddedPath = resolve(
    appPath,
    "Contents",
    "Resources",
    "vibermate-build-manifest.json",
  );
  const metadata = await requireCanonicalPath(
    embeddedPath,
    "file",
    "Embedded Desktop build manifest",
  );
  if (metadata.size === 0 || metadata.size > (128 << 10)) {
    throw new Error("Desktop build manifest size is invalid");
  }
  const source = await readFile(embeddedPath);
  const provenance = validateEmbeddedBuildManifest(
    parseClosedJSONObject(source.toString("utf8"), "Embedded Desktop build manifest"),
    { expectedRevision },
  );
  return Object.freeze({
    manifestSHA256: createHash("sha256").update(source).digest("hex"),
    nestedCodeSHA256: provenance.nestedCodeSHA256,
    sidecarSHA256: provenance.sidecarSHA256,
    sourceCommitTime: provenance.commitTime,
    sourceRevision: provenance.revision,
  });
}

function inspectUnsignedCodeObject(path, label) {
  const result = spawnSync(
    "/usr/bin/codesign",
    ["--display", "--verbose=4", path],
    {
      cwd: repositoryDirectory,
      encoding: "utf8",
      env: localEnvironment(),
      maxBuffer: maximumCommandOutput,
      timeout: localValidationTimeoutMilliseconds,
    },
  );
  if (result.error !== undefined || result.signal !== null) {
    throw new Error(`${label} unsigned-signature inspection failed`);
  }
  const output = `${result.stdout ?? ""}\n${result.stderr ?? ""}`;
  if (result.status === 0) {
    if (
      /^Authority=/mu.test(output) ||
      /^TeamIdentifier=(?!not set$).+/mu.test(output) ||
      (!/Signature=adhoc/mu.test(output) && !/\b(?:adhoc|linker-signed)\b/mu.test(output))
    ) {
      throw new Error(`${label} contains a non-ad-hoc preexisting signature`);
    }
    return;
  }
  if (result.status !== 1 || !output.includes("code object is not signed at all")) {
    throw new Error(`${label} has an indeterminate preexisting signature`);
  }
}

function inspectEntitlements(path, label) {
  const result = runTool(
    "/usr/bin/codesign",
    ["--display", "--entitlements", "-", "--xml", path],
    `${label} entitlement inspection`,
  );
  validateClosedEntitlements(result.stdout, label);
}

function inspectCodeSignature(
  path,
  expectedTeamID,
  { deep = false, expectedIdentifier, label, requireRuntime },
) {
  const arguments_ = ["--verify"];
  if (deep) {
    arguments_.push("--deep");
  }
  arguments_.push("--strict=all", "--all-architectures", "--verbose=4", path);
  runTool(
    "/usr/bin/codesign",
    arguments_,
    `${label} strict code-signature verification`,
  );
  const display = runTool(
    "/usr/bin/codesign",
    ["--display", "--verbose=4", path],
    `${label} code-signature inspection`,
  );
  const metadata = validateCodesignMetadata(
    `${display.stdout}\n${display.stderr}`,
    expectedTeamID,
    { expectedIdentifier, label, requireRuntime },
  );
  inspectEntitlements(path, label);
  return metadata;
}

async function signingCertificateSHA256(path, label) {
  const temporaryDirectory = await mkdtemp(
    resolve(tmpdir(), "vibermate-signing-certificate-"),
  );
  try {
    const prefix = resolve(temporaryDirectory, "certificate");
    runTool(
      "/usr/bin/codesign",
      ["--display", `--extract-certificates=${prefix}`, path],
      `${label} certificate extraction`,
    );
    const leafPath = `${prefix}0`;
    await requireCanonicalPath(leafPath, "file", `${label} leaf certificate`);
    return requireCertificateSHA256(await sha256File(leafPath));
  } finally {
    await rm(temporaryDirectory, { recursive: true, force: true });
  }
}

function digestMap(paths) {
  return Promise.all(
    Object.entries(paths).map(async ([name, path]) => [name, await sha256File(path)]),
  ).then((entries) => Object.freeze(Object.fromEntries(entries)));
}

function requireDigestMapEquality(expected, actual, label) {
  const names = Object.keys(expected).sort();
  if (
    names.length !== Object.keys(actual).length ||
    names.some((name) => expected[name] !== actual[name])
  ) {
    throw new Error(`${label} does not match the trusted signing evidence`);
  }
}

export async function inspectSignedMacOSApplicationAtPath(
  appPath,
  expectedTeamID,
  { expectedRevision, toolchain: providedToolchain } = {},
) {
  await requireCanonicalPath(appPath, "directory", "signed macOS application");
  if (basename(appPath) !== macOSDistributionPolicy.appBundleName) {
    throw new Error("The signed macOS application has an unexpected bundle name");
  }
  const toolchain = providedToolchain ?? (await appleToolchainEvidence());
  inspectApplicationMetadata(appPath);
  await inspectInfoPlist(appPath);
  const buildProvenance = await inspectBuildManifest(appPath, expectedRevision);
  const ledger = await applicationTreeLedger(appPath);
  const executablePaths = await inspectMachOPaths(appPath, ledger);
  const certificateFingerprints = new Set();
  inspectCodeSignature(appPath, expectedTeamID, {
    deep: true,
    expectedIdentifier: macOSDistributionPolicy.appIdentifier,
    label: "ViberMate.app",
    requireRuntime: true,
  });
  certificateFingerprints.add(
    await signingCertificateSHA256(appPath, "ViberMate.app"),
  );
  for (const [name, path] of Object.entries(executablePaths)) {
    validateLipoArchitectures(
      runTool(
        toolchain.paths.lipo,
        ["-archs", path],
        `${name} architecture inspection`,
      ).stdout,
      name,
    );
    inspectCodeSignature(path, expectedTeamID, {
      deep: false,
      expectedIdentifier: macOSDistributionPolicy.codeObjects[name].identifier,
      label: name,
      requireRuntime: true,
    });
    certificateFingerprints.add(await signingCertificateSHA256(path, name));
  }
  if (certificateFingerprints.size !== 1) {
    throw new Error("The application executables must use one signing certificate");
  }
  return Object.freeze({
    appPath,
    applicationLedger: ledger,
    applicationTreeSHA256: treeLedgerSHA256(ledger),
    architectures: macOSDistributionPolicy.architectures,
    certificateSHA256: [...certificateFingerprints][0],
    executableSHA256: await digestMap(executablePaths),
    manifestSHA256: buildProvenance.manifestSHA256,
    nestedCodeSHA256: buildProvenance.nestedCodeSHA256,
    sidecarSHA256: buildProvenance.sidecarSHA256,
    sourceCommitTime: buildProvenance.sourceCommitTime,
    sourceRevision: buildProvenance.sourceRevision,
    teamIdentifier: expectedTeamID,
    tools: toolchain.evidence,
  });
}

export async function inspectSignedMacOSDiskImageAtPath(
  dmgPath,
  expectedTeamID,
) {
  await requireCanonicalPath(dmgPath, "file", "signed macOS disk image");
  verifyDiskImage(dmgPath, expectedTeamID);
  return Object.freeze({
    certificateSHA256: await signingCertificateSHA256(
      dmgPath,
      "Distribution DMG",
    ),
    diskImageSHA256: await sha256File(dmgPath),
    dmgFilename: basename(dmgPath),
    dmgPath,
    teamIdentifier: expectedTeamID,
  });
}

export async function inspectUnsignedMacOSDistributionCandidate() {
  if (process.platform !== "darwin") {
    throw new Error("macOS distribution preflight requires macOS");
  }
  const revisions = releaseRevisionsFromEnvironment(process.env);
  const appPath = await resolveUnsignedApplicationPath();
  const toolchain = await appleToolchainEvidence();
  inspectApplicationMetadata(appPath);
  await inspectInfoPlist(appPath);
  const buildProvenance = await inspectBuildManifest(
    appPath,
    revisions.candidateRevision,
  );
  const ledger = await applicationTreeLedger(appPath);
  const executablePaths = await inspectMachOPaths(appPath, ledger);
  inspectUnsignedCodeObject(appPath, "ViberMate.app");
  for (const [name, path] of Object.entries(executablePaths)) {
    validateLipoArchitectures(
      runTool(toolchain.paths.lipo, ["-archs", path], `${name} architecture inspection`)
        .stdout,
      name,
    );
    inspectUnsignedCodeObject(path, name);
  }
  const executableSHA256 = await digestMap(executablePaths);
  for (const name of [
    "app-framework",
    "flutter-macos-framework",
    "vibermate",
    "vibermated",
  ]) {
    if (executableSHA256[name] !== buildProvenance.nestedCodeSHA256[name]) {
      throw new Error(`${name} does not match its embedded build-manifest digest`);
    }
  }
  return Object.freeze({
    appPath,
    applicationLedger: ledger,
    applicationTreeSHA256: treeLedgerSHA256(ledger),
    executablePaths,
    executableSHA256,
    manifestSHA256: buildProvenance.manifestSHA256,
    nestedCodeSHA256: buildProvenance.nestedCodeSHA256,
    sidecarSHA256: buildProvenance.sidecarSHA256,
    sourceCommitTime: buildProvenance.sourceCommitTime,
    sourceRevision: buildProvenance.sourceRevision,
    toolingRevision: revisions.toolingRevision,
    tools: toolchain.evidence,
  });
}

async function mountedDMGTopLevel(directory) {
  const entries = [];
  for (const name of await readdir(directory)) {
    const path = resolve(directory, name);
    const metadata = await lstat(path);
    if (metadata.isSymbolicLink()) {
      entries.push({ name, target: await readlink(path), type: "symlink" });
    } else if (metadata.isDirectory()) {
      entries.push({ name, type: "directory" });
    } else if (metadata.isFile()) {
      entries.push({ name, type: "file" });
    } else {
      throw new Error("The mounted DMG contains a special top-level file");
    }
  }
  return entries;
}

async function verifyMountedApplication(paths, expectedTeamID, externalLedger) {
  const mountDirectory = await mkdtemp(resolve(tmpdir(), "vibermate-dmg-mount-"));
  let attached = false;
  let verificationError;
  try {
    runTool(
      "/usr/bin/hdiutil",
      [
        "attach",
        paths.dmgPath,
        "-readonly",
        "-nobrowse",
        "-noautoopen",
        "-mountpoint",
        mountDirectory,
        "-plist",
      ],
      "Read-only DMG attachment",
    );
    attached = true;
    validateMountedDMGTopLevel(await mountedDMGTopLevel(mountDirectory));
    const mountedAppPath = resolve(mountDirectory, macOSDistributionPolicy.appBundleName);
    const metadata = await lstat(mountedAppPath);
    if (metadata.isSymbolicLink() || !metadata.isDirectory()) {
      throw new Error("The mounted ViberMate.app is invalid");
    }
    validateTreeLedgerEquality(externalLedger, await applicationTreeLedger(mountedAppPath));
    inspectCodeSignature(mountedAppPath, expectedTeamID, {
      deep: true,
      expectedIdentifier: macOSDistributionPolicy.appIdentifier,
      label: "Mounted ViberMate.app",
      requireRuntime: true,
    });
  } catch (error) {
    verificationError = error;
  }
  let detachError;
  if (attached) {
    try {
      runTool(
        "/usr/bin/hdiutil",
        ["detach", mountDirectory],
        "Read-only DMG detachment",
      );
      attached = false;
    } catch (error) {
      detachError = error;
    }
  }
  let cleanupError;
  if (!attached) {
    try {
      await rmdir(mountDirectory);
    } catch (error) {
      cleanupError = error;
    }
  }
  const failures = [verificationError, detachError, cleanupError].filter(Boolean);
  if (failures.length > 1) {
    throw new AggregateError(failures, "DMG verification or cleanup failed more than once");
  }
  if (failures.length === 1) {
    throw failures[0];
  }
  return treeLedgerSHA256(externalLedger);
}

function verifyDiskImage(path, expectedTeamID) {
  runTool("/usr/bin/hdiutil", ["verify", path], "DMG checksum verification");
  validateDiskImageFormat(
    runTool(
      "/usr/bin/hdiutil",
      ["imageinfo", "-format", path],
      "DMG format inspection",
    ).stdout,
  );
  return inspectCodeSignature(path, expectedTeamID, {
    deep: false,
    expectedIdentifier: macOSDistributionPolicy.diskImageIdentifier,
    label: "Distribution DMG",
    requireRuntime: false,
  });
}

export async function inspectSignedMacOSDistributionCandidateCore(expectedTeamID) {
  if (process.platform !== "darwin") {
    throw new Error("macOS distribution verification requires macOS");
  }
  const revisions = releaseRevisionsFromEnvironment(process.env);
  const paths = await resolveSignedCandidatePaths();
  const toolchain = await appleToolchainEvidence();
  const application = await inspectSignedMacOSApplicationAtPath(
    paths.appPath,
    expectedTeamID,
    {
      expectedRevision: revisions.candidateRevision,
      toolchain,
    },
  );
  const diskImage = await inspectSignedMacOSDiskImageAtPath(
    paths.dmgPath,
    expectedTeamID,
  );
  if (application.certificateSHA256 !== diskImage.certificateSHA256) {
    throw new Error("The application, executables, and DMG must use one signing certificate");
  }
  const applicationTreeSHA256 = await verifyMountedApplication(
    paths,
    expectedTeamID,
    application.applicationLedger,
  );
  return Object.freeze({
    appPath: paths.appPath,
    applicationTreeSHA256,
    architectures: application.architectures,
    certificateSHA256: application.certificateSHA256,
    diskImageSHA256: diskImage.diskImageSHA256,
    dmgFilename: paths.dmgFilename,
    dmgPath: paths.dmgPath,
    executableSHA256: application.executableSHA256,
    manifestSHA256: application.manifestSHA256,
    nestedCodeSHA256: application.nestedCodeSHA256,
    sidecarSHA256: application.sidecarSHA256,
    sourceCommitTime: application.sourceCommitTime,
    sourceRevision: application.sourceRevision,
    teamIdentifier: expectedTeamID,
    toolingRevision: revisions.toolingRevision,
    tools: toolchain.evidence,
  });
}

async function readSigningEvidence() {
  const metadata = await requireCanonicalPath(
    macOSDistributionDirectories.signingEvidencePath,
    "file",
    "macOS signing transformation evidence",
  );
  if (
    metadata.size === 0 ||
    metadata.size > maximumEvidenceBytes ||
    (metadata.mode & 0o077) !== 0
  ) {
    throw new Error("macOS signing transformation evidence is not a private bounded file");
  }
  const source = await readFile(macOSDistributionDirectories.signingEvidencePath);
  const value = parseClosedJSONObject(
    source.toString("utf8"),
    "macOS signing transformation evidence",
  );
  validateSigningTransformationEvidence(value);
  return Object.freeze({
    digest: createHash("sha256").update(source).digest("hex"),
    value,
  });
}

export async function verifySignedMacOSDistributionCandidate(
  expectedTeamID = process.env.VIBERMATE_APPLE_TEAM_ID,
  { expectedPreStapleSHA256 } = {},
) {
  const candidate = await inspectSignedMacOSDistributionCandidateCore(expectedTeamID);
  const evidence = await readSigningEvidence();
  const recorded = evidence.value;
  if (
    recorded.candidate.sourceRevision !== candidate.sourceRevision ||
    recorded.candidate.toolingRevision !== candidate.toolingRevision ||
    recorded.candidate.buildManifestSHA256 !== candidate.manifestSHA256 ||
    recorded.candidate.signedApplicationTreeSHA256 !== candidate.applicationTreeSHA256 ||
    recorded.codeSigning.certificateSHA256 !== candidate.certificateSHA256 ||
    recorded.codeSigning.teamIdentifier !== candidate.teamIdentifier ||
    JSON.stringify(recorded.tools) !== JSON.stringify(candidate.tools)
  ) {
    throw new Error("The signed candidate does not match its trusted signing evidence");
  }
  requireDigestMapEquality(
    recorded.candidate.signedExecutableSHA256,
    candidate.executableSHA256,
    "Signed executable inventory",
  );
  requireDigestMapEquality(
    recorded.candidate.unsignedSidecarSHA256,
    candidate.sidecarSHA256,
    "Embedded sidecar provenance",
  );
  if (expectedPreStapleSHA256 === undefined) {
    if (recorded.candidate.diskImageSHA256 !== candidate.diskImageSHA256) {
      throw new Error("The pre-staple DMG does not match the trusted signing evidence");
    }
  } else if (recorded.candidate.diskImageSHA256 !== expectedPreStapleSHA256) {
    throw new Error("The stapled DMG is not bound to the submitted pre-staple digest");
  }
  return Object.freeze({
    ...candidate,
    signingEvidenceSHA256: evidence.digest,
    unsignedApplicationTreeSHA256:
      recorded.candidate.unsignedApplicationTreeSHA256,
    unsignedArchiveFilename: recorded.candidate.unsignedArchiveFilename,
    unsignedArchiveSHA256: recorded.candidate.unsignedArchiveSHA256,
  });
}

function isMainModule() {
  return (
    typeof process.argv[1] === "string" &&
    import.meta.url === pathToFileURL(resolve(process.argv[1])).href
  );
}

if (isMainModule()) {
  if (process.argv.length !== 2) {
    throw new Error("The signed macOS candidate verifier accepts no arguments");
  }
  await verifySignedMacOSDistributionCandidate();
  process.stdout.write("Signed macOS distribution candidate verified.\n");
}
