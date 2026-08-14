package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/acceptancereport"
)

const (
	expectedGoVersion       = acceptancereport.ExpectedGoVersion
	expectedFlutterVersion  = acceptancereport.ExpectedFlutterVersion
	expectedFlutterRevision = acceptancereport.ExpectedFlutterRevision
	expectedDartVersion     = acceptancereport.ExpectedDartVersion
	expectedXcodeVersion    = acceptancereport.ExpectedXcodeVersion

	desktopBuildManifestSchema = acceptancereport.DesktopBuildManifestSchema
	desktopBuildManifestV3     = acceptancereport.DesktopBuildManifestSchemaV3
	desktopBuildManifestName   = "vibermate-build-manifest.json"
	maxBuildManifestBytes      = 128 << 10
)

type sourceProvenance = acceptancereport.SourceProvenance
type artifactProvenance = acceptancereport.ArtifactProvenance
type toolchainProvenance = acceptancereport.ToolchainProvenance
type desktopBuildToolchains = acceptancereport.DesktopBuildToolchains

type desktopBuildProfiles struct {
	Desktop  string `json:"desktop"`
	Sidecars string `json:"sidecars"`
	Target   string `json:"target"`
	Toolkit  string `json:"toolkit"`
}

type desktopBuildManifest struct {
	Schema              string                 `json:"schema"`
	Source              sourceProvenance       `json:"source"`
	Profiles            desktopBuildProfiles   `json:"profiles"`
	Toolchains          desktopBuildToolchains `json:"toolchains"`
	ConfigurationSHA256 map[string]string      `json:"configurationSHA256"`
	NestedCodeSHA256    map[string]string      `json:"nestedCodeSHA256"`
}

type buildProvenance = acceptancereport.BuildProvenance
type acceptanceConfiguration = acceptancereport.Configuration
type acceptanceProvenance = acceptancereport.Provenance

type goBinaryEvidence struct {
	role       string
	goVersion  string
	vcs        string
	revision   string
	commitTime string
	dirty      bool
	tags       string
}

func collectAcceptanceProvenance(
	ctx context.Context,
	config config,
) (acceptanceProvenance, error) {
	client, err := selectedAcceptanceClient(config)
	if err != nil {
		return acceptanceProvenance{}, err
	}
	provenance := acceptanceProvenance{
		Build: buildProvenance{
			GoBuildVersions: make(map[string]string),
			GoBuildTags:     make(map[string]string),
		},
		Configuration: newAcceptanceConfiguration(config, client),
	}
	if ctx == nil {
		return provenance, errors.New("provenance context is nil")
	}
	acceptancePath, err := os.Executable()
	if err != nil {
		return provenance, fmt.Errorf("locate acceptance executable: %w", err)
	}
	acceptancePath, err = executablePath(acceptancePath)
	if err != nil {
		return provenance, fmt.Errorf("acceptance executable: %w", err)
	}
	desktopPath, err := appBundleExecutable(
		config.desktopAppPath,
		"vibermate-desktop",
	)
	if err != nil {
		return provenance, err
	}
	clientPath, err := executablePath(client.ExecutablePath)
	if err != nil {
		return provenance, err
	}
	manifestPath := filepath.Join(
		config.desktopAppPath,
		"Contents",
		"Resources",
		desktopBuildManifestName,
	)
	manifest, err := readDesktopBuildManifest(manifestPath)
	if err != nil {
		return provenance, fmt.Errorf("read Desktop build manifest: %w", err)
	}

	bundle, err := acceptancereport.DigestBundle(config.desktopAppPath)
	if err != nil {
		return provenance, fmt.Errorf("digest Desktop App bundle: %w", err)
	}
	bundle.Role = "desktop-app-bundle"
	artifacts := []artifactProvenance{bundle}
	for _, artifact := range []struct {
		role string
		path string
	}{
		{role: "desktop-app-executable", path: desktopPath},
		{
			role: "app-framework",
			path: filepath.Join(
				config.desktopAppPath,
				"Contents", "Frameworks", "App.framework", "Versions", "A", "App",
			),
		},
		{
			role: "flutter-macos-framework",
			path: filepath.Join(
				config.desktopAppPath,
				"Contents", "Frameworks", "FlutterMacOS.framework", "Versions", "A", "FlutterMacOS",
			),
		},
		{role: "daemon", path: config.daemonPath},
		{role: "launcher", path: config.launcherPath},
		{role: "acceptance", path: acceptancePath},
		{role: "client-entrypoint", path: clientPath},
		{role: "desktop-build-manifest", path: manifestPath},
	} {
		evidence, digestErr := acceptancereport.DigestArtifact(
			artifact.role,
			artifact.path,
		)
		if digestErr != nil {
			return provenance, digestErr
		}
		artifacts = append(artifacts, evidence)
	}
	sort.Slice(artifacts, func(left, right int) bool {
		return artifacts[left].Role < artifacts[right].Role
	})
	provenance.Artifacts = artifacts

	goBinaries := make([]goBinaryEvidence, 0, 3)
	for _, binary := range []struct {
		role string
		path string
	}{
		{role: "acceptance", path: acceptancePath},
		{role: "daemon", path: config.daemonPath},
		{role: "launcher", path: config.launcherPath},
	} {
		evidence, readErr := readGoBinaryEvidence(binary.role, binary.path)
		if readErr != nil {
			return provenance, readErr
		}
		goBinaries = append(goBinaries, evidence)
		provenance.Build.GoBuildVersions[evidence.role] = evidence.goVersion
		provenance.Build.GoBuildTags[evidence.role] = evidence.tags
	}
	source, err := commonSourceEvidence(goBinaries)
	if err != nil {
		return provenance, err
	}
	provenance.Source = source
	profile, err := sidecarProfile(goBinaries)
	if err != nil {
		return provenance, err
	}
	if err := validateDesktopBuildManifest(
		manifest,
		source,
		profile,
		artifacts,
	); err != nil {
		return provenance, err
	}
	provenance.Build.ManifestSchema = manifest.Schema
	provenance.Build.DesktopProfile = manifest.Profiles.Desktop
	provenance.Build.SidecarProfile = profile
	provenance.Build.Target = manifest.Profiles.Target
	provenance.Build.Toolkit = manifest.Profiles.Toolkit
	provenance.Build.Toolchains = manifest.Toolchains
	provenance.Build.ConfigurationSHA256 = mapsClone(
		manifest.ConfigurationSHA256,
	)

	toolchains, err := collectToolchains(ctx)
	if err != nil {
		return provenance, err
	}
	if err := validateToolchains(toolchains, goBinaries); err != nil {
		return provenance, err
	}
	provenance.Toolchains = toolchains
	return provenance, nil
}

