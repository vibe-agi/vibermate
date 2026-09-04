package clientadapter_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

func TestBuiltInCatalogPublishesExactExpectedEvidence(t *testing.T) {
	t.Parallel()
	catalog := clientadapter.BuiltInCatalog()
	for _, expected := range []struct {
		id              string
		version         string
		operatingSystem string
		architecture    string
		shape           clientadapter.InstallShape
		recipe          clientadapter.LaunchRecipe
	}{
		{
			id: "claude-code", version: "2.1.220",
			operatingSystem: "darwin", architecture: "arm64",
			shape:  clientadapter.InstallNativeSingleBinary,
			recipe: clientadapter.LaunchNodeEnvProxy,
		},
		{
			id: "claude-code", version: "2.1.220",
			operatingSystem: "darwin", architecture: "amd64",
			shape:  clientadapter.InstallNativeSingleBinary,
			recipe: clientadapter.LaunchNodeEnvProxy,
		},
		{
			id: "claude-code", version: "2.1.220",
			operatingSystem: "linux", architecture: "arm64",
			shape:  clientadapter.InstallNativeSingleBinary,
			recipe: clientadapter.LaunchNodeEnvProxy,
		},
		{
			id: "claude-code", version: "2.1.220",
			operatingSystem: "linux", architecture: "amd64",
			shape:  clientadapter.InstallNativeSingleBinary,
			recipe: clientadapter.LaunchNodeEnvProxy,
		},
		{
			id: "codex-cli", version: "0.145.0",
			operatingSystem: "darwin", architecture: "arm64",
			shape:  clientadapter.InstallNPMWrapperNativeChild,
			recipe: clientadapter.LaunchCodexResponsesHTTP,
		},
		{
			id: "codex-cli", version: "0.145.0",
			operatingSystem: "darwin", architecture: "amd64",
			shape:  clientadapter.InstallNPMWrapperNativeChild,
			recipe: clientadapter.LaunchCodexResponsesHTTP,
		},
		{
			id: "codex-cli", version: "0.145.0",
			operatingSystem: "linux", architecture: "arm64",
			shape:  clientadapter.InstallNPMWrapperNativeChild,
			recipe: clientadapter.LaunchCodexResponsesHTTP,
		},
		{
			id: "codex-cli", version: "0.145.0",
			operatingSystem: "linux", architecture: "amd64",
			shape:  clientadapter.InstallNPMWrapperNativeChild,
			recipe: clientadapter.LaunchCodexResponsesHTTP,
		},
	} {
		evidence, ok := catalog.ExpectedEvidenceForPlatform(
			expected.id,
			expected.version,
			expected.operatingSystem,
			expected.architecture,
		)
		if !ok {
			t.Fatalf(
				"ExpectedEvidenceForPlatform(%q, %q, %q, %q) is missing",
				expected.id,
				expected.version,
				expected.operatingSystem,
				expected.architecture,
			)
		}
		if err := evidence.Validate(); err != nil {
			t.Fatalf("ExpectedEvidence(%q, %q): %v", expected.id, expected.version, err)
		}
		if evidence.ID != expected.id ||
			evidence.Version != expected.version ||
			evidence.CatalogRevision != catalog.Revision() ||
			evidence.InstallShape != expected.shape ||
			evidence.LaunchRecipe != expected.recipe {
			t.Fatalf("ExpectedEvidence(%q, %q) = %+v", expected.id, expected.version, evidence)
		}
	}
	if _, ok := catalog.ExpectedEvidenceForPlatform(
		"claude-code",
		"latest",
		"linux",
		"amd64",
	); ok {
		t.Fatal("ExpectedEvidenceForPlatform() accepted an unfrozen client version")
	}
}

