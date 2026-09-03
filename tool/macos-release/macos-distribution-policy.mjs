import { Buffer } from "node:buffer";
import { createHash } from "node:crypto";
import { isAbsolute, normalize } from "node:path";
import {
  flutterDesktopBuildConfigurationNames,
  validateFlutterDesktopBuildManifest,
} from "../../ui/flutter_app/tool/desktop_build_manifest.mjs";

const sha256Pattern = /^[0-9a-f]{64}$/u;
const certificateSHA256Pattern = /^[0-9a-f]{64}$/u;
const appleTeamIDPattern = /^[A-Z0-9]{10}$/u;
const appleAPIKeyIDPattern = /^[A-Z0-9]{10,32}$/u;
const uuidPattern =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/iu;
const cdhashPattern = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/u;
const maximumJSONBytes = 8 << 20;
const fullGitRevisionPattern = /^[0-9a-f]{40}$/u;
const signingConfigurationNames = new Set([
  "CODE_SIGN_IDENTITY",
  "DEVELOPMENT_TEAM",
  "EXPANDED_CODE_SIGN_IDENTITY",
  "OTHER_CODE_SIGN_FLAGS",
  "PROVISIONING_PROFILE",
  "PROVISIONING_PROFILE_SPECIFIER",
]);

export const macOSDistributionPolicy = Object.freeze({
  appBundleName: "ViberMate.app",
  appBuildNumber: "1",
  appIdentifier: "io.vibermate.desktop",
  appVersion: "0.1.0",
  allowedApplicationSymlinks: Object.freeze({
    "Contents/Frameworks/App.framework/App": "Versions/Current/App",
    "Contents/Frameworks/App.framework/Resources":
      "Versions/Current/Resources",
    "Contents/Frameworks/App.framework/Versions/Current": "A",
    "Contents/Frameworks/FlutterMacOS.framework/FlutterMacOS":
      "Versions/Current/FlutterMacOS",
    "Contents/Frameworks/FlutterMacOS.framework/Resources":
      "Versions/Current/Resources",
    "Contents/Frameworks/FlutterMacOS.framework/Versions/Current": "A",
  }),
  architectures: Object.freeze(["arm64", "x86_64"]),
  developerDirectory: "/Applications/Xcode_16.2.app/Contents/Developer",
  diskImageFilename: "ViberMate_0.1.0_universal.dmg",
  diskImageIdentifier: "io.vibermate.desktop.dmg",
  evidenceSchema: "vibermate.macos-distribution-notarization/v1",
  lipoIdentity: "Apple lipo (version unavailable; SHA-256 bound)",
  codeObjectNames: Object.freeze([
    "app-framework",
    "flutter-macos-framework",
    "vibermate",
    "vibermate-desktop",
    "vibermated",
  ]),
  codeObjects: Object.freeze({
    "app-framework": Object.freeze({
      identifier: "io.flutter.flutter.app",
      relativePath:
        "Contents/Frameworks/App.framework/Versions/A/App",
      signingPath: "Contents/Frameworks/App.framework",
    }),
    "flutter-macos-framework": Object.freeze({
      identifier: "io.flutter.flutter-macos",
      relativePath:
        "Contents/Frameworks/FlutterMacOS.framework/Versions/A/FlutterMacOS",
      signingPath: "Contents/Frameworks/FlutterMacOS.framework",
    }),
    vibermate: Object.freeze({
      identifier: "io.vibermate.desktop.vibermate",
      relativePath: "Contents/MacOS/vibermate",
      signingPath: "Contents/MacOS/vibermate",
    }),
    "vibermate-desktop": Object.freeze({
      identifier: "io.vibermate.desktop",
      relativePath: "Contents/MacOS/vibermate-desktop",
      signingPath: "ViberMate.app",
    }),
    vibermated: Object.freeze({
      identifier: "io.vibermate.desktop.vibermated",
      relativePath: "Contents/MacOS/vibermated",
      signingPath: "Contents/MacOS/vibermated",
    }),
  }),
  // Apple may report a signed bundle entry point in addition to its canonical
  // executable. Validation maps each admitted alias back to one code object and
  // requires every reported cdhash for that object and architecture to agree.
  notaryTicketAliases: Object.freeze({
    "app-framework": Object.freeze([
      "Contents/Frameworks/App.framework/Versions/Current",
    ]),
    "flutter-macos-framework": Object.freeze([
      "Contents/Frameworks/FlutterMacOS.framework/Versions/Current",
    ]),
    "vibermate-desktop": Object.freeze(["."]),
  }),
  executableNames: Object.freeze([
    "vibermate",
    "vibermate-desktop",
    "vibermated",
  ]),
  executableIdentifiers: Object.freeze({
    vibermate: "io.vibermate.desktop.vibermate",
    "vibermate-desktop": "io.vibermate.desktop",
    vibermated: "io.vibermate.desktop.vibermated",
  }),
  minimumSystemVersion: "14.0",
  notaryEvidenceFilename: "notarization-evidence.json",
  notaryLogFilename: "apple-notary-log.json",
  notarySubmitFilename: "apple-notary-submit.json",
  notaryTicketedCodeDirectoryCount: 10,
  releaseRelativeDirectory:
    "ui/flutter_app/build/distribution/universal-apple-darwin/release",
  signingEvidenceFilename: "signing-transformation.json",
  signingEvidenceSchema: "vibermate.macos-distribution-signing/v1",
  signedAppArchiveFilename: "ViberMate.signed.app.vma",
  signedTransferChecksumFilename: "vibermate-macos-signed.sha256",
  target: "universal-apple-darwin",
  unsignedAppArchiveChecksumFilename: "ViberMate.unsigned.app.vma.sha256",
  unsignedAppArchiveFilename: "ViberMate.unsigned.app.vma",
  volumeName: "ViberMate",
  xcodeSDKVersion: "15.2",
  xcodeVersion: "Xcode 16.2\nBuild version 16C5032a",
});

