import { execFileSync } from "node:child_process";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

// Check deployment versions, not SDK versions: a new SDK can still target 14.
export function validateMacOSBuildVersions(source, sliceCount, label) {
  const platforms = [...source.matchAll(/^\s*platform (\S+)\s*$/gmu)];
  const versions = [...source.matchAll(/^\s*minos (\d+(?:\.\d+){1,2})\s*$/gmu)];
  if (
    sliceCount < 1 ||
    platforms.length !== sliceCount ||
    versions.length !== sliceCount ||
    platforms.some((match) => match[1] !== "MACOS") ||
    versions.some((match) => {
      const [major, minor, patch = 0] = match[1].split(".").map(Number);
      return major > 14 || (major === 14 && (minor > 0 || patch > 0));
    })
  ) {
    throw new Error(`${label} must support macOS 14.0 on every architecture`);
  }
}

export function verifyMacOSBuildVersions(path) {
  const options = {
    encoding: "utf8",
    timeout: 10_000,
    maxBuffer: 64 << 10,
    env: { ...process.env, LANG: "C", LC_ALL: "C" },
  };
  const architectures = execFileSync("/usr/bin/lipo", ["-archs", path], options)
    .trim()
    .split(/\s+/u);
  validateMacOSBuildVersions(
    execFileSync("/usr/bin/vtool", ["-show-build", path], options),
    architectures.length,
    path,
  );
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  if (process.argv.length < 3) throw new Error("Supply the Mach-O paths to verify");
  for (const path of process.argv.slice(2)) verifyMacOSBuildVersions(path);
}
