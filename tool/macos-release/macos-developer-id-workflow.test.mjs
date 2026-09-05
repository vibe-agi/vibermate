import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import {
  diskImageCreationArguments,
  signingCommandArguments,
} from "./bundle-macos-distribution-candidate.mjs";
import { macOSDistributionPolicy } from "./macos-distribution-policy.mjs";

const repositoryDirectory = resolve(
  dirname(fileURLToPath(import.meta.url)),
  "../..",
);
const workflow = await readFile(
  resolve(repositoryDirectory, ".github/workflows/macos-developer-id-candidate.yml"),
  "utf8",
);
const distributionBuilder = await readFile(
  resolve(
    repositoryDirectory,
    "ui/flutter_app/tool/build_macos_distribution.sh",
  ),
  "utf8",
);
const candidateVerifier = await readFile(
  resolve(
    repositoryDirectory,
    "tool/macos-release/verify-macos-signed-candidate.mjs",
  ),
  "utf8",
);
const unsignedStart = workflow.indexOf("\n  unsigned:\n");
const evidenceStart = workflow.indexOf("\n  r0_evidence:\n");
const signStart = workflow.indexOf("\n  sign:\n");
const notaryStart = workflow.indexOf("\n  notarize:\n");
const installedEvidenceStart = workflow.indexOf("\n  installed_evidence:\n");
const signJob = workflow.slice(signStart, notaryStart);
const notaryJob = workflow.slice(notaryStart, installedEvidenceStart);
const installedEvidenceJob = workflow.slice(installedEvidenceStart);
const protectedJobs = `${signJob}\n${notaryJob}`;
const unsignedJob = workflow.slice(unsignedStart, evidenceStart);

test("workflow uses the exact current distribution filename", () => {
  const filenames =
    workflow.match(/ViberMate_\d+\.\d+\.\d+_universal\.dmg/gu) ?? [];
  assert.ok(filenames.length > 0);
  assert.ok(
    filenames.every(
      (filename) => filename === macOSDistributionPolicy.diskImageFilename,
    ),
  );
});

test("distribution builds use the pubspec version admitted by release policy", async () => {
  const pubspec = await readFile(
    resolve(repositoryDirectory, "ui/flutter_app/pubspec.yaml"),
    "utf8",
  );
  const version = pubspec.match(/^version: (\d+\.\d+\.\d+)\+(\d+)$/mu);
  assert.deepEqual(version?.slice(1), [
    macOSDistributionPolicy.appVersion,
    macOSDistributionPolicy.appBuildNumber,
  ]);
  assert.doesNotMatch(distributionBuilder, /--build-(?:name|number)/u);
});

test("unsigned candidate build uses only the pinned Flutter desktop toolchain", () => {
  assert.ok(unsignedStart > 0 && evidenceStart > unsignedStart);
  assert.match(unsignedJob, /candidate\/ui\/flutter_app\/tool\/install_ci_flutter\.sh/u);
  assert.match(unsignedJob, /candidate\/ui\/flutter_app\/tool\/build_macos_distribution\.sh/u);
  assert.match(
    unsignedJob,
    /candidate\/ui\/flutter_app\/build\/distribution\/universal-apple-darwin\/release\/bundle\/macos\/ViberMate\.app/u,
  );
  assert.match(
    unsignedJob,
    /candidate\/ui\/flutter_app\/build\/distribution\/universal-apple-darwin\/release\/r0-dist/u,
  );
  assert.doesNotMatch(
    unsignedJob,
    /pnpm|rust-toolchain|exec tauri|src-tauri|candidate\/ui\/desktop/u,
  );
});

test("Universal CGO slices compile against the admitted macOS SDK", () => {
  assert.match(
    distributionBuilder,
    /xcrun --sdk macosx --show-sdk-path/u,
  );
  assert.match(distributionBuilder, /test -d "\$\{macos_sdk_root\}"/u);
  assert.match(
    distributionBuilder,
    /SDKROOT="\$\{macos_sdk_root\}"/u,
  );
  for (const flag of ["CGO_CFLAGS", "CGO_CXXFLAGS", "CGO_LDFLAGS"]) {
    assert.ok(
      distributionBuilder.includes(
        `${flag}="-arch \${clang_architecture} -isysroot \${macos_sdk_root} `,
      ),
    );
  }
});

