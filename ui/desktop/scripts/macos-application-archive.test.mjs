import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import {
  chmod,
  mkdir,
  mkdtemp,
  readFile,
  realpath,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import test from "node:test";
import {
  createMacOSApplicationArchive,
  extractMacOSApplicationArchive,
} from "./macos-application-archive.mjs";
import {
  applicationTreeLedger,
} from "./verify-macos-signed-candidate.mjs";
import { validateTreeLedgerEquality } from "./macos-distribution-policy.mjs";

function record(value) {
  const source = Buffer.from(JSON.stringify(value), "utf8");
  const length = Buffer.alloc(4);
  length.writeUInt32BE(source.length);
  return Buffer.concat([length, source]);
}

test("closed App archive round-trips bytes and POSIX modes", async (context) => {
  const directory = await realpath(
    await mkdtemp(resolve(tmpdir(), "vibermate-archive-test-")),
  );
  context.after(() => rm(directory, { recursive: true, force: true }));
  const source = resolve(directory, "source", "VibeMate.app");
  await mkdir(resolve(source, "Contents", "MacOS"), { recursive: true, mode: 0o755 });
  const executable = resolve(source, "Contents", "MacOS", "vibermate-desktop");
  await writeFile(executable, "candidate bytes\n");
  await chmod(executable, 0o755);
  const archive = resolve(directory, "candidate.vma");
  await createMacOSApplicationArchive(source, archive);
  const restored = resolve(directory, "restored", "VibeMate.app");
  await extractMacOSApplicationArchive(archive, restored);
  validateTreeLedgerEquality(
    await applicationTreeLedger(source),
    await applicationTreeLedger(restored),
  );
  assert.equal(
    await readFile(resolve(restored, "Contents", "MacOS", "vibermate-desktop"), "utf8"),
    "candidate bytes\n",
  );
});

test("closed App archive rejects replacement of its opened output inode", async (context) => {
  const directory = await realpath(
    await mkdtemp(resolve(tmpdir(), "vibermate-archive-output-race-")),
  );
  context.after(() => rm(directory, { recursive: true, force: true }));
  const source = resolve(directory, "source", "VibeMate.app");
  await mkdir(source, { recursive: true, mode: 0o755 });
  await writeFile(resolve(source, "payload"), Buffer.alloc(16 << 20, 0x61));
  const archive = resolve(directory, "candidate.vma");
  const displaced = resolve(directory, "candidate.displaced.vma");
  const attacker = spawn(
    process.execPath,
    [
      "-e",
      `
        const { renameSync, writeFileSync } = require("node:fs");
        process.stdout.write("ready\\n");
        for (;;) {
          try {
            renameSync(process.env.ARCHIVE_PATH, process.env.DISPLACED_PATH);
            writeFileSync(process.env.ARCHIVE_PATH, "replacement\\n", {
              flag: "wx",
              mode: 0o600,
            });
            break;
          } catch (error) {
            if (error?.code !== "ENOENT") {
              throw error;
            }
            Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 1);
          }
        }
      `,
    ],
    {
      env: {
        ARCHIVE_PATH: archive,
        DISPLACED_PATH: displaced,
      },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  context.after(() => attacker.kill());
  const exited = once(attacker, "exit");
  const [ready] = await once(attacker.stdout, "data");
  assert.equal(ready.toString("utf8"), "ready\n");
  await assert.rejects(
    () => createMacOSApplicationArchive(source, archive),
    /created application archive (?:is not stable|path changed)/u,
  );
  const [code, signal] = await exited;
  assert.equal(signal, null);
  assert.equal(code, 0);
  assert.equal(await readFile(archive, "utf8"), "replacement\n");
});

test("closed App archive rejects links, extensions, and traversal records", async (context) => {
  const directory = await mkdtemp(resolve(tmpdir(), "vibermate-archive-attack-"));
  context.after(() => rm(directory, { recursive: true, force: true }));
  const magic = Buffer.from("VIBERMATE-APP-ARCHIVE-V1\n", "ascii");
  for (const [name, header] of [
    [
      "symlink",
      { mode: 0o777, path: "VibeMate.app/link", size: 0, type: "symlink" },
    ],
    [
      "pax",
      { mode: 0o644, path: "VibeMate.app/pax", size: 0, type: "pax" },
    ],
    [
      "traversal",
      { mode: 0o755, path: "VibeMate.app/../escape", size: 0, type: "directory" },
    ],
  ]) {
    const archive = resolve(directory, `${name}.vma`);
    await writeFile(
      archive,
      Buffer.concat([
        magic,
        record({ mode: 0o755, path: "VibeMate.app", size: 0, type: "directory" }),
        record(header),
      ]),
    );
    await assert.rejects(() =>
      extractMacOSApplicationArchive(
        archive,
        resolve(directory, `${name}-output`, "VibeMate.app"),
      ),
    );
  }
});

test("closed App archive rejects owner-unreadable file modes", async (context) => {
  const directory = await mkdtemp(resolve(tmpdir(), "vibermate-archive-mode-"));
  context.after(() => rm(directory, { recursive: true, force: true }));
  const magic = Buffer.from("VIBERMATE-APP-ARCHIVE-V1\n", "ascii");
  for (const mode of [0o000, 0o100]) {
    const archive = resolve(directory, `mode-${mode.toString(8)}.vma`);
    await writeFile(
      archive,
      Buffer.concat([
        magic,
        record({ mode: 0o755, path: "VibeMate.app", size: 0, type: "directory" }),
        record({
          mode,
          path: "VibeMate.app/unreadable",
          sha256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
          size: 0,
          type: "file",
        }),
      ]),
    );
    await assert.rejects(() =>
      extractMacOSApplicationArchive(
        archive,
        resolve(directory, `mode-${mode.toString(8)}-output`, "VibeMate.app"),
      ),
    );
  }
});

test("closed App archive rejects group/world-writable modes on both boundaries", async (context) => {
  const directory = await mkdtemp(resolve(tmpdir(), "vibermate-archive-writable-mode-"));
  context.after(() => rm(directory, { recursive: true, force: true }));
  const magic = Buffer.from("VIBERMATE-APP-ARCHIVE-V1\n", "ascii");
  for (const [name, headers] of [
    [
      "directory-777",
      [record({ mode: 0o777, path: "VibeMate.app", size: 0, type: "directory" })],
    ],
    [
      "file-666",
      [
        record({ mode: 0o755, path: "VibeMate.app", size: 0, type: "directory" }),
        record({
          mode: 0o666,
          path: "VibeMate.app/writable",
          sha256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
          size: 0,
          type: "file",
        }),
      ],
    ],
  ]) {
    const archive = resolve(directory, `${name}.vma`);
    await writeFile(archive, Buffer.concat([magic, ...headers]));
    await assert.rejects(() =>
      extractMacOSApplicationArchive(
        archive,
        resolve(directory, `${name}-output`, "VibeMate.app"),
      ),
    );
  }

  const source = resolve(directory, "source", "VibeMate.app");
  await mkdir(source, { recursive: true, mode: 0o755 });
  await chmod(source, 0o777);
  await assert.rejects(() => applicationTreeLedger(source));
  await assert.rejects(() =>
    createMacOSApplicationArchive(source, resolve(directory, "directory-777-source.vma")),
  );

  await chmod(source, 0o755);
  const writableFile = resolve(source, "writable");
  await writeFile(writableFile, "candidate bytes\n");
  await chmod(writableFile, 0o666);
  await assert.rejects(() => applicationTreeLedger(source));
  await assert.rejects(() =>
    createMacOSApplicationArchive(source, resolve(directory, "file-666-source.vma")),
  );
});

test("closed App archive rejects case aliases and duplicate paths", async (context) => {
  const directory = await mkdtemp(resolve(tmpdir(), "vibermate-archive-alias-"));
  context.after(() => rm(directory, { recursive: true, force: true }));
  const magic = Buffer.from("VIBERMATE-APP-ARCHIVE-V1\n", "ascii");
  const archive = resolve(directory, "alias.vma");
  await writeFile(
    archive,
    Buffer.concat([
      magic,
      record({ mode: 0o755, path: "VibeMate.app", size: 0, type: "directory" }),
      record({ mode: 0o755, path: "VibeMate.app/Data", size: 0, type: "directory" }),
      record({ mode: 0o755, path: "VibeMate.app/data", size: 0, type: "directory" }),
    ]),
  );
  await assert.rejects(() =>
    extractMacOSApplicationArchive(
      archive,
      resolve(directory, "output", "VibeMate.app"),
    ),
  );
});

test("closed App archive rejects non-ASCII and ill-formed path aliases", async (context) => {
  const directory = await mkdtemp(resolve(tmpdir(), "vibermate-archive-unicode-"));
  context.after(() => rm(directory, { recursive: true, force: true }));
  const magic = Buffer.from("VIBERMATE-APP-ARCHIVE-V1\n", "ascii");
  for (const [name, path] of [
    ["unpaired-surrogate", "VibeMate.app/\ud800"],
    ["long-s-alias", "VibeMate.app/ſ"],
    ["sharp-s-alias", "VibeMate.app/ß"],
  ]) {
    const archive = resolve(directory, `${name}.vma`);
    await writeFile(
      archive,
      Buffer.concat([
        magic,
        record({ mode: 0o755, path: "VibeMate.app", size: 0, type: "directory" }),
        record({ mode: 0o644, path, sha256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", size: 0, type: "file" }),
      ]),
    );
    await assert.rejects(() =>
      extractMacOSApplicationArchive(
        archive,
        resolve(directory, `${name}-output`, "VibeMate.app"),
      ),
    );
  }
});

test("closed App archive rejects semantically equivalent reordered headers", async (context) => {
  const directory = await mkdtemp(resolve(tmpdir(), "vibermate-archive-order-"));
  context.after(() => rm(directory, { recursive: true, force: true }));
  const archive = resolve(directory, "reordered.vma");
  await writeFile(
    archive,
    Buffer.concat([
      Buffer.from("VIBERMATE-APP-ARCHIVE-V1\n", "ascii"),
      record({ type: "directory", size: 0, path: "VibeMate.app", mode: 0o755 }),
      record({
        type: "end",
        treeSHA256: "0".repeat(64),
        totalBytes: 0,
        entries: 1,
      }),
    ]),
  );
  await assert.rejects(() =>
    extractMacOSApplicationArchive(
      archive,
      resolve(directory, "output", "VibeMate.app"),
    ),
  );
});
