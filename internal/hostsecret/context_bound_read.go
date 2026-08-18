package hostsecret

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/secretstore"
)

const (
	// A host call still inside the delegate after this long is treated as
	// wedged. It is set far above any plausible user interaction because a
	// Keychain read can legitimately block on a Touch ID prompt; a call that has
	// not returned in half a minute is stuck or abandoned, not being answered.
	defaultHostCallWedgeAfter = 30 * time.Second
	// Stepping past a wedged call permanently costs the goroutine and the OS
	// thread its cgo call pinned, so the budget is finite and never reclaimed.
	// It exists because the alternative — refusing to step past — made the first
	// stuck call disable every credential read for the life of the process.
	defaultMaxWedgedHostCalls = 4
)

// ErrHostSecretsUnresponsive reports that the host secret API stopped returning
// and the lane has spent its budget for abandoning stuck calls. It is the
// `unavailable` secret state of design 06 §4.1 reaching a caller as an error,
// rather than each caller discovering it as its own deadline.
var ErrHostSecretsUnresponsive = errors.New(
	"host SecretStore is not responding",
)

// hostCallLane admits one caller at a time into a delegate that may never
// return, and can step past an occupant that has stopped responding a bounded
// number of times.
type hostCallLane struct {
	permits    chan struct{}
	wedgeAfter time.Duration

	mu        sync.Mutex
	minted    int
	maxWedged int
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
func (lane *hostCallLane) enter(ctx context.Context) error {
	select {
	case <-lane.permits:
		return nil
	default:
	}
	timer := time.NewTimer(lane.wedgeAfter)
	defer timer.Stop()
	select {
	case <-lane.permits:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	if !lane.mintReplacement() {
		return ErrHostSecretsUnresponsive
	}
	select {
	case <-lane.permits:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// leave returns the permit. A call that was already stepped past may still
// return much later; the buffer has room for every permit the lane can mint, so
// the hand-back never blocks and simply restores capacity.
func (lane *hostCallLane) leave() {
	select {
	case lane.permits <- struct{}{}:
	default:
	}
}

func (lane *hostCallLane) mintReplacement() bool {
	lane.mu.Lock()
	defer lane.mu.Unlock()
	if lane.minted >= lane.maxWedged {
		return false
	}
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
	if err := store.reads.enter(ctx); err != nil {
		return nil, err
	}
	// Unbuffered on purpose: the hand-off must be a rendezvous so the goroutine
	// learns that the caller gave up and can destroy a secret nothing will read.
	result := make(chan contextBoundReadResult)
	go func() {
		defer store.reads.leave()
		var value *secretstore.Value
		var err error
		if pinned {
			value, err = store.Store.ReadAtRevision(ctx, reference, expected)
		} else {
			value, err = store.Store.Read(ctx, reference)
		}
		if err != nil && value != nil {
			value.Destroy()
			value = nil
		}
		select {
		case result <- contextBoundReadResult{value: value, err: err}:
		case <-ctx.Done():
			// The caller gave up. Nothing else can reach this value.
			if value != nil {
				value.Destroy()
			}
		}
	}()
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
	if err := store.inspections.enter(ctx); err != nil {
		return secretstore.Metadata{}, err
	}
	result := make(chan contextBoundInspectResult)
	go func() {
		defer store.inspections.leave()
		metadata, err := store.Store.Inspect(ctx, reference)
		select {
		case result <- contextBoundInspectResult{metadata: metadata, err: err}:
		case <-ctx.Done():
		}
	}()
	select {
	case outcome := <-result:
		if err := ctx.Err(); err != nil {
			return secretstore.Metadata{}, err
		}
		return outcome.metadata, outcome.err
	case <-ctx.Done():
		return secretstore.Metadata{}, ctx.Err()
	}
}
