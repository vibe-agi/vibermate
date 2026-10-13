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

func TestOfficialCompoundReleaseRejectsMetadataRedirection(t *testing.T) {
	path, node := os.Getenv("VIBERMATE_OFFICIAL_CODEX"), os.Getenv("VIBERMATE_PINNED_NODE")
	if path == "" || node == "" {
		t.Skip("requires official package and pinned Node")
	}
	entry, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"different-child", "dangling-child", "outside-root", "whole-package-symlink"} {
		t.Run(variant, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "node_modules", "@openai")
			for _, relative := range []string{"codex/bin/codex.js", "codex/package.json", "codex-darwin-arm64/package.json", "codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex"} {
				destination := filepath.Join(root, relative)
				if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
					t.Fatal(err)
				}
				input, err := os.Open(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(entry))), relative))
				if err != nil {
					t.Fatal(err)
				}
				output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
				if err != nil {
					input.Close()
					t.Fatal(err)
				}
				_, copyErr := io.Copy(output, input)
				input.Close()
				closeErr := output.Close()
				if copyErr != nil || closeErr != nil {
					t.Fatal("copy official fixture")
				}
			}
			privateEntry := filepath.Join(root, "codex/bin/codex.js")
			launcher := filepath.Join(root, "launch/codex")
			if err := os.MkdirAll(filepath.Dir(launcher), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(privateEntry, launcher); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			verifier, err := clientadapter.NewReleaseVerifier(clientadapter.BuiltInCatalog())
			if err != nil {
				t.Fatal(err)
			}
			request := clientadapter.Request{Command: []string{"codex"}, CWD: root, ExecutablePath: launcher}
			detection, err := verifier.Verify(ctx, request)
			if err != nil || detection.Status != clientadapter.StatusVerified {
				t.Fatal("unmodified official tuple not verified")
			}
			if variant == "whole-package-symlink" {
				packagePath := filepath.Join(root, "codex-darwin-arm64")
				target := filepath.Join(root, "relocated-package")
				if err := os.Rename(packagePath, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, packagePath); err != nil {
					t.Fatal(err)
				}
				detection, err = verifier.Verify(ctx, request)
				if err != nil || detection.Status != clientadapter.StatusVerified {
					t.Fatal("whole-package in-root link changed metadata/native relationship")
				}
				return
			}
			redirected := filepath.Join(root, "redirected")
			if variant == "outside-root" {
				redirected = t.TempDir()
			}
			if err := os.MkdirAll(redirected, 0700); err != nil {
				t.Fatal(err)
			}
			metadata := filepath.Join(root, "codex-darwin-arm64/package.json")
			target := filepath.Join(redirected, "package.json")
			if err := os.Rename(metadata, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, metadata); err != nil {
				t.Fatal(err)
			}
			child := filepath.Join(redirected, "vendor/aarch64-apple-darwin/bin/codex")
			if err := os.MkdirAll(filepath.Dir(child), 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(root, "alternate-executed")
			if variant == "dangling-child" {
				if err := os.Symlink(filepath.Join(root, "absent"), child); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(child, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			// Resolution only: never load the wrapper or execute the alternate.
			command := exec.CommandContext(ctx, node, "-e", `const {createRequire}=require("node:module");const p=require("node:path");const m=createRequire(process.argv[1]).resolve("@openai/codex-darwin-arm64/package.json");process.stdout.write(p.join(p.dirname(m),"vendor/aarch64-apple-darwin/bin/codex"));`, privateEntry)
			command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root}
			command.Dir = root
			selected, err := command.Output()
			if err != nil {
				t.Fatal(err)
			}
			resolvedDirectory, err := filepath.EvalSymlinks(redirected)
			if err != nil || strings.TrimSpace(string(selected)) != filepath.Join(resolvedDirectory, "vendor/aarch64-apple-darwin/bin/codex") {
				t.Fatal("Node did not select redirected vendor")
			}
			detection, err = verifier.Verify(ctx, request)
			if err == nil {
				t.Errorf("metadata redirected child accepted without hard error: %s", detection.Status)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("alternate child executed")
			}
		})
	}
}
