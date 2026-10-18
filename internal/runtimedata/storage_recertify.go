package runtimedata

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// A persisted digest is not a certificate. Reconstruct an independent private
// source from the retained original artifacts and compare complete projections.
// This is only receipt reconciliation, never ordinary startup.
func (r storageCutoverRunner) recertify(ctx context.Context, owned OwnedStorage, receipt roleCutoverReceipt, stage string) error {
	if receipt.Phase == "ready" {
		if err := verifyCutoverOriginalLocations(ctx, owned, receipt); err != nil {
			return err
		}
	} else if err := verifyCutoverArchive(ctx, owned, receipt); err != nil {
		return err
	}
	for _, name := range []string{"recert-source.db", "recert-source.db-wal", "recert-source.db-shm", "recert-control.db", "recert-control.db-journal"} {
		path := filepath.Join(stage, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return ErrTarget
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	}
	derived := filepath.Join(stage, "recert-control.db")
	var expected [32]byte
	var expectedRows int64
	if receipt.Source.Revision == 0 {
		created, _ := time.Parse(time.RFC3339Nano, receipt.CreatedAt)
		if err := runtimepersistence.CreateStagedControl(ctx, derived, receipt.Layout.ID, created); err != nil {
			return err
		}
		var err error
		expected, expectedRows, err = runtimepersistence.CertifyStagedControlProjection(ctx, derived, receipt.Layout.ID, 0)
		if err != nil {
			return err
		}
	} else {
		archive := filepath.Join(owned.Directory, receipt.Layout.Archives[0].RelativeDirectory)
		for _, file := range receipt.Files {
			source := filepath.Join(archive, file.Name)
			if receipt.Phase == "ready" {
				rootSource := filepath.Join(owned.Directory, file.Name)
				_, rootErr := os.Lstat(rootSource)
				_, archiveErr := os.Lstat(source)
				if rootErr == nil && errors.Is(archiveErr, os.ErrNotExist) {
					source = rootSource
				} else if !errors.Is(rootErr, os.ErrNotExist) || archiveErr != nil {
					return ErrTarget
				}
			}
			if err := verifyCutoverArtifact(ctx, source, file); err != nil {
				return err
			}
			if file.Name == "runtime.db-shm" {
				continue
			}
			info, err := os.Lstat(source)
			if err != nil {
				return err
			}
			if err = r.copyFile(ctx, source, filepath.Join(stage, "recert-source.db"+strings.TrimPrefix(file.Name, "runtime.db")), info); err != nil {
				return err
			}
		}
		proof, err := r.extract(ctx, runtimepersistence.ControlExtraction{SourcePath: filepath.Join(stage, "recert-source.db"), TargetPath: derived, LayoutID: receipt.Layout.ID, ArchiveID: receipt.Layout.Archives[0].ID})
		if err != nil {
			return err
		}
		if proof.Source != receipt.Source || hex.EncodeToString(proof.ProjectionDigest[:]) != receipt.ProjectionSHA256 {
			return ErrBackupInvalid
		}
		expected, expectedRows = proof.ProjectionDigest, proof.Rows
	}
	actualPath := filepath.Join(stage, "control.db")
	if _, err := os.Lstat(actualPath); errors.Is(err, os.ErrNotExist) && receipt.Phase != "ready" {
		actualPath = filepath.Join(owned.Directory, "runtime.db")
	}
	if err := noControlSidecars(actualPath); err != nil {
		return err
	}
	actual, count, err := runtimepersistence.CertifyStagedControlProjection(ctx, actualPath, receipt.Layout.ID, receipt.Source.Revision)
	if err != nil {
		return err
	}
	if actual != expected || count != expectedRows {
		return ErrBackupInvalid
	}
	return checkStorageOwnership(ctx, owned)
}
func noControlSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			return ErrTarget
		}
	}
	return nil
}
func expectedArchiveManifest(receipt roleCutoverReceipt) archiveManifest {
	return archiveManifest{Schema: "vibermate-legacy-archive/v1", Archive: receipt.Layout.Archives[0], Files: receipt.Files, Certification: "required-control-projection-and-original-bytes/v1", ProjectionSHA256: receipt.ProjectionSHA256, ArchiveEpoch: 1, CaptureGeneration: 1}
}
func verifyArchiveManifest(path string, expected archiveManifest) (bool, error) {
	encoded, exists, err := readStorageDocument(path)
	if err != nil || !exists {
		return exists, err
	}
	var actual archiveManifest
	if err = decodeStorageDocument(encoded, &actual); err != nil || !reflect.DeepEqual(actual, expected) {
		return true, ErrBackupInvalid
	}
	return true, nil
}
func verifyCutoverArchive(ctx context.Context, owned OwnedStorage, receipt roleCutoverReceipt) error {
	if len(receipt.Layout.Archives) == 0 {
		return nil
	}
	archive := filepath.Join(owned.Directory, receipt.Layout.Archives[0].RelativeDirectory)
	if err := checkCutoverDirectory(archive); err != nil {
		return err
	}
	if err := verifyCutoverInventory(ctx, archive, receipt.Files); err != nil {
		return err
	}
	exists, err := verifyArchiveManifest(filepath.Join(archive, "archive-manifest.json"), expectedArchiveManifest(receipt))
	if err != nil {
		return err
	}
	if !exists {
		return ErrBackupInvalid
	}
	return nil
}

