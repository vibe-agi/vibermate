package productruntime

import (
	"context"
	"errors"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// PrepareNativeRecovery makes native execution require a new explicit owner
// decision. The caller keeps the fresh grant private until this returns.
func (r *Runtime) PrepareNativeRecovery(ctx context.Context, attached capturerun.View) error {
	if ctx == nil || r == nil || r.captureRuns == nil || r.storage == nil || r.clock == nil {
		return nativetool.ErrUnavailable
	}
	current, err := r.captureRuns.GetRun(ctx, attached.ID)
	if err != nil {
		return err
	}
	now := r.clock.Now().UTC()
	if attached.State != capturerun.StateAttached || attached.ProcessID <= 0 || current.State != capturerun.StateAttached || current.ProcessID != attached.ProcessID || !current.ExpiresAt.After(now) {
		return capturerun.ErrStateConflict
	}
	ref, err := captureidentity.New(captureidentity.KindManagedRun, current.ID)
	if err != nil {
		return err
	}
	repo := r.NativeToolRepository()
	scope, err := repo.GetScope(ctx, ref)
	if errors.Is(err, nativetool.ErrNotFound) {
		// This placeholder cannot execute without a real native session. The
		// executor's later owner update preserves the installed replay barrier.
		revision, err := repo.EnsureScope(ctx, nativetool.ScopeInput{Capture: ref, OwnerEpoch: 1, ExpiresAt: current.ExpiresAt})
		if err != nil {
			return err
		}
		return repo.MarkReplayBarrier(ctx, ref, revision, now)
	}
	if err != nil {
		return err
	}
	if scope.State != "active" || !scope.ExpiresAt.After(now) {
		return nativetool.ErrDenied
	}
	if scope.ReplayBarrier {
		return nil
	}
	return repo.MarkReplayBarrier(ctx, ref, scope.Revision, now)
}
