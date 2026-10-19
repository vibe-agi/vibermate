package runtimedata

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

const (
	schemaSnapshotManifestName   = "schema-upgrade-manifest.json"
	schemaSnapshotManifestSchema = "runtime-schema-upgrade/v1"
)

type schemaSnapshotManifest struct {
	backupRoles
	Schema    string                         `json:"schema"`
	CreatedAt string                         `json:"createdAt"`
	Database  runtimepersistence.SchemaState `json:"database"`
	Files     []backupFile                   `json:"files"`
}

type snapshotManifestFile interface {
	io.WriteCloser
	Sync() error
}

// Each operation owns its dependencies; there are no process-global fault
// hooks. The ordinary backup/export code never uses this private runner.
type schemaSnapshotRunner struct {
	copyFile       func(context.Context, string, string, fs.FileInfo) error
	syncPath       func(string) error
	createManifest func(string) (snapshotManifestFile, error)
	removeManifest func(string) error
}

func newSchemaSnapshotRunner() schemaSnapshotRunner {
	return schemaSnapshotRunner{
		copyFile: copyVerified,
		syncPath: func(name string) error {
			file, err := os.Open(name)
			if err != nil {
				return err
			}
			return errors.Join(file.Sync(), file.Close())
		},
		createManifest: func(name string) (snapshotManifestFile, error) {
			return os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		},
		removeManifest: os.Remove,
	}
}

// CreateSchemaUpgradeSnapshot creates a private rollback copy of every local
// persistent file, including credentials. The caller must stop all writers
// and retain the exact live data and (if supplied) host guards throughout.
// This is not an ordinary backup/export and performs no schema migration.
func CreateSchemaUpgradeSnapshot(ctx context.Context, source, target string, owner, hostOwner *instanceguard.Guard, createdAt time.Time) error {
	return newSchemaSnapshotRunner().create(ctx, source, target, owner, hostOwner, createdAt)
}

