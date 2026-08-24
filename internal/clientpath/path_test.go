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
