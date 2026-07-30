package runtimepath_test

import (
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/runtimepath"
)

func TestLayoutUsesOneFixedApplicationRendezvous(t *testing.T) {
	t.Parallel()

	cache := filepath.Join(t.TempDir(), runtimepath.ApplicationID)
	layout, err := runtimepath.FromAppCache(cache)
	if err != nil {
		t.Fatal(err)
	}
	if layout.AppCacheDirectory != cache ||
		layout.RuntimeDirectory != filepath.Join(cache, "runtime") ||
		layout.GenerationLock != filepath.Join(cache, "runtime", "daemon.lock") ||
		layout.LauncherRecord != filepath.Join(cache, "runtime", "launcher-v1.json") {
		t.Fatalf("layout = %+v", layout)
	}
}
