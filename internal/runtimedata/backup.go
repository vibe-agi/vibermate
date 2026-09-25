package runtimedata

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

const (
	backupManifestName         = "backup-manifest.json"
	backupManifestSchema       = "vibermate-data-backup/v1"
	maximumManifestBytes       = 4 << 20
	maximumManifestFiles       = 10_000
	serverSecretDirectory      = "server-secrets"
	developmentSecretDirectory = "development-secrets"
)

type backupManifest struct {
	Schema      string                         `json:"schema"`
	CreatedAt   string                         `json:"createdAt"`
	Database    runtimepersistence.SchemaState `json:"database"`
	Files       []backupFile                   `json:"files"`
	Recoverable backupScope                    `json:"recoverable"`
}

type backupFile struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type backupScope struct {
	RuntimeDatabase         bool `json:"runtimeDatabase"`
	LocalProxyCA            bool `json:"localProxyCA"`
	RuntimeConfiguration    bool `json:"runtimeConfiguration"`
	ProviderCredentials     bool `json:"providerCredentials"`
	HostKeychain            bool `json:"hostKeychain"`
	ExternalServerTLSFiles  bool `json:"externalServerTLSFiles"`
	DatabaseEncryptedAtRest bool `json:"databaseEncryptedAtRest"`
}

// Backup creates a new, verified directory snapshot while the Runtime is
// stopped. Provider credentials are deliberately omitted; the manifest makes
// that boundary machine-readable instead of relying on documentation.
func Backup(
	ctx context.Context,
	source string,
	target string,
	createdAt time.Time,
) error {
	if createdAt.IsZero() || createdAt.Location() != time.UTC {
		return ErrTarget
	}
	return copyDataDirectory(ctx, source, target, dataCopyBackup, createdAt)
}

func Restore(ctx context.Context, source, target string) error {
	return copyDataDirectory(ctx, source, target, dataCopyRestore, time.Time{})
}

func ValidateBackup(ctx context.Context, directory string) error {
	if ctx == nil || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory ||
		directory == string(filepath.Separator) {
		return ErrTarget
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return ErrTarget
	}
	guard, err := Acquire(directory)
	if err != nil {
		if errors.Is(err, instanceguard.ErrAlreadyOwned) {
			return ErrBusy
		}
		return ErrBackupInvalid
	}
	defer guard.Release()
	return validateBackupLocked(ctx, directory)
}

