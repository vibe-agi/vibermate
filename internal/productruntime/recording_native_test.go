package productruntime

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/recording"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type nativeRecordedSink struct {
	entered chan nativetool.RecordedHistory
	release chan struct{}
}

func (s *nativeRecordedSink) PutNativeHistory(_ context.Context, h nativetool.RecordedHistory) error {
	s.entered <- h
	<-s.release
	return nil
}
func nativeRecordingFixture(t *testing.T) (*observationRecorder, *recording.Budget, *nativeRecordedSink, recording.Stamp, nativetool.HistoryObservation) {
	t.Helper()
	budget, _ := recording.NewBudget(recording.Limits{Records: 2048, Bytes: 64 << 20})
	sink := &nativeRecordedSink{make(chan nativetool.RecordedHistory, 4), make(chan struct{})}
	r := newObservationRecorder(context.Background(), budget, nil, nil)
	r.nativeHistory = sink
	t.Cleanup(func() {
		close(sink.release)
		if err := r.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	stamp := r.ScopeStamp(captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: "capture"})
	now := time.Now().UTC().Truncate(time.Millisecond)
	count := int64(7)
	o := nativetool.HistoryObservation{Operation: nativetool.Operation{ID: "11111111111111111111111111111111", Capture: stamp.Scope().Capture, RecordingScope: stamp.Scope(), EnvironmentID: "environment", EnvironmentRevision: 1, Recording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, Kind: nativetool.RequestToolsCall, TargetOrigin: "https://example.test", Revision: 4, State: "completed", DecisionSource: "user", Reason: "completed", CreatedAt: now.Add(-time.Minute), CompletedAt: &now, BytesIn: &count}, EventKind: "completed", EventAt: now, MaximumEventAt: now, MetadataExpiresAt: now.Add(29 * 24 * time.Hour)}
	o.Operation.EnvironmentDigest[0] = 1
	o.Operation.MaximumEventAt = o.MaximumEventAt
	o.Operation.MetadataExpiresAt = o.MetadataExpiresAt
	return r, budget, sink, stamp, o
}
func TestNativePreparedHistoryOwnsBorrowAndWaitsForPublication(t *testing.T) {
	r, b, sink, stamp, o := nativeRecordingFixture(t)
	ticket, result := r.PrepareNativeHistory(stamp, o)
	if result != recording.Accepted || ticket == nil {
		t.Fatalf("prepare = %v", result)
	}
	if b.Snapshot().ReservedRecords != 1 || b.Snapshot().ReservedBytes > 16<<10 {
		t.Fatalf("prepared budget: %+v", b.Snapshot())
	}
	select {
	case <-sink.entered:
		t.Fatal("unpublished history reached writer")
	default:
	}
	*o.Operation.BytesIn = 99
	if ticket.Publish() != recording.Accepted {
		t.Fatal("publish refused")
	}
	got := <-sink.entered
	if got.Observation.Operation.BytesIn == nil || *got.Observation.Operation.BytesIn != 7 {
		t.Fatal("producer pointer retained")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); ticket.Publish(); ticket.Discard(nativetool.HistoryRolledBack) }()
	}
	wg.Wait()
	if b.Snapshot().ReservedRecords != 1 {
		t.Fatal("ownership released before writer returned")
	}
	select {
	case <-sink.entered:
		t.Fatal("duplicate publication")
	default:
	}
}
func TestNativePreparedHistoryDiscardAndStalePublishReleaseOnce(t *testing.T) {
	for _, mode := range []string{"discard", "stale", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			r, b, _, stamp, o := nativeRecordingFixture(t)
			if mode == "oversize" {
				o.Operation.ToolName = strings.Repeat("x", 20<<10)
			}
			ticket, result := r.PrepareNativeHistory(stamp, o)
			if mode == "oversize" {
				if ticket != nil || result != recording.DroppedTooLarge {
					t.Fatal("oversized preparation retained")
				}
				return
			}
			if result != recording.Accepted {
				t.Fatal(result)
			}
			if mode == "stale" {
				r.epoch.Advance(2)
				if ticket.Publish() != recording.DroppedStale {
					t.Fatal("stale ticket published")
				}
			} else {
				ticket.Discard(nativetool.HistoryCommitUnknown)
			}
			ticket.Discard(nativetool.HistoryOwnerCanceled)
			if b.Snapshot().ReservedRecords != 0 {
				t.Fatal("discard leaked reservation")
			}
			if b.Snapshot().ByKind[recording.Native].Settlements[recording.Unconfirmed].Records != 0 {
				t.Fatal("unknown control commit invented observation write")
			}
		})
	}
}

type nativeCountingHistorySink struct{ writes atomic.Int64 }

func (s *nativeCountingHistorySink) PutNativeHistory(context.Context, nativetool.RecordedHistory) error {
	s.writes.Add(1)
	return nil
}
func TestNativePreparedHistoryFirstPublishDiscardRaceSettlesExactlyOnce(t *testing.T) {
	r, b, _, stamp, o := nativeRecordingFixture(t)
	sink := &nativeCountingHistorySink{}
	r.nativeHistory = sink
	for i := 0; i < 100; i++ {
		ticket, result := r.PrepareNativeHistory(stamp, o)
		if result != recording.Accepted {
			t.Fatal(result)
		}
		ready := make(chan struct{})
		var wg sync.WaitGroup
		for j := 0; j < 8; j++ {
			wg.Add(1)
			go func(publish bool) {
				defer wg.Done()
				<-ready
				if publish {
					ticket.Publish()
				} else {
					ticket.Discard(nativetool.HistoryRolledBack)
				}
			}(j%2 == 0)
		}
		close(ready)
		wg.Wait()
	}
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats := b.Snapshot()
	accepted := stats.ByKind[recording.Native].Offers[recording.Accepted].Records
	discarded := stats.ByKind[recording.Native].Offers[recording.DroppedRolledBack].Records
	if stats.ReservedRecords != 0 || stats.ReservedBytes != 0 || accepted+discarded != 100 || uint64(sink.writes.Load()) != accepted {
		t.Fatalf("ticket settled more/less than once: %+v writes=%d", stats, sink.writes.Load())
	}
}
func TestNativePreparedHistoryStringBudgetBoundary(t *testing.T) {
	r, b, _, stamp, o := nativeRecordingFixture(t)
	o.Operation.Capture.ID = strings.Repeat("c", 128)
	stamp = r.ScopeStamp(o.Operation.Capture)
	o.Operation.RecordingScope = stamp.Scope()
	o.Operation.EnvironmentID = environment.EnvironmentID(strings.Repeat("e", 128))
	o.Operation.ToolName = strings.Repeat("x", 256)
	remaining := 1024 - nativeHistoryStringBytes(o)
	if remaining <= 0 || remaining > 128 {
		t.Fatal("fixture exceeds admitted string bound")
	}
	o.Operation.CredentialOwner = strings.Repeat("x", int(remaining))
	ticket, result := r.PrepareNativeHistory(stamp, o)
	if result != recording.Accepted {
		t.Fatalf("exact accounted boundary refused: %v", result)
	}
	ticket.Discard(nativetool.HistoryOwnerCanceled)
	o.Operation.CredentialOwner += "x"
	ticket, result = r.PrepareNativeHistory(stamp, o)
	if ticket != nil || result != recording.DroppedTooLarge || b.Snapshot().ReservedRecords != 0 {
		t.Fatal("over-bound snapshot retained")
	}
}
