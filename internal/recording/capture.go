package recording

import (
	"context"
	"time"
)

// CaptureObservation is evidence of an admitted proxy authentication. It is
// never authority. Revision zero denotes a managed run; Manual observations
// retain the exact admitted credential revision across later rotations.
type CaptureObservation struct {
	ObservedAt         time.Time
	CredentialRevision uint64
}
type CaptureObserver interface {
	ObserveCapture(Stamp, CaptureObservation) OfferResult
}
type CaptureWriter interface {
	PutCaptureObservation(context.Context, Scope, CaptureObservation) error
}
type CaptureTimes struct{ First, Last time.Time }
