//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPrivateCredentialRequiresOneStablePrivateRegularFile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "anthropic.key")
	if err := os.WriteFile(path, []byte("credential-sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := readPrivateCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := value.CopyBytes()
	value.Destroy()
	if err != nil || string(copy) != "credential-sentinel" {
		clear(copy)
		t.Fatalf("credential copy did not match: %v", err)
	}
	clear(copy)

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateCredential(path); err == nil {
		t.Fatal("world-readable credential file was accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "credential-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateCredential(link); err == nil {
		t.Fatal("credential symlink was accepted")
	}
	if err := os.WriteFile(path, []byte("credential\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateCredential(path); err == nil {
		t.Fatal("credential containing a header control byte was accepted")
	}
}
