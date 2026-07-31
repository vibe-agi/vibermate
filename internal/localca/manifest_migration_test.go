package localca

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootManifestV1MigratesWithoutChangingRootMaterial(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "ca")
	authority := openAuthority(t, directory, nil)
	identity := authority.Identity()
	shutdownAuthority(t, authority)
	keyBefore := readTestFile(t, filepath.Join(directory, rootKeyFile))
	certificateBefore := readTestFile(t, filepath.Join(directory, rootCertFile))
	writeV1Manifest(t, directory, identity)

	reopened := openAuthority(t, directory, nil)
	shutdownAuthority(t, reopened)
	if reopened.Identity() != identity {
		t.Fatal("manifest migration changed public Root identity")
	}
	if string(readTestFile(t, filepath.Join(directory, rootKeyFile))) !=
		string(keyBefore) ||
		string(readTestFile(t, filepath.Join(directory, rootCertFile))) !=
			string(certificateBefore) {
		t.Fatal("manifest migration changed Root key or certificate")
	}
	assertV2Manifest(t, directory, identity)
	assertPermissions(t, filepath.Join(directory, rootManifestFile), 0o600)
}

func TestRootManifestMigrationFailureLeavesCompleteRecoverableState(
	t *testing.T,
) {
	t.Parallel()

	for _, stage := range []string{
		"create",
		"chmod",
		"write",
		"file-sync",
		"close",
		"rename",
		"directory-sync",
	} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "ca")
			authority := openAuthority(t, directory, nil)
			identity := authority.Identity()
			shutdownAuthority(t, authority)
			keyBefore := readTestFile(t, filepath.Join(directory, rootKeyFile))
			certificateBefore := readTestFile(
				t,
				filepath.Join(directory, rootCertFile),
			)
			writeV1Manifest(t, directory, identity)

			options := DefaultOptions(directory, context.Background())
			opened, err := openWithFileOperations(
				context.Background(),
				options,
				&failingAtomicOperations{stage: stage},
			)
			if opened != nil || err == nil ||
				!strings.Contains(err.Error(), "migrate local Root manifest") {
				t.Fatalf("migration result = authority:%v error:%v", opened, err)
			}
			if string(readTestFile(t, filepath.Join(directory, rootKeyFile))) !=
				string(keyBefore) ||
				string(readTestFile(t, filepath.Join(directory, rootCertFile))) !=
					string(certificateBefore) {
				t.Fatal("failed migration changed Root key or certificate")
			}
			matches, globErr := filepath.Glob(
				filepath.Join(directory, "."+rootManifestFile+".*.tmp"),
			)
			if globErr != nil || len(matches) != 0 {
				t.Fatalf("migration temporary files = %v, error = %v", matches, globErr)
			}

			// Rename is the only destination mutation. A failure before it leaves
			// complete v1; directory fsync failure leaves complete v2. Either is
			// accepted and normalized by the next open.
			recovered := openAuthority(t, directory, nil)
			shutdownAuthority(t, recovered)
			if recovered.Identity() != identity {
				t.Fatal("recovery after migration failure changed Root identity")
			}
			assertV2Manifest(t, directory, identity)
		})
	}
}

func TestRootManifestRejectsUnboundOrAmbiguousIdentity(t *testing.T) {
	t.Parallel()

	for name, manifestFor := range map[string]func(RootIdentity) string{
		"v1-digest": func(RootIdentity) string {
			return `{"schema":"vibermate-local-root-v1","fingerprint":"` +
				strings.Repeat("0", 64) + `"}`
		},
		"v2-digest": func(RootIdentity) string {
			return `{"schema":"vibermate-local-root-v2","revision":1,"certificateSha256":"` +
				strings.Repeat("0", 64) + `"}`
		},
		"v2-zero-revision": func(identity RootIdentity) string {
			return `{"schema":"vibermate-local-root-v2","revision":0,"certificateSha256":"` +
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

type failingAtomicOperations struct {
	stage string
}

func (operations *failingAtomicOperations) CreateTemp(
	directory, pattern string,
) (atomicTempFile, error) {
	if operations.stage == "create" {
		return nil, errors.New("injected create failure")
	}
	file, err := (systemAtomicFileOperations{}).CreateTemp(directory, pattern)
	if err != nil {
		return nil, err
	}
	return &failingAtomicTempFile{
		atomicTempFile: file,
		stage:          operations.stage,
	}, nil
}

func (operations *failingAtomicOperations) Rename(oldPath, newPath string) error {
	if operations.stage == "rename" {
		return errors.New("injected rename failure")
	}
	return (systemAtomicFileOperations{}).Rename(oldPath, newPath)
}

func (operations *failingAtomicOperations) Remove(path string) error {
	return (systemAtomicFileOperations{}).Remove(path)
}

func (operations *failingAtomicOperations) SyncDirectory(path string) error {
	if operations.stage == "directory-sync" {
		return errors.New("injected directory sync failure")
	}
	return (systemAtomicFileOperations{}).SyncDirectory(path)
}

type failingAtomicTempFile struct {
	atomicTempFile
	stage string
}

func (file *failingAtomicTempFile) Chmod(mode os.FileMode) error {
	if file.stage == "chmod" {
		return errors.New("injected chmod failure")
	}
	return file.atomicTempFile.Chmod(mode)
}

func (file *failingAtomicTempFile) Write(data []byte) (int, error) {
	if file.stage == "write" {
		written, _ := file.atomicTempFile.Write(data[:len(data)/2])
		return written, errors.New("injected write failure")
	}
	return file.atomicTempFile.Write(data)
}

func (file *failingAtomicTempFile) Sync() error {
	if file.stage == "file-sync" {
		return errors.New("injected file sync failure")
	}
	return file.atomicTempFile.Sync()
}

func (file *failingAtomicTempFile) Close() error {
	err := file.atomicTempFile.Close()
	if file.stage == "close" {
		return errors.Join(err, errors.New("injected close failure"))
	}
	return err
}

func writeV1Manifest(t *testing.T, directory string, identity RootIdentity) {
	t.Helper()
	encoded, err := json.Marshal(rootManifestV1{
		Schema:      manifestSchemaV1,
		Fingerprint: identity.Digest().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, rootManifestFile),
		append(encoded, '\n'),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
}

func assertV2Manifest(
	t *testing.T,
	directory string,
	identity RootIdentity,
) {
	t.Helper()
	var manifest rootManifestV2
	if err := decodeStrictJSON(
		readTestFile(t, filepath.Join(directory, rootManifestFile)),
		&manifest,
	); err != nil {
		t.Fatalf("decode v2 manifest: %v", err)
	}
	if manifest.Schema != manifestSchemaV2 ||
		manifest.Revision != identity.Revision() ||
		manifest.CertificateSHA256 != identity.Digest().String() {
		t.Fatalf("v2 manifest = %+v", manifest)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
