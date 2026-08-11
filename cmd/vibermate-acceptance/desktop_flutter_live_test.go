//go:build darwin

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimepath"
)

func TestPackagedFlutterDesktopShellLive(t *testing.T) {
	appPath := os.Getenv("VIBERMATE_LIVE_TEST_APP")
	if appPath == "" {
		t.Skip("Set VIBERMATE_LIVE_TEST_APP to an absolute packaged ViberMate.app path.")
	}
	canonicalApp, err := canonicalDesktopBundlePath(appPath)
	if err != nil {
		t.Fatal(err)
	}
	homeDirectory := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(homeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	layout, err := runtimepath.FromAppCache(filepath.Join(
		homeDirectory,
		"Library",
		"Caches",
		runtimepath.ApplicationID,
	))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := exercisePackagedDesktopShell(
		ctx,
		canonicalApp,
		layout,
		homeDirectory,
	); err != nil {
		t.Fatal(err)
	}
}
