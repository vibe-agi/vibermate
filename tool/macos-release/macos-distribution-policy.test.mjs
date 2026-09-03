import assert from "node:assert/strict";
import test from "node:test";
import { flutterDesktopBuildConfigurationNames } from "../../ui/flutter_app/tool/desktop_build_manifest.mjs";
import {
  extractSubmissionIDForLog,
  macOSDistributionPolicy,
  notarizationCredentialsFromEnvironment,
  parseClosedJSONObject,
  releaseRevisionsFromEnvironment,
  requireNoGetTaskAllow,
  treeLedgerSHA256,
  validateAppleToolchainEvidence,
  validateCodesignMetadata,
  validateClosedEntitlements,
  validateDiskImageFormat,
  validateEmbeddedBuildManifest,
  validateGatekeeperAssessment,
  validateInfoPlist,
  validateLipoArchitectures,
  validateMachOInventory,
  validateMountedDMGTopLevel,
  validateNotaryLog,
  validateNotarizationEvidence,
  validateNotarySubmitResult,
  validateSigningTransformationEvidence,
  validateStapleResults,
  validateTreeLedgerEquality,
} from "./macos-distribution-policy.mjs";

const teamID = "A1B2C3D4E5";
const submissionID = "12345678-1234-4abc-8def-1234567890ab";
const archiveFilename = "ViberMate_0.1.0_universal.dmg";
const preStapleSHA256 = "a".repeat(64);

function admittedAppleTools() {
  const toolPaths = {
    clang:
      "/Applications/Xcode_16.2.app/Contents/Developer/Toolchains/XcodeDefault.xctoolchain/usr/bin/clang",
    codesign: "/usr/bin/codesign",
    ditto: "/usr/bin/ditto",
    hdiutil: "/usr/bin/hdiutil",
    lipo:
      "/Applications/Xcode_16.2.app/Contents/Developer/Toolchains/XcodeDefault.xctoolchain/usr/bin/lipo",
    security: "/usr/bin/security",
    spctl: "/usr/sbin/spctl",
    xcrun: "/usr/bin/xcrun",
  };
  return {
    clang: "Apple clang version 16.0.0 (clang-1600.0.26.6)",
    codesign: "/usr/bin/codesign",
    developerDirectory: "/Applications/Xcode_16.2.app/Contents/Developer",
    hdiutil: "/usr/bin/hdiutil",
    lipo: macOSDistributionPolicy.lipoIdentity,
    macOS: "15.7.5",
    macOSBuild: "24G617",
    node: "v22.23.1",
    runnerImage: "macos15/20260428.0039.1",
    sdk: "15.2",
    toolPaths,
    toolSHA256: Object.fromEntries(
      Object.keys(toolPaths).map((name, index) => [
        name,
        (index + 1).toString(16).repeat(64),
      ]),
    ),
    xcode: "Xcode 16.2\nBuild version 16C5032a",
  };
}

function developerIDMetadata(overrides = {}) {
  const team = overrides.team ?? teamID;
  const flags = overrides.flags ?? "0x10000(runtime)";
  const signature = overrides.signature ?? "8991 bytes";
  return [
    "Executable=/candidate/ViberMate.app/Contents/MacOS/vibermate-desktop",
    `Identifier=${overrides.identifier ?? macOSDistributionPolicy.appIdentifier}`,
    `CodeDirectory v=20500 size=123 flags=${flags} hashes=4+7 location=embedded`,
    `Signature size=${signature}`,
    `Authority=Developer ID Application: ViberMate (${team})`,
    "Authority=Developer ID Certification Authority",
    "Authority=Apple Root CA",
    "Timestamp=Aug 3, 2026 at 12:00:00 PM",
    `TeamIdentifier=${team}`,
  ].join("\n");
}

function acceptedTicketContents() {
  const tickets = [
    {
      path: archiveFilename,
      digestAlgorithm: "SHA-256",
      cdhash: "1".repeat(40),
    },
  ];
  for (const codeObject of Object.values(macOSDistributionPolicy.codeObjects)) {
    for (const architecture of macOSDistributionPolicy.architectures) {
      tickets.push({
        path: `${archiveFilename}/ViberMate.app/${codeObject.relativePath}`,
        digestAlgorithm: "SHA-256",
        cdhash: "2".repeat(40),
        arch: architecture,
      });
    }
  }
  return tickets;
}

