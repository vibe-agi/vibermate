package conversationprojection

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/recording"
	"time"
)

func includeExpiry(current *time.Time, next time.Time) {
	if !next.IsZero() && (current.IsZero() || next.Before(*current)) {
		*current = next
	}
}

// An outer inspection keeps no SQL transaction or source lease while invoking
// client-local resolvers. Every constituent port must refer to the same view.
func (indexer *Indexer) beginInspection(ctx context.Context) (func(time.Time) error, error) {
	if !indexer.inspection {
		return func(time.Time) error { return nil }, nil
	}
	ports := []recording.InspectionBoundary{indexer.activities.(recording.InspectionBoundary), indexer.contents.(recording.InspectionBoundary), indexer.identities.(recording.InspectionBoundary)}
	var token recording.InspectionToken
	for n, port := range ports {
		current, err := port.Begin(ctx)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			token = current
		} else if current != token {
			return nil, recording.ErrInspectionChanged
		}
	}
	return func(expiry time.Time) error {
		if !expiry.IsZero() && !time.Now().Before(expiry) {
			return recording.ErrInspectionChanged
		}
		for _, port := range ports {
			if err := port.Check(ctx, token); err != nil {
				return err
			}
		}
		if !expiry.IsZero() && !time.Now().Before(expiry) {
			return recording.ErrInspectionChanged
		}
		return nil
	}, nil
}