function requirePlainObject(value, label) {
  if (
    value === null ||
    typeof value !== "object" ||
    Array.isArray(value) ||
    Object.getPrototypeOf(value) !== Object.prototype
  ) {
    throw new Error(`${label} must be an object`);
  }
  return value;
}

function requireExactKeys(value, expected, label) {
  requirePlainObject(value, label);
  const actual = Object.keys(value).sort();
  const wanted = [...expected].sort();
  if (
    actual.length !== wanted.length ||
    actual.some((key, index) => key !== wanted[index])
  ) {
    throw new Error(`${label} has an unexpected shape`);
  }
}

function requireNonemptyString(value, label, maximumLength = 4096) {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value.length > maximumLength ||
    value.includes("\0")
  ) {
    throw new Error(`${label} is invalid`);
  }
  return value;
}

function nonemptyEnvironmentValue(environment, name) {
  return (
    typeof environment[name] === "string" &&
    environment[name].trim().length !== 0
  );
}

function skipJSONWhitespace(source, start) {
  let index = start;
  while (
    index < source.length &&
    (source[index] === " " ||
      source[index] === "\n" ||
      source[index] === "\r" ||
      source[index] === "\t")
  ) {
    index += 1;
  }
  return index;
}

function scanJSONString(source, start) {
  let index = start + 1;
  while (index < source.length) {
    if (source[index] === "\\") {
      index += 2;
      continue;
    }
    if (source[index] === '"') {
      const end = index + 1;
      return {
        end,
        value: JSON.parse(source.slice(start, end)),
      };
    }
    index += 1;
  }
  throw new Error("JSON string is unterminated");
}

function scanJSONValue(source, start, depth) {
  if (depth > 128) {
    throw new Error("JSON nesting is too deep");
  }
  let index = skipJSONWhitespace(source, start);
  const character = source[index];
  if (character === '"') {
    return scanJSONString(source, index).end;
  }
  if (character === "{") {
    index = skipJSONWhitespace(source, index + 1);
    const keys = new Set();
    if (source[index] === "}") {
      return index + 1;
    }
    while (index < source.length) {
      if (source[index] !== '"') {
        throw new Error("JSON object key is invalid");
      }
      const key = scanJSONString(source, index);
      if (keys.has(key.value)) {
        throw new Error(`JSON object contains duplicate key ${key.value}`);
      }
      keys.add(key.value);
      index = skipJSONWhitespace(source, key.end);
      if (source[index] !== ":") {
        throw new Error("JSON object separator is invalid");
      }
      index = skipJSONWhitespace(
        source,
        scanJSONValue(source, index + 1, depth + 1),
      );
      if (source[index] === "}") {
        return index + 1;
      }
      if (source[index] !== ",") {
        throw new Error("JSON object delimiter is invalid");
      }
      index = skipJSONWhitespace(source, index + 1);
    }
    throw new Error("JSON object is unterminated");
  }
  if (character === "[") {
    index = skipJSONWhitespace(source, index + 1);
    if (source[index] === "]") {
      return index + 1;
    }
    while (index < source.length) {
      index = skipJSONWhitespace(
        source,
        scanJSONValue(source, index, depth + 1),
      );
      if (source[index] === "]") {
        return index + 1;
      }
      if (source[index] !== ",") {
        throw new Error("JSON array delimiter is invalid");
      }
      index = skipJSONWhitespace(source, index + 1);
    }
    throw new Error("JSON array is unterminated");
  }
  for (const literal of ["true", "false", "null"]) {
    if (source.startsWith(literal, index)) {
      return index + literal.length;
    }
  }
  const number = source.slice(index).match(
    /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/u,
  );
  if (number !== null) {
    return index + number[0].length;
  }
  throw new Error("JSON value is invalid");
}

function rejectDuplicateJSONKeys(source) {
  const end = skipJSONWhitespace(source, scanJSONValue(source, 0, 0));
  if (end !== source.length) {
    throw new Error("JSON has trailing data");
  }
}

export function parseClosedJSONObject(source, label) {
  if (
    typeof source !== "string" ||
    Buffer.byteLength(source, "utf8") > maximumJSONBytes
  ) {
    throw new Error(`${label} JSON is invalid`);
  }
  let value;
  try {
    value = JSON.parse(source);
    rejectDuplicateJSONKeys(source);
  } catch {
    throw new Error(`${label} is not valid JSON`);
  }
  return requirePlainObject(value, label);
}

export function requireAppleTeamID(value) {
  if (!appleTeamIDPattern.test(value ?? "")) {
    throw new Error("The Apple Team ID is invalid");
  }
  return value;
}

