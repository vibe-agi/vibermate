package runtimedata

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) (string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "local-ca"), 0o700); err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(source, "runtime.db")}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE evidence (id INTEGER PRIMARY KEY, value TEXT); INSERT INTO evidence VALUES (1, 'retained evidence')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "local-ca", "private.pem"), []byte("test-only-ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	return source, filepath.Join(root, "target")
}

func TestCopyPreservesEntireSourceAndValidatesDatabase(t *testing.T) {
	source, target := fixture(t)
	before, _ := os.ReadFile(filepath.Join(source, "runtime.db"))
	if err := Copy(context.Background(), source, target); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(source, "runtime.db"))
	if string(before) != string(after) {
		t.Fatal("source database changed")
	}
	for _, root := range []string{source, target} {
		value, err := os.ReadFile(filepath.Join(root, "local-ca", "private.pem"))
		if err != nil || string(value) != "test-only-ca" {
			t.Fatalf("CA missing at %s", root)
		}
	}
	info, _ := os.Stat(filepath.Join(target, "local-ca", "private.pem"))
	if info.Mode().Perm() != 0o600 {
		t.Fatal("copied secrets must be private")
	}
	if err := Copy(context.Background(), source, target); !errors.Is(err, ErrTarget) {
		t.Fatalf("overwrite allowed: %v", err)
	}
}

func TestDataDirectoryOwnershipSecuresNativeShellDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	guard, err := Acquire(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("data directory must be private")
	}
	if another, err := Acquire(directory); err == nil {
		another.Release()
		t.Fatal("concurrent data owner admitted")
	}
}

func TestCopyRejectsBusyNestedSymlinkAndDamagedSources(t *testing.T) {
	for _, mode := range []string{"busy", "nested", "symlink", "corrupt", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			source, target := fixture(t)
			ctx := context.Background()
			want := ErrCopy
			switch mode {
			case "busy":
				guard, err := Acquire(source)
				if err != nil {
					t.Fatal(err)
				}
				defer guard.Release()
				want = ErrBusy
			case "nested":
				target = filepath.Join(source, "inside")
				want = ErrTarget
			case "symlink":
				if err := os.Symlink("/etc/hosts", filepath.Join(source, "link")); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(filepath.Join(source, "runtime.db"), []byte("not a database"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = ErrValidation
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			if err := Copy(ctx, source, target); !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			if _, err := os.Stat(filepath.Join(source, "runtime.db")); err != nil {
				t.Fatal("source deleted")
			}
		})
	}
}

func TestCopyIncludesUncheckpointedWAL(t *testing.T) {
	source, target := fixture(t)
	u := url.URL{Scheme: "file", Path: filepath.Join(source, "runtime.db")}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Simulate the durable leftovers of an unclean exit, holding this test's
	// connection only to keep SQLite from deleting the WAL before the copy.
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; INSERT INTO evidence VALUES (2, 'wal record')"); err != nil {
		t.Fatal(err)
	}
	if err := Copy(context.Background(), source, target); err != nil {
		t.Fatal(err)
	}
	u.Path = filepath.Join(target, "runtime.db")
	copied, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	var value string
	if err := copied.QueryRow("SELECT value FROM evidence WHERE id=2").Scan(&value); err != nil || value != "wal record" {
		t.Fatalf("WAL evidence lost: %q %v", value, err)
	}
}