func TestBuiltInCatalogPinsOfficialClientArtifactsByPlatform(t *testing.T) {
	t.Parallel()

	type expectedArtifact struct {
		path   string
		digest string
	}
	for _, testCase := range []struct {
		name        string
		release     clientadapter.Release
		entrypoint  string
		platform    *expectedArtifact
		nativeChild *expectedArtifact
	}{
		{
			name:       "Claude Darwin ARM64",
			release:    clientadapter.ClaudeCode221220DarwinARM64(),
			entrypoint: "8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081",
		},
		{
			name:       "Claude Darwin AMD64",
			release:    clientadapter.ClaudeCode221220DarwinAMD64(),
			entrypoint: "dca7be0aa7d3d924836d440e0c6d8e3d47ef3c8e61fa5809b54b9017170ce2f3",
		},
		{
			name:       "Claude Linux ARM64",
			release:    clientadapter.ClaudeCode221220LinuxARM64(),
			entrypoint: "159e4a51d796f3bf14677577100f7efb845611b1ceaf0c30cbd8d4650d942185",
		},
		{
			name:       "Claude Linux AMD64",
			release:    clientadapter.ClaudeCode221220LinuxAMD64(),
			entrypoint: "674f61f20ff306f3100cf9200e4c36c4b70278b5bef2884549819b942a89c863",
		},
		{
			name:       "Codex Darwin ARM64",
			release:    clientadapter.CodexCLI01450DarwinARM64(),
			entrypoint: "134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477",
			platform: &expectedArtifact{
				path:   "../../codex-darwin-arm64/package.json",
				digest: "da204207716d61f06a70d96dd66e9b6c0728a3bdf8f696f31026549d47667a98",
			},
			nativeChild: &expectedArtifact{
				path:   "../../codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex",
				digest: "1da3f4e0e96028b8a771814293c3033dafd1971f943f6c7e79b0897fe705f590",
			},
		},
		{
			name:       "Codex Darwin AMD64",
			release:    clientadapter.CodexCLI01450DarwinAMD64(),
			entrypoint: "134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477",
			platform: &expectedArtifact{
				path:   "../../codex-darwin-x64/package.json",
				digest: "975bec05112b59b789762dddb6a573b1564c187a91cbb8e12f43311c0794148a",
			},
			nativeChild: &expectedArtifact{
				path:   "../../codex-darwin-x64/vendor/x86_64-apple-darwin/bin/codex",
				digest: "6db9193ce2c9a8cef2b5482612cde24202a4329dfc34f4687a036d5d7da619af",
			},
		},
		{
			name:       "Codex Linux ARM64",
			release:    clientadapter.CodexCLI01450LinuxARM64(),
			entrypoint: "134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477",
			platform: &expectedArtifact{
				path:   "../../codex-linux-arm64/package.json",
				digest: "755fa8c48bdaf0f2ad4edfb74bb56fd2d633b70beabec94938a8f8ef501e5c7b",
			},
			nativeChild: &expectedArtifact{
				path:   "../../codex-linux-arm64/vendor/aarch64-unknown-linux-musl/bin/codex",
				digest: "57d79900fe95df2ab854adf581a28ec46d7442f07445032d86453a44b577dced",
			},
		},
		{
			name:       "Codex Linux AMD64",
			release:    clientadapter.CodexCLI01450LinuxAMD64(),
			entrypoint: "134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477",
			platform: &expectedArtifact{
				path:   "../../codex-linux-x64/package.json",
				digest: "84f3243fd73f23dc27effde18f96db6ed0a939448299a14d594022e6341f0fd5",
			},
			nativeChild: &expectedArtifact{
				path:   "../../codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex",
				digest: "a2a05dafaa1acb002a45eaec0a462de5b13694fcfcd7bc43305f14781ce7be14",
			},
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			artifacts := make(map[clientadapter.ArtifactRole]clientadapter.Artifact)
			for _, artifact := range testCase.release.Artifacts {
				artifacts[artifact.Role] = artifact
			}
			if artifacts[clientadapter.ArtifactEntrypoint].SHA256 != testCase.entrypoint {
				t.Fatalf("entrypoint = %+v", artifacts[clientadapter.ArtifactEntrypoint])
			}
			for role, expected := range map[clientadapter.ArtifactRole]*expectedArtifact{
				clientadapter.ArtifactPlatformPackageMetadata: testCase.platform,
				clientadapter.ArtifactNativeChild:             testCase.nativeChild,
			} {
				actual, exists := artifacts[role]
				if expected == nil {
					if exists {
						t.Fatalf("unexpected %s = %+v", role, actual)
					}
					continue
				}
				if !exists || actual.RelativePath != expected.path || actual.SHA256 != expected.digest {
					t.Fatalf("%s = %+v", role, actual)
				}
			}
		})
	}
}

