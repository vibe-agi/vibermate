package productruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
)

const storageLowSpaceThresholdBytes = uint64(1 << 30)

// StorageLocation describes this Runtime's resolved persistent directory and a
// bounded SQLite page snapshot. Paths always belong to the connected Runtime,
// never to a browser's filesystem or a guessed platform default.
type StorageLocation struct {
	LastCleanup              *StorageRecordCounts `json:"lastCleanup,omitempty"`
	Backend                  string               `json:"backend"`
	DataDirectory            string               `json:"dataDirectory"`
	DatabasePath             string               `json:"databasePath"`
	CollectedAt              time.Time            `json:"collectedAt"`
	DatabaseBytes            int64                `json:"databaseBytes"`
	WALBytes                 int64                `json:"walBytes"`
	SharedMemoryBytes        int64                `json:"sharedMemoryBytes"`
	EvidenceBytes            int64                `json:"evidenceBytes"`
	ReusableBytes            int64                `json:"reusableBytes"`
	FilesystemAvailableBytes *uint64              `json:"filesystemAvailableBytes"`
	LowSpaceThresholdBytes   uint64               `json:"lowSpaceThresholdBytes"`
	CapacityState            string               `json:"capacityState"`
	CleanupPreview           StorageRecordCounts  `json:"cleanupPreview"`
}

type StorageRecordCounts struct {
	CleanupPending           bool   `json:"cleanupPending"`
	CountsIncomplete         bool   `json:"countsIncomplete"`
	ApprovalOwnershipUnknown bool   `json:"approvalOwnershipUnknown"`
	PrivacyMetadataRetained  bool   `json:"privacyMetadataRetained"`
	Exchanges                uint64 `json:"exchanges"`
	Envelopes                uint64 `json:"envelopes"`
	Activities               uint64 `json:"activities"`
	Connections              uint64 `json:"connections"`
	Attempts                 uint64 `json:"attempts"`
	Approvals                uint64 `json:"approvals"`
	Assignments              uint64 `json:"assignments"`
	Captures                 uint64 `json:"captures"`
}

func (r *Runtime) StorageLocation(ctx context.Context) (StorageLocation, error) {
	if r == nil || r.storage == nil || ctx == nil {
		return StorageLocation{}, errors.New("Runtime storage is unavailable")
	}
	if r.recordingStorage == nil {
		return StorageLocation{}, recording.ErrInspectionUnavailable
	}
	s := r.recordingStorage
	version := s.catalog.Version()
	now := r.clock.Now().UTC()
	statistics, _, err := s.catalog.InspectRecordingStorage(ctx, r.storage, now, false)
	if err != nil {
		return StorageLocation{}, err
	}
	state, err := r.storage.RecordingCleanupState(ctx)
	if err != nil {
		return StorageLocation{}, err
	}
	statistics.Expired.CleanupPending = state.RequestedRevision != state.CompletedRevision || state.ActiveRevision != 0
	result := StorageLocation{Backend: "sqlite", DataDirectory: r.paths.DataDirectory(), DatabasePath: filepath.Join(r.paths.DataDirectory(), "runtime.db"), CollectedAt: now, EvidenceBytes: statistics.EvidenceBytes, ReusableBytes: statistics.ReusableBytes, LowSpaceThresholdBytes: storageLowSpaceThresholdBytes, CleanupPreview: storageRecordCounts(statistics.Expired)}
	certificate, exists, err := runtimedata.ReadRecordingMaintenanceCertificate(s.root)
	if err != nil {
		return StorageLocation{}, err
	}
	if exists {
		if certificate.LayoutID != s.layout.ID || certificate.SourceSet != version.SourceSet || certificate.SourceGeneration > state.SourceGeneration {
			return StorageLocation{}, recording.ErrInspectionUnavailable
		}
		if certificate.TargetRevision == state.CompletedRevision {
			last := certificate.Released
			last.CleanupPending = false
			last.CountsIncomplete = last.CountsIncomplete || !certificate.CountsExact
			counts := storageRecordCounts(last)
			result.LastCleanup = &counts
		}
	}
	paths := []string{result.DatabasePath}
	for _, source := range s.sources {
		paths = append(paths, filepath.Join(s.root, source.RelativePath))
	}
	for _, path := range paths {
		size, err := storageFileBytes(path, true)
		if err != nil {
			return StorageLocation{}, err
		}
		result.DatabaseBytes += size
		size, err = storageFileBytes(path+"-wal", false)
		if err != nil {
			return StorageLocation{}, err
		}
		result.WALBytes += size
		size, err = storageFileBytes(path+"-shm", false)
		if err != nil {
			return StorageLocation{}, err
		}
		result.SharedMemoryBytes += size
	}
	if available, err := filesystemAvailableBytes(result.DataDirectory); err == nil {
		result.FilesystemAvailableBytes = &available
	}
	result.CapacityState = storageCapacityState(result.FilesystemAvailableBytes)
	if err = s.catalog.Check(version); err != nil {
		return StorageLocation{}, err
	}
	return result, nil
}

