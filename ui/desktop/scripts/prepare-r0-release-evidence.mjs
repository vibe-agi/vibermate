import { createHash, randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import {
  closeSync,
  constants,
  fstatSync,
  lstatSync,
  openSync,
  readFileSync,
} from "node:fs";
import {
  chmod,
  lstat,
  mkdir,
  mkdtemp,
  open,
  opendir,
  realpath,
  rename,
  rm,
  rmdir,
} from "node:fs/promises";
import { dirname, isAbsolute, join, normalize, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDirectory = dirname(fileURLToPath(import.meta.url));
const defaultRepositoryRoot = resolve(scriptDirectory, "../../..");

const releaseSchema = "vibermate.release/v1";
const desktopBuildSchema = "vibermate.desktop-build/v2";
const appTreeLedgerSchema = "vibermate.app-tree-ledger/v1";
const knownIssuesSchema = "vibermate.known-issues/v1";
const unsignedPayloadName = "unsigned-payload";
const fullRevision = /^[0-9a-f]{40}$/u;
const digestPattern = /^[0-9a-f]{64}$/u;
const semanticVersion =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/u;
const spdxIDPattern = /^SPDXRef-[A-Za-z0-9.-]+$/u;
const identifierPattern = /^[a-z0-9][a-z0-9._-]{0,127}$/u;
const controlCharacterPattern = /[\u0000-\u001f\u007f]/u;
const portableRelativePathPattern = /^[A-Za-z0-9._/-]+$/u;
const syftVersion = "1.44.0";
const syftSourceName = "vibermate-unsigned-payload";
const admittedSyftDarwinArm64ExecutableSHA256 =
  "09aa35f766d0ea34b2e64d82b9c6e2e315ff74019bd7cebe7f3bf6057ef6c62f";
const syftRootSPDXID =
  "SPDXRef-DocumentRoot-Directory-vibermate-unsigned-payload";

const configurationNames = Object.freeze([
  "go.mod",
  "go.sum",
  "rust-toolchain.toml",
  "ui/desktop/package.json",
  "ui/desktop/pnpm-lock.yaml",
  "ui/desktop/src-tauri/Cargo.toml",
  "ui/desktop/src-tauri/Cargo.lock",
  "ui/desktop/src-tauri/tauri.conf.json",
]);

export const r0EvidenceLimits = Object.freeze({
  artifactDocumentBytes: 1 << 20,
  desktopBuildManifestBytes: 128 << 10,
  knownIssuesBytes: 1 << 20,
  ledgerBytes: 16 << 20,
  payloadEntries: 65536,
  payloadFileBytes: 2 * 1024 * 1024 * 1024,
  payloadTotalBytes: 16 * 1024 * 1024 * 1024,
  spdxDocumentBytes: 32 << 20,
  syftInputBytes: 32 << 20,
});

const metadataNames = Object.freeze({
  ledger: "app-tree.json",
  desktopBuild: "desktop-build-manifest.json",
  sbom: "sbom.spdx.json",
  knownIssues: "known-issues.json",
  spec: "release-spec.json",
});

function cleanAbsolutePath(label, value) {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    !isAbsolute(value) ||
    normalize(value) !== value ||
    controlCharacterPattern.test(value) ||
    (typeof value.isWellFormed === "function" && !value.isWellFormed())
  ) {
    throw new Error(`${label} must be a clean absolute UTF-8 path`);
  }
  return value;
}

export function parseR0EvidenceArguments(arguments_) {
  if (!Array.isArray(arguments_)) {
    throw new Error("Source-traceability evidence (R0) arguments must be an array");
  }
  const values = new Map();
  const allowed = new Set([
    "artifact-root",
    "expected-revision",
    "input-root",
    "source-root",
    "syft-bin",
  ]);
  for (const argument of arguments_) {
    if (typeof argument !== "string") {
      throw new Error("Source-traceability evidence (R0) arguments must be strings");
    }
    const match = argument.match(/^--([a-z-]+)=(.*)$/u);
    if (match === null || !allowed.has(match[1])) {
      throw new Error(
        "Source-traceability evidence (R0) preparation accepts only --artifact-root=<absolute>, --expected-revision=<40hex>, --input-root=<absolute>, --source-root=<absolute>, and --syft-bin=<absolute>",
      );
    }
    if (values.has(match[1])) {
      throw new Error(
        `Source-traceability evidence (R0) argument --${match[1]} was supplied twice`,
      );
    }
    values.set(match[1], match[2]);
  }
  if (values.size !== allowed.size) {
    throw new Error(
      "Source-traceability evidence (R0) preparation requires exactly --artifact-root, --expected-revision, --input-root, --source-root, and --syft-bin",
    );
  }
  const expectedRevision = values.get("expected-revision");
  if (!fullRevision.test(expectedRevision)) {
    throw new Error("--expected-revision must be 40 lowercase hexadecimal characters");
  }
  return Object.freeze({
    artifactRoot: cleanAbsolutePath(
      "--artifact-root",
      values.get("artifact-root"),
    ),
    expectedRevision,
    inputRoot: cleanAbsolutePath("--input-root", values.get("input-root")),
    repositoryRoot: cleanAbsolutePath(
      "--source-root",
      values.get("source-root"),
    ),
    syftBinaryPath: cleanAbsolutePath(
      "--syft-bin",
      values.get("syft-bin"),
    ),
  });
}

function exactKeys(value, expected, label) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
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

function metadataText(value, maximum, label, allowNewline = false) {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    Buffer.byteLength(value, "utf8") > maximum ||
    value.trim() !== value ||
    (typeof value.isWellFormed === "function" && !value.isWellFormed())
  ) {
    throw new Error(`${label} is empty, padded, invalid, or too long`);
  }
  for (const character of value) {
    const code = character.codePointAt(0);
    if (character === "\n" && allowNewline) {
      continue;
    }
    if (code < 0x20 || code === 0x7f) {
      throw new Error(`${label} contains a control character`);
    }
  }
  return value;
}

function canonicalJSON(value) {
  return Buffer.from(`${JSON.stringify(value, null, 2)}\n`, "utf8");
}

function digestBytes(payload) {
  return createHash("sha256").update(payload).digest("hex");
}

function sameIdentity(left, right) {
  return left.dev === right.dev && left.ino === right.ino;
}

function permissionMode(info) {
  return info.mode & 0o7777;
}

function rejectPrivilegedMode(info, label) {
  if ((permissionMode(info) & 0o7000) !== 0) {
    throw new Error(`${label} has privileged mode bits`);
  }
}

function validateRelativePath(value, label) {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    Buffer.byteLength(value, "utf8") > 1024 ||
    value.includes("\\") ||
    value.includes(":") ||
    value.includes("\0") ||
    value.startsWith("/") ||
    !portableRelativePathPattern.test(value) ||
    value.split("/").some((component) => component === "" || component === "." || component === "..") ||
    (typeof value.isWellFormed === "function" && !value.isWellFormed())
  ) {
    throw new Error(`${label} is not a clean relative path`);
  }
}

async function inspectAbsoluteDirectory(path, label) {
  cleanAbsolutePath(label, path);
  const canonical = await realpath(path);
  if (canonical !== path) {
    throw new Error(`${label} contains a symbolic-link component`);
  }
  const info = await lstat(path);
  if (info.isSymbolicLink() || !info.isDirectory()) {
    throw new Error(`${label} must be a non-symlink directory`);
  }
  rejectPrivilegedMode(info, label);
  return info;
}

async function inspectRelativePath(root, relativePath, label) {
  validateRelativePath(relativePath, label);
  let current = root;
  const components = relativePath.split("/");
  let info;
  for (let index = 0; index < components.length; index += 1) {
    current = join(current, components[index]);
    info = await lstat(current);
    if (info.isSymbolicLink()) {
      throw new Error(`${label} contains a symbolic link`);
    }
    rejectPrivilegedMode(info, label);
    if (index < components.length - 1 && !info.isDirectory()) {
      throw new Error(`${label} has a non-directory path component`);
    }
  }
  return { info, path: current };
}

async function openStableRegularFile(path, initialInfo, label) {
  if (
    !initialInfo.isFile() ||
    initialInfo.isSymbolicLink() ||
    initialInfo.nlink !== 1
  ) {
    throw new Error(`${label} must be a regular file without hard-link aliases`);
  }
  rejectPrivilegedMode(initialInfo, label);
  const handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  const openedInfo = await handle.stat();
  if (
    !openedInfo.isFile() ||
    openedInfo.nlink !== 1 ||
    !sameIdentity(initialInfo, openedInfo) ||
    openedInfo.size !== initialInfo.size ||
    permissionMode(openedInfo) !== permissionMode(initialInfo)
  ) {
    await handle.close();
    throw new Error(`${label} changed while it was opened`);
  }
  return { handle, openedInfo };
}

