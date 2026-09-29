// Bundles the Flutter Web engine's fallback fonts so the workbench never
// loads from fonts.gstatic.com at run time.
//
//   node tool/web_fonts.mjs sync <web build>    copy verified fonts into <build>/fonts
//   node tool/web_fonts.mjs verify <web root>   check a built or packaged root
//   node tool/web_fonts.mjs update <web build>  regenerate the pinned manifest
//
// The manifest pins every file's SHA-256. A Flutter upgrade that changes the
// engine's font list fails `sync` until the manifest is regenerated.
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile, copyFile, stat } from "node:fs/promises";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const origin = "https://fonts.gstatic.com/s/";
const toolDirectory = dirname(fileURLToPath(import.meta.url));
const manifestPath = join(toolDirectory, "web_fallback_fonts.json");
// The SIL Open Font License requires its text to travel with the fonts.
const licensePath = join(toolDirectory, "web_fonts_LICENSE.txt");
const cacheRoot = join(process.env.XDG_CACHE_HOME ?? join(homedir(), ".cache"), "vibermate", "web-fonts");
const fontPath = /"([a-z0-9]+\/v[0-9]+\/[A-Za-z0-9_.-]+\.(?:ttf|otf|woff2))"/g;

const [mode, webRoot] = process.argv.slice(2);
if (!["sync", "verify", "update"].includes(mode) || !webRoot) {
  console.error("usage: node tool/web_fonts.mjs <sync|verify|update> <web root>");
  process.exit(64);
}

const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");

async function referenced() {
  const engine = await readFile(join(webRoot, "main.dart.js"), "utf8");
  const paths = new Set([...engine.matchAll(fontPath)].map((match) => match[1]));
  if (paths.size === 0) {
    throw new Error("main.dart.js references no fallback fonts; the engine format changed");
  }
  return [...paths].sort();
}

async function fetchFont(path) {
  const response = await fetch(origin + path);
  if (!response.ok) {
    throw new Error(`download ${path}: HTTP ${response.status}`);
  }
  return Buffer.from(await response.arrayBuffer());
}

async function cached(entry) {
  const file = join(cacheRoot, entry.path);
  try {
    const bytes = await readFile(file);
    if (sha256(bytes) === entry.sha256) return file;
  } catch {}
  const bytes = await fetchFont(entry.path);
  if (sha256(bytes) !== entry.sha256) {
    throw new Error(`${entry.path}: SHA-256 does not match the manifest`);
  }
  await mkdir(dirname(file), { recursive: true });
  await writeFile(file, bytes);
  return file;
}

async function pool(items, worker, width = 16) {
  const queue = [...items];
  await Promise.all(Array.from({ length: width }, async () => {
    while (queue.length > 0) await worker(queue.shift());
  }));
}

if (mode === "update") {
  const entries = [];
  await pool(await referenced(), async (path) => {
    const bytes = await fetchFont(path);
    entries.push({ path, sha256: sha256(bytes), bytes: bytes.length });
  });
  entries.sort((left, right) => left.path.localeCompare(right.path));
  await writeFile(manifestPath, `${JSON.stringify({ origin, fonts: entries }, null, 2)}\n`);
  const total = entries.reduce((sum, entry) => sum + entry.bytes, 0);
  console.log(`pinned ${entries.length} fonts, ${(total / 1048576).toFixed(1)} MiB`);
} else if (mode === "verify") {
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  const pinned = new Set(manifest.fonts.map((entry) => entry.path));
  const missing = (await referenced()).filter((path) => !pinned.has(path));
  if (missing.length > 0) {
    throw new Error(`the engine references ${missing.length} unpinned fonts (first: ${missing[0]})`);
  }
  await pool(manifest.fonts, async (entry) => {
    const bytes = await readFile(join(webRoot, "fonts", entry.path));
    if (sha256(bytes) !== entry.sha256) {
      throw new Error(`${entry.path}: bundled font does not match the manifest`);
    }
  });
  const license = await readFile(licensePath);
  if (!license.equals(await readFile(join(webRoot, "fonts", "LICENSE.txt")))) {
    throw new Error("fonts/LICENSE.txt is missing or differs from the font license notice");
  }
  console.log(`verified ${manifest.fonts.length} bundled fallback fonts`);
} else {
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  const pinned = new Map(manifest.fonts.map((entry) => [entry.path, entry]));
  const missing = (await referenced()).filter((path) => !pinned.has(path));
  if (missing.length > 0) {
    throw new Error(
      `the engine references ${missing.length} fonts missing from the manifest ` +
      `(first: ${missing[0]}); run: node tool/web_fonts.mjs update ${webRoot}`,
    );
  }
  await pool(manifest.fonts, async (entry) => {
    const target = join(webRoot, "fonts", entry.path);
    await mkdir(dirname(target), { recursive: true });
    await copyFile(await cached(entry), target);
    if ((await stat(target)).size !== entry.bytes) {
      throw new Error(`${entry.path}: copied size does not match the manifest`);
    }
  });
  await copyFile(licensePath, join(webRoot, "fonts", "LICENSE.txt"));
  console.log(`bundled ${manifest.fonts.length} verified fallback fonts into ${join(webRoot, "fonts")}`);
}
