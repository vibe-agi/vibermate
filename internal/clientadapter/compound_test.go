package clientadapter_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

func TestVerifierRequiresCompleteCompoundReleaseAndFreezesCatalogEvidence(
	t *testing.T,
) {
	t.Parallel()

	fixture := newCompoundFixture(t)
	catalog := compoundCatalog(t, fixture)
	verifier, err := clientadapter.NewReleaseVerifier(catalog)
	if err != nil {
		t.Fatal(err)
	}
	detection, err := verifier.Verify(
		context.Background(),
		clientadapter.Request{
			Command:        []string{"codex", "exec", "hello"},
			CWD:            fixture.root,
			ExecutablePath: fixture.launcher,
		},
	)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if detection.Status != clientadapter.StatusVerified ||
		detection.CatalogRevision != 7 ||
		detection.Evidence == nil ||
		detection.Evidence.ID != "codex-cli" ||
		detection.Evidence.Revision != 3 ||
		detection.Evidence.Version != "0.145.0" ||
		detection.Evidence.CatalogRevision != 7 ||
		detection.Evidence.InstallShape !=
			clientadapter.InstallNPMWrapperNativeChild ||
		detection.Evidence.LaunchRecipe != clientadapter.LaunchSSLCertFile ||
		detection.Evidence.ReleaseSHA256 == "" ||
		!detection.Evidence.Supports(
			clientadapter.FeatureResponsesWebSocketHTTPFallback,
		) {
		t.Fatalf("verified detection = %+v", detection)
	}
	if _, err := os.Stat(fixture.executionMarker); !os.IsNotExist(err) {
		t.Fatalf("verification executed the wrapper: %v", err)
	}
	detection.Evidence.Version = "output-mutated"
	repeated, err := verifier.Verify(
		context.Background(),
		clientadapter.Request{
			Command:        []string{"codex"},
			CWD:            fixture.root,
			ExecutablePath: fixture.launcher,
		},
	)
	if err != nil ||
		repeated.Evidence == nil ||
		repeated.Evidence.Version != "0.145.0" {
		t.Fatalf(
			"evidence output alias changed verifier state: detection=%+v err=%v",
			repeated,
			err,
		)
	}

	catalogInput := fixture.release()
	mutableArtifacts := catalogInput.Artifacts
	immutableCatalog, err := clientadapter.NewCatalog(
		7,
		[]clientadapter.Release{catalogInput},
	)
	if err != nil {
		t.Fatal(err)
	}
	mutableArtifacts[0].SHA256 = digest([]byte("caller mutation"))
	immutableVerifier, err := clientadapter.NewReleaseVerifier(immutableCatalog)
	if err != nil {
		t.Fatal(err)
	}
	immutableDetection, err := immutableVerifier.Verify(
		context.Background(),
		clientadapter.Request{
			Command:        []string{"codex"},
			CWD:            fixture.root,
			ExecutablePath: fixture.launcher,
		},
	)
	if err != nil ||
		immutableDetection.Status != clientadapter.StatusVerified {
		t.Fatalf(
			"catalog input alias changed verification: detection=%+v err=%v",
			immutableDetection,
			err,
		)
	}
}

func TestVerifierFallsBackToGenericWhenAnyCompoundArtifactChanges(
	t *testing.T,
) {
	t.Parallel()

	for _, artifact := range []string{
		"entrypoint",
		"main-package",
		"platform-package",
		"native-child",
	} {
		artifact := artifact
		t.Run(artifact, func(t *testing.T) {
			t.Parallel()

			fixture := newCompoundFixture(t)
			verifier, err := clientadapter.NewReleaseVerifier(
				compoundCatalog(t, fixture),
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				fixture.paths[artifact],
				[]byte("changed"),
				0o700,
			); err != nil {
				t.Fatal(err)
			}
			detection, err := verifier.Verify(
				context.Background(),
				clientadapter.Request{
					Command:        []string{"codex"},
					CWD:            fixture.root,
					ExecutablePath: fixture.launcher,
				},
			)
			if err != nil {
				t.Fatalf("Verify() error = %v", err)
			}
			if detection.Status != clientadapter.StatusGeneric ||
				detection.CatalogRevision != 7 ||
				detection.Evidence != nil {
				t.Fatalf("mismatch detection = %+v", detection)
			}
		})
	}
}

