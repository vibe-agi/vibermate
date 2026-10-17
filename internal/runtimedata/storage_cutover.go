package runtimedata

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

const roleCutoverSchema = "runtime-role-cutover/v1"

type cutoverArtifact struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type roleCutoverReceipt struct {
	Schema           string                         `json:"schema"`
	Phase            string                         `json:"phase"`
	Layout           StorageLayout                  `json:"layout"`
	CreatedAt        string                         `json:"createdAt"`
	Source           runtimepersistence.SchemaState `json:"source"`
	Files            []cutoverArtifact              `json:"files"`
	ControlSHA256    string                         `json:"controlSHA256"`
	ProjectionSHA256 string                         `json:"projectionSHA256"`
}
type archiveManifest struct {
	Schema            string            `json:"schema"`
	Archive           LegacyArchive     `json:"archive"`
	Files             []cutoverArtifact `json:"files"`
	Certification     string            `json:"certification"`
	ProjectionSHA256  string            `json:"projectionSHA256"`
	ArchiveEpoch      uint64            `json:"archiveEpoch"`
	CaptureGeneration uint64            `json:"captureGeneration"`
}
type storageCutoverRunner struct {
	barrier        func(context.Context, string) error
	stageOwnership func() bool
	syncPath       func(string) error
	copyFile       func(context.Context, string, string, os.FileInfo) error
	extract        func(context.Context, runtimepersistence.ControlExtraction) (runtimepersistence.ControlExtractionProof, error)
}

func newStorageCutoverRunner() storageCutoverRunner {
	return storageCutoverRunner{copyFile: copyVerified, syncPath: newSchemaSnapshotRunner().syncPath, barrier: func(ctx context.Context, _ string) error { return ctx.Err() }, extract: runtimepersistence.ExtractControl}
}
func PrepareStorageOwned(ctx context.Context, owned OwnedStorage, now time.Time, progress func(StoragePhase) error) (StorageLayout, error) {
	return newStorageCutoverRunner().prepare(ctx, owned, now, progress)
}

