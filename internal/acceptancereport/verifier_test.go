package acceptancereport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

func TestVerifyFileAcceptsKnownGoodFixedClientFixtures(t *testing.T) {
	t.Parallel()
	for _, client := range []struct {
		id      string
		version string
		checks  int
	}{
		{id: "claude-code", version: "2.1.220", checks: 18},
		{id: "codex-cli", version: "0.145.0", checks: 19},
	} {
		client := client
		t.Run(client.id, func(t *testing.T) {
			t.Parallel()
			report, expected := validFixture(t, client.id, client.version)
			if len(report.Checks) != client.checks {
				t.Fatalf("fixture checks = %d, want %d", len(report.Checks), client.checks)
			}
			path := writeFixture(t, report)
			if err := VerifyFile(path, expected); err != nil {
				t.Fatalf("VerifyFile() error = %v", err)
			}
		})
	}
}

func TestVerifyFileRejectsBytesChangedAfterReportCreation(t *testing.T) {
	t.Parallel()

	t.Run("acceptance executable", func(t *testing.T) {
		report, expected := validFixture(t, "claude-code", "2.1.220")
		writeExecutableFixture(
			t,
			expected.Artifacts.AcceptanceExecutable,
			"changed acceptance executable\n",
		)
		if err := VerifyFile(writeFixture(t, report), expected); err == nil {
			t.Fatal("VerifyFile accepted changed acceptance bytes")
		}
	})

	t.Run("source configuration", func(t *testing.T) {
		report, expected := validFixture(t, "claude-code", "2.1.220")
		runGitFixture(
			t,
			expected.Artifacts.SourceRoot,
			"update-index",
			"--assume-unchanged",
			"go.mod",
		)
		writeRegularFixture(
			t,
			filepath.Join(expected.Artifacts.SourceRoot, "go.mod"),
			"changed module declaration\n",
			0o644,
		)
		if err := VerifyFile(writeFixture(t, report), expected); err == nil {
			t.Fatal("VerifyFile accepted changed source configuration")
		}
	})

	t.Run("dirty source checkout", func(t *testing.T) {
		report, expected := validFixture(t, "claude-code", "2.1.220")
		writeRegularFixture(
			t,
			filepath.Join(expected.Artifacts.SourceRoot, "untracked.txt"),
			"not part of the candidate\n",
			0o644,
		)
		err := VerifyFile(writeFixture(t, report), expected)
		if err == nil || !strings.Contains(err.Error(), "dirty") {
			t.Fatalf("VerifyFile dirty source error = %v", err)
		}
	})

	t.Run("clean source checkout at another commit", func(t *testing.T) {
		report, expected := validFixture(t, "claude-code", "2.1.220")
		writeRegularFixture(
			t,
			filepath.Join(expected.Artifacts.SourceRoot, "README.md"),
			"a later clean source commit\n",
			0o644,
		)
		runGitFixture(t, expected.Artifacts.SourceRoot, "add", "--all")
		commitGitFixture(
			t,
			expected.Artifacts.SourceRoot,
			"later fixture",
			"2026-08-02T10:00:00Z",
		)
		err := VerifyFile(writeFixture(t, report), expected)
		if err == nil || !strings.Contains(err.Error(), "checkout revision") {
			t.Fatalf("VerifyFile later checkout error = %v", err)
		}
	})
}

func TestVerifyFileAcceptsHistoricalV5CheckContract(t *testing.T) {
	t.Parallel()
	for _, client := range []struct {
		id      string
		version string
		checks  int
	}{
		{id: "claude-code", version: "2.1.220", checks: 17},
		{id: "codex-cli", version: "0.145.0", checks: 18},
	} {
		client := client
		t.Run(client.id, func(t *testing.T) {
			t.Parallel()
			report, expected := validFixture(t, client.id, client.version)
			report.Schema = SchemaV5
			report.Provenance.Build.ManifestSchema =
				DesktopBuildManifestSchemaV1
			delete(
				report.Provenance.Build.ConfigurationSHA256,
				"rust-toolchain.toml",
			)
			expected.Schema = SchemaV5
			checks := make([]Check, 0, len(report.Checks)-1)
			for _, check := range report.Checks {
				if check.ID != "packaged-main-navigation-cold-restore" {
					checks = append(checks, check)
				}
			}
			report.Checks = checks
			if len(report.Checks) != client.checks {
				t.Fatalf("v5 fixture checks = %d, want %d", len(report.Checks), client.checks)
			}

			if err := VerifyFile(writeFixture(t, report), expected); err != nil {
				t.Fatalf("VerifyFile(v5) error = %v", err)
			}
		})
	}
}