function acceptedNotaryLog(overrides = {}) {
  return {
    logFormatVersion: 1,
    jobId: submissionID,
    status: "Accepted",
    statusSummary: "Ready for distribution",
    statusCode: 0,
    archiveFilename,
    uploadDate: "2026-08-03T12:00:00.123Z",
    sha256: preStapleSHA256,
    ticketContents: acceptedTicketContents(),
    issues: null,
    ...overrides,
  };
}

test("Developer ID metadata requires the team, timestamp, and runtime", () => {
  assert.deepEqual(
    validateCodesignMetadata(developerIDMetadata(), teamID, {
      expectedIdentifier: macOSDistributionPolicy.appIdentifier,
      label: "ViberMate.app",
    }),
    {
      authority: `Developer ID Application: ViberMate (${teamID})`,
      teamIdentifier: teamID,
    },
  );
  assert.throws(() =>
    validateCodesignMetadata(
      developerIDMetadata({ flags: "0x2(adhoc)", signature: "adhoc" }),
      teamID,
    ),
  );
  assert.throws(() =>
    validateCodesignMetadata(developerIDMetadata(), "Z9Y8X7W6V5"),
  );
  assert.throws(() =>
    validateCodesignMetadata(
      developerIDMetadata().replace(
        "Timestamp=Aug 3, 2026 at 12:00:00 PM\n",
        "",
      ),
      teamID,
    ),
  );
});

test("every signed code object has an exact empty entitlement set", () => {
  assert.doesNotThrow(() =>
    requireNoGetTaskAllow(
      "<?xml version=\"1.0\"?><plist version=\"1.0\"><dict></dict></plist>",
    ),
  );
  assert.doesNotThrow(() => validateClosedEntitlements(""));
  assert.throws(() =>
    requireNoGetTaskAllow(
      "<plist><dict><key>com.apple.security.get-task-allow</key><false/></dict></plist>",
    ),
  );
  assert.throws(() =>
    validateClosedEntitlements(
      "<plist version=\"1.0\"><dict><key>com.apple.security.cs.allow-jit</key><true/></dict></plist>",
    ),
  );
});

test("application metadata and Mach-O inventory are fixed", () => {
  assert.doesNotThrow(() =>
    validateInfoPlist({
      bundleExecutable: "vibermate-desktop",
      bundleIdentifier: "io.vibermate.desktop",
      bundleVersion: "0.1.0",
      minimumSystemVersion: "14.0",
      shortVersion: "0.1.0",
    }),
  );
  assert.throws(() =>
    validateInfoPlist({
      bundleExecutable: "vibermate-desktop",
      bundleIdentifier: "io.example.desktop",
      bundleVersion: "0.1.0",
      minimumSystemVersion: "14.0",
      shortVersion: "0.1.0",
    }),
  );
  assert.doesNotThrow(() =>
    validateMachOInventory(
      Object.values(macOSDistributionPolicy.codeObjects).map(
        (codeObject) => codeObject.relativePath,
      ),
    ),
  );
  assert.throws(() =>
    validateMachOInventory([
      "Contents/MacOS/vibermated",
      "Contents/MacOS/vibermate-desktop",
    ]),
  );
  assert.doesNotThrow(() => validateLipoArchitectures("x86_64 arm64\n"));
  assert.throws(() => validateLipoArchitectures("arm64\n"));
  assert.equal(validateDiskImageFormat("UDZO\n"), "UDZO");
  assert.throws(() => validateDiskImageFormat("UDRW\n"));
});

test("notarytool submit JSON is a closed Accepted UUID contract", () => {
  const accepted = {
    id: submissionID,
    message: "Processing complete",
    status: "Accepted",
  };
  assert.equal(validateNotarySubmitResult(accepted).id, submissionID);
  assert.equal(extractSubmissionIDForLog(accepted), submissionID);
  assert.throws(() =>
    validateNotarySubmitResult({ ...accepted, unexpected: true }),
  );
  assert.throws(() =>
    validateNotarySubmitResult({ ...accepted, status: "Invalid" }),
  );
  assert.throws(() =>
    parseClosedJSONObject("not JSON", "notarytool submit result"),
  );
  assert.throws(() =>
    parseClosedJSONObject(
      '{"id":"first","id":"second"}',
      "notarytool submit result",
    ),
  );
  assert.throws(() =>
    parseClosedJSONObject(
      '{"tickets":[{"path":"first","path":"second"}]}',
      "notarytool submit result",
    ),
  );
});

