package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeWrapperRejectsMetadataDirectoryRedirection(t *testing.T) {
	for _, variant := range []string{"different-child", "dangling-child", "outside-root"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			entry := filepath.Join(root, "codex/bin/codex.js")
			native := filepath.Join(root, "codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex")
			for _, path := range []string{entry, native} {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("not executed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			redirected := filepath.Join(root, "redirected")
			if variant == "outside-root" {
				redirected = t.TempDir()
			}
			if err := os.MkdirAll(redirected, 0700); err != nil {
				t.Fatal(err)
			}
			metadata := filepath.Join(redirected, "package.json")
			if err := os.WriteFile(metadata, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(metadata, filepath.Join(root, "codex-darwin-arm64/package.json")); err != nil {
				t.Fatal(err)
			}
			child := filepath.Join(redirected, "vendor/aarch64-apple-darwin/bin/codex")
			if err := os.MkdirAll(filepath.Dir(child), 0700); err != nil {
				t.Fatal(err)
			}
			if variant == "dangling-child" {
				if err := os.Symlink(filepath.Join(root, "missing"), child); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(child, []byte("not executed"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := checkNativeWrapperResolution(entry, native); err == nil {
				t.Fatal("acceptance hashed an unrelated declared native child")
			}
		})
	}
}

func TestNativePrerequisitesRejectOfficialMetadataRedirectBeforeInvocation(t *testing.T) {
	path := os.Getenv("VIBERMATE_OFFICIAL_CODEX")
	if path == "" {
		t.Skip("requires official Codex package")
	}
	entry, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
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
	native := filepath.Join(root, "codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex")
	if err := checkNativeWrapperResolution(privateEntry, native); err != nil {
		t.Fatal("genuine hoisted layout rejected")
	}
	// A whole-package in-root link preserves the metadata/native relationship.
	packagePath := filepath.Join(root, "codex-darwin-arm64")
	relocated := filepath.Join(root, "relocated-package")
	if err := os.Rename(packagePath, relocated); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relocated, packagePath); err != nil {
		t.Fatal(err)
	}
	if err := checkNativeWrapperResolution(privateEntry, native); err != nil {
		t.Fatal("whole-package link rejected")
	}
	redirected := filepath.Join(root, "redirected")
	if err := os.MkdirAll(redirected, 0700); err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(root, "codex-darwin-arm64/package.json")
	if err := os.Rename(metadata, filepath.Join(redirected, "package.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(redirected, "package.json"), metadata); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "alternate-executed")
	child := filepath.Join(redirected, "vendor/aarch64-apple-darwin/bin/codex")
	if err := os.MkdirAll(filepath.Dir(child), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	invocations := t.TempDir()
	clients, err := verifyNativeMCPClients(context.Background(), config{codex145Path: privateEntry}, invocations)
	if err == nil || len(clients) != 0 {
		t.Fatal("redirected package reached version prerequisites")
	}
	entries, err := os.ReadDir(invocations)
	if err != nil || len(entries) != 0 {
		t.Fatal("redirected package created a client invocation before rejection")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("alternate or version executable ran")
	}
}
