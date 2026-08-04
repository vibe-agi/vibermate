import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  bundleProfileFromEnvironment,
  requireBundleSigningBoundary,
  requireDesktopBundleTarget,
} from "./desktop-build-policy.mjs";

const desktopDirectory = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const profile = bundleProfileFromEnvironment(process.env);
requireDesktopBundleTarget(profile, process.env);
requireBundleSigningBoundary(profile, "build", process.env);
const pnpm = process.platform === "win32" ? "pnpm.cmd" : "pnpm";
const result = spawnSync(
  pnpm,
  ["run", `prepare:desktop:${profile}`],
  {
    cwd: desktopDirectory,
    stdio: "inherit",
  },
);
if (result.error !== undefined) {
  throw result.error;
}
if (result.status !== 0) {
  throw new Error(`Could not prepare the ${profile} Desktop bundle`);
}
