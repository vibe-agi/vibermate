package desktophost

import (
	"context"
	"fmt"

	"github.com/vibe-agi/vibermate/internal/desktoptrust"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

func beginRootReplacement(
	ctx context.Context,
	runtime *productruntime.Runtime,
) (func(), error) {
	if ctx == nil || runtime == nil {
		return nil, fmt.Errorf("%w: runtime is unavailable", desktoptrust.ErrRootResetActiveCaptures)
	}
	release, err := runtime.BeginCaptureMaintenance(ctx)
	if err != nil {
		return nil, fmt.Errorf("enter Capture maintenance before Root replacement: %w", err)
	}
	accepted := false
	defer func() {
		if !accepted {
			release()
		}
	}()
	activeCaptureRuns, err := runtime.CaptureRunActivity().ActiveCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect CaptureRuns before Root replacement: %w", err)
	}
	if activeCaptureRuns != 0 {
		return nil, desktoptrust.ErrRootResetActiveCaptures
	}
	activeManualCaptures, err := runtime.ManualCaptureActivity().ActiveCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect ManualCaptures before Root replacement: %w", err)
	}
	if activeManualCaptures != 0 {
		return nil, desktoptrust.ErrRootResetActiveCaptures
	}
	accepted = true
	return release, nil
}
