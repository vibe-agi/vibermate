package desktopcontrol

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/conversationprojection"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/recording"
	"time"
)

func includeInspectionExpiry(current *time.Time, next time.Time) {
	if !next.IsZero() && (current.IsZero() || next.Before(*current)) {
		*current = next
	}
}
func (h *Handler) activityExpiryCheck(check func() error, expiry time.Time) func() error {
	if !h.activityInspection {
		return check
	}
	return func() error {
		if !expiry.IsZero() && !time.Now().Before(expiry) {
			return recording.ErrInspectionChanged
		}
		if err := check(); err != nil {
			return err
		}
		if !expiry.IsZero() && !time.Now().Before(expiry) {
			return recording.ErrInspectionChanged
		}
		return nil
	}
}
func (h *Handler) activityBodyAvailability(ctx context.Context, ids []string) (map[string]bool, time.Time, error) {
	if h.activityInspection {
		result, err := h.contents.(exchangecontent.InspectionReader).InspectAvailableBodies(ctx, ids)
		return result.Items, result.InspectionExpiresAt, err
	}
	result, err := h.contents.AvailableBodies(ctx, ids)
	return result, time.Time{}, err
}

func (h *Handler) inspectConversationIdentity(ctx context.Context, id string) (conversationprojection.IdentityInspection, error) {
	if h.activityInspection {
		if h.conversationIdentityInspection == nil {
			return conversationprojection.IdentityInspection{}, activity.ErrExchangeNotFound
		}
		return h.conversationIdentityInspection.InspectIdentity(ctx, id)
	}
	if h.conversationIndexer == nil {
		return conversationprojection.IdentityInspection{}, activity.ErrExchangeNotFound
	}
	value, err := h.conversationIndexer.Identity(ctx, id)
	return conversationprojection.IdentityInspection{Identity: value}, err
}
