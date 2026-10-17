package productruntime

import (
	"errors"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/recording"
	"unsafe"
)

func (r *observationRecorder) ObserveNativeContent(stamp recording.Stamp, in nativemcp.ContentInput) (result recording.OfferResult) {
	var bytes uint64
	defer func() { r.budget.RecordOffer(recording.Native, result, bytes) }()
	if !stamp.Current() {
		return recording.DroppedStale
	}
	r.mu.Lock()
	closed := r.closed || r.sealed
	sink := r.nativeContent
	r.mu.Unlock()
	if closed {
		return recording.DroppedClosed
	}
	if sink == nil {
		return recording.DroppedUnavailable
	}
	reservation, ok := r.budget.TryReserve(recording.Native, int64(unsafe.Sizeof(recordingItem{}))+4096)
	if !ok {
		return recording.DroppedCapacity
	}
	owned, err := nativemcp.OwnContentWithin(stamp, in, reservation)
	if err != nil {
		reservation.Release()
		if errors.Is(err, nativemcp.ErrContentOwnershipCapacity) {
			return recording.DroppedCapacity
		}
		return recording.DroppedInvalid
	}
	bytes = owned.RetainedBytes() + uint64(unsafe.Sizeof(recordingItem{}))
	return r.enqueue(&recordingItem{kind: recording.Native, stamp: stamp, nativeContent: owned, nativeContentSink: sink, reservation: reservation, bytes: int64(bytes)})
}
