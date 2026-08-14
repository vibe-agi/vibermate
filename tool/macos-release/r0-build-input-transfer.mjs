import { createHash } from "node:crypto";
import { constants } from "node:fs";
import {
  chmod,
  lstat,
  mkdir,
  mkdtemp,
  open,
  readdir,
  realpath,
  rename,
  rm,
} from "node:fs/promises";
import {
  basename,
  dirname,
  isAbsolute,
  normalize,
  resolve,
} from "node:path";
import { TextDecoder } from "node:util";
import {
  createMacOSApplicationArchive,
  extractMacOSApplicationArchive,
} from "./macos-application-archive.mjs";
import { treeLedgerSHA256 } from "./macos-distribution-policy.mjs";
import { applicationTreeLedger } from "./verify-macos-signed-candidate.mjs";

const executableNames = Object.freeze([
  "vibermate-desktop",
  "vibermate",
  "vibermated",
]);
const topLevelNames = Object.freeze([
  ...executableNames,
  "vibermate-build-manifest.json",
  "LICENSE",
  "dist",
]);
const maximumInputEntries = 8192;
const maximumInputFileBytes = 1 * 1024 * 1024 * 1024;
const maximumInputTotalBytes = 2 * 1024 * 1024 * 1024;
const maximumManifestBytes = 128 << 10;
const maximumLicenseBytes = 1 << 20;
const maximumArchiveBytes = 3 * 1024 * 1024 * 1024;
const maximumEnvironmentPathBytes = 4096;
const maximumInputPathBytes = 1024;
const digestPattern = /^[0-9a-f]{64}$/u;
const printableASCIIPathPattern = /^[\x21-\x7e]+$/u;
const portableInputPathPattern = /^[A-Za-z0-9._/-]+$/u;
const portableSuffixPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/u;
const decoder = new TextDecoder("utf-8", { fatal: true });

export const r0BuildInputTransferPolicy = Object.freeze({
  archiveFilename: "vibermate-r0-build-input.vma",
  checksumFilename: "vibermate-r0-build-input.vma.sha256",
  downloadDirectoryPrefix: "vibermate-r0-input-download-",
  executableNames,
  restoredInputDirectoryPrefix: "vibermate-r0-input-restored-",
  sourceInputDirectoryPrefix: "vibermate-r0-input-source-",
  topLevelNames,
  transferDirectoryPrefix: "vibermate-r0-input-",
});

function permissionMode(metadata) {
  return metadata.mode & 0o7777;
}

function compareText(left, right) {
  return Buffer.compare(Buffer.from(left, "utf8"), Buffer.from(right, "utf8"));
}

function sameIdentity(left, right) {
  return left.dev === right.dev && left.ino === right.ino;
}

function sameRegularFileState(left, right) {
  return (
    left.isFile() &&
    right.isFile() &&
    sameIdentity(left, right) &&
    left.nlink === 1 &&
    right.nlink === 1 &&
    left.size === right.size &&
    permissionMode(left) === permissionMode(right) &&
    left.ctimeMs === right.ctimeMs &&
    left.mtimeMs === right.mtimeMs
  );
}

function sameDirectoryState(left, right) {
  return (
    left.isDirectory() &&
    right.isDirectory() &&
    sameIdentity(left, right) &&
    permissionMode(left) === permissionMode(right) &&
    left.ctimeMs === right.ctimeMs &&
    left.mtimeMs === right.mtimeMs
  );
}

function requireCleanAbsoluteASCIIPath(value, label) {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value.trim() !== value ||
    Buffer.byteLength(value, "utf8") > maximumEnvironmentPathBytes ||
    !printableASCIIPathPattern.test(value) ||
    (typeof value.isWellFormed === "function" && !value.isWellFormed()) ||
    !isAbsolute(value) ||
    normalize(value) !== value ||
    resolve(value) !== value ||
    dirname(value) === value
  ) {
    throw new Error(`${label} must be a clean absolute non-root ASCII path`);
  }
  return value;
}

function environmentPath(environment, name) {
  if (environment === null || typeof environment !== "object") {
    throw new Error(
      "The source-traceability build-input environment (R0) is invalid",
    );
  }
  return requireCleanAbsoluteASCIIPath(environment[name], name);
}

