package releasemanifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyArtifacts(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	if err := VerifyArtifacts(root, manifest); err != nil {
		t.Fatalf("VerifyArtifacts() error = %v", err)
	}
}

func TestVerifyArtifactsRejectsChangedContent(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	if err := os.WriteFile(filepath.Join(root, "known-issues.json"), []byte("changed-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsRejectsChangedSize(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	if err := os.WriteFile(filepath.Join(root, "known-issues.json"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsRejectsNonRegularFile(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	artifactPath := filepath.Join(root, "known-issues.json")
	if err := os.Remove(artifactPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(artifactPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsRejectsSymlink(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	target := filepath.Join(t.TempDir(), "replacement")
	if err := os.WriteFile(target, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(root, "known-issues.json")
	if err := os.Remove(artifactPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, artifactPath); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsRejectsSymlinkDirectory(t *testing.T) {
	root := artifactTempDir(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "ledger.json"), []byte("ledger"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	manifest := validManifest()
	digest := sha256.Sum256([]byte("ledger"))
	manifest.Artifacts[0].Path = "linked/ledger.json"
	manifest.Artifacts[0].Size = int64(len("ledger"))
	manifest.Artifacts[0].SHA256 = hex.EncodeToString(digest[:])
	for index := 1; index < len(manifest.Artifacts); index++ {
		artifact := &manifest.Artifacts[index]
		data := []byte(artifact.Role)
		if err := os.WriteFile(filepath.Join(root, artifact.Path), data, 0o600); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		artifact.Size = int64(len(data))
		artifact.SHA256 = hex.EncodeToString(hash[:])
	}
	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsRejectsArbitraryMetadataForEveryRole(t *testing.T) {
	roles := []string{
		ArtifactRoleAppTreeLedger,
		ArtifactRoleDesktopBuildManifest,
		ArtifactRoleSBOM,
		ArtifactRoleKnownIssues,
	}
	for _, role := range roles {
		t.Run(role, func(t *testing.T) {
			root := artifactTempDir(t)
			manifest := manifestWithFiles(t, root)
			writeDeclaredArtifact(
				t,
				root,
				artifactForRole(t, &manifest, role),
				[]byte("arbitrary text that has a matching declared digest\n"),
			)
			if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
				t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
			}
		})
	}
}

func TestVerifyArtifactsRejectsDesktopBuildSemanticDrift(t *testing.T) {
	tests := map[string]func(map[string]any){
		"commit binding": func(document map[string]any) {
			document["source"].(map[string]any)["revision"] = strings.Repeat("b", 40)
		},
		"dirty source": func(document map[string]any) {
			document["source"].(map[string]any)["dirty"] = true
		},
		"distribution profile": func(document map[string]any) {
			document["profiles"].(map[string]any)["sidecars"] = "distribution"
		},
		"configuration key set": func(document map[string]any) {
			delete(document["configurationSHA256"].(map[string]any), "ui/flutter_app/pubspec.yaml")
		},
		"nested code digest": func(document map[string]any) {
			document["nestedCodeSHA256"].(map[string]any)["app-framework"] = strings.Repeat("A", 64)
		},
		"closed nested source": func(document map[string]any) {
			document["source"].(map[string]any)["unexpected"] = true
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root := artifactTempDir(t)
			manifest := manifestWithFiles(t, root)
			document := readArtifactObject(t, root, &manifest, ArtifactRoleDesktopBuildManifest)
			mutate(document)
			rewriteDesktopArtifactAndLedger(t, root, &manifest, marshalArtifactJSON(t, document))
			if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
				t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
			}
		})
	}
}

func TestVerifyArtifactsRejectsSPDXSemanticDrift(t *testing.T) {
	tests := map[string]func(map[string]any){
		"candidate binding": func(document map[string]any) {
			document["comment"] = "vibermate.release version=9.9.9 commit=" + strings.Repeat("b", 40)
		},
		"minimum package shape": func(document map[string]any) {
			delete(document["packages"].([]any)[0].(map[string]any), "downloadLocation")
		},
		"closed document": func(document map[string]any) {
			document["notSPDX"] = true
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root := artifactTempDir(t)
			manifest := manifestWithFiles(t, root)
			document := readArtifactObject(t, root, &manifest, ArtifactRoleSBOM)
			mutate(document)
			writeDeclaredArtifact(
				t,
				root,
				artifactForRole(t, &manifest, ArtifactRoleSBOM),
				marshalArtifactJSON(t, document),
			)
			if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
				t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
			}
		})
	}
}

func TestVerifyArtifactsRejectsKnownIssuesSemanticDrift(t *testing.T) {
	tests := map[string]func(map[string]any){
		"version binding": func(document map[string]any) {
			document["version"] = "9.9.9"
		},
		"commit binding": func(document map[string]any) {
			document["commit"] = strings.Repeat("b", 40)
		},
		"closed issue": func(document map[string]any) {
			document["issues"] = []any{map[string]any{
				"id": "issue-1", "summary": "fixture", "severity": "low", "unexpected": true,
			}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root := artifactTempDir(t)
			manifest := manifestWithFiles(t, root)
			document := readArtifactObject(t, root, &manifest, ArtifactRoleKnownIssues)
			mutate(document)
			writeDeclaredArtifact(
				t,
				root,
				artifactForRole(t, &manifest, ArtifactRoleKnownIssues),
				marshalArtifactJSON(t, document),
			)
			if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
				t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
			}
		})
	}
}

func TestVerifyArtifactsRejectsAppTreeLedgerSemanticDrift(t *testing.T) {
	tests := map[string]func(map[string]any){
		"payload root binding": func(document map[string]any) {
			document["root"] = "some-other-payload"
		},
		"commit binding": func(document map[string]any) {
			document["commit"] = strings.Repeat("b", 40)
		},
		"desktop manifest binding": func(document map[string]any) {
			document["desktopBuildManifestSHA256"] = strings.Repeat("c", 64)
		},
		"unclean path": func(document map[string]any) {
			ledgerEntryObject(t, document, "dist/index.html")["path"] = "dist/../escape"
		},
		"invalid digest": func(document map[string]any) {
			ledgerEntryObject(t, document, "dist/index.html")["sha256"] = strings.Repeat("A", 64)
		},
		"duplicate path": func(document map[string]any) {
			entries := document["entries"].([]any)
			document["entries"] = append(entries, entries[len(entries)-1])
		},
		"null file field on directory": func(document map[string]any) {
			document["entries"].([]any)[0].(map[string]any)["sha256"] = nil
		},
		"symbolic link": func(document map[string]any) {
			entry := ledgerEntryObject(t, document, "dist/index.html")
			entry["type"] = "symlink"
			entry["target"] = "../LICENSE"
			delete(entry, "sha256")
			delete(entry, "size")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root := artifactTempDir(t)
			manifest := manifestWithFiles(t, root)
			document := readArtifactObject(t, root, &manifest, ArtifactRoleAppTreeLedger)
			mutate(document)
			writeDeclaredArtifact(
				t,
				root,
				artifactForRole(t, &manifest, ArtifactRoleAppTreeLedger),
				marshalArtifactJSON(t, document),
			)
			if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
				t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
			}
		})
	}
}

func TestVerifyArtifactsRejectsFictitiousUnsignedPayloadLedger(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	if err := os.RemoveAll(filepath.Join(root, UnsignedPayloadRoot)); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsRejectsUnsignedPayloadExtraEntry(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	writePayloadFixtureFile(t, root, "dist/unledgered.js", []byte("extra\n"), 0o644)
	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsRejectsUnsignedPayloadMissingEntry(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	if err := os.Remove(filepath.Join(root, UnsignedPayloadRoot, "LICENSE")); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsRejectsUnsignedPayloadDeclaredButMissingEntry(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	ledger := readArtifactObject(t, root, &manifest, ArtifactRoleAppTreeLedger)
	ghostPayload := []byte("ghost\n")
	ledger["entries"] = append(ledger["entries"].([]any), map[string]any{
		"mode":   0o644,
		"path":   "dist/ghost.js",
		"type":   "file",
		"sha256": payloadSHA256(ghostPayload),
		"size":   len(ghostPayload),
	})
	writeDeclaredArtifact(
		t,
		root,
		artifactForRole(t, &manifest, ArtifactRoleAppTreeLedger),
		marshalArtifactJSON(t, ledger),
	)
	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsRejectsSwappedUnsignedPayloadSidecars(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	launcherPath := filepath.Join(root, UnsignedPayloadRoot, "vibermate")
	daemonPath := filepath.Join(root, UnsignedPayloadRoot, "vibermated")
	temporaryPath := filepath.Join(root, UnsignedPayloadRoot, "sidecar-swap")
	if err := os.Rename(launcherPath, temporaryPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(daemonPath, launcherPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporaryPath, daemonPath); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsRejectsUnsignedPayloadSymlink(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	payloadPath := filepath.Join(root, UnsignedPayloadRoot, "dist", "index.html")
	if err := os.Remove(payloadPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../LICENSE", payloadPath); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsRejectsUnsignedPayloadPrivilegedModeBits(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	payloadPath := filepath.Join(root, UnsignedPayloadRoot, "vibermate")
	if err := os.Chmod(payloadPath, 0o4755); err != nil {
		t.Skipf("set-user-ID mode is unavailable: %v", err)
	}
	info, err := os.Lstat(payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSetuid == 0 {
		t.Skip("filesystem did not retain the set-user-ID mode")
	}
	ledger := readArtifactObject(t, root, &manifest, ArtifactRoleAppTreeLedger)
	ledgerEntryObject(t, ledger, "vibermate")["mode"] = 0o4755
	writeDeclaredArtifact(
		t,
		root,
		artifactForRole(t, &manifest, ArtifactRoleAppTreeLedger),
		marshalArtifactJSON(t, ledger),
	)
	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsRejectsDesktopNestedCodeDigestNotBoundToPayload(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	desktop := readArtifactObject(t, root, &manifest, ArtifactRoleDesktopBuildManifest)
	desktop["nestedCodeSHA256"].(map[string]any)["app-framework"] = strings.Repeat("c", 64)
	rewriteDesktopArtifactAndLedger(t, root, &manifest, marshalArtifactJSON(t, desktop))
	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsRejectsEmbeddedDesktopManifestDifferentFromCoreArtifact(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	embeddedPayload := []byte("different embedded manifest\n")
	writePayloadFixtureFile(
		t,
		root,
		"vibermate-build-manifest.json",
		embeddedPayload,
		0o644,
	)
	ledger := readArtifactObject(t, root, &manifest, ArtifactRoleAppTreeLedger)
	embedded := ledgerEntryObject(t, ledger, "vibermate-build-manifest.json")
	embedded["sha256"] = payloadSHA256(embeddedPayload)
	embedded["size"] = len(embeddedPayload)
	writeDeclaredArtifact(
		t,
		root,
		artifactForRole(t, &manifest, ArtifactRoleAppTreeLedger),
		marshalArtifactJSON(t, ledger),
	)
	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsBindsCurrentCapabilityEvidence(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithCurrentCapabilityEvidence(t, root)
	if err := VerifyArtifacts(root, manifest); err != nil {
		t.Fatalf("VerifyArtifacts() error = %v", err)
	}

	tests := map[string]func(map[string]any){
		"commit": func(document map[string]any) {
			document["commit"] = strings.Repeat("b", 40)
		},
		"conformance revision": func(document map[string]any) {
			document["conformanceRevision"] = "other-revision"
		},
		"capability": func(document map[string]any) {
			document["capability"] = "different-capability"
		},
		"status": func(document map[string]any) {
			document["status"] = "degraded"
		},
		"closed document": func(document map[string]any) {
			document["detail"] = "not part of the contract"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			caseRoot := artifactTempDir(t)
			caseManifest := manifestWithCurrentCapabilityEvidence(t, caseRoot)
			document := readArtifactObject(t, caseRoot, &caseManifest, "root-trust-conformance")
			mutate(document)
			writeDeclaredArtifact(
				t,
				caseRoot,
				artifactForRole(t, &caseManifest, "root-trust-conformance"),
				marshalArtifactJSON(t, document),
			)
			if err := VerifyArtifacts(caseRoot, caseManifest); !errors.Is(err, ErrArtifactMismatch) {
				t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
			}
		})
	}
}

func TestVerifyArtifactsRejectsMetadataOverRoleLimit(t *testing.T) {
	root := artifactTempDir(t)
	manifest := manifestWithFiles(t, root)
	artifact := artifactForRole(t, &manifest, ArtifactRoleKnownIssues)
	artifact.Size = maxKnownIssuesBytes + 1
	if err := VerifyArtifacts(root, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func TestVerifyArtifactsRejectsSymlinkAncestorOfRoot(t *testing.T) {
	base := artifactTempDir(t)
	realParent := filepath.Join(base, "real-parent")
	realRoot := filepath.Join(realParent, "artifacts")
	if err := os.MkdirAll(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := manifestWithFiles(t, realRoot)
	linkedParent := filepath.Join(base, "linked-parent")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	linkedRoot := filepath.Join(linkedParent, "artifacts")
	if err := VerifyArtifacts(linkedRoot, manifest); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("VerifyArtifacts() error = %v, want ErrArtifactMismatch", err)
	}
}

func manifestWithFiles(t *testing.T, root string) Manifest {
	t.Helper()
	manifest := validManifest()
	writeSemanticArtifacts(t, root, &manifest)
	return manifest
}

func manifestWithCurrentCapabilityEvidence(t *testing.T, root string) Manifest {
	t.Helper()
	manifest := manifestWithFiles(t, root)
	manifest.Artifacts = append(manifest.Artifacts, Artifact{
		Path:      "root-trust-conformance.json",
		MediaType: "application/json",
		Role:      "root-trust-conformance",
	})
	capability := &manifest.PlatformSupport[0].HostCapabilities[0]
	capability.Status = CapabilitySupported
	capability.EvidenceStatus = EvidenceCurrent
	capability.EvidenceArtifactRole = "root-trust-conformance"
	payload := marshalArtifactJSON(t, capabilityEvidenceDocument{
		Schema:              CapabilityEvidenceSchema,
		Commit:              manifest.Commit,
		ConformanceRevision: manifest.PlatformSupport[0].ConformanceRevision,
		Capability:          capability.Capability,
		Status:              capability.Status,
		ObservedAt:          "2026-08-03T04:34:56Z",
	})
	writeDeclaredArtifact(
		t,
		root,
		artifactForRole(t, &manifest, "root-trust-conformance"),
		payload,
	)
	return manifest
}

func readArtifactObject(
	t *testing.T,
	root string,
	manifest *Manifest,
	role string,
) map[string]any {
	t.Helper()
	artifact := artifactForRole(t, manifest, role)
	payload, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(artifact.Path)))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func rewriteDesktopArtifactAndLedger(
	t *testing.T,
	root string,
	manifest *Manifest,
	desktopPayload []byte,
) {
	t.Helper()
	writeDeclaredArtifact(
		t,
		root,
		artifactForRole(t, manifest, ArtifactRoleDesktopBuildManifest),
		desktopPayload,
	)
	writePayloadFixtureFile(
		t,
		root,
		"vibermate-build-manifest.json",
		desktopPayload,
		0o644,
	)
	desktopArtifact := artifactForRole(
		t,
		manifest,
		ArtifactRoleDesktopBuildManifest,
	)
	ledger := readArtifactObject(t, root, manifest, ArtifactRoleAppTreeLedger)
	ledger["desktopBuildManifestSHA256"] = desktopArtifact.SHA256
	embedded := ledgerEntryObject(t, ledger, "vibermate-build-manifest.json")
	embedded["sha256"] = desktopArtifact.SHA256
	embedded["size"] = desktopArtifact.Size
	writeDeclaredArtifact(
		t,
		root,
		artifactForRole(t, manifest, ArtifactRoleAppTreeLedger),
		marshalArtifactJSON(t, ledger),
	)
}

func writeSemanticArtifacts(t *testing.T, root string, manifest *Manifest) {
	t.Helper()
	clean := false
	configuration := make(map[string]string, len(desktopBuildConfigurationNames))
	configurationDigits := "123456789ab"
	for index, name := range desktopBuildConfigurationNames {
		configuration[name] = strings.Repeat(string(configurationDigits[index]), 64)
	}
	sidecarPayloads := map[string][]byte{
		"vibermate":  []byte("launcher\n"),
		"vibermated": []byte("daemon!!\n"),
	}
	appFrameworkPayload := []byte("universal App framework\n")
	flutterFrameworkPayload := []byte("universal FlutterMacOS framework\n")
	nestedCode := map[string]string{
		"app-framework":           payloadSHA256(appFrameworkPayload),
		"flutter-macos-framework": payloadSHA256(flutterFrameworkPayload),
		"vibermate":               payloadSHA256(sidecarPayloads["vibermate"]),
		"vibermated":              payloadSHA256(sidecarPayloads["vibermated"]),
	}
	desktopPayload := marshalArtifactJSON(t, desktopBuildDocument{
		Schema: DesktopBuildSchemaV3,
		Source: desktopBuildSource{
			VCS:        "git",
			Revision:   manifest.Commit,
			CommitTime: "2026-08-03T04:34:56Z",
			Dirty:      &clean,
		},
		Profiles: desktopBuildProfiles{
			Desktop:  "release",
			Sidecars: "release",
			Target:   "universal-apple-darwin",
			Toolkit:  "flutter",
		},
		Toolchains: desktopBuildToolchains{
			Go:      "go version go1.25.12 darwin/arm64",
			Flutter: "Flutter 3.41.5 (2c9...)",
			Dart:    "Dart 3.11.3",
			Xcode:   "Xcode 16.2\nBuild version 16C5032a",
		},
		ConfigurationSHA256: configuration,
		NestedCodeSHA256:    nestedCode,
	})
	writeDeclaredArtifact(
		t,
		root,
		artifactForRole(t, manifest, ArtifactRoleDesktopBuildManifest),
		desktopPayload,
	)
	desktopArtifact := artifactForRole(t, manifest, ArtifactRoleDesktopBuildManifest)
	payloadRoot := filepath.Join(root, UnsignedPayloadRoot)
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
	writePayloadFixtureFile(t, root, "vibermate-desktop", mainPayload, 0o755)
	writePayloadFixtureFile(t, root, "vibermate", sidecarPayloads["vibermate"], 0o755)
	writePayloadFixtureFile(t, root, "vibermated", sidecarPayloads["vibermated"], 0o755)
	writePayloadFixtureFile(t, root, "dist/App.framework/App", appFrameworkPayload, 0o755)
	writePayloadFixtureFile(t, root, "dist/FlutterMacOS.framework/FlutterMacOS", flutterFrameworkPayload, 0o755)
	writePayloadFixtureFile(t, root, "vibermate-build-manifest.json", desktopPayload, 0o644)
	writePayloadFixtureFile(t, root, "LICENSE", licensePayload, 0o644)
	writePayloadFixtureFile(t, root, "dist/index.html", distPayload, 0o644)
	ledgerPayload := marshalArtifactJSON(t, appTreeLedgerDocument{
		Schema:                     AppTreeLedgerSchemaV1,
		Commit:                     manifest.Commit,
		Root:                       UnsignedPayloadRoot,
		DesktopBuildManifestSHA256: desktopArtifact.SHA256,
		Entries: []appTreeLedgerEntry{
			fixtureDirectoryLedgerEntry(".", 0o755),
			fixtureFileLedgerEntry("LICENSE", licensePayload, 0o644),
			fixtureDirectoryLedgerEntry("dist", 0o755),
			fixtureDirectoryLedgerEntry("dist/App.framework", 0o755),
			fixtureFileLedgerEntry("dist/App.framework/App", appFrameworkPayload, 0o755),
			fixtureDirectoryLedgerEntry("dist/FlutterMacOS.framework", 0o755),
			fixtureFileLedgerEntry("dist/FlutterMacOS.framework/FlutterMacOS", flutterFrameworkPayload, 0o755),
			fixtureFileLedgerEntry("dist/index.html", distPayload, 0o644),
			fixtureFileLedgerEntry("vibermate", sidecarPayloads["vibermate"], 0o755),
			fixtureFileLedgerEntry("vibermate-build-manifest.json", desktopPayload, 0o644),
			fixtureFileLedgerEntry("vibermate-desktop", mainPayload, 0o755),
			fixtureFileLedgerEntry("vibermated", sidecarPayloads["vibermated"], 0o755),
		},
	})
	writeDeclaredArtifact(
		t,
		root,
		artifactForRole(t, manifest, ArtifactRoleAppTreeLedger),
		ledgerPayload,
	)
	filesAnalyzed := false
	spdxPayload := marshalArtifactJSON(t, spdxDocument{
		SPDXID:            "SPDXRef-DOCUMENT",
		CreationInfo:      spdxCreationInfo{Created: "2026-08-03T04:34:56Z", Creators: []string{"Tool: vibermate-release-evidence"}},
		DataLicense:       "CC0-1.0",
		Name:              "vibermate-release-sbom",
		SPDXVersion:       "SPDX-2.3",
		DocumentNamespace: "https://vibermate.example.invalid/spdx/" + manifest.Commit,
		Comment:           expectedSPDXDocumentComment(*manifest),
		Packages: []spdxPackage{
			{
				SPDXID:           "SPDXRef-Package-vibermate",
				Name:             "vibermate",
				VersionInfo:      manifest.Version,
				DownloadLocation: "NOASSERTION",
				FilesAnalyzed:    &filesAnalyzed,
				LicenseConcluded: "NOASSERTION",
				LicenseDeclared:  "NOASSERTION",
				CopyrightText:    "NOASSERTION",
			},
		},
	})
	writeDeclaredArtifact(
		t,
		root,
		artifactForRole(t, manifest, ArtifactRoleSBOM),
		spdxPayload,
	)
	knownIssuesPayload := marshalArtifactJSON(t, knownIssuesDocument{
		Schema:  KnownIssuesSchemaV1,
		Version: manifest.Version,
		Commit:  manifest.Commit,
		Issues:  []knownIssue{},
	})
	writeDeclaredArtifact(
		t,
		root,
		artifactForRole(t, manifest, ArtifactRoleKnownIssues),
		knownIssuesPayload,
	)
}

func writePayloadFixtureFile(
	t *testing.T,
	root, relativePath string,
	payload []byte,
	mode os.FileMode,
) {
	t.Helper()
	fullPath := filepath.Join(
		root,
		UnsignedPayloadRoot,
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

func fixtureDirectoryLedgerEntry(relativePath string, mode uint32) appTreeLedgerEntry {
	return appTreeLedgerEntry{
		Mode: &mode,
		Path: relativePath,
		Type: "directory",
	}
}

func fixtureFileLedgerEntry(
	relativePath string,
	payload []byte,
	mode uint32,
) appTreeLedgerEntry {
	digest := payloadSHA256(payload)
	size := int64(len(payload))
	return appTreeLedgerEntry{
		Mode:   &mode,
		Path:   relativePath,
		Type:   "file",
		SHA256: &digest,
		Size:   &size,
	}
}

func payloadSHA256(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func ledgerEntryObject(
	t *testing.T,
	document map[string]any,
	relativePath string,
) map[string]any {
	t.Helper()
	entries, ok := document["entries"].([]any)
	if !ok {
		t.Fatal("ledger entries are not an array")
	}
	for _, value := range entries {
		entry, ok := value.(map[string]any)
		if ok && entry["path"] == relativePath {
			return entry
		}
	}
	t.Fatalf("ledger entry %q is missing", relativePath)
	return nil
}

func artifactForRole(t *testing.T, manifest *Manifest, role string) *Artifact {
	t.Helper()
	for index := range manifest.Artifacts {
		if manifest.Artifacts[index].Role == role {
			return &manifest.Artifacts[index]
		}
	}
	t.Fatalf("artifact role %q is missing", role)
	return nil
}

func writeDeclaredArtifact(t *testing.T, root string, artifact *Artifact, payload []byte) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(artifact.Path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	artifact.Size = int64(len(payload))
	artifact.SHA256 = hex.EncodeToString(digest[:])
}

func marshalArtifactJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(payload, '\n')
}

func artifactTempDir(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directory
}