func TestVerifyFileRejectsHistoricalV1BuildManifestInV6Report(t *testing.T) {
	t.Parallel()

	report, expected := validFixture(t, "claude-code", "2.1.220")
	report.Provenance.Build.ManifestSchema = DesktopBuildManifestSchemaV1
	delete(
		report.Provenance.Build.ConfigurationSHA256,
		"rust-toolchain.toml",
	)
	if err := VerifyFile(writeFixture(t, report), expected); err == nil {
		t.Fatal("VerifyFile accepted a v6 report with a v1 build manifest")
	}
}

func TestVerifyFileRejectsV5ReportWithV6CheckSet(t *testing.T) {
	t.Parallel()
	report, expected := validFixture(t, "claude-code", "2.1.220")
	report.Schema = SchemaV5
	expected.Schema = SchemaV5
	if err := VerifyFile(writeFixture(t, report), expected); err == nil {
		t.Fatal("VerifyFile(v5) accepted the v6 check set")
	}
}

func TestVerifyFileRejectsSchemaDowngradeFromCurrentExpectation(t *testing.T) {
	t.Parallel()
	report, expected := validFixture(t, "claude-code", "2.1.220")
	report.Schema = SchemaV5
	checks := make([]Check, 0, len(report.Checks)-1)
	for _, check := range report.Checks {
		if check.ID != "packaged-main-navigation-cold-restore" {
			checks = append(checks, check)
		}
	}
	report.Checks = checks
	err := VerifyFile(writeFixture(t, report), expected)
	if err == nil || !strings.Contains(err.Error(), "schema differs") {
		t.Fatalf("VerifyFile(v6 expectation) downgrade error = %v", err)
	}
}