func (runner schemaSnapshotRunner) create(ctx context.Context, source, target string, owner, hostOwner *instanceguard.Guard, createdAt time.Time) (result error) {
	if err := snapshotRoots(ctx, source, target); err != nil {
		return err
	}
	if createdAt.IsZero() || createdAt.Location() != time.UTC || !snapshotOwns(owner, source, lockName) {
		return ErrTarget
	}
	if hostOwner != nil {
		if !snapshotOwns(hostOwner, source, "server.lock") {
			return ErrTarget
		}
	} else {
		var err error
		hostOwner, err = instanceguard.Acquire(filepath.Join(source, "server.lock"))
		if err != nil {
			return snapshotLockError(err)
		}
		defer hostOwner.Release()
	}
	before, err := schemaSnapshotFiles(ctx, source, false)
	if err != nil {
		return snapshotOperationError(ctx, ErrCopy)
	}
	if !snapshotHasDatabase(before) {
		return ErrBackupInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return ErrTarget
	}
	targetOwner, targetHost, err := lockSchemaSnapshot(target)
	if err != nil {
		return err
	}
	defer targetOwner.Release()
	defer targetHost.Release()
	// Failure never authorizes use. Attempt to remove and flush the receipt;
	// a filesystem that also refuses cleanup can leave an uncertain manifest.
	// Validation/restore must independently recertify data and durability.
	defer func() {
		if result != nil {
			_ = runner.removeManifest(filepath.Join(target, schemaSnapshotManifestName))
			_ = runner.syncPath(target)
		}
	}()
	if err := runner.copyDirectory(ctx, source, target); err != nil {
		return err
	}
	copied, err := schemaSnapshotFiles(ctx, target, true)
	if err != nil || !sameBackupFiles(before, copied) {
		return snapshotOperationError(ctx, ErrCopy)
	}
	// Validation recovers only independently owned inspection copies; both the
	// source and the snapshot keep their recorded DB/WAL bytes.
	state, roles, err := inspectBackupRoles(ctx, target)
	if err != nil {
		return snapshotOperationError(ctx, ErrBackupIncompatible)
	}
	after, err := schemaSnapshotFiles(ctx, source, false)
	if err != nil || !sameBackupFiles(before, after) || !snapshotOwns(owner, source, lockName) || !snapshotOwns(hostOwner, source, "server.lock") {
		return snapshotOperationError(ctx, ErrCopy)
	}
	files, err := schemaSnapshotFiles(ctx, target, true)
	if err != nil {
		return snapshotOperationError(ctx, ErrCopy)
	}
	if err := runner.syncTree(ctx, target); err != nil {
		return err
	}
	manifest := schemaSnapshotManifest{backupRoles: roles, Schema: schemaSnapshotManifestSchema, CreatedAt: createdAt.Format(time.RFC3339Nano), Database: state, Files: files}
	if roles.Layout != nil {
		manifest.Schema = schemaSnapshotManifestSchemaV2
	}
	if err := runner.writeManifest(ctx, target, manifest); err != nil {
		return err
	}
	if err := runner.syncPath(target); err != nil {
		return ErrCopy
	}
	if err := runner.syncPath(filepath.Dir(target)); err != nil {
		return ErrCopy
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Detect source mutation or ownership release throughout finalization too.
	after, err = schemaSnapshotFiles(ctx, source, false)
	if err != nil || !sameBackupFiles(before, after) || !snapshotOwns(owner, source, lockName) || !snapshotOwns(hostOwner, source, "server.lock") {
		return snapshotOperationError(ctx, ErrCopy)
	}
	files, err = schemaSnapshotFiles(ctx, target, true)
	if err != nil || !sameBackupFiles(manifest.Files, files) {
		return snapshotOperationError(ctx, ErrCopy)
	}
	return nil
}

// ValidateSchemaUpgradeSnapshot verifies the full inventory and known schema
// while independently holding both locks of the snapshot directory.
func ValidateSchemaUpgradeSnapshot(ctx context.Context, directory string) error {
	return newSchemaSnapshotRunner().validate(ctx, directory)
}

func (runner schemaSnapshotRunner) validate(ctx context.Context, directory string) error {
	if err := snapshotDirectory(ctx, directory); err != nil {
		return err
	}
	owner, host, err := lockSchemaSnapshot(directory)
	if err != nil {
		return err
	}
	defer owner.Release()
	defer host.Release()
	_, err = runner.certifySnapshotLocked(ctx, directory)
	return err
}

// RestoreSchemaUpgradeSnapshot restores only into a new disjoint directory.
// It retains snapshot ownership until independent restored validation finishes
// and never removes or overwrites the original directory.
func RestoreSchemaUpgradeSnapshot(ctx context.Context, snapshot, target string) error {
	return newSchemaSnapshotRunner().restore(ctx, snapshot, target)
}

func (runner schemaSnapshotRunner) restore(ctx context.Context, snapshot, target string) error {
	if err := snapshotRoots(ctx, snapshot, target); err != nil {
		return err
	}
	owner, host, err := lockSchemaSnapshot(snapshot)
	if err != nil {
		return err
	}
	defer owner.Release()
	defer host.Release()
	manifest, err := runner.certifySnapshotLocked(ctx, snapshot)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return ErrTarget
	}
	targetOwner, targetHost, err := lockSchemaSnapshot(target)
	if err != nil {
		return err
	}
	defer targetOwner.Release()
	defer targetHost.Release()
	if err := runner.copyDirectory(ctx, snapshot, target); err != nil {
		return err
	}
	files, err := schemaSnapshotFiles(ctx, target, true)
	if err != nil || !sameBackupFiles(manifest.Files, files) {
		return snapshotOperationError(ctx, ErrBackupInvalid)
	}
	state, roles, err := inspectBackupRoles(ctx, target)
	if err != nil || state != manifest.Database || !reflect.DeepEqual(roles, manifest.backupRoles) {
		return snapshotOperationError(ctx, ErrBackupIncompatible)
	}
	files, err = schemaSnapshotFiles(ctx, target, true)
	if err != nil || !sameBackupFiles(manifest.Files, files) {
		return snapshotOperationError(ctx, ErrBackupInvalid)
	}
	if _, err := validateSchemaSnapshotLocked(ctx, snapshot); err != nil {
		return err
	}
	if err := runner.syncTree(ctx, target); err != nil {
		return err
	}
	if err := runner.syncPath(filepath.Dir(target)); err != nil {
		return ErrCopy
	}
	files, err = schemaSnapshotFiles(ctx, target, true)
	if err != nil || !sameBackupFiles(manifest.Files, files) {
		return snapshotOperationError(ctx, ErrBackupInvalid)
	}
	if _, err := validateSchemaSnapshotLocked(ctx, snapshot); err != nil {
		return err
	}
	return ctx.Err()
}

// A physically present manifest does not establish crash durability. This
// independent operation verifies bytes/schema first, then flushes all files,
// child directories, root and parent before certifying the inventory again.
func (runner schemaSnapshotRunner) certifySnapshotLocked(ctx context.Context, directory string) (schemaSnapshotManifest, error) {
	manifest, err := validateSchemaSnapshotLocked(ctx, directory)
	if err != nil {
		return manifest, err
	}
	if err := runner.syncTree(ctx, directory); err != nil {
		return manifest, err
	}
	if err := runner.syncPath(filepath.Dir(directory)); err != nil {
		return manifest, ErrCopy
	}
	after, err := validateSchemaSnapshotLocked(ctx, directory)
	if err != nil {
		return manifest, err
	}
	if !reflect.DeepEqual(manifest.backupRoles, after.backupRoles) || manifest.Schema != after.Schema || manifest.CreatedAt != after.CreatedAt || manifest.Database != after.Database || !sameBackupFiles(manifest.Files, after.Files) {
		return manifest, ErrBackupInvalid
	}
	return manifest, nil
}

