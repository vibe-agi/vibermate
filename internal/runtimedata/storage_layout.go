package runtimedata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

type StoragePhase = SchemaUpgradePhase

type OwnedStorage struct {
	Directory     string
	Runtime, Host *instanceguard.Guard
}
type DatabaseRole struct {
	RelativePath string `json:"relativePath"`
	Identity     string `json:"identity"`
	Revision     int64  `json:"revision"`
	Required     bool   `json:"required"`
}
type LegacyArchive struct {
	ID                string                         `json:"id"`
	RelativeDirectory string                         `json:"relativeDirectory"`
	Database          runtimepersistence.SchemaState `json:"database"`
}
type StorageLayout struct {
	// Empty is an older undeclared optional role, never permission to recreate history.
	ObservationProvisioning string          `json:"observationProvisioning,omitempty"`
	ID                      string          `json:"id"`
	Control                 DatabaseRole    `json:"control"`
	Observation             DatabaseRole    `json:"observation"`
	Archives                []LegacyArchive `json:"archives"`
}

const storageLayoutName = "storage-layout.json"
const storageLayoutSchema = "vibermate-storage-layout/v1"

type storageLayoutDocument struct {
	Schema string `json:"schema"`
	StorageLayout
}

// ReadStorageLayout reads bounded declared metadata only. Archive existence and
// health are deliberately inspected later by independent inspection owners.
func ReadStorageLayout(root string) (StorageLayout, error) {
	encoded, exists, err := readStorageDocument(filepath.Join(root, storageLayoutName))
	if err != nil {
		return StorageLayout{}, err
	}
	if !exists {
		return StorageLayout{}, os.ErrNotExist
	}
	var document storageLayoutDocument
	if err = decodeStorageDocument(encoded, &document); err != nil || document.Schema != storageLayoutSchema || !validStorageLayout(document.StorageLayout) {
		return StorageLayout{}, ErrBackupInvalid
	}
	return document.StorageLayout, nil
}
func validStorageID(value string) bool {
	return len(value) > 0 && len(value) <= 128 && utf8.ValidString(value) && !strings.ContainsAny(value, "/\\\x00") && value != "." && value != ".."
}
func validStorageLayout(l StorageLayout) bool {
	// Unknown optional provisioning states withhold recording, not control.
	if len(l.ObservationProvisioning) > 32 || !utf8.ValidString(l.ObservationProvisioning) {
		return false
	}
	if !validStorageID(l.ID) || l.Control != (DatabaseRole{"runtime.db", "vibermate-control", 3, true}) || l.Observation != (DatabaseRole{"observations.db", "vibermate-observations", 1, false}) || len(l.Archives) > 128 {
		return false
	}
	seen := map[string]bool{}
	for _, a := range l.Archives {
		initialized, err := time.Parse(time.RFC3339Nano, a.Database.InitializedAt)
		if !validStorageID(a.ID) || seen[a.ID] || a.RelativeDirectory != filepath.ToSlash(filepath.Join("archives", a.ID)) || a.Database.Identity != "vibermate-runtime" || (a.Database.Revision != 1 && a.Database.Revision != 2) || err != nil || initialized.IsZero() || initialized.Location() != time.UTC {
			return false
		}
		seen[a.ID] = true
	}
	return true
}
func decodeStorageDocument(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrBackupInvalid
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrBackupInvalid
	}
	return nil
}
func readStorageDocument(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 64<<10 {
		return nil, true, ErrBackupInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, true, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, true, ErrBackupInvalid
	}
	encoded, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(encoded) > 64<<10 {
		return nil, true, ErrBackupInvalid
	}
	return encoded, true, nil
}

// MarkObservationProvisioned makes first publication durable in owned layout
// metadata. No optional I/O runs while a control SQL transaction is held.
func MarkObservationProvisioned(ctx context.Context, root, layoutID string, guard *instanceguard.Guard) error {
	check := func() error {
		if ctx == nil || ctx.Err() != nil {
			return ErrSchemaUpgradeIncomplete
		}
		if guard == nil || guard.Path() != filepath.Join(root, lockName) || !guard.OwnsDirectory(root) {
			return ErrSchemaUpgradeIncomplete
		}
		return nil
	}
	if err := check(); err != nil {
		return err
	}
	layout, err := ReadStorageLayout(root)
	if err != nil {
		return err
	}
	if layout.ID != layoutID || (layout.ObservationProvisioning != "unprovisioned" && layout.ObservationProvisioning != "provisioned") {
		return ErrBackupInvalid
	}
	if layout.ObservationProvisioning == "unprovisioned" {
		layout.ObservationProvisioning = "provisioned"
		encoded, err := json.Marshal(storageLayoutDocument{Schema: storageLayoutSchema, StorageLayout: layout})
		if err != nil || len(encoded) > 64<<10 {
			return ErrBackupInvalid
		}
		file, err := os.CreateTemp(root, ".storage-document-")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		if _, err = file.Write(encoded); err != nil {
			file.Close()
			return err
		}
		if err = errors.Join(file.Sync(), file.Close()); err != nil {
			return err
		}
		if err = check(); err != nil {
			return err
		}
		if err = os.Rename(file.Name(), filepath.Join(root, storageLayoutName)); err != nil {
			return err
		}
	}
	// Also reconcile an earlier rename whose directory Sync outcome was unknown.
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	if err = errors.Join(directory.Sync(), directory.Close()); err != nil {
		return err
	}
	return check()
}
