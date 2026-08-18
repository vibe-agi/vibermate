package clientadapter_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/codesignature"
)

// The case that motivated the tier. This machine's Codex is a build the
// catalog does not carry, and the catalog never will carry every build a user
// base runs. Detection must still say who made it.
func TestAnUncataloguedBuildFromACataloguedPublisherIsRecognized(t *testing.T) {
	t.Parallel()

	entrypoint := installedCodexEntrypoint(t)
	verifier := builtInVerifier(t)
	detection, err := verifier.Verify(context.Background(), clientadapter.Request{
		Command:        []string{"codex"},
		CWD:            "/",
		ExecutablePath: entrypoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	switch detection.Recognition {
	case clientadapter.RecognitionVerified:
		t.Skip("this machine carries the catalogued Codex build, so there is nothing to recognize")
	case clientadapter.RecognitionRecognized:
	default:
		t.Fatalf(
			"an OpenAI-signed Codex was not recognized: recognition=%q status=%q",
			detection.Recognition,
			detection.Status,
		)
	}
	if detection.Signer == nil {
		t.Fatal("a recognized detection carries no signer evidence")
	}
	if detection.Evidence != nil {
		t.Fatal("a recognized detection must not carry release evidence")
	}
	if detection.Signer.LaunchRecipe != clientadapter.LaunchCodexResponsesHTTP {
		t.Fatalf(
			"recipe is %q, want the one Codex's runtime reads",
			detection.Signer.LaunchRecipe,
		)
	}
	// The wrapper that was invoked is not the file that was checked.
	if strings.HasSuffix(detection.Signer.SignedPath, ".js") {
		t.Fatal("the unsigned wrapper was checked instead of the native child")
	}
	if !strings.HasSuffix(detection.Signer.SignedPath, "/bin/codex") {
		t.Fatalf("unexpected signed artifact %q", detection.Signer.SignedPath)
	}
}

func TestAnInstalledClaudeIsAtLeastRecognized(t *testing.T) {
	t.Parallel()

	entrypoint := installedClaudePath(t)
	verifier := builtInVerifier(t)
	detection, err := verifier.Verify(context.Background(), clientadapter.Request{
		Command:        []string{"claude"},
		CWD:            "/",
		ExecutablePath: entrypoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	switch detection.Recognition {
	case clientadapter.RecognitionVerified:
		if detection.Evidence == nil {
			t.Fatal("a verified detection carries no evidence")
		}
	case clientadapter.RecognitionRecognized:
		if detection.Signer == nil {
			t.Fatal("a recognized detection carries no signer evidence")
		}
		if detection.Signer.LaunchRecipe != clientadapter.LaunchNodeEnvProxy {
			t.Fatalf(
				"recipe is %q, want the one Claude Code's runtime reads",
				detection.Signer.LaunchRecipe,
			)
		}
	default:
		t.Fatalf(
			"an Anthropic-signed Claude Code was neither verified nor recognized: %q",
			detection.Recognition,
		)
	}
}

// Name is not identity. A program someone put on PATH as `claude` gets nothing
// from being called that.
func TestAProgramNamedLikeACatalogueClientIsNotRecognized(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "darwin" {
		t.Skip("signed-identity recognition is macOS-only here")
	}
	directory := t.TempDir()
	impostor := filepath.Join(directory, "claude")
	if err := os.WriteFile(
		impostor, []byte("#!/bin/sh\nexit 0\n"), 0o755,
	); err != nil {
		t.Fatal(err)
	}
	verifier := builtInVerifier(t)
	detection, err := verifier.Verify(context.Background(), clientadapter.Request{
		Command:        []string{"claude"},
		CWD:            "/",
		ExecutablePath: impostor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detection.Recognition != clientadapter.RecognitionUnverified {
		t.Fatalf(
			"an unsigned impostor got recognition %q",
			detection.Recognition,
		)
	}
	if detection.Signer != nil || detection.Status != clientadapter.StatusGeneric {
		t.Fatalf(
			"an unsigned impostor was given adapter treatment: %+v",
			detection,
		)
	}
}

// A catalog can no longer write a requirement, so what it can get wrong is an
// identity — and an identity that cannot produce a requirement is refused
// before the entry exists.
func TestACatalogRefusesAnUnusableSignerIdentity(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name       string
		identifier codesignature.SigningIdentifier
		team       codesignature.TeamID
	}{
		{"expression syntax in the identifier", `anchor apple subject.OU`, "Q6L2SF6YDW"},
		{"a quote in the identifier", `com.example"`, "Q6L2SF6YDW"},
		{"an empty identifier", "", "Q6L2SF6YDW"},
		{"a lowercase team", "com.example.app", "q6l2sf6ydw"},
		{"a short team", "com.example.app", "SHORT"},
		{"no team at all", "com.example.app", ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			signer := clientadapter.ClaudeCodeSignerDarwin()
			signer.SigningIdentifier = testCase.identifier
			signer.TeamID = testCase.team
			if _, err := clientadapter.NewCatalogWithSigners(
				1,
				[]clientadapter.Release{clientadapter.ClaudeCode221220DarwinARM64()},
				[]clientadapter.Signer{signer},
			); err == nil {
				t.Fatalf("%s was accepted", testCase.name)
			}
		})
	}
}

// The property the whole tier rests on, asserted against a real Developer ID
// signature: accepting a publisher must not accept a modified copy of their
// file. This lives here rather than in `codesignature` because Apple's own
// platform binaries are not Developer ID signed, so that package has no
// positive case it can reach without a client installed.
func TestAModifiedCopyOfASignedClientIsRefused(t *testing.T) {
	t.Parallel()

	entrypoint := installedClaudePath(t)
	resolved, err := filepath.EvalSymlinks(entrypoint)
	if err != nil {
		t.Skipf("claude could not be resolved: %v", err)
	}
	original, err := os.ReadFile(resolved)
	if err != nil {
		t.Skipf("the installed client is unreadable: %v", err)
	}
	requirement, err := codesignature.DeveloperIDRequirement(
		clientadapter.ClaudeCodeSignerDarwin().SigningIdentifier,
		clientadapter.ClaudeCodeSignerDarwin().TeamID,
	)
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(copied, original, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := codesignature.Verify(
		context.Background(), copied, requirement,
	); err != nil {
		t.Fatalf("an unmodified copy was refused, so the rest proves nothing: %v", err)
	}

	modified := append([]byte(nil), original...)
	modified[len(modified)/2] ^= 0xFF
	if err := os.WriteFile(copied, modified, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := codesignature.Verify(
		context.Background(), copied, requirement,
	); !errors.Is(err, codesignature.ErrNotSatisfied) {
		t.Fatalf("a modified copy was accepted: %v", err)
	}
}

// A different publisher's team, and a different program from the same
// publisher, must both be refused by the entry that names neither.
func TestNeitherAnotherTeamNorAnotherProgramIsAccepted(t *testing.T) {
	t.Parallel()

	entrypoint := installedClaudePath(t)
	resolved, err := filepath.EvalSymlinks(entrypoint)
	if err != nil {
		t.Skipf("claude could not be resolved: %v", err)
	}
	for _, testCase := range []struct {
		name       string
		identifier codesignature.SigningIdentifier
		team       codesignature.TeamID
	}{
		{"another team", "com.anthropic.claude-code", "2DC432GLL2"},
		{"another program from the same team", "com.anthropic.other", "Q6L2SF6YDW"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			requirement, err := codesignature.DeveloperIDRequirement(
				testCase.identifier, testCase.team,
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := codesignature.Verify(
				context.Background(), resolved, requirement,
			); !errors.Is(err, codesignature.ErrNotSatisfied) {
				t.Fatalf("%s was accepted: %v", testCase.name, err)
			}
		})
	}
}

// A recognized client gets the variable its runtime needs and nothing that was
// proven for one exact build.
func TestASignerCarriesNoVersionSpecificFeature(t *testing.T) {
	t.Parallel()

	release := clientadapter.CodexCLI01450DarwinARM64()
	if release.Features == 0 {
		t.Skip("the catalogued Codex release proves no feature, so there is nothing to withhold")
	}
	evidence := clientadapter.SignerEvidence{
		ID:              "codex-cli",
		Revision:        1,
		CatalogRevision: 1,
		InstallShape:    clientadapter.InstallNPMWrapperNativeChild,
		LaunchRecipe:    clientadapter.LaunchCodexResponsesHTTP,
		SignedPath:      "/absolute/path",
	}
	if err := evidence.Validate(); err != nil {
		t.Fatal(err)
	}
	// SignerEvidence has no Feature field at all, so a recognized client
	// cannot inherit one. This asserts the shape rather than a value.
	if _, ok := any(evidence).(interface {
		Supports(clientadapter.Feature) bool
	}); ok {
		t.Fatal("signer evidence exposes feature support")
	}
}

func builtInVerifier(t *testing.T) *clientadapter.ReleaseVerifier {
	t.Helper()

	verifier, err := clientadapter.NewReleaseVerifier(
		clientadapter.BuiltInCatalog(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func installedClaudePath(t *testing.T) string {
	t.Helper()

	if runtime.GOOS != "darwin" {
		t.Skip("signed-identity recognition is macOS-only here")
	}
	// The launcher submits the path it resolved from PATH, not the symlink
	// target: the label a catalog entry matches on comes from that name.
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("claude is not installed: %v", err)
	}
	return path
}

func installedCodexEntrypoint(t *testing.T) string {
	t.Helper()

	if runtime.GOOS != "darwin" {
		t.Skip("signed-identity recognition is macOS-only here")
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Skipf("codex is not installed: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Skipf("codex could not be resolved: %v", err)
	}
	if filepath.Base(resolved) != "codex.js" {
		t.Skipf(
			"this Codex install is shaped as %q, not the npm wrapper this entry describes",
			filepath.Base(resolved),
		)
	}
	return path
}
