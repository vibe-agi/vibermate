import { createHash } from "node:crypto";
import { constants } from "node:fs";
import { chmod, lstat, mkdir, open, realpath } from "node:fs/promises";
import { dirname, resolve, sep } from "node:path";
import { TextDecoder } from "node:util";
import {
  parseClosedJSONObject,
  treeLedgerSHA256,
  validateTreeLedgerEquality,
} from "./macos-distribution-policy.mjs";
import { applicationTreeLedger } from "./verify-macos-signed-candidate.mjs";

const magic = Buffer.from("VIBERMATE-APP-ARCHIVE-V1\n", "ascii");
const sha256Pattern = /^[0-9a-f]{64}$/u;
const maximumHeaderBytes = 16 << 10;
const maximumEntries = 8192;
const maximumFileBytes = 1 << 30;
const maximumTotalBytes = 2 * (1 << 30);
const maximumArchiveBytes =
  maximumTotalBytes + ((maximumEntries + 2) * (maximumHeaderBytes + 4)) + magic.length;
const maximumPathBytes = 4 << 10;
const decoder = new TextDecoder("utf-8", { fatal: true });
const portableComponentPattern = /^[A-Za-z0-9._+@(), -]+$/u;

function sameIdentity(left, right) {
  return left.dev === right.dev && left.ino === right.ino;
}

function sameRegularFile(left, right) {
  return (
    left.isFile() &&
    right.isFile() &&
    sameIdentity(left, right) &&
    left.size === right.size &&
    (left.mode & 0o7777) === (right.mode & 0o7777) &&
    left.ctimeMs === right.ctimeMs &&
    left.mtimeMs === right.mtimeMs &&
    left.nlink === 1 &&
    right.nlink === 1
  );
}

function exactKeys(value, expected) {
  const actual = Object.keys(value).sort();
  const wanted = [...expected].sort();
  return (
    actual.length === wanted.length &&
    actual.every((name, index) => name === wanted[index])
  );
}

function comparePaths(left, right) {
  return Buffer.compare(Buffer.from(left, "utf8"), Buffer.from(right, "utf8"));
}

function validateArchivePath(path, seenPortablePaths) {
  if (
    typeof path !== "string" ||
    (typeof path.isWellFormed === "function" && !path.isWellFormed()) ||
    path.normalize("NFC") !== path ||
    Buffer.byteLength(path, "utf8") === 0 ||
    Buffer.byteLength(path, "utf8") > maximumPathBytes ||
    path.startsWith("/") ||
    /[\0-\x1f\x7f\\]/u.test(path)
  ) {
    throw new Error("The application archive contains an invalid path");
  }
  const components = path.split("/");
  if (
    components[0] !== "ViberMate.app" ||
    components.some(
      (component) =>
        component.length === 0 ||
        component === "." ||
        component === ".." ||
        component.trim() !== component ||
        !portableComponentPattern.test(component) ||
        Buffer.byteLength(component, "utf8") > 255,
    )
  ) {
    throw new Error("The application archive path escapes its fixed root");
  }
  const portablePath = path.toLocaleLowerCase("en-US");
  if (seenPortablePaths.has(portablePath)) {
    throw new Error("The application archive contains a duplicate path alias");
  }
  seenPortablePaths.add(portablePath);
}

function validateMode(mode, type) {
  if (
    !Number.isInteger(mode) ||
    mode < 0 ||
    mode > 0o777 ||
    (mode & 0o022) !== 0 ||
    (type === "directory" && (mode & 0o700) !== 0o700) ||
    (type === "file" && (mode & 0o400) !== 0o400)
  ) {
    throw new Error("The application archive contains an invalid mode");
  }
}

function archivePathFromLedger(path) {
  return path === "." ? "ViberMate.app" : `ViberMate.app/${path}`;
}

function ledgerPathFromArchive(path) {
  return path === "ViberMate.app" ? "." : path.slice("ViberMate.app/".length);
}

