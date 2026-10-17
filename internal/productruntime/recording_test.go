package productruntime

import (
	"context"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/recording"
	"strings"
	"testing"
	"time"
	"unsafe"
)

type blockedObservationStore struct {
	entered chan struct{}
	release chan struct{}
}

func (s *blockedObservationStore) PutConnection(context.Context, recording.Scope, connectionevent.Event) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return errors.New("synthetic loss")
}
func (s *blockedObservationStore) PutScopedAttempt(context.Context, recording.Scope, egressaudit.Attempt) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return errors.New("synthetic loss")
}
func TestRecorderOwnsBlockedWritesAndClearEpoch(t *testing.T) {
	b, _ := recording.NewBudget(recording.Limits{Records: 2, Bytes: 1 << 20})
	s := &blockedObservationStore{make(chan struct{}, 1), make(chan struct{})}
	r := newObservationRecorder(context.Background(), b, s, s)
	stamp := r.ScopeStamp(captureidentity.Reference{})
	event := connectionevent.Event{ConnectionID: "connection", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Now().UTC()}
	if r.ObserveConnection(stamp, event) != recording.Accepted {
		t.Fatal("first refused")
	}
	<-s.entered
	if r.ObserveConnection(stamp, event) != recording.Accepted {
		t.Fatal("queued refused")
	}
	if r.ObserveConnection(stamp, event) != recording.DroppedCapacity {
		t.Fatal("capacity not bounded")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := r.beginClear(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("clear joined blocked writer: %v", err)
	}
	if b.Snapshot().ReservedRecords != 2 {
		t.Fatal("ownership released before I/O completion")
	}
	if r.ObserveConnection(stamp, event) != recording.DroppedClosed {
		t.Fatal("timed-out clear reopened recording")
	}
	close(s.release)
	release, err := r.beginClear(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if r.ObserveConnection(stamp, event) != recording.DroppedStale {
		t.Fatal("late pre-clear terminal can resurrect")
	}
	if r.ObserveConnection(r.ScopeStamp(captureidentity.Reference{}), event) != recording.Accepted {
		t.Fatal("new epoch refused")
	}
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.Snapshot().ReservedRecords != 0 {
		t.Fatal("reservation leaked")
	}
}

func TestRealCapturedForwardingWhileHistoryWriterBlocked(t *testing.T) {
	for _, family := range []string{"raw", "original_model"} {
		t.Run(family, func(t *testing.T) {
			sink := &blockedObservationStore{make(chan struct{}, 1), make(chan struct{})}
			defer close(sink.release)
			f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: family, choice: "builtin", recorder: func(ctx context.Context, b *recording.Budget, _ connectionevent.ScopedStore, _ egressaudit.ScopedAttemptStore) *observationRecorder {
				return newObservationRecorder(ctx, b, sink, sink)
			}})
			run := newOriginalForwardRun(t, f)
			first := run.start(t)
			select {
			case <-sink.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("history writer never entered")
			}
			assertOriginalForwardReply(t, f, first.join(t), false)
			assertOriginalForwardReply(t, f, run.start(t).join(t), false)
			if f.r.egressCompletion.owner.Err() != nil {
				t.Fatal("optional history stopped Runtime")
			}
		})
	}
}

func TestRealBlindAndCleartextWhileHistoryWriterBlocked(t *testing.T) {
	for _, port := range []uint16{443, 80} {
		t.Run(fmt.Sprint(port), func(t *testing.T) {
			sink := &blockedObservationStore{make(chan struct{}, 1), make(chan struct{})}
			defer close(sink.release)
			f := newB4Fixture(t, port, func(ctx context.Context, b *recording.Budget, _ connectionevent.ScopedStore, _ egressaudit.ScopedAttemptStore) *observationRecorder {
				return newObservationRecorder(ctx, b, sink, sink)
			})
			for range 2 {
				flight := f.job(func(context.Context) b4Result { return f.wire(port == 443) })
				select {
				case <-sink.entered:
				case <-time.After(20 * time.Millisecond):
				} // only the first worker can enter while blocked
				f.finish(flight, 200)
			}
			if f.r.egressCompletion.owner.Err() != nil {
				t.Fatal("history loss stopped Runtime")
			}
		})
	}
}
func TestRealUDPWhileHistoryWriterBlocked(t *testing.T) {
	sink := &blockedObservationStore{make(chan struct{}, 1), make(chan struct{})}
	defer close(sink.release)
	f := newUDPShutdownFixture(t, func(ctx context.Context, b *recording.Budget, _ connectionevent.ScopedStore, _ egressaudit.ScopedAttemptStore) *observationRecorder {
		return newObservationRecorder(ctx, b, sink, sink)
	})
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("recording sink not blocked")
	}
	// Fixture has already completed two real byte-exact datagram exchanges.
	for _, c := range f.controls {
		c.Close()
	}
	if err := f.runtime.udp.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.network.requireClosed(t)
	if f.runtime.recordingBudget.Snapshot().ReservedRecords == 0 {
		t.Fatal("blocked writer ownership lost")
	}
}

