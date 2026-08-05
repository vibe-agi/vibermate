import assert from "node:assert/strict";
import { test } from "node:test";
import {
  exactApplicationCopyArguments,
  installedCandidatePathsFromEnvironment,
  macOSInstalledEvidencePolicy,
  readOnlyAttachArguments,
  rejectAppleDistributionCredentials,
  validateDesktopSmokeEvidence,
  validateInstalledCandidateReport,
  validateMountedDiskImageReadOnlyFlags,
} from "./macos-installed-candidate-evidence.mjs";
import { validateIndependentInstalledCandidateReport } from "./verify-macos-installed-candidate-evidence.mjs";

const candidateRevision = "a".repeat(40);
const toolingRevision = "b".repeat(40);
const teamIdentifier = "ABCDE12345";

function installedReport() {
  return {
    schema: macOSInstalledEvidencePolicy.schema,
    createdAt: "2026-08-03T12:00:00.000Z",
    candidate: {
      applicationTreeSHA256: "1".repeat(64),
      architectures: ["arm64", "x86_64"],
      buildManifestSHA256: "2".repeat(64),
      bundleIdentifier: "io.vibermate.desktop",
      certificateSHA256: "3".repeat(64),
      diskImageFilename: "ViberMate_0.1.0_universal.dmg",
      diskImageSHA256: "4".repeat(64),
      minimumSystemVersion: "14.0",
      notarizationEvidenceSHA256: "5".repeat(64),
      signingEvidenceSHA256: "6".repeat(64),
      sourceRevision: candidateRevision,
      teamIdentifier,
      toolingRevision,
      version: "0.1.0",
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
      installedRelativePath: "Applications/ViberMate.app",
      source: "read-only-mounted-dmg",
      stateRemovedAfterVerification: true,
    },
    launch: {
      gracefulExit: true,
      isolatedHome: true,
      isolatedHomeRemovedAfterVerification: true,
      launches: 2,
      navigationPersistence: true,
      readiness: "launcher-discovery-and-router-mounted",
      status: "passed",
    },
    notarization: {
      status: "Accepted",
      submissionID: "12345678-1234-1234-1234-123456789abc",
      ticketedCodeDirectories: 6,
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

function environment() {
  const runnerTemp = "/private/runner-temp";
  return {
    RUNNER_TEMP: runnerTemp,
    VIBERMATE_CANDIDATE_REVISION: candidateRevision,
    VIBERMATE_TOOLING_REVISION: toolingRevision,
    VIBERMATE_NOTARIZED_DOWNLOAD_DIRECTORY: `${runnerTemp}/vibermate-notarized-download-1-1`,
    VIBERMATE_INSTALL_ROOT: `${runnerTemp}/vibermate-install-root-1-1`,
    VIBERMATE_INSTALL_MOUNT_DIRECTORY: `${runnerTemp}/vibermate-install-mount-1-1`,
    VIBERMATE_INSTALL_HOME: `${runnerTemp}/vibermate-install-home-1-1`,
    VIBERMATE_INSTALL_STATE_DIRECTORY: `${runnerTemp}/vibermate-install-state-1-1`,
    VIBERMATE_INSTALL_EVIDENCE_DIRECTORY: `${runnerTemp}/vibermate-installed-evidence-1-1`,
    VIBERMATE_INSTALL_SMOKE_BINARY: `${runnerTemp}/vibermate-installed-smoke-1-1`,
  };
}

test("producer and independent verifier accept the same closed report", () => {
  const report = installedReport();
  assert.equal(validateInstalledCandidateReport(report), report);
  assert.equal(validateIndependentInstalledCandidateReport(report), report);
  assert.equal(
    validateInstalledCandidateReport(report, {
      diskImageSHA256: "4".repeat(64),
      sourceRevision: candidateRevision,
      teamIdentifier,
    }),
    report,
  );
  const serialized = JSON.stringify(report);
  assert.doesNotMatch(serialized, /private\/runner-temp|GITHUB_TOKEN|APPLE_API/u);
});

test("both report validators reject extensions and weakened cleanup claims", () => {
  for (const mutate of [
    (report) => {
      report.extra = true;
    },
    (report) => {
      report.installation.installRootRemovedAfterVerification = false;
    },
    (report) => {
      report.launch.launches = 1;
    },
    (report) => {
      report.limitations.uninstall = "verified";
    },
    (report) => {
      report.candidate.architectures.reverse();
    },
  ]) {
    const report = structuredClone(installedReport());
    mutate(report);
    assert.throws(() => validateInstalledCandidateReport(report));
    assert.throws(() => validateIndependentInstalledCandidateReport(report));
  }
});

test("installed smoke evidence is exact and closed", () => {
  const smoke = {
    schema: macOSInstalledEvidencePolicy.smokeSchema,
    status: "passed",
    launches: 2,
    readiness: "launcher-discovery-and-router-mounted",
    navigationPersistence: true,
    gracefulExit: true,
    isolatedHome: true,
  };
  assert.equal(validateDesktopSmokeEvidence(smoke), smoke);
  smoke.pid = 123;
  assert.throws(() => validateDesktopSmokeEvidence(smoke), /unexpected shape/u);
});

test("all mutable paths are distinct direct runner children", () => {
  const paths = installedCandidatePathsFromEnvironment(environment());
  assert.equal(
    paths.installedAppPath,
    "/private/runner-temp/vibermate-install-root-1-1/Applications/ViberMate.app",
  );
  assert.equal(
    paths.smokeReportPath,
    "/private/runner-temp/vibermate-install-state-1-1/desktop-smoke.json",
  );

  for (const [name, value] of [
    ["VIBERMATE_INSTALL_ROOT", "/Applications"],
    [
      "VIBERMATE_INSTALL_HOME",
      "/private/runner-temp/nested/vibermate-install-home-1-1",
    ],
    ["VIBERMATE_INSTALL_STATE_DIRECTORY", "/private/runner-temp"],
  ]) {
    assert.throws(
      () =>
        installedCandidatePathsFromEnvironment({
          ...environment(),
          [name]: value,
        }),
      /outside its admitted runner path/u,
    );
  }
});

test("Apple distribution credentials are rejected by name", () => {
  for (const name of [
    "APPLE_API_PRIVATE_KEY",
    "APPLE_SIGNING_IDENTITY",
    "TAURI_SIGNING_PRIVATE_KEY",
    "SIGNING_KEYCHAIN_PATH",
    "PROVISIONING_PROFILE_SPECIFIER",
  ]) {
    assert.throws(
      () => rejectAppleDistributionCredentials({ [name]: "secret" }),
      new RegExp(name, "u"),
    );
  }
  assert.doesNotThrow(() =>
    rejectAppleDistributionCredentials({
      GITHUB_TOKEN: "GitHub actions retain their standard job token",
      VIBERMATE_APPLE_TEAM_ID: teamIdentifier,
    }),
  );
});

test("mount and copy commands use the admitted read-only exact-copy profiles", () => {
  assert.deepEqual(readOnlyAttachArguments("/tmp/candidate.dmg", "/tmp/mount"), [
    "attach",
    "/tmp/candidate.dmg",
    "-readonly",
    "-nobrowse",
    "-noautoopen",
    "-mountpoint",
    "/tmp/mount",
    "-plist",
  ]);
  assert.deepEqual(exactApplicationCopyArguments("/tmp/source", "/tmp/target"), [
    "--norsrc",
    "--noextattr",
    "--noacl",
    "--noqtn",
    "-X",
    "/tmp/source",
    "/tmp/target",
  ]);
  assert.deepEqual(
    validateMountedDiskImageReadOnlyFlags({
      Writable: "false",
      WritableMedia: "false",
      WritableVolume: "false",
    }),
    {
      Writable: "false",
      WritableMedia: "false",
      WritableVolume: "false",
    },
  );
  for (const name of ["Writable", "WritableMedia", "WritableVolume"]) {
    assert.throws(() =>
      validateMountedDiskImageReadOnlyFlags({
        Writable: "false",
        WritableMedia: "false",
        WritableVolume: "false",
        [name]: "true",
      }),
    );
  }
});
