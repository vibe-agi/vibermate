package localca

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootManifestRejectsUnboundOrAmbiguousIdentity(t *testing.T) {
	t.Parallel()

	for name, manifestFor := range map[string]func(RootIdentity) string{
		"digest": func(RootIdentity) string {
			return `{"schema":"vibermate-local-root-v2","revision":1,"certificateSha256":"` +
				strings.Repeat("0", 64) + `"}`
		},
		"zero-revision": func(identity RootIdentity) string {
			return `{"schema":"vibermate-local-root-v2","revision":0,"certificateSha256":"` +
				identity.Digest().String() + `"}`
		},
		"unknown-schema": func(identity RootIdentity) string {
			return `{"schema":"vibermate-local-root-v1","revision":1,"certificateSha256":"` +
				identity.Digest().String() + `"}`
		},
		"unknown-field": func(identity RootIdentity) string {
			return `{"schema":"vibermate-local-root-v2","revision":1,"certificateSha256":"` +
				identity.Digest().String() + `","path":"forbidden"}`
		},
		"trailing-value": func(identity RootIdentity) string {
			return `{"schema":"vibermate-local-root-v2","revision":1,"certificateSha256":"` +
				identity.Digest().String() + `"} {}`
		},
		"duplicate-field": func(identity RootIdentity) string {
			return `{"schema":"vibermate-local-root-v2","revision":1,"revision":2,"certificateSha256":"` +
				identity.Digest().String() + `"}`
		},
	} {
		name, manifestFor := name, manifestFor
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "ca")
			authority := openAuthority(t, directory, nil)
			identity := authority.Identity()
			shutdownAuthority(t, authority)
			if err := os.WriteFile(
				filepath.Join(directory, rootManifestFile),
				[]byte(manifestFor(identity)+"\n"),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(
				context.Background(),
				DefaultOptions(directory, context.Background()),
			); !errors.Is(err, ErrRootStateInvalid) {
				t.Fatalf("Open() error = %v", err)
			}
		})
	}
}

func TestRootStateRejectsWidenedPermissionsAndSymlinks(t *testing.T) {
	t.Parallel()

	for _, relative := range []string{".", rootKeyFile, rootCertFile, rootManifestFile} {
		relative := relative
		t.Run("permissions-"+relative, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "ca")
			authority := openAuthority(t, directory, nil)
			shutdownAuthority(t, authority)
			path := directory
			if relative != "." {
				path = filepath.Join(directory, relative)
			}
			if err := os.Chmod(path, 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(
				context.Background(),
				DefaultOptions(directory, context.Background()),
			); !errors.Is(err, ErrRootStateInvalid) {
				t.Fatalf("Open() error = %v", err)
			}
		})
	}

	for _, relative := range []string{rootKeyFile, rootCertFile, rootManifestFile} {
		relative := relative
		t.Run("symlink-"+relative, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "ca")
			authority := openAuthority(t, directory, nil)
			shutdownAuthority(t, authority)
			path := filepath.Join(directory, relative)
			backup := path + ".fixture"
			if err := os.Rename(path, backup); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(backup, path); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(
				context.Background(),
				DefaultOptions(directory, context.Background()),
			); !errors.Is(err, ErrRootStateInvalid) {
				t.Fatalf("Open() error = %v", err)
			}
		})
	}
}
