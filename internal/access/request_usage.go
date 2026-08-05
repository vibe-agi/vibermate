package access

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var ErrAccessRequestAdmissionClosed = errors.New("Access request admission is closed")

// RequestUseAdmitter accounts for one complete request after MITM and before
// current-plan revalidation. A deletion fence closes this admission boundary
// before it tests for drain, so a concurrent HTTP/2 stream cannot enter between
// a zero-count observation and durable deletion.
type RequestUseAdmitter interface {
	AdmitRequest(context.Context, IngressBinding) (RequestUse, error)
}

// RequestUse pins only the Access lifetime. The Exchange still resolves
// exactly one immutable plan snapshot and owns its own request/egress leases.
// The narrow interface keeps the proxy independent from the gate's concrete
// implementation while preserving one mandatory release operation.
type RequestUse interface {
	Release()
}

type requestUseLease struct {
	gate     *requestUsageGate
	accessID AccessID
	once     sync.Once
}

// Release is idempotent.
func (lease *requestUseLease) Release() {
	if lease == nil || lease.gate == nil {
		return
	}
	lease.once.Do(func() {
		lease.gate.release(lease.accessID)
	})
}

type requestUsageEntry struct {
	active  int
	closed  bool
	retired bool
	changed chan struct{}
}

type requestUsageGate struct {
	mu sync.Mutex

	stopping bool
	entries  map[AccessID]*requestUsageEntry
}

func newRequestUsageGate() *requestUsageGate {
	return &requestUsageGate{entries: make(map[AccessID]*requestUsageEntry)}
}

func (gate *requestUsageGate) admit(
	ctx context.Context,
	binding IngressBinding,
) (RequestUse, error) {
	if ctx == nil {
		return nil, errors.New("Access request context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("admit Access request: %w", err)
	}
	if err := binding.Validate(); err != nil {
		return nil, err
	}

	gate.mu.Lock()
	defer gate.mu.Unlock()
	entry := gate.entryLocked(binding.AccessID())
	if gate.stopping || entry.closed || entry.retired {
		return nil, ErrAccessRequestAdmissionClosed
	}
	entry.active++
	gate.notifyLocked(entry)
	return &requestUseLease{gate: gate, accessID: binding.AccessID()}, nil
}

func (gate *requestUsageGate) release(accessID AccessID) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	entry := gate.entries[accessID]
	if entry == nil || entry.active <= 0 {
		panic("Access request usage count became negative")
	}
	entry.active--
	gate.notifyLocked(entry)
}

func (gate *requestUsageGate) active(accessID AccessID) int {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	entry := gate.entries[accessID]
	if entry == nil {
		return 0
	}
	return entry.active
}

// beginDeletion closes admission for one Access and waits for all requests
// admitted before the cut. The returned fence must be committed after durable
// deletion or aborted on every other path.
func (gate *requestUsageGate) beginDeletion(
	ctx context.Context,
	accessID AccessID,
) (*requestDeletionFence, error) {
	if ctx == nil {
		return nil, errors.New("Access deletion drain context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("begin Access deletion drain: %w", err)
	}

	gate.mu.Lock()
	entry := gate.entryLocked(accessID)
	if gate.stopping || entry.closed || entry.retired {
		gate.mu.Unlock()
		return nil, ErrAccessRequestAdmissionClosed
	}
	entry.closed = true
	gate.notifyLocked(entry)
	fence := &requestDeletionFence{gate: gate, accessID: accessID}
	for entry.active > 0 {
		changed := entry.changed
		gate.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			fence.Abort()
			return nil, fmt.Errorf("drain Access requests: %w", ctx.Err())
		}
		gate.mu.Lock()
		entry = gate.entryLocked(accessID)
	}
	gate.mu.Unlock()
	return fence, nil
}

func (gate *requestUsageGate) close() {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.stopping = true
	for _, entry := range gate.entries {
		entry.closed = true
		gate.notifyLocked(entry)
	}
}

func (gate *requestUsageGate) entryLocked(accessID AccessID) *requestUsageEntry {
	entry := gate.entries[accessID]
	if entry == nil {
		entry = &requestUsageEntry{changed: make(chan struct{})}
		gate.entries[accessID] = entry
	}
	return entry
}

func (gate *requestUsageGate) notifyLocked(entry *requestUsageEntry) {
	close(entry.changed)
	entry.changed = make(chan struct{})
}

type requestDeletionFence struct {
	gate     *requestUsageGate
	accessID AccessID
	once     sync.Once
}

func (fence *requestDeletionFence) Commit() {
	if fence == nil || fence.gate == nil {
		return
	}
	fence.once.Do(func() {
		fence.gate.mu.Lock()
		defer fence.gate.mu.Unlock()
		entry := fence.gate.entryLocked(fence.accessID)
		if entry.active != 0 || !entry.closed {
			panic("Access deletion fence committed before drain")
		}
		entry.retired = true
		fence.gate.notifyLocked(entry)
	})
}

func (fence *requestDeletionFence) Abort() {
	if fence == nil || fence.gate == nil {
		return
	}
	fence.once.Do(func() {
		fence.gate.mu.Lock()
		defer fence.gate.mu.Unlock()
		entry := fence.gate.entryLocked(fence.accessID)
		if !entry.retired && !fence.gate.stopping {
			entry.closed = false
		}
		fence.gate.notifyLocked(entry)
	})
}