function entryHeader(entry) {
  if (entry.type === "directory") {
    return {
      mode: entry.mode,
      path: archivePathFromLedger(entry.path),
      size: 0,
      type: "directory",
    };
  }
  return {
    mode: entry.mode,
    path: archivePathFromLedger(entry.path),
    sha256: entry.sha256,
    size: entry.size,
    type: "file",
  };
}

async function writeAll(handle, buffer, position) {
  let offset = 0;
  while (offset < buffer.length) {
    const { bytesWritten } = await handle.write(
      buffer,
      offset,
      buffer.length - offset,
      position + offset,
    );
    if (bytesWritten === 0) {
      throw new Error("The application archive write made no progress");
    }
    offset += bytesWritten;
  }
  return position + buffer.length;
}

async function writeHeader(handle, header, position) {
  const source = Buffer.from(JSON.stringify(header), "utf8");
  if (source.length === 0 || source.length > maximumHeaderBytes) {
    throw new Error("The application archive header exceeds its bound");
  }
  const length = Buffer.alloc(4);
  length.writeUInt32BE(source.length);
  let next = await writeAll(handle, length, position);
  next = await writeAll(handle, source, next);
  return next;
}

async function streamFileIntoArchive(sourcePath, destination, position, expected) {
  const pathInfo = await lstat(sourcePath);
  if (
    !pathInfo.isFile() ||
    pathInfo.isSymbolicLink() ||
    pathInfo.nlink !== 1 ||
    pathInfo.size !== expected.size ||
    (pathInfo.mode & 0o7777) !== expected.mode
  ) {
    throw new Error("An application file changed before it was archived");
  }
  const source = await open(
    sourcePath,
    constants.O_RDONLY | constants.O_NOFOLLOW,
  );
  const hash = createHash("sha256");
  let copied = 0;
  const buffer = Buffer.allocUnsafe(1 << 20);
  try {
    const openedInfo = await source.stat();
    if (!sameRegularFile(pathInfo, openedInfo)) {
      throw new Error("An application file changed while it was opened");
    }
    while (copied < expected.size) {
      const wanted = Math.min(buffer.length, expected.size - copied);
      const { bytesRead } = await source.read(buffer, 0, wanted, copied);
      if (bytesRead === 0) {
        throw new Error("An application file changed while it was archived");
      }
      hash.update(buffer.subarray(0, bytesRead));
      position = await writeAll(
        destination,
        buffer.subarray(0, bytesRead),
        position,
      );
      copied += bytesRead;
    }
    const probe = Buffer.alloc(1);
    if ((await source.read(probe, 0, 1, copied)).bytesRead !== 0) {
      throw new Error("An application file grew while it was archived");
    }
    const finalOpenedInfo = await source.stat();
    const finalPathInfo = await lstat(sourcePath);
    if (
      !sameRegularFile(openedInfo, finalOpenedInfo) ||
      !sameRegularFile(openedInfo, finalPathInfo)
    ) {
      throw new Error("An application file changed while it was archived");
    }
  } finally {
    await source.close();
  }
  if (hash.digest("hex") !== expected.sha256) {
    throw new Error("An application file changed while it was archived");
  }
  return position;
}