export function validateCodesignMetadata(
  source,
  expectedTeamID,
  { expectedIdentifier, label = "Code signature", requireRuntime = true } = {},
) {
  requireAppleTeamID(expectedTeamID);
  requireNonemptyString(source, `${label} metadata`, 1 << 20);
  const lines = source.split(/\r?\n/u);
  const values = (name) =>
    lines
      .filter((line) => line.startsWith(`${name}=`))
      .map((line) => line.slice(name.length + 1));
  const authorities = values("Authority");
  if (
    authorities.length !== 3 ||
    authorities[1] !== "Developer ID Certification Authority" ||
    authorities[2] !== "Apple Root CA"
  ) {
    throw new Error(`${label} does not have the Developer ID trust chain`);
  }
  const leafAuthority = authorities[0].match(
    /^Developer ID Application: .+ \(([A-Z0-9]{10})\)$/u,
  );
  if (leafAuthority === null) {
    throw new Error(`${label} is not a Developer ID Application signature`);
  }
  if (leafAuthority[1] !== expectedTeamID) {
    throw new Error(`${label} authority has the wrong Apple Team ID`);
  }
  const teams = values("TeamIdentifier");
  if (teams.length !== 1 || teams[0] !== expectedTeamID) {
    throw new Error(`${label} has the wrong Apple Team ID`);
  }
  const timestamps = values("Timestamp");
  if (
    timestamps.length !== 1 ||
    timestamps[0].trim().length === 0 ||
    timestamps[0].toLowerCase() === "none"
  ) {
    throw new Error(`${label} does not contain a secure timestamp`);
  }
  const codeDirectoryLines = lines.filter((line) =>
    line.startsWith("CodeDirectory "),
  );
  if (codeDirectoryLines.length !== 1) {
    throw new Error(`${label} CodeDirectory metadata is invalid`);
  }
  const flags = codeDirectoryLines[0].match(
    /\bflags=0x([0-9a-f]+)\(([^)]*)\)/u,
  );
  if (flags === null) {
    throw new Error(`${label} CodeDirectory flags are invalid`);
  }
  const flagValue = Number.parseInt(flags[1], 16);
  const flagNames = new Set(
    flags[2]
      .split(",")
      .map((value) => value.trim())
      .filter((value) => value.length !== 0),
  );
  if (
    !Number.isSafeInteger(flagValue) ||
    flagNames.has("adhoc") ||
    flagNames.has("linker-signed") ||
    values("Signature").some((value) => value === "adhoc")
  ) {
    throw new Error(`${label} is ad-hoc signed`);
  }
  if (
    requireRuntime &&
    ((flagValue & 0x10000) === 0 || !flagNames.has("runtime"))
  ) {
    throw new Error(`${label} does not enable the hardened runtime`);
  }
  if (expectedIdentifier !== undefined) {
    const identifiers = values("Identifier");
    if (
      identifiers.length !== 1 ||
      identifiers[0] !== expectedIdentifier
    ) {
      throw new Error(`${label} has the wrong identifier`);
    }
  }
  return Object.freeze({
    authority: authorities[0],
    teamIdentifier: teams[0],
  });
}

export function validateClosedEntitlements(source, label = "Code signature") {
  if (typeof source !== "string" || source.length > (1 << 20)) {
    throw new Error(`${label} entitlements are invalid`);
  }
  if (source.trim().length === 0) {
    return;
  }
  const emptyEntitlements =
    /^\s*(?:<\?xml[^>]*\?>\s*)?(?:<!DOCTYPE plist[^>]*>\s*)?<plist\s+version="1\.0"\s*>\s*<dict\s*(?:\/\s*>|>\s*<\/dict\s*>)\s*<\/plist\s*>\s*$/u;
  if (!emptyEntitlements.test(source)) {
    throw new Error(`${label} must have an exact empty entitlement set`);
  }
}

export function requireNoGetTaskAllow(source, label = "Code signature") {
  validateClosedEntitlements(source, label);
}

function requireDigestObject(value, expectedNames, label) {
  requireExactKeys(value, expectedNames, label);
  for (const name of expectedNames) {
    if (!sha256Pattern.test(value[name] ?? "")) {
      throw new Error(`${label} contains an invalid digest`);
    }
  }
}

export function validateEmbeddedBuildManifest(value, { expectedRevision }) {
  const provenance = validateFlutterDesktopBuildManifest(value, {
    expectedRevision,
    expectedTarget: macOSDistributionPolicy.target,
    requireClean: true,
  });
  if (
    Object.keys(value.configurationSHA256).length !==
    flutterDesktopBuildConfigurationNames.length
  ) {
    throw new Error("Embedded Flutter build configuration is incomplete");
  }
  return Object.freeze({
    commitTime: provenance.commitTime,
    nestedCodeSHA256: provenance.nestedCodeSHA256,
    revision: provenance.revision,
    sidecarSHA256: Object.freeze({
      vibermate: provenance.nestedCodeSHA256.vibermate,
      vibermated: provenance.nestedCodeSHA256.vibermated,
    }),
  });
}

export function validateInfoPlist(values) {
  requireExactKeys(
    values,
    [
      "bundleExecutable",
      "bundleIdentifier",
      "bundleVersion",
      "minimumSystemVersion",
      "shortVersion",
    ],
    "macOS application Info.plist",
  );
  if (
    values.bundleExecutable !== "vibermate-desktop" ||
    values.bundleIdentifier !== macOSDistributionPolicy.appIdentifier ||
    values.bundleVersion !== macOSDistributionPolicy.appBuildNumber ||
    values.shortVersion !== macOSDistributionPolicy.appVersion ||
    values.minimumSystemVersion !==
      macOSDistributionPolicy.minimumSystemVersion
  ) {
    throw new Error("macOS application Info.plist is inconsistent");
  }
  return values;
}

