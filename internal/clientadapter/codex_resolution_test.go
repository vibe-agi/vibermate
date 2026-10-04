package clientadapter_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

// A verified hoisted tuple must not identify a different child from the one
// selected by the exact official wrapper's createRequire(...).resolve call.
func TestOfficialCompoundReleaseRejectsNearerNativePackage(t *testing.T) {
	path, node := os.Getenv("VIBERMATE_OFFICIAL_CODEX"), os.Getenv("VIBERMATE_PINNED_NODE")
	if path == "" || node == "" {
		t.Skip("requires official Codex package and pinned Node")
	}
	entry, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "node_modules", "@openai")
	privateEntry := filepath.Join(root, "codex", "bin", "codex.js")
	for _, relative := range []string{"codex/bin/codex.js", "codex/package.json", "codex-darwin-arm64/package.json", "codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex"} {
		source := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(entry))), relative)
		destination := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			t.Fatal(err)
		}
		input, err := os.Open(source)
		if err != nil {
			t.Fatal(err)
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			_ = input.Close()
			t.Fatal(err)
		}
		_, copyErr := io.Copy(output, input)
		_ = input.Close()
		closeErr := output.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatal("copy official fixture")
		}
	}
	nearer := filepath.Join(root, "codex", "node_modules", "@openai", "codex-darwin-arm64", "package.json")
	if err := os.MkdirAll(filepath.Dir(nearer), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nearer, []byte(`{"name":"@openai/codex","version":"synthetic-shadow"}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "-e", `const {createRequire}=require("node:module");process.stdout.write(createRequire(process.argv[1]).resolve("@openai/codex-darwin-arm64/package.json"))`, privateEntry)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root}
	command.Dir = root
	selected, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	resolvedNearer, err := filepath.EvalSymlinks(nearer)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(selected)) != resolvedNearer {
		t.Fatal("fixture did not reproduce actual Node resolution shadowing")
	}
	verifier, err := clientadapter.NewReleaseVerifier(clientadapter.BuiltInCatalog())
	if err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(root, "codex-launch", "codex")
	if err := os.MkdirAll(filepath.Dir(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(privateEntry, launcher); err != nil {
		t.Fatal(err)
	}
	detection, err := verifier.Verify(ctx, clientadapter.Request{Command: []string{"codex"}, CWD: root, ExecutablePath: launcher})
	if err == nil {
		t.Fatalf("shadowed pinned release did not fail hard; signer fallback remains possible: %s", detection.Status)
	}
	// Relocate only the private copies into a deliberately declared nested
	// layout. The same official wrapper now selects exactly the pinned child.
	metadataBytes, err := os.ReadFile(filepath.Join(root, "codex", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var release clientadapter.Release
	if strings.Contains(string(metadataBytes), `"version": "0.159.2"`) {
		release = clientadapter.CodexCLI01592DarwinARM64()
	} else if strings.Contains(string(metadataBytes), `"version": "0.160.0"`) {
		release = clientadapter.CodexCLI01600DarwinARM64()
	} else {
		t.Fatal("nested regression requires exact159 or160 package")
	}
	if err := os.Remove(nearer); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "codex-darwin-arm64", "package.json"), nearer); err != nil {
		t.Fatal(err)
	}
	relativeNative := "vendor/aarch64-apple-darwin/bin/codex"
	nestedNative := filepath.Join(filepath.Dir(nearer), relativeNative)
	if err := os.MkdirAll(filepath.Dir(nestedNative), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "codex-darwin-arm64", relativeNative), nestedNative); err != nil {
		t.Fatal(err)
	}
	release.ArtifactRoot = ".."
	for i := range release.Artifacts {
		switch release.Artifacts[i].Role {
		case clientadapter.ArtifactPlatformPackageMetadata:
			release.Artifacts[i].RelativePath = "../node_modules/@openai/codex-darwin-arm64/package.json"
		case clientadapter.ArtifactNativeChild:
			release.Artifacts[i].RelativePath = "../node_modules/@openai/codex-darwin-arm64/" + relativeNative
		}
	}
	nestedCatalog, err := clientadapter.NewCatalog(3, []clientadapter.Release{release})
	if err != nil {
		t.Fatal(err)
	}
	nestedVerifier, err := clientadapter.NewReleaseVerifier(nestedCatalog)
	if err != nil {
		t.Fatal(err)
	}
	detection, err = nestedVerifier.Verify(ctx, clientadapter.Request{Command: []string{"codex"}, CWD: root, ExecutablePath: launcher})
	if err != nil || detection.Status != clientadapter.StatusVerified {
		t.Fatalf("genuine declared nested tuple rejected: %v", err)
	}
}