test("Apple log binds the job and pre-staple DMG hash", () => {
  assert.equal(
    validateNotaryLog(acceptedNotaryLog(), {
      archiveFilename,
      preStapleSHA256,
      submissionID,
    }).status,
    "Accepted",
  );
  assert.throws(() =>
    validateNotaryLog(
      acceptedNotaryLog({ sha256: "b".repeat(64) }),
      { archiveFilename, preStapleSHA256, submissionID },
    ),
  );
  assert.throws(() =>
    validateNotaryLog(
      { ...acceptedNotaryLog(), unexpected: true },
      { archiveFilename, preStapleSHA256, submissionID },
    ),
  );
});

test("every Apple notary warning or error blocks stapling", () => {
  assert.throws(() =>
    validateNotaryLog(
      acceptedNotaryLog({
        issues: [
          {
            severity: "warning",
            message: "A future requirement is not satisfied",
          },
        ],
      }),
      { archiveFilename, preStapleSHA256, submissionID },
    ),
  );
});

test("DMG log must ticket every Flutter and Go Universal code object", () => {
  const ticketContents = acceptedTicketContents().filter(
    (ticket) =>
      !(
        ticket.path.endsWith("/Contents/MacOS/vibermated") &&
        ticket.arch === "x86_64"
      ),
  );
  assert.throws(() =>
    validateNotaryLog(
      acceptedNotaryLog({ ticketContents }),
      { archiveFilename, preStapleSHA256, submissionID },
    ),
  );
  const duplicateTickets = acceptedTicketContents();
  duplicateTickets[duplicateTickets.length - 1] = {
    ...duplicateTickets[1],
  };
  assert.throws(() =>
    validateNotaryLog(
      acceptedNotaryLog({ ticketContents: duplicateTickets }),
      { archiveFilename, preStapleSHA256, submissionID },
    ),
  );
});

test("embedded build manifest is clean distribution provenance", () => {
  const revision = "1".repeat(40);
  const manifest = {
    schema: "vibermate.desktop-build/v3",
    source: {
      vcs: "git",
      revision,
      commitTime: "2026-08-03T12:00:00Z",
      dirty: false,
    },
    profiles: {
      desktop: "release",
      sidecars: "release",
      target: "universal-apple-darwin",
      toolkit: "flutter",
    },
    toolchains: {
      go: "go version go1.25.13 darwin/arm64",
      flutter:
        "Flutter 3.41.5 (2c9eb20739dfec95e2c74bd3dfa4601b0a8a36aa)",
      dart: "Dart 3.11.3",
      xcode: "Xcode 16.2\nBuild version 16C5032a",
    },
    configurationSHA256: Object.fromEntries(
      flutterDesktopBuildConfigurationNames.map((name, index) => [
        name,
        ((index % 9) + 1).toString().repeat(64),
      ]),
    ),
    nestedCodeSHA256: {
      "app-framework": "8".repeat(64),
      "flutter-macos-framework": "9".repeat(64),
      vibermate: "a".repeat(64),
      vibermated: "b".repeat(64),
    },
  };
  assert.equal(
    validateEmbeddedBuildManifest(manifest, {
      expectedRevision: revision,
    }).revision,
    revision,
  );
  assert.throws(() =>
    validateEmbeddedBuildManifest(
      { ...manifest, unexpected: true },
      { expectedRevision: revision },
    ),
  );
  assert.throws(() =>
    validateEmbeddedBuildManifest(
      { ...manifest, source: { ...manifest.source, dirty: true } },
      { expectedRevision: revision },
    ),
  );
});

test("mounted DMG shape and full app tree must match exactly", () => {
  assert.doesNotThrow(() =>
    validateMountedDMGTopLevel([
      { name: "ViberMate.app", type: "directory" },
      { name: "Applications", target: "/Applications", type: "symlink" },
    ]),
  );
  assert.throws(() =>
    validateMountedDMGTopLevel([
      { name: "ViberMate.app", type: "directory" },
      { name: "Applications", target: "/tmp/Applications", type: "symlink" },
    ]),
  );
  const ledger = [
    { mode: 0o755, path: ".", type: "directory" },
    {
      mode: 0o755,
      path: "Contents/MacOS/vibermate-desktop",
      sha256: "b".repeat(64),
      size: 1024,
      type: "file",
    },
  ];
  assert.doesNotThrow(() => validateTreeLedgerEquality(ledger, ledger));
  assert.throws(() =>
    validateTreeLedgerEquality(ledger, [
      ledger[0],
      { ...ledger[1], sha256: "c".repeat(64) },
    ]),
  );
});

