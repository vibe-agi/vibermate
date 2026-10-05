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

type nativeClaimSnapshot struct {
	handle nativeClaimHandle
	class  nativeClaimPhysicalClass
}

// The sole registry supplies a transaction-local snapshot, not a persistent
// copy of Store stream/child associations. Only returned receipts select proof.
func (e *executor) snapshotRetirementClaims(ownerKey, sessionKey uint64, child nativetool.ID) []nativeClaimSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ownerKey == 0 || sessionKey == 0 {
		return nil
	}
	var snapshot []nativeClaimSnapshot
	for id, token := range e.claimed {
		if token.ownerKey == ownerKey && token.sessionKey == sessionKey && token.physicalClass != nativeClaimUnbound && (child == "" || child == id) {
			snapshot = append(snapshot, nativeClaimSnapshot{handle: nativeClaimHandle{id: id, token: token}, class: token.physicalClass})
		}
	}
	return snapshot
}

func (e *executor) noteRetirementProof(snapshot []nativeClaimSnapshot, receipts []nativetool.StartReceipt, ownerKey, sessionKey uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ownerKey == 0 || sessionKey == 0 {
		return
	}
	for _, receipt := range receipts {
		for _, prior := range snapshot {
			h := prior.handle
			if h.token == nil || h.id != receipt.OperationID() || e.claimed[h.id] != h.token {
				continue
			}
			if h.token.ownerKey != ownerKey || h.token.sessionKey != sessionKey || prior.class == nativeClaimUnbound || h.token.physicalClass != prior.class {
				continue
			}
			h.token.physicalTerminal = true
			e.collectClaimLocked(h)
		}
	}
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

// These snapshots are short-lived transaction inputs, not another registry.
// Zero affinity and observed streams cannot borrow ordinary closure proof.
func (e *executor) ordinaryClaimsLocked(ownerKey, sessionKey uint64) []nativeClaimHandle {
	if ownerKey == 0 {
		return nil
	}
	var handles []nativeClaimHandle
	for id, token := range e.claimed {
		if token.ownerKey == ownerKey && token.sessionKey != 0 && (sessionKey == 0 || token.sessionKey == sessionKey) && token.physicalClass == nativeClaimOwnedOrdinary {
			handles = append(handles, nativeClaimHandle{id: id, token: token})
		}
	}
	return handles
}

// e.mu is held; the actual matching Store closure must have returned nil.
// Recheck classification because observation may have raced the transaction.
func (e *executor) noteOrdinaryClaimsLocked(handles []nativeClaimHandle, ownerKey, sessionKey uint64) {
	for _, h := range handles {
		token := h.token
		if token != nil && e.claimed[h.id] == token && ownerKey != 0 && token.ownerKey == ownerKey && token.sessionKey != 0 && (sessionKey == 0 || token.sessionKey == sessionKey) && token.physicalClass == nativeClaimOwnedOrdinary {
			token.physicalTerminal = true
			e.collectClaimLocked(h)
		}
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
