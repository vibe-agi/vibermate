package runtimedata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

const RecordingMaintenanceCertificateName = "recording-maintenance.json"
const recordingCertificateLimit = 256 << 10

type RecordingSourceCertificate struct {
	ID               string                         `json:"id"`
	RelativePath     string                         `json:"relativePath"`
	Database         runtimepersistence.SchemaState `json:"database"`
	Bytes            int64                          `json:"bytes"`
	SHA256           string                         `json:"sha256"`
	ProvenanceSHA256 string                         `json:"provenanceSHA256,omitempty"`
}

// This records bytes at the completed managed-recording maintenance boundary.
// Current observations may subsequently grow; Task6 must recertify their live
// snapshot. Original archive provenance is linked, never rewritten or claimed
// byte-identical to the now-maintained database.
type RecordingMaintenanceCertificate struct {
	Schema           string                       `json:"schema"`
	LayoutID         string                       `json:"layoutId"`
	SourceSet        string                       `json:"sourceSet"`
	TargetRevision   uint64                       `json:"targetRevision"`
	SourceGeneration uint64                       `json:"sourceGeneration"`
	CutoffMillis     int64                        `json:"cutoffMillis"`
	CountsExact      bool                         `json:"countsExact"`
	Released         resourcedeletion.Released    `json:"released"`
	Sources          []RecordingSourceCertificate `json:"sources"`
}

// CertifyRecordingMaintenance is the file half of the existing exclusive
// maintenance protocol. Its caller has already verified schema, privacy chain,
// all predicates and SQLite integrity/checkpoints under catalog/writer exclusion.
// It creates no data copy and acknowledges no required cleanup claim.
func CertifyRecordingMaintenance(ctx context.Context, root string, guard *instanceguard.Guard, layout StorageLayout, receipt RecordingCleanupReceipt) error {
	if err := checkRecordingCleanupOwnership(ctx, root, guard); err != nil {
		return err
	}
	if !validStorageLayout(layout) || !validRecordingCleanupReceipt(receipt) || receipt.LayoutID != layout.ID || receipt.Phase != "verify" {
		return ErrBackupInvalid
	}
	certificate := RecordingMaintenanceCertificate{Schema: "vibermate-recording-maintenance/v1", LayoutID: layout.ID, SourceSet: receipt.SourceSet, TargetRevision: receipt.TargetRevision, SourceGeneration: receipt.SourceGeneration, CutoffMillis: receipt.CutoffMillis, CountsExact: receipt.CountsExact, Released: receipt.Released}
	for _, archive := range layout.Archives {
		provenancePath := filepath.Join(root, archive.RelativeDirectory, "archive-manifest.json")
		encoded, exists, err := readStorageDocument(provenancePath)
		if err != nil || !exists {
			return errors.Join(ErrBackupInvalid, err)
		}
		var provenance archiveManifest
		if decodeStorageDocument(encoded, &provenance) != nil || provenance.Schema != "vibermate-legacy-archive/v1" || !reflect.DeepEqual(provenance.Archive, archive) || provenance.ArchiveEpoch != 1 || provenance.CaptureGeneration != 1 || provenance.Certification != "required-control-projection-and-original-bytes/v1" || !recordingCertificateHash(provenance.ProjectionSHA256) {
			return ErrBackupInvalid
		}
		if len(provenance.Files) < 1 || len(provenance.Files) > 3 {
			return ErrBackupInvalid
		}
		for _, file := range provenance.Files {
			if (file.Name != "runtime.db" && file.Name != "runtime.db-wal" && file.Name != "runtime.db-shm") || file.Size < 0 || !recordingCertificateHash(file.SHA256) {
				return ErrBackupInvalid
			}
		}
		digest := sha256.Sum256(encoded)
		certificate.Sources = append(certificate.Sources, RecordingSourceCertificate{ID: archive.ID, RelativePath: archive.RelativeDirectory + "/runtime.db", Database: archive.Database, ProvenanceSHA256: hex.EncodeToString(digest[:])})
	}
	currentDigest := sha256.Sum256([]byte(layout.ID))
	certificate.Sources = append(certificate.Sources, RecordingSourceCertificate{ID: "current-" + hex.EncodeToString(currentDigest[:16]), RelativePath: layout.Observation.RelativePath, Database: runtimepersistence.SchemaState{Identity: layout.Observation.Identity, Revision: layout.Observation.Revision}})
	for i := range certificate.Sources {
		source := &certificate.Sources[i]
		path := filepath.Join(root, source.RelativePath)
		physical, err := filepath.EvalSymlinks(path)
		if err != nil || physical != path {
			return ErrBackupInvalid
		}
		for _, suffix := range []string{"-wal", "-journal", "-shm"} {
			info, err := os.Lstat(path + suffix)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || !info.Mode().IsRegular() || suffix != "-shm" && info.Size() != 0 {
				return errors.Join(ErrBackupInvalid, err)
			}
		}
		artifact, err := hashCutoverArtifact(ctx, path, source.RelativePath)
		if err != nil || artifact.Size == 0 {
			return errors.Join(ErrBackupInvalid, err)
		}
		source.Bytes, source.SHA256 = artifact.Size, artifact.SHA256
	}
	return writeRecordingCertificate(ctx, root, guard, certificate)
}
func recordingCertificateHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}
func writeRecordingCertificate(ctx context.Context, root string, guard *instanceguard.Guard, certificate RecordingMaintenanceCertificate) error {
	encoded, err := json.Marshal(certificate)
	if err != nil || len(encoded) > recordingCertificateLimit {
		return ErrBackupInvalid
	}
	path := filepath.Join(root, RecordingMaintenanceCertificateName)
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600) || err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrBackupInvalid
	}
	file, err := os.CreateTemp(root, ".recording-certificate-")
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
	if err = checkRecordingCleanupOwnership(ctx, root, guard); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