test("stapler success is not accepted without a successful validate", () => {
  const success = { error: undefined, signal: null, status: 0 };
  assert.doesNotThrow(() => validateStapleResults(success, success));
  assert.throws(() =>
    validateStapleResults(success, {
      error: undefined,
      signal: null,
      status: 1,
    }),
  );
});

test("trusted revisions and Apple tool admission are closed", () => {
  assert.deepEqual(
    releaseRevisionsFromEnvironment({
      VIBERMATE_CANDIDATE_REVISION: "1".repeat(40),
      VIBERMATE_TOOLING_REVISION: "2".repeat(40),
    }),
    {
      candidateRevision: "1".repeat(40),
      toolingRevision: "2".repeat(40),
    },
  );
  assert.throws(() =>
    releaseRevisionsFromEnvironment({
      VIBERMATE_CANDIDATE_REVISION: "main",
      VIBERMATE_TOOLING_REVISION: "2".repeat(40),
    }),
  );
  const tools = admittedAppleTools();
  assert.equal(validateAppleToolchainEvidence(tools), tools);
  assert.throws(() =>
    validateAppleToolchainEvidence({
      ...tools,
      developerDirectory: "/Applications/Xcode.app/Contents/Developer",
    }),
  );
  assert.throws(() =>
    validateAppleToolchainEvidence({
      ...tools,
      toolSHA256: { ...tools.toolSHA256, codesign: "not-a-digest" },
    }),
  );
  assert.throws(() =>
    validateAppleToolchainEvidence({
      ...tools,
      lipo: "Apple Inc. version cctools-1010.6",
    }),
  );
});

test("signing evidence binds the hostile archive to both App ledgers", () => {
  const ledger = [
    { mode: 0o755, path: ".", type: "directory" },
    {
      mode: 0o755,
      path: "Contents/MacOS/vibermate-desktop",
      sha256: "d".repeat(64),
      size: 1024,
      type: "file",
    },
  ];
  const evidence = {
    schema: macOSDistributionPolicy.signingEvidenceSchema,
    createdAt: "2026-08-03T12:30:00Z",
    candidate: {
      app: `${macOSDistributionPolicy.releaseRelativeDirectory}/bundle/macos/ViberMate.app`,
      buildManifestSHA256: "1".repeat(64),
      diskImage:
        `${macOSDistributionPolicy.releaseRelativeDirectory}/bundle/dmg/ViberMate_0.1.0_universal.dmg`,
      diskImageSHA256: "2".repeat(64),
      signedApplicationTreeSHA256: "3".repeat(64),
      signedExecutableSHA256: {
        "app-framework": "1".repeat(64),
        "flutter-macos-framework": "2".repeat(64),
        vibermate: "4".repeat(64),
        "vibermate-desktop": "5".repeat(64),
        vibermated: "6".repeat(64),
      },
      sourceRevision: "7".repeat(40),
      toolingRevision: "8".repeat(40),
      unsignedApplicationTreeSHA256: treeLedgerSHA256(ledger),
      unsignedArchiveFilename: "ViberMate.unsigned.app.vma",
      unsignedArchiveSHA256: "9".repeat(64),
      unsignedExecutableSHA256: {
        "app-framework": "7".repeat(64),
        "flutter-macos-framework": "8".repeat(64),
        vibermate: "a".repeat(64),
        "vibermate-desktop": "b".repeat(64),
        vibermated: "c".repeat(64),
      },
      unsignedSidecarSHA256: {
        vibermate: "a".repeat(64),
        vibermated: "c".repeat(64),
      },
    },
    codeSigning: {
      certificateSHA256: "e".repeat(64),
      teamIdentifier: teamID,
    },
    tools: admittedAppleTools(),
    unsignedApplicationLedger: ledger,
  };
  assert.equal(validateSigningTransformationEvidence(evidence), evidence);
  assert.throws(() =>
    validateSigningTransformationEvidence({
      ...evidence,
      candidate: {
        ...evidence.candidate,
        unsignedArchiveSHA256: "invalid",
      },
    }),
  );
  assert.throws(() =>
    validateSigningTransformationEvidence({
      ...evidence,
      unsignedApplicationLedger: [
        ledger[0],
        { ...ledger[1], sha256: "f".repeat(64) },
      ],
    }),
  );
});