func TestLateRuntimeTerminalCannotReappearAfterArchiveClear(t *testing.T) {
	r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	defer shutdownRuntime(t, r)
	stamp := r.recorder.ScopeStamp(captureidentity.Reference{})
	a, err := egressaudit.New(egressaudit.NewInput{ObservationStamp: stamp, ID: "runtime-clear", Purpose: egressaudit.PurposeUpdate, PayloadClass: egressaudit.PayloadRuntime, Parent: egressaudit.ParentRef{Kind: egressaudit.ParentRuntimeAction, ID: "runtime-action"}, Caller: egressaudit.CallerCore, TargetOrigin: "https://example.test", Decision: egressaudit.BuiltInDirectDecision(egressaudit.AuthorityRuntime), StartedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if r.egressCompletion.ObserveEgress(stamp, a) != recording.Accepted {
		t.Fatal("start not accepted")
	}
	if err := r.recorder.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	release, err := r.EvidenceArchiveBarrier().BeginClear(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.EvidenceArchive().ClearEvidence(t.Context()); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	terminal, err := a.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, CompletedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if r.egressCompletion.ObserveEgress(terminal.ObservationStamp(), terminal) != recording.DroppedStale {
		t.Fatal("late runtime terminal accepted after clear")
	}
	page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("history resurrected: %+v %v", page, err)
	}
}

func TestRecorderOwnerCancellationClosesAdmission(t *testing.T) {
	owner, cancel := context.WithCancel(context.Background())
	b, _ := recording.NewBudget(recording.Limits{Records: 2, Bytes: 1 << 20})
	sink := &blockedObservationStore{make(chan struct{}, 1), make(chan struct{})}
	close(sink.release)
	r := newObservationRecorder(owner, b, sink, sink)
	stamp := r.ScopeStamp(captureidentity.Reference{})
	cancel()
	<-r.done
	event := connectionevent.Event{ConnectionID: "after-owner", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Now().UTC()}
	if got := r.ObserveConnection(stamp, event); got != recording.DroppedClosed {
		t.Fatalf("stopped worker accepted ownership: %v", got)
	}
	if b.Snapshot().ReservedRecords != 0 {
		t.Fatal("stopped worker leaked queue ownership")
	}
}

func TestCaptureDeletionJoinsLateUDPObservations(t *testing.T) {
	f := newUDPShutdownFixture(t)
	id, _ := manualcapture.ParseID(f.capture.ID)
	if _, err := f.runtime.ManualCaptures().Revoke(t.Context(), manualcapture.RevokeCommand{Owner: manualcapture.NewLocalOwnerScope(), ID: id, ExpectedCredentialRevision: 1}); err != nil {
		t.Fatal(err)
	}
	f.firstTerminal(t)
	f.network.requireClosed(t)
	// The control operation is terminal, but its final observation is still in
	// actual writer I/O. Physical erase must not acknowledge this as drained.
	short, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := f.runtime.EvidenceArchive().DeleteCapture(short, string(f.capture.Kind), f.capture.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deletion did not wait for late terminal: %v", err)
	}
	f.repository.unblock()
	if _, err := f.runtime.EvidenceArchive().DeleteCapture(t.Context(), string(f.capture.Kind), f.capture.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.recorder.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	attempts, err := f.runtime.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Purpose: egressaudit.PurposeDatagram})
	if err != nil || len(attempts.Items) != 0 {
		t.Fatalf("deleted UDP attempts reappeared: %+v %v", attempts, err)
	}
	events, err := f.runtime.connections.List(t.Context(), connectionevent.PageRequest{Limit: 20})
	if err != nil || len(events.Items) != 0 {
		t.Fatalf("deleted connection events reappeared: %+v %v", events, err)
	}
}

type copiedConnectionStore struct {
	entered chan connectionevent.Event
	release chan struct{}
}

func (s *copiedConnectionStore) PutConnection(_ context.Context, _ recording.Scope, e connectionevent.Event) error {
	s.entered <- e
	<-s.release
	return nil
}
func TestRecorderSharesRawBudgetAndOwnsStringCopies(t *testing.T) {
	b, _ := recording.NewBudget(recording.Limits{Records: 2, Bytes: 1 << 20})
	raw, ok := b.TryReserve(recording.Raw, 32)
	if !ok {
		t.Fatal("raw reservation refused")
	}
	defer raw.Release()
	sink := &copiedConnectionStore{make(chan connectionevent.Event, 1), make(chan struct{})}
	r := newObservationRecorder(context.Background(), b, sink, nil)
	host := strings.Repeat("x", 20) + ".example.test"
	e := connectionevent.Event{ConnectionID: "owned-copy", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: host, Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Now().UTC()}
	stamp := r.ScopeStamp(captureidentity.Reference{})
	if r.ObserveConnection(stamp, e) != recording.Accepted {
		t.Fatal("first copy refused")
	}
	copied := <-sink.entered
	if unsafe.StringData(copied.RequestedHost) == unsafe.StringData(host) {
		t.Fatal("worker borrowed producer string backing")
	}
	if r.ObserveConnection(stamp, e) != recording.DroppedCapacity {
		t.Fatal("general recorder ignored Raw ownership")
	}
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := r.Shutdown(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("shutdown manufactured an I/O join", err)
	}
	if b.Snapshot().ReservedRecords != 2 {
		t.Fatal("shutdown released live writer or Raw owner")
	}
	close(sink.release)
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.Snapshot().ReservedRecords != 1 {
		t.Fatal("worker release changed unrelated Raw owner")
	}
}
