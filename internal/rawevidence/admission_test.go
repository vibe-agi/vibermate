package rawevidence

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestSaturatedWriterDoesNotWaitForStorage(t *testing.T) {
	repository := &blockingAppendRepository{memoryRepository: &memoryRepository{}, appendStarted: make(chan struct{}), appendRelease: make(chan struct{})}
	manager, err := Open(context.Background(), Options{Repository: repository, Random: bytes.NewReader(bytes.Repeat([]byte{1}, 64)), Clock: fixedClock{value: time.Now()}, Config: Config{MaximumQueueRecords: 1, MaximumQueueBytes: 1 << 20, MaximumBatchRecords: 1, MaximumBatchBytes: 1 << 20, FlushInterval: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(repository.appendRelease); _ = manager.Shutdown(context.Background()) }()
	if _, err := manager.Observe(context.Background(), testObservation()); err != nil {
		t.Fatal(err)
	}
	<-repository.appendStarted
	returned := make(chan struct{})
	go func() { _, _ = manager.Observe(context.Background(), testObservation()); close(returned) }()
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("saturated recording waited for storage")
	}
}

func TestCommittedReadDoesNotFlushBlockedWriter(t *testing.T) {
	repository := &blockingAppendRepository{memoryRepository: &memoryRepository{}, appendStarted: make(chan struct{}), appendRelease: make(chan struct{})}
	manager, err := Open(context.Background(), Options{Repository: repository, Random: bytes.NewReader(bytes.Repeat([]byte{1}, 64)), Clock: fixedClock{value: time.Now()}, Config: Config{MaximumQueueRecords: 1, MaximumQueueBytes: 1 << 20, MaximumBatchRecords: 1, MaximumBatchBytes: 1 << 20, FlushInterval: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(repository.appendRelease); _ = manager.Shutdown(context.Background()) }()
	_, _ = manager.Observe(context.Background(), testObservation())
	<-repository.appendStarted
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := manager.ListExchange(ctx, testObservation().ExchangeID); err != nil {
		t.Fatalf("committed read waited for optional writer: %v", err)
	}
}

func TestCanceledShutdownClosesAdmissionBeforeReturning(t *testing.T) {
	m := openFenceTestManager(t, &memoryRepository{}, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = m.Shutdown(ctx)
	if mark, _ := m.Observe(context.Background(), testObservation()); mark.Sequence != 0 {
		t.Fatal("canceled Shutdown returned with admission open")
	}
	<-m.shutdownDone
	if m.budget.Snapshot().ReservedRecords != 0 {
		t.Fatal("shutdown lost reservation ownership")
	}
}

type uncancelableRawRepository struct {
	*memoryRepository
	entered, release chan struct{}
}

func (r *uncancelableRawRepository) AppendBatch(ctx context.Context, records []StoredEnvelope, now time.Time) error {
	close(r.entered)
	<-r.release
	return r.memoryRepository.AppendBatch(ctx, records, now)
}

func TestShutdownDeadlineKeepsActualWorkerOwnership(t *testing.T) {
	repo := &uncancelableRawRepository{memoryRepository: &memoryRepository{}, entered: make(chan struct{}), release: make(chan struct{})}
	m := openFenceTestManager(t, repo, Config{MaximumQueueRecords: 2, MaximumQueueBytes: 1 << 20, MaximumBatchRecords: 1, MaximumBatchBytes: 1 << 20, FlushInterval: time.Hour})
	_, _ = m.Observe(context.Background(), testObservation())
	<-repo.entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if m.Shutdown(ctx) == nil {
		t.Fatal("blocked worker reported stopped")
	}
	select {
	case <-m.done:
		t.Fatal("live I/O reported joined")
	default:
	}
	if m.budget.Snapshot().ReservedRecords != 1 {
		t.Fatal("in-flight ownership released before I/O stopped")
	}
	close(repo.release)
	<-m.shutdownDone
	if m.budget.Snapshot().ReservedRecords != 0 {
		t.Fatal("joined worker leaked ownership")
	}
}

func TestTerminalScopeSealsAfterUnconfirmedBatchHasSettled(t *testing.T) {
	repo := &failingFenceRepository{memoryRepository: &memoryRepository{}, fail: true}
	m := openFenceTestManager(t, repo, DefaultConfig())
	o := testObservation()
	_, _ = m.Observe(context.Background(), o)
	terminal, err := m.PrepareTerminalScope(context.Background(), o.ScopeKind, o.ScopeID)
	if err != nil {
		t.Fatalf("settled optional loss prevented sealing: %v", err)
	}
	terminal.Commit()
	if m.Capture(CaptureMetadata{Context: o.Context, Layer: o.Layer}, 1024) != nil {
		t.Fatal("sealed scope admitted live capture")
	}
	if _, err := m.BeginScope(context.Background(), o.ScopeKind, o.ScopeID); err != ErrScopeTerminal {
		t.Fatalf("finished scope admitted a request: %v", err)
	}
	if mark, _ := m.Observe(context.Background(), o); mark.Sequence != 0 {
		t.Fatal("sealed scope admitted new observation")
	}
	if m.Statistics().DurableWatermark != 0 {
		t.Fatal("unconfirmed batch claimed durable")
	}
}
