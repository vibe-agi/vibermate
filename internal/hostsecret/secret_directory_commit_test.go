package hostsecret

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/secretstore"
)

// Catch assuming ownership when mkdir reports an entry created by another
// process between the missing-entry check and the actual mkdir operation.
func TestServerDirectoryCommitMkdirExistsPreservesAncestorPermissions(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "owned-boundary"} {
		t.Run(kind, func(t *testing.T) {
			anchor, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			shared := filepath.Join(anchor, "shared")
			data := filepath.Join(shared, "server")
			raced := shared
			if kind == "owned-boundary" {
				raced = data
			}
			target := filepath.Join(anchor, "target")
			if kind == "symlink" {
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(target, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			factory, err := NewServerFileFactory(data)
			if err != nil {
				t.Fatal(err)
			}
			real := factory.directories.ops
			factory.directories.ops.mkdir = func(path string, mode os.FileMode) error {
				if path != raced {
					return real.mkdir(path, mode)
				}
				if kind == "symlink" {
					if err := os.Symlink(target, path); err != nil {
						t.Skipf("ancestor aliases unavailable: %v", err)
					}
				} else {
					if err := os.Mkdir(path, 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(path, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				return os.ErrExist
			}
			visited := make(map[string]bool)
			factory.directories.ops.syncDirectory = func(path string) error {
				visited[path] = true
				return real.syncDirectory(path)
			}
			store, err := factory.Open(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(raced)
			if err != nil {
				t.Fatal(err)
			}
			wantMode := os.FileMode(0o755)
			if kind == "owned-boundary" {
				wantMode = 0o700
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != wantMode {
				t.Fatalf("mkdir ErrExist changed directory permissions: got %o want %o", info.Mode().Perm(), wantMode)
			}
			if !visited[raced] {
				t.Fatal("mkdir ErrExist skipped the directory barrier")
			}
			if kind == "symlink" && !visited[target] {
				t.Fatal("mkdir ErrExist skipped the physical target barrier")
			}
			for path := filepath.Join(data, "server-secrets"); ; path = filepath.Dir(path) {
				if !visited[path] {
					t.Fatal("mkdir ErrExist skipped a required path barrier")
				}
				if filepath.Dir(path) == path {
					break
				}
			}
			reference, err := secretstore.ParseReference("secret://provider/account")
			if err != nil {
				t.Fatal(err)
			}
			value := newServerCommitValue(t, "synthetic-credential")
			defer value.Destroy()
			metadata, err := store.Replace(t.Context(), secretstore.ReplaceCommand{Reference: reference, Value: value})
			if err != nil || metadata.Revision != 1 {
				t.Fatalf("first commit after mkdir ErrExist: %+v %v", metadata, err)
			}
		})
	}
}

// Catch acknowledging a first credential through directory entries whose
// barriers were skipped, including a retry where failed provisioning left dirs.
func TestServerFreshDirectoryCommitBarriersAndRetry(t *testing.T) {
	for _, phase := range []string{"anchor", "ancestor-one", "ancestor-two", "data", "secrets"} {
		t.Run(phase, func(t *testing.T) {
			anchor, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			first := filepath.Join(anchor, "first")
			second := filepath.Join(first, "second")
			data := filepath.Join(second, "server")
			secrets := filepath.Join(data, "server-secrets")
			faultDirectory := map[string]string{"anchor": anchor, "ancestor-one": first, "ancestor-two": second, "data": data, "secrets": secrets}[phase]
			factory, err := NewServerFileFactory(data)
			if err != nil {
				t.Fatal(err)
			}
			realSync := factory.directories.ops.syncDirectory
			failed := false
			visited := make(map[string]int)
			injectedSync := func(path string) error {
				if path != filepath.VolumeName(anchor)+string(filepath.Separator) && path != anchor && !strings.HasPrefix(path, anchor+string(filepath.Separator)) && !strings.HasPrefix(anchor, path+string(filepath.Separator)) {
					t.Fatal("provisioning synchronized an unrelated directory")
				}
				if info, err := os.Stat(path); err != nil || !info.IsDir() {
					t.Fatal("directory barrier was not backed by a real directory")
				}
				visited[path]++
				if path == faultDirectory && !failed {
					failed = true
					return errors.New("synthetic-provisioning-secret")
				}
				return realSync(path)
			}
			factory.directories.ops.syncDirectory = injectedSync
			if _, err := factory.Open(t.Context()); !errors.Is(err, secretstore.ErrUnavailable) {
				t.Fatalf("fresh provisioning barrier failure was acknowledged: %v", err)
			}
			if _, err := os.Stat(factory.path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed provisioning created a credential document")
			}
			beforeRetry := visited[faultDirectory]
			// A newly constructed factory must also retry barriers, with no
			// in-memory pending list from the failed first instance.
			retry, err := NewServerFileFactory(data)
			if err != nil {
				t.Fatal(err)
			}
			retry.directories.ops.syncDirectory = injectedSync
			store, err := retry.Open(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if visited[faultDirectory] <= beforeRetry {
				t.Fatal("retry trusted a merely existing unsynced directory")
			}
			for _, directory := range []string{anchor, first, second, data, secrets} {
				if visited[directory] == 0 {
					t.Fatal("fresh path omitted a required directory barrier")
				}
			}
			for directory := anchor; ; directory = filepath.Dir(directory) {
				if visited[directory] == 0 {
					t.Fatal("provisioning skipped a path entry above the temporary boundary")
				}
				if filepath.Dir(directory) == directory {
					break
				}
			}
			for _, directory := range []string{first, second, data, secrets} {
				info, err := os.Stat(directory)
				if err != nil {
					t.Fatal(err)
				}
				if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
					t.Fatal("provisioned directory is not owner protected")
				}
			}
			reference, err := secretstore.ParseReference("secret://provider/account")
			if err != nil {
				t.Fatal(err)
			}
			value := newServerCommitValue(t, "synthetic-credential")
			defer value.Destroy()
			metadata, err := store.Replace(t.Context(), secretstore.ReplaceCommand{Reference: reference, Value: value})
			if err != nil || metadata.Revision != 1 {
				t.Fatalf("first durable credential: %+v %v", metadata, err)
			}
			if _, err := store.Replace(t.Context(), secretstore.ReplaceCommand{Reference: reference, Value: value}); !errors.Is(err, secretstore.ErrRevisionConflict) {
				t.Fatalf("first revision CAS: %v", err)
			}
			if err := store.Delete(t.Context(), reference); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(secrets)
			if err != nil || len(entries) != 1 || entries[0].Name() != "store.json" {
				t.Fatal("directory or file commit left a temporary credential file")
			}
		})
	}
}

func TestServerFreshDirectoryCommitCertifiesAncestorAliasTarget(t *testing.T) {
	anchor, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	physicalParent := filepath.Join(anchor, "physical")
	physicalBranch := filepath.Join(physicalParent, "branch")
	if err := os.MkdirAll(physicalBranch, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(physicalParent, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(anchor, "alias")
	if err := os.Symlink(physicalBranch, alias); err != nil {
		t.Skipf("ancestor aliases unavailable: %v", err)
	}
	factory, err := NewServerFileFactory(filepath.Join(alias, "server"))
	if err != nil {
		t.Fatal(err)
	}
	realSync := factory.directories.ops.syncDirectory
	failed := false
	visits := 0
	factory.directories.ops.syncDirectory = func(path string) error {
		if path == physicalParent {
			visits++
			if !failed {
				failed = true
				return errors.New("synthetic-provisioning-secret")
			}
		}
		return realSync(path)
	}
	if _, err := factory.Open(t.Context()); !errors.Is(err, secretstore.ErrUnavailable) {
		t.Fatalf("ancestor alias target was not certified: %v", err)
	}
	if visits != 1 {
		t.Fatal("physical ancestor barrier was not reached")
	}
	retry, err := NewServerFileFactory(filepath.Join(alias, "server"))
	if err != nil {
		t.Fatal(err)
	}
	retry.directories.ops.syncDirectory = factory.directories.ops.syncDirectory
	if _, err := retry.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	if visits != 2 {
		t.Fatal("fresh retry skipped the existing alias target's barrier")
	}
	info, err := os.Stat(physicalParent)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Fatal("provisioning changed permissions on a pre-existing ancestor")
	}
}

func TestServerFreshDirectoryCommitCertifiesIntermediateAliasEntry(t *testing.T) {
	anchor, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	physical := filepath.Join(anchor, "physical")
	intermediate := filepath.Join(anchor, "intermediate")
	if err := os.Mkdir(physical, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(intermediate, 0o700); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(intermediate, "second")
	if err := os.Symlink(physical, second); err != nil {
		t.Skipf("ancestor aliases unavailable: %v", err)
	}
	first := filepath.Join(anchor, "first")
	if err := os.Symlink(second, first); err != nil {
		t.Fatal(err)
	}
	factory, err := NewServerFileFactory(filepath.Join(first, "server"))
	if err != nil {
		t.Fatal(err)
	}
	real := factory.directories.ops.syncDirectory
	factory.directories.ops.syncDirectory = func(path string) error {
		if path == intermediate {
			return errors.New("synthetic-provisioning-secret")
		}
		return real(path)
	}
	if _, err := factory.Open(t.Context()); !errors.Is(err, secretstore.ErrUnavailable) {
		t.Fatalf("intermediate alias entry was not certified: %v", err)
	}
}

func TestServerFreshDirectoryCommitRejectsUncertifiableAliasTarget(t *testing.T) {
	anchor := t.TempDir()
	physical := filepath.Join(anchor, "physical")
	if err := os.Mkdir(physical, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(anchor, "alias")
	if err := os.Symlink("physical/../physical", alias); err != nil {
		t.Skipf("ancestor aliases unavailable: %v", err)
	}
	factory, err := NewServerFileFactory(filepath.Join(alias, "server"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := factory.Open(t.Context()); !errors.Is(err, secretstore.ErrUnavailable) {
		t.Fatalf("uncertifiable alias target was accepted: %v", err)
	}
}

func TestServerFreshDirectoryCommitCancellationAndPermissions(t *testing.T) {
	for _, phase := range []string{"cancel", "mkdir", "chmod"} {
		t.Run(phase, func(t *testing.T) {
			anchor := t.TempDir()
			data := filepath.Join(anchor, "server")
			factory, err := NewServerFileFactory(data)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failed := false
			real := factory.directories.ops
			factory.directories.ops.mkdir = func(path string, mode os.FileMode) error {
				if phase == "mkdir" && !failed {
					failed = true
					return errors.New("synthetic-provisioning-secret")
				}
				return real.mkdir(path, mode)
			}
			factory.directories.ops.chmod = func(path string, mode os.FileMode) error {
				if phase == "chmod" && !failed {
					failed = true
					return errors.New("synthetic-provisioning-secret")
				}
				return real.chmod(path, mode)
			}
			factory.directories.ops.syncDirectory = func(path string) error {
				if phase == "cancel" && !failed {
					failed = true
					cancel()
				}
				return real.syncDirectory(path)
			}
			_, openErr := factory.Open(ctx)
			if !errors.Is(openErr, secretstore.ErrUnavailable) {
				t.Fatalf("failed initialization returned success: %v", openErr)
			}
			if strings.Contains(openErr.Error(), "synthetic-provisioning-secret") {
				t.Fatal("initialization diagnostic disclosed private material")
			}
			if _, err := factory.Open(context.Background()); err != nil {
				t.Fatalf("initialization retry: %v", err)
			}
		})
	}
}
