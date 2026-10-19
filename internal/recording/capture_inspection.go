package recording

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"time"
)

// CaptureInspectionSelection is a short trusted read coordinate. It carries
// neither credential material nor an authorization decision.
type CaptureInspectionSelection struct {
	Stamp              InspectionStamp
	Scope              Scope
	CredentialRevision uint64
	Visible            bool
}
type CaptureInspectionControl interface {
	ReadCaptureInspectionSelection(context.Context, captureidentity.Reference) (CaptureInspectionSelection, error)
	RecheckCaptureInspectionSelection(context.Context, CaptureInspectionSelection) error
}

// CaptureObservationView keeps sparse retained facts distinct from request
// extrema. A missing counterpart remains nil, never inferred from creation.
type CaptureObservationView struct {
	Selection                       CaptureInspectionSelection
	Sources                         InspectionSourceVersion
	FirstObservedAt, LastObservedAt *time.Time
}
type CaptureObservationReader interface {
	ReadCaptureObservation(context.Context, captureidentity.Reference) (CaptureObservationView, error)
	CheckCaptureObservation(context.Context, CaptureObservationView) error
}
