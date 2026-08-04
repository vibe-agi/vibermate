package releasemanifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestManifestRoundTrip(t *testing.T) {
	manifest := validManifest()
	payload, err := Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	decoded, err := DecodeBytes(payload)
	if err != nil {
		t.Fatalf("DecodeBytes() error = %v", err)
	}
	if decoded.Commit != manifest.Commit || decoded.SBOM.Role != ArtifactRoleSBOM {
		t.Fatalf("DecodeBytes() = %#v", decoded)
	}
}

func TestDecodeRejectsOpenOrAmbiguousJSON(t *testing.T) {
	payload, err := json.Marshal(validManifest())
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"unknown nested field": bytes.Replace(
			payload,
			[]byte(`"evidenceStatus":"missing"`),
			[]byte(`"evidenceStatus":"missing","detail":"not-run"`),
			1,
		),
		"duplicate member": bytes.Replace(
			payload,
			[]byte(`"schema":"vibermate.release/v1"`),
			[]byte(`"schema":"vibermate.release/v1","schema":"vibermate.release/v1"`),
			1,
		),
		"case-insensitive top-level alias": bytes.Replace(
			payload,
			[]byte(`"schema":"vibermate.release/v1"`),
			[]byte(`"schema":"vibermate.release/v1","Schema":"vibermate.release/v1"`),
			1,
		),
		"case-insensitive nested alias": bytes.Replace(
			payload,
			[]byte(`"status":"unknown","evidenceStatus":"missing"`),
			[]byte(`"status":"unknown","Status":"unsupported","evidenceStatus":"missing"`),
			1,
		),
		"Unicode EqualFold nested alias": bytes.Replace(
			payload,
			[]byte(`"status":"unknown","evidenceStatus":"missing"`),
			[]byte(`"status":"unknown","\u017ftatus":"unsupported","evidenceStatus":"missing"`),
			1,
		),
		"trailing JSON": append(append([]byte(nil), payload...), []byte(` {}`)...),
		"invalid UTF-8": append(append([]byte(nil), payload...), 0xff),
		"JSON null":     []byte("null"),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeBytes(input); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("DecodeBytes() error = %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func TestDecodeRejectsDocumentOverLimit(t *testing.T) {
	input := bytes.Repeat([]byte(" "), MaxDocumentBytes+1)
	if _, err := Decode(bytes.NewReader(input)); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("Decode() error = %v, want ErrInvalidManifest", err)
	}
}

