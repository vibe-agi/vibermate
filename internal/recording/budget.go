package recording

import (
	"errors"
	"math"
	"sync"
)

// Limits applies to all lanes sharing one Budget, including preparation and
// worker-held storage. It is a capacity bound, not an RSS estimate.
type Limits struct{ Records, Bytes int64 }

// Budget must not be copied. Operations use short in-memory critical sections;
// admission never waits for capacity or for any owner to finish.
type Budget struct {
	mu         sync.Mutex
	limits     Limits
	statistics Statistics
}

// Reservation owns one record slot and retained allocation bytes until Release.
// Transfer the pointer between producer, queue and worker without releasing it.
// Copies share the same release state. A zero Reservation is inert.
type Reservation struct{ state *reservationState }
type reservationState struct {
	budget   *Budget
	bytes    int64
	released bool
}

func NewBudget(limits Limits) (*Budget, error) {
	if limits.Records <= 0 || limits.Bytes <= 0 {
		return nil, errors.New("recording limits must be positive")
	}
	return &Budget{limits: limits}, nil
}

// TryReserve atomically charges an item and its bytes; zero bytes is valid.
// Allocation accounting does not increment offer or settlement counters.
func (b *Budget) TryReserve(kind Kind, bytes int64) (*Reservation, bool) {
	if b == nil || kind >= KindCount || bytes < 0 {
		return nil, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s := &b.statistics
	if s.ReservedRecords >= b.limits.Records || bytes > b.limits.Bytes-s.ReservedBytes {
		return nil, false
	}
	s.ReservedRecords++
	s.ReservedBytes += bytes
	return &Reservation{state: &reservationState{budget: b, bytes: bytes}}, true
}

// TryGrow charges additional simultaneously live bytes. Before replacing a
// buffer, charge the complete new capacity while the old allocation is live;
// after transferring data, Shrink the old capacity. Refusal leaves totals intact.
func (r *Reservation) TryGrow(bytes int64) bool {
	if r == nil || r.state == nil || bytes < 0 {
		return false
	}
	state := r.state
	b := state.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if state.released || bytes > b.limits.Bytes-b.statistics.ReservedBytes {
		return false
	}
	state.bytes += bytes
	b.statistics.ReservedBytes += bytes
	return true
}

// Shrink releases bytes only; invalid or excessive reductions are ignored.
func (r *Reservation) Shrink(bytes int64) {
	if r == nil || r.state == nil || bytes < 0 {
		return
	}
	state := r.state
	b := state.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if state.released || bytes > state.bytes {
		return
	}
	state.bytes -= bytes
	b.statistics.ReservedBytes -= bytes
}

// Release is idempotent and safe concurrently with growth or shrinkage.
func (r *Reservation) Release() {
	if r == nil || r.state == nil {
		return
	}
	state := r.state
	b := state.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if state.released {
		return
	}
	state.released = true
	b.statistics.ReservedRecords--
	b.statistics.ReservedBytes -= state.bytes
	state.bytes = 0
}

func (b *Budget) Snapshot() Statistics {
	if b == nil {
		return Statistics{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.statistics
}

// RecordOffer is called once by the producer when an offer completes. bytes is
// retained observation size, excluding temporary workspace. Accepted is not a
// persistence acknowledgment. Invalid enum values are ignored.
func (b *Budget) RecordOffer(kind Kind, result OfferResult, bytes uint64) {
	if b == nil || kind >= KindCount || result >= OfferResultCount {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	addCount(&b.statistics.ByKind[kind].Offers[result], bytes)
}

// RecordSettlement is called once by the worker per accepted observation.
// Unconfirmed is an ambiguous write, distinct from a known discarded copy.
func (b *Budget) RecordSettlement(kind Kind, result Settlement, bytes uint64) {
	if b == nil || kind >= KindCount || result >= SettlementCount {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	addCount(&b.statistics.ByKind[kind].Settlements[result], bytes)
}

// Lifetime counters saturate rather than wrapping and misreporting loss.
func addCount(count *Count, bytes uint64) {
	if count.Records < math.MaxUint64 {
		count.Records++
	}
	if bytes > math.MaxUint64-count.Bytes {
		count.Bytes = math.MaxUint64
	} else {
		count.Bytes += bytes
	}
}