test("lipo evidence is digest-bound without an unsupported version probe", () => {
  assert.doesNotMatch(candidateVerifier, /lipoPath,\s*\["-version"\]/u);
  assert.match(
    candidateVerifier,
    /lipo: macOSDistributionPolicy\.lipoIdentity/u,
  );
});

test("workflow admits only the default workflow SHA and an ancestor candidate", () => {
  assert.match(workflow, /VIBERMATE_TOOLING_REVISION: \$\{\{ github\.workflow_sha \}\}/u);
  assert.match(workflow, /test "\$\{DISPATCH_REF\}" = "refs\/heads\/\$\{DEFAULT_BRANCH\}"/u);
  assert.match(workflow, /git -C candidate merge-base --is-ancestor/u);
  assert.match(workflow, /fetch-depth: 0/u);
  assert.match(workflow, /fetch-tags: true/u);
  assert.equal((workflow.match(/persist-credentials: false/gu) ?? []).length, 8);
  assert.equal((workflow.match(/test "\$\{REF_PROTECTED\}" = "true"/gu) ?? []).length, 5);
  assert.equal((workflow.match(/test "\$\{WORKFLOW_REF\}" = /gu) ?? []).length, 5);
  assert.equal(
    (workflow.match(/test "\$\(uname -m\)" = "arm64"/gu) ?? []).length,
    5,
  );
  assert.equal((workflow.match(/runs-on: macos-15/gu) ?? []).length, 5);
  assert.match(workflow, /permissions:\n  contents: read/u);
  assert.doesNotMatch(workflow, /id-token:\s*write/u);
  assert.doesNotMatch(workflow, /uncredentialed|without credentials/iu);
  assert.match(workflow, /without Apple distribution credentials/u);
});

