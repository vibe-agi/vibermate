package hostsecret

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/secretstore"
)

func TestContextBoundReadStoreCancelsCallersWithoutMultiplyingBlockedHostReads(
	t *testing.T,
) {
	t.Parallel()

	reference, err := secretstore.ParseReference("secret://test/blocked-read")
	if err != nil {
		t.Fatalf("parse reference: %v", err)
	}
	delegate := newBlockingReadStore()
	store := newContextBoundReadStore(delegate)

	firstContext, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		value, readErr := store.Read(firstContext, reference)
		if value != nil {
			value.Destroy()
		}
		firstDone <- readErr
	}()

	select {
	case <-delegate.started:
	case <-time.After(time.Second):
		t.Fatal("delegate read did not start")
	}
	cancelFirst()
	select {
	case readErr := <-firstDone:
		if !errors.Is(readErr, context.Canceled) {
			t.Fatalf("first read error = %v, want context canceled", readErr)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled caller remained blocked behind host read")
	}

	secondContext, cancelSecond := context.WithTimeout(
		context.Background(),
		20*time.Millisecond,
	)
	defer cancelSecond()
	value, readErr := store.Read(secondContext, reference)
	if value != nil {
		value.Destroy()
		t.Fatal("timed-out read returned a value")
	}
	if !errors.Is(readErr, context.DeadlineExceeded) {
		t.Fatalf("second read error = %v, want deadline exceeded", readErr)
	}
	if calls := delegate.readCount(); calls != 1 {
		t.Fatalf("delegate read calls = %d, want one bounded blocked call", calls)
	}

	close(delegate.release)
	var abandoned *secretstore.Value
	select {
	case abandoned = <-delegate.returned:
	case <-time.After(time.Second):
		t.Fatal("delegate did not return after release")
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, copyErr := abandoned.CopyBytes()
		if errors.Is(copyErr, secretstore.ErrDestroyed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("abandoned secret was not destroyed: %v", copyErr)
		}
		time.Sleep(time.Millisecond)
	}
}

type blockingReadStore struct {
	mu       sync.Mutex
	reads    int
	started  chan struct{}
	release  chan struct{}
	returned chan *secretstore.Value
}

func newBlockingReadStore() *blockingReadStore {
	return &blockingReadStore{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		returned: make(chan *secretstore.Value, 1),
	}
}

func (store *blockingReadStore) Read(
	context.Context,
	secretstore.Reference,
) (*secretstore.Value, error) {
	store.mu.Lock()
	store.reads++
	if store.reads == 1 {
		close(store.started)
	}
	store.mu.Unlock()
	<-store.release
	value, err := secretstore.NewValue([]byte("host-secret"))
	if err != nil {
		return nil, err
	}
	store.returned <- value
	return value, nil
}

func (store *blockingReadStore) ReadAtRevision(
	ctx context.Context,
	reference secretstore.Reference,
	_ secretstore.Revision,
) (*secretstore.Value, error) {
	return store.Read(ctx, reference)
}

func (store *blockingReadStore) Inspect(
	context.Context,
	secretstore.Reference,
) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, errors.New("unexpected inspect")
}

func (store *blockingReadStore) Replace(
	context.Context,
	secretstore.ReplaceCommand,
) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, errors.New("unexpected replace")
}

func (store *blockingReadStore) Delete(
	context.Context,
	secretstore.Reference,
) error {
	return errors.New("unexpected delete")
}

func (store *blockingReadStore) readCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.reads
}