func TestManifestValidateRejectsInvalidContracts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"schema", func(value *Manifest) { value.Schema = "vibermate.release/v2" }},
		{"semver", func(value *Manifest) { value.Version = "01.2.3" }},
		{"channel", func(value *Manifest) { value.Channel = "beta" }},
		{"stable channel overclaim", func(value *Manifest) { value.Channel = ChannelStable }},
		{"commit", func(value *Manifest) { value.Commit = strings.Repeat("A", 40) }},
		{"protocol revision", func(value *Manifest) { value.ProtocolSchema = "" }},
		{"platform status", func(value *Manifest) { value.PlatformSupport[0].SupportLevel = "planned" }},
		{"reversed architecture set", func(value *Manifest) {
			duplicate := value.PlatformSupport[0]
			duplicate.Architectures = []string{"x86_64", "arm64"}
			value.PlatformSupport = append(value.PlatformSupport, duplicate)
		}},
		{"stable support overclaim", func(value *Manifest) { value.PlatformSupport[0].SupportLevel = SupportStable }},
		{"missing capability status", func(value *Manifest) { value.PlatformSupport[0].HostCapabilities[0].Status = "" }},
		{"missing evidence status", func(value *Manifest) { value.PlatformSupport[0].HostCapabilities[0].EvidenceStatus = "" }},
		{"supported without current evidence", func(value *Manifest) { value.PlatformSupport[0].HostCapabilities[0].Status = CapabilitySupported }},
		{"degraded without current evidence", func(value *Manifest) { value.PlatformSupport[0].HostCapabilities[0].Status = CapabilityDegraded }},
		{"supported with stale evidence", func(value *Manifest) {
			capability := &value.PlatformSupport[0].HostCapabilities[0]
			capability.Status = CapabilitySupported
			capability.EvidenceStatus = EvidenceStale
			capability.EvidenceArtifactRole = ArtifactRoleKnownIssues
		}},
		{"current evidence without artifact", func(value *Manifest) { value.PlatformSupport[0].HostCapabilities[0].EvidenceStatus = EvidenceCurrent }},
		{"duplicate artifact role", func(value *Manifest) { value.Artifacts[1].Role = value.Artifacts[0].Role }},
		{"duplicate artifact path", func(value *Manifest) { value.Artifacts[1].Path = value.Artifacts[0].Path }},
		{"artifact traversal", func(value *Manifest) { value.Artifacts[0].Path = "../app" }},
		{"artifact case alias", func(value *Manifest) { value.Artifacts[1].Path = "APP-TREE.JSON" }},
		{"artifact media type", func(value *Manifest) { value.Artifacts[0].MediaType = "Application/JSON" }},
		{"SPDX media type", func(value *Manifest) { value.Artifacts[2].MediaType = "application/json" }},
		{"required role", func(value *Manifest) { value.Artifacts[0].Role = "other-ledger" }},
		{"signed DMG in R0", func(value *Manifest) {
			value.Artifacts = append(value.Artifacts, Artifact{
				Path:      "vibermate.dmg",
				MediaType: "application/x-apple-diskimage",
				Role:      ArtifactRoleDMG,
				Size:      1,
				SHA256:    strings.Repeat("9", 64),
			})
		}},
		{"artifact digest", func(value *Manifest) { value.Artifacts[0].SHA256 = strings.Repeat("A", 64) }},
		{"unknown artifact reference", func(value *Manifest) { value.SBOM.Role = "missing" }},
		{"swapped artifact reference", func(value *Manifest) { value.KnownIssues.Role = value.SBOM.Role }},
		{"R2 overclaim", func(value *Manifest) { value.EvidenceScope.R2Reproducibility = "verified" }},
		{"migration range", func(value *Manifest) { value.Migration.MinimumSchema = "27"; value.Migration.MaximumSchema = "26" }},
		{"rollback compatibility", func(value *Manifest) { value.Migration.RollbackCompatibility = "" }},
		{"host omission", func(value *Manifest) { value.HostSupport = value.HostSupport[:1] }},
		{"host duplicate", func(value *Manifest) { value.HostSupport[1].Host = "desktop" }},
		{"timestamp", func(value *Manifest) { value.PublishedAt = "2026-08-03" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest()
			test.mutate(&manifest)
			if err := manifest.Validate(); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("Validate() error = %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func TestValidateArtifactPathUsesPortableASCII(t *testing.T) {
	for _, value := range []string{
		"release metadata.json",
		"dist/caf\u00e9.js",
		"dist/@scope.js",
	} {
		if err := ValidateArtifactPath(value); err == nil {
			t.Fatalf("ValidateArtifactPath(%q) succeeded, want error", value)
		}
	}
	for _, value := range []string{
		"LICENSE",
		"dist/assets/index-D5ubGS8e.js",
		"known-issues_1.json",
	} {
		if err := ValidateArtifactPath(value); err != nil {
			t.Fatalf("ValidateArtifactPath(%q) error = %v", value, err)
		}
	}
}

func TestManifestValidateAcceptsDigestBoundCurrentEvidence(t *testing.T) {
	manifest := validManifest()
	manifest.Artifacts = append(manifest.Artifacts, Artifact{
		Path:      "macos-conformance.json",
		MediaType: "application/json",
		Role:      "macos-conformance",
		Size:      1,
		SHA256:    strings.Repeat("6", 64),
	}, Artifact{
		Path:      "desktop-conformance.json",
		MediaType: "application/json",
		Role:      "desktop-conformance",
		Size:      1,
		SHA256:    strings.Repeat("7", 64),
	})
	manifest.PlatformSupport[0].HostCapabilities[0].EvidenceStatus = EvidenceCurrent
	manifest.PlatformSupport[0].HostCapabilities[0].EvidenceArtifactRole = "macos-conformance"
	manifest.PlatformSupport[0].HostCapabilities[0].Status = CapabilitySupported
	manifest.HostSupport[0].Capabilities[0].EvidenceStatus = EvidenceCurrent
	manifest.HostSupport[0].Capabilities[0].EvidenceArtifactRole = "desktop-conformance"
	manifest.HostSupport[0].Capabilities[0].Status = CapabilitySupported
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func validManifest() Manifest {
	return Manifest{
		Schema:         Schema,
		Version:        "1.2.3-preview.1+build.7",
		Channel:        ChannelPreview,
		Commit:         strings.Repeat("a", 40),
		ProtocolSchema: "2026-08-01",
		PluginSDK:      "1.0.0",
		PlatformSupport: []PlatformSupportEntry{
			{
				OS:                  "macos",
				Range:               ">=14.0",
				Architectures:       []string{"arm64", "x86_64"},
				InstallShape:        "dmg",
				SupportLevel:        SupportPreview,
				ConformanceRevision: "macos-pack-2026-08-03",
				HostCapabilities: []CapabilityStatus{
					{
						Capability:           "root-trust",
						Status:               CapabilityUnknown,
						EvidenceStatus:       EvidenceMissing,
						EvidenceArtifactRole: "none",
					},
				},
			},
		},
		Artifacts: []Artifact{
			{Path: "app-tree.json", MediaType: "application/json", Role: ArtifactRoleAppTreeLedger, Size: 1, SHA256: strings.Repeat("1", 64)},
			{Path: "desktop-build-manifest.json", MediaType: "application/json", Role: ArtifactRoleDesktopBuildManifest, Size: 1, SHA256: strings.Repeat("3", 64)},
			{Path: "sbom.spdx.json", MediaType: "application/spdx+json", Role: ArtifactRoleSBOM, Size: 1, SHA256: strings.Repeat("4", 64)},
			{Path: "known-issues.json", MediaType: "application/json", Role: ArtifactRoleKnownIssues, Size: 1, SHA256: strings.Repeat("5", 64)},
		},
		SBOM: ArtifactReference{Role: ArtifactRoleSBOM},
		EvidenceScope: EvidenceScope{
			Level:                  "r0",
			ArtifactState:          "unsigned-pre-sign",
			R2Reproducibility:      "not-asserted",
			R3SignedPackageBinding: "not-asserted",
			ReleaseApproval:        "not-asserted",
		},
		Migration: MigrationCompatibility{
			MinimumSchema:         "0",
			MaximumSchema:         "26",
			RollbackCompatibility: "backup-required",
		},
		HostSupport: []HostSupportEntry{
			{
				Host:                "desktop",
				SupportLevel:        SupportPreview,
				ConformanceRevision: "desktop-pack-2026-08-03",
				Capabilities: []CapabilityStatus{
					{
						Capability:           "local-control",
						Status:               CapabilityUnknown,
						EvidenceStatus:       EvidenceMissing,
						EvidenceArtifactRole: "none",
					},
				},
			},
			{
				Host:                "server",
				SupportLevel:        SupportUnsupported,
				ConformanceRevision: "server-not-shipped",
				Capabilities: []CapabilityStatus{
					{
						Capability:           "runtime",
						Status:               CapabilityUnsupported,
						EvidenceStatus:       EvidenceMissing,
						EvidenceArtifactRole: "none",
					},
				},
			},
		},
		KnownIssues: ArtifactReference{Role: ArtifactRoleKnownIssues},
		PublishedAt: "2026-08-03T12:34:56+08:00",
	}
}
