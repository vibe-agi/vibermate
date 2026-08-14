import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { createMacOSInstalledCandidateEvidence } from "./macos-installed-candidate-evidence.mjs";

function isMainModule() {
  return (
    typeof process.argv[1] === "string" &&
    import.meta.url === pathToFileURL(resolve(process.argv[1])).href
  );
}

if (isMainModule()) {
  if (process.argv.length !== 2) {
    throw new Error("The installed macOS candidate evidence creator accepts no arguments");
  }
  await createMacOSInstalledCandidateEvidence();
  process.stdout.write("Installed macOS candidate evidence created.\n");
}
