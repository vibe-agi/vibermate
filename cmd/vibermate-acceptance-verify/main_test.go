package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/acceptancereport"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

type cliBuildProfiles struct {
	Desktop  string `json:"desktop"`
	Sidecars string `json:"sidecars"`
	Target   string `json:"target"`
}

type cliBuildManifest struct {
	Schema              string                                  `json:"schema"`
	Source              acceptancereport.SourceProvenance       `json:"source"`
	Profiles            cliBuildProfiles                        `json:"profiles"`
	Toolchains          acceptancereport.DesktopBuildToolchains `json:"toolchains"`
	ConfigurationSHA256 map[string]string                       `json:"configurationSHA256"`
	SidecarSHA256       map[string]string                       `json:"sidecarSHA256"`
}

func TestRunVerifiesExactReportRevisionAndFixedClient(t *testing.T) {
	t.Parallel()
	report := validCLIReport(t)
	revision := report.Provenance.Source.Revision
	path := writeCLIReport(t, report)
	var stdout, stderr bytes.Buffer
	exitCode := run(cliArguments(path, revision, report), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("run() exit = %d, stderr = %q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), acceptancereport.SchemaV6) ||
		!strings.Contains(stdout.String(), revision) ||
		!strings.Contains(stdout.String(), "claude-code@2.1.220") {
		t.Fatalf("run() stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("run() stderr = %q", stderr.String())
	}
}

func TestRunRequiresAnExplicitNonDowngradedSchema(t *testing.T) {
	t.Parallel()
	report := validCLIReport(t)
	revision := report.Provenance.Source.Revision
	report.Schema = acceptancereport.SchemaV5
	report.Provenance.Build.ManifestSchema =
		acceptancereport.DesktopBuildManifestSchemaV1
	delete(
		report.Provenance.Build.ConfigurationSHA256,
		"rust-toolchain.toml",
	)
	checks := make([]acceptancereport.Check, 0, len(report.Checks)-1)
	for _, check := range report.Checks {
		if check.ID != "packaged-main-navigation-cold-restore" {
			checks = append(checks, check)
		}
	}
	report.Checks = checks
	path := writeCLIReport(t, report)

	var currentStdout, currentStderr bytes.Buffer
	if exitCode := run(
		cliArguments(path, revision, report),
		&currentStdout,
		&currentStderr,
	); exitCode != 1 || !strings.Contains(currentStderr.String(), "schema") {
		t.Fatalf(
			"current run exit = %d, stderr = %q",
			exitCode,
			currentStderr.String(),
		)
	}

	var historicalStdout, historicalStderr bytes.Buffer
	if exitCode := run(
		cliArgumentsForSchema(
			path,
			revision,
			acceptancereport.SchemaV5,
			report,
		),
		&historicalStdout,
		&historicalStderr,
	); exitCode != 0 {
		t.Fatalf(
			"historical run exit = %d, stderr = %q",
			exitCode,
			historicalStderr.String(),
		)
	}
}

func TestRunRejectsStaleFailedAndMissingReports(t *testing.T) {
	t.Parallel()
	t.Run("stale revision", func(t *testing.T) {
		report := validCLIReport(t)
		path := writeCLIReport(t, report)
		var stdout, stderr bytes.Buffer
		exitCode := run(
			cliArguments(path, strings.Repeat("a", 40), report),
			&stdout,
			&stderr,
		)
		if exitCode != 1 || !strings.Contains(stderr.String(), "stale") {
			t.Fatalf("run() exit = %d, stderr = %q", exitCode, stderr.String())
		}
	})
	t.Run("failed report", func(t *testing.T) {
		report := validCLIReport(t)
		revision := report.Provenance.Source.Revision
		report.Status = acceptancereport.StatusFailed
		path := writeCLIReport(t, report)
		var stdout, stderr bytes.Buffer
		exitCode := run(
			cliArguments(path, revision, report),
			&stdout,
			&stderr,
		)
		if exitCode != 1 || !strings.Contains(stderr.String(), "not passed") {
			t.Fatalf("run() exit = %d, stderr = %q", exitCode, stderr.String())
		}
	})
	t.Run("missing report", func(t *testing.T) {
		report := validCLIReport(t)
		revision := report.Provenance.Source.Revision
		path := filepath.Join(t.TempDir(), "missing.json")
		var stdout, stderr bytes.Buffer
		exitCode := run(
			cliArguments(path, revision, report),
			&stdout,
			&stderr,
		)
		if exitCode != 1 || stderr.Len() == 0 {
			t.Fatalf("run() exit = %d, stderr = %q", exitCode, stderr.String())
		}
	})
}

func TestRunRejectsIncompleteOrPositionalArguments(t *testing.T) {
	t.Parallel()
	report := validCLIReport(t)
	revision := report.Provenance.Source.Revision
	for _, arguments := range [][]string{
		{"--report", "/private/tmp/report.json"},
		{
			"--report", "/private/tmp/report.json",
			"--expected-schema", acceptancereport.SchemaV6,
			"--expected-revision", revision,
			"--expected-client-id", "claude-code",
			"--expected-client-version", "2.1.220",
		},
		append(
			cliArguments("/private/tmp/report.json", revision, report),
			"extra",
		),
	} {
		var stdout, stderr bytes.Buffer
		if exitCode := run(arguments, &stdout, &stderr); exitCode != 2 {
			t.Fatalf("run(%q) exit = %d, stderr = %q", arguments, exitCode, stderr.String())
		}
	}
}

func cliArguments(
	path, revision string,
	report acceptancereport.Report,
) []string {
	return cliArgumentsForSchema(
		path,
		revision,
		acceptancereport.SchemaV6,
		report,
	)
}

func cliArgumentsForSchema(
	path, revision, schema string,
	report acceptancereport.Report,
) []string {
	arguments := []string{
		"--report", path,
		"--expected-schema", schema,
		"--expected-revision", revision,
		"--expected-client-id", "claude-code",
		"--expected-client-version", "2.1.220",
	}
	if schema == acceptancereport.SchemaV6 {
		coordinates := cliArtifactCoordinates(report)
		arguments = append(
			arguments,
			"--source-root", coordinates.SourceRoot,
			"--desktop-app", coordinates.DesktopApp,
			"--acceptance-executable", coordinates.AcceptanceExecutable,
			"--client-entrypoint", coordinates.ClientEntrypoint,
		)
	}
	return arguments
}

func validCLIReport(t *testing.T) acceptancereport.Report {
	t.Helper()
	evidence, ok := clientadapter.BuiltInCatalog().ExpectedEvidence(
		"claude-code",
		"2.1.220",
	)
	if !ok {
		t.Fatal("fixed Claude evidence is missing")
	}
	checkIDs, err := acceptancereport.RequiredCheckIDs(
		"claude-code",
		"2.1.220",
	)
	if err != nil {
		t.Fatal(err)
	}
	checks := make([]acceptancereport.Check, len(checkIDs))
	for index, id := range checkIDs {
		checks[index] = acceptancereport.Check{
			ID: id, Status: acceptancereport.StatusPassed, Detail: "passed",
		}
	}
	roles := []string{
		"acceptance",
		"client-entrypoint",
		"daemon",
		"desktop-app-bundle",
		"desktop-app-executable",
		"desktop-build-manifest",
		"launcher",
	}
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	bundle := filepath.Join(root, "VibeMate.app")
	macOSDirectory := filepath.Join(bundle, "Contents", "MacOS")
	resourcesDirectory := filepath.Join(bundle, "Contents", "Resources")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(macOSDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(resourcesDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	artifactPaths := map[string]string{
		"acceptance":             filepath.Join(root, "vibermate-acceptance"),
		"client-entrypoint":      filepath.Join(root, "client"),
		"daemon":                 filepath.Join(macOSDirectory, "vibermated"),
		"desktop-app-bundle":     bundle,
		"desktop-app-executable": filepath.Join(macOSDirectory, "vibermate-desktop"),
		"desktop-build-manifest": filepath.Join(resourcesDirectory, "vibermate-build-manifest.json"),
		"launcher":               filepath.Join(macOSDirectory, "vibermate"),
	}
	for role, path := range artifactPaths {
		if role == "desktop-app-bundle" || role == "desktop-build-manifest" {
			continue
		}
		writeCLIArtifact(t, path, "fixture executable "+role+"\n", 0o755)
	}
	for _, role := range []string{"acceptance", "client-entrypoint"} {
		canonical, err := filepath.EvalSymlinks(artifactPaths[role])
		if err != nil {
			t.Fatal(err)
		}
		artifactPaths[role] = canonical
	}
	configurationPaths := []string{
		"go.mod",
		"go.sum",
		"rust-toolchain.toml",
		"ui/desktop/package.json",
		"ui/desktop/pnpm-lock.yaml",
		"ui/desktop/src-tauri/Cargo.toml",
		"ui/desktop/src-tauri/Cargo.lock",
		"ui/desktop/src-tauri/tauri.conf.json",
	}
	configurationDigests := make(map[string]string, len(configurationPaths))
	for _, name := range configurationPaths {
		path := filepath.Join(sourceRoot, filepath.FromSlash(name))
		writeCLIArtifact(t, path, "fixture configuration "+name+"\n", 0o644)
		evidence, err := acceptancereport.DigestArtifact(
			"configuration "+name,
			path,
		)
		if err != nil {
			t.Fatal(err)
		}
		configurationDigests[name] = evidence.SHA256
	}
	revision, commitTime := initializeCLIGitFixture(t, sourceRoot)
	tools := acceptancereport.ToolchainProvenance{
		Go:    "go version go1.25.12 darwin/arm64",
		Node:  acceptancereport.ExpectedNodeVersion,
		Rustc: "rustc 1.88.0 (fixture 2026-01-01)\nhost: aarch64-apple-darwin",
		Cargo: "cargo 1.88.0 (fixture 2026-01-01)",
		PNPM:  acceptancereport.ExpectedPNPMVersion,
	}
	source := acceptancereport.SourceProvenance{
		VCS: "git", Revision: revision,
		CommitTime: commitTime,
	}
	buildTools := acceptancereport.DesktopBuildToolchains{
		Go: tools.Go, Node: tools.Node, Rustc: tools.Rustc,
		Cargo: tools.Cargo, PNPM: tools.PNPM,
		Tauri: acceptancereport.ExpectedTauriVersion,
	}
	daemon, err := acceptancereport.DigestArtifact(
		"daemon",
		artifactPaths["daemon"],
	)
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := acceptancereport.DigestArtifact(
		"launcher",
		artifactPaths["launcher"],
	)
	if err != nil {
		t.Fatal(err)
	}
	manifestPayload, err := json.MarshalIndent(cliBuildManifest{
		Schema: acceptancereport.DesktopBuildManifestSchemaV2,
		Source: source,
		Profiles: cliBuildProfiles{
			Desktop: "release", Sidecars: "development",
			Target: acceptancereport.ExpectedBuildTarget,
		},
		Toolchains:          buildTools,
		ConfigurationSHA256: configurationDigests,
		SidecarSHA256: map[string]string{
			"vibermated": daemon.SHA256,
			"vibermate":  launcher.SHA256,
		},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeCLIArtifact(
		t,
		artifactPaths["desktop-build-manifest"],
		string(append(manifestPayload, '\n')),
		0o644,
	)
	artifacts := make([]acceptancereport.ArtifactProvenance, len(roles))
	for index, role := range roles {
		var artifact acceptancereport.ArtifactProvenance
		if role == "desktop-app-bundle" {
			artifact, err = acceptancereport.DigestBundle(bundle)
		} else {
			artifact, err = acceptancereport.DigestArtifact(
				role,
				artifactPaths[role],
			)
		}
		if err != nil {
			t.Fatal(err)
		}
		artifacts[index] = artifact
	}
	return acceptancereport.Report{
		Schema:       acceptancereport.SchemaV6,
		StartedAt:    time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC),
		FinishedAt:   time.Date(2026, 8, 2, 10, 1, 0, 0, time.UTC),
		Platform:     acceptancereport.ExpectedPlatform,
		Architecture: acceptancereport.ExpectedArchitecture,
		Client: acceptancereport.Client{
			ID: "claude-code", Version: "2.1.220", Adapter: &evidence,
		},
		Provenance: &acceptancereport.Provenance{
			Source:     source,
			Artifacts:  artifacts,
			Toolchains: tools,
			Build: acceptancereport.BuildProvenance{
				ManifestSchema:      acceptancereport.DesktopBuildManifestSchema,
				DesktopProfile:      "release",
				SidecarProfile:      "development",
				Target:              acceptancereport.ExpectedBuildTarget,
				Toolchains:          buildTools,
				ConfigurationSHA256: configurationDigests,
				GoBuildVersions: map[string]string{
					"acceptance": acceptancereport.ExpectedGoVersion,
					"daemon":     acceptancereport.ExpectedGoVersion,
					"launcher":   acceptancereport.ExpectedGoVersion,
				},
				GoBuildTags: map[string]string{
					"acceptance": "", "daemon": "", "launcher": "",
				},
			},
			Configuration: acceptancereport.Configuration{
				DeterministicOnly: true,
				ClientID:          "claude-code",
				ClientVersion:     "2.1.220",
				AccessID:          "assembly-001",
				ProviderOrigin:    "http://127.0.0.1:23333/v1",
				ProviderModel:     "dashscope:glm-5",
				Timeout:           "8m0s",
			},
		},
		Status: acceptancereport.StatusPassed,
		Checks: checks,
	}
}

func initializeCLIGitFixture(t *testing.T, root string) (string, string) {
	t.Helper()
	runCLIGitFixture(t, root, "init", "-q")
	runCLIGitFixture(t, root, "add", "--all")
	command := exec.Command(
		"git",
		"-C",
		root,
		"-c",
		"user.name=VibeMate Fixture",
		"-c",
		"user.email=fixture@vibermate.invalid",
		"commit",
		"-q",
		"-m",
		"fixture",
	)
	command.Env = append(
		os.Environ(),
		"GIT_AUTHOR_DATE=2026-08-02T09:59:00Z",
		"GIT_COMMITTER_DATE=2026-08-02T09:59:00Z",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("commit Git fixture: %v: %s", err, output)
	}
	revision := runCLIGitFixture(t, root, "rev-parse", "HEAD")
	commitTime := runCLIGitFixture(t, root, "show", "-s", "--format=%cI", "HEAD")
	return revision, commitTime
}

func runCLIGitFixture(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	commandArguments := append([]string{"-C", root}, arguments...)
	command := exec.Command("git", commandArguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func cliArtifactCoordinates(
	report acceptancereport.Report,
) acceptancereport.ArtifactCoordinates {
	byRole := make(map[string]string)
	if report.Provenance != nil {
		for _, artifact := range report.Provenance.Artifacts {
			byRole[artifact.Role] = artifact.Path
		}
	}
	bundle := byRole["desktop-app-bundle"]
	return acceptancereport.ArtifactCoordinates{
		SourceRoot:           filepath.Join(filepath.Dir(bundle), "source"),
		DesktopApp:           bundle,
		AcceptanceExecutable: byRole["acceptance"],
		ClientEntrypoint:     byRole["client-entrypoint"],
	}
}

func writeCLIArtifact(
	t *testing.T,
	path, content string,
	mode os.FileMode,
) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func writeCLIReport(t *testing.T, report acceptancereport.Report) string {
	t.Helper()
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "acceptance-report.json")
	if err := os.WriteFile(path, append(payload, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