async function readHandleExactly(handle, size, label) {
  const chunks = [];
  const hash = createHash("sha256");
  const buffer = Buffer.allocUnsafe(Math.min(1 << 20, Math.max(1, size)));
  let position = 0;
  while (position < size) {
    const wanted = Math.min(buffer.length, size - position);
    const { bytesRead } = await handle.read(buffer, 0, wanted, position);
    if (bytesRead === 0) {
      throw new Error(`${label} became shorter while it was read`);
    }
    const chunk = Buffer.from(buffer.subarray(0, bytesRead));
    chunks.push(chunk);
    hash.update(chunk);
    position += bytesRead;
  }
  const extra = Buffer.allocUnsafe(1);
  const { bytesRead: extraBytes } = await handle.read(extra, 0, 1, size);
  if (extraBytes !== 0) {
    throw new Error(`${label} became longer while it was read`);
  }
  return {
    digest: hash.digest("hex"),
    payload: Buffer.concat(chunks, size),
  }
}

async function digestHandleExactly(handle, size, label) {
  const hash = createHash("sha256");
  const buffer = Buffer.allocUnsafe(Math.min(1 << 20, Math.max(1, size)));
  let position = 0;
  while (position < size) {
    const wanted = Math.min(buffer.length, size - position);
    const { bytesRead } = await handle.read(buffer, 0, wanted, position);
    if (bytesRead === 0) {
      throw new Error(`${label} became shorter while it was hashed`);
    }
    hash.update(buffer.subarray(0, bytesRead));
    position += bytesRead;
  }
  const extra = Buffer.allocUnsafe(1);
  const { bytesRead: extraBytes } = await handle.read(extra, 0, 1, size);
  if (extraBytes !== 0) {
    throw new Error(`${label} became longer while it was hashed`);
  }
  return hash.digest("hex");
}

async function readStableFile(
  path,
  initialInfo,
  label,
  maximumBytes,
  { nonempty = true } = {},
) {
  if (!initialInfo.isFile() || initialInfo.isSymbolicLink()) {
    throw new Error(`${label} must be a regular file`);
  }
  if (
    initialInfo.size < (nonempty ? 1 : 0) ||
    initialInfo.size > maximumBytes
  ) {
    throw new Error(`${label} has an invalid or excessive size`);
  }
  const { handle, openedInfo } = await openStableRegularFile(
    path,
    initialInfo,
    label,
  );
  try {
    const first = await readHandleExactly(handle, openedInfo.size, label);
    const secondDigest = await digestHandleExactly(
      handle,
      openedInfo.size,
      label,
    );
    const finalOpenInfo = await handle.stat();
    const finalPathInfo = await lstat(path);
    if (
      !sameIdentity(openedInfo, finalOpenInfo) ||
      !sameIdentity(openedInfo, finalPathInfo) ||
      finalOpenInfo.size !== openedInfo.size ||
      permissionMode(finalOpenInfo) !== permissionMode(openedInfo) ||
      first.digest !== secondDigest
    ) {
      throw new Error(`${label} changed while it was read`);
    }
    return Object.freeze({
      digest: first.digest,
      info: openedInfo,
      payload: first.payload,
    });
  } finally {
    await handle.close();
  }
}

async function copyStableRegularFile(
  sourcePath,
  initialInfo,
  destinationPath,
  destinationMode,
  label,
  limits,
  { executable = false, nonempty = true } = {},
) {
  if (!initialInfo.isFile() || initialInfo.isSymbolicLink()) {
    throw new Error(`${label} must be a regular file`);
  }
  if (
    initialInfo.size < (nonempty ? 1 : 0) ||
    initialInfo.size > limits.payloadFileBytes
  ) {
    throw new Error(`${label} has an invalid or excessive size`);
  }
  if (executable && (permissionMode(initialInfo) & 0o111) === 0) {
    throw new Error(`${label} must be executable`);
  }
  const { handle: source, openedInfo } = await openStableRegularFile(
    sourcePath,
    initialInfo,
    label,
  );
  let destination;
  try {
    destination = await open(
      destinationPath,
      constants.O_WRONLY |
        constants.O_CREAT |
        constants.O_EXCL |
        constants.O_NOFOLLOW,
      destinationMode,
    );
    const buffer = Buffer.allocUnsafe(
      Math.min(1 << 20, Math.max(1, openedInfo.size)),
    );
    const hash = createHash("sha256");
    let position = 0;
    while (position < openedInfo.size) {
      const wanted = Math.min(buffer.length, openedInfo.size - position);
      const { bytesRead } = await source.read(buffer, 0, wanted, position);
      if (bytesRead === 0) {
        throw new Error(`${label} became shorter while it was copied`);
      }
      let written = 0;
      while (written < bytesRead) {
        const result = await destination.write(
          buffer,
          written,
          bytesRead - written,
          position + written,
        );
        if (result.bytesWritten === 0) {
          throw new Error(`could not finish writing staged ${label}`);
        }
        written += result.bytesWritten;
      }
      hash.update(buffer.subarray(0, bytesRead));
      position += bytesRead;
    }
    const extra = Buffer.allocUnsafe(1);
    const { bytesRead: extraBytes } = await source.read(
      extra,
      0,
      1,
      openedInfo.size,
    );
    if (extraBytes !== 0) {
      throw new Error(`${label} became longer while it was copied`);
    }
    const secondDigest = await digestHandleExactly(
      source,
      openedInfo.size,
      label,
    );
    await destination.chmod(destinationMode);
    await destination.sync();
    const destinationInfo = await destination.stat();
    const finalSourceInfo = await source.stat();
    const finalSourcePathInfo = await lstat(sourcePath);
    if (
      !sameIdentity(openedInfo, finalSourceInfo) ||
      !sameIdentity(openedInfo, finalSourcePathInfo) ||
      finalSourceInfo.size !== openedInfo.size ||
      finalSourceInfo.nlink !== 1 ||
      permissionMode(finalSourceInfo) !== permissionMode(openedInfo) ||
      finalSourcePathInfo.size !== openedInfo.size ||
      finalSourcePathInfo.nlink !== 1 ||
      permissionMode(finalSourcePathInfo) !== permissionMode(openedInfo) ||
      !destinationInfo.isFile() ||
      destinationInfo.nlink !== 1 ||
      destinationInfo.size !== openedInfo.size ||
      permissionMode(destinationInfo) !== destinationMode ||
      hash.copy().digest("hex") !== secondDigest
    ) {
      throw new Error(`${label} changed while it was copied`);
    }
    return Object.freeze({
      digest: hash.digest("hex"),
      size: openedInfo.size,
    });
  } finally {
    await Promise.allSettled([
      source.close(),
      destination === undefined ? Promise.resolve() : destination.close(),
    ]);
  }
}

async function writeExclusiveFile(path, payload, mode, label, maximumBytes) {
  if (!Buffer.isBuffer(payload) || payload.length === 0 || payload.length > maximumBytes) {
    throw new Error(`${label} has an invalid or excessive encoded size`);
  }
  const handle = await open(
    path,
    constants.O_WRONLY |
      constants.O_CREAT |
      constants.O_EXCL |
      constants.O_NOFOLLOW,
    mode,
  );
  try {
    let position = 0;
    while (position < payload.length) {
      const { bytesWritten } = await handle.write(
        payload,
        position,
        payload.length - position,
        position,
      );
      if (bytesWritten === 0) {
        throw new Error(`could not finish writing ${label}`);
      }
      position += bytesWritten;
    }
    await handle.chmod(mode);
    await handle.sync();
    const info = await handle.stat();
    if (
      !info.isFile() ||
      info.size !== payload.length ||
      permissionMode(info) !== mode
    ) {
      throw new Error(`${label} was not staged exactly`);
    }
  } finally {
    await handle.close();
  }
  return Object.freeze({
    digest: digestBytes(payload),
    size: payload.length,
  });
}

function JSONDuplicateScanner(source, label) {
  let index = 0;
  const maximumDepth = 128;

  function skipWhitespace() {
    while (index < source.length && /[\t\n\r ]/u.test(source[index])) {
      index += 1;
    }
  }

  function stringToken() {
    if (source[index] !== '"') {
      throw new Error(`${label} contains malformed JSON`);
    }
    const start = index;
    index += 1;
    while (index < source.length) {
      const character = source[index];
      if (character === '"') {
        index += 1;
        try {
          return JSON.parse(source.slice(start, index));
        } catch {
          throw new Error(`${label} contains malformed JSON`);
        }
      }
      if (character === "\\") {
        index += 2;
      } else {
        index += 1;
      }
    }
    throw new Error(`${label} contains malformed JSON`);
  }

  function value(depth) {
    if (depth > maximumDepth) {
      throw new Error(`${label} exceeds the JSON nesting limit`);
    }
    skipWhitespace();
    if (source[index] === '"') {
      stringToken();
      return;
    }
    if (source[index] === "{") {
      index += 1;
      skipWhitespace();
      const members = new Set();
      if (source[index] === "}") {
        index += 1;
        return;
      }
      while (index < source.length) {
        const name = stringToken();
        if (members.has(name)) {
          throw new Error(`${label} contains duplicate member ${JSON.stringify(name)}`);
        }
        members.add(name);
        skipWhitespace();
        if (source[index] !== ":") {
          throw new Error(`${label} contains malformed JSON`);
        }
        index += 1;
        value(depth + 1);
        skipWhitespace();
        if (source[index] === "}") {
          index += 1;
          return;
        }
        if (source[index] !== ",") {
          throw new Error(`${label} contains malformed JSON`);
        }
        index += 1;
        skipWhitespace();
      }
      throw new Error(`${label} contains malformed JSON`);
    }
    if (source[index] === "[") {
      index += 1;
      skipWhitespace();
      if (source[index] === "]") {
        index += 1;
        return;
      }
      while (index < source.length) {
        value(depth + 1);
        skipWhitespace();
        if (source[index] === "]") {
          index += 1;
          return;
        }
        if (source[index] !== ",") {
          throw new Error(`${label} contains malformed JSON`);
        }
        index += 1;
      }
      throw new Error(`${label} contains malformed JSON`);
    }
    const start = index;
    while (
      index < source.length &&
      !/[\t\n\r ,\]}]/u.test(source[index])
    ) {
      index += 1;
    }
    if (start === index) {
      throw new Error(`${label} contains malformed JSON`);
    }
    try {
      JSON.parse(source.slice(start, index));
    } catch {
      throw new Error(`${label} contains malformed JSON`);
    }
  }

  value(0);
  skipWhitespace();
  if (index !== source.length) {
    throw new Error(`${label} contains trailing JSON`);
  }
}

