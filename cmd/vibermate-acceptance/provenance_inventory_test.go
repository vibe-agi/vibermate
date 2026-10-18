package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactCollectorBindsEveryProducerNestedMember(t *testing.T) {
	t.Parallel()
	payload, err := os.ReadFile("../../internal/acceptancereport/testdata/desktop-build-inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	var inventory struct {
		Nested map[string]string `json:"nested"`
	}
	if err := json.Unmarshal(payload, &inventory); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bundle := filepath.Join(root, "ViberMate.app")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, relative := range inventory.Nested {
		write(filepath.Join(bundle, relative), "packaged "+name)
	}
	desktop := filepath.Join(bundle, "Contents", "MacOS", "vibermate-desktop")
	manifest := filepath.Join(bundle, "Contents", "Resources", desktopBuildManifestName)
	acceptance, client := filepath.Join(root, "acceptance"), filepath.Join(root, "client")
	for _, path := range []string{desktop, manifest, acceptance, client} {
		write(path, path)
	}
	config := config{desktopAppPath: bundle, daemonPath: filepath.Join(bundle, inventory.Nested["vibermated"]), launcherPath: filepath.Join(bundle, inventory.Nested["vibermate"])}
	collect := func() ([]artifactProvenance, error) {
		return collectArtifactProvenance(config, acceptance, desktop, client, manifest)
	}
	artifacts, err := collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != len(inventory.Nested)+5 {
		t.Fatalf("artifact count = %d", len(artifacts))
	}
	byRole := make(map[string]artifactProvenance)
	for _, artifact := range artifacts {
		byRole[artifact.Role] = artifact
	}
	for name, relative := range inventory.Nested {
		role := name
		if name == "vibermated" {
			role = "daemon"
		}
		if name == "vibermate" {
			role = "launcher"
		}
		artifact := byRole[role]
		if artifact.Path != filepath.Join(bundle, relative) || !validSHA256(artifact.SHA256) || artifact.Bytes <= 0 {
			t.Fatalf("producer member %s is not collected: %+v", name, artifact)
		}
	}
	kernel := byRole["vibermate-xray"]
	write(kernel.Path, "changed shipped kernel")
	changed, err := collect()
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range changed {
		if artifact.Role == kernel.Role && artifact.SHA256 == kernel.SHA256 {
			t.Fatal("kernel digest did not bind shipped bytes")
		}
	}
	if err := os.Remove(kernel.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := collect(); err == nil || !strings.Contains(err.Error(), "vibermate-xray") {
		t.Fatalf("missing kernel error = %v", err)
	}
	if err := os.Symlink(client, kernel.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := collect(); err == nil {
		t.Fatal("symbolic kernel accepted")
	}
}
