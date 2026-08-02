package codesignature_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/vibe-agi/vibermate/internal/codesignature"
)

// appleSignedProbe is a file the operating system ships and signs itself. It
// stands in for a client binary so these runs need nothing installed.
const appleSignedProbe = "/bin/echo"

// appleEchoRequirement is the platform's own designated requirement for the
// probe binary, taken whole. Apple signs its binaries with no Developer ID
// team, which is why a catalog would never hold this one — but it is a valid
// identity claim and that is what this package evaluates.
const appleAnchor = codesignature.Requirement(
	`identifier "com.apple.echo" and anchor apple`,
)

func TestARequirementMustAnchorToThePlatformRoot(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name        string
		requirement codesignature.Requirement
		want        bool
	}{
		{"empty", "", false},
		{"whitespace", "   ", false},
		{
			name:        "identity without an anchor accepts anything self-signed",
			requirement: `identifier "com.anthropic.claude-code"`,
			want:        false,
		},
		{
			name:        "team identity without an anchor is no better",
			requirement: `certificate leaf[subject.OU] = "Q6L2SF6YDW"`,
			want:        false,
		},
		{
			name: "an anchored team without an identifier accepts every program that publisher signed",
			requirement: `anchor apple generic and ` +
				`certificate leaf[subject.OU] = "Q6L2SF6YDW"`,
			want: false,
		},
		{
			name: "an anchored identifier is an identity claim",
			requirement: `identifier "com.anthropic.claude-code" and ` +
				`anchor apple generic and ` +
				`certificate leaf[subject.OU] = "Q6L2SF6YDW"`,
			want: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := testCase.requirement.Valid(); got != testCase.want {
				t.Fatalf("got %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestVerifyRejectsUnusableInput(t *testing.T) {
	t.Parallel()

	//nolint:staticcheck // a nil context is exactly what this rejects.
	if err := codesignature.Verify(nil, "/bin/echo", appleAnchor); err == nil {
		t.Fatal("a nil context was accepted")
	}
	if err := codesignature.Verify(
		context.Background(), "relative/path", appleAnchor,
	); err == nil {
		t.Fatal("a relative path was accepted")
	}
	if err := codesignature.Verify(
		context.Background(), "/bin/echo", `identifier "x"`,
	); err == nil {
		t.Fatal("an unanchored requirement was accepted")
	}
	if err := codesignature.Verify(
		context.Background(), "/bin/echo", "anchor apple generic",
	); err == nil {
		t.Fatal("a requirement naming no identifier was accepted")
	}
}

func TestAPlatformWithoutSignedIdentityDegradesInsteadOfGuessing(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "darwin" {
		t.Skip("this is about the platforms that have no such check")
	}
	err := codesignature.Verify(context.Background(), "/bin/echo", appleAnchor)
	if !errors.Is(err, codesignature.ErrUnsupportedPlatform) {
		t.Fatalf("got %v, want ErrUnsupportedPlatform", err)
	}
}

func TestThePlatformAcceptsAFileItSigned(t *testing.T) {
	t.Parallel()

	requireDarwinProbe(t)
	if err := codesignature.Verify(
		context.Background(), appleSignedProbe, appleAnchor,
	); err != nil {
		t.Fatalf("%s is signed by Apple and was refused: %v", appleSignedProbe, err)
	}
}

func TestADifferentSignerIsRefused(t *testing.T) {
	t.Parallel()

	requireDarwinProbe(t)
	// Apple signs its own binaries with no Developer ID team, so requiring one
	// names a signer this file does not have.
	err := codesignature.Verify(
		context.Background(),
		appleSignedProbe,
		`identifier "com.anthropic.claude-code" and anchor apple generic and `+
			`certificate leaf[subject.OU] = "Q6L2SF6YDW"`,
	)
	if !errors.Is(err, codesignature.ErrNotSatisfied) {
		t.Fatalf("got %v, want ErrNotSatisfied", err)
	}
}

// The property the whole tier rests on: accepting a signer must not accept a
// modified copy of that signer's file.
func TestAModifiedCopyIsRefused(t *testing.T) {
	t.Parallel()

	requireDarwinProbe(t)
	original, err := os.ReadFile(appleSignedProbe)
	if err != nil {
		t.Skipf("%s is unreadable: %v", appleSignedProbe, err)
	}
	if len(original) < 2 {
		t.Skipf("%s is too small to modify meaningfully", appleSignedProbe)
	}
	copied := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(copied, original, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := codesignature.Verify(
		context.Background(), copied, appleAnchor,
	); err != nil {
		t.Fatalf("an unmodified copy was refused, so the rest proves nothing: %v", err)
	}

	modified := append([]byte(nil), original...)
	middle := len(modified) / 2
	modified[middle] ^= 0xFF
	if err := os.WriteFile(copied, modified, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := codesignature.Verify(
		context.Background(), copied, appleAnchor,
	); !errors.Is(err, codesignature.ErrNotSatisfied) {
		t.Fatalf("a modified copy was accepted: %v", err)
	}
}

func TestAnUnsignedFileIsRefused(t *testing.T) {
	t.Parallel()

	requireDarwinProbe(t)
	path := filepath.Join(t.TempDir(), "unsigned")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := codesignature.Verify(
		context.Background(), path, appleAnchor,
	); !errors.Is(err, codesignature.ErrNotSatisfied) {
		t.Fatalf("an unsigned file was accepted: %v", err)
	}
}

func requireDarwinProbe(t *testing.T) {
	t.Helper()

	if runtime.GOOS != "darwin" {
		t.Skip("signed-identity verification is macOS-only here")
	}
	if _, err := os.Stat("/usr/bin/codesign"); err != nil {
		t.Skipf("codesign is unavailable: %v", err)
	}
	if _, err := os.Stat(appleSignedProbe); err != nil {
		t.Skipf("%s is unavailable: %v", appleSignedProbe, err)
	}
}
