package toolapproval

import (
	"context"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
)

// CaptureCanceler is the existing required approval owner's narrow lifecycle
// port. It wakes existing waiters only after each durable cancellation batch.
type CaptureCanceler interface {
	CancelCaptureApprovals(context.Context, captureidentity.Reference, string) error
}
type ownedCancellationRepository interface {
	CancelOwnedCapture(context.Context, captureidentity.Reference, string, time.Time, int) ([]Record, error)
}

func (authority *Authority) CancelCaptureApprovals(ctx context.Context, capture captureidentity.Reference, reason string) error {
	if capture.Validate() != nil {
		return ErrInvalidApproval
	}
	operation, finish, err := authority.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	repository, ok := authority.repository.(ownedCancellationRepository)
	if !ok {
		return nil
	}
	for {
		records, err := repository.CancelOwnedCapture(operation, capture, reason, authority.clock.Now().UTC(), 100)
		if err != nil {
			return err
		}
		for _, record := range records {
			authority.waiters.resolve(record.ID, record.Clone())
			authority.forgetEphemeralSubjectLabels(record.ID)
		}
		if len(records) > 0 {
			authority.mu.Lock()
			authority.notifyLocked()
			authority.mu.Unlock()
		}
		if len(records) < 100 {
			return nil
		}
	}
}
