package recording

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestOwnershipAcrossLanes(t *testing.T) {
	b, _ := NewBudget(Limits{3, 96})
	producer, ok := b.TryReserve(Raw, 32)
	if !ok {
		t.Fatal("producer refused")
	}
	queued, _ := b.TryReserve(Content, 32)
	worker, _ := b.TryReserve(Egress, 32)
	done := make(chan bool, 1)
	go func() { extra, ok := b.TryReserve(Activity, 1); done <- !ok && extra == nil && !producer.TryGrow(1) }()
	select {
	case good := <-done:
		if !good {
			t.Fatal("capacity exceeded")
		}
	case <-time.After(time.Second):
		t.Fatal("admission waited")
	}
	if s := b.Snapshot(); s.ReservedRecords != 3 || s.ReservedBytes != 96 {
		t.Fatalf("ownership lost: %+v", s)
	}
	// Dequeue transfers the same pointer: it does not release ownership.
	dequeued := *queued
	producer.Release()
	producer.Release()
	if !dequeued.TryGrow(32) {
		t.Fatal("released bytes not reusable")
	}
	dequeued.Shrink(32)
	worker.Release()
	dequeued.Release()
	queued.Release()
	if queued.TryGrow(1) {
		t.Fatal("value copy released only one alias")
	}
	if s := b.Snapshot(); s.ReservedRecords != 0 || s.ReservedBytes != 0 {
		t.Fatalf("leak: %+v", s)
	}
	if dequeued.TryGrow(1) {
		t.Fatal("released reservation grew")
	}
}
func TestInvalidArithmeticAndCounters(t *testing.T) {
	for _, l := range []Limits{{0, 1}, {1, 0}, {-1, 2}} {
		if _, err := NewBudget(l); err == nil {
			t.Fatal("invalid limits")
		}
	}
	b, _ := NewBudget(Limits{2, math.MaxInt64})
	r, _ := b.TryReserve(Raw, 10)
	before := b.Snapshot()
	for _, n := range []int64{-1, math.MaxInt64} {
		if r.TryGrow(n) {
			t.Fatal("invalid growth accepted")
		}
	}
	r.Shrink(-1)
	r.Shrink(11)
	if _, ok := b.TryReserve(Kind(255), 1); ok {
		t.Fatal("invalid kind")
	}
	if _, ok := b.TryReserve(Content, -1); ok {
		t.Fatal("negative reserve")
	}
	if got := b.Snapshot(); got != before {
		t.Fatal("invalid operation changed statistics")
	}
	b.RecordOffer(Raw, Accepted, 10)
	b.RecordOffer(Content, DroppedCapacity, 5)
	b.RecordSettlement(Raw, Unconfirmed, 10)
	b.RecordSettlement(Content, Discarded, 5)
	b.RecordSettlement(Egress, Persisted, 2)
	b.RecordOffer(Kind(255), Accepted, 1)
	b.RecordOffer(Raw, OfferResult(255), 1)
	b.RecordSettlement(Raw, Settlement(255), 1)
	s := b.Snapshot()
	if s.ByKind[Raw].Offers[Accepted].Records != 1 || s.ByKind[Raw].Offers[Accepted].Bytes != 10 || s.ByKind[Raw].Settlements[Unconfirmed].Records != 1 || s.ByKind[Raw].Settlements[Persisted].Records != 0 || s.ByKind[Content].Settlements[Discarded].Bytes != 5 || s.ByKind[Egress].Settlements[Persisted].Records != 1 {
		t.Fatalf("wrong counters: %+v", s)
	}
	r.Release()
	if b.Snapshot().ByKind != s.ByKind {
		t.Fatal("release counted as offer/settlement")
	}
}
func TestConcurrentOwnership(t *testing.T) {
	b, _ := NewBudget(Limits{8, 64})
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for range 500 {
				r, ok := b.TryReserve(Raw, 4)
				if !ok {
					continue
				}
				r.TryGrow(4)
				s := b.Snapshot()
				if s.ReservedRecords > 8 || s.ReservedBytes > 64 {
					t.Error("capacity exceeded")
				}
				r.Shrink(1)
				r.Release()
				r.Release()
			}
		})
	}
	wg.Wait()
	if s := b.Snapshot(); s.ReservedRecords != 0 || s.ReservedBytes != 0 {
		t.Fatal("leaked capacity")
	}
	r, _ := b.TryReserve(Content, 8)
	for range 10 {
		wg.Go(func() { r.TryGrow(1); r.Shrink(1); r.Release() })
	}
	wg.Wait()
	if s := b.Snapshot(); s.ReservedRecords != 0 || s.ReservedBytes != 0 {
		t.Fatal("concurrent release leaked")
	}
}
