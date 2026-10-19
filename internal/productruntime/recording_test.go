package productruntime

import (
	"context"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
	"path/filepath"
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
				return primeBlockedRecordingQueue(t, ctx, b, sink)
			}})
			run := newOriginalForwardRun(t, f)
			first := run.start(t)
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
				return primeBlockedRecordingQueue(t, ctx, b, sink)
			})
			for range 2 {
				flight := f.job(func(context.Context) b4Result { return f.wire(port == 443) })
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
		return primeBlockedRecordingQueue(t, ctx, b, sink)
	})
	// The queue was primed before either actual byte-exact datagram exchange.
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
	waitRecording(t, r)
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
	waitRecordingMirror(t, r)
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

func TestCaptureDeletionFencesLateUDPWithoutWaitingForHistory(t *testing.T) {
	f := newUDPShutdownFixture(t)
	id, _ := manualcapture.ParseID(f.capture.ID)
	if _, err := f.runtime.ManualCaptures().Revoke(t.Context(), manualcapture.RevokeCommand{Owner: manualcapture.NewLocalOwnerScope(), ID: id, ExpectedCredentialRevision: 1}); err != nil {
		t.Fatal(err)
	}
	f.firstTerminal(t)
	f.network.requireClosed(t)
	// Required deletion is acknowledged while the actual accepted optional
	// terminal remains owned. It reports pending physical work explicitly.
	short, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	released, err := f.runtime.EvidenceArchive().DeleteCapture(short, string(f.capture.Kind), f.capture.ID)
	if err != nil || !released.CleanupPending || !released.CountsIncomplete {
		t.Fatalf("required deletion waited for history or claimed erase: %+v %v", released, err)
	}
	if f.runtime.recordingBudget.Snapshot().ReservedRecords == 0 {
		t.Fatal("deletion released still-owned terminal")
	}
	state, err := f.runtime.storage.RecordingCleanupState(t.Context())
	if err != nil || state.RequestedRevision <= state.CompletedRevision {
		t.Fatalf("held writer falsely acknowledged physical cleanup: %+v %v", state, err)
	}
	f.repository.unblock()
	if err := f.runtime.recorder.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitRecordingMirror(t, f.runtime)
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

func TestTimedOutCapturePreparationRemainsOwnedUntilWriterSettles(t *testing.T) {
	for _, path := range []string{"managed_prepare", "manual_prepare"} {
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
			waitRecording(t, r)
			id, stamp := recordingAuthorityScope(t, r, captureKind)
			perform := func(ctx context.Context, target string) error {
				// This is the administrative settlement primitive. Normal
				// Capture Finish has its separate non-durability test.
				terminal, err := r.recorder.prepareScope(ctx, r.rawEvidence, kind, target, nil)
				if err == nil {
					terminal.Abort()
				}
				return err
			}
			event := connectionevent.Event{ConnectionID: "held-admin-connection", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Now().UTC()}
			if r.recorder.ObserveConnection(stamp, event) != recording.Accepted {
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
			release()
			joined, cancelJoined := context.WithTimeout(t.Context(), time.Second)
			defer cancelJoined()
			if err := perform(joined, id); err != nil {
				t.Fatalf("settled worker failed to release admin slot: %v", err)
			}
			lease, err = r.rawEvidence.BeginScope(joined, kind, id)
			if err != nil {
				t.Fatalf("settled aborted prepare did not reopen scope: %v", err)
			}
			lease.Release()
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
	waitRecording(t, r)
	id, stamp := recordingAuthorityScope(t, r, captureidentity.KindManualCapture)
	event := connectionevent.Event{ConnectionID: "two-write-connection", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Now().UTC()}
	for range 2 {
		if r.recorder.ObserveConnection(stamp, event) != recording.Accepted {
			t.Fatal("queued target refused")
		}
	}
	<-sink.entered
	short, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	_, err = r.recorder.prepareScope(short, r.rawEvidence, rawevidence.ScopeManualCapture, id, nil)
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

func recordingAuthorityScope(t *testing.T, r *Runtime, kind captureidentity.Kind) (string, recording.Stamp) {
	t.Helper()
	if kind == captureidentity.KindManualCapture {
		grant, err := r.manualCaptures.Create(t.Context(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Administrative scope", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
		if err != nil {
			t.Fatal(err)
		}
		admitted, err := r.manualCaptures.AuthorizeProxy(t.Context(), grant.Credential)
		if err != nil {
			t.Fatal(err)
		}
		return grant.Capture.ID, r.recorder.StampScope(admitted.RecordingScope)
	}
	dir := t.TempDir()
	grant, err := r.captureRuns.Create(t.Context(), capturerun.CreateCommand{CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "agent"), ExecutableLabel: "agent", CatalogRevision: 1, Lifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := r.captureRuns.AuthorizeProxy(t.Context(), grant.ProxyCapability)
	if err != nil {
		t.Fatal(err)
	}
	return grant.Run.ID, r.recorder.StampScope(admitted.RecordingScope)
}
