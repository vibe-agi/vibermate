package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/releasemanifest"
)

func TestRunWritesPrivateManifestAndChecksum(t *testing.T) {
	fixture := newCLIFixture(t)
	var stdout bytes.Buffer
	if err := run(fixture.arguments(), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	payload, err := os.ReadFile(fixture.output)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := releasemanifest.DecodeBytes(payload)
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if manifest.Commit != fixture.revision {
		t.Fatalf("output commit = %q, want %q", manifest.Commit, fixture.revision)
	}
	digest := sha256.Sum256(payload)
	wantChecksum := hex.EncodeToString(digest[:]) + "  " + filepath.Base(fixture.output) + "\n"
	checksum, err := os.ReadFile(fixture.output + ".sha256")
	if err != nil {
		t.Fatal(err)
	}
	if string(checksum) != wantChecksum {
		t.Fatalf("checksum = %q, want %q", checksum, wantChecksum)
	}
	for _, path := range []string{fixture.output, fixture.output + ".sha256"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if permission := info.Mode().Perm(); permission != 0o600 {
			t.Fatalf("%s permission = %o, want 600", path, permission)
		}
	}
	if !strings.Contains(stdout.String(), fixture.output) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunRejectsDirtySource(t *testing.T) {
	fixture := newCLIFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.sourceRoot, "dirty"), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run(fixture.arguments(), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("run() error = %v, want dirty source error", err)
	}
	if _, statErr := os.Stat(fixture.output); !os.IsNotExist(statErr) {
		t.Fatalf("output exists after failure: %v", statErr)
	}
}

func TestRunRejectsActiveRepositoryExcludePattern(t *testing.T) {
	fixture := newCLIFixture(t)
	excludePath := strings.TrimSpace(git(
		t,
		fixture.sourceRoot,
		"rev-parse",
		"--git-path",
		"info/exclude",
	))
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(fixture.sourceRoot, excludePath)
	}
	if err := os.WriteFile(excludePath, []byte("masked.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(fixture.sourceRoot, "masked.txt"),
		[]byte("masked\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	err := run(fixture.arguments(), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "exclude file contains an active pattern") {
		t.Fatalf("run() error = %v, want active repository exclude error", err)
	}
	if _, statErr := os.Stat(fixture.output); !os.IsNotExist(statErr) {
		t.Fatalf("output exists after failure: %v", statErr)
	}
}

func TestRunRejectsSourceRevisionMismatch(t *testing.T) {
	fixture := newCLIFixture(t)
	manifest := fixture.manifest
	manifest.Commit = strings.Repeat("b", 40)
	fixture.revision = manifest.Commit
	writeSpec(t, fixture.spec, manifest)
	err := run(fixture.arguments(), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "Git HEAD") {
		t.Fatalf("run() error = %v, want Git HEAD mismatch", err)
	}
}

func TestRunDisablesReplacementObjectsWhileVerifyingCommit(t *testing.T) {
	fixture := newCLIFixture(t)
	originalTree := strings.TrimSpace(git(
		t,
		fixture.sourceRoot,
		"show",
		"--no-patch",
		"--format=%T",
		"HEAD",
	))
	if err := os.WriteFile(
		filepath.Join(fixture.sourceRoot, "replacement-only.txt"),
		[]byte("replacement tree\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	git(t, fixture.sourceRoot, "add", "replacement-only.txt")
	replacementTree := strings.TrimSpace(git(t, fixture.sourceRoot, "write-tree"))
	git(t, fixture.sourceRoot, "reset", "-q", "--hard", fixture.revision)
	replacementCommit := strings.TrimSpace(git(
		t,
		fixture.sourceRoot,
		"commit-tree",
		replacementTree,
		"-m",
		"replacement commit",
	))
	git(t, fixture.sourceRoot, "replace", fixture.revision, replacementCommit)

	visibleTree := strings.TrimSpace(git(
		t,
		fixture.sourceRoot,
		"show",
		"--no-patch",
		"--format=%T",
		"HEAD",
	))
	if originalTree == replacementTree || visibleTree != replacementTree {
		t.Fatalf(
			"replace fixture did not redirect HEAD tree: original = %q, replacement = %q, visible = %q",
			originalTree,
			replacementTree,
			visibleTree,
		)
	}
	if status := git(
		t,
		fixture.sourceRoot,
		"status",
		"--porcelain=v1",
		"--untracked-files=all",
	); !strings.Contains(status, "replacement-only.txt") {
		t.Fatalf("ordinary Git status did not observe replacement tree: %q", status)
	}

	if err := run(fixture.arguments(), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run() error with active replace ref = %v", err)
	}
	payload, err := os.ReadFile(fixture.output)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := releasemanifest.DecodeBytes(payload)
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if manifest.Commit != fixture.revision || manifest.Commit == replacementCommit {
		t.Fatalf(
			"output commit = %q, want original HEAD %q and not replacement %q",
			manifest.Commit,
			fixture.revision,
			replacementCommit,
		)
	}
}

func TestRunRejectsSPDXPayloadLedgerDigestMismatch(t *testing.T) {
	fixture := newCLIFixture(t)
	spdxArtifact := cliArtifactForRole(
		t,
		&fixture.manifest,
		releasemanifest.ArtifactRoleSBOM,
	)
	spdxPath := filepath.Join(fixture.artifactRoot, spdxArtifact.Path)
	payload, err := os.ReadFile(spdxPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	comment, ok := document["comment"].(string)
	if !ok {
		t.Fatalf("SPDX comment has type %T, want string", document["comment"])
	}
	ledgerDigest := cliArtifactForRole(
		t,
		&fixture.manifest,
		releasemanifest.ArtifactRoleAppTreeLedger,
	).SHA256
	wantBinding := "payloadLedgerSHA256=" + ledgerDigest
	if !strings.Contains(comment, wantBinding) {
		t.Fatalf("SPDX comment = %q, want binding %q", comment, wantBinding)
	}
	document["comment"] = strings.Replace(
		comment,
		wantBinding,
		"payloadLedgerSHA256="+strings.Repeat("f", 64),
		1,
	)
	writeCLIArtifact(
		t,
		fixture.artifactRoot,
		&fixture.manifest,
		releasemanifest.ArtifactRoleSBOM,
		cliJSON(t, document),
	)
	writeSpec(t, fixture.spec, fixture.manifest)

	err = run(fixture.arguments(), &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, releasemanifest.ErrArtifactMismatch) ||
		!strings.Contains(err.Error(), "payload ledger") {
		t.Fatalf("run() error = %v, want SPDX payload ledger binding error", err)
	}
	if _, statErr := os.Stat(fixture.output); !os.IsNotExist(statErr) {
		t.Fatalf("output exists after failure: %v", statErr)
	}
}

func TestRunRejectsOutputSelfReference(t *testing.T) {
	fixture := newCLIFixture(t)
	fixture.output = filepath.Join(fixture.artifactRoot, "app-tree.json")
	err := run(fixture.arguments(), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "inside the artifact root") {
		t.Fatalf("run() error = %v, want artifact-root overlap error", err)
	}
}

func TestRunRejectsOutputInsideUnsignedPayload(t *testing.T) {
	fixture := newCLIFixture(t)
	fixture.output = filepath.Join(
		fixture.artifactRoot,
		releasemanifest.UnsignedPayloadRoot,
		"dist",
		"release-manifest.json",
	)
	err := run(fixture.arguments(), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "inside the artifact root") {
		t.Fatalf("run() error = %v, want artifact-root overlap error", err)
	}
	if _, statErr := os.Lstat(fixture.output); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output exists after overlap failure: %v", statErr)
	}
}

func TestRunRejectsOutputInsideSourceRoot(t *testing.T) {
	fixture := newCLIFixture(t)
	fixture.output = filepath.Join(fixture.sourceRoot, "release-manifest.json")
	err := run(fixture.arguments(), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "inside the source root") {
		t.Fatalf("run() error = %v, want source-root overlap error", err)
	}
	if _, statErr := os.Lstat(fixture.output); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output exists after overlap failure: %v", statErr)
	}
}

func TestRunRejectsSpecOverwrite(t *testing.T) {
	fixture := newCLIFixture(t)
	fixture.output = fixture.spec
	err := run(fixture.arguments(), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "input spec") {
		t.Fatalf("run() error = %v, want input overwrite error", err)
	}
}

func TestRunDoesNotOverwriteExistingOutput(t *testing.T) {
	fixture := newCLIFixture(t)
	if err := os.WriteFile(fixture.output, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run(fixture.arguments(), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("run() error = %v, want existing output error", err)
	}
	content, readErr := os.ReadFile(fixture.output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "keep" {
		t.Fatalf("existing output was changed to %q", content)
	}
}

func TestRunRejectsSymlinkSpec(t *testing.T) {
	fixture := newCLIFixture(t)
	link := filepath.Join(canonicalTempDir(t), "release-spec.json")
	if err := os.Symlink(fixture.spec, link); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	fixture.spec = link
	err := run(fixture.arguments(), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "symbolic-link") {
		t.Fatalf("run() error = %v, want symlink spec error", err)
	}
}

func TestRunRejectsSymlinkAncestors(t *testing.T) {
	tests := map[string]func(*testing.T, *cliFixture){
		"spec": func(t *testing.T, fixture *cliFixture) {
			link := filepath.Join(canonicalTempDir(t), "spec-parent")
			if err := os.Symlink(filepath.Dir(fixture.spec), link); err != nil {
				t.Skipf("symbolic links are unavailable: %v", err)
			}
			fixture.spec = filepath.Join(link, filepath.Base(fixture.spec))
		},
		"source root": func(t *testing.T, fixture *cliFixture) {
			link := filepath.Join(canonicalTempDir(t), "source-root")
			if err := os.Symlink(fixture.sourceRoot, link); err != nil {
				t.Skipf("symbolic links are unavailable: %v", err)
			}
			fixture.sourceRoot = link
		},
		"artifact root": func(t *testing.T, fixture *cliFixture) {
			link := filepath.Join(canonicalTempDir(t), "artifact-root")
			if err := os.Symlink(fixture.artifactRoot, link); err != nil {
				t.Skipf("symbolic links are unavailable: %v", err)
			}
			fixture.artifactRoot = link
		},
		"output directory": func(t *testing.T, fixture *cliFixture) {
			link := filepath.Join(canonicalTempDir(t), "output-parent")
			if err := os.Symlink(filepath.Dir(fixture.output), link); err != nil {
				t.Skipf("symbolic links are unavailable: %v", err)
			}
			fixture.output = filepath.Join(link, filepath.Base(fixture.output))
		},
	}
	for name, prepare := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newCLIFixture(t)
			prepare(t, &fixture)
			err := run(fixture.arguments(), &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil ||
				(!strings.Contains(err.Error(), "symbolic") &&
					!strings.Contains(err.Error(), "symlink")) {
				t.Fatalf("run() error = %v, want symbolic-link ancestor error", err)
			}
		})
	}
}

func TestParseOptionsRequiresExplicitAbsolutePaths(t *testing.T) {
	_, err := parseOptions([]string{
		"--spec", "relative.json",
		"--artifact-root", "/artifacts",
		"--source-root", "/source",
		"--expected-revision", strings.Repeat("a", 40),
		"--output", "/release.json",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("parseOptions() error = %v, want absolute path error", err)
	}
}

type cliFixture struct {
	sourceRoot   string
	artifactRoot string
	spec         string
	output       string
	revision     string
	manifest     releasemanifest.Manifest
}

func (fixture cliFixture) arguments() []string {
	return []string{
		"--spec", fixture.spec,
		"--artifact-root", fixture.artifactRoot,
		"--source-root", fixture.sourceRoot,
		"--expected-revision", fixture.revision,
		"--output", fixture.output,
	}
}

func newCLIFixture(t *testing.T) cliFixture {
	t.Helper()
	sourceRoot := canonicalTempDir(t)
	git(t, sourceRoot, "init", "-q")
	git(t, sourceRoot, "config", "user.email", "release-test@example.invalid")
	git(t, sourceRoot, "config", "user.name", "Release Test")
	if err := os.WriteFile(filepath.Join(sourceRoot, "source.txt"), []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, sourceRoot, "add", "source.txt")
	git(t, sourceRoot, "commit", "-q", "-m", "fixture")
	revision := strings.TrimSpace(git(t, sourceRoot, "rev-parse", "HEAD"))

	artifactRoot := canonicalTempDir(t)
	manifest := cliManifest(t, artifactRoot, revision)
	specDir := canonicalTempDir(t)
	spec := filepath.Join(specDir, "release-spec.json")
	writeSpec(t, spec, manifest)
	output := filepath.Join(canonicalTempDir(t), "release-manifest.json")
	return cliFixture{
		sourceRoot:   sourceRoot,
		artifactRoot: artifactRoot,
		spec:         spec,
		output:       output,
		revision:     revision,
		manifest:     manifest,
	}
}

func cliManifest(t *testing.T, artifactRoot, revision string) releasemanifest.Manifest {
	t.Helper()
	artifacts := []releasemanifest.Artifact{
		{Path: "app-tree.json", MediaType: "application/json", Role: releasemanifest.ArtifactRoleAppTreeLedger},
		{Path: "desktop-build-manifest.json", MediaType: "application/json", Role: releasemanifest.ArtifactRoleDesktopBuildManifest},
		{Path: "sbom.spdx.json", MediaType: "application/spdx+json", Role: releasemanifest.ArtifactRoleSBOM},
		{Path: "known-issues.json", MediaType: "application/json", Role: releasemanifest.ArtifactRoleKnownIssues},
	}
	capability := releasemanifest.CapabilityStatus{
		Capability:           "local-control",
		Status:               releasemanifest.CapabilityUnknown,
		EvidenceStatus:       releasemanifest.EvidenceMissing,
		EvidenceArtifactRole: "none",
	}
	manifest := releasemanifest.Manifest{
		Schema:         releasemanifest.Schema,
		Version:        "0.1.0-preview.1",
		Channel:        releasemanifest.ChannelPreview,
		Commit:         revision,
		ProtocolSchema: "2026-08-01",
		PluginSDK:      "1.0.0",
		PlatformSupport: []releasemanifest.PlatformSupportEntry{
			{
				OS:                  "macos",
				Range:               ">=14.0",
				Architectures:       []string{"arm64", "x86_64"},
				InstallShape:        "dmg",
				SupportLevel:        releasemanifest.SupportPreview,
				ConformanceRevision: "macos-pack-2026-08-03",
				HostCapabilities:    []releasemanifest.CapabilityStatus{capability},
			},
		},
		Artifacts: artifacts,
		SBOM:      releasemanifest.ArtifactReference{Role: releasemanifest.ArtifactRoleSBOM},
		EvidenceScope: releasemanifest.EvidenceScope{
			Level:                  "r0",
			ArtifactState:          "unsigned-pre-sign",
			R2Reproducibility:      "not-asserted",
			R3SignedPackageBinding: "not-asserted",
			ReleaseApproval:        "not-asserted",
		},
		Migration: releasemanifest.MigrationCompatibility{
			MinimumSchema:         "0",
			MaximumSchema:         "26",
			RollbackCompatibility: "backup-required",
		},
		HostSupport: []releasemanifest.HostSupportEntry{
			{
				Host:                "desktop",
				SupportLevel:        releasemanifest.SupportPreview,
				ConformanceRevision: "desktop-pack-2026-08-03",
				Capabilities:        []releasemanifest.CapabilityStatus{capability},
			},
			{
				Host:                "server",
				SupportLevel:        releasemanifest.SupportUnsupported,
				ConformanceRevision: "server-not-shipped",
				Capabilities: []releasemanifest.CapabilityStatus{
					{
						Capability:           "runtime",
						Status:               releasemanifest.CapabilityUnsupported,
						EvidenceStatus:       releasemanifest.EvidenceMissing,
						EvidenceArtifactRole: "none",
					},
				},
			},
		},
		KnownIssues: releasemanifest.ArtifactReference{Role: releasemanifest.ArtifactRoleKnownIssues},
		PublishedAt: "2026-08-03T12:34:56+08:00",
	}
	configuration := map[string]string{
		"go.mod":                              strings.Repeat("1", 64),
		"go.sum":                              strings.Repeat("2", 64),
		"ui/flutter_app/.metadata":            strings.Repeat("3", 64),
		"ui/flutter_app/pubspec.yaml":         strings.Repeat("4", 64),
		"ui/flutter_app/pubspec.lock":         strings.Repeat("5", 64),
		"ui/flutter_app/tool/flutter-sdk.env": strings.Repeat("6", 64),
		"ui/flutter_app/macos/Runner.xcodeproj/project.pbxproj": strings.Repeat("7", 64),
		"ui/flutter_app/macos/Runner/Configs/AppInfo.xcconfig":  strings.Repeat("8", 64),
		"ui/flutter_app/macos/Runner/Configs/Release.xcconfig":  strings.Repeat("9", 64),
		"ui/flutter_app/macos/Runner/Info.plist":                strings.Repeat("a", 64),
		"ui/flutter_app/macos/Runner/Release.entitlements":      strings.Repeat("b", 64),
	}
	sidecarPayloads := map[string][]byte{
		"vibermate":  []byte("launcher\n"),
		"vibermated": []byte("daemon!!\n"),
	}
	appFrameworkPayload := []byte("universal App framework\n")
	flutterFrameworkPayload := []byte("universal FlutterMacOS framework\n")
	nestedCode := map[string]string{
		"app-framework":           cliSHA256(appFrameworkPayload),
		"flutter-macos-framework": cliSHA256(flutterFrameworkPayload),
		"vibermate":               cliSHA256(sidecarPayloads["vibermate"]),
		"vibermated":              cliSHA256(sidecarPayloads["vibermated"]),
	}
	desktopPayload := cliJSON(t, map[string]any{
		"schema": releasemanifest.DesktopBuildSchemaV3,
		"source": map[string]any{
			"vcs":        "git",
			"revision":   revision,
			"commitTime": "2026-08-03T04:34:56Z",
			"dirty":      false,
		},
		"profiles": map[string]string{
			"desktop":  "release",
			"sidecars": "release",
			"target":   "universal-apple-darwin",
			"toolkit":  "flutter",
		},
		"toolchains": map[string]string{
			"go":      "go version go1.25.13 darwin/arm64",
			"flutter": "Flutter 3.41.5 (2c9...)",
			"dart":    "Dart 3.11.3",
			"xcode":   "Xcode 16.2\nBuild version 16C5032a",
		},
		"configurationSHA256": configuration,
		"nestedCodeSHA256":    nestedCode,
	})
	writeCLIArtifact(t, artifactRoot, &manifest, releasemanifest.ArtifactRoleDesktopBuildManifest, desktopPayload)
	desktopDigest := cliArtifactForRole(t, &manifest, releasemanifest.ArtifactRoleDesktopBuildManifest).SHA256
	payloadRoot := filepath.Join(artifactRoot, releasemanifest.UnsignedPayloadRoot)
	if err := os.Mkdir(payloadRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(payloadRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(payloadRoot, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(payloadRoot, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	mainPayload := []byte("desktop-main\n")
	licensePayload := []byte("fixture license\n")
	distPayload := []byte("<!doctype html>\n")
	writeCLIPayloadFile(t, artifactRoot, "vibermate-desktop", mainPayload, 0o755)
	writeCLIPayloadFile(t, artifactRoot, "vibermate", sidecarPayloads["vibermate"], 0o755)
	writeCLIPayloadFile(t, artifactRoot, "vibermated", sidecarPayloads["vibermated"], 0o755)
	writeCLIPayloadFile(t, artifactRoot, "dist/App.framework/App", appFrameworkPayload, 0o755)
	writeCLIPayloadFile(t, artifactRoot, "dist/FlutterMacOS.framework/FlutterMacOS", flutterFrameworkPayload, 0o755)
	writeCLIPayloadFile(t, artifactRoot, "vibermate-build-manifest.json", desktopPayload, 0o644)
	writeCLIPayloadFile(t, artifactRoot, "LICENSE", licensePayload, 0o644)
	writeCLIPayloadFile(t, artifactRoot, "dist/index.html", distPayload, 0o644)
	ledgerPayload := cliJSON(t, map[string]any{
		"schema":                     releasemanifest.AppTreeLedgerSchemaV1,
		"commit":                     revision,
		"root":                       releasemanifest.UnsignedPayloadRoot,
		"desktopBuildManifestSHA256": desktopDigest,
		"entries": []map[string]any{
			{"mode": 0o755, "path": ".", "type": "directory"},
			cliLedgerFileEntry("LICENSE", licensePayload, 0o644),
			{"mode": 0o755, "path": "dist", "type": "directory"},
			{"mode": 0o755, "path": "dist/App.framework", "type": "directory"},
			cliLedgerFileEntry("dist/App.framework/App", appFrameworkPayload, 0o755),
			{"mode": 0o755, "path": "dist/FlutterMacOS.framework", "type": "directory"},
			cliLedgerFileEntry("dist/FlutterMacOS.framework/FlutterMacOS", flutterFrameworkPayload, 0o755),
			cliLedgerFileEntry("dist/index.html", distPayload, 0o644),
			cliLedgerFileEntry("vibermate", sidecarPayloads["vibermate"], 0o755),
			cliLedgerFileEntry("vibermate-build-manifest.json", desktopPayload, 0o644),
			cliLedgerFileEntry("vibermate-desktop", mainPayload, 0o755),
			cliLedgerFileEntry("vibermated", sidecarPayloads["vibermated"], 0o755),
		},
	})
	writeCLIArtifact(t, artifactRoot, &manifest, releasemanifest.ArtifactRoleAppTreeLedger, ledgerPayload)
	ledgerDigest := sha256.Sum256(ledgerPayload)
	spdxPayload := cliJSON(t, map[string]any{
		"SPDXID":            "SPDXRef-DOCUMENT",
		"creationInfo":      map[string]any{"created": "2026-08-03T04:34:56Z", "creators": []string{"Tool: vibermate-release-evidence"}},
		"dataLicense":       "CC0-1.0",
		"name":              "vibermate-release-sbom",
		"spdxVersion":       "SPDX-2.3",
		"documentNamespace": "https://vibermate.example.invalid/spdx/" + revision,
		"comment": "vibermate.release version=" + manifest.Version +
			" commit=" + revision +
			" payloadLedgerSHA256=" + hex.EncodeToString(ledgerDigest[:]),
		"packages": []map[string]any{
			{
				"SPDXID":           "SPDXRef-Package-vibermate",
				"name":             "vibermate",
				"versionInfo":      manifest.Version,
				"downloadLocation": "NOASSERTION",
				"filesAnalyzed":    false,
				"licenseConcluded": "NOASSERTION",
				"licenseDeclared":  "NOASSERTION",
				"copyrightText":    "NOASSERTION",
			},
		},
	})
	writeCLIArtifact(t, artifactRoot, &manifest, releasemanifest.ArtifactRoleSBOM, spdxPayload)
	knownIssuesPayload := cliJSON(t, map[string]any{
		"schema":  releasemanifest.KnownIssuesSchemaV1,
		"version": manifest.Version,
		"commit":  revision,
		"issues":  []any{},
	})
	writeCLIArtifact(t, artifactRoot, &manifest, releasemanifest.ArtifactRoleKnownIssues, knownIssuesPayload)
	return manifest
}

func writeCLIPayloadFile(
	t *testing.T,
	root, relativePath string,
	payload []byte,
	mode os.FileMode,
) {
	t.Helper()
	fullPath := filepath.Join(
		root,
		releasemanifest.UnsignedPayloadRoot,
		filepath.FromSlash(relativePath),
	)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, payload, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fullPath, mode); err != nil {
		t.Fatal(err)
	}
}

func cliLedgerFileEntry(
	relativePath string,
	payload []byte,
	mode uint32,
) map[string]any {
	return map[string]any{
		"mode":   mode,
		"path":   relativePath,
		"type":   "file",
		"sha256": cliSHA256(payload),
		"size":   len(payload),
	}
}

func cliSHA256(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func writeCLIArtifact(
	t *testing.T,
	root string,
	manifest *releasemanifest.Manifest,
	role string,
	payload []byte,
) {
	t.Helper()
	artifact := cliArtifactForRole(t, manifest, role)
	if err := os.WriteFile(filepath.Join(root, artifact.Path), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	artifact.Size = int64(len(payload))
	artifact.SHA256 = hex.EncodeToString(digest[:])
}

func cliArtifactForRole(
	t *testing.T,
	manifest *releasemanifest.Manifest,
	role string,
) *releasemanifest.Artifact {
	t.Helper()
	for index := range manifest.Artifacts {
		if manifest.Artifacts[index].Role == role {
			return &manifest.Artifacts[index]
		}
	}
	t.Fatalf("artifact role %q is missing", role)
	return nil
}

func cliJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(payload, '\n')
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func writeSpec(t *testing.T, path string, manifest releasemanifest.Manifest) {
	t.Helper()
	payload, err := releasemanifest.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	commandArguments := append([]string{"-C", root}, arguments...)
	command := exec.Command("git", commandArguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return string(output)
}
