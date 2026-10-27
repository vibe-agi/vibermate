import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { lstat, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { isAbsolute, resolve } from "node:path";

export const transparentProxyBundleIdentifier = "io.vibermate.desktop.transparent-proxy";
export const transparentProxyBundlePath =
  `Contents/Library/SystemExtensions/${transparentProxyBundleIdentifier}.systemextension`;
export const transparentProxyExecutablePath =
  `${transparentProxyBundlePath}/Contents/MacOS/${transparentProxyBundleIdentifier}`;
const networkCapability = "app-proxy-provider-systemextension";

export function capabilityEntitlements(role, teamID) {
  if (!/^[A-Z0-9]{10}$/u.test(teamID ?? "")) {
    throw new Error("The Apple Team ID is invalid");
  }
  if (role !== "application" && role !== "transparent-proxy") {
    throw new Error("The requested capability role is not admitted");
  }
  const extension = role === "transparent-proxy";
  return {
    "com.apple.application-identifier": `${teamID}.${extension ? transparentProxyBundleIdentifier : "io.vibermate.desktop"}`,
    "com.apple.developer.team-identifier": teamID,
    "com.apple.developer.networking.networkextension": [networkCapability],
    ...(extension
      ? {
        "com.apple.security.app-sandbox": true,
        // NEMachServiceName must begin with an App Group the extension holds,
        // or system extension validation fails (OSSystemExtensionErrorDomain 9).
        "com.apple.security.application-groups": [`${teamID}.${transparentProxyBundleIdentifier}`],
        "com.apple.security.network.client": true,
      }
      : { "com.apple.developer.system-extension.install": true }),
  };
}

export function capabilityEntitlementsXML(role, teamID) {
  const valueXML = (value) => value === true ? "<true/>"
    : Array.isArray(value) ? `<array>${value.map(valueXML).join("")}</array>`
      : `<string>${value}</string>`;
  return `<?xml version="1.0" encoding="UTF-8"?>\n<plist version="1.0"><dict>${Object.entries(capabilityEntitlements(role, teamID))
    .map(([key, value]) => `<key>${key}</key>${valueXML(value)}`).join("")}</dict></plist>\n`;
}

export function provisioningInputsFromEnvironment(environment) {
  const identity = environment.APPLE_SIGNING_IDENTITY?.trim().toLowerCase();
  const teamID = environment.VIBERMATE_APPLE_TEAM_ID?.trim();
  if (!/^[0-9a-f]{40}$/u.test(identity ?? "")) {
    throw new Error("APPLE_SIGNING_IDENTITY must be one Developer ID certificate SHA-1 selector");
  }
  capabilityEntitlements("application", teamID);
  const explicitPath = (name, optional = false) => {
    const path = environment[name]?.trim();
    if (optional && !path) return undefined;
    if (!path || !isAbsolute(path)) throw new Error(`${name} must be an explicit absolute path`);
    return resolve(path);
  };
  return Object.freeze({
    identity,
    teamID,
    appProfilePath: explicitPath("VIBERMATE_MACOS_APP_PROVISION_PROFILE"),
    extensionProfilePath: explicitPath("VIBERMATE_MACOS_EXTENSION_PROVISION_PROFILE"),
    keychainPath: explicitPath("SIGNING_KEYCHAIN_PATH", true),
  });
}

export function validateProvisioningProfile(profile, { role, teamID, identity, now = new Date() }) {
  const required = capabilityEntitlements(role, teamID);
  if (!profile || typeof profile !== "object" ||
      !/^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/iu.test(profile.UUID ?? "") ||
      !Array.isArray(profile.Platform) || !profile.Platform.includes("OSX") ||
      JSON.stringify(profile.TeamIdentifier) !== JSON.stringify([teamID]) ||
      JSON.stringify(profile.ApplicationIdentifierPrefix) !== JSON.stringify([teamID]) ||
      profile.ProvisionsAllDevices !== true || profile.ProvisionedDevices !== undefined) {
    throw new Error(`${role} provisioning profile is not a Developer ID profile for this team`);
  }
  const created = Date.parse(profile.CreationDate);
  const expires = Date.parse(profile.ExpirationDate);
  if (!Number.isFinite(created) || !Number.isFinite(expires) || created > now.valueOf() || expires <= now.valueOf()) {
    throw new Error(`${role} provisioning profile is outside its validity period`);
  }
  const entitlements = profile.Entitlements;
  if (!entitlements || entitlements["com.apple.application-identifier"] !== required["com.apple.application-identifier"] ||
      entitlements["com.apple.developer.team-identifier"] !== teamID) {
    throw new Error(`${role} provisioning profile has the wrong application identifier`);
  }
  if (!Array.isArray(entitlements["com.apple.developer.networking.networkextension"]) ||
      !entitlements["com.apple.developer.networking.networkextension"].includes(networkCapability) ||
      (role === "application" && entitlements["com.apple.developer.system-extension.install"] !== true) ||
      entitlements["get-task-allow"] === true || entitlements["com.apple.security.get-task-allow"] === true) {
    throw new Error(`${role} provisioning profile does not authorize the release capabilities`);
  }
  if (!/^[0-9a-f]{40}$/iu.test(identity ?? "") || !Array.isArray(profile.DeveloperCertificates) ||
      !profile.DeveloperCertificates.some((certificate) =>
        typeof certificate === "string" && createHash("sha1").update(Buffer.from(certificate, "base64")).digest("hex") === identity.toLowerCase())) {
    throw new Error(`${role} provisioning profile does not contain the selected signing certificate`);
  }
  return Object.freeze({ uuid: profile.UUID, entitlements: required });
}

export function runSigningTool(command, arguments_, { input, encoding = "utf8", environment = process.env } = {}) {
  const result = spawnSync(command, arguments_, {
    encoding, input,
    env: {
      DEVELOPER_DIR: environment.DEVELOPER_DIR,
      HOME: environment.HOME,
      LANG: "C",
      LC_ALL: "C",
      PATH: "/usr/bin:/bin:/usr/sbin:/sbin",
      TMPDIR: environment.TMPDIR ?? tmpdir(),
    },
    maxBuffer: 8 << 20, timeout: 120_000,
  });
  if (result.error || result.signal !== null || result.status !== 0) {
    throw new Error(`${command} failed: ${result.error?.message ?? result.stderr?.toString().trim() ?? result.status}`);
  }
  return result;
}

export async function readProvisioningProfile(path, options) {
  const metadata = await lstat(path);
  if (!metadata.isFile() || metadata.isSymbolicLink() || metadata.size <= 0 || metadata.size > (4 << 20)) {
    throw new Error(`${options.role} provisioning profile must be a direct nonempty file`);
  }
  const payload = await readFile(path);
  const decoded = runSigningTool("/usr/bin/security", ["cms", "-D"], { input: payload, encoding: "buffer" }).stdout;
  const normalized = runSigningTool("/usr/bin/python3", ["-c", [
    "import base64, datetime, json, plistlib, sys",
    "def encode(value):",
    "    if isinstance(value, bytes): return base64.b64encode(value).decode('ascii')",
    "    if isinstance(value, datetime.datetime): return value.isoformat() + 'Z'",
    "    raise TypeError(type(value).__name__)",
    "print(json.dumps(plistlib.loads(sys.stdin.buffer.read()), default=encode))",
  ].join("\n")], { input: decoded }).stdout;
  const profile = JSON.parse(normalized);
  return { payload, ...validateProvisioningProfile(profile, options) };
}

export async function readSigningProfiles(inputs) {
  const options = { teamID: inputs.teamID, identity: inputs.identity };
  const application = await readProvisioningProfile(inputs.appProfilePath, { ...options, role: "application" });
  const extension = await readProvisioningProfile(inputs.extensionProfilePath, { ...options, role: "transparent-proxy" });
  return { application, extension };
}

export async function prepareSystemExtensionSigning(appPath, inputs) {
  // Validate both before changing either bundle; profile contents stay outside source control.
  const profiles = await readSigningProfiles(inputs);
  const extensionPath = resolve(appPath, transparentProxyBundlePath);
  for (const [path, identifier] of [[appPath, "io.vibermate.desktop"], [extensionPath, transparentProxyBundleIdentifier]]) {
    const metadata = await lstat(path);
    if (!metadata.isDirectory() || metadata.isSymbolicLink()) throw new Error(`Signing bundle is not a directory: ${path}`);
    const observed = runSigningTool("/usr/libexec/PlistBuddy", ["-c", "Print :CFBundleIdentifier", resolve(path, "Contents/Info.plist")]).stdout.trim();
    if (observed !== identifier) throw new Error(`Signing bundle has the wrong identifier: ${observed}`);
  }
  // Ad-hoc Xcode builds have no TeamIdentifierPrefix; bind the service before sealing.
  runSigningTool("/usr/libexec/PlistBuddy", [
    "-c", `Set :NetworkExtension:NEMachServiceName ${inputs.teamID}.${transparentProxyBundleIdentifier}`,
    resolve(extensionPath, "Contents/Info.plist"),
  ]);
  const directory = await mkdtemp(resolve(tmpdir(), "vibermate-capability-signing-"));
  try {
    const entitlements = {};
    for (const [role, bundle, profile] of [
      ["application", appPath, profiles.application],
      ["transparent-proxy", extensionPath, profiles.extension],
    ]) {
      await writeFile(resolve(bundle, "Contents/embedded.provisionprofile"), profile.payload, { mode: 0o644 });
      entitlements[role] = resolve(directory, `${role}.entitlements`);
      await writeFile(entitlements[role], capabilityEntitlementsXML(role, inputs.teamID), { mode: 0o600 });
    }
    return { entitlements, cleanup: () => rm(directory, { recursive: true, force: true }) };
  } catch (error) {
    await rm(directory, { recursive: true, force: true });
    throw error;
  }
}
