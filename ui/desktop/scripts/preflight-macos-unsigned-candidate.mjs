import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { inspectUnsignedMacOSDistributionCandidate } from "./verify-macos-signed-candidate.mjs";

async function main() {
  if (process.argv.length !== 2) {
    throw new Error("The unsigned macOS candidate preflight accepts no arguments");
  }
  await inspectUnsignedMacOSDistributionCandidate();
  process.stdout.write("Unsigned macOS distribution candidate verified as inert data.\n");
}

if (
  typeof process.argv[1] === "string" &&
  import.meta.url === pathToFileURL(resolve(process.argv[1])).href
) {
  await main();
}
