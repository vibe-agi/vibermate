package clientpath

import (
	"path/filepath"
	"testing"
)

func TestDefaultRemoteStateDirectoryIsAbsoluteAndClientScoped(t *testing.T) {
	directory, err := DefaultRemoteStateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(directory) || filepath.Base(directory) != "remote-client" {
		t.Fatalf("directory = %q", directory)
	}
}

func TestCustomRemoteStateNeverRequiresAHomeFallback(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	directory, err := LegacyRemoteLoginDirectory(filepath.Join(t.TempDir(), "custom"))
	if err != nil || directory != "" {
		t.Fatalf("custom state consulted unavailable home fallback: %q %v", directory, err)
	}
}
