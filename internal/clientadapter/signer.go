package clientadapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/vibe-agi/vibermate/internal/codesignature"
)

// Signer is a catalogued publisher identity.
//
// Unlike a Release it freezes no version, and that is the whole point: a
// publisher's signature is the same across their releases, so a user who
// updated this morning is not locked out until we ship a catalog naming their
// build. See `docs/adr/0016-signed-identity-client-recognition.md` in the
// design repository.
//
// It deliberately carries no Feature set. A feature was proven for one exact
// release and a different build has not proven it, so a recognized client
// inherits no version-specific behaviour — only the environment variable its
// runtime family needs in order to read a CA file at all.
type Signer struct {
	ID              string
	Revision        AdapterRevision
	OperatingSystem string
	Architecture    string
	InstallShape    InstallShape
	InvocationLabel string
	// CanonicalEntrypointName, ArtifactRoot and SignedRelativePath mirror
	// Release. A compound install signs its native child rather than the
	// wrapper script that starts it, so the file to check is not always the
	// file that was invoked.
	CanonicalEntrypointName string
	ArtifactRoot            string
	SignedRelativePath      string
	// SigningIdentifier and TeamID are what a catalog declares. It cannot
	// declare a requirement expression: one is generated from these two
	// fields in a single fixed shape, so a mistaken entry can name the wrong
	// publisher but cannot express a wider claim than ADR-0016 permits.
	SigningIdentifier codesignature.SigningIdentifier
	TeamID            codesignature.TeamID
	LaunchRecipe      LaunchRecipe
}

// requirement builds the platform expression this entry stands for.
func (signer Signer) requirement() (codesignature.Requirement, error) {
	return codesignature.DeveloperIDRequirement(
		signer.SigningIdentifier,
		signer.TeamID,
	)
}

// SignerEvidence is what a recognized detection carries instead of Evidence.
//
// It names who signed the launched artifact and nothing about which build it
// is, because that is genuinely all that was established. A caller that wants
// a version has to look at a verified detection.
type SignerEvidence struct {
	ID              string          `json:"id"`
	Revision        AdapterRevision `json:"revision"`
	CatalogRevision CatalogRevision `json:"catalogRevision"`
	InstallShape    InstallShape    `json:"installShape"`
	LaunchRecipe    LaunchRecipe    `json:"launchRecipe"`
	// SignedPath is the artifact the platform actually evaluated, which for a
	// compound install is not the invoked entrypoint.
	SignedPath string `json:"signedPath"`
}

func (evidence SignerEvidence) Validate() error {
	if evidence.ID == "" ||
		!evidence.Revision.Valid() ||
		!evidence.CatalogRevision.Valid() ||
		!evidence.InstallShape.Valid() ||
		!evidence.LaunchRecipe.Valid() ||
		evidence.LaunchRecipe == LaunchGeneric ||
		evidence.SignedPath == "" ||
		!filepath.IsAbs(evidence.SignedPath) {
		return errors.New("client signer evidence is invalid")
	}
	return nil
}

func validateSigner(signer Signer) error {
	if signer.ID == "" ||
		!signer.Revision.Valid() ||
		signer.OperatingSystem == "" ||
		signer.Architecture == "" ||
		!signer.InstallShape.Valid() ||
		signer.InvocationLabel == "" ||
		!signer.LaunchRecipe.Valid() ||
		signer.LaunchRecipe == LaunchGeneric {
		return errors.New("client signer entry is invalid")
	}
	// A recipe that delivers no Root has nothing to gate, so a signer entry
	// declaring one is a mistake rather than a harmless no-op.
	if !signer.LaunchRecipe.RequiresRoot() {
		return errors.New("a signer entry must declare a Root-bearing recipe")
	}
	// Building it is the validation. A catalogued publisher is a Developer ID
	// publisher by construction now, rather than by a check on a string
	// somebody wrote.
	if _, err := signer.requirement(); err != nil {
		return fmt.Errorf("client signer identity is invalid: %w", err)
	}
	if signer.ArtifactRoot == "" {
		return errors.New("client signer artifact root is required")
	}
	if filepath.IsAbs(signer.ArtifactRoot) ||
		filepath.IsAbs(signer.SignedRelativePath) {
		return errors.New("client signer paths must be relative")
	}
	if strings.ContainsRune(signer.InvocationLabel, filepath.Separator) {
		return errors.New("client signer invocation label is not a path")
	}
	return nil
}

func cloneSigner(signer Signer) Signer {
	return signer
}

