package clientadapter_test

import (
	"context"
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
	if detection.Signer.LaunchRecipe != clientadapter.LaunchSSLCertFile {
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

// A signer entry that names no platform anchor would accept anything claiming
// the right identifier, so the catalog refuses to hold one.
func TestACatalogRefusesASignerWithoutAPlatformAnchor(t *testing.T) {
	t.Parallel()

	signer := clientadapter.ClaudeCodeSignerDarwin()
	signer.Requirement = codesignature.Requirement(
		`identifier "com.anthropic.claude-code"`,
	)
	if _, err := clientadapter.NewCatalogWithSigners(
		1,
		[]clientadapter.Release{clientadapter.ClaudeCode221220DarwinARM64()},
		[]clientadapter.Signer{signer},
	); err == nil {
		t.Fatal("an unanchored signer requirement was accepted")
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
		LaunchRecipe:    clientadapter.LaunchSSLCertFile,
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
