package codesignature_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/codesignature"
)

// The smuggling case that motivated this, expressed as a compile-time fact.
//
// `identifier "anchor apple subject.OU"` used to satisfy every check, because
// the checks were `strings.Contains` and all three words appeared inside one
// literal. There is now no way to write that at all: a Requirement cannot be
// built from a string, and the only constructor validates two fields whose
// character sets exclude quotes, spaces and operators. This test asserts what
// remains possible to attempt — a field carrying expression syntax — and that
// it is refused.
func TestExpressionSyntaxCannotEnterAnIdentity(t *testing.T) {
	t.Parallel()

	for _, identifier := range []codesignature.SigningIdentifier{
		`anchor apple subject.OU`,
		`com.example" or anchor apple and identifier "x`,
		`com.example and anchor apple`,
		`com.example[field.1.2.3]`,
		"com.example\tor",
		"com.example\nor",
		`com.example'`,
		`com.example"`,
		`com.example*`,
		`com.example=x`,
		"",
		".leading",
		"trailing.",
		"double..dot",
		codesignature.SigningIdentifier(strings.Repeat("a", 129)),
	} {
		if _, err := codesignature.DeveloperIDRequirement(
			identifier, "Q6L2SF6YDW",
		); !errors.Is(err, codesignature.ErrInvalidIdentifier) {
			t.Fatalf("identifier %q was accepted: %v", identifier, err)
		}
	}
	for _, team := range []codesignature.TeamID{
		"",
		"SHORT",
		"TOOLONGTEAMID",
		"q6l2sf6ydw",
		`Q6L2SF6YD"`,
		"Q6L2SF6YD ",
		"Q6L2SF6Y.W",
	} {
		if _, err := codesignature.DeveloperIDRequirement(
			"com.example.app", team,
		); !errors.Is(err, codesignature.ErrInvalidTeamID) {
			t.Fatalf("team %q was accepted: %v", team, err)
		}
	}
}

// The generated shape is the whole security claim, so it is asserted clause by
// clause rather than as one opaque string. Losing any one of these widens what
// the requirement accepts.
func TestTheGeneratedRequirementCarriesEveryClause(t *testing.T) {
	t.Parallel()

	requirement, err := codesignature.DeveloperIDRequirement(
		"com.anthropic.claude-code", "Q6L2SF6YDW",
	)
	if err != nil {
		t.Fatal(err)
	}
	expression := requirement.Expression()
	for _, clause := range []struct {
		name string
		text string
	}{
		{"the identifier, so a publisher's other programs do not match",
			`identifier "com.anthropic.claude-code"`},
		{"Apple's root as the anchor",
			"anchor apple generic"},
		{"the Developer ID intermediate extension",
			"certificate 1[field.1.2.840.113635.100.6.2.6]"},
		{"the Developer ID leaf extension",
			"certificate leaf[field.1.2.840.113635.100.6.1.13]"},
		{"the publisher's team",
			`certificate leaf[subject.OU] = "Q6L2SF6YDW"`},
	} {
		if !strings.Contains(expression, clause.text) {
			t.Fatalf("the requirement lost %s: %s", clause.name, expression)
		}
	}
}

// A zero Requirement is what a caller gets by declaring one instead of
// building it, and it must reach no platform call.
func TestAnUnbuiltRequirementIsRefused(t *testing.T) {
	t.Parallel()

	var unbuilt codesignature.Requirement
	if unbuilt.Usable() {
		t.Fatal("a zero requirement reported usable")
	}
	if err := codesignature.Verify(
		context.Background(), "/bin/echo", unbuilt,
	); err == nil {
		t.Fatal("a zero requirement reached the platform")
	}
}

func TestVerifyRejectsUnusableInput(t *testing.T) {
	t.Parallel()

	requirement := mustRequirement(t, "com.apple.echo", "Q6L2SF6YDW")
	//nolint:staticcheck // a nil context is exactly what this rejects.
	if err := codesignature.Verify(nil, "/bin/echo", requirement); err == nil {
		t.Fatal("a nil context was accepted")
	}
	if err := codesignature.Verify(
		context.Background(), "relative/path", requirement,
	); err == nil {
		t.Fatal("a relative path was accepted")
	}
}

func TestAPlatformWithoutSignedIdentityDegradesInsteadOfGuessing(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "darwin" {
		t.Skip("this is about the platforms that have no such check")
	}
	err := codesignature.Verify(
		context.Background(),
		"/bin/echo",
		mustRequirement(t, "com.apple.echo", "Q6L2SF6YDW"),
	)
	if !errors.Is(err, codesignature.ErrUnsupportedPlatform) {
		t.Fatalf("got %v, want ErrUnsupportedPlatform", err)
	}
}

// A Developer ID requirement names a third-party publisher, and Apple's own
// platform binaries are not signed that way. So the probe this package can
// always reach is a negative case, and the positive one belongs where a real
// client is available.
func TestApplePlatformBinariesAreNotDeveloperIDSigned(t *testing.T) {
	t.Parallel()

	requireDarwinProbe(t)
	err := codesignature.Verify(
		context.Background(),
		"/bin/echo",
		mustRequirement(t, "com.apple.echo", "Q6L2SF6YDW"),
	)
	if !errors.Is(err, codesignature.ErrNotSatisfied) {
		t.Fatalf("got %v, want ErrNotSatisfied", err)
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
		context.Background(),
		path,
		mustRequirement(t, "com.example.app", "Q6L2SF6YDW"),
	); !errors.Is(err, codesignature.ErrNotSatisfied) {
		t.Fatalf("an unsigned file was accepted: %v", err)
	}
}

func mustRequirement(
	t *testing.T,
	identifier codesignature.SigningIdentifier,
	team codesignature.TeamID,
) codesignature.Requirement {
	t.Helper()

	requirement, err := codesignature.DeveloperIDRequirement(identifier, team)
	if err != nil {
		t.Fatal(err)
	}
	return requirement
}

func requireDarwinProbe(t *testing.T) {
	t.Helper()

	if runtime.GOOS != "darwin" {
		t.Skip("signed-identity verification is macOS-only here")
	}
	if _, err := os.Stat("/usr/bin/codesign"); err != nil {
		t.Skipf("codesign is unavailable: %v", err)
	}
	if _, err := os.Stat("/bin/echo"); err != nil {
		t.Skipf("/bin/echo is unavailable: %v", err)
	}
}
