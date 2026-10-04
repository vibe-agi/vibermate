package clientadapter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPinnedCodexResolutionPreservesDeclaredLayouts(t *testing.T) {
	for _, nested := range []bool{false, true} {
		root := t.TempDir()
		entry := filepath.Join(root, "codex", "bin", "codex.js")
		release := CodexCLI01592DarwinARM64()
		if nested {
			for i := range release.Artifacts {
				if release.Artifacts[i].Role == ArtifactPlatformPackageMetadata {
					release.Artifacts[i].RelativePath = "../node_modules/@openai/codex-darwin-arm64/package.json"
				}
			}
		}
		if err := os.MkdirAll(filepath.Dir(entry), 0700); err != nil {
			t.Fatal(err)
		}
		for _, artifact := range release.Artifacts {
			if artifact.Role == ArtifactPlatformPackageMetadata {
				metadata := filepath.Join(filepath.Dir(entry), artifact.RelativePath)
				if err := os.MkdirAll(filepath.Dir(metadata), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(metadata, []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := verifyCodexPackageResolution(entry, release); err != nil {
			t.Fatalf("declared nested=%t rejected: %v", nested, err)
		}
	}
}

func TestPinnedCodexResolutionRejectsEarlierAndUnsupportedPackages(t *testing.T) {
	for _, testCase := range []struct {
		name, shadow, metadata, osName, arch string
		link                                 bool
	}{
		{name: "bin shadow", shadow: "codex/bin/node_modules/@openai/codex-darwin-arm64"},
		{name: "nested shadow", shadow: "codex/node_modules/@openai/codex-darwin-arm64"},
		{name: "scope shadow", shadow: "node_modules/@openai/codex-darwin-arm64"},
		{name: "dangling link", shadow: "codex/node_modules/@openai/codex-darwin-arm64", link: true},
		{name: "unsupported declaration", metadata: "../../elsewhere/codex-darwin-arm64/package.json"},
		{name: "wrong architecture", arch: "amd64"},
		{name: "wrong OS", osName: "linux"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			entry := filepath.Join(root, "codex", "bin", "codex.js")
			if err := os.MkdirAll(filepath.Dir(entry), 0700); err != nil {
				t.Fatal(err)
			}
			release := CodexCLI01600DarwinARM64()
			if testCase.metadata != "" {
				for i := range release.Artifacts {
					if release.Artifacts[i].Role == ArtifactPlatformPackageMetadata {
						release.Artifacts[i].RelativePath = testCase.metadata
					}
				}
			}
			if testCase.osName != "" {
				release.OperatingSystem = testCase.osName
			}
			if testCase.arch != "" {
				release.Architecture = testCase.arch
			}
			if testCase.shadow != "" {
				path := filepath.Join(root, testCase.shadow)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if testCase.link {
					if err := os.Symlink(filepath.Join(t.TempDir(), "absent"), path); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := verifyCodexPackageResolution(entry, release); err == nil {
				t.Fatal("invalid package resolution accepted")
			}
		})
	}
}
