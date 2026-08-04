import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import {
  chmod,
  copyFile,
  mkdir,
  mkdtemp,
  readFile,
  rename,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  cargoPackageVersion,
  desktopBuildManifestSchema,
  desktopDistributionSidecarTargetAliases,
  goModuleVersion,
  parseSidecarProfile,
  pnpmPackageManagerVersion,
  requireReleaseSource,
  requireStableRevision,
  rustToolchainVersion,
  sidecarBuildTags,
  validateDesktopVersions,
  validateReleaseToolchains,
} from "./desktop-build-policy.mjs";

const scriptDirectory = dirname(fileURLToPath(import.meta.url));
const desktopDirectory = resolve(scriptDirectory, "..");
const repositoryDirectory = resolve(desktopDirectory, "../..");
const binariesDirectory = resolve(desktopDirectory, "src-tauri", "binaries");
const profile = parseSidecarProfile(process.argv.slice(2));

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

function extractedVersion(label, source, pattern) {
  const match = source.match(pattern);
  if (match === null) {
    throw new Error(`Could not read the ${label} version`);
  }
  return match[1];
}

async function sha256(path) {
  const content = await readFile(path);
  return createHash("sha256").update(content).digest("hex");
}

const rustVersion = commandOutput("rustc", ["-vV"]);
const hostLine = rustVersion
  .split(/\r?\n/u)
  .find((line) => line.startsWith("host: "));
const hostTarget = hostLine?.slice("host: ".length);
const target =
  process.env.TAURI_ENV_TARGET_TRIPLE?.trim() ||
  (profile === "distribution" ? "universal-apple-darwin" : hostTarget);
