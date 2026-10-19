package runtimedata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

const backupManifestSchemaV2 = "vibermate-data-backup/v2"
const schemaSnapshotManifestSchemaV2 = "runtime-schema-upgrade/v2"

type backupRole struct {
	Path      string `json:"path"`
	Identity  string `json:"identity"`
	Revision  int64  `json:"revision"`
	Required  bool   `json:"required"`
	Status    string `json:"status"`
	ArchiveID string `json:"archiveId,omitempty"`
}

// V1 omits this entire extension. V2 binds the bounded layout document and its
// ordered roles, including absent history; file hashes do not imply completeness.
type backupRoles struct {
	LayoutID        string                 `json:"layoutId,omitempty"`
	Layout          *storageLayoutDocument `json:"layout,omitempty"`
	Roles           []backupRole           `json:"roles,omitempty"`
	HistoryComplete *bool                  `json:"historyComplete,omitempty"`
}

func currentBackupSourceID(layout string) string {
	digest := sha256.Sum256([]byte(layout))
	return "current-" + hex.EncodeToString(digest[:16])
}
func roleSHMPaths(root string) map[string]bool {
	paths := map[string]bool{"runtime.db-shm": true}
	if layout, err := ReadStorageLayout(root); err == nil {
		paths[layout.Observation.RelativePath+"-shm"] = true
		for _, a := range layout.Archives {
			paths[a.RelativeDirectory+"/runtime.db-shm"] = true
		}
	}
	return paths
}
func refusePendingExport(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ErrBackupInvalid
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == schemaUpgradeReceiptName || name == RecordingCleanupReceiptName || strings.HasPrefix(name, ".recording-cleanup-") || strings.HasPrefix(name, ".recording-certificate-") {
			return ErrSchemaUpgradeIncomplete
		}
	}
	return nil
}