func TestReleaseVerifierMatchesFixedDigestWithoutExecutingCandidate(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	marker := filepath.Join(directory, "executed")
	versioned := filepath.Join(directory, "versions", "2.1.220")
	if err := os.MkdirAll(filepath.Dir(versioned), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("#!/bin/sh\nprintf executed > '" + marker + "'\n")
	if err := os.WriteFile(versioned, content, 0o700); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(directory, "claude")
	if err := os.Symlink(versioned, claude); err != nil {
		t.Fatal(err)
	}
	catalog, err := clientadapter.NewCatalog(
		4,
		[]clientadapter.Release{fixedTestRelease(
			"claude",
			digest(content),
		)},
	)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := clientadapter.NewReleaseVerifier(catalog)
	if err != nil {
		t.Fatal(err)
	}
	detection, err := verifier.Verify(context.Background(), clientadapter.Request{
		Command:        []string{"claude", "--print", "hello"},
		CWD:            directory,
		ExecutablePath: claude,
	})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	canonicalClaude, err := filepath.EvalSymlinks(claude)
	if err != nil {
		t.Fatal(err)
	}
	if detection.Status != clientadapter.StatusVerified ||
		detection.CanonicalPath != canonicalClaude ||
		detection.ExecutableLabel != "claude" ||
		detection.CatalogRevision != 4 ||
		detection.Evidence == nil ||
		detection.Evidence.Version != "2.1.220" ||
		detection.Evidence.LaunchRecipe != clientadapter.LaunchNodeEnvProxy ||
		detection.Evidence.ReleaseSHA256 == "" {
		t.Fatalf("verified detection = %+v", detection)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("verification executed the candidate: %v", err)
	}

	if err := os.WriteFile(
		versioned,
		append(content, []byte("# changed\n")...),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	generic, err := verifier.Verify(context.Background(), clientadapter.Request{
		Command:        []string{"claude"},
		CWD:            directory,
		ExecutablePath: claude,
	})
	if err != nil {
		t.Fatal(err)
	}
	if generic.Status != clientadapter.StatusGeneric ||
		generic.Evidence != nil {
		t.Fatalf("digest mismatch detection = %+v", generic)
	}
}

func TestReleaseVerifierLeavesUnknownExecutableOnGenericProxyRecipe(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	executable := filepath.Join(directory, "custom-agent")
	content := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(executable, content, 0o700); err != nil {
		t.Fatal(err)
	}
	catalog, err := clientadapter.NewCatalog(
		4,
		[]clientadapter.Release{fixedTestRelease(
			"claude",
			digest(content),
		)},
	)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := clientadapter.NewReleaseVerifier(catalog)
	if err != nil {
		t.Fatal(err)
	}
	detection, err := verifier.Verify(context.Background(), clientadapter.Request{
		Command:        []string{"custom-agent"},
		CWD:            directory,
		ExecutablePath: executable,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detection.Status != clientadapter.StatusGeneric ||
		detection.Evidence != nil {
		t.Fatalf("unknown executable detection = %+v", detection)
	}
}

func digest(content []byte) string {
	value := sha256.Sum256(content)
	return hex.EncodeToString(value[:])
}

func fixedTestRelease(
	invocationLabel string,
	executableDigest string,
) clientadapter.Release {
	return clientadapter.Release{
		ID:              "claude-code",
		Revision:        1,
		Version:         "2.1.220",
		OperatingSystem: runtime.GOOS,
		Architecture:    runtime.GOARCH,
		InstallShape:    clientadapter.InstallNativeSingleBinary,
		InvocationLabel: invocationLabel,
		ArtifactRoot:    ".",
		Artifacts: []clientadapter.Artifact{{
			Role:   clientadapter.ArtifactEntrypoint,
			SHA256: executableDigest,
		}},
		LaunchRecipe: clientadapter.LaunchNodeEnvProxy,
	}
}