func TestVerifyFileRejectsTypedMutations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Report, *Expectations)
	}{
		{
			name: "missing expected schema",
			mutate: func(_ *Report, expected *Expectations) {
				expected.Schema = ""
			},
		},
		{
			name: "unsupported expected schema",
			mutate: func(_ *Report, expected *Expectations) {
				expected.Schema = "vibermate.m0-assembly-acceptance/v4"
			},
		},
		{
			name: "short expected revision",
			mutate: func(_ *Report, expected *Expectations) {
				expected.Revision = "0123456"
			},
		},
		{
			name: "uppercase expected revision",
			mutate: func(_ *Report, expected *Expectations) {
				expected.Revision = strings.ToUpper(expected.Revision)
			},
		},
		{
			name: "unsupported expected client",
			mutate: func(_ *Report, expected *Expectations) {
				expected.ClientID = "other-client"
			},
		},
		{
			name: "unsupported expected client version",
			mutate: func(_ *Report, expected *Expectations) {
				expected.ClientVersion = "latest"
			},
		},
		{
			name: "missing trusted artifact coordinates",
			mutate: func(_ *Report, expected *Expectations) {
				expected.Artifacts = ArtifactCoordinates{}
			},
		},
		{
			name: "trusted artifact coordinate swap",
			mutate: func(_ *Report, expected *Expectations) {
				expected.Artifacts.AcceptanceExecutable =
					expected.Artifacts.ClientEntrypoint
			},
		},
		{
			name: "wrong schema",
			mutate: func(report *Report, _ *Expectations) {
				report.Schema = "vibermate.m0-assembly-acceptance/v4"
			},
		},
		{
			name: "wrong platform",
			mutate: func(report *Report, _ *Expectations) {
				report.Platform = "linux"
			},
		},
		{
			name: "wrong architecture",
			mutate: func(report *Report, _ *Expectations) {
				report.Architecture = "amd64"
			},
		},
		{
			name: "zero started timestamp",
			mutate: func(report *Report, _ *Expectations) {
				report.StartedAt = time.Time{}
			},
		},
		{
			name: "finished before started",
			mutate: func(report *Report, _ *Expectations) {
				report.FinishedAt = report.StartedAt.Add(-time.Second)
			},
		},
		{
			name: "failed report",
			mutate: func(report *Report, _ *Expectations) {
				report.Status = StatusFailed
			},
		},
		{
			name: "blocked report",
			mutate: func(report *Report, _ *Expectations) {
				report.Status = StatusBlocked
			},
		},
		{
			name: "missing provenance",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance = nil
			},
		},
		{
			name: "dirty source",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Source.Dirty = true
			},
		},
		{
			name: "stale source revision",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Source.Revision = strings.Repeat("a", 40)
			},
		},
		{
			name: "abbreviated source revision",
			mutate: func(report *Report, expected *Expectations) {
				report.Provenance.Source.Revision = expected.Revision[:12]
			},
		},
		{
			name: "uppercase source revision",
			mutate: func(report *Report, expected *Expectations) {
				report.Provenance.Source.Revision = strings.ToUpper(expected.Revision)
			},
		},
		{
			name: "non Git source",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Source.VCS = "hg"
			},
		},
		{
			name: "invalid source commit time",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Source.CommitTime = "yesterday"
			},
		},
		{
			name: "report client ID drift",
			mutate: func(report *Report, _ *Expectations) {
				report.Client.ID = "codex-cli"
			},
		},
		{
			name: "report client version drift",
			mutate: func(report *Report, _ *Expectations) {
				report.Client.Version = "2.1.221"
			},
		},
		{
			name: "missing adapter evidence",
			mutate: func(report *Report, _ *Expectations) {
				report.Client.Adapter = nil
			},
		},
		{
			name: "adapter digest drift",
			mutate: func(report *Report, _ *Expectations) {
				report.Client.Adapter.ReleaseSHA256 = strings.Repeat("a", 64)
			},
		},
		{
			name: "adapter catalog revision drift",
			mutate: func(report *Report, _ *Expectations) {
				report.Client.Adapter.CatalogRevision++
			},
		},
		{
			name: "adapter launch recipe drift",
			mutate: func(report *Report, _ *Expectations) {
				report.Client.Adapter.LaunchRecipe = clientadapter.LaunchSSLCertFile
			},
		},
		{
			name: "non deterministic configuration",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Configuration.DeterministicOnly = false
			},
		},
		{
			name: "configuration client ID drift",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Configuration.ClientID = "codex-cli"
			},
		},
		{
			name: "configuration client version drift",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Configuration.ClientVersion = "latest"
			},
		},
		{
			name: "invalid access ID",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Configuration.AccessID = " has spaces "
			},
		},
		{
			name: "invalid provider origin",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Configuration.ProviderOrigin = "file:///tmp/provider"
			},
		},
		{
			name: "remote cleartext provider origin",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Configuration.ProviderOrigin = "http://api.example.com/v1"
			},
		},
		{
			name: "blank provider model",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Configuration.ProviderModel = ""
			},
		},
		{
			name: "nonpositive timeout",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Configuration.Timeout = "0s"
			},
		},
		{
			name: "failed check",
			mutate: func(report *Report, _ *Expectations) {
				report.Checks[0].Status = StatusFailed
			},
		},
		{
			name: "blocked check",
			mutate: func(report *Report, _ *Expectations) {
				report.Checks[0].Status = StatusBlocked
			},
		},
		{
			name: "empty check detail",
			mutate: func(report *Report, _ *Expectations) {
				report.Checks[0].Detail = ""
			},
		},
		{
			name: "missing check",
			mutate: func(report *Report, _ *Expectations) {
				report.Checks = report.Checks[:len(report.Checks)-1]
			},
		},
		{
			name: "duplicate check ID",
			mutate: func(report *Report, _ *Expectations) {
				report.Checks[len(report.Checks)-1].ID = report.Checks[0].ID
			},
		},
		{
			name: "extra check",
			mutate: func(report *Report, _ *Expectations) {
				report.Checks = append(report.Checks, Check{
					ID: "not-required", Status: StatusPassed, Detail: "passed",
				})
			},
		},
		{
			name: "unexpected check ID",
			mutate: func(report *Report, _ *Expectations) {
				report.Checks[0].ID = "not-required"
			},
		},
		{
			name: "missing artifact",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Artifacts = report.Provenance.Artifacts[1:]
			},
		},
		{
			name: "duplicate artifact role",
			mutate: func(report *Report, _ *Expectations) {
				artifacts := report.Provenance.Artifacts
				artifacts[len(artifacts)-1].Role = artifacts[0].Role
			},
		},
		{
			name: "unexpected artifact role",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Artifacts[0].Role = "other"
			},
		},
		{
			name: "extra artifact role",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Artifacts = append(
					report.Provenance.Artifacts,
					ArtifactProvenance{
						Role: "other", Path: "/private/tmp/other",
						SHA256: digest(99), Bytes: 1,
					},
				)
			},
		},
		{
			name: "relative artifact path",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Artifacts[0].Path = "relative/artifact"
			},
		},
		{
			name: "duplicate artifact path",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Artifacts[1].Path = report.Provenance.Artifacts[0].Path
			},
		},
		{
			name: "packaged daemon outside App bundle",
			mutate: func(report *Report, _ *Expectations) {
				for index := range report.Provenance.Artifacts {
					if report.Provenance.Artifacts[index].Role == "daemon" {
						report.Provenance.Artifacts[index].Path = "/private/tmp/vibermated"
					}
				}
			},
		},
		{
			name: "malformed artifact digest",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Artifacts[0].SHA256 = "not-a-digest"
			},
		},
		{
			name: "uppercase artifact digest",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Artifacts[0].SHA256 = strings.Repeat("A", 64)
			},
		},
		{
			name: "well formed but false artifact digest",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Artifacts[0].SHA256 = digest(97)
			},
		},
		{
			name: "duplicate artifact digest",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Artifacts[1].SHA256 = report.Provenance.Artifacts[0].SHA256
			},
		},
		{
			name: "empty artifact",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Artifacts[0].Bytes = 0
			},
		},
		{
			name: "unbounded artifact size",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Artifacts[0].Bytes = maxArtifactSize + 1
			},
		},
		{
			name: "runtime Go toolchain drift",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Toolchains.Go = "go version go1.25.11 darwin/arm64"
			},
		},
		{
			name: "runtime Node toolchain drift",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Toolchains.Node = "v23.0.0"
			},
		},
		{
			name: "runtime Rust host drift",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Toolchains.Rustc = strings.ReplaceAll(
					report.Provenance.Toolchains.Rustc,
					ExpectedBuildTarget,
					"x86_64-apple-darwin",
				)
			},
		},
		{
			name: "build and runtime toolchain continuity drift",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Toolchains.Cargo =
					"cargo 1.88.0 (different-build 2026-01-01)"
			},
		},
		{
			name: "wrong build manifest schema",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Build.ManifestSchema = "vibermate.desktop-build/v3"
			},
		},
		{
			name: "wrong Desktop profile",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Build.DesktopProfile = "debug"
			},
		},
		{
			name: "wrong sidecar profile",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Build.SidecarProfile = "debug"
			},
		},
		{
			name: "wrong build target",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Build.Target = "x86_64-apple-darwin"
			},
		},
		{
			name: "Desktop Tauri toolchain drift",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Build.Toolchains.Tauri = "tauri-cli 2.12.0"
			},
		},
		{
			name: "missing Rust declaration digest",
			mutate: func(report *Report, _ *Expectations) {
				delete(
					report.Provenance.Build.ConfigurationSHA256,
					"rust-toolchain.toml",
				)
			},
		},
		{
			name: "extra configuration digest",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Build.ConfigurationSHA256["extra"] = digest(98)
			},
		},
		{
			name: "malformed configuration digest",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Build.ConfigurationSHA256["go.mod"] = "ABC"
			},
		},
		{
			name: "well formed but false configuration digest",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Build.ConfigurationSHA256["go.mod"] =
					digest(97)
			},
		},
		{
			name: "missing Go build version",
			mutate: func(report *Report, _ *Expectations) {
				delete(report.Provenance.Build.GoBuildVersions, "daemon")
			},
		},
		{
			name: "extra Go build version",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Build.GoBuildVersions["other"] = ExpectedGoVersion
			},
		},
		{
			name: "Go build version drift",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Build.GoBuildVersions["launcher"] = "go1.25.11"
			},
		},
		{
			name: "missing Go build tags",
			mutate: func(report *Report, _ *Expectations) {
				delete(report.Provenance.Build.GoBuildTags, "daemon")
			},
		},
		{
			name: "extra Go build tags",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Build.GoBuildTags["other"] = ""
			},
		},
		{
			name: "development sidecar has release tag",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Build.GoBuildTags["daemon"] = "vibermate_native_secrets"
			},
		},
		{
			name: "release sidecar lacks release tags",
			mutate: func(report *Report, _ *Expectations) {
				report.Provenance.Build.SidecarProfile = "release"
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			report, expected := validFixture(t, "claude-code", "2.1.220")
			test.mutate(&report, &expected)
			path := writeFixture(t, report)
			if err := VerifyFile(path, expected); err == nil {
				t.Fatal("VerifyFile() accepted mutated report")
			}
		})
	}
}