function validateBoundaryDirectoryMode(metadata, label) {
  const mode = permissionMode(metadata);
  if (
    (mode & 0o7000) !== 0 ||
    (mode & 0o022) !== 0 ||
    (mode & 0o700) !== 0o700
  ) {
    throw new Error(`${label} has an unsafe mode`);
  }
}

async function requireCanonicalBoundaryDirectory(path, label) {
  const metadata = await lstat(path);
  if (metadata.isSymbolicLink() || !metadata.isDirectory()) {
    throw new Error(`${label} must be a non-symbolic-link directory`);
  }
  if ((await realpath(path)) !== path) {
    throw new Error(`${label} must be canonical`);
  }
  validateBoundaryDirectoryMode(metadata, label);
  return metadata;
}

async function runnerTempFromEnvironment(environment) {
  const runnerTemp = environmentPath(environment, "RUNNER_TEMP");
  await requireCanonicalBoundaryDirectory(runnerTemp, "RUNNER_TEMP");
  return runnerTemp;
}

function admittedRunnerChild(environment, name, runnerTemp, prefix) {
  const path = environmentPath(environment, name);
  const childName = basename(path);
  const suffix = childName.startsWith(prefix)
    ? childName.slice(prefix.length)
    : "";
  if (
    dirname(path) !== runnerTemp ||
    !childName.startsWith(prefix) ||
    !portableSuffixPattern.test(suffix)
  ) {
    throw new Error(`${name} is not an admitted RUNNER_TEMP path`);
  }
  return path;
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
  throw new Error(`${label} already exists`);
}

function requireExactNames(actual, expected, label) {
  const observed = [...actual].sort(compareText);
  const wanted = [...expected].sort(compareText);
  if (
    observed.length !== wanted.length ||
    observed.some((name, index) => name !== wanted[index])
  ) {
    throw new Error(`${label} has an unexpected inventory`);
  }
  return observed;
}

function validateInputPath(path) {
  if (path === ".") {
    return;
  }
  if (
    Buffer.byteLength(path, "utf8") === 0 ||
    Buffer.byteLength(path, "utf8") > maximumInputPathBytes ||
    !portableInputPathPattern.test(path) ||
    path.startsWith("/") ||
    path.includes("\\") ||
    path.includes(":") ||
    path.split("/").some(
      (component) =>
        component.length === 0 || component === "." || component === "..",
    )
  ) {
    throw new Error(
      "The source-traceability build-input tree (R0) contains an invalid path",
    );
  }
}

function validateInputLedger(ledger) {
  if (!Array.isArray(ledger) || ledger.length === 0 || ledger.length > maximumInputEntries) {
    throw new Error(
      "The source-traceability build-input tree (R0) exceeds its entry bound",
    );
  }
  let totalBytes = 0;
  let distFiles = 0;
  const byPath = new Map();
  for (const entry of ledger) {
    validateInputPath(entry.path);
    byPath.set(entry.path, entry);
    if (entry.type === "file") {
      if (
        !Number.isSafeInteger(entry.size) ||
        entry.size < 0 ||
        entry.size > maximumInputFileBytes ||
        !digestPattern.test(entry.sha256 ?? "")
      ) {
        throw new Error(
          "The source-traceability build-input tree (R0) contains an out-of-bounds file",
        );
      }
      totalBytes += entry.size;
      if (totalBytes > maximumInputTotalBytes) {
        throw new Error(
          "The source-traceability build-input tree (R0) exceeds its total byte bound",
        );
      }
    }
  }

  const root = byPath.get(".");
  if (root?.type !== "directory" || root.mode !== 0o755) {
    throw new Error(
      "The source-traceability build-input root (R0) has an invalid type or mode",
    );
  }
  for (const name of executableNames) {
    const entry = byPath.get(name);
    if (entry?.type !== "file" || entry.mode !== 0o755 || entry.size === 0) {
      throw new Error(`${name} has an invalid type, mode, or size`);
    }
  }
  const manifest = byPath.get("vibermate-build-manifest.json");
  if (
    manifest?.type !== "file" ||
    manifest.mode !== 0o600 ||
    manifest.size === 0 ||
    manifest.size > maximumManifestBytes
  ) {
    throw new Error("The Desktop build manifest has an invalid type, mode, or size");
  }
  const license = byPath.get("LICENSE");
  if (
    license?.type !== "file" ||
    license.mode !== 0o644 ||
    license.size === 0 ||
    license.size > maximumLicenseBytes
  ) {
    throw new Error("LICENSE has an invalid type, mode, or size");
  }
  const dist = byPath.get("dist");
  if (dist?.type !== "directory" || dist.mode !== 0o755) {
    throw new Error("dist has an invalid type or mode");
  }
  for (const entry of ledger) {
    if (!entry.path.startsWith("dist/")) {
      continue;
    }
    if (
      (entry.type === "directory" && entry.mode !== 0o755) ||
      (entry.type === "file" && entry.mode !== 0o644) ||
      (entry.type !== "directory" && entry.type !== "file")
    ) {
      throw new Error("The dist tree contains an invalid type or mode");
    }
    if (entry.type === "file") {
      distFiles += 1;
    }
  }
  if (distFiles === 0) {
    throw new Error("The dist tree must contain at least one regular file");
  }
}

