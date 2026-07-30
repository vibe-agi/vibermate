import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { chmod, mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDirectory = dirname(fileURLToPath(import.meta.url));
const desktopDirectory = resolve(scriptDirectory, "..");
const repositoryDirectory = resolve(desktopDirectory, "../..");
const binariesDirectory = resolve(desktopDirectory, "src-tauri", "binaries");
const profileArgument = process.argv[2];
const profile = profileArgument?.startsWith("--profile=")
  ? profileArgument.slice("--profile=".length)
  : undefined;
if (process.argv.length !== 3 || profile !== "development") {
  throw new Error("Sidecar build requires --profile=development");
}

function commandOutput(command, commandArguments) {
  const result = spawnSync(command, commandArguments, {
    cwd: repositoryDirectory,
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(`Could not run ${command}`);
  }
  const output = result.stdout.trim();
  if (output.length === 0) {
    throw new Error(`${command} returned no output`);
  }
  return output;
}

async function sha256(path) {
  const content = await readFile(path);
  return createHash("sha256").update(content).digest("hex");
}

const rustVersion = commandOutput("rustc", ["-vV"]);
const hostLine = rustVersion
  .split(/\r?\n/u)
  .find((line) => line.startsWith("host: "));
const target = hostLine?.slice("host: ".length);
if (target !== "aarch64-apple-darwin") {
  throw new Error("M0 Desktop sidecars support only Darwin arm64");
}

await mkdir(binariesDirectory, { recursive: true, mode: 0o700 });
const sidecarDigests = {};
for (const command of ["vibermated", "vibermate"]) {
  const output = resolve(binariesDirectory, `${command}-${target}`);
  const buildArguments = ["build", "-buildvcs=true", "-trimpath"];
  buildArguments.push("-o", output, `./cmd/${command}`);
  const build = spawnSync(
    "go",
    buildArguments,
    {
      cwd: repositoryDirectory,
      stdio: "inherit",
    },
  );
  if (build.status !== 0) {
    throw new Error(`Could not build the ${command} sidecar`);
  }
  sidecarDigests[command] = await sha256(output);
}

const configurationPaths = {
  "go.mod": resolve(repositoryDirectory, "go.mod"),
  "go.sum": resolve(repositoryDirectory, "go.sum"),
  "ui/desktop/package.json": resolve(desktopDirectory, "package.json"),
  "ui/desktop/pnpm-lock.yaml": resolve(desktopDirectory, "pnpm-lock.yaml"),
  "ui/desktop/src-tauri/Cargo.toml": resolve(
    desktopDirectory,
    "src-tauri",
    "Cargo.toml",
  ),
  "ui/desktop/src-tauri/Cargo.lock": resolve(
    desktopDirectory,
    "src-tauri",
    "Cargo.lock",
  ),
  "ui/desktop/src-tauri/tauri.conf.json": resolve(
    desktopDirectory,
    "src-tauri",
    "tauri.conf.json",
  ),
};
const configurationSHA256 = {};
for (const [name, path] of Object.entries(configurationPaths)) {
  configurationSHA256[name] = await sha256(path);
}

const sourceStatus = spawnSync(
  "git",
  ["status", "--porcelain=v1", "--untracked-files=all"],
  {
    cwd: repositoryDirectory,
    encoding: "utf8",
  },
);
if (sourceStatus.status !== 0) {
  throw new Error("Could not inspect the Git worktree");
}
const commitTime = new Date(
  commandOutput("git", ["show", "-s", "--format=%cI", "HEAD"]),
).toISOString().replace(".000Z", "Z");
const manifest = {
  schema: "vibermate.desktop-build/v1",
  source: {
    vcs: "git",
    revision: commandOutput("git", ["rev-parse", "HEAD"]),
    commitTime,
    dirty: sourceStatus.stdout.trim().length !== 0,
  },
  profiles: {
    desktop: "release",
    sidecars: profile,
    target,
  },
  toolchains: {
    go: commandOutput("go", ["version"]),
    node: process.version,
    rustc: rustVersion,
    cargo: commandOutput("cargo", ["--version"]),
    pnpm: commandOutput("pnpm", ["--version"]),
    tauri: commandOutput("pnpm", [
      "--dir",
      desktopDirectory,
      "exec",
      "tauri",
      "--version",
    ]),
  },
  configurationSHA256,
  sidecarSHA256: sidecarDigests,
};
const manifestPath = resolve(
  binariesDirectory,
  "vibermate-build-manifest.json",
);
const temporaryPath = `${manifestPath}.tmp`;
await writeFile(temporaryPath, `${JSON.stringify(manifest, null, 2)}\n`, {
  mode: 0o600,
});
await chmod(temporaryPath, 0o600);
await rename(temporaryPath, manifestPath);
