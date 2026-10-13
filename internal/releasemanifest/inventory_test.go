package releasemanifest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnsignedKernelInventoryRequiresHelperAndAdmitsOnlyNoticeLicenseLocations(t *testing.T) {
	makeLedger := func(extra string, omitKernel bool) ([]appTreeLedgerEntry, map[string]string) {
		types := map[string]string{".": "directory", "vibermate-desktop": "file", "vibermate": "file", "vibermated": "file", "vibermate-build-manifest.json": "file", "LICENSE": "file", "dist": "directory", "dist/index.html": "file"}
		if !omitKernel {
			types["vibermate-xray"] = "file"
			types["vless-kernel"] = "directory"
			types["vless-kernel/package-manifest.json"] = "file"
		}
		if extra != "" {
			types[extra] = "file"
		}
		var entries []appTreeLedgerEntry
		for path, kind := range types {
			mode := uint32(0o755)
			entries = append(entries, appTreeLedgerEntry{Path: path, Type: kind, Mode: &mode})
		}
		return entries, types
	}
	for _, path := range []string{"vless-kernel/licenses/go/LICENSE", "vless-kernel/licenses/modules/" + strings.Repeat("a", 64) + "/nested/LICENSE"} {
		entries, types := makeLedger(path, false)
		if err := validateUnsignedPayloadLedgerShape(entries, types); err != nil {
			t.Fatalf("valid kernel notice %q rejected: %v", path, err)
		}
	}
	entries, types := makeLedger("", true)
	if err := validateUnsignedPayloadLedgerShape(entries, types); err == nil {
		t.Fatal("missing required kernel accepted")
	}
	for _, path := range []string{"dist/LICENSE", "vless-kernel/LICENSE", "vless-kernel/licenses/modules/not-a-digest/LICENSE", "vless-kernel/licenses/go/vibermate-xray"} {
		entries, types := makeLedger(path, false)
		if err := validateUnsignedPayloadLedgerShape(entries, types); err == nil {
			t.Fatalf("illegal reserved alias %q accepted", path)
		}
	}
}

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