export async function inspectR0BuildInputTree(inputRoot) {
  requireCleanAbsoluteASCIIPath(
    inputRoot,
    "source-traceability build-input root (R0)",
  );
  const initialRoot = await requireCanonicalBoundaryDirectory(
    inputRoot,
    "source-traceability build-input root (R0)",
  );
  const initialNames = requireExactNames(
    await readdir(inputRoot),
    topLevelNames,
    "The source-traceability build-input root (R0)",
  );
  const ledger = await applicationTreeLedger(inputRoot);
  validateInputLedger(ledger);
  const finalRoot = await lstat(inputRoot);
  const finalNames = requireExactNames(
    await readdir(inputRoot),
    topLevelNames,
    "The source-traceability build-input root (R0)",
  );
  if (
    !sameDirectoryState(initialRoot, finalRoot) ||
    initialNames.some((name, index) => name !== finalNames[index]) ||
    (await realpath(inputRoot)) !== inputRoot
  ) {
    throw new Error(
      "The source-traceability build-input root (R0) changed while it was inspected",
    );
  }
  return Object.freeze({
    inputLedger: ledger,
    inputTreeSHA256: treeLedgerSHA256(ledger),
  });
}

function validateTransferFileMetadata(metadata, label, maximumBytes, exactBytes) {
  const mode = permissionMode(metadata);
  if (
    metadata.isSymbolicLink() ||
    !metadata.isFile() ||
    metadata.nlink !== 1 ||
    metadata.size === 0 ||
    metadata.size > maximumBytes ||
    (exactBytes !== undefined && metadata.size !== exactBytes) ||
    (mode & 0o7000) !== 0 ||
    (mode & 0o111) !== 0 ||
    (mode & 0o022) !== 0 ||
    (mode & 0o400) === 0
  ) {
    throw new Error(`${label} is not an admitted bounded regular file`);
  }
}

function expectedChecksumBytes() {
  return Buffer.byteLength(
    `${"0".repeat(64)}  ${r0BuildInputTransferPolicy.archiveFilename}\n`,
    "ascii",
  );
}

async function inspectTransferInventory(directory) {
  const directoryMetadata = await requireCanonicalBoundaryDirectory(
    directory,
    "source-traceability build-input transfer directory (R0)",
  );
  const names = requireExactNames(
    await readdir(directory),
    [
      r0BuildInputTransferPolicy.archiveFilename,
      r0BuildInputTransferPolicy.checksumFilename,
    ],
    "The source-traceability build-input transfer (R0)",
  );
  const metadata = new Map();
  for (const name of names) {
    const path = resolve(directory, name);
    const info = await lstat(path);
    validateTransferFileMetadata(
      info,
      `source-traceability build-input transfer file (R0) ${name}`,
      name === r0BuildInputTransferPolicy.archiveFilename
        ? maximumArchiveBytes
        : expectedChecksumBytes(),
      name === r0BuildInputTransferPolicy.checksumFilename
        ? expectedChecksumBytes()
        : undefined,
    );
    if ((await realpath(path)) !== path) {
      throw new Error(
        `Source-traceability build-input transfer file (R0) ${name} is not canonical`,
      );
    }
    metadata.set(name, info);
  }
  return Object.freeze({ directoryMetadata, fileMetadata: metadata });
}