export async function createMacOSApplicationArchive(appPath, archivePath) {
  if ((await realpath(appPath)) !== appPath) {
    throw new Error("The source application path is not canonical");
  }
  const initialLedger = await applicationTreeLedger(appPath);
  const records = [...initialLedger]
    .sort((left, right) =>
      comparePaths(archivePathFromLedger(left.path), archivePathFromLedger(right.path)),
    );
  const destination = await open(
    archivePath,
    constants.O_CREAT |
      constants.O_EXCL |
      constants.O_RDWR |
      constants.O_NOFOLLOW,
    0o600,
  );
  let position = 0;
  let archiveSHA256;
  try {
    position = await writeAll(destination, magic, position);
    for (const entry of records) {
      const header = entryHeader(entry);
      position = await writeHeader(destination, header, position);
      if (entry.type === "file") {
        position = await streamFileIntoArchive(
          resolve(appPath, entry.path),
          destination,
          position,
          entry,
        );
      }
    }
    position = await writeHeader(
      destination,
      {
        entries: records.length,
        totalBytes: records
          .filter((entry) => entry.type === "file")
          .reduce((total, entry) => total + entry.size, 0),
        treeSHA256: treeLedgerSHA256(initialLedger),
        type: "end",
      },
      position,
    );
    if (position > maximumArchiveBytes) {
      throw new Error("The application archive exceeds its total bound");
    }
    await destination.sync();
    const openedMetadata = await destination.stat();
    if (
      !openedMetadata.isFile() ||
      openedMetadata.nlink !== 1 ||
      openedMetadata.size !== position
    ) {
      throw new Error("The created application archive is not a stable regular file");
    }
    archiveSHA256 = await hashOpenArchive(
      destination,
      openedMetadata.size,
      openedMetadata,
    );
    const finalPathMetadata = await lstat(archivePath);
    if (
      !sameRegularFile(openedMetadata, finalPathMetadata) ||
      (await realpath(archivePath)) !== archivePath
    ) {
      throw new Error("The created application archive path changed while it was verified");
    }
    validateTreeLedgerEquality(initialLedger, await applicationTreeLedger(appPath));
  } finally {
    await destination.close();
  }
  return Object.freeze({
    archiveSHA256,
    applicationTreeSHA256: treeLedgerSHA256(initialLedger),
  });
}

async function readExact(handle, length, position, archiveHash) {
  const buffer = Buffer.alloc(length);
  let offset = 0;
  while (offset < length) {
    const { bytesRead } = await handle.read(
      buffer,
      offset,
      length - offset,
      position + offset,
    );
    if (bytesRead === 0) {
      throw new Error("The application archive is truncated");
    }
    offset += bytesRead;
  }
  archiveHash?.update(buffer);
  return buffer;
}

async function readHeader(handle, position, archiveSize, archiveHash) {
  if (position + 4 > archiveSize) {
    throw new Error("The application archive is missing its end record");
  }
  const lengthBytes = await readExact(handle, 4, position, archiveHash);
  const length = lengthBytes.readUInt32BE();
  if (length === 0 || length > maximumHeaderBytes || position + 4 + length > archiveSize) {
    throw new Error("The application archive header is invalid");
  }
  const source = decoder.decode(
    await readExact(handle, length, position + 4, archiveHash),
  );
  return Object.freeze({
    nextPosition: position + 4 + length,
    source,
    value: parseClosedJSONObject(source, "Application archive header"),
  });
}

async function hashOpenArchive(handle, expectedSize, expectedInfo) {
  const hash = createHash("sha256");
  const buffer = Buffer.allocUnsafe(1 << 20);
  let position = 0;
  while (position < expectedSize) {
    const wanted = Math.min(buffer.length, expectedSize - position);
    const { bytesRead } = await handle.read(buffer, 0, wanted, position);
    if (bytesRead === 0) {
      throw new Error("The application archive changed while it was hashed");
    }
    hash.update(buffer.subarray(0, bytesRead));
    position += bytesRead;
  }
  if ((await handle.read(Buffer.alloc(1), 0, 1, position)).bytesRead !== 0) {
    throw new Error("The application archive grew while it was hashed");
  }
  const finalInfo = await handle.stat();
  if (!sameRegularFile(expectedInfo, finalInfo)) {
    throw new Error("The application archive changed while it was hashed");
  }
  return hash.digest("hex");
}