// inspectBackupRoles never starts a normal writer, initializes authority, or
// keeps control SQL open while validating optional sources.
func inspectBackupRoles(ctx context.Context, root string) (runtimepersistence.SchemaState, backupRoles, error) {
	layout, err := ReadStorageLayout(root)
	if errors.Is(err, os.ErrNotExist) {
		state, err := runtimepersistence.ValidateOfflineDatabase(ctx, filepath.Join(root, "runtime.db"))
		return state, backupRoles{}, err
	}
	if err != nil {
		return runtimepersistence.SchemaState{}, backupRoles{}, err
	}
	if err = refusePendingExport(root); err != nil {
		return runtimepersistence.SchemaState{}, backupRoles{}, err
	}
	control, err := runtimepersistence.ValidateOfflineRole(ctx, filepath.Join(root, "runtime.db"), layout.Control.Identity, layout.Control.Revision, layout.ID, "", recording.InspectionStamp{})
	if err != nil {
		return control.Database, backupRoles{}, err
	}
	cleanup := control.Cleanup
	if cleanup.RequestedRevision != cleanup.CompletedRevision || cleanup.ActiveRevision != 0 || cleanup.SourceGeneration == 0 {
		return control.Database, backupRoles{}, ErrSchemaUpgradeIncomplete
	}
	complete := true
	metadata := backupRoles{LayoutID: layout.ID, Layout: &storageLayoutDocument{Schema: storageLayoutSchema, StorageLayout: layout}, HistoryComplete: &complete}
	metadata.Roles = append(metadata.Roles, backupRole{Path: layout.Control.RelativePath, Identity: layout.Control.Identity, Revision: layout.Control.Revision, Required: true, Status: "verified"})
	currentID := currentBackupSourceID(layout.ID)
	if err = validateBackupProvision(root, layout, currentID); err != nil {
		return control.Database, backupRoles{}, err
	}
	observation := backupRole{Path: layout.Observation.RelativePath, Identity: layout.Observation.Identity, Revision: layout.Observation.Revision, ArchiveID: currentID, Status: "verified"}
	info, err := os.Lstat(filepath.Join(root, observation.Path))
	if errors.Is(err, os.ErrNotExist) {
		if control.Stamp.DenialCount != 0 || control.Stamp.DenialHash != ([32]byte{}) {
			return control.Database, backupRoles{}, runtimepersistence.ErrRecordingPrivacyUnavailable
		}
		observation.Status = "absent"
		complete = false
	} else if err != nil || !info.Mode().IsRegular() {
		return control.Database, backupRoles{}, ErrBackupInvalid
	} else if _, err = runtimepersistence.ValidateOfflineRole(ctx, filepath.Join(root, observation.Path), observation.Identity, observation.Revision, layout.ID, currentID, control.Stamp); err != nil {
		return control.Database, backupRoles{}, err
	}
	metadata.Roles = append(metadata.Roles, observation)
	certificate, exists, err := ReadRecordingMaintenanceCertificate(root)
	if err != nil {
		return control.Database, backupRoles{}, err
	}
	sources := make([]runtimepersistence.InspectionSource, 0, len(layout.Archives)+1)
	for _, a := range layout.Archives {
		sources = append(sources, runtimepersistence.InspectionSource{ID: a.ID, RelativePath: a.RelativeDirectory + "/runtime.db", Database: a.Database})
	}
	sources = append(sources, runtimepersistence.InspectionSource{ID: currentID, RelativePath: observation.Path, Database: runtimepersistence.SchemaState{Identity: observation.Identity, Revision: observation.Revision}, Current: true})
	catalog, err := runtimepersistence.NewInspectionCatalog(runtimepersistence.InspectionCatalogOptions{Root: root, LayoutID: layout.ID, Sources: sources, Generation: cleanup.SourceGeneration})
	if err != nil {
		return control.Database, backupRoles{}, err
	}
	version := catalog.Version()
	if err = catalog.Close(ctx); err != nil {
		return control.Database, backupRoles{}, err
	}
	if exists {
		if certificate.LayoutID != layout.ID || certificate.SourceSet != version.SourceSet || certificate.SourceGeneration != cleanup.SourceGeneration || certificate.TargetRevision != cleanup.CompletedRevision || certificate.CutoffMillis != cleanup.CutoffMillis || len(certificate.Sources) != len(sources) {
			return control.Database, backupRoles{}, ErrBackupInvalid
		}
		for i, source := range sources {
			certified := certificate.Sources[i]
			if certified.ID != source.ID || certified.RelativePath != source.RelativePath || certified.Database != source.Database {
				return control.Database, backupRoles{}, ErrBackupInvalid
			}
		}
	} else if cleanup.SourceGeneration != 1 || cleanup.CompletedRevision != 0 {
		return control.Database, backupRoles{}, ErrBackupInvalid
	}
	for i, a := range layout.Archives {
		role := backupRole{Path: a.RelativeDirectory + "/runtime.db", Identity: a.Database.Identity, Revision: a.Database.Revision, ArchiveID: a.ID, Status: "unverified"}
		complete = false
		if _, err = os.Lstat(filepath.Join(root, role.Path)); errors.Is(err, os.ErrNotExist) {
			role.Status = "absent"
			complete = false
		} else {
			state, err := runtimepersistence.ValidateOfflineDatabase(ctx, filepath.Join(root, role.Path))
			if err != nil || state != a.Database {
				return control.Database, backupRoles{}, errors.Join(ErrBackupIncompatible, err)
			}
			var maintained *RecordingSourceCertificate
			if exists {
				maintained = &certificate.Sources[i]
			}
			if err = verifyBackupArchive(ctx, root, a, maintained); err != nil {
				return control.Database, backupRoles{}, err
			}
		}
		metadata.Roles = append(metadata.Roles, role)
	}
	return control.Database, metadata, nil
}