async function readStableRegularFile(path, initialMetadata, label) {
  const handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const openedMetadata = await handle.stat();
    if (!sameRegularFileState(initialMetadata, openedMetadata)) {
      throw new Error(`${label} changed while it was opened`);
    }
    const payload = await handle.readFile();
    const finalOpenedMetadata = await handle.stat();
    const finalPathMetadata = await lstat(path);
    if (
      payload.length !== openedMetadata.size ||
      !sameRegularFileState(openedMetadata, finalOpenedMetadata) ||
      !sameRegularFileState(openedMetadata, finalPathMetadata)
    ) {
      throw new Error(`${label} changed while it was read`);
    }
    return payload;
  } finally {
    await handle.close();
  }
}

async function hashStableRegularFile(path, initialMetadata, label) {
  const handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  const hash = createHash("sha256");
  let position = 0;
  const buffer = Buffer.allocUnsafe(1 << 20);
  try {
    const openedMetadata = await handle.stat();
    if (!sameRegularFileState(initialMetadata, openedMetadata)) {
      throw new Error(`${label} changed while it was opened`);
    }
    while (position < openedMetadata.size) {
      const wanted = Math.min(buffer.length, openedMetadata.size - position);
      const { bytesRead } = await handle.read(buffer, 0, wanted, position);
      if (bytesRead === 0) {
        throw new Error(`${label} became shorter while it was hashed`);
      }
      hash.update(buffer.subarray(0, bytesRead));
      position += bytesRead;
    }
    if ((await handle.read(Buffer.alloc(1), 0, 1, position)).bytesRead !== 0) {
      throw new Error(`${label} became longer while it was hashed`);
    }
    const finalOpenedMetadata = await handle.stat();
    const finalPathMetadata = await lstat(path);
    if (
      !sameRegularFileState(openedMetadata, finalOpenedMetadata) ||
      !sameRegularFileState(openedMetadata, finalPathMetadata)
    ) {
      throw new Error(`${label} changed while it was hashed`);
    }
  } finally {
    await handle.close();
  }
  return hash.digest("hex");
}

function parseChecksum(payload) {
  let source;
  try {
    source = decoder.decode(payload);
  } catch {
    throw new Error(
      "The source-traceability build-input checksum (R0) is not valid UTF-8",
    );
  }
  const archiveSHA256 = source.slice(0, 64);
  if (
    !digestPattern.test(archiveSHA256) ||
    source !==
      `${archiveSHA256}  ${r0BuildInputTransferPolicy.archiveFilename}\n`
  ) {
    throw new Error(
      "The source-traceability build-input checksum (R0) has a non-canonical shape",
    );
  }
  return archiveSHA256;
}

function requireUnchangedInventory(initial, final) {
  if (!sameDirectoryState(initial.directoryMetadata, final.directoryMetadata)) {
    throw new Error(
      "The source-traceability build-input transfer directory (R0) changed while it was consumed",
    );
  }
  for (const name of [
    r0BuildInputTransferPolicy.archiveFilename,
    r0BuildInputTransferPolicy.checksumFilename,
  ]) {
    if (
      !sameRegularFileState(
        initial.fileMetadata.get(name),
        final.fileMetadata.get(name),
      )
    ) {
      throw new Error(
        "The source-traceability build-input transfer (R0) changed while it was consumed",
      );
    }
  }
}

function requireCreatedTransferModes(transfer) {
  if (permissionMode(transfer.directoryMetadata) !== 0o700) {
    throw new Error(
      "The created source-traceability build-input transfer directory (R0) has an invalid mode",
    );
  }
  for (const metadata of transfer.fileMetadata.values()) {
    if (permissionMode(metadata) !== 0o600) {
      throw new Error(
        "The created source-traceability build-input transfer file (R0) has an invalid mode",
      );
    }
  }
}

