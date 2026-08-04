package releasemanifest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyArtifactsRejectsExtraTopLevelEntry(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	if err := os.WriteFile(
		filepath.Join(root, "undeclared-metadata.json"),
		[]byte("undeclared\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsSupportsNestedPathsAndRejectsSibling(t *testing.T) {
	root := artifactTempDir(t)
	manifest := validManifest()
	artifactForRole(t, &manifest, ArtifactRoleKnownIssues).Path =
		"metadata/release/known-issues.json"
	artifactForRole(t, &manifest, ArtifactRoleSBOM).Path =
		"metadata/sbom.spdx.json"
	writeSemanticArtifacts(t, root, &manifest)

	if err := VerifyArtifacts(root, manifest); err != nil {
		t.Fatalf("VerifyArtifacts() with nested paths error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "metadata", "release", "undeclared.json"),
		[]byte("undeclared\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsWithSpecAllowsOnlyExplicitSpec(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	const specPath = "release-spec.json"
	if err := os.WriteFile(
		filepath.Join(root, specPath),
		[]byte("validated input spec\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	if err := VerifyArtifactsWithSpec(root, manifest, specPath); err != nil {
		t.Fatalf("VerifyArtifactsWithSpec() error = %v", err)
	}
	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "another-input.json"),
		[]byte("not explicitly allowed\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifactsWithSpec(root, manifest, specPath); !errors.Is(
		err,
		ErrArtifactMismatch,
	) {
		t.Fatalf(
			"VerifyArtifactsWithSpec() error = %v, want ErrArtifactMismatch",
			err,
		)
	}
}
