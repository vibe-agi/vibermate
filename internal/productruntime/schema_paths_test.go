package productruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSchemaSwitchPathsHaveOnePhysicalAuthority(t *testing.T) {
	root := t.TempDir()
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	paths, err := NewRuntimePaths(filepath.Join(alias, "new", "nested"))
	if err != nil || paths.DataDirectory() != filepath.Join(physical, "new", "nested") {
		t.Fatalf("physical authority=%q %v", paths.DataDirectory(), err)
	}
	endpoint := filepath.Join(root, "endpoint")
	if err := os.Symlink(physical, endpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRuntimePaths(endpoint); err == nil {
		t.Fatal("final endpoint link accepted")
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRuntimePaths(filepath.Join(file, "child")); err == nil {
		t.Fatal("file ancestor accepted")
	}
}
