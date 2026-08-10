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

const developmentBundle = resolve(
  desktopDirectory,
  "src-tauri/target/aarch64-apple-darwin/release/bundle/macos/ViberMate.app",
);
const developmentExecutables = [
  resolve(developmentBundle, "Contents/MacOS/vibermate-desktop"),
  resolve(developmentBundle, "Contents/MacOS/vibermated"),
];

function runningDevelopmentProcesses() {
  if (process.platform !== "darwin") {
    return [];
  }
  const result = spawnSync("ps", ["-axo", "pid=,command="], {
    encoding: "utf8",
  });
  if (result.error !== undefined) {
    throw result.error;
  }
  if (result.status !== 0) {
    throw new Error("could not inspect running ViberMate development processes");
  }
  return result.stdout
    .split("\n")
    .map((line) => line.trim().match(/^(\d+)\s+(.+)$/))
    .filter((match) =>
      match !== null &&
      developmentExecutables.some(
        (executable) =>
          match[2] === executable || match[2].startsWith(`${executable} `),
      ),
    )
    .map((match) => Number(match[1]));
}

function stopRunningDevelopmentBundle() {
  const running = runningDevelopmentProcesses();
  if (running.length === 0) {
    return false;
  }

  // Stop only executables inside this checkout's development bundle. The
  // desktop parent owns bounded daemon shutdown, so signal it first and give
  // both processes time to leave before Tauri replaces the bundle on disk.
  for (const pid of running.slice(0, 1)) {
    process.kill(pid, "SIGTERM");
  }
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    if (runningDevelopmentProcesses().length === 0) {
      return true;
    }
    spawnSync("sleep", ["0.1"]);
  }
  throw new Error(
    "the running ViberMate development bundle did not stop; it was not replaced",
  );
}

const restartDevelopmentBundle =
  profile === "development" && stopRunningDevelopmentBundle();
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
if (restartDevelopmentBundle) {
  const launch = spawnSync("open", ["-na", developmentBundle], {
    stdio: "inherit",
  });
  if (launch.error !== undefined) {
    throw launch.error;
  }
  if (launch.status !== 0) {
    throw new Error("ViberMate development bundle was built but could not restart");
  }
}
