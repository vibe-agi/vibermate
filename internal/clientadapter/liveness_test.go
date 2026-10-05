package clientadapter

import (
	"path/filepath"
	"testing"
)

func TestResponsesDataKeepaliveRequiresExactCatalogEvidence(t *testing.T) {
	catalog := BuiltInCatalog()
	for _, tuple := range []struct {
		version, os, arch string
		want              bool
	}{
		{"0.160.0", "darwin", "arm64", true}, {"0.160.0", "darwin", "amd64", false}, {"0.160.0", "linux", "arm64", false}, {"0.159.2", "darwin", "arm64", false},
		{"0.145.0", "darwin", "arm64", false}, {"0.145.0", "darwin", "amd64", false}, {"0.145.0", "linux", "amd64", false}, {"0.145.0", "linux", "arm64", false}, {"0.160.1", "darwin", "arm64", false},
	} {
		evidence, found := catalog.ExpectedEvidenceForPlatform("codex-cli", tuple.version, tuple.os, tuple.arch)
		if (found && evidence.Supports(FeatureResponsesDataKeepalive)) != tuple.want {
			t.Fatalf("unproved tuple acquired liveness: %+v", tuple)
		}
	}
	evidence, ok := catalog.ExpectedEvidenceForPlatform("codex-cli", "0.160.0", "darwin", "arm64")
	if !ok {
		t.Fatal("proved tuple missing")
	}
	for _, test := range []struct {
		name   string
		change func(*Evidence)
	}{
		{"wrong_digest", func(e *Evidence) {
			e.ReleaseSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{"old_catalog", func(e *Evidence) { e.CatalogRevision = 3 }},
		{"old_release", func(e *Evidence) { e.Revision = 1 }},
		{"unknown_version", func(e *Evidence) { e.Version = "0.160.1" }},
		{"foreign_client", func(e *Evidence) { e.ID = "claude-code" }},
		{"added_authority", func(e *Evidence) { e.Features |= FeatureStructuredWorkspaceTools }},
		{"missing_bit", func(e *Evidence) { e.Features &^= FeatureResponsesDataKeepalive }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := evidence
			test.change(&changed)
			_, err := catalog.ValidateCompanionAttestation(Detection{Status: StatusVerified, Recognition: RecognitionVerified, CatalogRevision: catalog.Revision(), CanonicalPath: filepath.Join(t.TempDir(), "codex"), ExecutableLabel: "codex", Evidence: &changed})
			if err == nil {
				t.Fatal("stale/altered evidence became current compatibility")
			}
		})
	}
	release := CodexCLI01600DarwinARM64()
	current := releaseEvidenceDigest(release)
	release.Features &^= FeatureResponsesDataKeepalive
	if releaseEvidenceDigest(release) == current {
		t.Fatal("release digest failed to bind liveness compatibility")
	}
	release.Features |= FeatureResponsesDataKeepalive
	release.Artifacts[0].SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if releaseEvidenceDigest(release) == current {
		t.Fatal("artifact replacement retained current evidence")
	}
}