func validateBackupProvision(root string, layout StorageLayout, sourceID string) error {
	var receipt struct {
		Schema      string `json:"schema"`
		LayoutID    string `json:"layoutId"`
		SourceID    string `json:"sourceId"`
		Provisioned *bool  `json:"provisioned"`
	}
	name := filepath.Join(root, "recording-state.json")
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) && layout.ObservationProvisioning == "unprovisioned" {
		if _, err = os.Lstat(filepath.Join(root, layout.Observation.RelativePath)); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return ErrBackupInvalid
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 4096 {
		return ErrBackupInvalid
	}
	data, err := os.ReadFile(name)
	if err != nil || decodeStorageDocument(data, &receipt) != nil || receipt.Schema != "vibermate-recording-provision/v1" || receipt.LayoutID != layout.ID || receipt.SourceID != sourceID || receipt.Provisioned == nil {
		return ErrBackupInvalid
	}
	if layout.ObservationProvisioning == "provisioned" && *receipt.Provisioned {
		return nil
	}
	if layout.ObservationProvisioning == "unprovisioned" && !*receipt.Provisioned {
		if _, err = os.Lstat(filepath.Join(root, layout.Observation.RelativePath)); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	return ErrBackupInvalid
}

func verifyBackupArchive(ctx context.Context, root string, archive LegacyArchive, maintained *RecordingSourceCertificate) error {
	name := filepath.Join(root, archive.RelativeDirectory, "archive-manifest.json")
	data, exists, err := readStorageDocument(name)
	if err != nil || !exists {
		return ErrBackupInvalid
	}
	var provenance archiveManifest
	if decodeStorageDocument(data, &provenance) != nil || provenance.Schema != "vibermate-legacy-archive/v1" || !reflect.DeepEqual(provenance.Archive, archive) || provenance.ArchiveEpoch != 1 || provenance.CaptureGeneration != 1 || provenance.Certification != "required-control-projection-and-original-bytes/v1" || !recordingCertificateHash(provenance.ProjectionSHA256) || len(provenance.Files) < 1 || len(provenance.Files) > 3 {
		return ErrBackupInvalid
	}
	seen := map[string]bool{}
	for _, file := range provenance.Files {
		if seen[file.Name] || (file.Name != "runtime.db" && file.Name != "runtime.db-wal" && file.Name != "runtime.db-shm") || file.Size < 0 || !recordingCertificateHash(file.SHA256) {
			return ErrBackupInvalid
		}
		seen[file.Name] = true
	}
	if !seen["runtime.db"] {
		return ErrBackupInvalid
	}
	if maintained != nil {
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != maintained.ProvenanceSHA256 {
			return ErrBackupInvalid
		}
		file, err := hashCutoverArtifact(ctx, filepath.Join(root, archive.RelativeDirectory, "runtime.db"), "runtime.db")
		if err != nil || file.Size != maintained.Bytes || file.SHA256 != maintained.SHA256 {
			return ErrBackupInvalid
		}
		for _, suffix := range []string{"-wal", "-journal"} {
			info, err := os.Lstat(filepath.Join(root, archive.RelativeDirectory, "runtime.db"+suffix))
			if !errors.Is(err, os.ErrNotExist) && (err != nil || !info.Mode().IsRegular() || info.Size() != 0) {
				return ErrBackupInvalid
			}
		}
		return nil
	}
	for _, file := range provenance.Files {
		if file.Name == "runtime.db-shm" {
			continue
		}
		if err = verifyCutoverArtifact(ctx, filepath.Join(root, archive.RelativeDirectory, file.Name), file); err != nil {
			return err
		}
	}
	for _, suffix := range []string{"-wal", "-journal"} {
		info, err := os.Lstat(filepath.Join(root, archive.RelativeDirectory, "runtime.db"+suffix))
		if !seen["runtime.db"+suffix] && !errors.Is(err, os.ErrNotExist) && (err != nil || !info.Mode().IsRegular() || info.Size() != 0) {
			return ErrBackupInvalid
		}
	}
	return nil
}

func validBackupRolesVersion(v2 bool, roles backupRoles) bool {
	if !v2 {
		return roles.LayoutID == "" && roles.Layout == nil && roles.Roles == nil && roles.HistoryComplete == nil
	}
	if roles.Layout == nil || roles.Layout.Schema != storageLayoutSchema || roles.LayoutID != roles.Layout.ID || !validStorageLayout(roles.Layout.StorageLayout) || roles.HistoryComplete == nil || len(roles.Roles) != 2+len(roles.Layout.Archives) {
		return false
	}
	return true
}
