package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/acceptancereport"
)

func TestBundleDigestIsDeterministicAndCoversMemberContent(t *testing.T) {
	t.Parallel()

	bundle := filepath.Join(t.TempDir(), "ViberMate.app")
	member := filepath.Join(bundle, "Contents", "MacOS", "vibermate")
	if err := os.MkdirAll(filepath.Dir(member), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(member, []byte("first"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := acceptancereport.DigestBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := acceptancereport.DigestBundle(bundle)
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
	changed, err := acceptancereport.DigestBundle(bundle)
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
		Go:      "go version go1.25.13 darwin/arm64",
		Flutter: normalizedFlutterVersion(),
		Dart:    "Dart " + expectedDartVersion,
		Xcode:   expectedXcodeVersion,
	}
	binaries := []goBinaryEvidence{
		{role: "acceptance", goVersion: expectedGoVersion},
		{role: "daemon", goVersion: expectedGoVersion},
		{role: "launcher", goVersion: expectedGoVersion},
	}
	if err := validateToolchains(tools, binaries); err != nil {
		t.Fatalf("pinned toolchains were rejected: %v", err)
	}
	tools.Flutter = "Flutter 99.0.0 (" + expectedFlutterRevision + ")"
	if err := validateToolchains(tools, binaries); err == nil {
		t.Fatal("unpinned Flutter toolchain was accepted")
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
		environmentID:     "environment-001",
		timeout:           9,
	}, client)
	if configuration.ClientID != string(acceptanceClientCodexCLI) ||
		configuration.ClientVersion != "0.145.0" ||
		!configuration.DeterministicOnly ||
		configuration.EnvironmentID != "environment-001" ||
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
			Toolkit:  "flutter",
		},
		Toolchains: desktopBuildToolchains{
			Go:      "go version go1.25.13 darwin/arm64",
			Flutter: normalizedFlutterVersion(),
			Dart:    "Dart " + expectedDartVersion,
			Xcode:   expectedXcodeVersion,
		},
		ConfigurationSHA256: map[string]string{
			"go.mod":                              hash,
			"go.sum":                              hash,
			"ui/flutter_app/.metadata":            hash,
			"ui/flutter_app/pubspec.yaml":         hash,
			"ui/flutter_app/pubspec.lock":         hash,
			"ui/flutter_app/tool/flutter-sdk.env": hash,
			"ui/flutter_app/macos/Runner.xcodeproj/project.pbxproj": hash,
			"ui/flutter_app/macos/Runner/Configs/AppInfo.xcconfig":  hash,
			"ui/flutter_app/macos/Runner/Configs/Release.xcconfig":  hash,
			"ui/flutter_app/macos/Runner/Info.plist":                hash,
			"ui/flutter_app/macos/Runner/Release.entitlements":      hash,
		},
		NestedCodeSHA256: map[string]string{
			"app-framework":           strings.Repeat("d", 64),
			"flutter-macos-framework": strings.Repeat("e", 64),
			"vibermated":              strings.Repeat("b", 64),
			"vibermate":               strings.Repeat("c", 64),
		},
	}
	artifacts := []artifactProvenance{
		{Role: "app-framework", SHA256: strings.Repeat("d", 64)},
		{Role: "daemon", SHA256: strings.Repeat("b", 64)},
		{Role: "flutter-macos-framework", SHA256: strings.Repeat("e", 64)},
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
	manifest.NestedCodeSHA256["vibermated"] = hash
	if err := validateDesktopBuildManifest(
		manifest,
		source,
		"development",
		artifacts,
	); err == nil {
		t.Fatal("manifest with a mismatched daemon digest was accepted")
	}
	manifest.NestedCodeSHA256["vibermated"] = strings.Repeat("b", 64)
	delete(manifest.ConfigurationSHA256, "ui/flutter_app/pubspec.lock")
	if err := validateDesktopBuildManifest(
		manifest,
		source,
		"development",
		artifacts,
	); err == nil {
		t.Fatal("v3 manifest without the Flutter lockfile digest was accepted")
	}
	manifest.Schema = "vibermate.desktop-build/v1"
	if err := validateDesktopBuildManifest(
		manifest,
		source,
		"development",
		artifacts,
	); err == nil {
		t.Fatal("current acceptance accepted a historical v1 manifest")
	}
	manifest.ConfigurationSHA256["ui/flutter_app/pubspec.lock"] = hash
	manifest.Schema = desktopBuildManifestV3
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
