import { mkdir } from "node:fs/promises";
import { spawnSync } from "node:child_process";
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

const rustVersion = spawnSync("rustc", ["-vV"], {
  cwd: repositoryDirectory,
  encoding: "utf8",
});
if (rustVersion.status !== 0) {
  throw new Error("Could not determine the Rust host target");
}
const hostLine = rustVersion.stdout
  .split(/\r?\n/u)
  .find((line) => line.startsWith("host: "));
const target = hostLine?.slice("host: ".length);
if (target !== "aarch64-apple-darwin") {
  throw new Error("M0 Desktop sidecars support only Darwin arm64");
}

await mkdir(binariesDirectory, { recursive: true, mode: 0o700 });
for (const command of ["vibermated", "vibermate"]) {
  const output = resolve(binariesDirectory, `${command}-${target}`);
  const buildArguments = ["build", "-trimpath"];
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
}