test("notarization accepts only the API-key credential family", () => {
  const environment = {
    APPLE_API_ISSUER: "12345678-1234-4abc-8def-1234567890ab",
    APPLE_API_KEY: "A1B2C3D4E5",
    APPLE_API_KEY_PATH: "/private/runner/vibermate-notary-123/AuthKey.p8",
    RUNNER_TEMP: "/private/runner",
    VIBERMATE_APPLE_TEAM_ID: teamID,
  };
  assert.deepEqual(notarizationCredentialsFromEnvironment(environment), {
    issuerID: environment.APPLE_API_ISSUER,
    keyID: environment.APPLE_API_KEY,
    keyPath: environment.APPLE_API_KEY_PATH,
    teamID,
  });
  for (const [name, value] of [
    ["APPLE_ID", "release@example.test"],
    ["APPLE_PASSWORD", "secret"],
    ["APPLE_CERTIFICATE", "secret"],
    ["APPLE_SIGNING_IDENTITY", "a".repeat(40)],
    ["APPLE_FUTURE_CREDENTIAL", "secret"],
    ["DEVELOPMENT_TEAM", teamID],
    ["OTHER_CODE_SIGN_FLAGS", "--preserve-metadata=entitlements"],
    ["TAURI_SIGNING_PRIVATE_KEY", "secret"],
  ]) {
    assert.throws(() =>
      notarizationCredentialsFromEnvironment({
        ...environment,
        [name]: value,
      }),
    );
  }
});

test("Gatekeeper output is closed over notarized Developer ID", () => {
  assert.doesNotThrow(() =>
    validateGatekeeperAssessment(
      `/candidate/ViberMate.app: accepted\nsource=Notarized Developer ID\norigin=Developer ID Application: ViberMate (${teamID})\n`,
      teamID,
    ),
  );
  assert.throws(() =>
    validateGatekeeperAssessment(
      "/candidate/ViberMate.app: accepted\nsource=Developer ID\n",
      teamID,
    ),
  );
});

test("private evidence has a closed secret-free schema", () => {
  const evidence = {
    schema: macOSDistributionPolicy.evidenceSchema,
    createdAt: "2026-08-03T12:30:00Z",
    candidate: {
      app: `${macOSDistributionPolicy.releaseRelativeDirectory}/bundle/macos/ViberMate.app`,
      applicationTreeSHA256: "1".repeat(64),
      architectures: ["arm64", "x86_64"],
      buildManifestSHA256: "2".repeat(64),
      bundleIdentifier: "io.vibermate.desktop",
      diskImage:
        `${macOSDistributionPolicy.releaseRelativeDirectory}/bundle/dmg/ViberMate_0.1.0_universal.dmg`,
      finalSHA256: "3".repeat(64),
      minimumSystemVersion: "14.0",
      preStapleSHA256,
      signingEvidenceSHA256: "7".repeat(64),
      sourceCommitTime: "2026-08-03T12:00:00Z",
      sourceRevision: "4".repeat(40),
      toolingRevision: "8".repeat(40),
      unsignedArchiveSHA256: "9".repeat(64),
      version: "0.1.0",
    },
    codeSigning: {
      certificateSHA256: "5".repeat(64),
      teamIdentifier: teamID,
    },
    notarization: {
      developerLogFile: "apple-notary-log.json",
      developerSubmitFile: "apple-notary-submit.json",
      deliveryArtifact: "diskImage",
      diskImageStapled: true,
      logFormatVersion: 1,
      logSHA256: "6".repeat(64),
      outsideApplicationStapled: false,
      status: "Accepted",
      statusCode: 0,
      submissionID,
      submitSHA256: "a".repeat(64),
      ticketedCodeDirectories: 10,
    },
    tools: {
      ...admittedAppleTools(),
      notarytool: "1.1.2 (41)",
      notarytoolPath:
        "/Applications/Xcode_16.2.app/Contents/Developer/usr/bin/notarytool",
      notarytoolSHA256: "b".repeat(64),
      stapler:
        "/Applications/Xcode_16.2.app/Contents/Developer/usr/bin/stapler",
      staplerSHA256: "c".repeat(64),
    },
  };
  assert.equal(validateNotarizationEvidence(evidence), evidence);
  assert.throws(() =>
    validateNotarizationEvidence({ ...evidence, apiKey: "secret" }),
  );
  assert.throws(() =>
    validateNotarizationEvidence({
      ...evidence,
      candidate: {
        ...evidence.candidate,
        finalSHA256: evidence.candidate.preStapleSHA256,
      },
    }),
  );
});
