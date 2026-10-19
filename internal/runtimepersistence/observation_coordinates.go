package runtimepersistence

import (
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
	"math"
)

func validOriginalRecordingScope(scope recording.Scope) bool {
	return scope.Validate() == nil && scope.Fence.ArchiveEpoch <= math.MaxInt64 && scope.Fence.CaptureGeneration <= math.MaxInt64
}
func recordingScopeMatches(scope recording.Scope, managed, manual string) bool {
	if !validOriginalRecordingScope(scope) || managed != "" && manual != "" {
		return false
	}
	var expected captureidentity.Reference
	if managed != "" {
		expected = captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: managed}
	} else if manual != "" {
		expected = captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: manual}
	}
	return scope.Capture == expected
}
