package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundleDigestIsDeterministicAndCoversMemberContent(t *testing.T) {
	t.Parallel()

	bundle := filepath.Join(t.TempDir(), "VibeMate.app")
	member := filepath.Join(bundle, "Contents", "MacOS", "vibermate")
	if err := os.MkdirAll(filepath.Dir(member), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(member, []byte("first"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := digestBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := digestBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 == "" ||
		first.SHA256 != repeated.SHA256 ||
		first.Bytes != repeated.Bytes {
		t.Fatalf("bundle digests first=%+v repeated=%+v", first, repeated)
	}
	if err := os.WriteFile(member, []byte("second"), 0o700); err != nil {
		t.Fatal(err)
	}
	changed, err := digestBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if changed.SHA256 == first.SHA256 {
		t.Fatalf("bundle digest ignored member content: %+v", changed)
	}
}

func TestSourceEvidenceRequiresOneIdentityAndCleanFreeze(t *testing.T) {
	t.Parallel()

	evidence := []goBinaryEvidence{
		{
			role:       "acceptance",
			vcs:        "git",
			revision:   "0123456789012345678901234567890123456789",
			commitTime: "2026-07-30T00:00:00Z",
		},
		{
			role:       "daemon",
			vcs:        "git",
			revision:   "0123456789012345678901234567890123456789",
			commitTime: "2026-07-30T00:00:00Z",
		},
		{
			role:       "launcher",
			vcs:        "git",
			revision:   "0123456789012345678901234567890123456789",
			commitTime: "2026-07-30T00:00:00Z",
		},
	}
	source, err := commonSourceEvidence(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFrozenProvenance(
		acceptanceProvenance{Source: source},
	); err != nil {
		t.Fatalf("clean source was rejected: %v", err)
	}

	evidence[1].revision = "1123456789012345678901234567890123456789"
	if _, err := commonSourceEvidence(evidence); err == nil {
		t.Fatal("mismatched artifact source identities were accepted")
	}
	source.Dirty = true
	if err := validateFrozenProvenance(
		acceptanceProvenance{Source: source},
	); err == nil {
		t.Fatal("dirty source was accepted as frozen provenance")
	}
}

func TestSidecarProfileRequiresConsistentNativeTag(t *testing.T) {
	t.Parallel()

	development, err := sidecarProfile([]goBinaryEvidence{
		{role: "daemon"},
		{role: "launcher"},
	})
	if err != nil || development != "development" {
		t.Fatalf("development profile=%q error=%v", development, err)
	}
	release, err := sidecarProfile([]goBinaryEvidence{
		{role: "daemon", tags: "vibermate_native_secrets"},
		{role: "launcher", tags: "vibermate_native_secrets"},
	})
	if err != nil || release != "release" {
		t.Fatalf("release profile=%q error=%v", release, err)
	}
	if _, err := sidecarProfile([]goBinaryEvidence{
		{role: "daemon", tags: "vibermate_native_secrets"},
		{role: "launcher"},
	}); err == nil {
		t.Fatal("inconsistent sidecar tags were accepted")
	}
}

func TestToolchainValidationRequiresPinnedBuildAndHostVersions(t *testing.T) {
	t.Parallel()

	tools := toolchainProvenance{
		Go:    "go version go1.25.12 darwin/arm64",
		Node:  "v22.23.1",
		Rustc: "rustc 1.88.0 (fixture)\nhost: aarch64-apple-darwin",
		Cargo: "cargo 1.88.0 (fixture)",
		PNPM:  "10.33.2",
	}
	binaries := []goBinaryEvidence{
		{role: "acceptance", goVersion: expectedGoVersion},
		{role: "daemon", goVersion: expectedGoVersion},
		{role: "launcher", goVersion: expectedGoVersion},
	}
	if err := validateToolchains(tools, binaries); err != nil {
		t.Fatalf("pinned toolchains were rejected: %v", err)
	}
	tools.Node = "v25.8.1"
	if err := validateToolchains(tools, binaries); err == nil {
		t.Fatal("unpinned Node toolchain was accepted")
	}
}

func TestAcceptanceConfigurationBindsTheSelectedFixedClient(t *testing.T) {
	t.Parallel()

	client := acceptanceClient{
		ID:      acceptanceClientCodexCLI,
		Version: "0.145.0",
	}
	configuration := newAcceptanceConfiguration(config{
		deterministicOnly: true,
		accessID:          "Acc-001",
		providerOrigin:    "http://127.0.0.1:23333/v1",
		providerModel:     "dashscope:glm-5",
		timeout:           9,
	}, client)
	if configuration.ClientID != string(acceptanceClientCodexCLI) ||
		configuration.ClientVersion != "0.145.0" ||
		!configuration.DeterministicOnly ||
		configuration.AccessID != "Acc-001" ||
		configuration.ProviderOrigin != "http://127.0.0.1:23333/v1" ||
		configuration.ProviderModel != "dashscope:glm-5" ||
		configuration.Timeout != "9ns" {
		t.Fatalf("acceptance configuration = %+v", configuration)
	}
}

func TestDesktopBuildManifestBindsSourceSidecarsAndConfiguration(
	t *testing.T,
) {
	t.Parallel()

	hash := strings.Repeat("a", 64)
	source := sourceProvenance{
		VCS:        "git",
		Revision:   "0123456789012345678901234567890123456789",
		CommitTime: "2026-07-30T00:00:00Z",
	}
	manifest := desktopBuildManifest{
		Schema: desktopBuildManifestSchema,
		Source: source,
		Profiles: desktopBuildProfiles{
			Desktop:  "release",
			Sidecars: "development",
			Target:   "aarch64-apple-darwin",
		},
		Toolchains: desktopBuildToolchains{
			Go:    "go version go1.25.12 darwin/arm64",
			Node:  expectedNodeVersion,
			Rustc: "rustc 1.88.0 (fixture)\nhost: aarch64-apple-darwin",
			Cargo: "cargo 1.88.0 (fixture)",
			PNPM:  expectedPNPMVersion,
			Tauri: expectedTauriVersion,
		},
		ConfigurationSHA256: map[string]string{
			"go.mod":                               hash,
			"go.sum":                               hash,
			"ui/desktop/package.json":              hash,
			"ui/desktop/pnpm-lock.yaml":            hash,
			"ui/desktop/src-tauri/Cargo.toml":      hash,
			"ui/desktop/src-tauri/Cargo.lock":      hash,
			"ui/desktop/src-tauri/tauri.conf.json": hash,
		},
		SidecarSHA256: map[string]string{
			"vibermated": strings.Repeat("b", 64),
			"vibermate":  strings.Repeat("c", 64),
		},
	}
	artifacts := []artifactProvenance{
		{Role: "daemon", SHA256: strings.Repeat("b", 64)},
		{Role: "launcher", SHA256: strings.Repeat("c", 64)},
	}
	if err := validateDesktopBuildManifest(
		manifest,
		source,
		"development",
		artifacts,
	); err != nil {
		t.Fatalf("valid manifest was rejected: %v", err)
	}
	manifest.SidecarSHA256["vibermated"] = hash
	if err := validateDesktopBuildManifest(
		manifest,
		source,
		"development",
		artifacts,
	); err == nil {
		t.Fatal("manifest with a mismatched daemon digest was accepted")
	}
	manifest.SidecarSHA256["vibermated"] = strings.Repeat("b", 64)
	manifest.Source.Dirty = true
	if err := validateDesktopBuildManifest(
		manifest,
		source,
		"development",
		artifacts,
	); err == nil {
		t.Fatal("dirty Desktop manifest was accepted")
	}
}

func TestDesktopBuildManifestRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), desktopBuildManifestName)
	if err := os.WriteFile(
		path,
		[]byte(`{"schema":"vibermate.desktop-build/v1","unknown":true}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := readDesktopBuildManifest(path); err == nil {
		t.Fatal("unknown Desktop build manifest field was accepted")
	}
}
