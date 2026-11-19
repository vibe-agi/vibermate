package desktopcontrol

import (
	"context"
	"errors"
	"fmt"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
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

func nativeManagement(ctx context.Context, write bool) (nativeManagementAuthority, error) {
	a, ok := ctx.Value(nativeManagementKey{}).(nativeManagementAuthority)
	if !ok || !a.owner.Valid() || a.scope != ScopeRead && a.scope != ScopeWrite || write && a.scope != ScopeWrite {
		return nativeManagementAuthority{}, nativetool.ErrDenied
	}
	return a, nil
}

func (h *Handler) authorizeNativeCapture(ctx context.Context, key string, write bool) (captureidentity.Reference, error) {
	a, err := nativeManagement(ctx, write)
	if err != nil {
		return captureidentity.Reference{}, err
	}
	ref, err := captureidentity.ParseKey(key)
	if err != nil {
		return ref, nativetool.ErrInvalid
	}
	if _, err = h.nativeCaptureName(ctx, a, ref); errors.Is(err, errNativeCaptureHidden) || errors.Is(err, errNativeCaptureLookup) {
		return ref, nativetool.ErrNotFound
	}
	return ref, err
}

var (
	// The Capture does not exist, or belongs to another owner.
	errNativeCaptureHidden = errors.New("native Capture is not visible")
	// Storage could not say whether the Capture is visible.
	errNativeCaptureLookup = errors.New("native Capture lookup failed")
)

// nativeCaptureName is the visibility rule every native read shares: the
// Capture exists and, when manual, belongs to the authority's owner. It
// returns the name that Capture shows under in the Capture list.
func (h *Handler) nativeCaptureName(ctx context.Context, a nativeManagementAuthority, ref captureidentity.Reference) (string, error) {
	switch ref.Kind {
	case captureidentity.KindManagedRun:
		if h.captureRuns == nil {
			return "", nativetool.ErrUnavailable
		}
		view, err := h.captureRuns.GetRun(ctx, ref.ID)
		if errors.Is(err, capturerun.ErrNotFound) || err == nil && view.ID != ref.ID {
			return "", errNativeCaptureHidden
		}
		if err != nil {
			return "", fmt.Errorf("%w: %w", errNativeCaptureLookup, err)
		}
		return view.ExecutableLabel, nil
	case captureidentity.KindManualCapture:
		id, err := manualcapture.ParseID(ref.ID)
		if err != nil {
			return "", nativetool.ErrInvalid
		}
		if h.manualCaptures == nil {
			return "", nativetool.ErrUnavailable
		}
		view, err := h.manualCaptures.Get(ctx, a.owner, id)
		if errors.Is(err, manualcapture.ErrNotFound) || err == nil && view.ID != ref.ID {
			return "", errNativeCaptureHidden
		}
		if err != nil {
			return "", fmt.Errorf("%w: %w", errNativeCaptureLookup, err)
		}
		return view.DisplayName, nil
	default:
		return "", nativetool.ErrInvalid
	}
}
