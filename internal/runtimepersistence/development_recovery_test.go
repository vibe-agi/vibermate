package runtimepersistence

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveUnsupportedDevelopmentDatabasePreservesEveryArtifact(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	databasePath := filepath.Join(root, "runtime.db")
	want := map[string]string{
		"runtime.db":     "database",
		"runtime.db-wal": "wal",
		"runtime.db-shm": "shm",
	}
	for name, payload := range want {
		if err := os.WriteFile(filepath.Join(root, name), []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	backup, err := ArchiveUnsupportedDevelopmentDatabase(
		databasePath,
		time.Date(2026, 8, 5, 10, 11, 12, 13, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(backup) != filepath.Join(root, "development-backups") {
		t.Fatalf("backup path = %q", backup)
	}
	for name, payload := range want {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("source %q still exists: %v", name, err)
		}
		archived := filepath.Join(backup, name)
		got, err := os.ReadFile(archived)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != payload {
			t.Fatalf("archived %q = %q", name, got)
		}
		info, err := os.Stat(archived)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("archived %q mode = %o", name, info.Mode().Perm())
		}
	}
}

func TestArchiveUnsupportedDevelopmentDatabaseRejectsSymlinksAndMissingDatabase(
	t *testing.T,
) {
	t.Parallel()
	root := t.TempDir()
	databasePath := filepath.Join(root, "runtime.db")
	if _, err := ArchiveUnsupportedDevelopmentDatabase(
		databasePath,
		time.Now().UTC(),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing database error = %v", err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, databasePath); err != nil {
		t.Fatal(err)
	}
	if _, err := ArchiveUnsupportedDevelopmentDatabase(
		databasePath,
		time.Now().UTC(),
	); !errors.Is(err, ErrInvalidDatabasePath) {
		t.Fatalf("symlink database error = %v", err)
	}
}