func TestVerifyFileRejectsUnknownDuplicateMalformedAndTrailingJSON(t *testing.T) {
	t.Parallel()
	report, expected := validFixture(t, "claude-code", "2.1.220")
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{
			name: "unknown top-level field",
			mutate: func(input []byte) []byte {
				return addUnknownJSONField(t, input, nil)
			},
		},
		{
			name: "unknown nested field",
			mutate: func(input []byte) []byte {
				return addUnknownJSONField(t, input, []string{"client", "adapter"})
			},
		},
		{
			name: "duplicate JSON field",
			mutate: func(input []byte) []byte {
				return bytes.Replace(
					input,
					[]byte(`"schema":`),
					[]byte(`"schema":"`+SchemaV6+`","schema":`),
					1,
				)
			},
		},
		{
			name: "malformed JSON",
			mutate: func(input []byte) []byte {
				return input[:len(input)-1]
			},
		},
		{
			name: "trailing JSON",
			mutate: func(input []byte) []byte {
				return append(input, []byte(`{}`)...)
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := writePayload(t, test.mutate(append([]byte(nil), payload...)), 0o600)
			if err := VerifyFile(path, expected); err == nil {
				t.Fatal("VerifyFile() accepted malformed report JSON")
			}
		})
	}
}

func TestVerifyFileRejectsUnsafeReportFiles(t *testing.T) {
	t.Parallel()
	report, expected := validFixture(t, "claude-code", "2.1.220")
	goodPath := writeFixture(t, report)
	goodPayload, err := os.ReadFile(goodPath)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.json")
		if err := VerifyFile(path, expected); err == nil {
			t.Fatal("VerifyFile() accepted missing report")
		}
	})
	t.Run("relative path", func(t *testing.T) {
		if err := VerifyFile("report.json", expected); err == nil {
			t.Fatal("VerifyFile() accepted relative report path")
		}
	})
	t.Run("unclean path", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "child", "..", "report.json")
		if err := VerifyFile(path, expected); err == nil {
			t.Fatal("VerifyFile() accepted unclean report path")
		}
	})
	t.Run("public permissions", func(t *testing.T) {
		path := writePayload(t, goodPayload, 0o644)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := VerifyFile(path, expected); err == nil {
			t.Fatal("VerifyFile() accepted public report permissions")
		}
	})
	t.Run("directory", func(t *testing.T) {
		if err := VerifyFile(t.TempDir(), expected); err == nil {
			t.Fatal("VerifyFile() accepted a directory")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "report-link.json")
		if err := os.Symlink(goodPath, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err := VerifyFile(link, expected); err == nil {
			t.Fatal("VerifyFile() accepted a symlink")
		}
	})
	t.Run("empty", func(t *testing.T) {
		path := writePayload(t, nil, 0o600)
		if err := VerifyFile(path, expected); err == nil {
			t.Fatal("VerifyFile() accepted an empty report")
		}
	})
	t.Run("oversized", func(t *testing.T) {
		path := writePayload(t, make([]byte, MaxReportBytes+1), 0o600)
		if err := VerifyFile(path, expected); err == nil {
			t.Fatal("VerifyFile() accepted an oversized report")
		}
	})
}