export function validateMachOInventory(actualPaths) {
  if (!Array.isArray(actualPaths)) {
    throw new Error("Mach-O inventory is invalid");
  }
  const actual = [...actualPaths].sort();
  const expected = Object.values(macOSDistributionPolicy.codeObjects)
    .map((codeObject) => codeObject.relativePath)
    .sort();
  if (
    actual.length !== expected.length ||
    actual.some((path, index) => path !== expected[index])
  ) {
    throw new Error("The application has an unexpected Flutter Mach-O inventory");
  }
  return Object.freeze(actual);
}

export function validateMountedDMGTopLevel(entries) {
  if (!Array.isArray(entries)) {
    throw new Error("Mounted DMG top-level inventory is invalid");
  }
  const actual = [...entries].sort((left, right) =>
    left.name.localeCompare(right.name, "en"),
  );
  const expected = [
    { name: "Applications", target: "/Applications", type: "symlink" },
    {
      name: macOSDistributionPolicy.appBundleName,
      type: "directory",
    },
  ].sort((left, right) => left.name.localeCompare(right.name, "en"));
  if (actual.length !== expected.length) {
    throw new Error("Mounted DMG has an unexpected top-level shape");
  }
  for (let index = 0; index < expected.length; index += 1) {
    requireExactKeys(
      actual[index],
      Object.keys(expected[index]),
      "Mounted DMG top-level entry",
    );
    for (const [name, value] of Object.entries(expected[index])) {
      if (actual[index][name] !== value) {
        throw new Error("Mounted DMG has an unexpected top-level shape");
      }
    }
  }
}

function validateTreeLedger(ledger, label) {
  if (!Array.isArray(ledger) || ledger.length === 0 || ledger.length > 65536) {
    throw new Error(`${label} is invalid`);
  }
  const paths = new Set();
  for (const entry of ledger) {
    requirePlainObject(entry, `${label} entry`);
    const commonKeys = ["mode", "path", "type"];
    const expectedKeys =
      entry.type === "file"
        ? [...commonKeys, "sha256", "size"]
        : entry.type === "symlink"
          ? [...commonKeys, "target"]
          : commonKeys;
    if (!["directory", "file", "symlink"].includes(entry.type)) {
      throw new Error(`${label} contains an unknown entry type`);
    }
    requireExactKeys(entry, expectedKeys, `${label} entry`);
    if (
      typeof entry.path !== "string" ||
      entry.path.length === 0 ||
      entry.path.startsWith("/") ||
      entry.path.split("/").some((component) => component === "..") ||
      paths.has(entry.path) ||
      !Number.isInteger(entry.mode) ||
      entry.mode < 0 ||
      entry.mode > 0o7777
    ) {
      throw new Error(`${label} contains an invalid entry`);
    }
    paths.add(entry.path);
    if (
      entry.type === "file" &&
      (!Number.isSafeInteger(entry.size) ||
        entry.size < 0 ||
        !sha256Pattern.test(entry.sha256 ?? ""))
    ) {
      throw new Error(`${label} contains an invalid file entry`);
    }
    if (
      entry.type === "symlink" &&
      (typeof entry.target !== "string" ||
        entry.target.length === 0 ||
        entry.target.includes("\0"))
    ) {
      throw new Error(`${label} contains an invalid symbolic link`);
    }
  }
}

export function treeLedgerSHA256(ledger) {
  validateTreeLedger(ledger, "Application tree ledger");
  const canonical = [...ledger]
    .sort((left, right) => left.path.localeCompare(right.path, "en"))
    .map((entry) => JSON.stringify(entry))
    .join("\n");
  return createHash("sha256").update(canonical, "utf8").digest("hex");
}

export function validateTreeLedgerEquality(expected, actual) {
  validateTreeLedger(expected, "Expected application tree ledger");
  validateTreeLedger(actual, "Mounted application tree ledger");
  const canonical = (ledger) =>
    [...ledger]
      .sort((left, right) => left.path.localeCompare(right.path, "en"))
      .map((entry) => JSON.stringify(entry));
  const wanted = canonical(expected);
  const observed = canonical(actual);
  if (
    wanted.length !== observed.length ||
    wanted.some((entry, index) => entry !== observed[index])
  ) {
    throw new Error(
      "The mounted application does not match the signed application tree",
    );
  }
}

export function validateLipoArchitectures(source, label = "Mach-O file") {
  if (typeof source !== "string" || source.length > 1024) {
    throw new Error(`${label} architecture output is invalid`);
  }
  const actual = source.trim().split(/\s+/u).filter(Boolean).sort();
  const expected = [...macOSDistributionPolicy.architectures].sort();
  if (
    actual.length !== expected.length ||
    actual.some((architecture, index) => architecture !== expected[index])
  ) {
    throw new Error(`${label} is not an arm64+x86_64 Universal binary`);
  }
  return Object.freeze(actual);
}

export function validateDiskImageFormat(source) {
  if (typeof source !== "string" || source.trim() !== "UDZO") {
    throw new Error("The distribution disk image must be read-only UDZO");
  }
  return "UDZO";
}

export function requireCertificateSHA256(value) {
  if (!certificateSHA256Pattern.test(value ?? "")) {
    throw new Error("The signing certificate SHA-256 fingerprint is invalid");
  }
  return value;
}

export function releaseRevisionsFromEnvironment(environment) {
  const candidateRevision = environment.VIBERMATE_CANDIDATE_REVISION?.trim();
  const toolingRevision = environment.VIBERMATE_TOOLING_REVISION?.trim();
  if (
    !fullGitRevisionPattern.test(candidateRevision ?? "") ||
    !fullGitRevisionPattern.test(toolingRevision ?? "")
  ) {
    throw new Error(
      "The candidate and trusted tooling revisions must be full lowercase Git revisions",
    );
  }
  return Object.freeze({ candidateRevision, toolingRevision });
}

