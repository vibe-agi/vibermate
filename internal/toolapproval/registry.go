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
	// byAggregate maps a pending question to the record that represents it.
	byAggregate map[string]string
	// waiters holds every caller waiting on a record.
	waiters map[string][]*waiter
	// aggregateOf lets a resolved or emptied record clear its own index entry
	// without scanning.
	aggregateOf map[string]string
}

func newWaiterRegistry() *waiterRegistry {
	return &waiterRegistry{
		byAggregate: make(map[string]string),
		waiters:     make(map[string][]*waiter),
		aggregateOf: make(map[string]string),
	}
}

// join attaches a caller to the entry for this question. It reports whether an
// entry already existed, so the caller knows not to create a second record.
// The proposed record ID is used only when this call creates the entry.
func (registry *waiterRegistry) join(
	aggregateKey string,
	proposedRecordID string,
) (*waiter, bool) {
	pending := &waiter{result: make(chan Record, 1)}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if existing, found := registry.byAggregate[aggregateKey]; found {
		registry.waiters[existing] = append(registry.waiters[existing], pending)
		return pending, true
	}
	registry.byAggregate[aggregateKey] = proposedRecordID
	registry.aggregateOf[proposedRecordID] = aggregateKey
	registry.waiters[proposedRecordID] = []*waiter{pending}
	return pending, false
}

func (registry *waiterRegistry) recordFor(aggregateKey string) string {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.byAggregate[aggregateKey]
}

func (registry *waiterRegistry) waiterCount(recordID string) int {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return len(registry.waiters[recordID])
}

// resolve releases every caller waiting on this record. One decision answers
// the whole entry, which is the point of merging.
func (registry *waiterRegistry) resolve(recordID string, record Record) {
	registry.mu.Lock()
	pending := registry.waiters[recordID]
	registry.clearLocked(recordID)
	registry.mu.Unlock()
	for _, entry := range pending {
		select {
		case entry.result <- record:
		default:
		}
	}
}

// remove detaches one caller. The entry survives while other callers still
// wait on it, so one cancelled request does not answer the others.
func (registry *waiterRegistry) remove(recordID string, pending *waiter) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	remaining := registry.waiters[recordID][:0]
	for _, entry := range registry.waiters[recordID] {
		if entry != pending {
			remaining = append(remaining, entry)
		}
	}
	if len(remaining) == 0 {
		registry.clearLocked(recordID)
		return
	}
	registry.waiters[recordID] = remaining
}

func (registry *waiterRegistry) clearLocked(recordID string) {
	delete(registry.waiters, recordID)
	if aggregateKey, found := registry.aggregateOf[recordID]; found {
		delete(registry.byAggregate, aggregateKey)
		delete(registry.aggregateOf, recordID)
	}
}

// pendingRecordIDs lists the entries a shutdown must cancel.
func (registry *waiterRegistry) pendingRecordIDs() []string {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	identifiers := make([]string, 0, len(registry.waiters))
	for recordID := range registry.waiters {
		identifiers = append(identifiers, recordID)
	}
	return identifiers
}