func TestContextBoundReadStoreIsolatesBlockedInspectionsFromReads(t *testing.T) {
	t.Parallel()

	reference, err := secretstore.ParseReference("secret://test/blocked-inspect")
	if err != nil {
		t.Fatalf("parse reference: %v", err)
	}
	delegate := newBlockingInspectStore()
	store := newContextBoundReadStore(delegate)

	inspectContext, cancelInspect := context.WithCancel(context.Background())
	inspectDone := make(chan error, 1)
	go func() {
		_, inspectErr := store.Inspect(inspectContext, reference)
		inspectDone <- inspectErr
	}()
	select {
	case <-delegate.started:
	case <-time.After(time.Second):
		t.Fatal("delegate inspect did not start")
	}
	cancelInspect()
	select {
	case inspectErr := <-inspectDone:
		if !errors.Is(inspectErr, context.Canceled) {
			t.Fatalf("inspect error = %v, want context canceled", inspectErr)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled inspect caller remained blocked")
	}

	read, readErr := store.ReadAtRevision(
		context.Background(),
		reference,
		1,
	)
	if readErr != nil {
		t.Fatalf("read while inspect is blocked: %v", readErr)
	}
	read.Destroy()

	secondContext, cancelSecond := context.WithTimeout(
		context.Background(),
		20*time.Millisecond,
	)
	defer cancelSecond()
	if _, inspectErr := store.Inspect(secondContext, reference); !errors.Is(
		inspectErr,
		context.DeadlineExceeded,
	) {
		t.Fatalf("second inspect error = %v, want deadline exceeded", inspectErr)
	}
	if calls := delegate.inspectCount(); calls != 1 {
		t.Fatalf("delegate inspect calls = %d, want one bounded call", calls)
	}
	close(delegate.release)
}

type blockingInspectStore struct {
	mu          sync.Mutex
	inspections int
	started     chan struct{}
	release     chan struct{}
}

func newBlockingInspectStore() *blockingInspectStore {
	return &blockingInspectStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (store *blockingInspectStore) Read(
	context.Context,
	secretstore.Reference,
) (*secretstore.Value, error) {
	return secretstore.NewValue([]byte("host-secret"))
}

func (store *blockingInspectStore) ReadAtRevision(
	ctx context.Context,
	reference secretstore.Reference,
	_ secretstore.Revision,
) (*secretstore.Value, error) {
	return store.Read(ctx, reference)
}

func (store *blockingInspectStore) Inspect(
	context.Context,
	secretstore.Reference,
) (secretstore.Metadata, error) {
	store.mu.Lock()
	store.inspections++
	if store.inspections == 1 {
		close(store.started)
	}
	store.mu.Unlock()
	<-store.release
	return secretstore.Metadata{
		State:    secretstore.StateConfigured,
		Revision: 1,
	}, nil
}

func (store *blockingInspectStore) Replace(
	context.Context,
	secretstore.ReplaceCommand,
) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, errors.New("unexpected replace")
}

func (store *blockingInspectStore) Delete(
	context.Context,
	secretstore.Reference,
) error {
	return errors.New("unexpected delete")
}

func (store *blockingInspectStore) inspectCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.inspections
}

// A caller with no deadline is the normal ProductRuntime startup path. The
// host boundary, rather than every caller, must therefore put an upper bound on
// a Security.framework call that never returns.
func TestContextBoundReadStoreBoundsTheFirstWedgedHostCall(t *testing.T) {
	t.Parallel()

	reference, err := secretstore.ParseReference("secret://test/first-wedge")
	if err != nil {
		t.Fatal(err)
	}
	delegate := newBlockingReadStore()
	defer close(delegate.release)
	store := newContextBoundReadStoreWithLimits(delegate, 10*time.Millisecond, 1)

	done := make(chan error, 1)
	go func() {
		value, readErr := store.Read(context.Background(), reference)
		if value != nil {
			value.Destroy()
		}
		done <- readErr
	}()
	select {
	case readErr := <-done:
		if !errors.Is(readErr, ErrHostSecretsUnresponsive) ||
			!errors.Is(readErr, secretstore.ErrUnavailable) {
			t.Fatalf("first read error = %v, want host unavailable", readErr)
		}
	case <-time.After(time.Second):
		t.Fatal("first host read remained blocked past the host-call bound")
	}
}

