package desktopcontrol

import (
	"context"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

type nativeManagementKey struct{}
type nativeManagementAuthority struct {
	scope Scope
	owner manualcapture.OwnerScope
}

// WithNativeManagementAuthority is called only by the authenticated host boundary.
// Credentials and caller-supplied DTO fields cannot manufacture this context key.
func WithNativeManagementAuthority(ctx context.Context, scope Scope, owner manualcapture.OwnerScope) context.Context {
	authority := nativeManagementAuthority{}
	if (scope == ScopeRead || scope == ScopeWrite) && owner.Valid() && owner.Kind() == manualcapture.OwnerLocalInstallation {
		authority = nativeManagementAuthority{scope, owner}
	}
	return context.WithValue(ctx, nativeManagementKey{}, authority)
}

func (h *Handler) authorizeNativeCapture(ctx context.Context, key string, write bool) (captureidentity.Reference, error) {
	a, ok := ctx.Value(nativeManagementKey{}).(nativeManagementAuthority)
	if !ok || !a.owner.Valid() || a.scope != ScopeRead && a.scope != ScopeWrite || write && a.scope != ScopeWrite {
		return captureidentity.Reference{}, nativetool.ErrDenied
	}
	ref, err := captureidentity.ParseKey(key)
	if err != nil {
		return ref, nativetool.ErrInvalid
	}
	switch ref.Kind {
	case captureidentity.KindManagedRun:
		if h.captureRuns == nil {
			return ref, nativetool.ErrUnavailable
		}
		view, err := h.captureRuns.GetRun(ctx, ref.ID)
		if err != nil || view.ID != ref.ID {
			return ref, nativetool.ErrNotFound
		}
	case captureidentity.KindManualCapture:
		id, err := manualcapture.ParseID(ref.ID)
		if err != nil {
			return ref, nativetool.ErrInvalid
		}
		if h.manualCaptures == nil {
			return ref, nativetool.ErrUnavailable
		}
		view, err := h.manualCaptures.Get(ctx, a.owner, id)
		if err != nil || view.ID != ref.ID {
			return ref, nativetool.ErrNotFound
		}
	default:
		return ref, nativetool.ErrInvalid
	}
	return ref, nil
}
