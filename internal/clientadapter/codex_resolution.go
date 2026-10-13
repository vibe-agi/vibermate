package clientadapter

import (
	"errors"
	"os"
	"path/filepath"
)

// The pinned official wrappers resolve platform-package/package.json using
// createRequire(import.meta.url), before their vendor fallback. Matching a
// hoisted file's hash is insufficient when a nearer package shadows it.
// This check runs only AFTER the complete declared artifact tuple matches.
func verifyCodexPackageResolution(entrypoint string, release Release) error {
	if release.ID != "codex-cli" || release.InstallShape != InstallNPMWrapperNativeChild {
		return nil
	}
	knownWrapper := false
	var metadata string
	for _, artifact := range release.Artifacts {
		if artifact.Role == ArtifactEntrypoint {
			knownWrapper = artifact.SHA256 == "134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477" || artifact.SHA256 == "61b0194f3bb6534439c8d26a3ed57d0805f84b884588b761795323eeb92fcf70"
		}
		if artifact.Role == ArtifactPlatformPackageMetadata {
			metadata = artifact.RelativePath
		}
	}
	if !knownWrapper {
		return nil
	}
	osName := release.OperatingSystem
	if osName != "darwin" && osName != "linux" {
		return errors.New("unsupported pinned Codex platform resolution")
	}
	arch := release.Architecture
	if arch == "amd64" {
		arch = "x64"
	} else if arch != "arm64" {
		return errors.New("unsupported pinned Codex architecture resolution")
	}
	packageName := "codex-" + osName + "-" + arch
	entryDirectory := filepath.Dir(entrypoint)
	declared := filepath.Clean(filepath.Join(entryDirectory, metadata))
	if filepath.Base(declared) != "package.json" || filepath.Base(filepath.Dir(declared)) != packageName {
		return errors.New("pinned Codex platform package contradicts release platform")
	}
	packageDirectory := filepath.Dir(declared)
	for _, candidate := range []string{
		filepath.Join(entryDirectory, "node_modules", "@openai", packageName),
		filepath.Join(entryDirectory, "..", "node_modules", "@openai", packageName),
		filepath.Join(entryDirectory, "../..", "node_modules", "@openai", packageName),
		filepath.Join(entryDirectory, "../..", packageName),
	} {
		if candidate == packageDirectory {
			// Node derives vendor from the resolved metadata file, not the
			// lexical package. Keep it in the same resolved directory as the
			// declared package whose native artifact was hashed above.
			resolvedMetadata, err := filepath.EvalSymlinks(declared)
			if err != nil {
				return errors.New("pinned Codex platform metadata unavailable")
			}
			resolvedPackage, err := filepath.EvalSymlinks(candidate)
			if err != nil || filepath.Dir(resolvedMetadata) != resolvedPackage {
				return errors.New("pinned Codex platform metadata redirects native child")
			}
			return nil
		}
		if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
			return errors.New("pinned Codex native child resolution is shadowed")
		}
	}
	return errors.New("unsupported pinned Codex platform package layout")
}
