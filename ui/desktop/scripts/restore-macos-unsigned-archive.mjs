import { lstat, readFile, readdir, realpath } from "node:fs/promises";
import { resolve } from "node:path";
import {
  macOSDistributionPolicy,
  releaseRevisionsFromEnvironment,
} from "./macos-distribution-policy.mjs";
import { extractMacOSApplicationArchive } from "./macos-application-archive.mjs";
import {
  inspectUnsignedMacOSDistributionCandidate,
  macOSDistributionDirectories,
  sha256File,
} from "./verify-macos-signed-candidate.mjs";

function transferDirectory() {
  const directory = process.env.VIBERMATE_UNSIGNED_TRANSFER_DIRECTORY?.trim();
  const runnerTemp = process.env.RUNNER_TEMP?.trim();
  if (
    typeof directory !== "string" ||
    typeof runnerTemp !== "string" ||
    resolve(directory) !== directory ||
    resolve(runnerTemp) !== runnerTemp ||
    !directory.startsWith(`${runnerTemp}/vibermate-unsigned-download-`)
  ) {
    throw new Error("The unsigned download directory is not an admitted runner path");
  }
  return directory;
}

async function main() {
  if (process.argv.length !== 2) {
    throw new Error("The unsigned macOS archive restorer accepts no arguments");
  }
  if (process.platform !== "darwin") {
    throw new Error("The unsigned macOS archive restorer requires macOS");
  }
  releaseRevisionsFromEnvironment(process.env);
  const directory = transferDirectory();
  if ((await realpath(directory)) !== directory) {
    throw new Error("The unsigned download directory is not canonical");
  }
  const names = (await readdir(directory)).sort();
  const expected = [
    macOSDistributionPolicy.unsignedAppArchiveChecksumFilename,
    macOSDistributionPolicy.unsignedAppArchiveFilename,
  ].sort();
  if (JSON.stringify(names) !== JSON.stringify(expected)) {
    throw new Error("The unsigned artifact has an unexpected inventory");
  }
  const archivePath = resolve(directory, macOSDistributionPolicy.unsignedAppArchiveFilename);
  const checksumPath = resolve(
    directory,
    macOSDistributionPolicy.unsignedAppArchiveChecksumFilename,
  );
  for (const [path, label] of [
    [archivePath, "unsigned App archive"],
    [checksumPath, "unsigned App checksum"],
  ]) {
    const metadata = await lstat(path);
    if (metadata.isSymbolicLink() || !metadata.isFile()) {
      throw new Error(`The ${label} is not a regular file`);
    }
    if (path === checksumPath && (metadata.size === 0 || metadata.size > 256)) {
      throw new Error("The unsigned App checksum exceeds its bound");
    }
  }
  const checksumSource = await readFile(checksumPath, "utf8");
  const match = checksumSource.match(/^([0-9a-f]{64})  VibeMate\.unsigned\.app\.vma\n$/u);
  if (match === null || match[1] !== (await sha256File(archivePath))) {
    throw new Error("The unsigned App archive checksum is invalid");
  }
  try {
    await lstat(macOSDistributionDirectories.releaseDirectory);
    throw new Error("The trusted release output already exists");
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
  const restored = await extractMacOSApplicationArchive(
    archivePath,
    resolve(macOSDistributionDirectories.appDirectory, macOSDistributionPolicy.appBundleName),
  );
  if (restored.archiveSHA256 !== match[1]) {
    throw new Error("The unsigned App archive changed while it was extracted");
  }
  const candidate = await inspectUnsignedMacOSDistributionCandidate();
  if (candidate.applicationTreeSHA256 !== restored.applicationTreeSHA256) {
    throw new Error("The restored candidate does not match the closed archive ledger");
  }
  process.stdout.write(
    `archiveSHA256=${restored.archiveSHA256}\napplicationTreeSHA256=${candidate.applicationTreeSHA256}\n`,
  );
}

await main();
