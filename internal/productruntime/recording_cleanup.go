package productruntime

import (
	"context"
	"errors"
	"time"

	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

type recordingCleanupClaim struct {
	target, generation uint64
	cutoff             int64
	resumed            bool
}

func (s *recordingStorage) waitApprovalAuthority() error {
	select {
	case <-s.approvalReady:
		if s.legacyAuthority == nil {
			return recording.ErrInspectionUnavailable
		}
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// Only the existing fixed optional goroutine calls this method. Required intent
// commits signal a coalesced wake; the periodic reload also recovers missed wakes.
func (s *recordingStorage) performPendingCleanup(sinks *recordingSinks) {
	state, err := s.control.RecordingCleanupState(s.ctx)
	if err != nil {
		s.cleanupPending.Store(true)
		s.tracker.recordingCleanupPending()
		return
	}
	if state.RequestedRevision == state.CompletedRevision && state.ActiveRevision == 0 {
		return
	}
	s.cleanupPending.Store(true)
	if err = s.waitApprovalAuthority(); err != nil {
		return
	}
	target, generation, cutoff, err := s.control.ClaimRecordingCleanup(s.ctx)
	if err != nil || target == 0 {
		return
	}
	claim := recordingCleanupClaim{target: target, generation: generation, cutoff: cutoff, resumed: state.ActiveRevision != 0}
	s.maintain(sinks, recordingMaintenanceRequest{ctx: s.ctx, generation: generation, cleanup: &claim})
}
func (s *recordingStorage) executeRecordingCleanup(store *runtimepersistence.ObservationStore, token *runtimepersistence.InspectionMaintenance, claim recordingCleanupClaim) error {
	if token == nil || claim.target == 0 || claim.generation != s.catalog.Version().Generation {
		return recording.ErrInspectionUnavailable
	}
	receipt, exists, err := runtimedata.ReadRecordingCleanupReceipt(s.root)
	if err != nil {
		return err
	}
	if exists {
		if receipt.LayoutID != s.layout.ID || receipt.SourceSet != s.catalog.Version().SourceSet || receipt.TargetRevision != claim.target || receipt.SourceGeneration != claim.generation || receipt.CutoffMillis != claim.cutoff {
			return runtimedata.ErrBackupInvalid
		}
	} else {
		receipt = runtimedata.RecordingCleanupReceipt{Schema: "vibermate-recording-cleanup/v1", LayoutID: s.layout.ID, SourceSet: s.catalog.Version().SourceSet, TargetRevision: claim.target, SourceGeneration: claim.generation, CutoffMillis: claim.cutoff, Phase: "select", CountsExact: true}
	}
	if claim.resumed || exists {
		receipt.CountsExact = false
		receipt.Released.CountsIncomplete = true
	}
	receipt.Released.PrivacyMetadataRetained = true
	// A durable claim blocks backup even before this first receipt publication.
	if err = runtimedata.WriteRecordingCleanupReceipt(s.ctx, s.root, s.dataGuard, receipt); err != nil {
		return err
	}
	if err = runtimedata.RetireRecordingCleanupArtifacts(s.ctx, s.root, s.dataGuard, receipt); err != nil {
		return err
	}
	receipt.Phase = "delete"
	if err = runtimedata.WriteRecordingCleanupReceipt(s.ctx, s.root, s.dataGuard, receipt); err != nil {
		return err
	}
	appendCounts := func(value resourcedeletion.Released, workErr error) error {
		receipt.Released = receipt.Released.Add(value)
		if workErr != nil {
			receipt.CountsExact = false
			receipt.Released.CountsIncomplete = true
		}
		return errors.Join(workErr, runtimedata.WriteRecordingCleanupReceipt(s.ctx, s.root, s.dataGuard, receipt))
	}
	cutoff := time.UnixMilli(claim.cutoff).UTC()
	value, err := store.PurgeRecordingArchive(s.ctx, s.control, token, cutoff, s.legacyAuthority)
	if err = appendCounts(value, err); err != nil {
		return err
	}
	// The combined preparation froze the actual active mirror before proof
	// destruction. Never broaden it between that selection and certification.
	stamp, err := store.PublishedRecordingStamp(s.ctx)
	if err != nil {
		return err
	}
	s.publishedMirror = stamp
	if err = store.VerifyRecordingCleanup(s.ctx, token, cutoff); err != nil {
		return err
	}
	receipt.Phase = "compact"
	for i := range s.sources {
		receipt.SourceIndex = i
		if err = runtimedata.WriteRecordingCleanupReceipt(s.ctx, s.root, s.dataGuard, receipt); err != nil {
			return err
		}
		if err = store.CompactRecordingSource(s.ctx, token, i); err != nil {
			return err
		}
	}
	receipt.Phase = "verify"
	receipt.SourceIndex = len(s.sources)
	if err = runtimedata.WriteRecordingCleanupReceipt(s.ctx, s.root, s.dataGuard, receipt); err != nil {
		return err
	}
	if err = runtimedata.RetireRecordingCleanupArtifacts(s.ctx, s.root, s.dataGuard, receipt); err != nil {
		return err
	}
	if err = runtimedata.CertifyRecordingMaintenance(s.ctx, s.root, s.dataGuard, s.layout, receipt); err != nil {
		return err
	}
	if err = store.CheckRecordingWriter(s.ctx); err != nil {
		return err
	}
	if err = token.Publish(s.sources); err != nil {
		return err
	}
	receipt.Phase = "complete"
	if err = runtimedata.WriteRecordingCleanupReceipt(s.ctx, s.root, s.dataGuard, receipt); err != nil {
		return err
	}
	if err = runtimedata.RemoveRecordingCleanupReceipt(s.ctx, s.root, s.dataGuard); err != nil {
		return err
	}
	if err = s.control.CompleteRecordingCleanup(s.ctx, claim.target, claim.generation); err != nil {
		return err
	}
	state, err := s.control.RecordingCleanupState(s.ctx)
	if err != nil {
		return err
	}
	pending := state.RequestedRevision != state.CompletedRevision || state.ActiveRevision != 0
	s.cleanupPending.Store(pending)
	s.tracker.mu.Lock()
	s.tracker.status.Recording.CleanupPending = pending
	s.tracker.mu.Unlock()
	return nil
}

// The fixed optional owner publishes routine expiry no more than once a minute.
// This short durable intent never opens history and coalesces with user work.
func (s *recordingStorage) requestRoutineRecordingCleanup(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	next := s.nextExpiry
	s.mu.Unlock()
	if now.Before(next) {
		return nil
	}
	if err := s.control.RequestRecordingCleanup(ctx, now); err != nil {
		return err
	}
	s.mu.Lock()
	s.nextExpiry = now.Add(time.Minute)
	s.mu.Unlock()
	s.markCleanupPending()
	return nil
}