func (r *Runtime) EvidenceArchivePreview(
	ctx context.Context,
) (resourcedeletion.Released, error) {
	if r == nil || r.storage == nil || ctx == nil {
		return resourcedeletion.Released{}, errors.New("Runtime storage is unavailable")
	}
	if r.recordingStorage == nil {
		return resourcedeletion.Released{}, recording.ErrInspectionUnavailable
	}
	required, err := r.storage.RecordingArchivePreview(ctx)
	if err != nil {
		return required, err
	}
	_, optional, err := r.recordingStorage.catalog.InspectRecordingStorage(ctx, r.storage, r.clock.Now().UTC(), true)
	if err != nil {
		return resourcedeletion.Released{}, err
	}
	result := required.Add(optional)
	state, err := r.storage.RecordingCleanupState(ctx)
	if err != nil {
		return resourcedeletion.Released{}, err
	}
	result.CleanupPending = state.RequestedRevision != state.CompletedRevision || state.ActiveRevision != 0
	result.CountsIncomplete = result.CleanupPending
	return result, nil
}

func storageCapacityState(available *uint64) string {
	if available == nil {
		return "unavailable"
	}
	if *available < storageLowSpaceThresholdBytes {
		return "low"
	}
	return "healthy"
}

func (r *Runtime) CleanupExpiredStorage(
	ctx context.Context,
) (resourcedeletion.Released, error) {
	if r == nil || r.storage == nil || ctx == nil {
		return resourcedeletion.Released{}, errors.New("Runtime storage is unavailable")
	}
	if err := r.storage.RequestRecordingCleanup(ctx, r.clock.Now().UTC()); err != nil {
		return resourcedeletion.Released{}, err
	}
	r.recordingStorage.markCleanupPending()
	return resourcedeletion.Released{CleanupPending: true, CountsIncomplete: true, PrivacyMetadataRetained: true}, nil
}

func storageRecordCounts(value resourcedeletion.Released) StorageRecordCounts {
	return StorageRecordCounts{CleanupPending: value.CleanupPending, CountsIncomplete: value.CountsIncomplete, ApprovalOwnershipUnknown: value.ApprovalOwnershipUnknown, PrivacyMetadataRetained: value.PrivacyMetadataRetained,
		Exchanges: value.Exchanges, Envelopes: value.Envelopes,
		Activities: value.Activities, Connections: value.Connections,
		Attempts: value.Attempts, Approvals: value.Approvals,
		Assignments: value.Assignments, Captures: value.Captures,
	}
}

func storageFileBytes(path string, required bool) (int64, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.Size(), nil
	}
	if !required && os.IsNotExist(err) {
		return 0, nil
	}
	return 0, fmt.Errorf("inspect storage file: %w", err)
}
