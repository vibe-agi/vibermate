package clientadapter_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

func TestM0VerifierMatchesFixedDigestWithoutExecutingCandidate(t *testing.T) {
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
	verifier, err := clientadapter.NewM0Verifier([]clientadapter.Release{{
		ID:               "claude-code",
		Version:          "2.1.220",
		InvocationLabel:  "claude",
		ExecutableSHA256: digest(content),
		LaunchRecipe:     clientadapter.LaunchNodeEnvProxy,
	}})
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
		detection.Evidence == nil ||
		detection.Evidence.Version != "2.1.220" ||
		detection.Evidence.LaunchRecipe != clientadapter.LaunchNodeEnvProxy ||
		detection.Evidence.ExecutableSHA256 != digest(content) {
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

func TestM0VerifierLeavesUnknownExecutableOnGenericProxyRecipe(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	executable := filepath.Join(directory, "custom-agent")
	content := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(executable, content, 0o700); err != nil {
		t.Fatal(err)
	}
	verifier, err := clientadapter.NewM0Verifier([]clientadapter.Release{{
		ID:               "claude-code",
		Version:          "2.1.220",
		InvocationLabel:  "claude",
		ExecutableSHA256: digest(content),
		LaunchRecipe:     clientadapter.LaunchNodeEnvProxy,
	}})
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
