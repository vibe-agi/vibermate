package nativemcp

import (
	"context"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

// Test capture of the closed ownership seam: no storage call occurs inside the
// producer. Tests explicitly flush to the real independent observation owner
// after joining execution. General-worker blocking is covered in productruntime.
type fixtureContent struct {
	mu     sync.Mutex
	epoch  *recording.Epoch
	budget *recording.Budget
	items  []fixtureOwnedContent
	store  *runtimepersistence.ObservationStore
}
type fixtureOwnedContent struct {
	value       *OwnedContent
	reservation *recording.Reservation
}

func (f *fixtureContent) ScopeStamp(c captureidentity.Reference) recording.Stamp {
	s, _ := recording.NewStamp(f.epoch, c, recording.Fence{ArchiveEpoch: f.epoch.Value(), CaptureGeneration: 1})
	return s
}
func (f *fixtureContent) ObserveNativeContent(stamp recording.Stamp, in ContentInput) recording.OfferResult {
	reservation, ok := f.budget.TryReserve(recording.Native, 4096)
	if !ok {
		return recording.DroppedCapacity
	}
	owned, err := OwnContentWithin(stamp, in, reservation)
	if err != nil {
		reservation.Release()
		return recording.DroppedInvalid
	}
	f.mu.Lock()
	f.items = append(f.items, fixtureOwnedContent{owned, reservation})
	f.mu.Unlock()
	return recording.Accepted
}
func (f *fixtureContent) flush() error {
	f.mu.Lock()
	items := f.items
	f.items = nil
	f.mu.Unlock()
	var first error
	for _, item := range items {
		err := f.store.PutNativeContent(context.Background(), item.value.Observation())
		item.reservation.Release()
		if first == nil {
			first = err
		}
	}
	return first
}
func (f *nativeFixture) readNativeContent(ctx context.Context, c captureidentity.Reference, id nativetool.ID, part string, offset, limit int) ([]byte, error) {
	if err := f.content.flush(); err != nil {
		return nil, err
	}
	return f.content.store.ReadNativeContent(ctx, c, id, part, offset, limit)
}
func (f *nativeFixture) requireNoContent(t *testing.T, id nativetool.ID, parts ...nativetool.ContentPart) {
	t.Helper()
	if err := f.content.flush(); err != nil {
		t.Fatal(err)
	}
	for _, part := range parts {
		if _, err := f.content.store.GetNativeContent(context.Background(), id, part); err != nativetool.ErrNotFound {
			t.Fatalf("optional %s retained or unreadable: %v", part, err)
		}
	}
}