func (r storageCutoverRunner) prepare(ctx context.Context, owned OwnedStorage, now time.Time, progress func(StoragePhase) error) (layout StorageLayout, result error) {
	defer func() {
		if result != nil {
			layout = StorageLayout{}
			result = errors.Join(ErrSchemaUpgradeIncomplete, result)
		}
	}()
	if ctx == nil || now.IsZero() || now.Location() != time.UTC {
		return layout, ErrTarget
	}
	work, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if err := checkStorageOwnership(work, owned); err != nil {
		return layout, err
	}
	if progress != nil {
		if err := progress(SchemaUpgradePreparing); err != nil {
			return layout, err
		}
	}
	root := owned.Directory
	encoded, pending, err := readStorageDocument(filepath.Join(root, schemaUpgradeReceiptName))
	if err != nil {
		return layout, err
	}
	upgrade := func() error {
		if progress != nil {
			return progress(SchemaUpgradeRunning)
		}
		return work.Err()
	}
	if !pending {
		declared, err := ReadStorageLayout(root)
		if err == nil {
			if _, err = runtimepersistence.InspectControl(work, filepath.Join(root, declared.Control.RelativePath), declared.ID); err != nil {
				return layout, err
			}
			if err = r.syncPath(root); err != nil {
				return layout, err
			}
			return declared, checkStorageOwnership(work, owned)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return layout, err
		}
	}
	if err = upgrade(); err != nil {
		return layout, err
	}
	if pending {
		var discriminator struct {
			Schema string `json:"schema"`
		}
		if err = json.Unmarshal(encoded, &discriminator); err != nil {
			return layout, ErrBackupInvalid
		}
		if discriminator.Schema == "runtime-schema-upgrade-receipt/v1" {
			if _, err = UpgradeSchemaOwned(work, root, "", owned.Runtime, owned.Host, now, 15*time.Minute, nil); err != nil {
				return layout, err
			}
			pending = false
		} else if discriminator.Schema != roleCutoverSchema {
			return layout, ErrBackupInvalid
		}
	}
	var receipt roleCutoverReceipt
	if pending {
		if err = decodeStorageDocument(encoded, &receipt); err != nil || !validRoleCutoverReceipt(receipt) {
			return layout, ErrBackupInvalid
		}
	} else {
		// Reserved artifacts without a matching receipt/layout are never a fresh root.
		for _, name := range []string{"observations.db", "observations.db-wal", "observations.db-shm", "archives", "runtime.db-journal"} {
			if _, err = os.Lstat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
				return layout, ErrTarget
			}
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return layout, err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".storage-cutover-") {
				return layout, ErrTarget
			}
		}
		state, err := runtimepersistence.InspectSchema(work, filepath.Join(root, "runtime.db"), runtimepersistence.DefaultBusyTimeout)
		if err != nil {
			return layout, err
		}
		if state.Revision != 0 && state.Revision != 1 && state.Revision != 2 {
			return layout, runtimepersistence.ErrUnsupportedSchema
		}
		files, err := cutoverInventory(work, root)
		if err != nil {
			return layout, err
		}
		if state.Revision == 0 && len(files) != 0 || state.Revision != 0 && (len(files) == 0 || files[0].Size == 0) {
			return layout, ErrTarget
		}
		id, err := newStorageID()
		if err != nil {
			return layout, err
		}
		receipt = roleCutoverReceipt{Schema: roleCutoverSchema, Phase: "preparing", CreatedAt: now.Format(time.RFC3339Nano), Source: state, Files: files, Layout: StorageLayout{ID: id, Control: DatabaseRole{"runtime.db", "vibermate-control", 3, true}, Observation: DatabaseRole{"observations.db", "vibermate-observations", 1, false}, Archives: []LegacyArchive{}}}
		if state.Revision != 0 {
			archiveID := "legacy-" + id
			receipt.Layout.Archives = []LegacyArchive{{ID: archiveID, RelativeDirectory: "archives/" + archiveID, Database: state}}
		}
		if err = r.writeReceipt(work, owned, receipt); err != nil {
			return layout, err
		}
	}
	if receipt.Phase == "activated" {
		return r.finishActivated(work, owned, receipt)
	}
	stage := filepath.Join(root, ".storage-cutover-"+receipt.Layout.ID)
	if err = ensureCutoverDirectory(stage); err != nil {
		return layout, err
	}
	stageOwner, stageHost, err := lockSchemaSnapshot(stage)
	if err != nil {
		return layout, err
	}
	defer stageOwner.Release()
	defer stageHost.Release()
	stageHeld := true
	r.stageOwnership = func() bool {
		return !stageHeld || snapshotOwns(stageOwner, stage, lockName) && snapshotOwns(stageHost, stage, "server.lock")
	}
	if !r.stageOwnership() {
		return layout, ErrTarget
	}
	stagedControl := filepath.Join(stage, "control.db")
	if receipt.Phase != "preparing" {
		if err = r.recertify(work, owned, receipt, stage); err != nil {
			return layout, err
		}
	}
	if receipt.Phase == "preparing" {
		if err = clearCutoverScratch(stage); err != nil {
			return layout, err
		}
		if err = verifyCutoverInventory(work, root, receipt.Files); err != nil {
			return layout, err
		}
		created, _ := time.Parse(time.RFC3339Nano, receipt.CreatedAt)
		if receipt.Source.Revision == 0 {
			err = runtimepersistence.CreateStagedControl(work, stagedControl, receipt.Layout.ID, created)
		} else {
			for _, file := range receipt.Files {
				if file.Name == "runtime.db-shm" {
					continue
				}
				from := filepath.Join(root, file.Name)
				to := filepath.Join(stage, "source.db"+strings.TrimPrefix(file.Name, "runtime.db"))
				info, statErr := os.Lstat(from)
				if statErr != nil {
					return layout, statErr
				}
				if err = r.copyFile(work, from, to, info); err != nil {
					return layout, err
				}
			}
			if err = r.check(work, owned, "source-copy"); err != nil {
				return layout, err
			}
			proof, extractErr := r.extract(work, runtimepersistence.ControlExtraction{SourcePath: filepath.Join(stage, "source.db"), TargetPath: stagedControl, LayoutID: receipt.Layout.ID, ArchiveID: receipt.Layout.Archives[0].ID})
			if extractErr != nil {
				return layout, extractErr
			}
			if proof.Source != receipt.Source {
				return layout, ErrBackupInvalid
			}
			receipt.ProjectionSHA256 = hex.EncodeToString(proof.ProjectionDigest[:])
		}
		if err != nil {
			return layout, err
		}
		if err = noControlSidecars(stagedControl); err != nil {
			return layout, err
		}
		if _, err = runtimepersistence.InspectControl(work, stagedControl, receipt.Layout.ID); err != nil {
			return layout, err
		}
		if err = verifyCutoverInventory(work, root, receipt.Files); err != nil {
			return layout, err
		}
		target, err := hashCutoverArtifact(work, stagedControl, "control.db")
		if err != nil {
			return layout, err
		}
		receipt.ControlSHA256 = target.SHA256
		if err = r.sync(work, owned, stagedControl, "control-file-sync"); err != nil {
			return layout, err
		}
		if err = r.sync(work, owned, stage, "control-stage-sync"); err != nil {
			return layout, err
		}
		if err = r.check(work, owned, "control-certified"); err != nil {
			return layout, err
		}
		receipt.Phase = "ready"
		if err = r.writeReceipt(work, owned, receipt); err != nil {
			return layout, err
		}
	}
	if receipt.Phase == "ready" {
		if err = verifyCutoverOriginalLocations(work, owned, receipt); err != nil {
			return layout, err
		}
		if err = verifyStagedControl(work, stagedControl, receipt); err != nil {
			return layout, err
		}
		if len(receipt.Layout.Archives) != 0 {
			parent := filepath.Join(root, "archives")
			archive := filepath.Join(root, filepath.FromSlash(receipt.Layout.Archives[0].RelativeDirectory))
			if err = ensureCutoverDirectory(parent); err != nil {
				return layout, err
			}
			if err = r.sync(work, owned, root, "archive-parent-created-sync"); err != nil {
				return layout, err
			}
			if err = ensureCutoverDirectory(archive); err != nil {
				return layout, err
			}
			if err = r.sync(work, owned, parent, "archive-directory-created-sync"); err != nil {
				return layout, err
			}
			for _, file := range receipt.Files {
				from, to := filepath.Join(root, file.Name), filepath.Join(archive, file.Name)
				_, sourceErr := os.Lstat(from)
				_, targetErr := os.Lstat(to)
				switch {
				case sourceErr == nil && errors.Is(targetErr, os.ErrNotExist):
					if err = verifyCutoverArtifact(work, from, file); err != nil {
						return layout, err
					}
					if err = r.check(work, owned, "before-archive-"+file.Name); err != nil {
						return layout, err
					}
					if err = os.Rename(from, to); err != nil {
						return layout, err
					}
				case errors.Is(sourceErr, os.ErrNotExist) && targetErr == nil:
					if err = verifyCutoverArtifact(work, to, file); err != nil {
						return layout, err
					}
				default:
					return layout, ErrTarget
				}
				if err = r.sync(work, owned, archive, "archive-child-sync-"+file.Name); err != nil {
					return layout, err
				}
				if err = r.sync(work, owned, root, "archive-root-sync-"+file.Name); err != nil {
					return layout, err
				}
				if err = r.check(work, owned, "archive-"+file.Name); err != nil {
					return layout, err
				}
			}
			if err = verifyCutoverInventory(work, archive, receipt.Files); err != nil {
				return layout, err
			}
			manifest := expectedArchiveManifest(receipt)
			manifestPath := filepath.Join(archive, "archive-manifest.json")
			if _, err = verifyArchiveManifest(manifestPath, manifest); err != nil {
				return layout, err
			}
			if err = r.writeJSON(work, owned, manifestPath, manifest, "archive-manifest"); err != nil {
				return layout, err
			}

			if err = r.sync(work, owned, parent, "archives-parent-sync"); err != nil {
				return layout, err
			}
			if err = r.sync(work, owned, root, "archives-root-sync"); err != nil {
				return layout, err
			}
		}
		receipt.Phase = "archived"
		if err = r.writeReceipt(work, owned, receipt); err != nil {
			return layout, err
		}
	}
	if receipt.Phase == "archived" {
		if err = verifyCutoverArchive(work, owned, receipt); err != nil {
			return layout, err
		}
		final := filepath.Join(root, "runtime.db")
		_, sourceErr := os.Lstat(stagedControl)
		_, targetErr := os.Lstat(final)
		switch {
		case sourceErr == nil && errors.Is(targetErr, os.ErrNotExist):
			if err = verifyStagedControl(work, stagedControl, receipt); err != nil {
				return layout, err
			}
			if err = r.check(work, owned, "before-control-publication"); err != nil {
				return layout, err
			}
			if err = os.Rename(stagedControl, final); err != nil {
				return layout, err
			}
		case errors.Is(sourceErr, os.ErrNotExist) && targetErr == nil:
			if err = verifyStagedControl(work, final, receipt); err != nil {
				return layout, err
			}
		default:
			return layout, ErrTarget
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err = os.Lstat(final + suffix); !errors.Is(err, os.ErrNotExist) {
				return layout, ErrTarget
			}
		}
		if err = r.sync(work, owned, stage, "control-source-directory-sync"); err != nil {
			return layout, err
		}
		if err = r.sync(work, owned, root, "control-root-directory-sync"); err != nil {
			return layout, err
		}
		if err = r.check(work, owned, "control-published"); err != nil {
			return layout, err
		}
		existing, existsErr := ReadStorageLayout(root)
		if existsErr == nil && !reflect.DeepEqual(existing, receipt.Layout) {
			return layout, ErrTarget
		}
		if existsErr != nil && !errors.Is(existsErr, os.ErrNotExist) {
			return layout, existsErr
		}
		if err = r.writeJSON(work, owned, filepath.Join(root, storageLayoutName), storageLayoutDocument{storageLayoutSchema, receipt.Layout}, "layout-published"); err != nil {
			return layout, err
		}
		if err = verifyCutoverArchive(work, owned, receipt); err != nil {
			return layout, err
		}
		if err = r.cleanupStage(work, owned, stage, func() error {
			err := errors.Join(stageHost.Release(), stageOwner.Release())
			stageHeld = false
			return err
		}); err != nil {
			return layout, err
		}
		receipt.Phase = "activated"
		if err = r.writeReceipt(work, owned, receipt); err != nil {
			return layout, err
		}
	}
	return r.finishActivated(work, owned, receipt)
}