func ReadRecordingMaintenanceCertificate(root string) (certificate RecordingMaintenanceCertificate, exists bool, result error) {
	path := filepath.Join(root, RecordingMaintenanceCertificateName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return certificate, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > recordingCertificateLimit {
		return certificate, true, ErrBackupInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return certificate, true, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return certificate, true, ErrBackupInvalid
	}
	encoded, err := io.ReadAll(io.LimitReader(file, recordingCertificateLimit+1))
	if err != nil || len(encoded) > recordingCertificateLimit || decodeStorageDocument(encoded, &certificate) != nil {
		return certificate, true, ErrBackupInvalid
	}
	if certificate.Schema != "vibermate-recording-maintenance/v1" || !validStorageID(certificate.LayoutID) || !recordingCertificateHash(certificate.SourceSet) || certificate.TargetRevision == 0 || certificate.SourceGeneration < 2 || certificate.CutoffMillis <= 0 || len(certificate.Sources) < 1 || len(certificate.Sources) > 129 {
		return certificate, true, ErrBackupInvalid
	}
	for i, source := range certificate.Sources {
		if !validStorageID(source.ID) || source.Bytes <= 0 || !recordingCertificateHash(source.SHA256) {
			return certificate, true, ErrBackupInvalid
		}
		if i == len(certificate.Sources)-1 {
			if source.RelativePath != "observations.db" || source.Database.Identity != "vibermate-observations" || source.Database.Revision != 1 || source.ProvenanceSHA256 != "" {
				return certificate, true, ErrBackupInvalid
			}
		} else if source.RelativePath != "archives/"+source.ID+"/runtime.db" || source.Database.Identity != "vibermate-runtime" || (source.Database.Revision != 1 && source.Database.Revision != 2 && source.Database.Revision != 3) || !recordingCertificateHash(source.ProvenanceSHA256) {
			return certificate, true, ErrBackupInvalid
		}
	}
	return certificate, true, nil
}
