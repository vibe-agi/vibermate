package nativemcp

import "github.com/vibe-agi/vibermate/internal/nativetool"

type nativeClaimPhysicalClass uint8

const (
	nativeClaimUnbound nativeClaimPhysicalClass = iota
	nativeClaimOwnedOrdinary
	nativeClaimOwnedObserved
)

const nativeClaimReservationBytes int64 = 128

// Cleanup identities are local generations, never send or database authority.
// A token retains neither its owner/session nor any request/response payload.
type nativeClaim struct {
	ownerKey, sessionKey           uint64
	physicalClass                  nativeClaimPhysicalClass
	consumerDone, physicalTerminal bool
}

type nativeClaimHandle struct {
	id    nativetool.ID
	token *nativeClaim
}

func (e *executor) notePhysicalTerminal(h nativeClaimHandle) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if h.token != nil && e.claimed[h.id] == h.token {
		h.token.physicalTerminal = true
		e.collectClaimLocked(h)
	}
}

func (e *executor) drainClaimConsumer(h nativeClaimHandle) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if h.token != nil && e.claimed[h.id] == h.token {
		h.token.consumerDone = true
		e.collectClaimLocked(h)
	}
}

func (e *executor) collectClaimLocked(h nativeClaimHandle) {
	if h.token != nil && e.claimed[h.id] == h.token && h.token.physicalTerminal && h.token.consumerDone {
		delete(e.claimed, h.id)
		e.budget.release(nativeClaimReservationBytes)
	}
}

func (e *executor) observeClaim(h nativeClaimHandle) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if h.token != nil && e.claimed[h.id] == h.token && h.token.physicalClass == nativeClaimOwnedOrdinary {
		h.token.physicalClass = nativeClaimOwnedObserved
	}
}

// Called under e.mu before any durable admission mutation. Burning keys on a
// later failure is intentional: identities must never be reused or wrap.
func (e *executor) reserveClaimKeysLocked(count uint64) (uint64, error) {
	if count == 0 || ^uint64(0)-e.lastClaimKey < count {
		return 0, errBudget
	}
	first := e.lastClaimKey + 1
	e.lastClaimKey += count
	return first, nil
}
