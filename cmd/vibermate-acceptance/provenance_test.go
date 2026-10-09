package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/acceptancereport"
)

// Captured from the pinned SDK in a detached 3.41.5 tag checkout. The
// informational flutterRoot is omitted because it names the temporary checkout.
const detachedFlutterMachine = `{
  "frameworkVersion": "3.41.5",
  "channel": "[user-branch]",
  "repositoryUrl": "unknown source",
  "frameworkRevision": "2c9eb20739dfec95e2c74bd3dfa4601b0a8a36aa",
  "frameworkCommitDate": "2026-03-17 16:14:01 -0700",
  "engineRevision": "052f31d115eceda8cbff1b3481fcde4330c4ae12",
  "engineCommitDate": "2026-03-17 20:29:11.000Z",
  "engineContentHash": "c1db59d880ca73dd86cec08a6663f287522d9f39",
  "engineBuildDate": "2026-03-18 05:45:46.041",
  "dartSdkVersion": "3.11.3",
  "devToolsVersion": "2.54.2",
  "flutterVersion": "3.41.5"
}`

func TestFlutterToolchainEvidenceAcceptsPinnedDetachedTag(t *testing.T) {
	t.Parallel()

	for _, channel := range []string{"[user-branch]", "stable"} {
		t.Run(channel, func(t *testing.T) {
			source := strings.Replace(detachedFlutterMachine, "[user-branch]", channel, 1)
			tools, err := parseFlutterToolchains(source)
			if err != nil {
				t.Fatalf("pinned Flutter checkout was rejected: %v", err)
			}
			if tools.Flutter != "Flutter 3.41.5 (2c9eb20739dfec95e2c74bd3dfa4601b0a8a36aa)" ||
				tools.Dart != "Dart 3.11.3" {
				t.Fatalf("observed Flutter identity changed: %+v", tools)
			}
			tools.Go = "go version go1.26.9 darwin/arm64"
			tools.Xcode = "Xcode 16.2\nBuild version 16C5032a"
			if err := validateToolchains(tools, nil); err != nil {
				t.Fatalf("pinned toolchain identity was rejected: %v", err)
			}
		})
	}
}

func TestFlutterToolchainEvidenceRejectsInvalidMachineData(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"malformed":        "{",
		"trailing object":  detachedFlutterMachine + " {}",
		"trailing garbage": detachedFlutterMachine + " invalid",
		"null":             "null",
		"array":            "[]",
	}
	for _, field := range []string{"frameworkVersion", "frameworkRevision", "dartSdkVersion", "channel"} {
		for name, invalid := range map[string]any{"missing": nil, "empty": "", "wrong type": 42} {
			// Channel is display metadata; its absence or empty value is not
			// missing SDK identity, but an incompatible JSON type is malformed.
			if field == "channel" && invalid != 42 {
				continue
			}
			var machine map[string]any
			if err := json.Unmarshal([]byte(detachedFlutterMachine), &machine); err != nil {
				t.Fatal(err)
			}
			machine[field] = invalid
			if invalid == nil {
				delete(machine, field)
			}
			source, err := json.Marshal(machine)
			if err != nil {
				t.Fatal(err)
			}
			cases[field+" "+name] = string(source)
		}
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseFlutterToolchains(source); err == nil {
				t.Fatal("invalid Flutter machine evidence was accepted")
			}
		})
	}
}

func TestFlutterToolchainEvidencePreservesUnpinnedIdentityForRejection(t *testing.T) {
	t.Parallel()

	for _, changed := range []struct{ from, to string }{
		{"3.41.5", "3.41.6"},
		{"2c9eb20739dfec95e2c74bd3dfa4601b0a8a36aa", "ac9eb20739dfec95e2c74bd3dfa4601b0a8a36aa"},
		{"3.11.3", "3.11.4"},
	} {
		t.Run(changed.to, func(t *testing.T) {
			tools, err := parseFlutterToolchains(strings.ReplaceAll(detachedFlutterMachine, changed.from, changed.to))
			if err != nil {
				t.Fatalf("well-formed observed identity was not collected: %v", err)
			}
			if !strings.Contains(tools.Flutter+tools.Dart, changed.to) {
				t.Fatalf("observed identity was replaced: %+v", tools)
			}
			tools.Go = "go version go1.26.9 darwin/arm64"
			tools.Xcode = "Xcode 16.2\nBuild version 16C5032a"
			if err := validateToolchains(tools, nil); err == nil {
				t.Fatal("unpinned observed Flutter/Dart identity was accepted")
			}
		})
	}
}

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
		Go:      "go version go1.26.9 darwin/arm64",
		Flutter: normalizedFlutterVersion(),
		Dart:    "Dart " + expectedDartVersion,
		Xcode:   expectedXcodeVersion,
	}
	binaries := []goBinaryEvidence{
		{role: "acceptance", goVersion: "go1.26.9"},
		{role: "daemon", goVersion: "go1.26.9"},
		{role: "launcher", goVersion: "go1.26.9"},
	}
	if err := validateToolchains(tools, binaries); err != nil {
		t.Fatalf("pinned toolchains were rejected: %v", err)
	}
	for _, version := range []string{"go1.25.13", "go1.26.0", "go1.26.8", "go1.26.10"} {
		version := version
		t.Run("host "+version, func(t *testing.T) {
			candidate := tools
			candidate.Go = "go version " + version + " darwin/arm64"
			if err := validateToolchains(candidate, binaries); err == nil {
				t.Fatalf("host toolchain %q was accepted", version)
			}
		})
		for index, binary := range binaries {
			index, binary := index, binary
			t.Run(binary.role+" "+version, func(t *testing.T) {
				candidate := append([]goBinaryEvidence(nil), binaries...)
				candidate[index].goVersion = version
				if err := validateToolchains(tools, candidate); err == nil {
					t.Fatalf("%s Go version %q was accepted", binary.role, version)
				}
			})
		}
	}
	tools.Flutter = "Flutter 99.0.0 (" + expectedFlutterRevision + ")"
	if err := validateToolchains(tools, binaries); err == nil {
		t.Fatal("unpinned Flutter toolchain was accepted")
	}
	tools.Flutter = normalizedFlutterVersion()
	tools.Xcode = "Xcode 16.3\nBuild version 16E140"
	if err := validateToolchains(tools, binaries); err == nil {
		t.Fatal("unpinned Xcode toolchain was accepted")
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
			Go:      "go version go1.26.9 darwin/arm64",
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
			"file-selector-framework": strings.Repeat("8", 64),
			"flutter-macos-framework": strings.Repeat("e", 64),
			"url-launcher-framework":  strings.Repeat("9", 64),
			"vibermated":              strings.Repeat("b", 64),
			"vibermate":               strings.Repeat("c", 64),
		},
	}
	artifacts := []artifactProvenance{
		{Role: "app-framework", SHA256: strings.Repeat("d", 64)},
		{Role: "file-selector-framework", SHA256: strings.Repeat("8", 64)},
		{Role: "url-launcher-framework", SHA256: strings.Repeat("9", 64)},
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
