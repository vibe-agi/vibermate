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
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
	"strings"
	"sync"
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

func TestLiveRecordingStampsRemainAllocationFreeAtCapacityAndAfterDrain(t *testing.T) {
	b, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 1 << 20})
	sink := &blockedObservationStore{make(chan struct{}, 1), make(chan struct{})}
	close(sink.release)
	r := newObservationRecorder(context.Background(), b, sink, sink)
	defer r.Shutdown(context.Background())
	occupied, _ := b.TryReserve(recording.Raw, 32)
	defer occupied.Release()
	reference := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "retained-live-stamps"}
	var stamps [1024]recording.Stamp
	index := 0
	allocs := testing.AllocsPerRun(1000, func() { stamps[index%len(stamps)] = r.ScopeStamp(reference); index++ })
	if allocs != 0 {
		occupied.Release()
		t.Fatalf("full budget still allocated %g per retained stamp", allocs)
	}
	event := connectionevent.Event{ConnectionID: "stamp-capacity", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Now().UTC()}
	allocs = testing.AllocsPerRun(1000, func() {
		if r.ObserveConnection(stamps[0], event) != recording.DroppedCapacity {
			panic("full budget admitted copy")
		}
	})
	if allocs != 0 {
		t.Fatalf("refused offer materialized %g unreserved producer copies", allocs)
	}
	occupied.Release()
	if r.ObserveConnection(stamps[0], event) != recording.Accepted {
		t.Fatal("freed budget refused snapshot")
	}
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.Snapshot().ReservedRecords != 0 {
		t.Fatal("settled snapshot kept reservation")
	}
	allocs = testing.AllocsPerRun(1000, func() { stamps[index%len(stamps)] = r.ScopeStamp(reference); index++ })
	if allocs != 0 {
		t.Fatalf("drained budget hides %g producer allocations", allocs)
	}
	for _, s := range stamps[:1000] {
		if !s.Current() {
			t.Fatal("live inline coordinate lost")
		}
	}
}

type scopeFenceArchive struct{ deletes int }

func (a *scopeFenceArchive) DeleteCapture(context.Context, string, string) (resourcedeletion.Released, error) {
	a.deletes++
	return resourcedeletion.Released{}, nil
}
func (a *scopeFenceArchive) ClearEvidence(context.Context) (resourcedeletion.Released, error) {
	return resourcedeletion.Released{}, nil
}

func TestTimedOutCaptureFenceRemainsOwnedUntilWriterSettles(t *testing.T) {
	for _, path := range []string{"managed_prepare", "manual_prepare", "delete"} {
		t.Run(path, func(t *testing.T) {
			sink := &blockedObservationStore{make(chan struct{}, 1), make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(sink.release) }) }
			r, err := startWithBuilders(t.Context(), testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}), runtimeBuildOverrides{recorder: func(ctx context.Context, b *recording.Budget, _ connectionevent.ScopedStore, _ egressaudit.ScopedAttemptStore) *observationRecorder {
				return newObservationRecorder(ctx, b, sink, sink)
			}})
			if err != nil {
				release()
				t.Fatal(err)
			}
			t.Cleanup(func() { release(); shutdownRuntime(t, r) })
			kind := rawevidence.ScopeManualCapture
			captureKind := captureidentity.KindManualCapture
			if path == "managed_prepare" {
				kind = rawevidence.ScopeManagedRun
				captureKind = captureidentity.KindManagedRun
			}
			const id = "held-admin-fence"
			barrier := captureEvidenceBarrier{raw: r.rawEvidence, recorder: r.recorder}
			store := &scopeFenceArchive{}
			archive := recordingArchive{raw: r.rawEvidence, recorder: r.recorder, delegate: store}
			perform := func(ctx context.Context, target string) error {
				if path == "delete" {
					_, err := archive.DeleteCapture(ctx, string(captureKind), target)
					return err
				}
				var terminal interface{ Abort() }
				var err error
				if path == "managed_prepare" {
					terminal, err = barrier.PrepareManagedRun(ctx, target)
				} else {
					terminal, err = barrier.PrepareManualCapture(ctx, target)
				}
				if err == nil {
					terminal.Abort()
				}
				return err
			}
			event := connectionevent.Event{ConnectionID: "held-admin-connection", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Now().UTC()}
			if r.recorder.ObserveConnection(r.recorder.ScopeStamp(captureidentity.Reference{Kind: captureKind, ID: id}), event) != recording.Accepted {
				t.Fatal("observation refused")
			}
			<-sink.entered
			short, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			err = perform(short, id)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("admin wait = %v", err)
			}
			lease, err := r.rawEvidence.BeginScope(t.Context(), kind, id)
			if lease != nil {
				lease.Release()
			}
			if !errors.Is(err, rawevidence.ErrScopeTerminal) {
				t.Fatalf("deadline reopened Raw fence while general I/O is live: %v", err)
			}
			retry, cancelRetry := context.WithTimeout(t.Context(), 20*time.Millisecond)
			err = perform(retry, "other-admin-fence")
			cancelRetry()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("second admin owner bypassed occupied slot: %v", err)
			}
			other, err := r.rawEvidence.BeginScope(t.Context(), kind, "other-admin-fence")
			if err != nil {
				t.Fatalf("slot retry installed another Raw fence: %v", err)
			}
			other.Release()
			if store.deletes != 0 {
				t.Fatal("timed-out deletion acknowledged physical erase")
			}
			release()
			joined, cancelJoined := context.WithTimeout(t.Context(), time.Second)
			defer cancelJoined()
			if err := perform(joined, id); err != nil {
				t.Fatalf("settled worker failed to release admin slot: %v", err)
			}
			lease, err = r.rawEvidence.BeginScope(joined, kind, id)
			if path == "delete" {
				if !errors.Is(err, rawevidence.ErrScopeTerminal) || store.deletes != 1 {
					t.Fatal("successful erase did not seal scope")
				}
			} else {
				if err != nil {
					t.Fatalf("settled aborted prepare did not reopen scope: %v", err)
				}
				lease.Release()
			}
		})
	}
}