func writeBackupManifest(
	ctx context.Context,
	directory string,
	createdAt time.Time,
	state runtimepersistence.SchemaState,
) error {
	files, err := backupFiles(ctx, directory)
	if err != nil || len(files) == 0 || len(files) > maximumManifestFiles {
		return ErrBackupInvalid
	}
	if err := requiredBackupFiles(files); err != nil {
		return err
	}
	manifest := backupManifest{
		Schema:    backupManifestSchema,
		CreatedAt: createdAt.Format(time.RFC3339Nano),
		Database:  state,
		Files:     files,
		Recoverable: backupScope{
			RuntimeDatabase: true, LocalProxyCA: true, RuntimeConfiguration: true,
			ProviderCredentials: false, HostKeychain: false,
			ExternalServerTLSFiles: false, DatabaseEncryptedAtRest: false,
		},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil || len(encoded) > maximumManifestBytes {
		return ErrBackupInvalid
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(
		filepath.Join(directory, backupManifestName),
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return ErrBackupInvalid
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return ErrBackupInvalid
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return ErrBackupInvalid
	}
	return nil
}

func validateBackupLocked(ctx context.Context, directory string) error {
	manifest, err := readBackupManifest(directory)
	if err != nil {
		return err
	}
	state, err := runtimepersistence.ValidateOfflineDatabase(
		ctx, filepath.Join(directory, "runtime.db"),
	)
	if err != nil {
		if errors.Is(err, runtimepersistence.ErrSchemaBaselineMismatch) {
			return ErrBackupIncompatible
		}
		return ErrBackupInvalid
	}
	if state != manifest.Database {
		return ErrBackupIncompatible
	}
	observed, err := backupFiles(ctx, directory)
	if err != nil || !sameBackupFiles(manifest.Files, observed) {
		return ErrBackupInvalid
	}
	return requiredBackupFiles(observed)
}

func readBackupManifest(directory string) (backupManifest, error) {
	encoded, err := os.ReadFile(filepath.Join(directory, backupManifestName))
	if err != nil || len(encoded) == 0 || len(encoded) > maximumManifestBytes {
		return backupManifest{}, ErrBackupInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var manifest backupManifest
	if err := decoder.Decode(&manifest); err != nil {
		return backupManifest{}, ErrBackupInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return backupManifest{}, ErrBackupInvalid
	}
	createdAt, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt)
	if err != nil || createdAt.Location() != time.UTC || manifest.Schema != backupManifestSchema ||
		!validBackupScope(manifest.Recoverable) || len(manifest.Files) == 0 ||
		len(manifest.Files) > maximumManifestFiles {
		return backupManifest{}, ErrBackupInvalid
	}
	for index := range manifest.Files {
		if !validBackupFile(manifest.Files[index]) ||
			(index > 0 && manifest.Files[index-1].Path >= manifest.Files[index].Path) {
			return backupManifest{}, ErrBackupInvalid
		}
	}
	return manifest, nil
}

func validateRestoredFiles(ctx context.Context, backup, restored string) error {
	manifest, err := readBackupManifest(backup)
	if err != nil {
		return err
	}
	observed, err := backupFiles(ctx, restored)
	if err != nil || !sameBackupFiles(manifest.Files, observed) {
		return ErrBackupInvalid
	}
	return requiredBackupFiles(observed)
}

func sameBackupFiles(expected, observed []backupFile) bool {
	if len(expected) != len(observed) {
		return false
	}
	for index := range expected {
		if expected[index] != observed[index] {
			return false
		}
	}
	return true
}

func requiredBackupFiles(observed []backupFile) error {
	for _, required := range []string{
		"runtime.db", "local-ca/root-certificate.pem", "local-ca/root-key.pem",
		"local-ca/root-manifest.json",
	} {
		index := sort.Search(len(observed), func(index int) bool {
			return observed[index].Path >= required
		})
		if index == len(observed) || observed[index].Path != required {
			return ErrBackupInvalid
		}
	}
	return nil
}

func validBackupScope(scope backupScope) bool {
	return scope.RuntimeDatabase && scope.LocalProxyCA && scope.RuntimeConfiguration &&
		!scope.ProviderCredentials && !scope.HostKeychain &&
		!scope.ExternalServerTLSFiles && !scope.DatabaseEncryptedAtRest
}

func validBackupFile(file backupFile) bool {
	clean := path.Clean(file.Path)
	return file.Path != "" && clean == file.Path && clean != "." &&
		!path.IsAbs(clean) && clean != ".." && !strings.HasPrefix(clean, "../") &&
		file.Path != backupManifestName &&
		!manifestSecretPath(file.Path, serverSecretDirectory) &&
		!manifestSecretPath(file.Path, developmentSecretDirectory) && file.Bytes >= 0 &&
		len(file.SHA256) == sha256.Size*2 && strings.ToLower(file.SHA256) == file.SHA256
}

func manifestSecretPath(name, directory string) bool {
	return name == directory || strings.HasPrefix(name, directory+"/")
}

func backupFiles(ctx context.Context, directory string) ([]backupFile, error) {
	files := make([]backupFile, 0)
	err := filepath.WalkDir(directory, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(directory, name)
		if err != nil || rel == "." {
			return err
		}
		if rel == backupManifestName || rel == lockName || rel == "server.lock" ||
			rel == "runtime.db-shm" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return ErrBackupInvalid
		}
		digest, err := digestFile(ctx, name)
		if err != nil {
			return err
		}
		files = append(files, backupFile{
			Path: filepath.ToSlash(rel), Bytes: info.Size(), SHA256: digest,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return files, nil
}

func digestFile(ctx context.Context, name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, &contextReader{ctx: ctx, reader: file}); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
