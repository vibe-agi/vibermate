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