func snapshotDirectory(ctx context.Context, directory string) error {
	if ctx == nil || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == string(filepath.Separator) {
		return ErrTarget
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	physical, err := filepath.EvalSymlinks(directory)
	if err != nil || physical != directory {
		return ErrTarget
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() {
		return ErrTarget
	}
	return nil
}

func snapshotRoots(ctx context.Context, source, target string) error {
	if err := snapshotDirectory(ctx, source); err != nil {
		return err
	}
	if !filepath.IsAbs(target) || filepath.Clean(target) != target || target == string(filepath.Separator) || within(source, target) || within(target, source) {
		return ErrTarget
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil || parent != filepath.Dir(target) {
		return ErrTarget
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return ErrTarget
	}
	return nil
}

func snapshotOwns(owner *instanceguard.Guard, directory, lock string) bool {
	return owner != nil && owner.Path() == filepath.Join(directory, lock) && owner.OwnsDirectory(directory)
}

func snapshotLockError(err error) error {
	if errors.Is(err, instanceguard.ErrAlreadyOwned) {
		return ErrBusy
	}
	return ErrCopy
}

func lockSchemaSnapshot(directory string) (*instanceguard.Guard, *instanceguard.Guard, error) {
	owner, err := instanceguard.Acquire(filepath.Join(directory, lockName))
	if err != nil {
		return nil, nil, snapshotLockError(err)
	}
	host, err := instanceguard.Acquire(filepath.Join(directory, "server.lock"))
	if err != nil {
		_ = owner.Release()
		return nil, nil, snapshotLockError(err)
	}
	return owner, host, nil
}

func snapshotOperationError(ctx context.Context, category error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return category
}

func snapshotEphemeral(rel string) bool {
	return rel == lockName || rel == "server.lock" || rel == "runtime.db-shm"
}

func schemaSnapshotFiles(ctx context.Context, directory string, private bool) ([]backupFile, error) {
	var files []backupFile
	shm := roleSHMPaths(directory)
	err := filepath.WalkDir(directory, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return ErrBackupInvalid
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			return ErrBackupInvalid
		}
		if private && runtime.GOOS != "windows" {
			want := fs.FileMode(0o600)
			if info.IsDir() {
				want = 0o700
			}
			if info.Mode().Perm() != want {
				return ErrBackupInvalid
			}
		}
		rel, err := filepath.Rel(directory, name)
		if err != nil {
			return ErrBackupInvalid
		}
		if rel == "." || info.IsDir() {
			return nil
		}
		if snapshotEphemeral(rel) || shm[filepath.ToSlash(rel)] || rel == schemaSnapshotManifestName {
			return nil
		}
		digest, err := digestFile(ctx, name)
		if err != nil {
			return snapshotOperationError(ctx, ErrBackupInvalid)
		}
		file := backupFile{Path: filepath.ToSlash(rel), Bytes: info.Size(), SHA256: digest}
		if !validSchemaSnapshotFile(file) {
			return ErrBackupInvalid
		}
		files = append(files, file)
		if len(files) > maximumManifestFiles {
			return ErrBackupInvalid
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func snapshotHasDatabase(files []backupFile) bool {
	for _, file := range files {
		if file.Path == "runtime.db" {
			return true
		}
	}
	return false
}

func (runner schemaSnapshotRunner) copyDirectory(ctx context.Context, source, target string) error {
	shm := roleSHMPaths(source)
	err := filepath.WalkDir(source, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return ErrCopy
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			return ErrCopy
		}
		rel, err := filepath.Rel(source, name)
		if err != nil {
			return ErrCopy
		}
		if rel == "." {
			return nil
		}
		if snapshotEphemeral(rel) || shm[filepath.ToSlash(rel)] || rel == schemaSnapshotManifestName {
			if info.IsDir() {
				return ErrCopy
			}
			return nil
		}
		destination := filepath.Join(target, rel)
		if info.IsDir() {
			return os.Mkdir(destination, 0o700)
		}
		if err := runner.copyFile(ctx, name, destination, info); err != nil {
			return ErrCopy
		}
		// The move helper may preserve owner execute; credentials and unknown
		// private snapshot files always use the narrower 0600 policy.
		return os.Chmod(destination, 0o600)
	})
	if err != nil {
		return snapshotOperationError(ctx, ErrCopy)
	}
	return nil
}

func (runner schemaSnapshotRunner) syncTree(ctx context.Context, directory string) error {
	var directories []string
	err := filepath.WalkDir(directory, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return ErrCopy
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, name)
			return nil
		}
		if err := runner.syncPath(name); err != nil {
			return ErrCopy
		}
		return nil
	})
	if err != nil {
		return snapshotOperationError(ctx, ErrCopy)
	}
	// Flush children before the directory entries that publish them.
	for index := len(directories) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := runner.syncPath(directories[index]); err != nil {
			return ErrCopy
		}
	}
	return nil
}