// Activated is an admission boundary: a formerly removed receipt can reappear
// after uncertain directory durability. Current control may have admitted new
// writes and history may have been maintained. Never re-extract or compare it
// to the initial source projection in this phase.
func (r storageCutoverRunner) finishActivated(ctx context.Context, owned OwnedStorage, receipt roleCutoverReceipt) (StorageLayout, error) {
	root := owned.Directory
	if receipt.Phase != "activated" {
		return StorageLayout{}, ErrBackupInvalid
	}
	state, err := runtimepersistence.ValidateCurrentControl(ctx, filepath.Join(root, "runtime.db"), receipt.Layout.ID)
	if err != nil {
		return StorageLayout{}, err
	}
	initialized := receipt.Source.InitializedAt
	if receipt.Source.Revision == 0 {
		initialized = receipt.CreatedAt
	}
	if state.InitializedAt != initialized {
		return StorageLayout{}, ErrBackupInvalid
	}
	current, err := ReadStorageLayout(root)
	if err != nil || current.ID != receipt.Layout.ID || current.Control != receipt.Layout.Control || current.Observation != receipt.Layout.Observation {
		return StorageLayout{}, ErrBackupInvalid
	}
	if err = r.sync(ctx, owned, root, "activation-root-sync"); err != nil {
		return StorageLayout{}, err
	}
	if err = r.check(ctx, owned, "before-receipt-removal"); err != nil {
		return StorageLayout{}, err
	}
	if err = os.Remove(filepath.Join(root, schemaUpgradeReceiptName)); err != nil {
		return StorageLayout{}, err
	}
	if err = r.sync(ctx, owned, root, "receipt-removal-sync"); err != nil {
		return StorageLayout{}, err
	}
	if err = r.check(ctx, owned, "receipt-removed"); err != nil {
		return StorageLayout{}, err
	}
	return current, checkStorageOwnership(ctx, owned)
}

func (r storageCutoverRunner) cleanupStage(ctx context.Context, owned OwnedStorage, stage string, release func() error) error {
	if err := r.check(ctx, owned, "before-scratch-removal"); err != nil {
		return err
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	allowed := map[string]bool{"source.db": true, "source.db-wal": true, "source.db-shm": true, "control.db": true, "control.db-journal": true, "recert-source.db": true, "recert-source.db-wal": true, "recert-source.db-shm": true, "recert-control.db": true, "recert-control.db-journal": true, "runtime-data.lock": true, "server.lock": true}
	for _, entry := range entries {
		if !allowed[entry.Name()] || !entry.Type().IsRegular() {
			return ErrTarget
		}
		if strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		if err = os.Remove(filepath.Join(stage, entry.Name())); err != nil {
			return err
		}
	}
	if err = r.sync(ctx, owned, stage, "scratch-files-removed"); err != nil {
		return err
	}
	if err = release(); err != nil {
		return err
	}
	for _, name := range []string{"runtime-data.lock", "server.lock"} {
		if err = os.Remove(filepath.Join(stage, name)); err != nil {
			return err
		}
	}
	if err = os.Remove(stage); err != nil {
		return err
	}
	if err = r.sync(ctx, owned, owned.Directory, "scratch-directory-removed"); err != nil {
		return err
	}
	return nil
}

func verifyCutoverOriginalLocations(ctx context.Context, owned OwnedStorage, receipt roleCutoverReceipt) error {
	if len(receipt.Layout.Archives) == 0 {
		return verifyCutoverInventory(ctx, owned.Directory, nil)
	}
	archive := filepath.Join(owned.Directory, receipt.Layout.Archives[0].RelativeDirectory)
	for _, directory := range []string{filepath.Join(owned.Directory, "archives"), archive} {
		if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := checkCutoverDirectory(directory); err != nil {
			return err
		}
	}
	expected := map[string]cutoverArtifact{}
	for _, file := range receipt.Files {
		expected[file.Name] = file
	}
	for _, name := range []string{"runtime.db", "runtime.db-wal", "runtime.db-shm", "runtime.db-journal"} {
		rootPath, archivePath := filepath.Join(owned.Directory, name), filepath.Join(archive, name)
		_, rootErr := os.Lstat(rootPath)
		_, archiveErr := os.Lstat(archivePath)
		file, required := expected[name]
		if !required {
			if !errors.Is(rootErr, os.ErrNotExist) || !errors.Is(archiveErr, os.ErrNotExist) {
				return ErrTarget
			}
			continue
		}
		var chosen string
		if rootErr == nil && errors.Is(archiveErr, os.ErrNotExist) {
			chosen = rootPath
		} else if errors.Is(rootErr, os.ErrNotExist) && archiveErr == nil {
			chosen = archivePath
		} else {
			return ErrTarget
		}
		if err := verifyCutoverArtifact(ctx, chosen, file); err != nil {
			return err
		}
	}
	return nil
}
