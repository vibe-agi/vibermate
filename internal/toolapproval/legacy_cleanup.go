package toolapproval

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/recording"
)

// LegacyApprovalRemover is used only after exclusive optional maintenance has
// supplied validated all-source ownership proof and released every history SQL
// lease. The implementation must recheck the required row and revoked owner.
type LegacyApprovalRemover interface {
	RemoveLegacyApproval(context.Context, Record, recording.Scope) (bool, error)
}

func (authority *Authority) RemoveLegacyApproval(ctx context.Context, expected Record, scope recording.Scope) (bool, error) {
	repository, ok := authority.repository.(LegacyApprovalRemover)
	if !ok {
		return false, ErrInvalidApproval
	}
	// Reuse the short required publication gate without clearing other entries.
	// No optional source operation is allowed inside this boundary.
	lease, err := authority.BeginArchiveClear(ctx)
	if err != nil {
		return false, err
	}
	defer lease.Complete(false)
	removed, err := repository.RemoveLegacyApproval(ctx, expected, scope)
	if err != nil {
		return false, err
	}
	if removed {
		authority.waiters.resolve(expected.ID, Record{ID: expected.ID, State: StateCanceled, DecisionReason: "capture_deleted"})
		authority.forgetEphemeralSubjectLabels(expected.ID)
		authority.mu.Lock()
		authority.notifyLocked()
		authority.mu.Unlock()
	}
	return removed, nil
}