if (
  (profile === "distribution" && target !== "universal-apple-darwin") ||
  (profile !== "distribution" && target !== "aarch64-apple-darwin")
) {
  throw new Error("Desktop sidecar target is inconsistent with its profile");
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
requireReleaseSource(profile, sourceStatus.stdout);
const sourceRevision = commandOutput("git", ["rev-parse", "HEAD"]);

const goModule = await readFile(resolve(repositoryDirectory, "go.mod"), "utf8");
const rustToolchain = await readFile(
  resolve(repositoryDirectory, "rust-toolchain.toml"),
  "utf8",
);
const packageConfiguration = JSON.parse(
  await readFile(resolve(desktopDirectory, "package.json"), "utf8"),
);
const tauriConfiguration = JSON.parse(
  await readFile(
    resolve(desktopDirectory, "src-tauri", "tauri.conf.json"),
    "utf8",
  ),
);
const cargoConfiguration = await readFile(
  resolve(desktopDirectory, "src-tauri", "Cargo.toml"),
  "utf8",
);
validateDesktopVersions(profile, {
  packageVersion: packageConfiguration.version,
  tauriVersion: tauriConfiguration.version,
  cargoVersion: cargoPackageVersion(cargoConfiguration),
});
const goVersion = commandOutput("go", ["version"]);
const nodeVersion = process.version;
const cargoVersion = commandOutput("cargo", ["--version"]);
const pnpmVersion = commandOutput("pnpm", ["--version"]);
const tauriVersion = commandOutput("pnpm", [
  "--dir",
  desktopDirectory,
  "exec",
  "tauri",
  "--version",
]);
validateReleaseToolchains(profile, {
  expected: {
    go: goModuleVersion(goModule),
    node: packageConfiguration.engines?.node,
    rustc: rustToolchainVersion(rustToolchain),
    cargo: rustToolchainVersion(rustToolchain),
    pnpm: pnpmPackageManagerVersion(packageConfiguration.packageManager),
    tauri: packageConfiguration.devDependencies?.["@tauri-apps/cli"],
  },
  actual: {
    go: extractedVersion("Go", goVersion, /\bgo(\d+\.\d+\.\d+)\b/u),
    node: extractedVersion("Node", nodeVersion, /^v(\d+\.\d+\.\d+)$/u),
    rustc: extractedVersion(
      "Rust",
      rustVersion,
      /^release:\s*(\d+\.\d+\.\d+)\s*$/mu,
    ),
    cargo: extractedVersion(
      "Cargo",
      cargoVersion,
      /^cargo\s+(\d+\.\d+\.\d+)(?:\s|$)/u,
    ),
    pnpm: extractedVersion(
      "pnpm",
      pnpmVersion,
      /^(\d+\.\d+\.\d+)$/u,
    ),
    tauri: extractedVersion(
      "Tauri",
      tauriVersion,
      /^tauri-cli\s+(\d+\.\d+\.\d+)$/u,
    ),
  },
});

await mkdir(binariesDirectory, { recursive: true, mode: 0o700 });
const sidecarDigests = {};
const buildTags = sidecarBuildTags(profile);
const temporaryDirectory = await mkdtemp(
  resolve(tmpdir(), "vibermate-sidecars-"),
);

function buildSidecarSlice(command, architecture, output) {
  const buildArguments = ["build", "-buildvcs=true", "-trimpath"];
  if (buildTags.length > 0) {
    buildArguments.push(`-tags=${buildTags.join(",")}`);
  }
  buildArguments.push("-o", output, `./cmd/${command}`);
  const build = spawnSync(
    "go",
    buildArguments,
    {
      cwd: repositoryDirectory,
      env: {
        ...process.env,
        CC: "clang",
        CGO_ENABLED: "1",
        CGO_CFLAGS: `-arch ${architecture.clang} -mmacosx-version-min=14.0`,
        CGO_CXXFLAGS: `-arch ${architecture.clang} -mmacosx-version-min=14.0`,
        CGO_LDFLAGS: `-arch ${architecture.clang} -mmacosx-version-min=14.0`,
        GOARCH: architecture.go,
        GOENV: "off",
        GOFLAGS: "",
        GOOS: "darwin",
        GOWORK: "off",
        MACOSX_DEPLOYMENT_TARGET: "14.0",
      },
      stdio: "inherit",
    },
  );
  if (build.status !== 0) {
    throw new Error(
      `Could not build the ${command} ${architecture.clang} sidecar`,
    );
  }
}

try {
  for (const command of ["vibermated", "vibermate"]) {
    const output = resolve(binariesDirectory, `${command}-${target}`);
    if (target === "universal-apple-darwin") {
      const arm64 = resolve(temporaryDirectory, `${command}-arm64`);
      const x86_64 = resolve(temporaryDirectory, `${command}-x86_64`);
      const universal = resolve(temporaryDirectory, `${command}-universal`);
      buildSidecarSlice(command, { go: "arm64", clang: "arm64" }, arm64);
      buildSidecarSlice(command, { go: "amd64", clang: "x86_64" }, x86_64);
      const merge = spawnSync(
        "lipo",
        ["-create", arm64, x86_64, "-output", universal],
        { cwd: repositoryDirectory, stdio: "inherit" },
      );
      if (merge.status !== 0) {
        throw new Error(`Could not merge the ${command} universal sidecar`);
      }
      const architectures = commandOutput("lipo", ["-archs", universal])
        .split(/\s+/u)
        .sort();
      if (
        architectures.length !== 2 ||
        architectures[0] !== "arm64" ||
        architectures[1] !== "x86_64"
      ) {
        throw new Error(`${command} does not contain both macOS architectures`);
      }
      await chmod(universal, 0o755);
      await rename(universal, output);
      // Cargo's per-architecture checks still evaluate Tauri's externalBin
      // inventory even though the distribution bundle consumes only the
      // Universal target name. Publish byte-identical aliases so both Rust
      // target checks validate against the same two-slice production sidecar
      // instead of depending on stale development artifacts.
      for (const targetAlias of desktopDistributionSidecarTargetAliases) {
        const aliasTemporary = resolve(
          temporaryDirectory,
          `${command}-${targetAlias}`,
        );
        const aliasOutput = resolve(
          binariesDirectory,
          `${command}-${targetAlias}`,
        );
        await copyFile(output, aliasTemporary);
        await chmod(aliasTemporary, 0o755);
        await rename(aliasTemporary, aliasOutput);
      }
    } else {
      const arm64 = resolve(temporaryDirectory, `${command}-arm64`);
      buildSidecarSlice(command, { go: "arm64", clang: "arm64" }, arm64);
      await chmod(arm64, 0o755);
      await rename(arm64, output);
    }
    sidecarDigests[command] = await sha256(output);
  }
} finally {
  await rm(temporaryDirectory, { recursive: true, force: true });
}

const configurationPaths = {
  "go.mod": resolve(repositoryDirectory, "go.mod"),
  "go.sum": resolve(repositoryDirectory, "go.sum"),
  "rust-toolchain.toml": resolve(repositoryDirectory, "rust-toolchain.toml"),
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

const finalSourceStatus = spawnSync(
  "git",
  ["status", "--porcelain=v1", "--untracked-files=all"],
  {
    cwd: repositoryDirectory,
    encoding: "utf8",
  },
);
if (finalSourceStatus.status !== 0) {
  throw new Error("Could not re-inspect the Git worktree");
}
requireReleaseSource(profile, finalSourceStatus.stdout);
requireStableRevision(
  sourceRevision,
  commandOutput("git", ["rev-parse", "HEAD"]),
);

const commitTime = new Date(
  commandOutput("git", ["show", "-s", "--format=%cI", sourceRevision]),
).toISOString().replace(".000Z", "Z");
const manifest = {
  schema: desktopBuildManifestSchema,
  source: {
    vcs: "git",
    revision: sourceRevision,
    commitTime,
    dirty: sourceStatus.stdout.trim().length !== 0,
  },
  profiles: {
    desktop: "release",
    sidecars: profile,
    target,
  },
  toolchains: {
    go: goVersion,
    node: nodeVersion,
    rustc: rustVersion,
    cargo: cargoVersion,
    pnpm: pnpmVersion,
    tauri: tauriVersion,
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
