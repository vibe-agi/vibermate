import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { applicationSigningCommands } from "./bundle-macos-distribution-candidate.mjs";
import { macOSDistributionPolicy, validateClosedEntitlements, validateCodesignMetadata } from "./macos-distribution-policy.mjs";
import {
  prepareSystemExtensionSigning,
  provisioningInputsFromEnvironment,
  readSigningProfiles,
  runSigningTool,
} from "./macos-system-extension.mjs";
import { writeFlutterDesktopBuildManifest } from "../../ui/flutter_app/tool/desktop_build_manifest.mjs";

export async function signLocalMacOSCandidate({ appPath, target, inputs }) {
  const provisioning = await prepareSystemExtensionSigning(appPath, inputs);
  try {
    const commands = applicationSigningCommands(appPath, inputs.identity, inputs.keychainPath, provisioning.entitlements);
    for (const command of commands.slice(0, -1)) {
      runSigningTool(command.command, command.arguments);
    }
    // Local provenance records the final nested bytes, before the outer seal.
    await writeFlutterDesktopBuildManifest({ appPath, target });
    const outer = commands.at(-1);
    runSigningTool(outer.command, outer.arguments);
  } finally {
    await provisioning.cleanup();
  }
  runSigningTool("/usr/bin/codesign", ["--verify", "--deep", "--strict=all", "--all-architectures", appPath]);
  for (const name of macOSDistributionPolicy.codeObjectNames) {
    const code = macOSDistributionPolicy.codeObjects[name];
    const path = name === "vibermate-desktop" ? appPath : resolve(appPath, code.signingPath);
    const display = runSigningTool("/usr/bin/codesign", ["--display", "--verbose=4", path]);
    validateCodesignMetadata(`${display.stdout}\n${display.stderr}`, inputs.teamID, { expectedIdentifier: code.identifier, label: name });
    const entitlements = runSigningTool("/usr/bin/codesign", ["--display", "--entitlements", "-", "--xml", path]);
    validateClosedEntitlements(entitlements.stdout, name, {
      role: name === "vibermate-desktop" ? "application" : name,
      teamID: inputs.teamID,
    });
  }
}

async function main() {
  const inputs = provisioningInputsFromEnvironment(process.env);
  const arguments_ = process.argv.slice(2);
  if (arguments_.length === 1 && arguments_[0] === "--validate-inputs") {
    await readSigningProfiles(inputs);
    process.stdout.write("Developer ID certificate and both capability profiles match.\n");
    return;
  }
  const values = new Map();
  for (const argument of arguments_) {
    const match = argument.match(/^--(app|target)=(.+)$/u);
    if (!match || values.has(match[1])) throw new Error("Local signing requires --app=path and --target=target");
    values.set(match[1], match[2]);
  }
  if (values.size !== 2) throw new Error("Local signing requires --app=path and --target=target");
  await signLocalMacOSCandidate({ appPath: resolve(values.get("app")), target: values.get("target"), inputs });
  process.stdout.write("Developer ID local candidate signed and verified; notarization and installation have not been performed.\n");
}

if (typeof process.argv[1] === "string" && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  await main();
}