func TestVerifierCancellationFailsClosedDuringArtifactVerification(
	t *testing.T,
) {
	t.Parallel()

	fixture := newCompoundFixture(t)
	verifier, err := clientadapter.NewReleaseVerifier(
		compoundCatalog(t, fixture),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	detection, err := verifier.Verify(
		ctx,
		clientadapter.Request{
			Command:        []string{"codex"},
			CWD:            fixture.root,
			ExecutablePath: fixture.launcher,
		},
	)
	if !errors.Is(err, context.Canceled) ||
		detection.Status != clientadapter.StatusFailed ||
		detection.CatalogRevision != 7 ||
		detection.Evidence != nil {
		t.Fatalf(
			"cancelled detection = %+v, error = %v",
			detection,
			err,
		)
	}
}

func TestVerifierRejectsAmbiguousCompoundMatchesAndPathEscapes(
	t *testing.T,
) {
	t.Parallel()

	t.Run("ambiguous", func(t *testing.T) {
		t.Parallel()

		fixture := newCompoundFixture(t)
		full := fixture.release()
		narrow := full
		narrow.ID = "wrongly-overlapping-client"
		narrow.Revision = 1
		narrow.InstallShape = clientadapter.InstallNativeSingleBinary
		narrow.ArtifactRoot = "."
		narrow.Artifacts = []clientadapter.Artifact{full.Artifacts[0]}
		narrow.Features = 0
		catalog, err := clientadapter.NewCatalog(
			7,
			[]clientadapter.Release{full, narrow},
		)
		if err != nil {
			t.Fatal(err)
		}
		verifier, err := clientadapter.NewReleaseVerifier(catalog)
		if err != nil {
			t.Fatal(err)
		}
		detection, err := verifier.Verify(
			context.Background(),
			clientadapter.Request{
				Command:        []string{"codex"},
				CWD:            fixture.root,
				ExecutablePath: fixture.launcher,
			},
		)
		if err == nil || detection.Status != clientadapter.StatusFailed {
			t.Fatalf(
				"ambiguous detection = %+v, error = %v",
				detection,
				err,
			)
		}
	})

	t.Run("artifact symlink escape", func(t *testing.T) {
		t.Parallel()

		fixture := newCompoundFixture(t)
		outside := filepath.Join(t.TempDir(), "package.json")
		if err := os.WriteFile(
			outside,
			[]byte(`{"version":"0.145.0"}`),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(fixture.paths["main-package"]); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(
			outside,
			fixture.paths["main-package"],
		); err != nil {
			t.Fatal(err)
		}
		release := fixture.release()
		release.Artifacts[1].SHA256 = digest(
			[]byte(`{"version":"0.145.0"}`),
		)
		catalog, err := clientadapter.NewCatalog(
			7,
			[]clientadapter.Release{release},
		)
		if err != nil {
			t.Fatal(err)
		}
		verifier, err := clientadapter.NewReleaseVerifier(catalog)
		if err != nil {
			t.Fatal(err)
		}
		detection, err := verifier.Verify(
			context.Background(),
			clientadapter.Request{
				Command:        []string{"codex"},
				CWD:            fixture.root,
				ExecutablePath: fixture.launcher,
			},
		)
		if err == nil || detection.Status != clientadapter.StatusFailed {
			t.Fatalf(
				"escaped detection = %+v, error = %v",
				detection,
				err,
			)
		}
	})

	t.Run("artifact path conflict", func(t *testing.T) {
		t.Parallel()

		fixture := newCompoundFixture(t)
		if err := os.Remove(fixture.paths["platform-package"]); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(
			fixture.paths["main-package"],
			fixture.paths["platform-package"],
		); err != nil {
			t.Fatal(err)
		}
		release := fixture.release()
		release.Artifacts[2].SHA256 = release.Artifacts[1].SHA256
		catalog, err := clientadapter.NewCatalog(
			7,
			[]clientadapter.Release{release},
		)
		if err != nil {
			t.Fatal(err)
		}
		verifier, err := clientadapter.NewReleaseVerifier(catalog)
		if err != nil {
			t.Fatal(err)
		}
		detection, err := verifier.Verify(
			context.Background(),
			clientadapter.Request{
				Command:        []string{"codex"},
				CWD:            fixture.root,
				ExecutablePath: fixture.launcher,
			},
		)
		if err == nil || detection.Status != clientadapter.StatusFailed {
			t.Fatalf(
				"conflicting detection = %+v, error = %v",
				detection,
				err,
			)
		}
	})
}

func TestBuiltInCatalogMatchesExternalFixedCodexInstallation(t *testing.T) {
	entrypoint := os.Getenv("VIBERMATE_FIXED_CODEX_ENTRYPOINT")
	if entrypoint == "" {
		t.Skip("fixed Codex installation was not supplied")
	}
	entrypoint, err := filepath.Abs(entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := clientadapter.NewReleaseVerifier(
		clientadapter.BuiltInCatalog(),
	)
	if err != nil {
		t.Fatal(err)
	}
	detection, err := verifier.Verify(
		context.Background(),
		clientadapter.Request{
			Command:        []string{"codex"},
			CWD:            filepath.Dir(entrypoint),
			ExecutablePath: entrypoint,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if detection.Status != clientadapter.StatusVerified ||
		detection.Evidence == nil ||
		detection.Evidence.ID != "codex-cli" ||
		detection.Evidence.Version != "0.145.0" ||
		detection.Evidence.CatalogRevision != 1 {
		t.Fatalf("external fixed Codex detection = %+v", detection)
	}
}

func TestBuiltInCatalogLeavesExternalUnknownCodexGeneric(t *testing.T) {
	entrypoint := os.Getenv("VIBERMATE_UNKNOWN_CODEX_ENTRYPOINT")
	if entrypoint == "" {
		t.Skip("unknown Codex installation was not supplied")
	}
	entrypoint, err := filepath.Abs(entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := clientadapter.NewReleaseVerifier(
		clientadapter.BuiltInCatalog(),
	)
	if err != nil {
		t.Fatal(err)
	}
	detection, err := verifier.Verify(
		context.Background(),
		clientadapter.Request{
			Command:        []string{"codex"},
			CWD:            filepath.Dir(entrypoint),
			ExecutablePath: entrypoint,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if detection.Status != clientadapter.StatusGeneric ||
		detection.CatalogRevision != 1 ||
		detection.Evidence != nil {
		t.Fatalf("external unknown Codex detection = %+v", detection)
	}
}

type compoundFixture struct {
	root            string
	launcher        string
	executionMarker string
	contents        map[string][]byte
	paths           map[string]string
}

func newCompoundFixture(t *testing.T) compoundFixture {
	t.Helper()

	root := t.TempDir()
	packageRoot := filepath.Join(root, "lib", "node_modules", "@openai", "codex")
	entrypoint := filepath.Join(packageRoot, "bin", "codex.js")
	platformRoot := filepath.Join(
		packageRoot,
		"node_modules",
		"@openai",
		"codex-darwin-arm64",
	)
	native := filepath.Join(
		platformRoot,
		"vendor",
		"aarch64-apple-darwin",
		"bin",
		"codex",
	)
	for _, directory := range []string{
		filepath.Dir(entrypoint),
		filepath.Dir(native),
		filepath.Join(root, "bin"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(root, "wrapper-executed")
	contents := map[string][]byte{
		"entrypoint": []byte(
			"#!/bin/sh\nprintf executed > '" + marker + "'\n",
		),
		"main-package":     []byte(`{"name":"@openai/codex","version":"0.145.0"}`),
		"platform-package": []byte(`{"version":"0.145.0-darwin-arm64"}`),
		"native-child":     []byte("fixed native child"),
	}
	paths := map[string]string{
		"entrypoint":       entrypoint,
		"main-package":     filepath.Join(packageRoot, "package.json"),
		"platform-package": filepath.Join(platformRoot, "package.json"),
		"native-child":     native,
	}
	for name, path := range paths {
		mode := os.FileMode(0o600)
		if name == "entrypoint" || name == "native-child" {
			mode = 0o700
		}
		if err := os.WriteFile(path, contents[name], mode); err != nil {
			t.Fatal(err)
		}
	}
	launcher := filepath.Join(root, "bin", "codex")
	if err := os.Symlink(entrypoint, launcher); err != nil {
		t.Fatal(err)
	}
	return compoundFixture{
		root:            root,
		launcher:        launcher,
		executionMarker: marker,
		contents:        contents,
		paths:           paths,
	}
}

func (fixture compoundFixture) release() clientadapter.Release {
	return clientadapter.Release{
		ID:                      "codex-cli",
		Revision:                3,
		Version:                 "0.145.0",
		OperatingSystem:         runtime.GOOS,
		Architecture:            runtime.GOARCH,
		InstallShape:            clientadapter.InstallNPMWrapperNativeChild,
		InvocationLabel:         "codex",
		CanonicalEntrypointName: "codex.js",
		ArtifactRoot:            "..",
		Artifacts: []clientadapter.Artifact{
			{
				Role:   clientadapter.ArtifactEntrypoint,
				SHA256: digest(fixture.contents["entrypoint"]),
			},
			{
				Role:         clientadapter.ArtifactMainPackageMetadata,
				RelativePath: "../package.json",
				SHA256:       digest(fixture.contents["main-package"]),
			},
			{
				Role:         clientadapter.ArtifactPlatformPackageMetadata,
				RelativePath: "../node_modules/@openai/codex-darwin-arm64/package.json",
				SHA256:       digest(fixture.contents["platform-package"]),
			},
			{
				Role:         clientadapter.ArtifactNativeChild,
				RelativePath: "../node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex",
				SHA256:       digest(fixture.contents["native-child"]),
			},
		},
		LaunchRecipe: clientadapter.LaunchSSLCertFile,
		Features:     clientadapter.FeatureResponsesWebSocketHTTPFallback,
	}
}

func compoundCatalog(
	t *testing.T,
	fixture compoundFixture,
) clientadapter.Catalog {
	t.Helper()

	catalog, err := clientadapter.NewCatalog(
		7,
		[]clientadapter.Release{fixture.release()},
	)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
