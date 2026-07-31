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
