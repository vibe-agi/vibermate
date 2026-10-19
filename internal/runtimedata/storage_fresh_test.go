package runtimedata

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/hostsecret"
)

func freshRootSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += string(data)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += target
		}
		result[relative] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestOwnedStorageFreshRefusesResidualStateBeforeReceipt(t *testing.T) {
	cases := []struct{ name, path, kind string }{
		{"identity", "machine-identity.json", "file"},
		{"ca", "local-ca/root-key.pem", "file"},
		{"empty_ca", "local-ca", "dir"},
		{"manifest", "root-manifest.json", "file"},
		{"reset", "root-reset-request.json", "file"},
		{"admin", "server-admin/admin-access-key", "file"},
		{"ui", "ui-state/workbench-preferences-v2.json", "file"},
		{"secret", "server-secrets/store.json", "file"},
		{"temp_secret", "server-secrets/.store.tmp", "file"},
		{"secret_file", "server-secrets", "file"},
		{"public_secret_directory", "server-secrets", "public_dir"},
		{"symlink_secret_directory", "server-secrets", "symlink"},
		{"unknown", "unknown-state", "file"},
		{"unknown_empty_directory", "server-other", "dir"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owned := storageCutoverFixture(t, 0)
			path := filepath.Join(owned.Directory, tc.path)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			switch tc.kind {
			case "file":
				if err := os.WriteFile(path, []byte("prior state must survive"), 0600); err != nil {
					t.Fatal(err)
				}
			case "dir", "public_dir":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if tc.kind == "public_dir" {
					if err := os.Chmod(path, 0755); err != nil {
						t.Fatal(err)
					}
				}
			case "symlink":
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
			}
			before := freshRootSnapshot(t, owned.Directory)
			layout, err := PrepareStorageOwned(context.Background(), owned, time.Now().UTC(), nil)
			if !errors.Is(err, ErrFreshControlAmbiguous) || !errors.Is(err, ErrSchemaUpgradeIncomplete) || layout.ID != "" {
				t.Fatalf("missing control accepted residual state: layout=%+v err=%v", layout, err)
			}
			if after := freshRootSnapshot(t, owned.Directory); !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal mutated root: before=%v after=%v", before, after)
			}
		})
	}
}

func TestOwnedStorageFreshAcceptsActualServerScaffoldAndLockRetry(t *testing.T) {
	for _, server := range []bool{false, true} {
		t.Run(map[bool]string{false: "locks", true: "actual_server_factory"}[server], func(t *testing.T) {
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(parent, "data")
			if server {
				factory, err := hostsecret.NewServerFileFactory(root)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = factory.Open(context.Background()); err != nil {
					t.Fatal(err)
				}
				entries, err := os.ReadDir(filepath.Join(root, "server-secrets"))
				if err != nil || len(entries) != 0 {
					t.Fatalf("factory scaffold: %v %v", entries, err)
				}
			} else if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			owner, host := upgradeGuards(t, root)
			if err := owner.Release(); err != nil {
				t.Fatal(err)
			}
			if err := host.Release(); err != nil {
				t.Fatal(err)
			}
			owner, host = upgradeGuards(t, root)
			owned := OwnedStorage{Directory: root, Runtime: owner, Host: host}
			runner := newStorageCutoverRunner()
			stop := errors.New("first admitted attempt interrupted")
			runner.barrier = func(ctx context.Context, name string) error {
				if name == "receipt-preparing" {
					return stop
				}
				return ctx.Err()
			}
			if _, err := runner.prepare(context.Background(), owned, time.Now().UTC(), nil); !errors.Is(err, stop) {
				t.Fatalf("fresh receipt was not admitted: %v", err)
			}
			layout, err := PrepareStorageOwned(context.Background(), owned, time.Now().UTC(), nil)
			if err != nil || layout.ID == "" {
				t.Fatalf("fresh receipt retry: %+v %v", layout, err)
			}
		})
	}
}