type stagedFenceStore struct {
	entered chan int
	release [2]chan struct{}
	writes  int
}

func (s *stagedFenceStore) PutConnection(context.Context, recording.Scope, connectionevent.Event) error {
	index := s.writes
	s.writes++
	s.entered <- index
	<-s.release[index]
	return nil
}

func TestCaptureFenceSurvivesPartialSettlementAndShutdown(t *testing.T) {
	sink := &stagedFenceStore{entered: make(chan int, 2), release: [2]chan struct{}{make(chan struct{}), make(chan struct{})}}
	var releases [2]sync.Once
	release := func(i int) { releases[i].Do(func() { close(sink.release[i]) }) }
	r, err := startWithBuilders(t.Context(), testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}), runtimeBuildOverrides{recorder: func(ctx context.Context, b *recording.Budget, _ connectionevent.ScopedStore, e egressaudit.ScopedAttemptStore) *observationRecorder {
		return newObservationRecorder(ctx, b, sink, e)
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { release(0); release(1); shutdownRuntime(t, r) })
	const id = "two-write-fence"
	stamp := r.recorder.ScopeStamp(captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: id})
	event := connectionevent.Event{ConnectionID: "two-write-connection", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Now().UTC()}
	for range 2 {
		if r.recorder.ObserveConnection(stamp, event) != recording.Accepted {
			t.Fatal("queued target refused")
		}
	}
	<-sink.entered
	barrier := captureEvidenceBarrier{raw: r.rawEvidence, recorder: r.recorder}
	short, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	_, err = barrier.PrepareManualCapture(short, id)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	release(0)
	<-sink.entered
	assertHeld := func() {
		lease, err := r.rawEvidence.BeginScope(t.Context(), rawevidence.ScopeManualCapture, id)
		if lease != nil {
			lease.Release()
		}
		if !errors.Is(err, rawevidence.ErrScopeTerminal) {
			t.Fatal("partial settlement released the fence")
		}
	}
	assertHeld()
	stop, cancelStop := context.WithTimeout(t.Context(), 20*time.Millisecond)
	err = r.recorder.Shutdown(stop)
	cancelStop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("blocked worker shutdown pretended to join", err)
	}
	assertHeld()
	release(1)
	if err := r.recorder.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	lease, err := r.rawEvidence.BeginScope(t.Context(), rawevidence.ScopeManualCapture, id)
	if err != nil {
		t.Fatal("completed worker retained fence", err)
	}
	lease.Release()
}
