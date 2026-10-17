package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
)

// One required-only SELECT freezes the current manual revision with the
// visibility coordinates. No credential/grant bytes or optional tables join it.
func (s *ControlStore) ReadCaptureInspectionSelection(ctx context.Context, ref captureidentity.Reference) (value recording.CaptureInspectionSelection, err error) {
	if ref.Validate() != nil {
		return value, ErrInspectionUnavailable
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return value, errors.Join(ErrInspectionUnavailable, err)
	}
	defer finish()
	var epoch, visibility, policy, generation, deleted int64
	var revision sql.NullInt64
	err = s.store.reads.QueryRowContext(operation, `SELECT s.layout_id,s.archive_epoch,s.visibility_revision,p.revision,COALESCE(f.generation,0),COALESCE(f.deleted,1),
 CASE WHEN ?='managed_run' THEN 0 ELSE (SELECT credential_revision FROM manual_captures WHERE capture_id=?) END
 FROM runtime_storage_state s JOIN runtime_usage_policy p ON p.singleton=s.singleton
 LEFT JOIN recording_capture_fences f ON f.capture_kind=? AND f.capture_id=? WHERE s.singleton=1`, ref.Kind, ref.ID, ref.Kind, ref.ID).Scan(&value.Stamp.LayoutID, &epoch, &visibility, &policy, &generation, &deleted, &revision)
	if err != nil {
		return value, errors.Join(ErrInspectionUnavailable, err)
	}
	if epoch <= 0 || visibility <= 0 || policy <= 0 || generation < 0 || deleted < 0 || deleted > 1 || revision.Valid && (revision.Int64 < 0 || ref.Kind == captureidentity.KindManualCapture && revision.Int64 == 0) {
		return value, ErrInspectionUnavailable
	}
	value.Stamp.ArchiveEpoch = uint64(epoch)
	value.Stamp.VisibilityRevision = uint64(visibility)
	value.Stamp.UsagePolicyRevision = uint64(policy)
	if !value.Stamp.Valid() {
		return value, ErrInspectionUnavailable
	}
	value.Scope = recording.Scope{Capture: ref, Fence: recording.Fence{ArchiveEpoch: uint64(epoch), CaptureGeneration: uint64(generation)}}
	if revision.Valid {
		value.CredentialRevision = uint64(revision.Int64)
	}
	value.Visible = generation > 0 && deleted == 0 && revision.Valid
	return value, nil
}
func (s *ControlStore) RecheckCaptureInspectionSelection(ctx context.Context, want recording.CaptureInspectionSelection) error {
	if !want.Stamp.Valid() || want.Scope.Capture.Validate() != nil {
		return ErrInspectionChanged
	}
	got, err := s.ReadCaptureInspectionSelection(ctx, want.Scope.Capture)
	if err != nil {
		return err
	}
	if got != want {
		return ErrInspectionChanged
	}
	return nil
}

var _ recording.CaptureInspectionControl = (*ControlStore)(nil)
