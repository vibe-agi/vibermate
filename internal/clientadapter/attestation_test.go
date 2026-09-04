package clientadapter

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
)

func TestCatalogAcceptsOnlyAnExactCompanionAttestation(t *testing.T) {
	t.Parallel()

	release := fixedAttestationRelease()
	catalog, err := NewCatalog(7, []Release{release})
	if err != nil {
		t.Fatal(err)
	}
	evidence, ok := catalog.ExpectedEvidenceForPlatform(
		release.ID,
		release.Version,
		release.OperatingSystem,
		release.Architecture,
	)
	if !ok {
		t.Fatal("expected evidence is missing")
	}
	detection := Detection{
		Status:          StatusVerified,
		Recognition:     RecognitionVerified,
		CatalogRevision: catalog.Revision(),
		CanonicalPath:   filepath.Join(string(filepath.Separator), "opt", "codex"),
		ExecutableLabel: "codex",
		Evidence:        &evidence,
	}
	accepted, err := catalog.ValidateCompanionAttestation(detection)
	if err != nil || accepted.Evidence == nil ||
		accepted.Evidence.ReleaseSHA256 != evidence.ReleaseSHA256 {
		t.Fatalf("ValidateCompanionAttestation() = %+v, %v", accepted, err)
	}

	tampered := detection
	tamperedEvidence := evidence
	tamperedEvidence.LaunchRecipe = LaunchNodeEnvProxy
	tampered.Evidence = &tamperedEvidence
	if _, err := catalog.ValidateCompanionAttestation(tampered); err == nil {
		t.Fatal("catalog accepted a tampered launch recipe")
	}
}

func TestCatalogAcceptsCrossPlatformCompanionEvidence(t *testing.T) {
	t.Parallel()

	darwin := fixedAttestationRelease()
	darwin.OperatingSystem = "darwin"
	darwin.Architecture = "arm64"
	darwin.Artifacts[0].SHA256 = attestationDigest("darwin-client")
	linux := fixedAttestationRelease()
	linux.OperatingSystem = "linux"
	linux.Architecture = "amd64"
	linux.Artifacts[0].SHA256 = attestationDigest("linux-client")
	catalog, err := NewCatalog(8, []Release{darwin, linux})
	if err != nil {
		t.Fatal(err)
	}
	evidence, ok := catalog.ExpectedEvidenceForPlatform(
		linux.ID,
		linux.Version,
		linux.OperatingSystem,
		linux.Architecture,
	)
	if !ok {
		t.Fatal("Linux evidence is missing")
	}
	detection := Detection{
		Status:          StatusVerified,
		Recognition:     RecognitionVerified,
		CatalogRevision: catalog.Revision(),
		CanonicalPath:   filepath.Join(string(filepath.Separator), "opt", "codex"),
		ExecutableLabel: "codex",
		Evidence:        &evidence,
	}
	accepted, err := catalog.ValidateCompanionAttestation(detection)
	if err != nil || accepted.Evidence == nil ||
		accepted.Evidence.ReleaseSHA256 != evidence.ReleaseSHA256 {
		t.Fatalf("ValidateCompanionAttestation() = %+v, %v", accepted, err)
	}
}

func attestationDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func TestCatalogAcceptsGenericCompanionEvidenceWithoutUpgradingIt(t *testing.T) {
	t.Parallel()

	catalog, err := NewCatalog(7, []Release{fixedAttestationRelease()})
	if err != nil {
		t.Fatal(err)
	}
	detection := Detection{
		Status:          StatusGeneric,
		Recognition:     RecognitionUnknown,
		CatalogRevision: catalog.Revision(),
		CanonicalPath:   filepath.Join(string(filepath.Separator), "opt", "other"),
		ExecutableLabel: "other",
	}
	accepted, err := catalog.ValidateCompanionAttestation(detection)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != StatusGeneric || accepted.Evidence != nil ||
		accepted.Signer != nil {
		t.Fatalf("accepted detection = %+v", accepted)
	}
}

func fixedAttestationRelease() Release {
	return Release{
		ID:                      "codex-cli",
		Revision:                3,
		Version:                 "1.2.3",
		OperatingSystem:         "darwin",
		Architecture:            "arm64",
		InstallShape:            InstallNativeSingleBinary,
		InvocationLabel:         "codex",
		CanonicalEntrypointName: "codex",
		ArtifactRoot:            ".",
		Artifacts: []Artifact{{
			Role:   ArtifactEntrypoint,
			SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
		LaunchRecipe: LaunchCodexResponsesHTTP,
	}
}
