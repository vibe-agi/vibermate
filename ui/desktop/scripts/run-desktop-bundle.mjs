import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  desktopBundleProfileEnvironment,
  parseSidecarProfile,
} from "./desktop-build-policy.mjs";

const desktopDirectory = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const profile = parseSidecarProfile(process.argv.slice(2));
if (profile === "distribution") {
  throw new Error(
    "macOS distribution candidates require separate unsigned-build and signed-bundle stages",
  );
}
const pnpm = process.platform === "win32" ? "pnpm.cmd" : "pnpm";
const result = spawnSync(
  pnpm,
  [
    "exec",
    "tauri",
    "build",
    "--ci",
    "--target",
    "aarch64-apple-darwin",
  ],
  {
  cwd: desktopDirectory,
  env: {
    ...process.env,
    [desktopBundleProfileEnvironment]: profile,
  },
  stdio: "inherit",
  },
);
if (result.error !== undefined) {
  throw result.error;
}
if (result.status !== 0) {
  throw new Error(`Tauri ${profile} bundle failed`);
}
