package productruntime

import (
	"github.com/vibe-agi/vibermate/internal/recording"
)

func (r *observationRecorder) ObserveCapture(stamp recording.Stamp, v recording.CaptureObservation) (result recording.OfferResult) {
	const charge = 8192 // item, inline stamp, detached scope and bounded scalar SQL workspace
	defer func() { r.budget.RecordOffer(recording.Capture, result, 0) }()
	if !stamp.Current() {
		return recording.DroppedStale
	}
	r.mu.Lock()
	closed := r.closed || r.sealed
	r.mu.Unlock()
	if closed {
		return recording.DroppedClosed
	}
	if r.capture == nil {
		return recording.DroppedUnavailable
	}
	if !stamp.HasCapture() || v.ObservedAt.IsZero() {
		return recording.DroppedInvalid
	}
	reservation, ok := r.budget.TryReserve(recording.Capture, charge)
	if !ok {
		return recording.DroppedCapacity
	}
	v.ObservedAt = v.ObservedAt.UTC()
	return r.enqueue(&recordingItem{kind: recording.Capture, stamp: stamp, capture: v, reservation: reservation})
}
