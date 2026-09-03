package clientadapter

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A client this build has evidence for, at a version it does not, is a
// different situation from a program nobody has ever catalogued. The first is
// launched without a trust root and will fail its handshake; the second was
// never going to have one. Telling them apart is what lets the product explain
// itself instead of going quiet.
func TestRecognitionTellsAnUnknownProgramFromAnUncatalogedVersion(t *testing.T) {
	t.Parallel()

	knownRelease := CodexCLI01450DarwinARM64()
	knownRelease.OperatingSystem = runtime.GOOS
	knownRelease.Architecture = runtime.GOARCH
	catalog, err := NewCatalog(1, []Release{knownRelease})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewReleaseVerifier(catalog)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	unknown := filepath.Join(directory, "some-tool")
	writeExecutable(t, unknown, "#!/bin/sh\nexit 0\n")
	detection, err := verifier.Verify(context.Background(), Request{
		Command:        []string{"some-tool"},
		ExecutablePath: unknown,
		CWD:            directory,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detection.Recognition != RecognitionUnknown {
		t.Fatalf("recognition = %q", detection.Recognition)
	}

	// The same shape, under a name the catalog knows, with contents it does
	// not: recognized, unverified.
	known := filepath.Join(directory, "codex")
	writeExecutable(t, known, "#!/bin/sh\nexit 0\n")
	detection, err = verifier.Verify(context.Background(), Request{
		Command:        []string{"codex"},
		ExecutablePath: known,
		CWD:            directory,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detection.Status != StatusGeneric {
		t.Fatalf("status = %q", detection.Status)
	}
	if detection.Recognition != RecognitionUnverified {
		t.Fatalf("recognition = %q", detection.Recognition)
	}
}

func writeExecutable(t *testing.T, path string, contents string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}
