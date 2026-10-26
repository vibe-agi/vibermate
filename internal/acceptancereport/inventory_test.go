package acceptancereport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProducerInventoryRemainsClosedAtIndependentVerifier(t *testing.T) {
	t.Parallel()
	report, expected := validFixture(t, "claude-code", "2.1.220")
	if err := VerifyFile(writeFixture(t, report), expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(requiredGoBinaries, []string{"acceptance", "daemon", "launcher"}) {
		t.Fatal("separately locked Xray upstream must not acquire the product Git source identity")
	}
	paths, err := expectedArtifactPaths(expected.Artifacts)
	if err != nil {
		t.Fatal(err)
	}
	inventory := producerInventoryFixture(t)
	for name, relative := range inventory.Nested {
		role := name
		if name == "vibermate" {
			role = "launcher"
		}
		if name == "vibermated" {
			role = "daemon"
		}
		if paths[role] != filepath.Join(expected.Artifacts.DesktopApp, relative) {
			t.Fatalf("producer path %s is not independently bound", name)
		}
	}
	for name, digest := range report.Provenance.Build.ConfigurationSHA256 {
		delete(report.Provenance.Build.ConfigurationSHA256, name)
		if err := verifyBuild(report.Provenance.Build); err == nil {
			t.Fatalf("missing configuration %s accepted", name)
		}
		report.Provenance.Build.ConfigurationSHA256[name] = digest
	}
	manifest, err := readDesktopBuildManifest(paths["desktop-build-manifest"])
	if err != nil {
		t.Fatal(err)
	}
	artifacts := make(map[string]ArtifactProvenance)
	for _, item := range report.Provenance.Artifacts {
		artifacts[item.Role] = item
	}
	verify := func() error {
		return verifyDesktopBuildManifest(manifest, report.Provenance, artifacts, expected.Revision)
	}
	for name, digest := range manifest.NestedCodeSHA256 {
		delete(manifest.NestedCodeSHA256, name)
		if err := verify(); err == nil {
			t.Fatalf("missing nested member %s accepted", name)
		}
		manifest.NestedCodeSHA256[name] = strings.Repeat("f", 64)
		if err := verify(); err == nil {
			t.Fatalf("tampered nested member %s accepted", name)
		}
		manifest.NestedCodeSHA256[name] = digest
	}
	manifest.NestedCodeSHA256["extra"] = strings.Repeat("f", 64)
	if err := verify(); err == nil {
		t.Fatal("extra nested member accepted")
	}
}

func TestVerifyFileRejectsKernelEvidenceDrift(t *testing.T) {
	t.Parallel()
	verifyNestedExecutableDrift(t, "vibermate-xray", "Contents/MacOS/vibermate-xray")
}

func TestVerifyFileRejectsTransparentProxyEvidenceDrift(t *testing.T) {
	t.Parallel()
	verifyNestedExecutableDrift(t, "transparent-proxy",
		"Contents/Library/SystemExtensions/io.vibermate.desktop.transparent-proxy.systemextension/Contents/MacOS/io.vibermate.desktop.transparent-proxy")
}

func verifyNestedExecutableDrift(t *testing.T, role, relative string) {
	t.Helper()
	for _, mutation := range []string{"missing role", "wrong path", "changed bytes", "missing executable", "symbolic executable", "nonexecutable", "manifest digest", "extra nested member"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			report, expected := validFixture(t, "claude-code", "2.1.220")
			kernel := filepath.Join(expected.Artifacts.DesktopApp, filepath.FromSlash(relative))
			for index, artifact := range report.Provenance.Artifacts {
				if artifact.Role != role {
					continue
				}
				switch mutation {
				case "missing role":
					report.Provenance.Artifacts = append(report.Provenance.Artifacts[:index], report.Provenance.Artifacts[index+1:]...)
				case "wrong path":
					report.Provenance.Artifacts[index].Path = filepath.Join(t.TempDir(), filepath.Base(kernel))
				}
				break
			}
			switch mutation {
			case "changed bytes":
				writeExecutableFixture(t, kernel, "tampered executable")
			case "missing executable", "symbolic executable":
				if err := os.Remove(kernel); err != nil {
					t.Fatal(err)
				}
				if mutation == "symbolic executable" {
					if err := os.Symlink(expected.Artifacts.ClientEntrypoint, kernel); err != nil {
						t.Fatal(err)
					}
				}
			case "nonexecutable":
				if err := os.Chmod(kernel, 0o644); err != nil {
					t.Fatal(err)
				}
			case "manifest digest", "extra nested member":
				path := filepath.Join(expected.Artifacts.DesktopApp, "Contents", "Resources", "vibermate-build-manifest.json")
				manifest, err := readDesktopBuildManifest(path)
				if err != nil {
					t.Fatal(err)
				}
				key := role
				if mutation == "extra nested member" {
					key = "extra"
				}
				manifest.NestedCodeSHA256[key] = strings.Repeat("f", 64)
				payload, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				writeRegularFixture(t, path, string(payload), 0o644)
				// Rebind enclosing artifacts so only the nested-code contract can reject it.
				for index, artifact := range report.Provenance.Artifacts {
					if artifact.Role == "desktop-build-manifest" {
						updated, err := DigestArtifact(artifact.Role, path)
						if err != nil {
							t.Fatal(err)
						}
						report.Provenance.Artifacts[index] = updated
					}
				}
			}
			// Rebind the outer tree: a bundle-only check cannot satisfy this regression.
			for index, artifact := range report.Provenance.Artifacts {
				if artifact.Role != "desktop-app-bundle" {
					continue
				}
				updated, err := DigestBundle(expected.Artifacts.DesktopApp)
				if err != nil {
					t.Fatal(err)
				}
				updated.Role = artifact.Role
				report.Provenance.Artifacts[index] = updated
			}
			if err := VerifyFile(writeFixture(t, report), expected); err == nil {
				t.Fatalf("accepted %s", mutation)
			}
		})
	}
}

func TestVerifyFileRehashesKernelPodAndTransparentProxyConfiguration(t *testing.T) {
	t.Parallel()
	for _, name := range producerInventoryFixture(t).Configuration {
		if !strings.HasPrefix(name, "tool/vless-kernel/") && !strings.Contains(name, "/Podfile") &&
			!strings.Contains(name, "/TransparentProxy/") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			report, expected := validFixture(t, "claude-code", "2.1.220")
			runGitFixture(t, expected.Artifacts.SourceRoot, "update-index", "--assume-unchanged", name)
			writeRegularFixture(t, filepath.Join(expected.Artifacts.SourceRoot, name), "changed configuration", 0o644)
			err := VerifyFile(writeFixture(t, report), expected)
			if err == nil || !strings.Contains(err.Error(), "configuration differs from disk: "+name) {
				t.Fatalf("changed configuration error = %v", err)
			}
		})
	}
}
