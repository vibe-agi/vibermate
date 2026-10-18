package runtimedata

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
	"math"
	"os"
	"path/filepath"
)

const RecordingCleanupReceiptName = "recording-cleanup.json"

type RecordingCleanupReceipt struct {
	Schema           string                    `json:"schema"`
	LayoutID         string                    `json:"layoutId"`
	SourceSet        string                    `json:"sourceSet"`
	TargetRevision   uint64                    `json:"targetRevision"`
	SourceGeneration uint64                    `json:"sourceGeneration"`
	CutoffMillis     int64                     `json:"cutoffMillis"`
	Phase            string                    `json:"phase"`
	SourceIndex      int                       `json:"sourceIndex"`
	CountsExact      bool                      `json:"countsExact"`
	Released         resourcedeletion.Released `json:"released"`
}

func validRecordingCleanupReceipt(r RecordingCleanupReceipt) bool {
	hash, err := hex.DecodeString(r.SourceSet)
	if err != nil || len(hash) != 32 || r.Schema != "vibermate-recording-cleanup/v1" || !validStorageID(r.LayoutID) || r.TargetRevision == 0 || r.TargetRevision > math.MaxInt64 || r.SourceGeneration < 2 || r.SourceGeneration > math.MaxInt64 || r.CutoffMillis <= 0 || r.SourceIndex < 0 || r.SourceIndex > 129 {
		return false
	}
	switch r.Phase {
	case "select", "delete", "compact", "verify", "complete":
		return true
	}
	return false
}
func checkRecordingCleanupOwnership(ctx context.Context, root string, guard *instanceguard.Guard) error {
	if ctx == nil || ctx.Err() != nil || guard == nil || guard.Path() != filepath.Join(root, lockName) || !guard.OwnsDirectory(root) {
		return ErrSchemaUpgradeIncomplete
	}
	return nil
}
func WriteRecordingCleanupReceipt(ctx context.Context, root string, guard *instanceguard.Guard, r RecordingCleanupReceipt) error {
	if err := checkRecordingCleanupOwnership(ctx, root, guard); err != nil {
		return err
	}
	if !validRecordingCleanupReceipt(r) {
		return ErrBackupInvalid
	}
	previous, exists, err := ReadRecordingCleanupReceipt(root)
	if err != nil {
		return err
	}
	if exists && (previous.LayoutID != r.LayoutID || previous.SourceSet != r.SourceSet || previous.SourceGeneration != r.SourceGeneration || previous.TargetRevision != r.TargetRevision || !previous.CountsExact && r.CountsExact) {
		return ErrBackupInvalid
	}
	encoded, err := json.Marshal(r)
	if err != nil || len(encoded) > 64<<10 {
		return ErrBackupInvalid
	}
	file, err := os.CreateTemp(root, ".recording-cleanup-")
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
	if err = os.Rename(file.Name(), filepath.Join(root, RecordingCleanupReceiptName)); err != nil {
		return err
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	if err = errors.Join(directory.Sync(), directory.Close()); err != nil {
		return err
	}
	return checkRecordingCleanupOwnership(ctx, root, guard)
}
func ReadRecordingCleanupReceipt(root string) (r RecordingCleanupReceipt, exists bool, result error) {
	encoded, exists, err := readStorageDocument(filepath.Join(root, RecordingCleanupReceiptName))
	if err != nil || !exists {
		return r, exists, err
	}
	if err = decodeStorageDocument(encoded, &r); err != nil || !validRecordingCleanupReceipt(r) {
		return RecordingCleanupReceipt{}, true, ErrBackupInvalid
	}
	return r, true, nil
}
func RemoveRecordingCleanupReceipt(ctx context.Context, root string, guard *instanceguard.Guard) error {
	if err := checkRecordingCleanupOwnership(ctx, root, guard); err != nil {
		return err
	}
	_, exists, err := ReadRecordingCleanupReceipt(root)
	if err != nil {
		return err
	}
	if exists {
		if err = os.Remove(filepath.Join(root, RecordingCleanupReceiptName)); err != nil {
			return err
		}
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	if err = errors.Join(directory.Sync(), directory.Close()); err != nil {
		return err
	}
	return checkRecordingCleanupOwnership(ctx, root, guard)
}
