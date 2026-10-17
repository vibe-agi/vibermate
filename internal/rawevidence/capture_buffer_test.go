package rawevidence

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
)

func TestCaptureBuffersShareWorkerOwnershipAndTransferBody(t *testing.T) {
	budget, _ := recording.NewBudget(recording.Limits{Records: 3, Bytes: 1 << 20})
	repository := &blockingAppendRepository{memoryRepository: &memoryRepository{}, appendStarted: make(chan struct{}), appendRelease: make(chan struct{})}
	m, err := Open(context.Background(), Options{Budget: budget, Repository: repository, Random: bytes.NewReader(bytes.Repeat([]byte{1}, 64)), Clock: fixedClock{value: time.Now()}, Config: Config{MaximumQueueRecords: 3, MaximumQueueBytes: 1 << 20, MaximumBatchRecords: 1, MaximumBatchBytes: 1 << 20, FlushInterval: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(repository.appendRelease); _ = m.Shutdown(context.Background()) }()
	_, _ = m.Observe(context.Background(), testObservation())
	<-repository.appendStarted
	meta := CaptureMetadata{Context: testObservation().Context, Layer: LayerProviderResponse, StatusCode: 200}
	a, b := m.Capture(meta, 16384), m.Capture(meta, 16384)
	if a == nil || b == nil {
		t.Fatal("live buffer refused")
	}
	if budget.Snapshot().ReservedRecords != 3 {
		t.Fatal("worker or live buffers uncharged")
	}
	if extra := m.Capture(meta, 16384); extra != nil {
		t.Fatal("item bound bypassed")
	}
	input := bytes.Repeat([]byte("data: exact\n\n"), 500)
	a.ObserveBytes(input, FrameData)
	owned := a.body
	input[0] = '!'
	if a.Finish(TerminalMetadata{Complete: true}) != recording.Accepted {
		t.Fatal("finish refused")
	}
	// The worker is blocked, so the queued record is a stable ownership witness.
	item := <-m.records
	if &owned[0] != &item.record.Body[0] || item.record.Body[0] == '!' {
		t.Fatal("Finish cloned or retained borrowed body")
	}
	m.records <- item
	b.Discard()
	if budget.Snapshot().ReservedRecords != 2 {
		t.Fatal("discard leaked live buffer")
	}
}

func TestCaptureGrowthRefusalPreservesHonestPrefix(t *testing.T) {
	budget, _ := recording.NewBudget(recording.Limits{Records: 2, Bytes: 100 << 10})
	repo := &memoryRepository{}
	m, err := Open(context.Background(), Options{Budget: budget, Repository: repo, Random: bytes.NewReader(bytes.Repeat([]byte{1}, 64)), Clock: fixedClock{value: time.Now()}, Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown(context.Background())
	b := m.Capture(CaptureMetadata{Context: testObservation().Context, Layer: LayerProviderResponse}, DefaultMaximumBodyBytes)
	b.ObserveBytes([]byte("prefix"), FrameData)
	b.ObserveBytes(make([]byte, 128<<10), FrameData)
	if b.Finish(TerminalMetadata{Complete: true}) != recording.Accepted {
		t.Fatal("prefix refused")
	}
	if err := m.Flush(context.Background(), Watermark{}); err != nil {
		t.Fatal(err)
	}
	records := repo.snapshot()
	if len(records) != 1 || string(records[0].Body) != "prefix" || records[0].PayloadState != PayloadTruncated || records[0].DigestScope != DigestFull || records[0].BodyBytes != (128<<10)+6 {
		t.Fatalf("dishonest prefix: %+v", records)
	}
	if budget.Snapshot().ReservedBytes != 0 {
		t.Fatal("settlement leaked capacity")
	}
}

func TestStampedRawRejectsStaleAndReleasesRevokedQueue(t *testing.T) {
	repo := &memoryRepository{}
	m := openFenceTestManager(t, repo, Config{MaximumQueueRecords: 8, MaximumQueueBytes: 1 << 20, MaximumBatchRecords: 8, MaximumBatchBytes: 1 << 20, FlushInterval: time.Hour})
	o := testObservation()
	o.ScopeKind = ScopeRuntime
	o.ScopeID = ""
	if m.TryObserve(recording.Stamp{}, o) != recording.DroppedStale {
		t.Fatal("zero stamp accepted")
	}
	epoch, _ := recording.NewEpoch(1)
	token, _ := recording.NewScopeToken(epoch, captureidentity.Reference{}, recording.Fence{ArchiveEpoch: 1})
	if m.TryObserve(token.Stamp(), o) != recording.Accepted {
		t.Fatal("current observation refused")
	}
	o.Body[0] = '!'
	token.Revoke()
	if err := m.Flush(context.Background(), Watermark{}); err != nil {
		t.Fatal(err)
	}
	if len(repo.snapshot()) != 0 || m.budget.Snapshot().ReservedRecords != 0 {
		t.Fatal("revoked record retained or persisted")
	}
	if m.TryCapture(token.Stamp(), CaptureMetadata{Context: o.Context, Layer: o.Layer}) != nil {
		t.Fatal("revoked capture accepted")
	}
}

func TestTryObserveOwnsBorrowedBodyAndHeaders(t *testing.T) {
	repo := &memoryRepository{}
	m := openFenceTestManager(t, repo, Config{MaximumQueueRecords: 8, MaximumQueueBytes: 1 << 20, MaximumBatchRecords: 8, MaximumBatchBytes: 1 << 20, FlushInterval: time.Hour})
	o := testObservation()
	o.ScopeKind = ScopeRuntime
	o.ScopeID = ""
	epoch, _ := recording.NewEpoch(1)
	token, _ := recording.NewScopeToken(epoch, captureidentity.Reference{}, recording.Fence{ArchiveEpoch: 1})
	expected := bytes.Clone(o.Body)
	if m.TryObserve(token.Stamp(), o) != recording.Accepted {
		t.Fatal("offer refused")
	}
	o.Body[0] = '!'
	o.Headers.Set("Authorization", "changed")
	if err := m.Flush(context.Background(), Watermark{}); err != nil {
		t.Fatal(err)
	}
	records := repo.snapshot()
	if len(records) != 1 || !bytes.Equal(records[0].Body, expected) || bytes.Contains(records[0].PayloadMetadata, []byte("changed")) || bytes.Contains(records[0].PayloadMetadata, []byte("private-token")) {
		t.Fatal("borrowed or unredacted payload retained")
	}
	stats := m.budget.Snapshot().ByKind[recording.Raw]
	if stats.Offers[recording.Accepted].Records != 1 || stats.Settlements[recording.Persisted].Records != 1 {
		t.Fatalf("wrong offer/settlement accounting: %+v", stats)
	}
}

func TestCaptureRejectsNonlocalTerminalReason(t *testing.T) {
	m := openFenceTestManager(t, &memoryRepository{}, DefaultConfig())
	b := m.Capture(CaptureMetadata{Context: testObservation().Context, Layer: LayerProviderResponse}, 1024)
	b.ObserveBytes([]byte("prefix"), "")
	if got := b.Finish(TerminalMetadata{IncompleteReason: "private_transport_error"}); got != recording.DroppedInvalid {
		t.Fatalf("nonlocal reason accepted: %v", got)
	}
	if m.budget.Snapshot().ReservedRecords != 0 {
		t.Fatal("invalid terminal retained ownership")
	}
}

func TestCaptureGrowthChargesOldAndNewBackingTogether(t *testing.T) {
	budget, _ := recording.NewBudget(recording.Limits{Records: 2, Bytes: 70 << 10})
	m, err := Open(context.Background(), Options{Budget: budget, Repository: &memoryRepository{}, Random: bytes.NewReader(bytes.Repeat([]byte{1}, 64)), Clock: fixedClock{value: time.Now()}, Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown(context.Background())
	b := m.Capture(CaptureMetadata{Context: testObservation().Context, Layer: LayerProviderResponse}, 64<<10)
	b.ObserveBytes(make([]byte, 16<<10), "")
	before := budget.Snapshot().ReservedBytes
	b.ObserveBytes([]byte("x"), "")
	if len(b.body) != 16<<10 || budget.Snapshot().ReservedBytes != before {
		t.Fatal("growth failed to charge simultaneous old/new capacities")
	}
	b.Discard()
	if budget.Snapshot().ReservedBytes != 0 {
		t.Fatal("discard leaked bytes")
	}
}