func TestVerifyFileIsSafeForConcurrentGateCalls(t *testing.T) {
	report, expected := validFixture(t, "claude-code", "2.1.220")
	path := writeFixture(t, report)
	const goroutines = 24
	const iterations = 20
	errorsSeen := make(chan error, goroutines)
	var wait sync.WaitGroup
	for worker := 0; worker < goroutines; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				if err := VerifyFile(path, expected); err != nil {
					errorsSeen <- err
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatalf("concurrent VerifyFile() error = %v", err)
	}
}

func TestRequiredCheckIDsReturnsAnIsolatedCopy(t *testing.T) {
	first, err := RequiredCheckIDs("claude-code", "2.1.220")
	if err != nil {
		t.Fatal(err)
	}
	first[0] = "mutated"
	second, err := RequiredCheckIDs("claude-code", "2.1.220")
	if err != nil {
		t.Fatal(err)
	}
	if second[0] != "build-provenance" {
		t.Fatalf("RequiredCheckIDs() leaked mutation: %q", second[0])
	}
	foundNavigationRestore := false
	for _, id := range second {
		if id == "packaged-main-navigation-cold-restore" {
			foundNavigationRestore = true
		}
	}
	if !foundNavigationRestore {
		t.Fatal("RequiredCheckIDs() omitted the current navigation restore proof")
	}
}