func (runner schemaSnapshotRunner) writeManifest(ctx context.Context, directory string, manifest schemaSnapshotManifest) error {
	encoded, err := json.Marshal(manifest)
	if err != nil || len(encoded)+1 > maximumManifestBytes {
		return ErrBackupInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := runner.createManifest(filepath.Join(directory, schemaSnapshotManifestName))
	if err != nil {
		return ErrCopy
	}
	defer file.Close()
	encoded = append(encoded, '\n')
	if count, err := file.Write(encoded); err != nil || count != len(encoded) {
		return ErrCopy
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return ErrCopy
	}
	return nil
}

func readSchemaSnapshotManifest(directory string) (schemaSnapshotManifest, error) {
	var manifest schemaSnapshotManifest
	name := filepath.Join(directory, schemaSnapshotManifestName)
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumManifestBytes {
		return manifest, ErrBackupInvalid
	}
	file, err := os.Open(name)
	if err != nil {
		return manifest, ErrBackupInvalid
	}
	encoded, readErr := io.ReadAll(io.LimitReader(file, maximumManifestBytes+1))
	if err := errors.Join(readErr, file.Close()); err != nil || len(encoded) > maximumManifestBytes {
		return manifest, ErrBackupInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, ErrBackupInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return manifest, ErrBackupInvalid
	}
	// Canonical encoding also rejects duplicate JSON members (which Go's
	// standard decoder otherwise accepts with last-member-wins semantics).
	canonical, err := json.Marshal(manifest)
	if err != nil || !bytes.Equal(encoded, append(canonical, '\n')) {
		return manifest, ErrBackupInvalid
	}
	created, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt)
	if err != nil || created.IsZero() || created.Location() != time.UTC || (manifest.Schema != schemaSnapshotManifestSchema && manifest.Schema != schemaSnapshotManifestSchemaV2) || len(manifest.Files) == 0 || len(manifest.Files) > maximumManifestFiles {
		return manifest, ErrBackupInvalid
	}
	if !validBackupRolesVersion(manifest.Schema == schemaSnapshotManifestSchemaV2, manifest.backupRoles) {
		return manifest, ErrBackupInvalid
	}
	for index, file := range manifest.Files {
		if !validSchemaSnapshotFile(file) || index > 0 && manifest.Files[index-1].Path >= file.Path {
			return manifest, ErrBackupInvalid
		}
	}
	if !snapshotHasDatabase(manifest.Files) {
		return manifest, ErrBackupInvalid
	}
	return manifest, nil
}

func validSchemaSnapshotFile(file backupFile) bool {
	clean := path.Clean(file.Path)
	digest, err := hex.DecodeString(file.SHA256)
	return file.Path != "" && clean == file.Path && clean != "." && clean != ".." && !path.IsAbs(clean) && !strings.HasPrefix(clean, "../") && !strings.ContainsAny(clean, "\\\x00:") && !snapshotEphemeral(clean) && clean != schemaSnapshotManifestName && file.Bytes >= 0 && err == nil && len(digest) == 32 && strings.ToLower(file.SHA256) == file.SHA256
}

func validateSchemaSnapshotLocked(ctx context.Context, directory string) (schemaSnapshotManifest, error) {
	manifest, err := readSchemaSnapshotManifest(directory)
	if err != nil {
		return manifest, err
	}
	files, err := schemaSnapshotFiles(ctx, directory, true)
	if err != nil || !sameBackupFiles(manifest.Files, files) {
		return manifest, snapshotOperationError(ctx, ErrBackupInvalid)
	}
	state, roles, err := inspectBackupRoles(ctx, directory)
	if err != nil || state != manifest.Database || state.Revision == 0 || !reflect.DeepEqual(roles, manifest.backupRoles) {
		return manifest, snapshotOperationError(ctx, ErrBackupIncompatible)
	}
	files, err = schemaSnapshotFiles(ctx, directory, true)
	if err != nil || !sameBackupFiles(manifest.Files, files) {
		return manifest, snapshotOperationError(ctx, ErrBackupInvalid)
	}
	return manifest, nil
}