async function writeChecksum(path, archiveSHA256) {
  if (!digestPattern.test(archiveSHA256)) {
    throw new Error(
      "The source-traceability build-input archive digest (R0) is invalid",
    );
  }
  const handle = await open(
    path,
    constants.O_CREAT |
      constants.O_EXCL |
      constants.O_WRONLY |
      constants.O_NOFOLLOW,
    0o600,
  );
  try {
    await handle.writeFile(
      `${archiveSHA256}  ${r0BuildInputTransferPolicy.archiveFilename}\n`,
      "ascii",
    );
    await handle.chmod(0o600);
    await handle.sync();
  } finally {
    await handle.close();
  }
}

export async function createR0BuildInputTransfer(environment = process.env) {
  const runnerTemp = await runnerTempFromEnvironment(environment);
  const inputRoot = admittedRunnerChild(
    environment,
    "VIBERMATE_R0_INPUT_ROOT",
    runnerTemp,
    r0BuildInputTransferPolicy.sourceInputDirectoryPrefix,
  );
  const transferDirectory = admittedRunnerChild(
    environment,
    "VIBERMATE_R0_INPUT_TRANSFER_DIRECTORY",
    runnerTemp,
    r0BuildInputTransferPolicy.transferDirectoryPrefix,
  );
  await requireAbsent(
    transferDirectory,
    "The source-traceability build-input transfer directory (R0)",
  );
  const initial = await inspectR0BuildInputTree(inputRoot);
  await mkdir(transferDirectory, { mode: 0o700 });
  await chmod(transferDirectory, 0o700);
  await requireCanonicalBoundaryDirectory(
    transferDirectory,
    "source-traceability build-input transfer directory (R0)",
  );
  const archivePath = resolve(
    transferDirectory,
    r0BuildInputTransferPolicy.archiveFilename,
  );

  // The audited ViberMate.app archive v1 root is intentionally reused only as
  // an internal serialization token. The external artifact and restored tree
  // retain their machine-level R0 names while the human description stays
  // source-traceability build inputs.
  const archived = await createMacOSApplicationArchive(inputRoot, archivePath);
  await chmod(archivePath, 0o600);
  if (archived.applicationTreeSHA256 !== initial.inputTreeSHA256) {
    throw new Error(
      "The source-traceability build-input tree (R0) changed while it was archived",
    );
  }
  const checksumPath = resolve(
    transferDirectory,
    r0BuildInputTransferPolicy.checksumFilename,
  );
  await writeChecksum(checksumPath, archived.archiveSHA256);
  const transfer = await inspectTransferInventory(transferDirectory);
  requireCreatedTransferModes(transfer);
  const observedArchiveSHA256 = await hashStableRegularFile(
    archivePath,
    transfer.fileMetadata.get(r0BuildInputTransferPolicy.archiveFilename),
    "source-traceability build-input archive (R0)",
  );
  const checksumSHA256 = parseChecksum(
    await readStableRegularFile(
      checksumPath,
      transfer.fileMetadata.get(r0BuildInputTransferPolicy.checksumFilename),
      "source-traceability build-input checksum (R0)",
    ),
  );
  const final = await inspectR0BuildInputTree(inputRoot);
  if (
    observedArchiveSHA256 !== archived.archiveSHA256 ||
    checksumSHA256 !== archived.archiveSHA256 ||
    final.inputTreeSHA256 !== initial.inputTreeSHA256
  ) {
    throw new Error(
      "The source-traceability build-input transfer (R0) is not stable and closed",
    );
  }
  const finalTransfer = await inspectTransferInventory(transferDirectory);
  requireCreatedTransferModes(finalTransfer);
  requireUnchangedInventory(transfer, finalTransfer);
  return Object.freeze({
    archivePath,
    archiveSHA256: archived.archiveSHA256,
    checksumPath,
    inputTreeSHA256: initial.inputTreeSHA256,
    transferDirectory,
  });
}

