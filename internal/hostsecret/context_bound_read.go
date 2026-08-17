package hostsecret

import (
	"context"
	"errors"

	"github.com/vibe-agi/vibermate/internal/secretstore"
)

// contextBoundReadStore isolates host secret APIs that may ignore Go context
// cancellation. Exactly one worker may enter each read-only delegate lane. A
// blocked Security.framework call therefore cannot block its caller or create
// an unbounded goroutine/OS-thread pile-up on later reads or inspections.
// Read and Inspect use separate lanes so a blocked health check cannot prevent
// a data-plane credential read, and vice versa.
//
// Mutations remain direct calls on the embedded Store. Returning early from a
// mutation would make its commit result ambiguous; only read operations can be
// safely abandoned by the caller.
type contextBoundReadStore struct {
	secretstore.Store
	reads       chan contextBoundReadRequest
	inspections chan contextBoundInspectRequest
}

var _ secretstore.Store = (*contextBoundReadStore)(nil)

type contextBoundReadRequest struct {
	ctx              context.Context
	reference        secretstore.Reference
	expectedRevision secretstore.Revision
	pinned           bool
	result           chan contextBoundReadResult
}

type contextBoundReadResult struct {
	value *secretstore.Value
	err   error
}

type contextBoundInspectRequest struct {
	ctx       context.Context
	reference secretstore.Reference
	result    chan contextBoundInspectResult
}

type contextBoundInspectResult struct {
	metadata secretstore.Metadata
	err      error
}

func newContextBoundReadStore(delegate secretstore.Store) secretstore.Store {
	store := &contextBoundReadStore{
		Store:       delegate,
		reads:       make(chan contextBoundReadRequest),
		inspections: make(chan contextBoundInspectRequest),
	}
	go store.runReads()
	go store.runInspections()
	return store
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
	request := contextBoundReadRequest{
		ctx:              ctx,
		reference:        reference,
		expectedRevision: expected,
		pinned:           pinned,
		result:           make(chan contextBoundReadResult),
	}
	select {
	case store.reads <- request:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case result := <-request.result:
		if err := ctx.Err(); err != nil {
			if result.value != nil {
				result.value.Destroy()
			}
			return nil, err
		}
		return result.value, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (store *contextBoundReadStore) runReads() {
	for request := range store.reads {
		if request.ctx.Err() != nil {
			continue
		}
		var value *secretstore.Value
		var err error
		if request.pinned {
			value, err = store.Store.ReadAtRevision(
				request.ctx,
				request.reference,
				request.expectedRevision,
			)
		} else {
			value, err = store.Store.Read(request.ctx, request.reference)
		}
		if err != nil && value != nil {
			value.Destroy()
			value = nil
		}
		result := contextBoundReadResult{value: value, err: err}
		select {
		case request.result <- result:
		case <-request.ctx.Done():
			if value != nil {
				value.Destroy()
			}
		}
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
	request := contextBoundInspectRequest{
		ctx:       ctx,
		reference: reference,
		result:    make(chan contextBoundInspectResult),
	}
	select {
	case store.inspections <- request:
	case <-ctx.Done():
		return secretstore.Metadata{}, ctx.Err()
	}
	select {
	case result := <-request.result:
		if err := ctx.Err(); err != nil {
			return secretstore.Metadata{}, err
		}
		return result.metadata, result.err
	case <-ctx.Done():
		return secretstore.Metadata{}, ctx.Err()
	}
}

func (store *contextBoundReadStore) runInspections() {
	for request := range store.inspections {
		if request.ctx.Err() != nil {
			continue
		}
		metadata, err := store.Store.Inspect(
			request.ctx,
			request.reference,
		)
		select {
		case request.result <- contextBoundInspectResult{
			metadata: metadata,
			err:      err,
		}:
		case <-request.ctx.Done():
		}
	}
}