func newAcceptanceConfiguration(
	config config,
	client acceptanceClient,
) acceptanceConfiguration {
	return acceptanceConfiguration{
		DeterministicOnly: config.deterministicOnly,
		ClientID:          string(client.ID),
		ClientVersion:     client.Version,
		EnvironmentID:     config.environmentID,
		Timeout:           config.timeout.String(),
	}
}

func readDesktopBuildManifest(path string) (desktopBuildManifest, error) {
	var manifest desktopBuildManifest
	info, err := os.Lstat(path)
	if err != nil {
		return manifest, err
	}
	if !info.Mode().IsRegular() ||
		info.Size() <= 0 ||
		info.Size() > maxBuildManifestBytes {
		return manifest, errors.New(
			"Desktop build manifest is not a bounded regular file",
		)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return manifest, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return desktopBuildManifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return desktopBuildManifest{}, errors.New(
			"Desktop build manifest has trailing data",
		)
	}
	return manifest, nil
}

func validateDesktopBuildManifest(
	manifest desktopBuildManifest,
	source sourceProvenance,
	sidecarProfile string,
	artifacts []artifactProvenance,
) error {
	if manifest.Schema != desktopBuildManifestV3 {
		return errors.New("current acceptance requires Flutter Desktop build manifest v3")
	}
	if manifest.Source != source || manifest.Source.Dirty {
		return errors.New(
			"Desktop App and Go artifacts do not share one clean Git source identity",
		)
	}
	if manifest.Profiles.Desktop != "release" ||
		manifest.Profiles.Sidecars != sidecarProfile ||
		manifest.Profiles.Target != "aarch64-apple-darwin" ||
		manifest.Profiles.Toolkit != "flutter" {
		return errors.New("Desktop build profiles are inconsistent")
	}
	if err := validateDesktopBuildToolchains(manifest.Toolchains); err != nil {
		return err
	}
	requiredConfiguration := []string{
		"go.mod",
		"go.sum",
		"ui/flutter_app/.metadata",
		"ui/flutter_app/pubspec.yaml",
		"ui/flutter_app/pubspec.lock",
		"ui/flutter_app/tool/flutter-sdk.env",
		"ui/flutter_app/macos/Runner.xcodeproj/project.pbxproj",
		"ui/flutter_app/macos/Runner/Configs/AppInfo.xcconfig",
		"ui/flutter_app/macos/Runner/Configs/Release.xcconfig",
		"ui/flutter_app/macos/Runner/Info.plist",
		"ui/flutter_app/macos/Runner/Release.entitlements",
	}
	if len(manifest.ConfigurationSHA256) != len(requiredConfiguration) {
		return errors.New("Desktop build configuration evidence is incomplete")
	}
	for _, name := range requiredConfiguration {
		if !validSHA256(manifest.ConfigurationSHA256[name]) {
			return fmt.Errorf(
				"Desktop build configuration digest is invalid: %s",
				name,
			)
		}
	}
	artifactDigests := make(map[string]string, len(artifacts))
	for _, artifact := range artifacts {
		artifactDigests[artifact.Role] = artifact.SHA256
	}
	wantedNestedCode := map[string]string{
		"app-framework":           artifactDigests["app-framework"],
		"flutter-macos-framework": artifactDigests["flutter-macos-framework"],
		"vibermate":               artifactDigests["launcher"],
		"vibermated":              artifactDigests["daemon"],
	}
	if len(manifest.NestedCodeSHA256) != len(wantedNestedCode) {
		return errors.New("Desktop build manifest nested-code evidence is incomplete")
	}
	for name, digest := range wantedNestedCode {
		if manifest.NestedCodeSHA256[name] != digest {
			return errors.New(
				"Desktop build manifest does not identify the packaged nested code",
			)
		}
	}
	return nil
}

