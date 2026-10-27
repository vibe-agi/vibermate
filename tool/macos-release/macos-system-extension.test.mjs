import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import test from "node:test";
import {
  capabilityEntitlements,
  provisioningInputsFromEnvironment,
  readSigningProfiles,
  validateProvisioningProfile,
} from "./macos-system-extension.mjs";

const teamID = "A1B2C3D4E5";
const certificate = Buffer.from("fixture Developer ID certificate");
const identity = createHash("sha1").update(certificate).digest("hex");
const now = new Date("2026-10-26T00:00:00Z");
function profile(role) {
  return {
    UUID: "12345678-1234-4abc-8def-1234567890ab",
    Platform: ["OSX"],
    TeamIdentifier: [teamID],
    ApplicationIdentifierPrefix: [teamID],
    CreationDate: "2026-01-01T00:00:00Z",
    ExpirationDate: "2027-01-01T00:00:00Z",
    ProvisionsAllDevices: true,
    DeveloperCertificates: [certificate.toString("base64")],
    Entitlements: { ...capabilityEntitlements(role, teamID), "get-task-allow": false },
  };
}

test("explicit signing inputs require both profiles and a certificate selector", () => {
  const environment = {
    APPLE_SIGNING_IDENTITY: identity.toUpperCase(),
    VIBERMATE_APPLE_TEAM_ID: teamID,
    VIBERMATE_MACOS_APP_PROVISION_PROFILE: "/profiles/app.provisionprofile",
    VIBERMATE_MACOS_EXTENSION_PROVISION_PROFILE: "/profiles/extension.provisionprofile",
  };
  const inputs = provisioningInputsFromEnvironment(environment);
  assert.equal(inputs.identity, identity);
  assert.equal(inputs.appProfilePath, environment.VIBERMATE_MACOS_APP_PROVISION_PROFILE);
  assert.equal(inputs.extensionProfilePath, environment.VIBERMATE_MACOS_EXTENSION_PROVISION_PROFILE);
  assert.equal(inputs.keychainPath, undefined);
  for (const name of Object.keys(environment)) {
    assert.throws(() => provisioningInputsFromEnvironment({ ...environment, [name]: "" }));
  }
  assert.throws(() => provisioningInputsFromEnvironment({ ...environment, APPLE_SIGNING_IDENTITY: "-" }));
});

test("a missing profile file fails before bundle preparation", async (t) => {
  const directory = await mkdtemp(resolve(tmpdir(), "vibermate-missing-profile-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  await assert.rejects(readSigningProfiles({
    teamID, identity,
    appProfilePath: resolve(directory, "missing-app.provisionprofile"),
    extensionProfilePath: resolve(directory, "missing-extension.provisionprofile"),
  }), { code: "ENOENT" });
});

test("Developer ID profiles bind each bundle, capability, certificate, team, and validity period", () => {
  for (const role of ["application", "transparent-proxy"]) {
    const options = { role, teamID, identity, now };
    assert.equal(validateProvisioningProfile(profile(role), options).uuid, profile(role).UUID);
    assert.throws(() => validateProvisioningProfile(profile(role), { ...options, identity: "f".repeat(40) }), /certificate/u);
    assert.throws(() => validateProvisioningProfile(profile(role), { ...options, role: role === "application" ? "transparent-proxy" : "application" }), /identifier/u);
    for (const mutate of [
      (value) => { value.TeamIdentifier = ["Z9Y8X7W6V5"]; },
      (value) => { value.ExpirationDate = "2026-10-25T00:00:00Z"; },
      (value) => { value.CreationDate = "2026-10-27T00:00:00Z"; },
      (value) => { value.ProvisionsAllDevices = false; },
      (value) => { value.Entitlements["com.apple.developer.networking.networkextension"] = ["packet-tunnel-provider-systemextension"]; },
      (value) => { value.Entitlements["get-task-allow"] = true; },
    ]) {
      const invalid = profile(role);
      mutate(invalid);
      assert.throws(() => validateProvisioningProfile(invalid, options));
    }
  }
  const missingInstall = profile("application");
  delete missingInstall.Entitlements["com.apple.developer.system-extension.install"];
  assert.throws(() => validateProvisioningProfile(missingInstall, { role: "application", teamID, identity, now }));
});