function validateEntryHeader(source, value, seenPortablePaths) {
  if (value.type === "directory") {
    if (
      !exactKeys(value, ["mode", "path", "size", "type"]) ||
      value.size !== 0 ||
      JSON.stringify({
        mode: value.mode,
        path: value.path,
        size: value.size,
        type: value.type,
      }) !== source
    ) {
      throw new Error("The application archive directory header is not canonical");
    }
  } else if (value.type === "file") {
    if (
      !exactKeys(value, ["mode", "path", "sha256", "size", "type"]) ||
      !sha256Pattern.test(value.sha256 ?? "") ||
      !Number.isSafeInteger(value.size) ||
      value.size < 0 ||
      value.size > maximumFileBytes ||
      JSON.stringify({
        mode: value.mode,
        path: value.path,
        sha256: value.sha256,
        size: value.size,
        type: value.type,
      }) !== source
    ) {
      throw new Error("The application archive file header is not canonical");
    }
  } else {
    throw new Error("The application archive contains a forbidden record type");
  }
  validateMode(value.mode, value.type);
  validateArchivePath(value.path, seenPortablePaths);
  return value;
}

async function extractFile(handle, position, header, path, archiveHash) {
  const destination = await open(
    path,
    constants.O_CREAT |
      constants.O_EXCL |
      constants.O_WRONLY |
      constants.O_NOFOLLOW,
    header.mode,
  );
  const hash = createHash("sha256");
  let copied = 0;
  const buffer = Buffer.allocUnsafe(1 << 20);
  try {
    while (copied < header.size) {
      const wanted = Math.min(buffer.length, header.size - copied);
      const { bytesRead } = await handle.read(buffer, 0, wanted, position + copied);
      if (bytesRead === 0) {
        throw new Error("The application archive file is truncated");
      }
      const chunk = buffer.subarray(0, bytesRead);
      archiveHash.update(chunk);
      hash.update(chunk);
      let written = 0;
      while (written < bytesRead) {
        const result = await destination.write(
          chunk,
          written,
          bytesRead - written,
          copied + written,
        );
        if (result.bytesWritten === 0) {
          throw new Error("The application extraction write made no progress");
        }
        written += result.bytesWritten;
      }
      copied += bytesRead;
    }
    await destination.chmod(header.mode);
    await destination.sync();
  } finally {
    await destination.close();
  }
  if (hash.digest("hex") !== header.sha256) {
    throw new Error("The application archive file digest is invalid");
  }
  return position + header.size;
}

