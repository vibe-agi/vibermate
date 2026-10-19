package productruntime

import (
	"context"

	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

func (s *recordingStorage) markCleanupPending() {
	s.cleanupPending.Store(true)
	s.tracker.recordingCleanupPending()
	// Required commit publication is nonblocking and coalesced. Durable intent
	// is authoritative; owner reload/restart never depends on receiving this wake.
	select {
	case s.cleanupWake <- struct{}{}:
	default:
	}
}
func (s *recordingStorage) DeleteCapture(ctx context.Context, kind, id string) (resourcedeletion.Released, error) {
	released, err := s.control.DeleteCapture(ctx, kind, id)
	if err != nil {
		return released, err
	}
	released.CleanupPending = true
	released.CountsIncomplete = true
	released.PrivacyMetadataRetained = true
	unknown, err := s.control.UnknownCaptureApprovalOwnership(ctx)
	released.ApprovalOwnershipUnknown = err != nil || unknown
	s.markCleanupPending()
	return released, nil
}
func (s *recordingStorage) ClearEvidence(ctx context.Context) (resourcedeletion.Released, error) {
	if s == nil || s.control == nil || s.clearAuthority == nil || ctx == nil {
		return resourcedeletion.Released{}, recording.ErrInspectionUnavailable
	}
	lease, err := s.clearAuthority.BeginArchiveClear(ctx)
	if err != nil {
		return resourcedeletion.Released{}, err
	}
	defer lease.Complete(false)
	released, epoch, err := s.control.ClearRecordingArchive(ctx)
	if err != nil {
		return resourcedeletion.Released{}, err
	}
	s.recorder.epoch.Advance(epoch)
	released.CleanupPending = true
	released.CountsIncomplete = true
	released.PrivacyMetadataRetained = true
	s.markCleanupPending()
	lease.Complete(true)
	return released, nil
}
