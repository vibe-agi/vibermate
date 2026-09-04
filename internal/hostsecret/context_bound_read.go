package hostsecret

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/secretstore"
)

const (
	// A host call still inside the delegate after this long is treated as
	// wedged. Native stores are deliberately non-interactive: five seconds is
	// generous for an unlocked local credential store, while still letting the
	// App explain an unavailable store instead of hanging at launch.
	defaultHostCallWedgeAfter = 5 * time.Second
	// Stepping past a wedged call permanently costs the goroutine and the OS
	// thread its cgo call pinned, so the budget is finite and never reclaimed.
	// It exists because the alternative — refusing to step past — made the first
	// stuck call disable every credential read for the life of the process.
	defaultMaxWedgedHostCalls = 4
)

// ErrHostSecretsUnresponsive reports that the host secret API passed its
// responsiveness bound. It is the `unavailable` secret state of design 06
// §4.1 reaching a caller as an error, rather than each caller discovering the
// same wedged host boundary as its own unrelated deadline.
var ErrHostSecretsUnresponsive = fmt.Errorf(
	"host SecretStore is not responding: %w",
	secretstore.ErrUnavailable,
)

// hostCallLane admits one current caller at a time into a delegate that may
// never return. It can abandon a stopped occupant and admit one replacement a
// bounded number of times; abandoned OS calls may still physically remain.
type hostCallLane struct {
	permits    chan struct{}
	wedgeAfter time.Duration

	mu        sync.Mutex
	minted    int
	maxWedged int
	holder    *hostCallLease
}

// hostCallLease ties one consumed permit to the exact delegate call that owns
// it. Once the lane steps past that call, its eventual late return must not put
// an obsolete second permit back into the lane.
type hostCallLease struct {
	lane      *hostCallLane
	abandoned bool
	released  bool
}

func newHostCallLane(wedgeAfter time.Duration, maxWedged int) *hostCallLane {
	lane := &hostCallLane{
		permits:    make(chan struct{}, 1+maxWedged),
		wedgeAfter: wedgeAfter,
		maxWedged:  maxWedged,
	}
	lane.permits <- struct{}{}
	return lane
}