export function validateAppleToolchainEvidence(value) {
  requireExactKeys(
    value,
    [
      "clang",
      "codesign",
      "developerDirectory",
      "hdiutil",
      "lipo",
      "macOS",
      "macOSBuild",
      "node",
      "runnerImage",
      "sdk",
      "toolPaths",
      "toolSHA256",
      "xcode",
    ],
    "Apple toolchain evidence",
  );
  for (const [name, version] of Object.entries(value)) {
    if (name === "toolPaths" || name === "toolSHA256") {
      continue;
    }
    requireNonemptyString(version, `${name} Apple toolchain evidence`, 16 << 10);
  }
  const toolNames = [
    "clang",
    "codesign",
    "ditto",
    "hdiutil",
    "lipo",
    "security",
    "spctl",
    "xcrun",
  ];
  requireExactKeys(value.toolPaths, toolNames, "Apple tool paths");
  requireDigestObject(value.toolSHA256, toolNames, "Apple tool digests");
  for (const path of Object.values(value.toolPaths)) {
    requireNonemptyString(path, "Apple tool path", 16 << 10);
  }
  if (
    value.developerDirectory !== macOSDistributionPolicy.developerDirectory ||
    value.xcode !== macOSDistributionPolicy.xcodeVersion ||
    value.sdk !== macOSDistributionPolicy.xcodeSDKVersion ||
    !/^15\./u.test(value.macOS) ||
    !/^24[A-Z0-9]+$/u.test(value.macOSBuild) ||
    !/^macos15\//u.test(value.runnerImage) ||
    value.node !== "v22.23.1" ||
    !value.clang.startsWith("Apple clang version 16.0.0 ") ||
    value.lipo !== macOSDistributionPolicy.lipoIdentity ||
    value.codesign !== "/usr/bin/codesign" ||
    value.hdiutil !== "/usr/bin/hdiutil" ||
    !value.toolPaths.clang.startsWith(
      `${macOSDistributionPolicy.developerDirectory}/`,
    ) ||
    !value.toolPaths.lipo.startsWith(
      `${macOSDistributionPolicy.developerDirectory}/`,
    ) ||
    value.toolPaths.codesign !== "/usr/bin/codesign" ||
    value.toolPaths.ditto !== "/usr/bin/ditto" ||
    value.toolPaths.hdiutil !== "/usr/bin/hdiutil" ||
    value.toolPaths.security !== "/usr/bin/security" ||
    value.toolPaths.spctl !== "/usr/sbin/spctl" ||
    value.toolPaths.xcrun !== "/usr/bin/xcrun"
  ) {
    throw new Error("The Apple build/sign/notary toolchain is not admitted");
  }
  return value;
}

export function validateSigningTransformationEvidence(value) {
  requireExactKeys(
    value,
    [
      "candidate",
      "codeSigning",
      "createdAt",
      "schema",
      "tools",
      "unsignedApplicationLedger",
    ],
    "macOS signing transformation evidence",
  );
  requireExactKeys(
    value.candidate,
    [
      "app",
      "buildManifestSHA256",
      "diskImage",
      "diskImageSHA256",
      "signedApplicationTreeSHA256",
      "signedExecutableSHA256",
      "sourceRevision",
      "toolingRevision",
      "unsignedApplicationTreeSHA256",
      "unsignedArchiveFilename",
      "unsignedArchiveSHA256",
      "unsignedExecutableSHA256",
      "unsignedSidecarSHA256",
    ],
    "macOS signing transformation candidate",
  );
  requireExactKeys(
    value.codeSigning,
    ["certificateSHA256", "teamIdentifier"],
    "macOS signing transformation identity",
  );
  requireDigestObject(
    value.candidate.unsignedExecutableSHA256,
    macOSDistributionPolicy.codeObjectNames,
    "Unsigned executable digests",
  );
  requireDigestObject(
    value.candidate.signedExecutableSHA256,
    macOSDistributionPolicy.codeObjectNames,
    "Signed executable digests",
  );
  requireDigestObject(
    value.candidate.unsignedSidecarSHA256,
    ["vibermate", "vibermated"],
    "Unsigned sidecar digests",
  );
  for (const name of macOSDistributionPolicy.codeObjectNames) {
    if (
      value.candidate.unsignedExecutableSHA256[name] ===
      value.candidate.signedExecutableSHA256[name]
    ) {
      throw new Error("Developer ID signing did not transform every executable");
    }
  }
  validateTreeLedger(
    value.unsignedApplicationLedger,
    "Unsigned application tree ledger",
  );
  const expectedApp =
    `${macOSDistributionPolicy.releaseRelativeDirectory}/bundle/macos/ViberMate.app`;
  const expectedDMG =
    `${macOSDistributionPolicy.releaseRelativeDirectory}/bundle/dmg/ViberMate_0.1.0_universal.dmg`;
  if (
    value.schema !== macOSDistributionPolicy.signingEvidenceSchema ||
    typeof value.createdAt !== "string" ||
    Number.isNaN(Date.parse(value.createdAt)) ||
    value.candidate.app !== expectedApp ||
    value.candidate.diskImage !== expectedDMG ||
    value.candidate.unsignedArchiveFilename !==
      macOSDistributionPolicy.unsignedAppArchiveFilename ||
    !fullGitRevisionPattern.test(value.candidate.sourceRevision ?? "") ||
    !fullGitRevisionPattern.test(value.candidate.toolingRevision ?? "") ||
    !sha256Pattern.test(value.candidate.unsignedArchiveSHA256 ?? "") ||
    !sha256Pattern.test(
      value.candidate.unsignedApplicationTreeSHA256 ?? "",
    ) ||
    value.candidate.unsignedApplicationTreeSHA256 !==
      treeLedgerSHA256(value.unsignedApplicationLedger) ||
    !sha256Pattern.test(value.candidate.signedApplicationTreeSHA256 ?? "") ||
    !sha256Pattern.test(value.candidate.buildManifestSHA256 ?? "") ||
    !sha256Pattern.test(value.candidate.diskImageSHA256 ?? "")
  ) {
    throw new Error("macOS signing transformation evidence is invalid");
  }
  requireCertificateSHA256(value.codeSigning.certificateSHA256);
  requireAppleTeamID(value.codeSigning.teamIdentifier);
  validateAppleToolchainEvidence(value.tools);
  return value;
}

