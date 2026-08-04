import { lstat, mkdir, open, realpath } from "node:fs/promises";
import { resolve } from "node:path";
import {
  macOSDistributionPolicy,
  releaseRevisionsFromEnvironment,
} from "./macos-distribution-policy.mjs";
import { createMacOSApplicationArchive } from "./macos-application-archive.mjs";
import {
  inspectUnsignedMacOSDistributionCandidate,
  macOSDistributionDirectories,
} from "./verify-macos-signed-candidate.mjs";

function requireTransferDirectory() {
  const directory = process.env.VIBERMATE_UNSIGNED_TRANSFER_DIRECTORY?.trim();
  const runnerTemp = process.env.RUNNER_TEMP?.trim();
  if (
    typeof directory !== "string" ||
    typeof runnerTemp !== "string" ||
    resolve(directory) !== directory ||
    resolve(runnerTemp) !== runnerTemp ||
    !directory.startsWith(`${runnerTemp}/vibermate-unsigned-`)
  ) {
    throw new Error("The unsigned transfer directory is not an admitted runner path");
  }
  return directory;
}

async function main() {
  if (process.argv.length !== 2) {
    throw new Error("The unsigned macOS archive creator accepts no arguments");
  }
  if (process.platform !== "darwin") {
    throw new Error("The unsigned macOS archive creator requires macOS");
  }
  releaseRevisionsFromEnvironment(process.env);
  const initial = await inspectUnsignedMacOSDistributionCandidate();
  const directory = requireTransferDirectory();
  try {
    await lstat(directory);
    throw new Error("The unsigned transfer directory already exists");
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
  await mkdir(directory, { mode: 0o700 });
  if ((await realpath(directory)) !== directory) {
    throw new Error("The unsigned transfer directory is not canonical");
  }
  const archivePath = resolve(directory, macOSDistributionPolicy.unsignedAppArchiveFilename);
  const archived = await createMacOSApplicationArchive(initial.appPath, archivePath);
  const checksumPath = resolve(
    directory,
    macOSDistributionPolicy.unsignedAppArchiveChecksumFilename,
  );
  const checksum = await open(checksumPath, "wx", 0o600);
  try {
    await checksum.writeFile(
      `${archived.archiveSHA256}  ${macOSDistributionPolicy.unsignedAppArchiveFilename}\n`,
    );
    await checksum.sync();
  } finally {
    await checksum.close();
  }
  const final = await inspectUnsignedMacOSDistributionCandidate();
  if (final.applicationTreeSHA256 !== archived.applicationTreeSHA256) {
    throw new Error("The unsigned application changed during archival");
  }
  process.stdout.write("Closed unsigned App archive and checksum created.\n");
}

await main();