// enter blocks until this caller may run the delegate, its context ends, or the
// lane is judged unresponsive.
func (lane *hostCallLane) enter(ctx context.Context) (*hostCallLease, error) {
	select {
	case <-lane.permits:
		return lane.hold(), nil
	default:
	}
	observed := lane.current()
	timer := time.NewTimer(lane.wedgeAfter)
	defer timer.Stop()
	select {
	case <-lane.permits:
		return lane.hold(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	// Only step past the call that made this caller wait. If that call returned
	// at the deadline and another one acquired the lane, do not mistake the new
	// call for the old wedge.
	if observed == nil || !observed.abandon() {
		return nil, ErrHostSecretsUnresponsive
	}
	select {
	case <-lane.permits:
		return lane.hold(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (lane *hostCallLane) current() *hostCallLease {
	lane.mu.Lock()
	defer lane.mu.Unlock()
	return lane.holder
}

func (lane *hostCallLane) hold() *hostCallLease {
	lane.mu.Lock()
	defer lane.mu.Unlock()
	lease := &hostCallLease{lane: lane}
	lane.holder = lease
	return lease
}

func (lease *hostCallLease) release() {
	if lease == nil || lease.lane == nil {
		return
	}
	lane := lease.lane
	lane.mu.Lock()
	defer lane.mu.Unlock()
	if lease.released {
		return
	}
	lease.released = true
	if lease.abandoned || lane.holder != lease {
		return
	}
	lane.holder = nil
	select {
	case lane.permits <- struct{}{}:
	default:
	}
}

func (lease *hostCallLease) abandon() bool {
	if lease == nil || lease.lane == nil {
		return false
	}
	return lease.lane.abandon(lease)
}

func (lane *hostCallLane) abandon(expected *hostCallLease) bool {
	lane.mu.Lock()
	defer lane.mu.Unlock()
	if expected == nil || lane.holder == nil || lane.holder != expected ||
		lane.holder.released || lane.holder.abandoned || lane.minted >= lane.maxWedged {
		return false
	}
	lane.holder.abandoned = true
	lane.holder = nil
	lane.minted++
	lane.permits <- struct{}{}
	return true
}

// contextBoundReadStore isolates host secret APIs that may ignore Go context
// cancellation. One caller at a time enters each read-only delegate lane, so a
// blocked Security.framework call cannot block its caller and cannot create an
// unbounded goroutine or OS-thread pile-up behind it. Read and Inspect use
// separate lanes so a blocked health check cannot prevent a data-plane
// credential read, and vice versa.
//
// Serializing alone was not enough. A cgo call that never returns holds its lane
// forever, so the first stuck call disabled every later read for the life of the
// process and each caller learned it only as its own deadline. A lane may
// therefore step past an occupant that has stopped responding, a bounded number
// of times; each step permanently costs the goroutine and OS thread that call
// pinned, and once the budget is spent the lane reports
// ErrHostSecretsUnresponsive so the control plane can show the `unavailable`
// secret state instead of a timeout.
//
// Mutations remain direct calls on the embedded Store. Returning early from a
// mutation would make its commit result ambiguous; only read operations can be
// safely abandoned by the caller.
type contextBoundReadStore struct {
	secretstore.Store
	reads       *hostCallLane
	inspections *hostCallLane
}

var _ secretstore.Store = (*contextBoundReadStore)(nil)

type contextBoundReadResult struct {
	value *secretstore.Value
	err   error
}

type contextBoundInspectResult struct {
	metadata secretstore.Metadata
	err      error
}

func newContextBoundReadStore(delegate secretstore.Store) secretstore.Store {
	return newContextBoundReadStoreWithLimits(
		delegate, defaultHostCallWedgeAfter, defaultMaxWedgedHostCalls,
	)
}

// newContextBoundReadStoreWithLimits exists so a test can drive the wedge
// behaviour without waiting out the production threshold. There are no
// long-lived goroutines: an accepted call runs on a goroutine that exits when
// the delegate returns, so opening a store costs nothing and needs no shutdown,
// and only a call that never returns keeps one alive.
func newContextBoundReadStoreWithLimits(
	delegate secretstore.Store,
	wedgeAfter time.Duration,
	maxWedged int,
) secretstore.Store {
	return &contextBoundReadStore{
		Store:       delegate,
		reads:       newHostCallLane(wedgeAfter, maxWedged),
		inspections: newHostCallLane(wedgeAfter, maxWedged),
	}
}

func (store *contextBoundReadStore) Read(
	ctx context.Context,
	reference secretstore.Reference,
) (*secretstore.Value, error) {
	return store.read(ctx, reference, 0, false)
}

func (store *contextBoundReadStore) ReadAtRevision(
	ctx context.Context,
	reference secretstore.Reference,
	expected secretstore.Revision,
) (*secretstore.Value, error) {
	if expected == 0 || expected > secretstore.MaxRevision {
		return nil, secretstore.ErrRevisionConflict
	}
	return store.read(ctx, reference, expected, true)
}

func (store *contextBoundReadStore) read(
	ctx context.Context,
	reference secretstore.Reference,
	expected secretstore.Revision,
	pinned bool,
) (*secretstore.Value, error) {
	if ctx == nil {
		return nil, errors.New("host SecretStore read context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lease, err := store.reads.enter(ctx)
	if err != nil {
		return nil, err
	}
	callContext, cancelCall := context.WithCancel(ctx)
	defer cancelCall()
	// Unbuffered on purpose: the hand-off must be a rendezvous so the goroutine
	// learns that the caller gave up and can destroy a secret nothing will read.
	result := make(chan contextBoundReadResult)
	go func() {
		defer lease.release()
		var value *secretstore.Value
		var err error
		if pinned {
			value, err = store.Store.ReadAtRevision(
				callContext, reference, expected,
			)
		} else {
			value, err = store.Store.Read(callContext, reference)
		}
		if err != nil && value != nil {
			value.Destroy()
			value = nil
		}
		select {
		case result <- contextBoundReadResult{value: value, err: err}:
		case <-callContext.Done():
			// The caller gave up. Nothing else can reach this value.
			if value != nil {
				value.Destroy()
			}
		}
	}()
	timer := time.NewTimer(store.reads.wedgeAfter)
	defer timer.Stop()
	select {
	case outcome := <-result:
		if err := ctx.Err(); err != nil {
			if outcome.value != nil {
				outcome.value.Destroy()
			}
			return nil, err
		}
		return outcome.value, outcome.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		// The accepted call itself is wedged. Make one bounded replacement
		// available to later callers before returning the host-level failure.
		// The delegate may ignore cancellation, but cancelCall ensures a value it
		// eventually returns is destroyed instead of blocking on the rendezvous.
		lease.abandon()
		return nil, ErrHostSecretsUnresponsive
	}
}

func (store *contextBoundReadStore) Inspect(
	ctx context.Context,
	reference secretstore.Reference,
) (secretstore.Metadata, error) {
	if ctx == nil {
		return secretstore.Metadata{}, errors.New(
			"host SecretStore inspect context is nil",
		)
	}
	if err := ctx.Err(); err != nil {
		return secretstore.Metadata{}, err
	}
	lease, err := store.inspections.enter(ctx)
	if err != nil {
		return secretstore.Metadata{}, err
	}
	callContext, cancelCall := context.WithCancel(ctx)
	defer cancelCall()
	result := make(chan contextBoundInspectResult)
	go func() {
		defer lease.release()
		metadata, err := store.Store.Inspect(callContext, reference)
		select {
		case result <- contextBoundInspectResult{metadata: metadata, err: err}:
		case <-callContext.Done():
		}
	}()
	timer := time.NewTimer(store.inspections.wedgeAfter)
	defer timer.Stop()
	select {
	case outcome := <-result:
		if err := ctx.Err(); err != nil {
			return secretstore.Metadata{}, err
		}
		return outcome.metadata, outcome.err
	case <-ctx.Done():
		return secretstore.Metadata{}, ctx.Err()
	case <-timer.C:
		lease.abandon()
		return secretstore.Metadata{}, ErrHostSecretsUnresponsive
	}
}
