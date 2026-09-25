package productruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

const storageLowSpaceThresholdBytes = uint64(1 << 30)

// StorageLocation describes this Runtime's resolved persistent directory and a
// bounded SQLite page snapshot. Paths always belong to the connected Runtime,
// never to a browser's filesystem or a guessed platform default.
type StorageLocation struct {
	Backend                  string              `json:"backend"`
	DataDirectory            string              `json:"dataDirectory"`
	DatabasePath             string              `json:"databasePath"`
	CollectedAt              time.Time           `json:"collectedAt"`
	DatabaseBytes            int64               `json:"databaseBytes"`
	WALBytes                 int64               `json:"walBytes"`
	SharedMemoryBytes        int64               `json:"sharedMemoryBytes"`
	EvidenceBytes            int64               `json:"evidenceBytes"`
	ReusableBytes            int64               `json:"reusableBytes"`
	FilesystemAvailableBytes *uint64             `json:"filesystemAvailableBytes"`
	LowSpaceThresholdBytes   uint64              `json:"lowSpaceThresholdBytes"`
	CapacityState            string              `json:"capacityState"`
	CleanupPreview           StorageRecordCounts `json:"cleanupPreview"`
}

type StorageRecordCounts struct {
	Exchanges   uint64 `json:"exchanges"`
	Envelopes   uint64 `json:"envelopes"`
	Activities  uint64 `json:"activities"`
	Connections uint64 `json:"connections"`
	Attempts    uint64 `json:"attempts"`
	Approvals   uint64 `json:"approvals"`
	Assignments uint64 `json:"assignments"`
	Captures    uint64 `json:"captures"`
}

func (r *Runtime) StorageLocation(ctx context.Context) (StorageLocation, error) {
	if r == nil || r.storage == nil || ctx == nil {
		return StorageLocation{}, errors.New("Runtime storage is unavailable")
	}
	now := r.clock.Now().UTC()
	statistics, err := r.storage.StorageStatistics(ctx, now)
	if err != nil {
		return StorageLocation{}, err
	}
	databasePath := r.paths.DatabasePath()
	databaseBytes, err := storageFileBytes(databasePath, true)
	if err != nil {
		return StorageLocation{}, err
	}
	walBytes, err := storageFileBytes(databasePath+"-wal", false)
	if err != nil {
		return StorageLocation{}, err
	}
	sharedMemoryBytes, err := storageFileBytes(databasePath+"-shm", false)
	if err != nil {
		return StorageLocation{}, err
	}
	available, capacityErr := filesystemAvailableBytes(r.paths.DataDirectory())
	var availablePointer *uint64
	if capacityErr == nil {
		availablePointer = &available
	}
	return StorageLocation{
		Backend: "sqlite", DataDirectory: r.paths.DataDirectory(),
		DatabasePath: databasePath, CollectedAt: now,
		DatabaseBytes: databaseBytes, WALBytes: walBytes,
		SharedMemoryBytes:        sharedMemoryBytes,
		EvidenceBytes:            statistics.EvidenceBytes,
		ReusableBytes:            statistics.ReusableBytes,
		FilesystemAvailableBytes: availablePointer,
		LowSpaceThresholdBytes:   storageLowSpaceThresholdBytes,
		CapacityState:            storageCapacityState(availablePointer),
		CleanupPreview:           storageRecordCounts(statistics.Expired),
	}, nil
}

func (r *Runtime) EvidenceArchivePreview(
	ctx context.Context,
) (resourcedeletion.Released, error) {
	if r == nil || r.storage == nil || ctx == nil {
		return resourcedeletion.Released{}, errors.New("Runtime storage is unavailable")
	}
	return r.storage.EvidenceArchivePreview(ctx)
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
	return r.storage.CleanupExpired(ctx, r.clock.Now().UTC())
}

func storageRecordCounts(value resourcedeletion.Released) StorageRecordCounts {
	return StorageRecordCounts{
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
