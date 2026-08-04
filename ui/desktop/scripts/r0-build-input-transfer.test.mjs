import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import {
  chmod,
  copyFile,
  lstat,
  mkdir,
  mkdtemp,
  open,
  readFile,
  readdir,
  realpath,
  rename,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { createMacOSApplicationArchive } from "./macos-application-archive.mjs";
import {
  createR0BuildInputTransfer,
  restoreR0BuildInputTransfer,
  r0BuildInputTransferPolicy,
} from "./r0-build-input-transfer.mjs";

async function syntheticInput(context, label) {
  const root = await realpath(
    await mkdtemp(resolve(tmpdir(), `vibermate-r0-transfer-${label}-`)),
  );
  context.after(() => rm(root, { recursive: true, force: true }));
  const runnerTemp = resolve(root, "runner-temp");
  const inputRoot = resolve(
    runnerTemp,
    `vibermate-r0-input-source-${label}`,
  );
  const candidateExecutionMarker = resolve(root, "candidate-executed");
  await mkdir(runnerTemp, { mode: 0o700 });
  await chmod(runnerTemp, 0o700);
  await mkdir(inputRoot, { mode: 0o755 });
  await chmod(inputRoot, 0o755);

  for (const name of r0BuildInputTransferPolicy.executableNames) {
    const path = resolve(inputRoot, name);
    await writeFile(
      path,
      `#!/bin/sh\n: > "${candidateExecutionMarker}"\n# ${name}\n`,
    );
    await chmod(path, 0o755);
  }
  await writeFile(
    resolve(inputRoot, "vibermate-build-manifest.json"),
    '{"schema":"vibermate.desktop-build/v2"}\n',
  );
  await chmod(resolve(inputRoot, "vibermate-build-manifest.json"), 0o600);
  await writeFile(resolve(inputRoot, "LICENSE"), "synthetic license\n");
  await chmod(resolve(inputRoot, "LICENSE"), 0o644);
  await mkdir(resolve(inputRoot, "dist", "assets"), {
    recursive: true,
    mode: 0o755,
  });
  await chmod(resolve(inputRoot, "dist"), 0o755);
  await chmod(resolve(inputRoot, "dist", "assets"), 0o755);
  await writeFile(resolve(inputRoot, "dist", "index.html"), "<main>R0</main>\n");
  await chmod(resolve(inputRoot, "dist", "index.html"), 0o644);
  await writeFile(resolve(inputRoot, "dist", "assets", "main.js"), "void 0;\n");
  await chmod(resolve(inputRoot, "dist", "assets", "main.js"), 0o644);
  return { candidateExecutionMarker, inputRoot, root, runnerTemp };
}

function creatorEnvironment(fixture, suffix) {
  return {
    RUNNER_TEMP: fixture.runnerTemp,
    VIBERMATE_R0_INPUT_ROOT: fixture.inputRoot,
    VIBERMATE_R0_INPUT_TRANSFER_DIRECTORY: resolve(
      fixture.runnerTemp,
      `vibermate-r0-input-${suffix}`,
    ),
  };
}

async function downloadTransfer(fixture, transferDirectory, suffix) {
  const downloadDirectory = resolve(
    fixture.runnerTemp,
    `vibermate-r0-input-download-${suffix}`,
  );
  await mkdir(downloadDirectory, { mode: 0o700 });
  await chmod(downloadDirectory, 0o700);
  for (const name of [
    r0BuildInputTransferPolicy.archiveFilename,
    r0BuildInputTransferPolicy.checksumFilename,
  ]) {
    const destination = resolve(downloadDirectory, name);
    await copyFile(resolve(transferDirectory, name), destination);
    await chmod(destination, 0o644);
  }
  return downloadDirectory;
}

function restorerEnvironment(fixture, downloadDirectory, suffix) {
  return {
    RUNNER_TEMP: fixture.runnerTemp,
    VIBERMATE_R0_INPUT_DOWNLOAD_DIRECTORY: downloadDirectory,
    VIBERMATE_R0_RESTORED_INPUT_ROOT: resolve(
      fixture.runnerTemp,
      `vibermate-r0-input-restored-${suffix}`,
    ),
  };
}

test("closed source-traceability build-input transfer (R0) round-trips without executing candidate data", async (context) => {
  const fixture = await syntheticInput(context, "round-trip");
  const created = await createR0BuildInputTransfer(
    creatorEnvironment(fixture, "123-1"),
  );
  assert.deepEqual((await readdir(created.transferDirectory)).sort(), [
    r0BuildInputTransferPolicy.archiveFilename,
    r0BuildInputTransferPolicy.checksumFilename,
  ].sort());
  const downloadDirectory = await downloadTransfer(
    fixture,
    created.transferDirectory,
    "123-1",
  );
  const restored = await restoreR0BuildInputTransfer(
    restorerEnvironment(fixture, downloadDirectory, "123-1"),
  );
  assert.equal(restored.archiveSHA256, created.archiveSHA256);
  assert.equal(restored.inputTreeSHA256, created.inputTreeSHA256);
  assert.deepEqual((await readdir(restored.restoredInputRoot)).sort(), [
    ...r0BuildInputTransferPolicy.topLevelNames,
  ].sort());
  assert.equal(
    await readFile(resolve(restored.restoredInputRoot, "dist", "index.html"), "utf8"),
    "<main>R0</main>\n",
  );
  for (const [name, mode] of [
    ["vibermate-desktop", 0o755],
    ["vibermate", 0o755],
    ["vibermated", 0o755],
    ["vibermate-build-manifest.json", 0o600],
    ["LICENSE", 0o644],
    ["dist", 0o755],
  ]) {
    assert.equal((await lstat(resolve(restored.restoredInputRoot, name))).mode & 0o7777, mode);
  }
  await assert.rejects(() => lstat(fixture.candidateExecutionMarker), {
    code: "ENOENT",
  });
});

test("source-traceability build-input restore CLI (R0) emits only the two closed digests", async (context) => {
  const fixture = await syntheticInput(context, "cli-output");
  const created = await createR0BuildInputTransfer(
    creatorEnvironment(fixture, "456-2"),
  );
  const downloadDirectory = await downloadTransfer(
    fixture,
    created.transferDirectory,
    "456-2",
  );
  const environment = restorerEnvironment(
    fixture,
    downloadDirectory,
    "456-2",
  );
  const result = spawnSync(
    process.execPath,
    [fileURLToPath(new URL("./restore-r0-build-input-transfer.mjs", import.meta.url))],
    {
      encoding: "utf8",
      env: { ...process.env, ...environment },
    },
  );
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stderr, "");
  assert.equal(
    result.stdout,
    `archiveSHA256=${created.archiveSHA256}\n` +
      `inputTreeSHA256=${created.inputTreeSHA256}\n`,
  );
  await assert.rejects(() => lstat(fixture.candidateExecutionMarker), {
    code: "ENOENT",
  });
});