export async function extractMacOSApplicationArchive(archivePath, appPath) {
  const archiveMetadata = await lstat(archivePath);
  if (
    archiveMetadata.isSymbolicLink() ||
    !archiveMetadata.isFile() ||
    archiveMetadata.nlink !== 1 ||
    archiveMetadata.size <= magic.length ||
    archiveMetadata.size > maximumArchiveBytes ||
    (await realpath(archivePath)) !== archivePath
  ) {
    throw new Error("The application archive is not a canonical bounded file");
  }
  try {
    await lstat(appPath);
    throw new Error("The application archive destination already exists");
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
  const parent = dirname(appPath);
  await mkdir(parent, { recursive: true, mode: 0o700 });
  if ((await realpath(parent)) !== parent) {
    throw new Error("The application archive destination parent is not canonical");
  }

  const source = await open(archivePath, constants.O_RDONLY | constants.O_NOFOLLOW);
  let position = magic.length;
  let totalBytes = 0;
  let entryCount = 0;
  let lastPath;
  const seenDirectories = new Set();
  const seenPortablePaths = new Set();
  const directoryModes = [];
  let endRecord;
  let archiveSHA256;
  try {
    const openedMetadata = await source.stat();
    if (!sameRegularFile(archiveMetadata, openedMetadata)) {
      throw new Error("The application archive changed while it was opened");
    }
    const beforeSHA256 = await hashOpenArchive(
      source,
      openedMetadata.size,
      openedMetadata,
    );
    const parsedArchiveHash = createHash("sha256");
    if (
      !(await readExact(source, magic.length, 0, parsedArchiveHash)).equals(magic)
    ) {
      throw new Error("The application archive magic is invalid");
    }
    while (position < archiveMetadata.size) {
      const decoded = await readHeader(
        source,
        position,
        archiveMetadata.size,
        parsedArchiveHash,
      );
      position = decoded.nextPosition;
      if (decoded.value.type === "end") {
        if (
          !exactKeys(decoded.value, ["entries", "totalBytes", "treeSHA256", "type"]) ||
          !Number.isSafeInteger(decoded.value.entries) ||
          !Number.isSafeInteger(decoded.value.totalBytes) ||
          !sha256Pattern.test(decoded.value.treeSHA256 ?? "") ||
          JSON.stringify({
            entries: decoded.value.entries,
            totalBytes: decoded.value.totalBytes,
            treeSHA256: decoded.value.treeSHA256,
            type: decoded.value.type,
          }) !== decoded.source ||
          decoded.value.entries !== entryCount ||
          decoded.value.totalBytes !== totalBytes ||
          position !== archiveMetadata.size
        ) {
          throw new Error("The application archive end record is invalid");
        }
        endRecord = decoded.value;
        break;
      }
      if (entryCount >= maximumEntries) {
        throw new Error("The application archive has too many entries");
      }
      const header = validateEntryHeader(
        decoded.source,
        decoded.value,
        seenPortablePaths,
      );
      if (lastPath !== undefined && comparePaths(lastPath, header.path) >= 0) {
        throw new Error("The application archive paths are not strictly ordered");
      }
      if (entryCount === 0 && (header.path !== "ViberMate.app" || header.type !== "directory")) {
        throw new Error("The application archive does not begin with its fixed root");
      }
      const parentPath = header.path.includes("/")
        ? header.path.slice(0, header.path.lastIndexOf("/"))
        : undefined;
      if (parentPath !== undefined && !seenDirectories.has(parentPath)) {
        throw new Error("The application archive entry precedes its parent directory");
      }
      const relativePath = ledgerPathFromArchive(header.path).split("/").join(sep);
      const outputPath = relativePath === "." ? appPath : resolve(appPath, relativePath);
      if (outputPath !== appPath && !outputPath.startsWith(`${appPath}${sep}`)) {
        throw new Error("The application archive output path escapes its root");
      }
      if (header.type === "directory") {
        await mkdir(outputPath, { mode: 0o700 });
        await chmod(outputPath, 0o700);
        directoryModes.push({
          depth: header.path.split("/").length,
          mode: header.mode,
          path: outputPath,
        });
        seenDirectories.add(header.path);
      } else {
        totalBytes += header.size;
        if (totalBytes > maximumTotalBytes || position + header.size > archiveMetadata.size) {
          throw new Error("The application archive exceeds its expanded-size bound");
        }
        position = await extractFile(
          source,
          position,
          header,
          outputPath,
          parsedArchiveHash,
        );
      }
      entryCount += 1;
      lastPath = header.path;
    }
    archiveSHA256 = await hashOpenArchive(
      source,
      openedMetadata.size,
      openedMetadata,
    );
    const parsedSHA256 = parsedArchiveHash.digest("hex");
    if (
      archiveSHA256 !== beforeSHA256 ||
      archiveSHA256 !== parsedSHA256
    ) {
      throw new Error("The application archive changed while it was extracted");
    }
    const finalPathMetadata = await lstat(archivePath);
    if (!sameRegularFile(openedMetadata, finalPathMetadata)) {
      throw new Error("The application archive path changed while it was extracted");
    }
  } finally {
    await source.close();
  }
  if (endRecord === undefined) {
    throw new Error("The application archive is missing its end record");
  }
  directoryModes.sort((left, right) => right.depth - left.depth);
  for (const directory of directoryModes) {
    await chmod(directory.path, directory.mode);
  }
  const ledger = await applicationTreeLedger(appPath);
  if (treeLedgerSHA256(ledger) !== endRecord.treeSHA256) {
    throw new Error("The extracted application tree does not match the archive ledger");
  }
  return Object.freeze({
    applicationLedger: ledger,
    applicationTreeSHA256: endRecord.treeSHA256,
    archiveSHA256,
  });
}
