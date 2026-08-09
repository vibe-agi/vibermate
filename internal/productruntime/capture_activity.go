package productruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

type captureActivityResolver struct {
	runs    capturerun.ActivityReader
	manuals manualcapture.ActivityReader
	clock   Clock
}

func newCaptureActivityResolver(
	runs capturerun.ActivityReader,
	manuals manualcapture.ActivityReader,
	clock Clock,
) (*captureActivityResolver, error) {
	if runs == nil || manuals == nil || clock == nil {
		return nil, errors.New("Capture activity dependencies are incomplete")
	}
	return &captureActivityResolver{runs: runs, manuals: manuals, clock: clock}, nil
}

func (resolver *captureActivityResolver) Active(
	ctx context.Context,
	reference captureidentity.Reference,
) (bool, error) {
	if resolver == nil || reference.Validate() != nil {
		return false, captureidentity.ErrInvalidReference
	}
	now := resolver.clock.Now().UTC()
	switch reference.Kind {
	case captureidentity.KindManagedRun:
		return resolver.runs.Active(ctx, reference.ID, now)
	case captureidentity.KindManualCapture:
		id, err := manualcapture.ParseID(reference.ID)
		if err != nil {
			return false, err
		}
		return resolver.manuals.Active(ctx, id, now)
	default:
		return false, fmt.Errorf("%w: unsupported Capture kind", captureidentity.ErrInvalidReference)
	}
}