test("source-traceability build-input creator (R0) rejects inventory, mode, ASCII, and size deviations", async (context) => {
  await context.test("unexpected top-level entry", async (subtest) => {
    const fixture = await syntheticInput(subtest, "extra");
    await writeFile(resolve(fixture.inputRoot, "extra"), "unexpected\n");
    await chmod(resolve(fixture.inputRoot, "extra"), 0o644);
    await assert.rejects(() =>
      createR0BuildInputTransfer(creatorEnvironment(fixture, "extra-1")),
    );
  });

  await context.test("group-writable fixed file", async (subtest) => {
    const fixture = await syntheticInput(subtest, "mode");
    await chmod(resolve(fixture.inputRoot, "LICENSE"), 0o666);
    await assert.rejects(() =>
      createR0BuildInputTransfer(creatorEnvironment(fixture, "mode-1")),
    );
  });

  await context.test("non-ASCII dist path", async (subtest) => {
    const fixture = await syntheticInput(subtest, "unicode");
    await writeFile(resolve(fixture.inputRoot, "dist", "caf\u00e9.js"), "void 0;\n");
    await chmod(resolve(fixture.inputRoot, "dist", "caf\u00e9.js"), 0o644);
    await assert.rejects(() =>
      createR0BuildInputTransfer(creatorEnvironment(fixture, "unicode-1")),
    );
  });

  await context.test("oversized manifest", async (subtest) => {
    const fixture = await syntheticInput(subtest, "size");
    await writeFile(
      resolve(fixture.inputRoot, "vibermate-build-manifest.json"),
      Buffer.alloc((128 << 10) + 1, 0x61),
    );
    await chmod(resolve(fixture.inputRoot, "vibermate-build-manifest.json"), 0o600);
    await assert.rejects(() =>
      createR0BuildInputTransfer(creatorEnvironment(fixture, "size-1")),
    );
  });

  await context.test("transfer path outside RUNNER_TEMP", async (subtest) => {
    const fixture = await syntheticInput(subtest, "path");
    const environment = creatorEnvironment(fixture, "path-1");
    environment.VIBERMATE_R0_INPUT_TRANSFER_DIRECTORY = resolve(
      fixture.root,
      "vibermate-r0-input-path-1",
    );
    await assert.rejects(() => createR0BuildInputTransfer(environment));
  });

  await context.test("input root outside RUNNER_TEMP", async (subtest) => {
    const fixture = await syntheticInput(subtest, "input-path");
    const environment = creatorEnvironment(fixture, "input-path-1");
    const outsideInput = resolve(fixture.root, "vibermate-r0-input-source-outside");
    await rename(fixture.inputRoot, outsideInput);
    environment.VIBERMATE_R0_INPUT_ROOT = outsideInput;
    await assert.rejects(() => createR0BuildInputTransfer(environment));
  });

  await context.test("input root with wrong prefix", async (subtest) => {
    const fixture = await syntheticInput(subtest, "input-prefix");
    const environment = creatorEnvironment(fixture, "input-prefix-1");
    const wrongPrefix = resolve(fixture.runnerTemp, "r0-input-source-wrong");
    await rename(fixture.inputRoot, wrongPrefix);
    environment.VIBERMATE_R0_INPUT_ROOT = wrongPrefix;
    await assert.rejects(() => createR0BuildInputTransfer(environment));
  });
});