func TestContextBoundReadStoreBoundsTheFirstWedgedHostInspection(t *testing.T) {
	t.Parallel()

	reference, err := secretstore.ParseReference("secret://test/first-inspection-wedge")
	if err != nil {
		t.Fatal(err)
	}
	delegate := newBlockingInspectStore()
	defer close(delegate.release)
	store := newContextBoundReadStoreWithLimits(delegate, 10*time.Millisecond, 1)

	done := make(chan error, 1)
	go func() {
		_, inspectErr := store.Inspect(context.Background(), reference)
		done <- inspectErr
	}()
	select {
	case inspectErr := <-done:
		if !errors.Is(inspectErr, ErrHostSecretsUnresponsive) {
			t.Fatalf("first inspect error = %v, want host unavailable", inspectErr)
		}
	case <-time.After(time.Second):
		t.Fatal("first host inspection remained blocked past the host-call bound")
	}
}

func TestHostCallLaneLateReturnDoesNotWidenConcurrency(t *testing.T) {
	t.Parallel()

	lane := newHostCallLane(time.Second, 1)
	lease, err := lane.enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !lease.abandon() {
		t.Fatal("failed to step past the occupied host-call lane")
	}
	// The original host call eventually returns after it was stepped past. Its
	// obsolete permit must not join the replacement permit and permanently turn
	// a serialized host boundary into a two-call lane.
	lease.release()
	if available := len(lane.permits); available != 1 {
		t.Fatalf("available host-call permits = %d, want 1", available)
	}
}

// One host call that never returns must not disable credential reads for the
// rest of the process. Serializing the lane is the right protection against an
// unbounded pile-up of pinned OS threads, but with no way past a wedged call it
// also meant the first stuck Security.framework call was terminal: every later
// read failed on its own deadline, forever, with nothing to distinguish that
// from a slow disk.
func TestContextBoundReadStoreStepsPastAWedgedHostCall(t *testing.T) {
	t.Parallel()

	reference, err := secretstore.ParseReference("secret://test/wedged")
	if err != nil {
		t.Fatal(err)
	}
	delegate := newBlockingReadStore()
	store := newContextBoundReadStoreWithLimits(delegate, time.Millisecond, 2)

	wedged, cancelWedged := context.WithCancel(context.Background())
	go func() {
		value, _ := store.Read(wedged, reference)
		if value != nil {
			value.Destroy()
		}
	}()
	select {
	case <-delegate.started:
	case <-time.After(time.Second):
		t.Fatal("delegate read did not start")
	}
	cancelWedged()

	// The first call is still inside the delegate and may never leave. A later
	// read must still reach the host.
	// This fake never returns, so the later read still ends on its own deadline.
	// What matters is that it reached the host at all: with no way past a wedged
	// call it would have expired waiting for the lane, leaving readCount at one.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	value, _ := store.Read(ctx, reference)
	if value != nil {
		value.Destroy()
	}
	if calls := delegate.readCount(); calls < 2 {
		t.Fatalf(
			"delegate read calls = %d; the lane never stepped past the wedge",
			calls,
		)
	}

	close(delegate.release)
}

// Stepping past a wedged call costs the goroutine and OS thread that call
// pinned, so the budget is finite. Once it is spent the store must say it is
// unavailable — the state the design already has a name for — instead of leaving
// every caller to discover it as a timeout.
func TestContextBoundReadStoreReportsUnavailableOnceTheWedgeBudgetIsSpent(
	t *testing.T,
) {
	t.Parallel()

	reference, err := secretstore.ParseReference("secret://test/exhausted")
	if err != nil {
		t.Fatal(err)
	}
	delegate := newBlockingReadStore()
	store := newContextBoundReadStoreWithLimits(delegate, time.Millisecond, 1)

	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		value, _ := store.Read(ctx, reference)
		if value != nil {
			value.Destroy()
		}
		cancel()
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	value, readErr := store.Read(ctx, reference)
	if value != nil {
		value.Destroy()
	}
	if !errors.Is(readErr, ErrHostSecretsUnresponsive) {
		t.Fatalf("read error = %v, want the store reported unavailable", readErr)
	}

	close(delegate.release)
}
