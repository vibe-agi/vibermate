import { constants } from "node:fs";
import {
  chmod,
  copyFile,
  lstat,
  mkdir,
  open,
  realpath,
} from "node:fs/promises";
import { resolve } from "node:path";
import { macOSDistributionPolicy } from "./macos-distribution-policy.mjs";
import { createMacOSApplicationArchive } from "./macos-application-archive.mjs";
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
    !directory.startsWith(`${runnerTemp}/vibermate-signed-`)
  ) {
    throw new Error("The signed transfer directory is not an admitted runner path");
  }
  return directory;
}

async function main() {
  if (process.argv.length !== 2) {
    throw new Error("The signed macOS transfer creator accepts no arguments");
  }
  if (process.platform !== "darwin") {
    throw new Error("The signed macOS transfer creator requires macOS");
  }
  const candidate = await verifySignedMacOSDistributionCandidate();
  const directory = transferDirectory();
  try {
    await lstat(directory);
    throw new Error("The signed transfer directory already exists");
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
  await mkdir(directory, { mode: 0o700 });
  if ((await realpath(directory)) !== directory) {
    throw new Error("The signed transfer directory is not canonical");
  }
  const archivePath = resolve(directory, macOSDistributionPolicy.signedAppArchiveFilename);
  const archived = await createMacOSApplicationArchive(candidate.appPath, archivePath);
  if (archived.applicationTreeSHA256 !== candidate.applicationTreeSHA256) {
    throw new Error("The signed App changed while its transfer was created");
  }
  const dmgPath = resolve(directory, macOSDistributionPolicy.diskImageFilename);
  const evidencePath = resolve(directory, macOSDistributionPolicy.signingEvidenceFilename);
  await copyFile(candidate.dmgPath, dmgPath, constants.COPYFILE_EXCL);
  await copyFile(
    macOSDistributionDirectories.signingEvidencePath,
    evidencePath,
    constants.COPYFILE_EXCL,
  );
  await chmod(dmgPath, 0o600);
  await chmod(evidencePath, 0o600);
  const digests = {
    [macOSDistributionPolicy.signedAppArchiveFilename]: archived.archiveSHA256,
    [macOSDistributionPolicy.diskImageFilename]: await sha256File(dmgPath),
    [macOSDistributionPolicy.signingEvidenceFilename]: await sha256File(evidencePath),
  };
  const checksum = await open(
    resolve(directory, macOSDistributionPolicy.signedTransferChecksumFilename),
    "wx",
    0o600,
  );
  try {
    await checksum.writeFile(
      Object.entries(digests)
        .map(([name, digest]) => `${digest}  ${name}\n`)
        .join(""),
    );
    await checksum.sync();
  } finally {
    await checksum.close();
  }
  const finalCandidate = await verifySignedMacOSDistributionCandidate();
  if (
    finalCandidate.applicationTreeSHA256 !== candidate.applicationTreeSHA256 ||
    finalCandidate.diskImageSHA256 !== candidate.diskImageSHA256 ||
    finalCandidate.signingEvidenceSHA256 !== candidate.signingEvidenceSHA256
  ) {
    throw new Error("The signed candidate changed while its transfer was created");
  }
  process.stdout.write("Closed signed App transfer and checksums created.\n");
}

await main();
