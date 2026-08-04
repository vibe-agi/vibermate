import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { lstat, readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  bundleProfileFromEnvironment,
  cargoPackageVersion,
  desktopBundleTarget,
  requireBundleSigningBoundary,
  requireDesktopBundleTarget,
  validateDesktopVersions,
  validatePreparedBundle,
} from "./desktop-build-policy.mjs";

const scriptDirectory = dirname(fileURLToPath(import.meta.url));
const desktopDirectory = resolve(scriptDirectory, "..");
const repositoryDirectory = resolve(desktopDirectory, "../..");
const binariesDirectory = resolve(desktopDirectory, "src-tauri", "binaries");
const profile = bundleProfileFromEnvironment(process.env);
requireDesktopBundleTarget(profile, process.env);
requireBundleSigningBoundary(profile, "bundle", process.env);
const target = desktopBundleTarget(profile);

function command(commandName, commandArguments) {
  const result = spawnSync(commandName, commandArguments, {
    cwd: repositoryDirectory,
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(`Could not run ${commandName}`);
  }
  return result.stdout.trim();
}

async function regularFile(path, label, executable = false) {
  const info = await lstat(path);
  if (!info.isFile() || info.size <= 0) {
    throw new Error(`${label} must be a non-empty regular file`);
  }
  if (executable && (info.mode & 0o111) === 0) {
    throw new Error(`${label} must be executable`);
  }
}

async function sha256(path) {
  const content = await readFile(path);
  return createHash("sha256").update(content).digest("hex");
}

const manifestPath = resolve(
  binariesDirectory,
  "vibermate-build-manifest.json",
);
await regularFile(manifestPath, "Desktop build manifest");
const manifestInfo = await lstat(manifestPath);
if (manifestInfo.size > 128 * 1024) {
  throw new Error("Desktop build manifest is too large");
}
const manifest = JSON.parse(await readFile(manifestPath, "utf8"));

const packagePath = resolve(desktopDirectory, "package.json");
const tauriPath = resolve(desktopDirectory, "src-tauri", "tauri.conf.json");
const cargoPath = resolve(desktopDirectory, "src-tauri", "Cargo.toml");
const packageConfiguration = JSON.parse(await readFile(packagePath, "utf8"));
const tauriConfiguration = JSON.parse(await readFile(tauriPath, "utf8"));
const cargoConfiguration = await readFile(cargoPath, "utf8");
validateDesktopVersions(profile, {
  packageVersion: packageConfiguration.version,
  tauriVersion: tauriConfiguration.version,
  cargoVersion: cargoPackageVersion(cargoConfiguration),
});

const configurationPaths = {
  "go.mod": resolve(repositoryDirectory, "go.mod"),
  "go.sum": resolve(repositoryDirectory, "go.sum"),
  "rust-toolchain.toml": resolve(repositoryDirectory, "rust-toolchain.toml"),
  "ui/desktop/package.json": packagePath,
  "ui/desktop/pnpm-lock.yaml": resolve(desktopDirectory, "pnpm-lock.yaml"),
  "ui/desktop/src-tauri/Cargo.toml": cargoPath,
  "ui/desktop/src-tauri/Cargo.lock": resolve(
    desktopDirectory,
    "src-tauri",
    "Cargo.lock",
  ),
  "ui/desktop/src-tauri/tauri.conf.json": tauriPath,
};
const currentConfigurationSHA256 = {};
for (const [name, path] of Object.entries(configurationPaths)) {
  await regularFile(path, name);
  currentConfigurationSHA256[name] = await sha256(path);
}

const currentSidecarSHA256 = {};
for (const name of ["vibermated", "vibermate"]) {
  const path = resolve(binariesDirectory, `${name}-${target}`);
  await regularFile(path, `${name} sidecar`, true);
  currentSidecarSHA256[name] = await sha256(path);
}

const status = spawnSync(
  "git",
  ["status", "--porcelain=v1", "--untracked-files=all"],
  {
    cwd: repositoryDirectory,
    encoding: "utf8",
  },
);
if (status.status !== 0) {
  throw new Error("Could not inspect the Git worktree before bundling");
}
validatePreparedBundle(profile, {
  manifest,
  currentRevision: command("git", ["rev-parse", "HEAD"]),
  currentCommitTime: new Date(
    command("git", ["show", "-s", "--format=%cI", "HEAD"]),
  ).toISOString().replace(".000Z", "Z"),
  porcelainStatus: status.stdout,
  currentToolchains: {
    go: command("go", ["version"]),
    node: process.version,
    rustc: command("rustc", ["-vV"]),
    cargo: command("cargo", ["--version"]),
    pnpm: command("pnpm", ["--version"]),
    tauri: command("pnpm", [
      "--dir",
      desktopDirectory,
      "exec",
      "tauri",
      "--version",
    ]),
  },
  currentConfigurationSHA256,
  currentSidecarSHA256,
});