func validFixture(
	t *testing.T,
	clientID, clientVersion string,
) (Report, Expectations) {
	t.Helper()
	evidence, ok := clientadapter.BuiltInCatalog().ExpectedEvidence(
		clientID,
		clientVersion,
	)
	if !ok {
		t.Fatal("fixed fixture client is missing")
	}
	checkIDs, err := RequiredCheckIDs(clientID, clientVersion)
	if err != nil {
		t.Fatal(err)
	}
	checks := make([]Check, len(checkIDs))
	for index, id := range checkIDs {
		checks[index] = Check{ID: id, Status: StatusPassed, Detail: "passed"}
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
		writeExecutableFixture(t, path, "fixture executable "+role+"\n")
	}
	for _, role := range []string{"acceptance", "client-entrypoint"} {
		canonical, err := filepath.EvalSymlinks(artifactPaths[role])
		if err != nil {
			t.Fatal(err)
		}
		artifactPaths[role] = canonical
	}
	configurationDigests := make(map[string]string, len(requiredConfigurationDigests))
	for _, name := range requiredConfigurationDigests {
		path := filepath.Join(sourceRoot, filepath.FromSlash(name))
		writeRegularFixture(t, path, "fixture configuration "+name+"\n", 0o644)
		evidence, err := DigestArtifact("configuration "+name, path)
		if err != nil {
			t.Fatal(err)
		}
		configurationDigests[name] = evidence.SHA256
	}
	revision, commitTime := initializeGitFixture(t, sourceRoot)
	runtimeToolchains := ToolchainProvenance{
		Go:    "go version go1.25.12 darwin/arm64",
		Node:  ExpectedNodeVersion,
		Rustc: "rustc 1.88.0 (fixture 2026-01-01)\nbinary: rustc\nhost: aarch64-apple-darwin\nrelease: 1.88.0",
		Cargo: "cargo 1.88.0 (fixture 2026-01-01)",
		PNPM:  ExpectedPNPMVersion,
	}
	source := SourceProvenance{
		VCS:        "git",
		Revision:   revision,
		CommitTime: commitTime,
		Dirty:      false,
	}
	buildTools := DesktopBuildToolchains{
		Go: runtimeToolchains.Go, Node: runtimeToolchains.Node,
		Rustc: runtimeToolchains.Rustc, Cargo: runtimeToolchains.Cargo,
		PNPM: runtimeToolchains.PNPM, Tauri: ExpectedTauriVersion,
	}
	daemon, err := DigestArtifact("daemon", artifactPaths["daemon"])
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := DigestArtifact("launcher", artifactPaths["launcher"])
	if err != nil {
		t.Fatal(err)
	}
	manifest := desktopBuildManifest{
		Schema: DesktopBuildManifestSchemaV2,
		Source: source,
		Profiles: desktopBuildProfiles{
			Desktop: "release", Sidecars: "development",
			Target: ExpectedBuildTarget,
		},
		Toolchains:          buildTools,
		ConfigurationSHA256: configurationDigests,
		SidecarSHA256: map[string]string{
			"vibermated": daemon.SHA256,
			"vibermate":  launcher.SHA256,
		},
	}
	manifestPayload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeRegularFixture(
		t,
		artifactPaths["desktop-build-manifest"],
		string(append(manifestPayload, '\n')),
		0o644,
	)
	artifacts := make([]ArtifactProvenance, len(requiredArtifactRoles))
	for index, role := range requiredArtifactRoles {
		var evidence ArtifactProvenance
		if role == "desktop-app-bundle" {
			evidence, err = DigestBundle(bundle)
		} else {
			evidence, err = DigestArtifact(role, artifactPaths[role])
		}
		if err != nil {
			t.Fatal(err)
		}
		artifacts[index] = evidence
	}
	report := Report{
		Schema:       SchemaV6,
		StartedAt:    time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC),
		FinishedAt:   time.Date(2026, 8, 2, 10, 1, 0, 0, time.UTC),
		Platform:     ExpectedPlatform,
		Architecture: ExpectedArchitecture,
		Client: Client{
			ID: clientID, Version: clientVersion, Adapter: &evidence,
		},
		Provenance: &Provenance{
			Source:     source,
			Artifacts:  artifacts,
			Toolchains: runtimeToolchains,
			Build: BuildProvenance{
				ManifestSchema:      DesktopBuildManifestSchema,
				DesktopProfile:      "release",
				SidecarProfile:      "development",
				Target:              ExpectedBuildTarget,
				Toolchains:          buildTools,
				ConfigurationSHA256: configurationDigests,
				GoBuildVersions: map[string]string{
					"acceptance": ExpectedGoVersion,
					"daemon":     ExpectedGoVersion,
					"launcher":   ExpectedGoVersion,
				},
				GoBuildTags: map[string]string{
					"acceptance": "",
					"daemon":     "",
					"launcher":   "",
				},
			},
			Configuration: Configuration{
				DeterministicOnly: true,
				ClientID:          clientID,
				ClientVersion:     clientVersion,
				AccessID:          "assembly-001",
				ProviderOrigin:    "http://127.0.0.1:23333/v1",
				ProviderModel:     "dashscope:glm-5",
				Timeout:           "8m0s",
			},
		},
		Status: StatusPassed,
		Checks: checks,
	}
	return report, Expectations{
		Schema: SchemaV6, Revision: revision,
		ClientID: clientID, ClientVersion: clientVersion,
		Artifacts: ArtifactCoordinates{
			SourceRoot:           sourceRoot,
			DesktopApp:           bundle,
			AcceptanceExecutable: artifactPaths["acceptance"],
			ClientEntrypoint:     artifactPaths["client-entrypoint"],
		},
	}
}

