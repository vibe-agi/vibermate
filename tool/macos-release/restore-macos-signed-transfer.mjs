import { constants } from "node:fs";
import {
  chmod,
  copyFile,
  lstat,
  mkdir,
  readFile,
  readdir,
  realpath,
} from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { macOSDistributionPolicy } from "./macos-distribution-policy.mjs";
import { extractMacOSApplicationArchive } from "./macos-application-archive.mjs";
import {
  macOSDistributionDirectories,
  sha256File,
  verifySignedMacOSDistributionCandidate,
} from "./verify-macos-signed-candidate.mjs";

function transferDirectory() {
  const directory = process.env.VIBERMATE_SIGNED_TRANSFER_DIRECTORY?.trim();
  const runnerTemp = process.env.RUNNER_TEMP?.trim();
  if (
    typeof directory !== "string" ||
    typeof runnerTemp !== "string" ||
    resolve(directory) !== directory ||
    resolve(runnerTemp) !== runnerTemp ||
    !directory.startsWith(`${runnerTemp}/vibermate-signed-download-`)
  ) {
    throw new Error("The signed download directory is not an admitted runner path");
  }
  return directory;
}

async function main() {
  if (process.argv.length !== 2) {
    throw new Error("The signed macOS transfer restorer accepts no arguments");
  }
  if (process.platform !== "darwin") {
    throw new Error("The signed macOS transfer restorer requires macOS");
  }
  const directory = transferDirectory();
  if ((await realpath(directory)) !== directory) {
    throw new Error("The signed download directory is not canonical");
  }
  const payloadNames = [
    macOSDistributionPolicy.signedAppArchiveFilename,
    macOSDistributionPolicy.diskImageFilename,
    macOSDistributionPolicy.signingEvidenceFilename,
  ];
  const expectedNames = [
    ...payloadNames,
    macOSDistributionPolicy.signedTransferChecksumFilename,
  ].sort();
  if (JSON.stringify((await readdir(directory)).sort()) !== JSON.stringify(expectedNames)) {
    throw new Error("The signed transfer has an unexpected inventory");
  }
  for (const name of expectedNames) {
    const metadata = await lstat(resolve(directory, name));
    if (metadata.isSymbolicLink() || !metadata.isFile()) {
      throw new Error("The signed transfer contains a non-regular entry");
    }
    const maximumBytes =
      name === macOSDistributionPolicy.signedTransferChecksumFilename
        ? 1024
        : name === macOSDistributionPolicy.signingEvidenceFilename
          ? 8 << 20
          : 3 * (1 << 30);
    if (metadata.size === 0 || metadata.size > maximumBytes) {
      throw new Error("The signed transfer contains an out-of-bounds file");
    }
  }
  const checksumSource = await readFile(
    resolve(directory, macOSDistributionPolicy.signedTransferChecksumFilename),
    "utf8",
  );
  const payloadDigests = Object.fromEntries(
    await Promise.all(
      payloadNames.map(async (name) => [name, await sha256File(resolve(directory, name))]),
    ),
  );
  const expectedChecksum = payloadNames
    .map((name) => `${payloadDigests[name]}  ${name}\n`)
    .join("");
  if (checksumSource !== expectedChecksum) {
    throw new Error("The signed transfer checksum inventory is invalid");
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
    resolve(directory, macOSDistributionPolicy.signedAppArchiveFilename),
    resolve(macOSDistributionDirectories.appDirectory, macOSDistributionPolicy.appBundleName),
  );
  if (
    restored.archiveSHA256 !==
    payloadDigests[macOSDistributionPolicy.signedAppArchiveFilename]
  ) {
    throw new Error("The signed App archive changed while it was extracted");
  }
  await mkdir(macOSDistributionDirectories.dmgDirectory, {
    recursive: true,
    mode: 0o700,
  });
  await mkdir(dirname(macOSDistributionDirectories.signingEvidencePath), {
    recursive: true,
    mode: 0o700,
  });
  await copyFile(
    resolve(directory, macOSDistributionPolicy.diskImageFilename),
    resolve(macOSDistributionDirectories.dmgDirectory, macOSDistributionPolicy.diskImageFilename),
    constants.COPYFILE_EXCL,
  );
  await copyFile(
    resolve(directory, macOSDistributionPolicy.signingEvidenceFilename),
    macOSDistributionDirectories.signingEvidencePath,
    constants.COPYFILE_EXCL,
  );
  await chmod(macOSDistributionDirectories.signingEvidencePath, 0o600);
  if (
    (await sha256File(
      resolve(
        macOSDistributionDirectories.dmgDirectory,
        macOSDistributionPolicy.diskImageFilename,
      ),
    )) !== payloadDigests[macOSDistributionPolicy.diskImageFilename] ||
    (await sha256File(macOSDistributionDirectories.signingEvidencePath)) !==
      payloadDigests[macOSDistributionPolicy.signingEvidenceFilename]
  ) {
    throw new Error("The signed transfer changed while it was restored");
  }
  const candidate = await verifySignedMacOSDistributionCandidate();
  if (candidate.applicationTreeSHA256 !== restored.applicationTreeSHA256) {
    throw new Error("The restored signed App does not match its closed archive");
  }
  process.stdout.write("Closed signed transfer restored and verified.\n");
}

await main();