export function notarizationCredentialsFromEnvironment(environment) {
  const allowedAppleNames = new Set([
    "APPLE_API_ISSUER",
    "APPLE_API_KEY",
    "APPLE_API_KEY_PATH",
  ]);
  for (const name of Object.keys(environment)) {
    if (
      nonemptyEnvironmentValue(environment, name) &&
      ((name.startsWith("APPLE_") && !allowedAppleNames.has(name)) ||
        name.startsWith("TAURI_SIGNING_") ||
        name === "API_PRIVATE_KEYS_DIR" ||
        signingConfigurationNames.has(name))
    ) {
      throw new Error(
        "The notarization stage refuses Apple ID, certificate, signing, and updater credentials",
      );
    }
  }
  const keyID = environment.APPLE_API_KEY?.trim();
  const issuerID = environment.APPLE_API_ISSUER?.trim();
  const keyPath = environment.APPLE_API_KEY_PATH?.trim();
  const teamID = environment.VIBERMATE_APPLE_TEAM_ID?.trim();
  const runnerTemp = environment.RUNNER_TEMP?.trim();
  if (!appleAPIKeyIDPattern.test(keyID ?? "")) {
    throw new Error("APPLE_API_KEY must be an App Store Connect API key ID");
  }
  if (!uuidPattern.test(issuerID ?? "")) {
    throw new Error("APPLE_API_ISSUER must be a UUID");
  }
  if (
    typeof keyPath !== "string" ||
    !isAbsolute(keyPath) ||
    normalize(keyPath) !== keyPath ||
    keyPath.includes("\0") ||
    typeof runnerTemp !== "string" ||
    !isAbsolute(runnerTemp) ||
    normalize(runnerTemp) !== runnerTemp ||
    !keyPath.startsWith(`${runnerTemp}/vibermate-notary-`) ||
    !keyPath.endsWith("/AuthKey.p8")
  ) {
    throw new Error("APPLE_API_KEY_PATH must be a normalized absolute path");
  }
  requireAppleTeamID(teamID);
  return Object.freeze({ issuerID, keyID, keyPath, teamID });
}

export function extractSubmissionIDForLog(value) {
  requirePlainObject(value, "notarytool submit result");
  if (!uuidPattern.test(value.id ?? "")) {
    throw new Error("notarytool did not return a submission UUID");
  }
  return value.id.toLowerCase();
}

export function validateNotarySubmitResult(value) {
  requireExactKeys(
    value,
    ["id", "message", "status"],
    "notarytool submit result",
  );
  const id = extractSubmissionIDForLog(value);
  requireNonemptyString(value.message, "notarytool submit message", 1024);
  if (value.status !== "Accepted") {
    throw new Error("The Apple notary service did not accept the candidate");
  }
  return Object.freeze({ id, message: value.message, status: value.status });
}

function validateNotaryTicketContents(value, archiveFilename) {
  const requiredTickets = new Set([`${archiveFilename}\0`]);
  const admittedTickets = new Map([[`${archiveFilename}\0`, "disk-image\0"]]);
  const appPrefix = `${archiveFilename}/${macOSDistributionPolicy.appBundleName}`;
  for (const [name, codeObject] of Object.entries(
    macOSDistributionPolicy.codeObjects,
  )) {
    for (const architecture of macOSDistributionPolicy.architectures) {
      const canonicalIdentity = `${name}\0${architecture}`;
      const requiredPath = `${appPrefix}/${codeObject.relativePath}`;
      requiredTickets.add(`${requiredPath}\0${architecture}`);
      admittedTickets.set(`${requiredPath}\0${architecture}`, canonicalIdentity);
      for (const alias of macOSDistributionPolicy.notaryTicketAliases[name] ?? []) {
        const aliasPath = alias === "." ? appPrefix : `${appPrefix}/${alias}`;
        admittedTickets.set(`${aliasPath}\0${architecture}`, canonicalIdentity);
      }
    }
  }
  if (!Array.isArray(value) || value.length < requiredTickets.size) {
    throw new Error("The Apple notary log ticket contents are invalid");
  }
  const actualTickets = new Set();
  const codeDirectoryHashes = new Map();
  for (const [index, ticket] of value.entries()) {
    const label = `Apple notary log ticket ${index}`;
    requirePlainObject(ticket, label);
    const hasArchitecture = Object.hasOwn(ticket, "arch");
    requireExactKeys(
      ticket,
      hasArchitecture
        ? ["arch", "cdhash", "digestAlgorithm", "path"]
        : ["cdhash", "digestAlgorithm", "path"],
      label,
    );
    const ticketPath = requireNonemptyString(ticket.path, `${label} path`);
    if (
      ticketPath.startsWith("/") ||
      ticketPath.split("/").some((component) => component === "..") ||
      ticket.digestAlgorithm !== "SHA-256" ||
      !cdhashPattern.test(ticket.cdhash ?? "")
    ) {
      throw new Error(`${label} is invalid`);
    }
    if (hasArchitecture) {
      if (!macOSDistributionPolicy.architectures.includes(ticket.arch)) {
        throw new Error(`${label} has an unexpected architecture`);
      }
    }
    const ticketIdentity = `${ticket.path}\0${hasArchitecture ? ticket.arch : ""}`;
    const canonicalIdentity = admittedTickets.get(ticketIdentity);
    if (canonicalIdentity === undefined) {
      throw new Error(`${label} is not an expected code-directory ticket`);
    }
    const observedHash = codeDirectoryHashes.get(canonicalIdentity);
    if (observedHash !== undefined && observedHash !== ticket.cdhash) {
      throw new Error(`${label} conflicts with another ticket for the same code object`);
    }
    codeDirectoryHashes.set(canonicalIdentity, ticket.cdhash);
    actualTickets.add(ticketIdentity);
  }
  if ([...requiredTickets].some((ticket) => !actualTickets.has(ticket))) {
    throw new Error(
      "The Apple notary log does not ticket every embedded executable slice",
    );
  }
}