func initializeGitFixture(t *testing.T, root string) (string, string) {
	t.Helper()
	runGitFixture(t, root, "init", "-q")
	runGitFixture(t, root, "add", "--all")
	commitGitFixture(t, root, "fixture", "2026-08-02T09:59:00Z")
	revision := runGitFixture(t, root, "rev-parse", "HEAD")
	commitTime := runGitFixture(t, root, "show", "-s", "--format=%cI", "HEAD")
	return revision, commitTime
}

func commitGitFixture(t *testing.T, root, message, commitTime string) {
	t.Helper()
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
		message,
	)
	command.Env = append(
		os.Environ(),
		"GIT_AUTHOR_DATE="+commitTime,
		"GIT_COMMITTER_DATE="+commitTime,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("commit Git fixture: %v: %s", err, output)
	}
}

func runGitFixture(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	commandArguments := append([]string{"-C", root}, arguments...)
	command := exec.Command("git", commandArguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeExecutableFixture(t *testing.T, path, content string) {
	t.Helper()
	writeRegularFixture(t, path, content, 0o755)
}

func writeRegularFixture(
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

func writeFixture(t *testing.T, report Report) string {
	t.Helper()
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return writePayload(t, append(payload, '\n'), 0o600)
}

func writePayload(t *testing.T, payload []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "acceptance-report.json")
	if err := os.WriteFile(path, payload, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func addUnknownJSONField(
	t *testing.T,
	payload []byte,
	path []string,
) []byte {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		t.Fatal(err)
	}
	current := root
	for _, name := range path {
		next, ok := current[name].(map[string]any)
		if !ok {
			t.Fatalf("fixture JSON path %q is not an object", name)
		}
		current = next
	}
	current["unexpected"] = true
	mutated, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return mutated
}

func digest(index int) string {
	return fmt.Sprintf("%064x", index)
}
