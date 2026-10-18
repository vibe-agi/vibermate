package recording

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"math"
	"unicode/utf8"
)

var ErrInspectionUnavailable = errors.New("inspection is unavailable")
var ErrInspectionCursor = errors.New("invalid inspection cursor")
var ErrInspectionChanged = errors.New("inspection visibility or source generation changed")

// These are bounded derivative read coordinates, never execution authority.
type InspectionStamp struct {
	LayoutID                                              string
	ArchiveEpoch, VisibilityRevision, UsagePolicyRevision uint64
	DenialCount                                           uint64
	DenialHash                                            [32]byte
}
type InspectionSourceVersion struct {
	SourceSet  string
	Generation uint64
}
type InspectionToken struct {
	Control InspectionStamp
	Sources InspectionSourceVersion
}
type InspectionCaptureFence struct {
	Generation uint64
	Deleted    bool
}
type VisibilitySnapshot struct {
	Stamp  InspectionStamp
	Scopes map[captureidentity.Reference]InspectionCaptureFence
}

func (s InspectionStamp) Valid() bool {
	return s.DenialCount <= math.MaxInt64 && ((s.DenialCount == 0) == (s.DenialHash == ([32]byte{}))) && len(s.LayoutID) > 0 && len(s.LayoutID) <= 128 && utf8.ValidString(s.LayoutID) && s.ArchiveEpoch > 0 && s.ArchiveEpoch <= math.MaxInt64 && s.VisibilityRevision > 0 && s.VisibilityRevision <= math.MaxInt64 && s.UsagePolicyRevision > 0 && s.UsagePolicyRevision <= math.MaxInt64
}
func (s VisibilitySnapshot) Allows(scope Scope) bool {
	if !s.Stamp.Valid() || scope.Validate() != nil || scope.Fence.ArchiveEpoch != s.Stamp.ArchiveEpoch {
		return false
	}
	if scope.Capture == (captureidentity.Reference{}) {
		return scope.Fence.CaptureGeneration == 0
	}
	fence, exists := s.Scopes[scope.Capture]
	return exists && !fence.Deleted && fence.Generation > 0 && scope.Fence.CaptureGeneration == fence.Generation
}

type InspectionVisibility interface {
	ReadInspectionSnapshot(context.Context, []captureidentity.Reference) (VisibilitySnapshot, error)
	RecheckInspectionStamp(context.Context, InspectionStamp) error
}

// Begin/Check bracket outer response enrichment, after all constituent source
// reads and any reveal audit. They never retain a control transaction or lease.
type InspectionBoundary interface {
	Begin(context.Context) (InspectionToken, error)
	Check(context.Context, InspectionToken) error
}