export async function restoreR0BuildInputTransfer(environment = process.env) {
  const runnerTemp = await runnerTempFromEnvironment(environment);
  const downloadDirectory = admittedRunnerChild(
    environment,
    "VIBERMATE_R0_INPUT_DOWNLOAD_DIRECTORY",
    runnerTemp,
    r0BuildInputTransferPolicy.downloadDirectoryPrefix,
  );
  const restoredInputRoot = admittedRunnerChild(
    environment,
    "VIBERMATE_R0_RESTORED_INPUT_ROOT",
    runnerTemp,
    r0BuildInputTransferPolicy.restoredInputDirectoryPrefix,
  );
  await requireAbsent(
    restoredInputRoot,
    "The restored source-traceability build-input root (R0)",
  );
  const initialTransfer = await inspectTransferInventory(downloadDirectory);
  const archivePath = resolve(
    downloadDirectory,
    r0BuildInputTransferPolicy.archiveFilename,
  );
  const checksumPath = resolve(
    downloadDirectory,
    r0BuildInputTransferPolicy.checksumFilename,
  );
  const expectedArchiveSHA256 = parseChecksum(
    await readStableRegularFile(
      checksumPath,
      initialTransfer.fileMetadata.get(
        r0BuildInputTransferPolicy.checksumFilename,
      ),
      "source-traceability build-input checksum (R0)",
    ),
  );
  const observedArchiveSHA256 = await hashStableRegularFile(
    archivePath,
    initialTransfer.fileMetadata.get(r0BuildInputTransferPolicy.archiveFilename),
    "source-traceability build-input archive (R0)",
  );
  if (observedArchiveSHA256 !== expectedArchiveSHA256) {
    throw new Error(
      "The source-traceability build-input archive checksum (R0) is invalid",
    );
  }
  const stagingDirectory = await realpath(
    await mkdtemp(resolve(runnerTemp, ".vibermate-r0-input-stage-")),
  );
  await chmod(stagingDirectory, 0o700);
  await requireCanonicalBoundaryDirectory(
    stagingDirectory,
    "source-traceability build-input staging directory (R0)",
  );
  const stagingInputRoot = resolve(stagingDirectory, "input");
  let published = false;
  let stagedRootMetadata;
  try {
    const restored = await extractMacOSApplicationArchive(
      archivePath,
      stagingInputRoot,
    );
    if (restored.archiveSHA256 !== expectedArchiveSHA256) {
      throw new Error(
        "The source-traceability build-input archive (R0) changed while it was extracted",
      );
    }
    const inspected = await inspectR0BuildInputTree(stagingInputRoot);
    if (inspected.inputTreeSHA256 !== restored.applicationTreeSHA256) {
      throw new Error(
        "The restored source-traceability build-input tree (R0) does not match its archive",
      );
    }
    const finalTransfer = await inspectTransferInventory(downloadDirectory);
    requireUnchangedInventory(initialTransfer, finalTransfer);
    await requireAbsent(
      restoredInputRoot,
      "The restored source-traceability build-input root (R0)",
    );
    stagedRootMetadata = await lstat(stagingInputRoot);
    await rename(stagingInputRoot, restoredInputRoot);
    published = true;
    const publishedMetadata = await lstat(restoredInputRoot);
    if (
      !publishedMetadata.isDirectory() ||
      !sameIdentity(stagedRootMetadata, publishedMetadata) ||
      permissionMode(stagedRootMetadata) !== permissionMode(publishedMetadata)
    ) {
      throw new Error(
        "The restored source-traceability build-input root (R0) changed while it was published",
      );
    }
    const publishedTree = await inspectR0BuildInputTree(restoredInputRoot);
    if (publishedTree.inputTreeSHA256 !== inspected.inputTreeSHA256) {
      throw new Error(
        "The published source-traceability build-input tree (R0) changed after validation",
      );
    }
    return Object.freeze({
      archiveSHA256: restored.archiveSHA256,
      downloadDirectory,
      inputTreeSHA256: publishedTree.inputTreeSHA256,
      restoredInputRoot,
    });
  } catch (error) {
    if (published && stagedRootMetadata !== undefined) {
      try {
        const current = await lstat(restoredInputRoot);
        if (current.isDirectory() && sameIdentity(current, stagedRootMetadata)) {
          await rm(restoredInputRoot, { recursive: true, force: true });
        }
      } catch (cleanupError) {
        if (cleanupError?.code !== "ENOENT") {
          throw new AggregateError(
            [error, cleanupError],
            "Could not clean a rejected source-traceability build-input publication (R0)",
          );
        }
      }
    }
    throw error;
  } finally {
    await rm(stagingDirectory, { recursive: true, force: true });
  }
}