export function validateNotaryLog(
  value,
  { archiveFilename, preStapleSHA256, submissionID },
) {
  requireExactKeys(
    value,
    [
      "archiveFilename",
      "issues",
      "jobId",
      "logFormatVersion",
      "sha256",
      "status",
      "statusCode",
      "statusSummary",
      "ticketContents",
      "uploadDate",
    ],
    "Apple notary log",
  );
  if (!uuidPattern.test(submissionID ?? "")) {
    throw new Error("The expected notary submission UUID is invalid");
  }
  if (!sha256Pattern.test(preStapleSHA256 ?? "")) {
    throw new Error("The pre-staple disk image digest is invalid");
  }
  requireNonemptyString(archiveFilename, "Notarized archive filename", 255);
  if (
    value.logFormatVersion !== 1 ||
    value.jobId?.toLowerCase() !== submissionID.toLowerCase() ||
    value.status !== "Accepted" ||
    value.statusCode !== 0 ||
    value.statusSummary !== "Ready for distribution" ||
    value.archiveFilename !== archiveFilename ||
    value.sha256 !== preStapleSHA256
  ) {
    throw new Error("The Apple notary log does not match the submission");
  }
  if (
    typeof value.uploadDate !== "string" ||
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/u.test(
      value.uploadDate,
    ) ||
    Number.isNaN(Date.parse(value.uploadDate))
  ) {
    throw new Error("The Apple notary log upload date is invalid");
  }
  if (
    value.issues !== null &&
    (!Array.isArray(value.issues) || value.issues.length !== 0)
  ) {
    throw new Error("The Apple notary log contains an error or warning");
  }
  validateNotaryTicketContents(value.ticketContents, archiveFilename);
  return Object.freeze({
    jobID: value.jobId.toLowerCase(),
    logFormatVersion: value.logFormatVersion,
    status: value.status,
    statusCode: value.statusCode,
  });
}

function commandSucceeded(result) {
  return (
    result !== null &&
    typeof result === "object" &&
    Number.isInteger(result.status) &&
    result.status === 0 &&
    result.error === undefined &&
    result.signal === null
  );
}

export function validateStapleResults(staple, validate) {
  if (!commandSucceeded(staple)) {
    throw new Error("stapler failed to staple the disk image");
  }
  if (!commandSucceeded(validate)) {
    throw new Error("stapler did not validate the stapled disk image");
  }
}

export function validateGatekeeperAssessment(
  source,
  expectedTeamID,
  label = "Gatekeeper assessment",
) {
  requireAppleTeamID(expectedTeamID);
  requireNonemptyString(source, label, 1 << 20);
  const lines = source.split(/\r?\n/u).map((line) => line.trim());
  if (
    !lines.some((line) => line.endsWith(": accepted")) ||
    !lines.includes("source=Notarized Developer ID") ||
    !lines.some((line) =>
      new RegExp(
        `^origin=Developer ID Application: .+ \\(${expectedTeamID}\\)$`,
        "u",
      ).test(line),
    )
  ) {
    throw new Error(`${label} is not an accepted Notarized Developer ID`);
  }
}

export function requireUnchangedDigest(before, after, label) {
  if (
    !sha256Pattern.test(before ?? "") ||
    !sha256Pattern.test(after ?? "") ||
    before !== after
  ) {
    throw new Error(`${label} changed during notarization`);
  }
}