func checkStorageOwnership(ctx context.Context, owned OwnedStorage) error {
	if err := snapshotDirectory(ctx, owned.Directory); err != nil {
		return err
	}
	if !snapshotOwns(owned.Runtime, owned.Directory, lockName) || !snapshotOwns(owned.Host, owned.Directory, "server.lock") {
		return ErrTarget
	}
	return ctx.Err()
}
func (r storageCutoverRunner) check(ctx context.Context, owned OwnedStorage, name string) error {
	if err := checkStorageOwnership(ctx, owned); err != nil {
		return err
	}
	if r.stageOwnership != nil && !r.stageOwnership() {
		return ErrTarget
	}
	if err := r.barrier(ctx, name); err != nil {
		return err
	}
	if r.stageOwnership != nil && !r.stageOwnership() {
		return ErrTarget
	}
	return checkStorageOwnership(ctx, owned)
}
func (r storageCutoverRunner) sync(ctx context.Context, owned OwnedStorage, path, name string) error {
	if err := r.check(ctx, owned, "before-"+name); err != nil {
		return err
	}
	if err := r.syncPath(path); err != nil {
		return err
	}
	return r.check(ctx, owned, name)
}
func (r storageCutoverRunner) writeReceipt(ctx context.Context, owned OwnedStorage, receipt roleCutoverReceipt) error {
	return r.writeJSON(ctx, owned, filepath.Join(owned.Directory, schemaUpgradeReceiptName), receipt, "receipt-"+receipt.Phase)
}
func (r storageCutoverRunner) writeJSON(ctx context.Context, owned OwnedStorage, path string, value any, name string) error {
	if err := r.check(ctx, owned, "before-"+name); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 64<<10 {
		return ErrBackupInvalid
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600) || err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrTarget
	}
	parent := filepath.Dir(path)
	if err = checkCutoverDirectory(parent); err != nil {
		return err
	}
	file, err := os.CreateTemp(parent, ".storage-document-")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err = file.Write(encoded); err != nil {
		file.Close()
		return err
	}
	if err = errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	if err = r.check(ctx, owned, name+"-file-sync"); err != nil {
		return err
	}
	if err = os.Rename(temporary, path); err != nil {
		return err
	}
	if err = r.check(ctx, owned, name+"-renamed"); err != nil {
		return err
	}
	if err = r.sync(ctx, owned, parent, name+"-directory-sync"); err != nil {
		return err
	}
	return r.check(ctx, owned, name)
}
func newStorageID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func checkCutoverDirectory(path string) error {
	physical, err := filepath.EvalSymlinks(path)
	if err != nil || physical != path {
		return ErrTarget
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return ErrTarget
	}
	return nil
}
func ensureCutoverDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return checkCutoverDirectory(path)
}
func clearCutoverScratch(stage string) error {
	entries, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	allowed := map[string]bool{"runtime-data.lock": true, "server.lock": true, "source.db": true, "source.db-wal": true, "source.db-shm": true, "control.db": true, "control.db-journal": true}
	for _, e := range entries {
		if !allowed[e.Name()] || !e.Type().IsRegular() {
			return ErrTarget
		}
		if strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		if err = os.Remove(filepath.Join(stage, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
func hashCutoverArtifact(ctx context.Context, path, name string) (cutoverArtifact, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return cutoverArtifact{}, ErrTarget
	}
	file, err := os.Open(path)
	if err != nil {
		return cutoverArtifact{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return cutoverArtifact{}, ErrTarget
	}
	h := sha256.New()
	n, err := io.Copy(h, &contextReader{ctx, file})
	if err != nil {
		return cutoverArtifact{}, err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || n != info.Size() || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return cutoverArtifact{}, ErrTarget
	}
	return cutoverArtifact{Name: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}
func cutoverInventory(ctx context.Context, root string) ([]cutoverArtifact, error) {
	var files []cutoverArtifact
	for _, name := range []string{"runtime.db", "runtime.db-wal", "runtime.db-shm"} {
		path := filepath.Join(root, name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		item, err := hashCutoverArtifact(ctx, path, name)
		if err != nil {
			return nil, err
		}
		files = append(files, item)
	}
	return files, nil
}
func verifyCutoverArtifact(ctx context.Context, path string, expected cutoverArtifact) error {
	actual, err := hashCutoverArtifact(ctx, path, expected.Name)
	if err != nil {
		return err
	}
	if actual != expected {
		return ErrBackupInvalid
	}
	return nil
}
func verifyCutoverInventory(ctx context.Context, root string, expected []cutoverArtifact) error {
	actual, err := cutoverInventory(ctx, root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, expected) {
		return ErrBackupInvalid
	}
	if _, err = os.Lstat(filepath.Join(root, "runtime.db-journal")); !errors.Is(err, os.ErrNotExist) {
		return ErrTarget
	}
	return nil
}
func verifyStagedControl(ctx context.Context, path string, r roleCutoverReceipt) error {
	if err := noControlSidecars(path); err != nil {
		return err
	}
	actual, err := hashCutoverArtifact(ctx, path, "control.db")
	if err != nil {
		return err
	}
	if actual.SHA256 != r.ControlSHA256 {
		return ErrBackupInvalid
	}
	state, err := runtimepersistence.InspectControl(ctx, path, r.Layout.ID)
	if err != nil {
		return err
	}
	expected := r.Source.InitializedAt
	if r.Source.Revision == 0 {
		expected = r.CreatedAt
	}
	if state.InitializedAt != expected {
		return ErrBackupInvalid
	}
	return nil
}
func validRoleCutoverReceipt(r roleCutoverReceipt) bool {
	if r.Schema != roleCutoverSchema || !validStorageLayout(r.Layout) || len(r.Files) > 3 {
		return false
	}
	created, err := time.Parse(time.RFC3339Nano, r.CreatedAt)
	if err != nil || created.IsZero() || created.Location() != time.UTC {
		return false
	}
	if r.Source.Revision == 0 {
		if r.Source != (runtimepersistence.SchemaState{}) || len(r.Files) != 0 || len(r.Layout.Archives) != 0 {
			return false
		}
	} else {
		if len(r.Layout.Archives) != 1 || r.Layout.Archives[0].Database != r.Source || len(r.Files) == 0 || r.Files[0].Name != "runtime.db" {
			return false
		}
	}
	switch r.Phase {
	case "preparing":
	case "ready", "archived", "activated":
		if !cutoverDigest(r.ControlSHA256) || r.Source.Revision != 0 && !cutoverDigest(r.ProjectionSHA256) {
			return false
		}
	default:
		return false
	}
	seen := map[string]bool{}
	for _, f := range r.Files {
		if f.Name != "runtime.db" && f.Name != "runtime.db-wal" && f.Name != "runtime.db-shm" || seen[f.Name] || f.Size < 0 || !cutoverDigest(f.SHA256) {
			return false
		}
		seen[f.Name] = true
	}
	return true
}
func cutoverDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}
