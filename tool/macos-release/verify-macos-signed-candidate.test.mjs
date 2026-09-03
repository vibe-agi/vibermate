import assert from "node:assert/strict";
import {
  mkdir,
  mkdtemp,
  realpath,
  rm,
  symlink,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import test from "node:test";
import {
  createCanonicalTemporaryDirectory,
  requireSignedCandidateMatchesSigningEvidence,
} from "./verify-macos-signed-candidate.mjs";

const digest = (character) => character.repeat(64);

function signingEvidenceFixture() {
  return {
    candidate: {
      buildManifestSHA256: digest("1"),
      diskImageSHA256: digest("2"),
      signedApplicationTreeSHA256: digest("3"),
      signedExecutableSHA256: {
        "app-framework": digest("4"),
        "flutter-macos-framework": digest("5"),
        vibermate: digest("6"),
        "vibermate-desktop": digest("7"),
        vibermated: digest("8"),
      },
      sourceRevision: "9".repeat(40),
      toolingRevision: "a".repeat(40),
      unsignedSidecarSHA256: {
        vibermate: digest("b"),
        vibermated: digest("c"),
      },
    },
    codeSigning: {
      certificateSHA256: digest("d"),
      teamIdentifier: "A1B2C3D4E5",
    },
    tools: {
      macOS: "15.7.7",
      macOSBuild: "24G720",
      runnerImage: "macos15/20260727.0256.1",
    },
  };
}

function inspectedCandidateFixture(recorded) {
  return {
    applicationTreeSHA256: recorded.candidate.signedApplicationTreeSHA256,
    certificateSHA256: recorded.codeSigning.certificateSHA256,
    diskImageSHA256: recorded.candidate.diskImageSHA256,
    executableSHA256: recorded.candidate.signedExecutableSHA256,
    manifestSHA256: recorded.candidate.buildManifestSHA256,
    sidecarSHA256: recorded.candidate.unsignedSidecarSHA256,
    sourceRevision: recorded.candidate.sourceRevision,
    teamIdentifier: recorded.codeSigning.teamIdentifier,
    toolingRevision: recorded.candidate.toolingRevision,
    tools: {
      macOS: "15.7.9",
      macOSBuild: "24G830",
      runnerImage: "macos15/20260829.0321.1",
    },
  };
}

test("a signed candidate remains verifiable after the admitted runner patch changes", () => {
  const recorded = signingEvidenceFixture();

  assert.doesNotThrow(() =>
    requireSignedCandidateMatchesSigningEvidence(
      recorded,
      inspectedCandidateFixture(recorded),
    ),
  );
});

test("signed candidate verification still rejects an application tree mismatch", () => {
  const recorded = signingEvidenceFixture();
  const candidate = {
    ...inspectedCandidateFixture(recorded),
    applicationTreeSHA256: digest("e"),
  };

  assert.throws(
    () => requireSignedCandidateMatchesSigningEvidence(recorded, candidate),
    /does not match its trusted signing evidence/u,
  );
});

test("certificate extraction canonicalizes a symlinked temporary-directory parent", async (context) => {
  const fixture = await realpath(
    await mkdtemp(resolve(tmpdir(), "vibermate-canonical-temp-test-")),
  );
  context.after(() => rm(fixture, { recursive: true, force: true }));
  const canonicalParent = resolve(fixture, "canonical");
  const aliasedParent = resolve(fixture, "alias");
  await mkdir(canonicalParent);
  await symlink(canonicalParent, aliasedParent, "dir");

  const directory = await createCanonicalTemporaryDirectory(
    "certificate-",
    aliasedParent,
  );

  assert.equal(directory, await realpath(directory));
  assert.equal(dirname(directory), canonicalParent);
});
