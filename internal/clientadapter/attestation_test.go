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

// A recognized companion claim names its client only through the label it was
// evaluated for, so the Server must refuse a claim whose signer belongs to a
// different label, or to another catalog revision.
func TestCompanionRecognizedClaimIsAcceptedOnlyForItsLabel(t *testing.T) {
	t.Parallel()

	catalog := BuiltInCatalog()
	revision := catalog.Revision()
	claude := SignerEvidence{
		ID:              "claude-code",
		Revision:        1,
		CatalogRevision: revision,
		InstallShape:    InstallNativeSingleBinary,
		LaunchRecipe:    LaunchNodeEnvProxy,
		SignedPath:      filepath.Join(string(filepath.Separator), "opt", "claude"),
	}
	codexWrapper := SignerEvidence{
		ID:              "codex-cli",
		Revision:        1,
		CatalogRevision: revision,
		InstallShape:    InstallNPMWrapperNativeChild,
		LaunchRecipe:    LaunchCodexResponsesHTTP,
		SignedPath:      filepath.Join(string(filepath.Separator), "opt", "codex-native"),
	}
	codexNative := codexWrapper
	codexNative.InstallShape = InstallNativeSingleBinary
	recognized := func(label string, revision CatalogRevision, signer SignerEvidence) Detection {
		return Detection{
			Status:          StatusGeneric,
			Recognition:     RecognitionRecognized,
			CatalogRevision: revision,
			CanonicalPath:   filepath.Join(string(filepath.Separator), "opt", label),
			ExecutableLabel: label,
			Signer:          &signer,
		}
	}

	for _, testCase := range []struct {
		name      string
		detection Detection
	}{
		{"claude with claude-code evidence", recognized("claude", revision, claude)},
		{"codex with wrapper evidence", recognized("codex", revision, codexWrapper)},
		{"codex with native evidence", recognized("codex", revision, codexNative)},
	} {
		accepted, err := catalog.ValidateCompanionAttestation(testCase.detection)
		if err != nil || accepted.Signer == nil ||
			*accepted.Signer != *testCase.detection.Signer ||
			accepted.ExecutableLabel != testCase.detection.ExecutableLabel {
			t.Fatalf("%s: ValidateCompanionAttestation() = %+v, %v", testCase.name, accepted, err)
		}
	}

	staleSigner := claude
	staleSigner.CatalogRevision = revision - 1
	for _, testCase := range []struct {
		name      string
		detection Detection
	}{
		{"claude with codex-cli evidence", recognized("claude", revision, codexNative)},
		{"codex with claude-code evidence", recognized("codex", revision, claude)},
		{"claude-code as the label", recognized("claude-code", revision, claude)},
		{"a stale signer revision", recognized("claude", revision, staleSigner)},
		{"a stale detection revision", recognized("claude", revision-1, staleSigner)},
		{"a detection revision the signer does not share", recognized("claude", revision-1, claude)},
	} {
		if accepted, err := catalog.ValidateCompanionAttestation(testCase.detection); err == nil {
			t.Fatalf("%s was accepted: %+v", testCase.name, accepted)
		}
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