export function validateNotarizationEvidence(value) {
  requireExactKeys(
    value,
    [
      "candidate",
      "codeSigning",
      "createdAt",
      "notarization",
      "schema",
      "tools",
    ],
    "macOS notarization evidence",
  );
  requireExactKeys(
    value.candidate,
    [
      "app",
      "applicationTreeSHA256",
      "architectures",
      "buildManifestSHA256",
      "bundleIdentifier",
      "diskImage",
      "finalSHA256",
      "minimumSystemVersion",
      "preStapleSHA256",
      "sourceCommitTime",
      "sourceRevision",
      "signingEvidenceSHA256",
      "toolingRevision",
      "unsignedArchiveSHA256",
      "version",
    ],
    "macOS notarization candidate evidence",
  );
  requireExactKeys(
    value.codeSigning,
    ["certificateSHA256", "teamIdentifier"],
    "macOS code-signing evidence",
  );
  requireExactKeys(
    value.notarization,
    [
      "developerLogFile",
      "developerSubmitFile",
      "deliveryArtifact",
      "diskImageStapled",
      "logFormatVersion",
      "logSHA256",
      "outsideApplicationStapled",
      "status",
      "statusCode",
      "submissionID",
      "submitSHA256",
      "ticketedCodeDirectories",
    ],
    "macOS Apple notarization evidence",
  );
  requireExactKeys(
    value.tools,
    [
      "clang",
      "codesign",
      "developerDirectory",
      "hdiutil",
      "lipo",
      "macOS",
      "macOSBuild",
      "node",
      "notarytool",
      "notarytoolPath",
      "notarytoolSHA256",
      "runnerImage",
      "sdk",
      "stapler",
      "staplerSHA256",
      "toolPaths",
      "toolSHA256",
      "xcode",
    ],
    "macOS notarization tool versions",
  );
  const expectedArchitectures = [...macOSDistributionPolicy.architectures];
  const expectedApp =
    `${macOSDistributionPolicy.releaseRelativeDirectory}/bundle/macos/ViberMate.app`;
  const expectedDMG =
    `${macOSDistributionPolicy.releaseRelativeDirectory}/bundle/dmg/${macOSDistributionPolicy.diskImageFilename}`;
  if (
    value.schema !== macOSDistributionPolicy.evidenceSchema ||
    typeof value.createdAt !== "string" ||
    Number.isNaN(Date.parse(value.createdAt)) ||
    value.candidate.bundleIdentifier !==
      macOSDistributionPolicy.appIdentifier ||
    value.candidate.version !== macOSDistributionPolicy.appVersion ||
    value.candidate.minimumSystemVersion !==
      macOSDistributionPolicy.minimumSystemVersion ||
    !Array.isArray(value.candidate.architectures) ||
    value.candidate.architectures.length !== expectedArchitectures.length ||
    value.candidate.architectures.some(
      (architecture, index) => architecture !== expectedArchitectures[index],
    ) ||
    !fullGitRevisionPattern.test(value.candidate.sourceRevision ?? "") ||
    !fullGitRevisionPattern.test(value.candidate.toolingRevision ?? "") ||
    typeof value.candidate.sourceCommitTime !== "string" ||
    Number.isNaN(Date.parse(value.candidate.sourceCommitTime)) ||
    !sha256Pattern.test(value.candidate.applicationTreeSHA256 ?? "") ||
    !sha256Pattern.test(value.candidate.buildManifestSHA256 ?? "") ||
    !sha256Pattern.test(value.candidate.signingEvidenceSHA256 ?? "") ||
    !sha256Pattern.test(value.candidate.unsignedArchiveSHA256 ?? "") ||
    !sha256Pattern.test(value.candidate.preStapleSHA256 ?? "") ||
    !sha256Pattern.test(value.candidate.finalSHA256 ?? "") ||
    value.candidate.preStapleSHA256 === value.candidate.finalSHA256 ||
    value.candidate.app !== expectedApp ||
    value.candidate.diskImage !== expectedDMG
  ) {
    throw new Error("macOS notarization candidate evidence is invalid");
  }
  requireCertificateSHA256(value.codeSigning.certificateSHA256);
  requireAppleTeamID(value.codeSigning.teamIdentifier);
  if (
    !uuidPattern.test(value.notarization.submissionID ?? "") ||
    value.notarization.developerLogFile !==
      macOSDistributionPolicy.notaryLogFilename ||
    value.notarization.developerSubmitFile !==
      macOSDistributionPolicy.notarySubmitFilename ||
    value.notarization.deliveryArtifact !== "diskImage" ||
    value.notarization.diskImageStapled !== true ||
    value.notarization.logFormatVersion !== 1 ||
    !sha256Pattern.test(value.notarization.logSHA256 ?? "") ||
    !sha256Pattern.test(value.notarization.submitSHA256 ?? "") ||
    value.notarization.outsideApplicationStapled !== false ||
    value.notarization.status !== "Accepted" ||
    value.notarization.statusCode !== 0 ||
    value.notarization.ticketedCodeDirectories !==
      macOSDistributionPolicy.notaryTicketedCodeDirectoryCount
  ) {
    throw new Error("macOS Apple notarization evidence is invalid");
  }
  validateAppleToolchainEvidence({
    clang: value.tools.clang,
    codesign: value.tools.codesign,
    developerDirectory: value.tools.developerDirectory,
    hdiutil: value.tools.hdiutil,
    lipo: value.tools.lipo,
    macOS: value.tools.macOS,
    macOSBuild: value.tools.macOSBuild,
    node: value.tools.node,
    runnerImage: value.tools.runnerImage,
    sdk: value.tools.sdk,
    toolPaths: value.tools.toolPaths,
    toolSHA256: value.tools.toolSHA256,
    xcode: value.tools.xcode,
  });
  requireNonemptyString(value.tools.notarytool, "notarytool version", 16 << 10);
  if (
    value.tools.notarytoolPath !==
      `${macOSDistributionPolicy.developerDirectory}/usr/bin/notarytool` ||
    !sha256Pattern.test(value.tools.notarytoolSHA256 ?? "") ||
    value.tools.stapler !==
      `${macOSDistributionPolicy.developerDirectory}/usr/bin/stapler` ||
    !sha256Pattern.test(value.tools.staplerSHA256 ?? "")
  ) {
    throw new Error("The notarization stapler tool is not admitted");
  }
  return value;
}
