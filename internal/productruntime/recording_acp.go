package productruntime

import (
	"reflect"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/recording"
)

func (r *observationRecorder) ObserveACP(stamp recording.Stamp, value acpobservation.Record) (result recording.OfferResult) {
	var bytes uint64
	defer func() { r.budget.RecordOffer(recording.ACP, result, bytes) }()
	if r.available != nil && !r.available() {
		return recording.DroppedUnavailable
	}
	if !stamp.Current() {
		return recording.DroppedStale
	}
	r.mu.Lock()
	closed := r.closed || r.sealed
	r.mu.Unlock()
	if closed {
		return recording.DroppedClosed
	}
	if r.acp == nil {
		return recording.DroppedUnavailable
	}
	if value.Scope.Capture.Kind != captureidentity.KindManagedRun || !stamp.MatchesCapture(value.Scope.Capture) || stamp.Fence() != value.Scope.Fence || value.Scope.Capture.ID != value.RunID || value.Snapshot.ValidatePolicy(value.Policy) != nil {
		return recording.DroppedInvalid
	}
	// snapshotBytes walks this closed value without allocating. Let n include all
	// struct/slice descriptors and original string bytes. Ownership costs <=n;
	// escaped JSON <=6n, encoder growth <=12n, returned buffer <=6n and a driver
	// copy <=6n. 32n plus 128 KiB covers these simultaneous lifetimes, item/scope,
	// fixed SQL workspace. Two MaxSnapshotBytes buffers cover an existing
	// larger revision and its driver copy during duplicate comparison. No worker can acquire more budget.
	const fixed = 2*acpobservation.MaxSnapshotBytes + 128*1024
	n := snapshotBytes(reflect.ValueOf(value))
	if n > (64<<20-fixed)/32 {
		return recording.DroppedTooLarge
	}
	reservation, ok := r.budget.TryReserve(recording.ACP, int64(32*n+fixed))
	if !ok {
		return recording.DroppedCapacity
	}
	exchangecontent.CloneOwned(reflect.ValueOf(&value).Elem())
	bytes = n
	return r.enqueue(&recordingItem{kind: recording.ACP, stamp: stamp, acp: &value, reservation: reservation, bytes: int64(bytes)})
}
