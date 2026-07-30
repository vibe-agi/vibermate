import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDirectory = dirname(fileURLToPath(import.meta.url));
const desktopDirectory = resolve(scriptDirectory, "..");
const repositoryDirectory = resolve(desktopDirectory, "../..");
const checkOnly = process.argv.slice(2).includes("--check");
const locales = ["en-US", "zh-CN"];

let drift = false;
for (const locale of locales) {
  const sourcePath = resolve(repositoryDirectory, "locales", `${locale}.json`);
  const destinationPath = resolve(
    desktopDirectory,
    "src",
    "generated",
    "locales",
    `${locale}.json`,
  );
  const parsed = JSON.parse(await readFile(sourcePath, "utf8"));
  const canonical = `${JSON.stringify(parsed, null, 2)}\n`;
  if (checkOnly) {
    let current = "";
    try {
      current = await readFile(destinationPath, "utf8");
    } catch {
      drift = true;
      continue;
    }
    if (current !== canonical) {
      drift = true;
    }
    continue;
  }
  await mkdir(dirname(destinationPath), { recursive: true, mode: 0o700 });
  await writeFile(destinationPath, canonical, "utf8");
}

if (drift) {
  throw new Error("Desktop locale artifacts are out of date; run pnpm generate");
}