function parseJSON(payload, label) {
  let source;
  try {
    source = new TextDecoder("utf-8", { fatal: true }).decode(payload);
  } catch {
    throw new Error(`${label} is not valid UTF-8`);
  }
  JSONDuplicateScanner(source, label);
  try {
    return JSON.parse(source);
  } catch {
    throw new Error(`${label} contains malformed JSON`);
  }
}

function validateDigestMap(value, names, label) {
  exactKeys(value, names, label);
  for (const name of names) {
    if (!digestPattern.test(value[name])) {
      throw new Error(`${label} contains an invalid digest for ${name}`);
    }
  }
}

function validateDesktopBuildManifest(value, expectedRevision, commitTime) {
  exactKeys(
    value,
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
  exactKeys(
    value.source,
    ["vcs", "revision", "commitTime", "dirty"],
    "Desktop build manifest source",
  );
  exactKeys(
    value.profiles,
    ["desktop", "sidecars", "target"],
    "Desktop build manifest profiles",
  );
  exactKeys(
    value.toolchains,
    ["go", "node", "rustc", "cargo", "pnpm", "tauri"],
    "Desktop build manifest toolchains",
  );
  if (
    value.schema !== desktopBuildSchema ||
    value.source.vcs !== "git" ||
    value.source.revision !== expectedRevision ||
    value.source.commitTime !== commitTime ||
    value.source.dirty !== false
  ) {
    throw new Error("Desktop build manifest does not bind the exact clean Git source");
  }
  if (
    value.profiles.desktop !== "release" ||
    value.profiles.sidecars !== "distribution" ||
    value.profiles.target !== "universal-apple-darwin"
  ) {
    throw new Error("Desktop build manifest is not the Universal distribution build");
  }
  for (const [name, version] of Object.entries(value.toolchains)) {
    metadataText(version, 4096, `Desktop build toolchain ${name}`, true);
  }
  validateDigestMap(
    value.configurationSHA256,
    configurationNames,
    "Desktop configurationSHA256",
  );
  validateDigestMap(
    value.sidecarSHA256,
    ["vibermate", "vibermated"],
    "Desktop sidecarSHA256",
  );
  return value;
}

function normalizedSPDXPackage(name, version) {
  const hash = digestBytes(Buffer.from(`${name}\0${version}`, "utf8")).slice(0, 20);
  const slug = `${name}-${version}`
    .normalize("NFKD")
    .replace(/[^A-Za-z0-9.-]+/gu, "-")
    .replace(/^-+|-+$/gu, "")
    .slice(0, 72) || "package";
  return {
    SPDXID: `SPDXRef-Package-${slug}-${hash}`,
    name,
    versionInfo: version,
    downloadLocation: "NOASSERTION",
    filesAnalyzed: false,
    licenseConcluded: "NOASSERTION",
    licenseDeclared: "NOASSERTION",
    copyrightText: "NOASSERTION",
  };
}

function compareText(left, right) {
  return left < right ? -1 : left > right ? 1 : 0;
}

function validateSyftScanBinding(input, ledger, version) {
  if (
    input.creationInfo === null ||
    typeof input.creationInfo !== "object" ||
    Array.isArray(input.creationInfo) ||
    JSON.stringify(input.creationInfo.creators) !==
      JSON.stringify(["Organization: Anchore, Inc", `Tool: syft-${syftVersion}`])
  ) {
    throw new Error("Syft SPDX creators do not match the admitted Syft release");
  }
  if (
    !Array.isArray(input.relationships) ||
    input.relationships.length > r0EvidenceLimits.payloadEntries * 8
  ) {
    throw new Error("Syft SPDX relationships are missing or exceed their bound");
  }
  const describes = input.relationships.filter(
    (relationship) =>
      relationship?.spdxElementId === "SPDXRef-DOCUMENT" &&
      relationship?.relationshipType === "DESCRIBES",
  );
  if (describes.length !== 1) {
    throw new Error("Syft SPDX must describe exactly one payload root");
  }
  exactKeys(
    describes[0],
    ["spdxElementId", "relatedSpdxElement", "relationshipType"],
    "Syft SPDX DESCRIBES relationship",
  );
  if (describes[0].relatedSpdxElement !== syftRootSPDXID) {
    throw new Error("Syft SPDX describes the wrong payload root");
  }

  const packageIDs = new Set();
  let rootPackage;
  for (let index = 0; index < input.packages.length; index += 1) {
    const pkg = input.packages[index];
    if (
      pkg === null ||
      typeof pkg !== "object" ||
      Array.isArray(pkg) ||
      !spdxIDPattern.test(pkg.SPDXID ?? "") ||
      packageIDs.has(pkg.SPDXID)
    ) {
      throw new Error(`Syft SPDX packages[${index}] has an invalid or duplicate SPDXID`);
    }
    packageIDs.add(pkg.SPDXID);
    if (pkg.SPDXID === syftRootSPDXID) {
      rootPackage = pkg;
    }
  }
  if (
    rootPackage === undefined ||
    rootPackage.name !== syftSourceName ||
    rootPackage.versionInfo !== version ||
    rootPackage.primaryPackagePurpose !== "FILE" ||
    rootPackage.filesAnalyzed !== false
  ) {
    throw new Error("Syft SPDX payload root metadata is invalid");
  }

  if (
    !Array.isArray(input.files) ||
    input.files.length !== ledger.entries.length ||
    input.files.length > r0EvidenceLimits.payloadEntries
  ) {
    throw new Error("Syft SPDX file inventory does not match the payload ledger size");
  }
  const ledgerByPath = new Map(
    ledger.entries.map((entry) => [entry.path, entry]),
  );
  const observedPaths = new Set();
  const fileIDs = new Set();
  for (let index = 0; index < input.files.length; index += 1) {
    const file = input.files[index];
    if (
      file === null ||
      typeof file !== "object" ||
      Array.isArray(file) ||
      !spdxIDPattern.test(file.SPDXID ?? "") ||
      fileIDs.has(file.SPDXID) ||
      typeof file.fileName !== "string"
    ) {
      throw new Error(`Syft SPDX files[${index}] is invalid`);
    }
    fileIDs.add(file.SPDXID);
    const ledgerPath = file.fileName === "" ? "." : file.fileName;
    if (ledgerPath !== ".") {
      validateRelativePath(ledgerPath, `Syft SPDX files[${index}].fileName`);
    }
    const expected = ledgerByPath.get(ledgerPath);
    if (expected === undefined || observedPaths.has(ledgerPath)) {
      throw new Error("Syft SPDX contains an unknown or duplicate payload path");
    }
    observedPaths.add(ledgerPath);
    if (!Array.isArray(file.checksums) || file.checksums.length !== 1) {
      throw new Error("Syft SPDX payload entry has an unexpected checksum set");
    }
    const checksum = file.checksums[0];
    exactKeys(
      checksum,
      ["algorithm", "checksumValue"],
      `Syft SPDX files[${index}] checksum`,
    );
    if (expected.type === "directory") {
      if (
        JSON.stringify(file.fileTypes) !== JSON.stringify(["OTHER"]) ||
        checksum.algorithm !== "SHA1" ||
        checksum.checksumValue !== "0".repeat(40)
      ) {
        throw new Error("Syft SPDX directory sentinel does not match the payload ledger");
      }
    } else if (
      expected.type !== "file" ||
      checksum.algorithm !== "SHA256" ||
      checksum.checksumValue !== expected.sha256
    ) {
      throw new Error("Syft SPDX file digest does not match the payload ledger");
    }
  }
  if (observedPaths.size !== ledgerByPath.size) {
    throw new Error("Syft SPDX omits one or more payload ledger paths");
  }
}

function normalizeSyftSPDX(
  input,
  { commitTime, ledger, payloadLedgerSHA256, revision, version },
) {
  if (input === null || typeof input !== "object" || Array.isArray(input)) {
    throw new Error("Syft SPDX document must be an object");
  }
  if (input.spdxVersion !== "SPDX-2.3" || !Array.isArray(input.packages)) {
    throw new Error("Syft input must be an SPDX 2.3 JSON document with packages");
  }
  if (input.packages.length > r0EvidenceLimits.payloadEntries - 1) {
    throw new Error("Syft SPDX package inventory exceeds the package limit");
  }
  if (!digestPattern.test(payloadLedgerSHA256 ?? "")) {
    throw new Error("Payload ledger digest is invalid during SPDX normalization");
  }
  validateSyftScanBinding(input, ledger, version);
  const discovered = [];
  const pairs = new Set();
  for (let index = 0; index < input.packages.length; index += 1) {
    const pkg = input.packages[index];
    if (pkg === null || typeof pkg !== "object" || Array.isArray(pkg)) {
      throw new Error(`Syft SPDX packages[${index}] must be an object`);
    }
    const name = metadataText(pkg.name, 256, `Syft SPDX packages[${index}].name`);
    if (pkg.SPDXID === syftRootSPDXID) {
      continue;
    }
    const packageVersion =
      pkg.versionInfo === undefined
        ? "NOASSERTION"
        : metadataText(
            pkg.versionInfo,
            256,
            `Syft SPDX packages[${index}].versionInfo`,
    );
    const key = `${name}\0${packageVersion}`;
    if (pairs.has(key)) {
      continue;
    }
    pairs.add(key);
    if (name === "vibermate") {
      if (packageVersion !== version) {
        throw new Error("Syft SPDX contains a conflicting VibeMate package version");
      }
      continue;
    }
    discovered.push({ name, version: packageVersion });
  }
  discovered.push({ name: "vibermate", version });
  discovered.sort(
    (left, right) =>
      compareText(left.name, right.name) || compareText(left.version, right.version),
  );
  const packages = discovered.map(({ name, version: packageVersion }) =>
    normalizedSPDXPackage(name, packageVersion),
  );
  const identifiers = new Set();
  for (const pkg of packages) {
    if (!spdxIDPattern.test(pkg.SPDXID) || identifiers.has(pkg.SPDXID)) {
      throw new Error("Normalized SPDX package identities are invalid or duplicated");
    }
    identifiers.add(pkg.SPDXID);
  }
  return {
    SPDXID: "SPDXRef-DOCUMENT",
    creationInfo: {
      created: commitTime,
      creators: [
        `Tool: syft-${syftVersion}`,
        "Tool: vibermate-r0-evidence-preparer",
      ],
    },
    dataLicense: "CC0-1.0",
    name: "vibermate-release-sbom",
    spdxVersion: "SPDX-2.3",
    documentNamespace: `https://vibermate.example.invalid/spdx/${revision}/${payloadLedgerSHA256}`,
    comment: `vibermate.release version=${version} commit=${revision} payloadLedgerSHA256=${payloadLedgerSHA256}`,
    packages,
  };
}

function directoryEntrySignature(entry) {
  let type = "other";
  if (entry.isDirectory()) {
    type = "directory";
  } else if (entry.isFile()) {
    type = "file";
  } else if (entry.isSymbolicLink()) {
    type = "symlink";
  }
  return `${entry.name}\0${type}`;
}

async function stableDirectoryEntries(path, label, limit) {
  const entries = [];
  const directory = await opendir(path);
  try {
    while (true) {
      const entry = await directory.read();
      if (entry === null) {
        break;
      }
      if (entries.length >= limit) {
        throw new Error(`${label} exceeds the entry limit`);
      }
      entries.push(entry);
    }
  } finally {
    await directory.close();
  }
  entries.sort((left, right) => compareText(left.name, right.name));
  const seen = new Set();
  for (const entry of entries) {
    metadataText(entry.name, 255, `${label} entry name`);
    if (entry.name.includes("/") || entry.name === "." || entry.name === "..") {
      throw new Error(`${label} contains an invalid entry name`);
    }
    if (seen.has(entry.name)) {
      throw new Error(`${label} contains duplicate entry name ${entry.name}`);
    }
    seen.add(entry.name);
  }
  return entries;
}

async function stageDistTree(sourceRoot, destinationRoot, limits) {
  const state = {
    entries: 0,
    files: 0,
    totalBytes: 0,
  };

  async function visit(sourcePath, destinationPath, relativePath) {
    validateRelativePath(relativePath, "dist payload path");
    const initialInfo = await lstat(sourcePath);
    if (initialInfo.isSymbolicLink()) {
      throw new Error(`dist source ${relativePath} is a symbolic link`);
    }
    rejectPrivilegedMode(initialInfo, `dist source ${relativePath}`);
    state.entries += 1;
    if (state.entries > limits.payloadEntries - 6) {
      throw new Error("dist source exceeds the payload entry limit");
    }
    if (initialInfo.isDirectory()) {
      await mkdir(destinationPath, { mode: 0o755 });
      await chmod(destinationPath, 0o755);
      const before = await stableDirectoryEntries(
        sourcePath,
        `dist source ${relativePath}`,
        limits.payloadEntries,
      );
      for (const child of before) {
        const childRelative = `${relativePath}/${child.name}`;
        await visit(
          join(sourcePath, child.name),
          join(destinationPath, child.name),
          childRelative,
        );
      }
      const finalInfo = await lstat(sourcePath);
      const after = await stableDirectoryEntries(
        sourcePath,
        `dist source ${relativePath}`,
        limits.payloadEntries,
      );
      if (
        !sameIdentity(initialInfo, finalInfo) ||
        permissionMode(initialInfo) !== permissionMode(finalInfo) ||
        before.length !== after.length ||
        before.some(
          (entry, index) =>
            directoryEntrySignature(entry) !== directoryEntrySignature(after[index]),
        )
      ) {
        throw new Error(`dist source ${relativePath} changed during traversal`);
      }
      return;
    }
    if (!initialInfo.isFile()) {
      throw new Error(`dist source ${relativePath} is a special file`);
    }
    if (state.totalBytes > limits.payloadTotalBytes - initialInfo.size) {
      throw new Error("dist source exceeds the payload byte limit");
    }
    await copyStableRegularFile(
      sourcePath,
      initialInfo,
      destinationPath,
      0o644,
      `dist source ${relativePath}`,
      limits,
      { nonempty: false },
    );
    state.totalBytes += initialInfo.size;
    state.files += 1;
  }

  await visit(sourceRoot, destinationRoot, "dist");
  if (state.files === 0) {
    throw new Error("dist source must contain at least one regular file");
  }
  return state;
}

async function hashStableRegularFile(path, info, label, maximumBytes) {
  if (!info.isFile() || info.isSymbolicLink() || info.size > maximumBytes) {
    throw new Error(`${label} is not a bounded regular file`);
  }
  const { handle, openedInfo } = await openStableRegularFile(path, info, label);
  try {
    const firstDigest = await digestHandleExactly(
      handle,
      openedInfo.size,
      label,
    );
    const secondDigest = await digestHandleExactly(
      handle,
      openedInfo.size,
      label,
    );
    const finalOpenInfo = await handle.stat();
    const finalPathInfo = await lstat(path);
    if (
      firstDigest !== secondDigest ||
      !sameIdentity(openedInfo, finalOpenInfo) ||
      !sameIdentity(openedInfo, finalPathInfo) ||
      finalOpenInfo.size !== openedInfo.size ||
      permissionMode(finalOpenInfo) !== permissionMode(openedInfo)
    ) {
      throw new Error(`${label} changed while it was hashed`);
    }
    return { digest: firstDigest, size: openedInfo.size };
  } finally {
    await handle.close();
  }
}

async function buildPayloadLedger(payloadRoot, limits) {
  const entries = [];
  let count = 0;
  let totalBytes = 0;

  async function visit(path, relativePath) {
    const initialInfo = await lstat(path);
    if (initialInfo.isSymbolicLink()) {
      throw new Error(`staged payload ${relativePath} is a symbolic link`);
    }
    rejectPrivilegedMode(initialInfo, `staged payload ${relativePath}`);
    count += 1;
    if (count > limits.payloadEntries) {
      throw new Error("staged payload exceeds the entry limit");
    }
    if (initialInfo.isDirectory()) {
      entries.push({
        mode: permissionMode(initialInfo),
        path: relativePath,
        type: "directory",
      });
      const before = await stableDirectoryEntries(
        path,
        `staged payload ${relativePath}`,
        limits.payloadEntries,
      );
      for (const child of before) {
        const childRelative =
          relativePath === "." ? child.name : `${relativePath}/${child.name}`;
        await visit(join(path, child.name), childRelative);
      }
      const finalInfo = await lstat(path);
      const after = await stableDirectoryEntries(
        path,
        `staged payload ${relativePath}`,
        limits.payloadEntries,
      );
      if (
        !sameIdentity(initialInfo, finalInfo) ||
        permissionMode(initialInfo) !== permissionMode(finalInfo) ||
        before.length !== after.length ||
        before.some(
          (entry, index) =>
            directoryEntrySignature(entry) !== directoryEntrySignature(after[index]),
        )
      ) {
        throw new Error(`staged payload ${relativePath} changed during traversal`);
      }
      return;
    }
    if (!initialInfo.isFile()) {
      throw new Error(`staged payload ${relativePath} is a special file`);
    }
    if (initialInfo.size > limits.payloadFileBytes) {
      throw new Error(`staged payload ${relativePath} exceeds the file byte limit`);
    }
    if (totalBytes > limits.payloadTotalBytes - initialInfo.size) {
      throw new Error("staged payload exceeds the total byte limit");
    }
    const hashed = await hashStableRegularFile(
      path,
      initialInfo,
      `staged payload ${relativePath}`,
      limits.payloadFileBytes,
    );
    totalBytes += initialInfo.size;
    entries.push({
      mode: permissionMode(initialInfo),
      path: relativePath,
      sha256: hashed.digest,
      size: hashed.size,
      type: "file",
    });
  }

  await visit(payloadRoot, ".");
  const [root, ...children] = entries;
  children.sort((left, right) => compareText(left.path, right.path));
  return [root, ...children];
}

function commandOutput(repositoryRoot, command, arguments_, label) {
  const nullDevice = process.platform === "win32" ? "NUL" : "/dev/null";
  const environment = {
    GIT_ATTR_NOSYSTEM: "1",
    GIT_CONFIG_GLOBAL: nullDevice,
    GIT_CONFIG_NOSYSTEM: "1",
    GIT_NO_REPLACE_OBJECTS: "1",
    GIT_OPTIONAL_LOCKS: "0",
    GIT_PAGER: "cat",
    GIT_TERMINAL_PROMPT: "0",
    LANG: "C",
    LC_ALL: "C",
    PATH:
      process.platform === "win32"
        ? process.env.PATH
        : "/usr/bin:/bin:/usr/sbin:/sbin",
  };
  const result = spawnSync(command, arguments_, {
    cwd: repositoryRoot,
    encoding: "utf8",
    env: environment,
    maxBuffer: 1 << 20,
  });
  if (result.error !== undefined || result.status !== 0) {
    throw new Error(`Could not ${label}`);
  }
  return result.stdout;
}

function gitOutput(repositoryRoot, ...arguments_) {
  return commandOutput(
    repositoryRoot,
    process.platform === "win32" ? "git" : "/usr/bin/git",
    [
      "-c",
      "core.checkStat=default",
      "-c",
      "core.filemode=true",
      "-c",
      "core.fsmonitor=false",
      "-c",
      `core.excludesFile=${process.platform === "win32" ? "NUL" : "/dev/null"}`,
      "-c",
      `core.hooksPath=${process.platform === "win32" ? "NUL" : "/dev/null"}`,
      "-c",
      "core.ignorestat=false",
      "-C",
      repositoryRoot,
      ...arguments_,
    ],
    `run git ${arguments_[0] ?? "command"}`,
  );
}

function requireNoRepositoryExcludePatterns(repositoryRoot) {
  const reportedPath = gitOutput(
    repositoryRoot,
    "rev-parse",
    "--path-format=absolute",
    "--git-path",
    "info/exclude",
  ).trim();
  const excludePath = resolve(repositoryRoot, reportedPath);
  let initialInfo;
  try {
    initialInfo = lstatSync(excludePath);
  } catch (error) {
    if (error?.code === "ENOENT") {
      return;
    }
    throw new Error("Could not inspect the repository-local Git exclude file");
  }
  if (
    initialInfo.isSymbolicLink() ||
    !initialInfo.isFile() ||
    initialInfo.nlink !== 1 ||
    initialInfo.size > 64 * 1024
  ) {
    throw new Error("The repository-local Git exclude file is not an admitted regular file");
  }
  const descriptor = openSync(
    excludePath,
    constants.O_RDONLY | constants.O_NOFOLLOW,
  );
  let payload;
  let openedInfo;
  let finalOpenedInfo;
  try {
    openedInfo = fstatSync(descriptor);
    if (!sameIdentity(initialInfo, openedInfo)) {
      throw new Error("The repository-local Git exclude file changed before reading");
    }
    payload = readFileSync(descriptor);
    finalOpenedInfo = fstatSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
  const finalInfo = lstatSync(excludePath);
  if (
    !sameIdentity(initialInfo, finalInfo) ||
    !sameIdentity(openedInfo, finalOpenedInfo) ||
    initialInfo.size !== payload.length ||
    finalOpenedInfo.size !== payload.length ||
    initialInfo.mode !== finalInfo.mode ||
    initialInfo.mtimeMs !== finalInfo.mtimeMs ||
    initialInfo.ctimeMs !== finalInfo.ctimeMs
  ) {
    throw new Error("The repository-local Git exclude file changed while reading");
  }
  let source;
  try {
    source = new TextDecoder("utf-8", { fatal: true }).decode(payload);
  } catch {
    throw new Error("The repository-local Git exclude file is not valid UTF-8");
  }
  if (
    source.includes("\0") ||
    source
      .split("\n")
      .map((line) => line.endsWith("\r") ? line.slice(0, -1) : line)
      .some((line) => line.length !== 0 && !line.startsWith("#"))
  ) {
    throw new Error("The repository-local Git exclude file contains an active pattern");
  }
}

function requireUnmaskedGitState(repositoryRoot) {
  const records = gitOutput(repositoryRoot, "ls-files", "-v", "-z")
    .split("\0")
    .filter((record) => record.length !== 0);
  if (
    records.length === 0 ||
    records.some(
      (record) =>
        record.length < 3 || record[0] !== "H" || record[1] !== " ",
    )
  ) {
    throw new Error("Git index contains a masked or non-ordinary tracked entry");
  }
  try {
    gitOutput(repositoryRoot, "update-index", "-q", "--really-refresh");
    gitOutput(
      repositoryRoot,
      "diff-files",
      "--quiet",
      "--ignore-submodules=none",
      "--",
    );
    gitOutput(
      repositoryRoot,
      "diff-index",
      "--cached",
      "--quiet",
      "HEAD",
      "--",
    );
  } catch {
    throw new Error("Git source worktree or index differs from HEAD");
  }
}

function requireCleanSource(repositoryRoot, expectedRevision) {
  const topLevel = gitOutput(repositoryRoot, "rev-parse", "--show-toplevel").trim();
  if (topLevel !== repositoryRoot) {
    throw new Error("repository root does not identify the Git worktree root");
  }
  const revision = gitOutput(
    repositoryRoot,
    "rev-parse",
    "--verify",
    "HEAD",
  ).trim();
  if (revision !== expectedRevision) {
    throw new Error("Git HEAD does not match --expected-revision");
  }
  requireNoRepositoryExcludePatterns(repositoryRoot);
  requireUnmaskedGitState(repositoryRoot);
  const status = gitOutput(
    repositoryRoot,
    "status",
    "--porcelain=v1",
    "--untracked-files=all",
    "--ignore-submodules=none",
  );
  if (status.length !== 0) {
    throw new Error("Git source worktree is dirty");
  }
}

function gitCommitTime(repositoryRoot, revision) {
  const source = gitOutput(
    repositoryRoot,
    "show",
    "-s",
    "--format=%cI",
    revision,
  ).trim();
  const parsed = new Date(source);
  if (source.length === 0 || Number.isNaN(parsed.valueOf())) {
    throw new Error("Git commit timestamp is invalid");
  }
  return parsed.toISOString().replace(".000Z", "Z");
}

async function defaultVerifyUniversalBinary(path, label, expectedDigest) {
  if (!digestPattern.test(expectedDigest ?? "")) {
    throw new Error(`${label} expected digest is invalid`);
  }
  const initialInfo = await lstat(path);
  const before = await hashStableRegularFile(
    path,
    initialInfo,
    label,
    r0EvidenceLimits.payloadFileBytes,
  );
  if (before.digest !== expectedDigest) {
    throw new Error(`${label} changed before Mach-O verification`);
  }
  const commandOptions = {
    encoding: "utf8",
    env: {
      LANG: "C",
      LC_ALL: "C",
      PATH: "/usr/bin:/bin:/usr/sbin:/sbin",
    },
    maxBuffer: 64 << 10,
    timeout: 60_000,
  };
  const lipo = spawnSync(
    "/usr/bin/lipo",
    ["-archs", path],
    commandOptions,
  );
  const architectures = lipo.stdout.trim().split(/\s+/u).sort();
  if (
    lipo.error !== undefined ||
    lipo.signal !== null ||
    lipo.status !== 0 ||
    architectures.length !== 2 ||
    architectures[0] !== "arm64" ||
    architectures[1] !== "x86_64"
  ) {
    throw new Error(`${label} is not exactly an arm64+x86_64 Universal binary`);
  }
  const headers = spawnSync("/usr/bin/otool", ["-hv", path], commandOptions);
  const headerSource = headers.stdout ?? "";
  const headingArchitectures = [...headerSource.matchAll(/\(architecture (arm64|x86_64)\):$/gmu)]
    .map((match) => match[1])
    .sort();
  const executableHeaders = headerSource
    .split(/\r?\n/u)
    .filter((line) => /^MH_MAGIC_64\s+\S+\s+\S+\s+\S+\s+EXECUTE\s+/u.test(line));
  if (
    headers.error !== undefined ||
    headers.signal !== null ||
    headers.status !== 0 ||
    headingArchitectures.length !== 2 ||
    headingArchitectures[0] !== "arm64" ||
    headingArchitectures[1] !== "x86_64" ||
    executableHeaders.length !== 2
  ) {
    throw new Error(`${label} is not a two-slice MH_EXECUTE Mach-O binary`);
  }
  const buildVersions = spawnSync(
    "/usr/bin/vtool",
    ["-show-build", path],
    commandOptions,
  );
  const buildSource = buildVersions.stdout ?? "";
  const buildArchitectures = [
    ...buildSource.matchAll(/^.+ \(architecture (arm64|x86_64)\):$/gmu),
  ].map((match) => match[1]).sort();
  const platforms = [
    ...buildSource.matchAll(/^\s*platform (\S+)\s*$/gmu),
  ].map((match) => match[1]);
  const minimumVersions = [
    ...buildSource.matchAll(/^\s*minos ([0-9]+(?:\.[0-9]+){1,2})\s*$/gmu),
  ].map((match) => match[1]);
  const supportsMacOS14 = minimumVersions.every((value) => {
    const [major, minor = 0, patch = 0] = value
      .split(".")
      .map((component) => Number.parseInt(component, 10));
    return (
      major < 14 ||
      (major === 14 && minor === 0 && patch === 0)
    );
  });
  if (
    buildVersions.error !== undefined ||
    buildVersions.signal !== null ||
    buildVersions.status !== 0 ||
    buildArchitectures.length !== 2 ||
    buildArchitectures[0] !== "arm64" ||
    buildArchitectures[1] !== "x86_64" ||
    platforms.length !== 2 ||
    platforms.some((platform) => platform !== "MACOS") ||
    minimumVersions.length !== 2 ||
    !supportsMacOS14
  ) {
    throw new Error(`${label} does not support macOS 14 on both Universal slices`);
  }
  const finalInfo = await lstat(path);
  if (!sameIdentity(initialInfo, finalInfo)) {
    throw new Error(`${label} path changed during Mach-O verification`);
  }
  const after = await hashStableRegularFile(
    path,
    finalInfo,
    label,
    r0EvidenceLimits.payloadFileBytes,
  );
  if (after.digest !== expectedDigest) {
    throw new Error(`${label} changed during Mach-O verification`);
  }
}

function syftCommandEnvironment(workDirectory) {
  return {
    LANG: "C",
    LC_ALL: "C",
    PATH: "/usr/bin:/bin:/usr/sbin:/sbin",
    SYFT_CHECK_FOR_APP_UPDATE: "false",
    SYFT_FILE_METADATA_DIGESTS: "sha256",
    SYFT_FILE_METADATA_SELECTION: "all",
    TMPDIR: workDirectory,
    XDG_CACHE_HOME: workDirectory,
    XDG_CONFIG_HOME: workDirectory,
  };
}

function requireSuccessfulSyft(result, label) {
  if (
    result.error !== undefined ||
    result.signal !== null ||
    result.status !== 0
  ) {
    throw new Error(`${label} failed`);
  }
}

async function defaultGenerateSyftSPDX({
  artifactRoot,
  limits,
  payloadRoot,
  syftBinaryPath,
  version,
}) {
  if (process.platform !== "darwin" || process.arch !== "arm64") {
    throw new Error(
      "Source-traceability evidence (R0) admits only the pinned Syft darwin/arm64 binary",
    );
  }
  cleanAbsolutePath("Syft binary", syftBinaryPath);
  await inspectAbsoluteDirectory(dirname(syftBinaryPath), "Syft binary parent");
  const syftInfo = await lstat(syftBinaryPath);
  if (
    syftInfo.isSymbolicLink() ||
    !syftInfo.isFile() ||
    syftInfo.nlink !== 1 ||
    (permissionMode(syftInfo) & 0o111) === 0 ||
    (await realpath(syftBinaryPath)) !== syftBinaryPath
  ) {
    throw new Error("The admitted Syft binary is not a canonical executable file");
  }
  const initialSyft = await hashStableRegularFile(
    syftBinaryPath,
    syftInfo,
    "Syft binary",
    limits.payloadFileBytes,
  );
  if (initialSyft.digest !== admittedSyftDarwinArm64ExecutableSHA256) {
    throw new Error("The Syft binary does not match the admitted v1.44.0 digest");
  }

  const workDirectory = join(
    artifactRoot,
    `.syft-private-${randomUUID()}`,
  );
  await mkdir(workDirectory, { mode: 0o700 });
  await chmod(workDirectory, 0o700);
  try {
    const configPath = join(workDirectory, "trusted-empty.yaml");
    await writeExclusiveFile(
      configPath,
      Buffer.from("{}\n", "utf8"),
      0o600,
      "trusted empty Syft configuration",
      1024,
    );
    const environment = syftCommandEnvironment(workDirectory);
    const versionResult = spawnSync(
      syftBinaryPath,
      ["version", "--output", "json", "--config", configPath],
      {
        encoding: "utf8",
        env: environment,
        maxBuffer: 1 << 20,
        timeout: 60_000,
      },
    );
    requireSuccessfulSyft(versionResult, "Pinned Syft version inspection");
    const versionDocument = parseJSON(
      Buffer.from(versionResult.stdout, "utf8"),
      "Pinned Syft version output",
    );
    exactKeys(
      versionDocument,
      [
        "application",
        "buildDate",
        "compiler",
        "gitCommit",
        "gitDescription",
        "goVersion",
        "platform",
        "schemaVersion",
        "version",
      ],
      "Pinned Syft version output",
    );
    if (
      versionDocument.application !== "syft" ||
      versionDocument.version !== syftVersion ||
      versionDocument.gitDescription !== `v${syftVersion}` ||
      versionDocument.gitCommit !== "8cb78ce40ced6a731fb83f2a491a67444f541bf1" ||
      versionDocument.platform !== "darwin/arm64" ||
      versionDocument.compiler !== "gc"
    ) {
      throw new Error("Pinned Syft version output is not the admitted release");
    }

    const outputPath = join(workDirectory, "payload.spdx.json");
    const scanResult = spawnSync(
      syftBinaryPath,
      [
        "scan",
        "--config",
        configPath,
        "--source-name",
        syftSourceName,
        "--source-version",
        version,
        `dir:${payloadRoot}`,
        "--quiet",
        "--output",
        `spdx-json=${outputPath}`,
      ],
      {
        encoding: "utf8",
        env: environment,
        maxBuffer: 1 << 20,
        timeout: 20 * 60_000,
      },
    );
    requireSuccessfulSyft(scanResult, "Pinned Syft payload scan");
    if (`${scanResult.stdout ?? ""}${scanResult.stderr ?? ""}`.trim() !== "") {
      throw new Error("Pinned Syft payload scan produced unexpected console output");
    }
    const outputInfo = await lstat(outputPath);
    const output = await readStableFile(
      outputPath,
      outputInfo,
      "Pinned Syft SPDX output",
      limits.syftInputBytes,
    );
    const finalSyftInfo = await lstat(syftBinaryPath);
    if (!sameIdentity(syftInfo, finalSyftInfo)) {
      throw new Error("The Syft binary path changed during the payload scan");
    }
    const finalSyft = await hashStableRegularFile(
      syftBinaryPath,
      finalSyftInfo,
      "Syft binary",
      limits.payloadFileBytes,
    );
    if (finalSyft.digest !== initialSyft.digest) {
      throw new Error("The Syft binary changed during the payload scan");
    }
    return output.payload;
  } finally {
    await rm(workDirectory, { recursive: true, force: false });
  }
}

function knownIssues(version, revision) {
  return {
    schema: knownIssuesSchema,
    version,
    commit: revision,
    issues: [
      {
        id: "r2-reproducibility-missing",
        summary: "R2 reproducibility has not been established for this candidate.",
        severity: "medium",
      },
      {
        id: "r3-signed-package-binding-missing",
        summary:
          "R3 signed-package binding, including a successful installed-candidate report, has not been established for this unsigned payload.",
        severity: "high",
      },
      {
        id: "packaged-conformance-current-missing",
        summary: "Current packaged conformance evidence is missing for this candidate.",
        severity: "high",
      },
      {
        id: "release-approval-missing",
        summary: "Release approval has not been asserted for this candidate.",
        severity: "high",
      },
      {
        id: "license-review-missing",
        summary: "Dependency license review has not been completed for this candidate.",
        severity: "high",
      },
    ],
  };
}

function missingCapability(capability, status = "unknown") {
  if (!identifierPattern.test(capability)) {
    throw new Error("internal capability identifier is invalid");
  }
  return {
    capability,
    status,
    evidenceStatus: "missing",
    evidenceArtifactRole: "none",
  };
}

function releaseSpec({ artifacts, commitTime, revision, version }) {
  return {
    schema: releaseSchema,
    version,
    channel: "nightly",
    commit: revision,
    protocolSchema: "not-asserted-r0",
    pluginSDK: "not-asserted-r0",
    platformSupport: [
      {
        os: "macos",
        range: ">=14.0",
        architectures: ["arm64", "x86_64"],
        installShape: "unsigned-payload",
        supportLevel: "preview",
        conformanceRevision: "not-established-r0",
        hostCapabilities: [
          missingCapability("packaged-conformance"),
          missingCapability("universal-execution"),
        ],
      },
    ],
    artifacts,
    sbom: { role: "sbom" },
    evidenceScope: {
      level: "r0",
      artifactState: "unsigned-pre-sign",
      r2Reproducibility: "not-asserted",
      r3SignedPackageBinding: "not-asserted",
      releaseApproval: "not-asserted",
    },
    migration: {
      minimumSchema: "0",
      maximumSchema: "26",
      rollbackCompatibility: "backup-required",
    },
    hostSupport: [
      {
        host: "desktop",
        supportLevel: "preview",
        conformanceRevision: "not-established-r0",
        capabilities: [
          missingCapability("local-control"),
          missingCapability("runtime"),
        ],
      },
      {
        host: "server",
        supportLevel: "unsupported",
        conformanceRevision: "server-not-shipped",
        capabilities: [missingCapability("runtime", "unsupported")],
      },
    ],
    knownIssues: { role: "known-issues" },
    publishedAt: commitTime,
  };
}

async function createArtifactRoot(path) {
  const parent = dirname(path);
  await inspectAbsoluteDirectory(parent, "artifact root parent");
  try {
    await lstat(path);
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
    await mkdir(path, { mode: 0o700 });
    await chmod(path, 0o700);
    const info = await lstat(path);
    if (!info.isDirectory() || info.isSymbolicLink() || permissionMode(info) !== 0o700) {
      throw new Error("artifact root was not created privately");
    }
    return info;
  }
  throw new Error("artifact root already exists");
}

async function syncDirectory(path) {
  const handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    await handle.sync();
  } finally {
    await handle.close();
  }
}

function artifactDeclaration(path, mediaType, role, evidence) {
  return {
    path,
    mediaType,
    role,
    size: evidence.size,
    sha256: evidence.digest,
  };
}

export async function prepareR0ReleaseEvidence(
  options,
  dependencies = {},
) {
  const repositoryRoot = cleanAbsolutePath(
    "repository root",
    options.repositoryRoot ?? defaultRepositoryRoot,
  );
  const hasExternalInputRoot = options.inputRoot !== undefined;
  const inputRoot = cleanAbsolutePath(
    "input root",
    options.inputRoot ?? repositoryRoot,
  );
  const artifactRoot = cleanAbsolutePath("artifact root", options.artifactRoot);
  const expectedRevision = options.expectedRevision;
  if (!fullRevision.test(expectedRevision ?? "")) {
    throw new Error("expected revision must be 40 lowercase hexadecimal characters");
  }
  const limits = { ...r0EvidenceLimits };
  for (const [name, value] of Object.entries(dependencies.limits ?? {})) {
    if (
      !Object.hasOwn(limits, name) ||
      !Number.isSafeInteger(value) ||
      value <= 0 ||
      value > limits[name]
    ) {
      throw new Error(`test limit override ${name} is invalid`);
    }
    limits[name] = value;
  }
  Object.freeze(limits);
  const verifyUniversalBinary =
    dependencies.verifyUniversalBinary ?? defaultVerifyUniversalBinary;
  const generateSyftSPDX =
    dependencies.generateSyftSPDX ?? defaultGenerateSyftSPDX;
  const syftBinaryPath =
    dependencies.generateSyftSPDX === undefined
      ? cleanAbsolutePath("Syft binary", options.syftBinaryPath)
      : options.syftBinaryPath;

  await inspectAbsoluteDirectory(repositoryRoot, "repository root");
  await inspectAbsoluteDirectory(inputRoot, "input root");
  const relativeArtifactRoot = relative(repositoryRoot, artifactRoot);
  if (
    relativeArtifactRoot === "" ||
    (relativeArtifactRoot !== ".." && !relativeArtifactRoot.startsWith(`..${process.platform === "win32" ? "\\" : "/"}`))
  ) {
    throw new Error("artifact root must be outside the Git source root");
  }
  if (hasExternalInputRoot) {
    const relativeInputRoot = relative(repositoryRoot, inputRoot);
    if (
      relativeInputRoot === "" ||
      (relativeInputRoot !== ".." &&
        !relativeInputRoot.startsWith(
          `..${process.platform === "win32" ? "\\" : "/"}`,
        ))
    ) {
      throw new Error("external input root must be outside the Git source root");
    }
    const artifactFromInput = relative(inputRoot, artifactRoot);
    const inputFromArtifact = relative(artifactRoot, inputRoot);
    const isBelow = (value) =>
      value === "" ||
      (value !== ".." &&
        !value.startsWith(
          `..${process.platform === "win32" ? "\\" : "/"}`,
        ));
    if (isBelow(artifactFromInput) || isBelow(inputFromArtifact)) {
      throw new Error("artifact root and external input root must not overlap");
    }
  }
  requireCleanSource(repositoryRoot, expectedRevision);
  const commitTime = gitCommitTime(repositoryRoot, expectedRevision);

  const repositorySourcePaths = {
    package: "ui/desktop/package.json",
    trackedLicense: "LICENSE",
  };
  const inputSourcePaths = hasExternalInputRoot
    ? {
        main: "vibermate-desktop",
        vibermate: "vibermate",
        vibermated: "vibermated",
        desktopBuild: "vibermate-build-manifest.json",
        license: "LICENSE",
        dist: "dist",
      }
    : {
        main: "ui/desktop/src-tauri/target/universal-apple-darwin/release/vibermate-desktop",
        vibermate: "ui/desktop/src-tauri/binaries/vibermate-universal-apple-darwin",
        vibermated: "ui/desktop/src-tauri/binaries/vibermated-universal-apple-darwin",
        desktopBuild: "ui/desktop/src-tauri/binaries/vibermate-build-manifest.json",
        license: "LICENSE",
        dist: "ui/desktop/dist",
      };
  if (hasExternalInputRoot) {
    const expectedInputNames = Object.values(inputSourcePaths).sort(compareText);
    const observedInputNames = (
      await stableDirectoryEntries(
        inputRoot,
        "external source-traceability build-input root (R0)",
        expectedInputNames.length,
      )
    ).map((entry) => entry.name);
    if (JSON.stringify(observedInputNames) !== JSON.stringify(expectedInputNames)) {
      throw new Error(
        "external source-traceability build-input root (R0) has an unexpected inventory",
      );
    }
  }
  const inspected = {};
  for (const [name, path] of Object.entries(repositorySourcePaths)) {
    inspected[name] = await inspectRelativePath(
      repositoryRoot,
      path,
      `${name} source`,
    );
  }
  for (const [name, path] of Object.entries(inputSourcePaths)) {
    inspected[name] = await inspectRelativePath(
      inputRoot,
      path,
      `${name} input`,
    );
  }
  if (!inspected.dist.info.isDirectory()) {
    throw new Error("dist source must be a directory");
  }

  const packageSource = await readStableFile(
    inspected.package.path,
    inspected.package.info,
    "Desktop package.json",
    1 << 20,
  );
  const packageDocument = parseJSON(packageSource.payload, "Desktop package.json");
  const version = packageDocument?.version;
  if (!semanticVersion.test(version ?? "") || version === "0.0.0") {
    throw new Error("Desktop package version must be a non-placeholder Semantic Version");
  }

  const trackedLicenseSource = await readStableFile(
    inspected.trackedLicense.path,
    inspected.trackedLicense.info,
    "Tracked LICENSE",
    limits.syftInputBytes,
  );

  const buildSource = await readStableFile(
    inspected.desktopBuild.path,
    inspected.desktopBuild.info,
    "Desktop build manifest",
    limits.desktopBuildManifestBytes,
  );
  const buildManifest = validateDesktopBuildManifest(
    parseJSON(buildSource.payload, "Desktop build manifest"),
    expectedRevision,
    commitTime,
  );
  for (const configurationName of configurationNames) {
    const configurationSource = await inspectRelativePath(
      repositoryRoot,
      configurationName,
      `Desktop configuration ${configurationName}`,
    );
    const hashed = await hashStableRegularFile(
      configurationSource.path,
      configurationSource.info,
      `Desktop configuration ${configurationName}`,
      limits.syftInputBytes,
    );
    if (hashed.digest !== buildManifest.configurationSHA256[configurationName]) {
      throw new Error(
        `Desktop build configurationSHA256 does not bind ${configurationName}`,
      );
    }
  }

  const createdRootInfo = await createArtifactRoot(artifactRoot);
  let complete = false;
  try {
    const payloadRoot = join(artifactRoot, unsignedPayloadName);
    await mkdir(payloadRoot, { mode: 0o755 });
    await chmod(payloadRoot, 0o755);

    const fixedCopies = [
      ["main", "vibermate-desktop", 0o755, true],
      ["vibermate", "vibermate", 0o755, true],
      ["vibermated", "vibermated", 0o755, true],
      ["license", "LICENSE", 0o644, false],
    ];
    const copied = {};
    let fixedBytes = buildSource.payload.length;
    for (const [sourceName, destinationName, mode, executable] of fixedCopies) {
      const source = inspected[sourceName];
      copied[sourceName] = await copyStableRegularFile(
        source.path,
        source.info,
        join(payloadRoot, destinationName),
        mode,
        `${sourceName} source`,
        limits,
        { executable },
      );
      fixedBytes += copied[sourceName].size;
      if (fixedBytes > limits.payloadTotalBytes) {
        throw new Error("fixed payload files exceed the total byte limit");
      }
    }
    if (
      copied.vibermate.digest !== buildManifest.sidecarSHA256.vibermate ||
      copied.vibermated.digest !== buildManifest.sidecarSHA256.vibermated
    ) {
      throw new Error("actual sidecar bytes do not match Desktop build sidecarSHA256");
    }
    if (copied.license.digest !== trackedLicenseSource.digest) {
      throw new Error(
        "Source-traceability build-input LICENSE (R0) does not match the tracked candidate LICENSE",
      );
    }

    await writeExclusiveFile(
      join(payloadRoot, "vibermate-build-manifest.json"),
      buildSource.payload,
      0o600,
      "embedded Desktop build manifest",
      limits.desktopBuildManifestBytes,
    );
    const distState = await stageDistTree(
      inspected.dist.path,
      join(payloadRoot, "dist"),
      limits,
    );
    if (fixedBytes > limits.payloadTotalBytes - distState.totalBytes) {
      throw new Error("staged payload exceeds the total byte limit");
    }

    for (const [name, label] of [
      ["vibermate-desktop", "Desktop main binary"],
      ["vibermate", "vibermate sidecar"],
      ["vibermated", "vibermated sidecar"],
    ]) {
      const sourceName = name === "vibermate-desktop" ? "main" : name;
      await verifyUniversalBinary(
        join(payloadRoot, name),
        label,
        copied[sourceName].digest,
      );
    }

    const desktopArtifactEvidence = await writeExclusiveFile(
      join(artifactRoot, metadataNames.desktopBuild),
      buildSource.payload,
      0o600,
      "Desktop build manifest artifact",
      limits.desktopBuildManifestBytes,
    );
    const ledger = {
      schema: appTreeLedgerSchema,
      commit: expectedRevision,
      root: unsignedPayloadName,
      desktopBuildManifestSHA256: desktopArtifactEvidence.digest,
      entries: await buildPayloadLedger(payloadRoot, limits),
    };
    const ledgerEntries = new Map(
      ledger.entries.map((entry) => [entry.path, entry]),
    );
    for (const [sourceName, payloadName] of [
      ["main", "vibermate-desktop"],
      ["vibermate", "vibermate"],
      ["vibermated", "vibermated"],
    ]) {
      if (ledgerEntries.get(payloadName)?.sha256 !== copied[sourceName].digest) {
        throw new Error(`${payloadName} ledger digest changed after Mach-O verification`);
      }
    }
    const ledgerEvidence = await writeExclusiveFile(
      join(artifactRoot, metadataNames.ledger),
      canonicalJSON(ledger),
      0o600,
      "application tree ledger",
      limits.ledgerBytes,
    );
    const rawSyftPayload = await generateSyftSPDX({
      artifactRoot,
      ledger,
      limits,
      payloadRoot,
      syftBinaryPath,
      version,
    });
    if (
      !Buffer.isBuffer(rawSyftPayload) ||
      rawSyftPayload.length === 0 ||
      rawSyftPayload.length > limits.syftInputBytes
    ) {
      throw new Error("Pinned Syft SPDX output has an invalid or excessive size");
    }
    const sbom = normalizeSyftSPDX(
      parseJSON(rawSyftPayload, "Pinned Syft SPDX output"),
      {
        commitTime,
        ledger,
        payloadLedgerSHA256: ledgerEvidence.digest,
        revision: expectedRevision,
        version,
      },
    );
    const postSyftLedger = await buildPayloadLedger(payloadRoot, limits);
    if (JSON.stringify(postSyftLedger) !== JSON.stringify(ledger.entries)) {
      throw new Error("The staged payload changed while Syft scanned it");
    }
    const sbomEvidence = await writeExclusiveFile(
      join(artifactRoot, metadataNames.sbom),
      canonicalJSON(sbom),
      0o600,
      "normalized SPDX SBOM",
      limits.spdxDocumentBytes,
    );
    const knownIssuesEvidence = await writeExclusiveFile(
      join(artifactRoot, metadataNames.knownIssues),
      canonicalJSON(knownIssues(version, expectedRevision)),
      0o600,
      "known issues",
      limits.knownIssuesBytes,
    );
    const artifacts = [
      artifactDeclaration(
        metadataNames.ledger,
        "application/json",
        "app-tree-ledger",
        ledgerEvidence,
      ),
      artifactDeclaration(
        metadataNames.desktopBuild,
        "application/json",
        "desktop-build-manifest",
        desktopArtifactEvidence,
      ),
      artifactDeclaration(
        metadataNames.sbom,
        "application/spdx+json",
        "sbom",
        sbomEvidence,
      ),
      artifactDeclaration(
        metadataNames.knownIssues,
        "application/json",
        "known-issues",
        knownIssuesEvidence,
      ),
    ];
    await writeExclusiveFile(
      join(artifactRoot, metadataNames.spec),
      canonicalJSON(
        releaseSpec({
          artifacts,
          commitTime,
          revision: expectedRevision,
          version,
        }),
      ),
      0o600,
      "source-traceability release specification (R0)",
      limits.artifactDocumentBytes,
    );

    const expectedRootNames = [
      metadataNames.ledger,
      metadataNames.desktopBuild,
      metadataNames.sbom,
      metadataNames.knownIssues,
      metadataNames.spec,
      unsignedPayloadName,
    ].sort(compareText);
    const observedRootNames = (
      await stableDirectoryEntries(
        artifactRoot,
        "source-traceability evidence root (R0)",
        expectedRootNames.length,
      )
    ).map((entry) => entry.name);
    if (JSON.stringify(observedRootNames) !== JSON.stringify(expectedRootNames)) {
      throw new Error(
        "Source-traceability evidence root (R0) has an unexpected final inventory",
      );
    }

    requireCleanSource(repositoryRoot, expectedRevision);
    const finalRootInfo = await lstat(artifactRoot);
    if (!sameIdentity(createdRootInfo, finalRootInfo) || permissionMode(finalRootInfo) !== 0o700) {
      throw new Error("artifact root changed during preparation");
    }
    await syncDirectory(payloadRoot);
    await syncDirectory(artifactRoot);
    complete = true;
    return Object.freeze({
      artifactRoot,
      spec: join(artifactRoot, metadataNames.spec),
    });
  } finally {
    if (!complete) {
      let current;
      try {
        current = await lstat(artifactRoot);
      } catch (error) {
        if (error?.code !== "ENOENT") {
          throw new Error(`could not inspect failed artifact root cleanup: ${error.message}`);
        }
      }
      if (current !== undefined) {
        if (!sameIdentity(createdRootInfo, current)) {
          throw new Error(
            "failed artifact root changed identity and was not removed",
          );
        }
        const cleanupDirectory = await mkdtemp(
          join(dirname(artifactRoot), ".vibermate-r0-cleanup-"),
        );
        await chmod(cleanupDirectory, 0o700);
        const tombstone = join(cleanupDirectory, "failed-artifact-root");
        try {
          await rename(artifactRoot, tombstone);
          const moved = await lstat(tombstone);
          if (!sameIdentity(createdRootInfo, moved)) {
            throw new Error(
              "failed artifact root changed identity while it was isolated",
            );
          }
          await rm(tombstone, { recursive: true, force: true });
          await rmdir(cleanupDirectory);
        } catch (error) {
          throw new Error(`could not isolate and remove failed artifact root: ${error.message}`);
        }
      }
    }
  }
}

export async function runR0EvidenceCLI(arguments_, stdout = process.stdout) {
  const parsed = parseR0EvidenceArguments(arguments_);
  const result = await prepareR0ReleaseEvidence(parsed);
  stdout.write(`prepared ${result.artifactRoot}\nspec ${result.spec}\n`);
  return result;
}

if (
  process.argv[1] !== undefined &&
  resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  try {
    await runR0EvidenceCLI(process.argv.slice(2));
  } catch (error) {
    process.stderr.write(
      `prepare-r0-release-evidence: ${error instanceof Error ? error.message : String(error)}\n`,
    );
    process.exitCode = 1;
  }
}