test("source-traceability build-input restorer (R0) rejects changed transfers before trust", async (context) => {
  for (const scenario of ["archive", "checksum", "inventory", "mode"]) {
    await context.test(scenario, async (subtest) => {
      const fixture = await syntheticInput(subtest, `tamper-${scenario}`);
      const created = await createR0BuildInputTransfer(
        creatorEnvironment(fixture, `${scenario}-1`),
      );
      const downloadDirectory = await downloadTransfer(
        fixture,
        created.transferDirectory,
        `${scenario}-1`,
      );
      const archivePath = resolve(
        downloadDirectory,
        r0BuildInputTransferPolicy.archiveFilename,
      );
      const checksumPath = resolve(
        downloadDirectory,
        r0BuildInputTransferPolicy.checksumFilename,
      );
      if (scenario === "archive") {
        const metadata = await lstat(archivePath);
        const handle = await open(archivePath, "r+");
        try {
          const byte = Buffer.alloc(1);
          await handle.read(byte, 0, 1, metadata.size - 1);
          byte[0] ^= 0xff;
          await handle.write(byte, 0, 1, metadata.size - 1);
          await handle.sync();
        } finally {
          await handle.close();
        }
      } else if (scenario === "checksum") {
        const checksum = await readFile(checksumPath);
        checksum[0] = checksum[0] === 0x30 ? 0x31 : 0x30;
        await writeFile(checksumPath, checksum);
      } else if (scenario === "inventory") {
        await writeFile(resolve(downloadDirectory, "unexpected"), "candidate bytes\n");
        await chmod(resolve(downloadDirectory, "unexpected"), 0o644);
      } else {
        await chmod(archivePath, 0o666);
      }
      const environment = restorerEnvironment(
        fixture,
        downloadDirectory,
        `${scenario}-1`,
      );
      await assert.rejects(() => restoreR0BuildInputTransfer(environment));
      await assert.rejects(() => lstat(environment.VIBERMATE_R0_RESTORED_INPUT_ROOT), {
        code: "ENOENT",
      });
    });
  }
});

test("an archive outside the source-traceability input contract (R0) is rejected without publishing a final root", async (context) => {
  const fixture = await syntheticInput(context, "wrong-shape");
  const downloadDirectory = resolve(
    fixture.runnerTemp,
    "vibermate-r0-input-download-wrong-shape-1",
  );
  await mkdir(downloadDirectory, { mode: 0o700 });
  await chmod(downloadDirectory, 0o700);
  const wrongRoot = resolve(fixture.root, "wrong-shape-input");
  await mkdir(wrongRoot, { mode: 0o755 });
  await chmod(wrongRoot, 0o755);
  await writeFile(resolve(wrongRoot, "unexpected"), "authenticated candidate bytes\n");
  await chmod(resolve(wrongRoot, "unexpected"), 0o644);
  const archivePath = resolve(
    downloadDirectory,
    r0BuildInputTransferPolicy.archiveFilename,
  );
  const archived = await createMacOSApplicationArchive(wrongRoot, archivePath);
  await chmod(archivePath, 0o644);
  const checksumPath = resolve(
    downloadDirectory,
    r0BuildInputTransferPolicy.checksumFilename,
  );
  await writeFile(
    checksumPath,
    `${archived.archiveSHA256}  ${r0BuildInputTransferPolicy.archiveFilename}\n`,
  );
  await chmod(checksumPath, 0o644);
  const environment = restorerEnvironment(
    fixture,
    downloadDirectory,
    "wrong-shape-1",
  );
  await assert.rejects(() => restoreR0BuildInputTransfer(environment));
  await assert.rejects(() => lstat(environment.VIBERMATE_R0_RESTORED_INPUT_ROOT), {
    code: "ENOENT",
  });
  assert.equal(
    (await readdir(fixture.runnerTemp)).some((name) =>
      name.startsWith(".vibermate-r0-input-stage-"),
    ),
    false,
  );
});
