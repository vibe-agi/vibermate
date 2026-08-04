import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  desktopBundleProfileEnvironment,
  desktopUnsignedDistributionBundleEnvironment,
} from "./desktop-build-policy.mjs";

if (process.argv.length !== 2) {
  throw new Error("The unsigned macOS candidate builder accepts no arguments");
}

const desktopDirectory = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const pnpm = process.platform === "win32" ? "pnpm.cmd" : "pnpm";
function runTauri(arguments_, extraEnvironment = {}) {
  const result = spawnSync(pnpm, ["exec", "tauri", ...arguments_], {
    cwd: desktopDirectory,
    env: {
      ...process.env,
      ...extraEnvironment,
      [desktopBundleProfileEnvironment]: "distribution",
    },
    stdio: "inherit",
  });
  if (result.error !== undefined) {
    throw result.error;
  }
  if (result.status !== 0) {
    throw new Error("Unsigned universal macOS candidate build failed");
  }
}

runTauri([
  "build",
  "--ci",
  "--target",
  "universal-apple-darwin",
  "--no-bundle",
  "--no-sign",
]);
runTauri(
  [
    "bundle",
    "--ci",
    "--target",
    "universal-apple-darwin",
    "--bundles",
    "app",
    "--no-sign",
  ],
  { [desktopUnsignedDistributionBundleEnvironment]: "1" },
);