// signersForLabel returns the entries a program invoked by this name could be
// on this machine.
//
// Name alone decides nothing; it only bounds which requirements are worth
// evaluating. The platform fields do the same job and were declared but not
// consulted, so an entry describing a darwin/arm64 install was offered for a
// requirement check on any platform at all — harmless where signature
// verification is unavailable, and wrong in principle everywhere.
func (verifier *ReleaseVerifier) signersForLabel(label string) []Signer {
	var matches []Signer
	for _, signer := range verifier.signers {
		if signer.InvocationLabel != label ||
			signer.OperatingSystem != runtime.GOOS ||
			signer.Architecture != runtime.GOARCH {
			continue
		}
		matches = append(matches, signer)
	}
	return matches
}

// verifySigner resolves the artifact that carries the signature and asks the
// platform whether it satisfies the frozen requirement.
//
// Path handling matches verifyRelease: everything resolves inside the declared
// root, and a path that escapes it is an error rather than a miss, because a
// signer entry that reaches outside its own install is not a near miss.
func verifySigner(
	ctx context.Context,
	canonicalEntrypoint string,
	signer Signer,
) (string, bool, error) {
	if signer.CanonicalEntrypointName != "" &&
		filepath.Base(canonicalEntrypoint) != signer.CanonicalEntrypointName {
		return "", false, nil
	}
	entrypointDirectory := filepath.Dir(canonicalEntrypoint)
	lexicalRoot := filepath.Clean(filepath.Join(
		entrypointDirectory,
		signer.ArtifactRoot,
	))
	lexicalPath := canonicalEntrypoint
	if signer.SignedRelativePath != "" {
		lexicalPath = filepath.Clean(filepath.Join(
			entrypointDirectory,
			signer.SignedRelativePath,
		))
	}
	if !pathWithin(lexicalRoot, lexicalPath) {
		return "", false, errors.New(
			"client signer artifact escapes its lexical root",
		)
	}
	resolvedRoot, err := resolveExisting(lexicalRoot)
	if err != nil {
		return "", false, err
	}
	if resolvedRoot == "" {
		return "", false, nil
	}
	resolvedPath, err := resolveExisting(lexicalPath)
	if err != nil {
		return "", false, err
	}
	if resolvedPath == "" {
		return "", false, nil
	}
	if !pathWithin(resolvedRoot, resolvedPath) {
		return "", false, errors.New(
			"client signer artifact escapes its resolved root",
		)
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("inspect signed client artifact: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", false, nil
	}
	requirement, err := signer.requirement()
	if err != nil {
		return "", false, fmt.Errorf("client signer identity is invalid: %w", err)
	}
	switch err := codesignature.Verify(
		ctx, resolvedPath, requirement,
	); {
	case err == nil:
		return resolvedPath, true, nil
	case errors.Is(err, codesignature.ErrNotSatisfied),
		errors.Is(err, codesignature.ErrUnsupportedPlatform):
		// Both are ordinary answers. A platform with no such check has no
		// recognized tier at all, which is a degradation to generic and not a
		// malfunction.
		return "", false, nil
	default:
		return "", false, err
	}
}

// resolveExisting returns the absolute, symlink-resolved path, or an empty
// string when it does not exist. A missing path is a miss, not an error.
func resolveExisting(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("resolve client signer path: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("make client signer path absolute: %w", err)
	}
	return filepath.Clean(resolved), nil
}

// ClaudeCodeSignerDarwin is Anthropic's published identity for Claude Code.
// Observed identical across 2.1.218, 2.1.219 and 2.1.220, which is what makes
// it usable where a per-build digest is not.
func ClaudeCodeSignerDarwin() Signer {
	return Signer{
		ID:                "claude-code",
		Revision:          1,
		OperatingSystem:   "darwin",
		Architecture:      "arm64",
		InstallShape:      InstallNativeSingleBinary,
		InvocationLabel:   "claude",
		ArtifactRoot:      ".",
		SigningIdentifier: "com.anthropic.claude-code",
		TeamID:            "Q6L2SF6YDW",
		LaunchRecipe:      LaunchNodeEnvProxy,
	}
}

// CodexCLISignerDarwin is OpenAI's published identity for the Codex native
// child. The invoked entrypoint is an unsigned wrapper script, so the file the
// platform evaluates is the binary it starts.
func CodexCLISignerDarwin() Signer {
	return Signer{
		ID:                      "codex-cli",
		Revision:                1,
		OperatingSystem:         "darwin",
		Architecture:            "arm64",
		InstallShape:            InstallNPMWrapperNativeChild,
		InvocationLabel:         "codex",
		CanonicalEntrypointName: "codex.js",
		ArtifactRoot:            "..",
		SignedRelativePath: "../node_modules/@openai/codex-darwin-arm64/" +
			"vendor/aarch64-apple-darwin/bin/codex",
		SigningIdentifier: "codex",
		TeamID:            "2DC432GLL2",
		LaunchRecipe:      LaunchSSLCertFile,
	}
}