test("protected jobs check out and execute trusted tooling only", () => {
  assert.equal((signJob.match(/actions\/checkout@/gu) ?? []).length, 1);
  assert.equal((notaryJob.match(/actions\/checkout@/gu) ?? []).length, 1);
  assert.equal((protectedJobs.match(/ref: \$\{\{ github\.workflow_sha \}\}/gu) ?? []).length, 2);
  assert.doesNotMatch(protectedJobs, /ref: \$\{\{ inputs\.candidate_revision \}\}/u);
  assert.doesNotMatch(
    protectedJobs,
    /\bpnpm\b|setup-go|rust-toolchain|exec tauri|candidate\/ui\/desktop/u,
  );
  assert.doesNotMatch(protectedJobs, /ViberMate\.app\/Contents\/MacOS\//u);
  assert.match(signJob, /node tool\/macos-release\/restore-macos-unsigned-archive\.mjs/u);
  assert.match(signJob, /node tool\/macos-release\/bundle-macos-distribution-candidate\.mjs/u);
  assert.match(notaryJob, /node tool\/macos-release\/restore-macos-signed-transfer\.mjs/u);
});

test("candidate transfer uses the same closed parser, not tar or zip extraction", () => {
  assert.match(workflow, /ViberMate\.unsigned\.app\.vma/u);
  assert.match(workflow, /ViberMate\.signed\.app\.vma/u);
  assert.match(workflow, /vibermate-r0-build-input\.vma/u);
  const syftStart = workflow.indexOf(
    "      - name: Download and authenticate the admitted Syft archive",
  );
  const syftEnd = workflow.indexOf(
    "      - name: Generate source-traceability evidence from inert candidate data (R0)",
  );
  assert.ok(syftStart > 0 && syftEnd > syftStart);
  const syftStep = workflow.slice(syftStart, syftEnd);
  assert.match(
    syftStep,
    /test "\$\{actual_archive_digest\}" = "\$\{SYFT_DARWIN_ARM64_ARCHIVE_SHA256\}"[\s\S]*\/usr\/bin\/tar -xzf "\$\{archive\}" -C "\$\{VIBERMATE_SYFT_DIRECTORY\}" syft/u,
  );
  const candidateTransfers = `${workflow.slice(0, syftStart)}${workflow.slice(syftEnd)}`;
  assert.doesNotMatch(candidateTransfers, /tar -[ctx]|ditto -x|zipinfo|unzip/u);
});

test("fresh source-traceability evidence (R0) executes trusted tooling and gates protected signing", () => {
  assert.ok(evidenceStart > 0 && signStart > evidenceStart);
  const evidenceJob = workflow.slice(evidenceStart, signStart);
  assert.match(signJob, /needs: \[unsigned, r0_evidence\]/u);
  assert.match(evidenceJob, /ref: \$\{\{ github\.workflow_sha \}\}/u);
  assert.match(evidenceJob, /ref: \$\{\{ inputs\.candidate_revision \}\}/u);
  assert.match(evidenceJob, /restore-r0-build-input-transfer\.mjs/u);
  assert.match(evidenceJob, /prepare-r0-release-evidence\.mjs/u);
  assert.match(evidenceJob, /go run \.\/cmd\/vibermate-release-evidence/u);
  assert.match(
    evidenceJob,
    /fresh trusted source-traceability payload and SBOM evidence \(R0\)/u,
  );
  assert.match(
    evidenceJob,
    /Generate source-traceability evidence from inert candidate data \(R0\)/u,
  );
  assert.doesNotMatch(
    evidenceJob,
    /pnpm|rust-toolchain|exec tauri|candidate\/ui\/desktop\/scripts/u,
  );
});

test("credential cleanup is ordered before every protected artifact action", () => {
  const signing = signJob.indexOf("Perform the trusted inside-out signing transformation");
  const signingCleanup = signJob.indexOf("Lock and delete the signing keychain before packaging");
  const signedPackaging = signJob.indexOf("Create the exact signed transfer after credential cleanup");
  const signedUpload = signJob.indexOf("Upload only the signed App archive");
  assert.ok(signing < signingCleanup && signingCleanup < signedPackaging && signedPackaging < signedUpload);

  const notary = notaryJob.indexOf("Submit and staple only the signed DMG");
  const notaryCleanup = notaryJob.indexOf("Delete the App Store Connect key before every artifact action");
  const successfulUpload = notaryJob.indexOf("Upload the stapled DMG and exact evidence files");
  const failedUpload = notaryJob.indexOf("Upload only fixed raw files from a failed Apple attempt");
  assert.ok(notary < notaryCleanup && notaryCleanup < successfulUpload);
  assert.ok(notaryCleanup < failedUpload);
  assert.equal(
    (notaryJob.match(/steps\.credential_cleanup\.outcome == 'success'/gu) ?? [])
      .length,
    3,
  );
});

test("Developer ID signing registers the temporary keychain on the user search list", () => {
  const importIdentity = signJob.indexOf(
    "Import the one protected Developer ID identity",
  );
  const registerKeychain = signJob.indexOf(
    '/usr/bin/security list-keychains -d user -s "${SIGNING_KEYCHAIN_PATH}"',
  );
  const signing = signJob.indexOf(
    "Perform the trusted inside-out signing transformation",
  );
  assert.ok(
    importIdentity >= 0 &&
      registerKeychain > importIdentity &&
      signing > registerKeychain,
  );
});

test("signing and notarization consume separate Hideout-compatible secrets", () => {
  assert.match(
    signJob,
    /secrets\.APPLE_DEVELOPER_ID_P12_BASE64/u,
  );
  assert.match(
    signJob,
    /secrets\.APPLE_DEVELOPER_ID_P12_PASSWORD/u,
  );
  assert.doesNotMatch(signJob, /secrets\.APPLE_NOTARY_/u);

  for (const name of [
    "APPLE_NOTARY_KEY_P8_BASE64",
    "APPLE_NOTARY_KEY_ID",
    "APPLE_NOTARY_ISSUER_ID",
  ]) {
    assert.match(notaryJob, new RegExp(`secrets\\.${name}`, "u"));
  }
  assert.doesNotMatch(notaryJob, /secrets\.APPLE_DEVELOPER_ID_/u);
  assert.doesNotMatch(
    workflow,
    /secrets\.(?:APPLE_CERTIFICATE|APPLE_API_PRIVATE_KEY)/u,
  );
});

test("fresh installed evidence runs without Apple distribution credentials and executes only the notarized App", () => {
  assert.ok(installedEvidenceStart > notaryStart);
  assert.match(
    notaryJob,
    /outputs:\n      artifact_name: \$\{\{ steps\.artifact_name\.outputs\.name \}\}/u,
  );
  assert.match(installedEvidenceJob, /needs: notarize/u);
  assert.doesNotMatch(installedEvidenceJob, /^    environment:/mu);
  assert.equal(
    (installedEvidenceJob.match(/actions\/checkout@/gu) ?? []).length,
    2,
  );
  assert.match(
    installedEvidenceJob,
    /ref: \$\{\{ github\.workflow_sha \}\}[\s\S]*path: trusted-tooling/u,
  );
  assert.match(
    installedEvidenceJob,
    /ref: \$\{\{ inputs\.candidate_revision \}\}[\s\S]*path: candidate/u,
  );
  assert.match(installedEvidenceJob, /Check out the candidate inertly for ancestry only/u);
  assert.match(installedEvidenceJob, /go -C trusted-tooling build -trimpath/u);
  assert.match(
    installedEvidenceJob,
    /node trusted-tooling\/tool\/macos-release\/create-macos-installed-candidate-evidence\.mjs/u,
  );
  assert.match(
    installedEvidenceJob,
    /node trusted-tooling\/tool\/macos-release\/verify-macos-installed-candidate-evidence\.mjs/u,
  );
  assert.doesNotMatch(
    installedEvidenceJob,
    /candidate\/ui\/desktop|pnpm --dir candidate|go -C candidate|APPLE_API_|APPLE_SIGNING_IDENTITY|TAURI_SIGNING_|SIGNING_KEYCHAIN_PATH/u,
  );
  assert.match(
    installedEvidenceJob,
    /name: \$\{\{ needs\.notarize\.outputs\.artifact_name \}\}/u,
  );
  assert.match(
    installedEvidenceJob,
    /signed-package-installation-report\.json[\s\S]*signed-package-installation-report\.sha256/u,
  );
  const upload = installedEvidenceJob.indexOf(
    "Upload only the closed installation report and checksum",
  );
  const cleanup = installedEvidenceJob.indexOf(
    "Remove only the exact transient installation paths",
  );
  assert.ok(upload > 0 && cleanup > upload);
  assert.match(
    installedEvidenceJob,
    /Remove only the exact transient installation paths[\s\S]*if: \$\{\{ always\(\) \}\}/u,
  );
});

test("every artifact identity includes run ID and attempt", () => {
  for (const name of [
    "UNSIGNED_ARTIFACT_NAME",
    "R0_INPUT_ARTIFACT_NAME",
    "R0_EVIDENCE_ARTIFACT_NAME",
    "SIGNED_ARTIFACT_NAME",
    "NOTARIZED_ARTIFACT_NAME",
    "NOTARY_ATTEMPT_ARTIFACT_NAME",
    "INSTALLATION_EVIDENCE_ARTIFACT_NAME",
  ]) {
    assert.match(
      workflow,
      new RegExp(`${name}: [^\\n]+\\$\\{\\{ github\\.run_id \\}\\}[^\\n]+\\$\\{\\{ github\\.run_attempt \\}\\}`, "u"),
    );
  }
});

test("signing commands are closed, inside-out, and entitlement-free", () => {
  const identity = "1".repeat(40);
  const keychain = "/runner/keychain";
  const nested = signingCommandArguments("vibermate", identity, keychain, "/app/vibermate");
  const app = signingCommandArguments("application", identity, keychain, "/app/ViberMate.app");
  const dmg = signingCommandArguments("diskImage", identity, keychain, "/app/ViberMate.dmg");
  for (const arguments_ of [nested, app, dmg]) {
    assert.ok(arguments_.includes("--force"));
    assert.ok(arguments_.includes("--timestamp"));
    assert.ok(arguments_.includes("--keychain"));
    assert.ok(!arguments_.includes("--deep"));
    assert.ok(!arguments_.includes("--entitlements"));
    assert.ok(!arguments_.includes("--preserve-metadata"));
  }
  assert.ok(nested.includes("runtime"));
  assert.ok(app.includes("runtime"));
  assert.ok(!dmg.includes("runtime"));
  assert.throws(() =>
    signingCommandArguments("vibermate-desktop", identity, keychain, "/app/main"),
  );
});

test("DMG creation is fixed read-only UDZO with no overwrite option", () => {
  const arguments_ = diskImageCreationArguments("/staging", "/output/ViberMate.dmg");
  assert.deepEqual(arguments_.slice(0, 5), [
    "create",
    "-srcfolder",
    "/staging",
    "-volname",
    "ViberMate",
  ]);
  assert.ok(arguments_.includes("UDZO"));
  assert.ok(arguments_.includes("HFS+"));
  assert.ok(!arguments_.includes("-ov"));
});
