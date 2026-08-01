package toolapproval

import "sync"

// waiterRegistry merges identical pending questions onto one entry. A burst of
// the same question must be one prompt with counts rather than one prompt per
// event, so callers that arrive with an aggregate key already pending attach to
// the existing entry and are all released by its single decision.
//
// It owns no durability. The record is the durable fact; this is the runtime
// rendezvous that decides which record a caller is waiting on.
type waiterRegistry struct {
	mu sync.Mutex
	// byAggregate maps a pending question to its entry.
	byAggregate map[string]*registryEntry
	// byRecord finds an entry from the record it represents.
	byRecord map[string]*registryEntry
}

// registryEntry is one open question and everyone waiting on it.
//
// The entry is published to later callers before its durable record exists,
// because the caller that creates the record must hold the entry to stop a
// second caller from creating a second one. `ready` closes when that race is
// over: a joiner that acted earlier would be pointing at a row that has not
// been written yet, or at one whose write failed.
type registryEntry struct {
	recordID string
	ready    chan struct{}
	// created reports whether the durable record exists. It is written once,
	// before ready closes, and read only after ready closes.
	created bool
	waiters []*waiter
}

func newWaiterRegistry() *waiterRegistry {
	return &waiterRegistry{
		byAggregate: make(map[string]*registryEntry),
		byRecord:    make(map[string]*registryEntry),
	}
}

// join attaches a caller to the entry for this question. It reports whether an
// entry already existed, so the caller knows not to create a second record.
// The proposed record ID is used only when this call creates the entry.
func (registry *waiterRegistry) join(
	aggregateKey string,
	proposedRecordID string,
) (*waiter, *registryEntry, bool) {
	pending := &waiter{result: make(chan Record, 1)}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if existing, found := registry.byAggregate[aggregateKey]; found {
		existing.waiters = append(existing.waiters, pending)
		return pending, existing, true
	}
	entry := &registryEntry{
		recordID: proposedRecordID,
		ready:    make(chan struct{}),
		waiters:  []*waiter{pending},
	}
	registry.byAggregate[aggregateKey] = entry
	registry.byRecord[proposedRecordID] = entry
	return pending, entry, false
}

// publish reports the outcome of writing the durable record and releases every
// caller that has been waiting to hear it.
func (registry *waiterRegistry) publish(entry *registryEntry, created bool) {
	registry.mu.Lock()
	entry.created = created
	registry.mu.Unlock()
	close(entry.ready)
}

// durable reports whether the entry's record was written. It is meaningful
// only after the entry is ready.
func (registry *waiterRegistry) durable(entry *registryEntry) bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return entry.created
}

func (registry *waiterRegistry) waiterCount(recordID string) int {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entry, found := registry.byRecord[recordID]
	if !found {
		return 0
	}
	return len(entry.waiters)
}

// resolve releases every caller waiting on this record. One decision answers
// the whole entry, which is the point of merging.
func (registry *waiterRegistry) resolve(recordID string, record Record) {
	registry.mu.Lock()
	entry, found := registry.byRecord[recordID]
	if !found {
		registry.mu.Unlock()
		return
	}
	pending := entry.waiters
	registry.clearLocked(recordID)
	registry.mu.Unlock()
	for _, waiting := range pending {
		select {
		case waiting.result <- record:
		default:
		}
	}
}

// remove detaches one caller. The entry survives while other callers still
// wait on it, so one cancelled request does not answer the others.
func (registry *waiterRegistry) remove(recordID string, pending *waiter) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entry, found := registry.byRecord[recordID]
	if !found {
		return
	}
	remaining := entry.waiters[:0]
	for _, waiting := range entry.waiters {
		if waiting != pending {
			remaining = append(remaining, waiting)
		}
	}
	if len(remaining) == 0 {
		registry.clearLocked(recordID)
		return
	}
	entry.waiters = remaining
}

func (registry *waiterRegistry) clearLocked(recordID string) {
	entry, found := registry.byRecord[recordID]
	if !found {
		return
	}
	entry.waiters = nil
	delete(registry.byRecord, recordID)
	for aggregateKey, candidate := range registry.byAggregate {
		if candidate == entry {
			delete(registry.byAggregate, aggregateKey)
			break
		}
	}
}

// pendingRecordIDs lists the entries a shutdown must cancel.
func (registry *waiterRegistry) pendingRecordIDs() []string {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	identifiers := make([]string, 0, len(registry.byRecord))
	for recordID := range registry.byRecord {
		identifiers = append(identifiers, recordID)
	}
	return identifiers
}