func validateDesktopBuildToolchains(
	tools desktopBuildToolchains,
) error {
	goFields := strings.Fields(tools.Go)
	if len(goFields) < 3 || goFields[2] != expectedGoVersion {
		return fmt.Errorf(
			"Desktop build Go toolchain is not %s",
			expectedGoVersion,
		)
	}
	if tools.Flutter != normalizedFlutterVersion() {
		return fmt.Errorf(
			"Desktop build Flutter toolchain is not %s",
			expectedFlutterVersion,
		)
	}
	if tools.Dart != "Dart "+expectedDartVersion {
		return fmt.Errorf(
			"Desktop build Dart toolchain is not %s",
			expectedDartVersion,
		)
	}
	if tools.Xcode != expectedXcodeVersion {
		return fmt.Errorf(
			"Desktop build Xcode toolchain is not admitted",
		)
	}
	return nil
}

func normalizedFlutterVersion() string {
	return "Flutter " + expectedFlutterVersion + " (" + expectedFlutterRevision + ")"
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size &&
		value == strings.ToLower(value)
}

func mapsClone(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func validateFrozenProvenance(provenance acceptanceProvenance) error {
	if provenance.Source.Dirty {
		return errors.New(
			"acceptance artifacts were built from a dirty Git worktree",
		)
	}
	if len(provenance.Source.Revision) != 40 {
		return errors.New("acceptance Git revision is not a full commit identity")
	}
	return nil
}

func readGoBinaryEvidence(role, path string) (goBinaryEvidence, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return goBinaryEvidence{}, fmt.Errorf(
			"read %s Go build identity: %w",
			role,
			err,
		)
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	dirty, err := strconv.ParseBool(settings["vcs.modified"])
	if err != nil {
		return goBinaryEvidence{}, fmt.Errorf(
			"%s Go build has no VCS dirty-state evidence",
			role,
		)
	}
	evidence := goBinaryEvidence{
		role:       role,
		goVersion:  info.GoVersion,
		vcs:        settings["vcs"],
		revision:   settings["vcs.revision"],
		commitTime: settings["vcs.time"],
		dirty:      dirty,
		tags:       canonicalBuildTags(settings["-tags"]),
	}
	if evidence.goVersion == "" ||
		evidence.vcs == "" ||
		evidence.revision == "" ||
		evidence.commitTime == "" {
		return goBinaryEvidence{}, fmt.Errorf(
			"%s Go build identity is incomplete",
			role,
		)
	}
	return evidence, nil
}

func commonSourceEvidence(
	binaries []goBinaryEvidence,
) (sourceProvenance, error) {
	if len(binaries) == 0 {
		return sourceProvenance{}, errors.New("no Go artifact source evidence")
	}
	first := binaries[0]
	for _, candidate := range binaries[1:] {
		if candidate.vcs != first.vcs ||
			candidate.revision != first.revision ||
			candidate.commitTime != first.commitTime ||
			candidate.dirty != first.dirty ||
			candidate.goVersion != first.goVersion {
			return sourceProvenance{}, errors.New(
				"acceptance, daemon, and launcher do not share one Git source identity",
			)
		}
	}
	if first.vcs != "git" {
		return sourceProvenance{}, errors.New(
			"acceptance source identity is not Git",
		)
	}
	return sourceProvenance{
		VCS:        first.vcs,
		Revision:   first.revision,
		CommitTime: first.commitTime,
		Dirty:      first.dirty,
	}, nil
}

func sidecarProfile(binaries []goBinaryEvidence) (string, error) {
	var daemonTags, launcherTags string
	for _, binary := range binaries {
		switch binary.role {
		case "daemon":
			daemonTags = binary.tags
		case "launcher":
			launcherTags = binary.tags
		}
	}
	if daemonTags == "" && launcherTags == "" {
		return "development", nil
	}
	if hasBuildTag(daemonTags, "vibermate_native_secrets") &&
		hasBuildTag(launcherTags, "vibermate_native_secrets") &&
		daemonTags == launcherTags {
		return "release", nil
	}
	return "", errors.New(
		"packaged daemon and launcher use inconsistent sidecar build profiles",
	)
}

func canonicalBuildTags(value string) string {
	fields := strings.FieldsFunc(value, func(character rune) bool {
		return character == ',' || character == ' '
	})
	sort.Strings(fields)
	return strings.Join(fields, ",")
}

func hasBuildTag(tags, expected string) bool {
	for _, tag := range strings.Split(tags, ",") {
		if tag == expected {
			return true
		}
	}
	return false
}

func collectToolchains(ctx context.Context) (toolchainProvenance, error) {
	goVersion, err := toolVersion(ctx, "Go", "go", []string{"version"})
	if err != nil {
		return toolchainProvenance{}, err
	}
	flutterSource, err := toolVersion(
		ctx,
		"Flutter",
		"flutter",
		[]string{"--version", "--machine"},
	)
	if err != nil {
		return toolchainProvenance{}, err
	}
	var flutter struct {
		Channel           string `json:"channel"`
		DartSDKVersion    string `json:"dartSdkVersion"`
		FrameworkRevision string `json:"frameworkRevision"`
		FrameworkVersion  string `json:"frameworkVersion"`
	}
	decoder := json.NewDecoder(strings.NewReader(flutterSource))
	if err := decoder.Decode(&flutter); err != nil {
		return toolchainProvenance{}, errors.New(
			"Flutter machine-readable toolchain evidence is malformed",
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return toolchainProvenance{}, errors.New(
			"Flutter machine-readable toolchain evidence has trailing data",
		)
	}
	if flutter.Channel != "stable" ||
		flutter.FrameworkVersion == "" ||
		flutter.FrameworkRevision == "" ||
		flutter.DartSDKVersion == "" {
		return toolchainProvenance{}, errors.New(
			"Flutter machine-readable toolchain evidence is incomplete",
		)
	}
	xcodeVersion, err := toolVersion(
		ctx,
		"Xcode",
		"/usr/bin/xcodebuild",
		[]string{"-version"},
	)
	if err != nil {
		return toolchainProvenance{}, err
	}
	return toolchainProvenance{
		Go: goVersion,
		Flutter: "Flutter " + flutter.FrameworkVersion + " (" +
			flutter.FrameworkRevision + ")",
		Dart:  "Dart " + flutter.DartSDKVersion,
		Xcode: xcodeVersion,
	}, nil
}

func validateToolchains(
	tools toolchainProvenance,
	binaries []goBinaryEvidence,
) error {
	for _, binary := range binaries {
		if binary.goVersion != expectedGoVersion {
			return fmt.Errorf(
				"%s was built with %s, want %s",
				binary.role,
				binary.goVersion,
				expectedGoVersion,
			)
		}
	}
	goFields := strings.Fields(tools.Go)
	if len(goFields) < 3 || goFields[2] != expectedGoVersion {
		return fmt.Errorf("Go toolchain is not %s", expectedGoVersion)
	}
	if tools.Flutter != normalizedFlutterVersion() {
		return fmt.Errorf("Flutter toolchain is not %s", expectedFlutterVersion)
	}
	if tools.Dart != "Dart "+expectedDartVersion {
		return fmt.Errorf("Dart toolchain is not %s", expectedDartVersion)
	}
	if tools.Xcode != expectedXcodeVersion {
		return errors.New("Xcode toolchain is not the frozen release version")
	}
	return nil
}

func toolVersion(
	parent context.Context,
	label, name string,
	arguments []string,
) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("locate %s toolchain: %w", label, err)
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, arguments...)
	command.Env = acceptanceEnvironment(os.Environ())
	output := newBoundedBuffer(16 << 10)
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("read %s toolchain version: %w", label, err)
	}
	payload, overflow := output.snapshot()
	version := strings.TrimSpace(string(payload))
	if overflow || version == "" {
		return "", fmt.Errorf("%s toolchain version is invalid", label)
	}
	return version, nil
}
