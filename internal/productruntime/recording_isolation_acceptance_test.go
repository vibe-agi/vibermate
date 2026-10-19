package productruntime

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

// Prime the actual shared worker before any measured Agent operation. The
// constructor is test-only and delegates the production bounded queue.
func primeBlockedRecordingQueue(t *testing.T, ctx context.Context, b *recording.Budget, sink *blockedObservationStore) *observationRecorder {
	t.Helper()
	q := newObservationRecorder(ctx, b, sink, sink)
	event := connectionevent.Event{ConnectionID: "fault-before-action", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Now().UTC()}
	if q.ObserveConnection(q.ScopeStamp(captureidentity.Reference{}), event) != recording.Accepted {
		t.Fatal("prime refused")
	}
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("optional writer fault not active")
	}
	return q
}

func unavailableAccountReadFixture(t *testing.T, driver providerauth.DriverRef, faults ...string) accountReadFixture {
	t.Helper()
	entered := make(chan struct{})
	var once sync.Once
	f := newAccountReadFixtureWithBuilders(t, driver, runtimeBuildOverrides{openObservations: func(ctx context.Context, options runtimepersistence.ObservationOptions) (*runtimepersistence.ObservationStore, error) {
		if len(faults) > 0 && faults[0] == "corrupt" {
			original := []byte("retained corrupt optional history")
			if err := os.WriteFile(options.DatabasePath, original, 0600); err != nil {
				return nil, err
			}
			owner, err := runtimepersistence.OpenObservations(ctx, options)
			if err == nil {
				owner.Shutdown(context.Background())
				return nil, errors.New("corrupt optional history accepted")
			}
			once.Do(func() { close(entered) })
			return nil, err
		}
		once.Do(func() { close(entered) })
		return nil, errors.New("optional observation open unavailable")
	}})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("optional open failure not active")
	}
	exerciseAcceptanceCaptureControls(t, f.runtime)
	return f
}

// BEGIN IMMEDIATE owns the actual observations.db writer lock before traffic.
// A second real SQLite writer confirms the fault; no history facade is replaced.
func holdRuntimeObservationWriter(t *testing.T, r *Runtime) func() {
	t.Helper()
	WaitRecordingTestRuntime(t, r)
	path := filepath.Join(r.paths.DataDirectory(), "observations.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(t.Context(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	verifier, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = verifier.ExecContext(t.Context(), `BEGIN IMMEDIATE`); err == nil {
		t.Fatal("actual writer lock is not active")
	}
	verifier.Close()
	var once sync.Once
	release := func() {
		once.Do(func() {
			_, err := conn.ExecContext(context.Background(), `ROLLBACK`)
			if err != nil {
				t.Error(err)
			}
			conn.Close()
			db.Close()
		})
	}
	t.Cleanup(release)
	return release
}

func TestRuntimeNativeAuthorityAndReplayWhileRecordingAlreadyBlocked(t *testing.T) {
	for _, media := range []string{"json", "sse"} {
		t.Run(media, func(t *testing.T) {
			sink := &blockedObservationStore{entered: make(chan struct{}, 1), release: make(chan struct{})}
			defer close(sink.release)
			f := newRuntimeNativeEgressFixture(t, runtimeNativeEgressConfig{choice: "builtin", fullRecording: true, recorder: func(ctx context.Context, b *recording.Budget, _ connectionevent.ScopedStore, _ egressaudit.ScopedAttemptStore) *observationRecorder {
				return primeBlockedRecordingQueue(t, ctx, b, sink)
			}})
			if media == "sse" {
				f.mode = "matrix"
			}
			f.handshake(t)
			body := `{"jsonrpc":"2.0","id":45,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`
			if media == "sse" {
				body = `{"jsonrpc":"2.0","id":44,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"native-token"}}}`
			}
			tool := f.begin(t, "POST", body, "")
			f.allow(t, tool.ID)
			got := f.join(t, tool)
			if media == "json" && got.Outcome != nativemcp.OutcomeCompleted {
				t.Fatal(got)
			}
			if media == "sse" && (got.Outcome != nativemcp.OutcomeFailed || got.Reason != nativemcp.ReasonStreamDetached) {
				t.Fatal(got)
			}
			want := `{"jsonrpc":"2.0","id":45,"result":{"content":[{"type":"text","text":"synthetic result"}]}}`
			if media == "sse" {
				want = "id: native-progress\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"native-token\",\"progress\":1}}\n\n"
			}
			if tool.Response.Body.String() != want {
				t.Fatalf("native JSON bytes changed: %q", tool.Response.Body.String())
			}
			before, err := f.r.NativeToolRepository().Get(t.Context(), tool.ID)
			if err != nil {
				t.Fatal(err)
			}
			counts := f.counts()
			replay := f.r.nativeTools.Serve(t.Context(), tool.Captured, httptest.NewRecorder())
			if replay.Outcome != nativemcp.OutcomeRejected || replay.Reason != nativemcp.ReasonProtocolInvalid {
				t.Fatal("duplicate authorized send replayed", replay)
			}
			scope, err := f.r.NativeToolRepository().GetScope(t.Context(), f.capture)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.r.NativeToolRepository().Start(t.Context(), tool.ID, "fresh-after-completed", scope.Revision, time.Now().UTC()); !errors.Is(err, nativetool.ErrReplay) {
				t.Fatal("fresh Start bypassed once", err)
			}
			after, err := f.r.NativeToolRepository().Get(t.Context(), tool.ID)
			if err != nil || !reflect.DeepEqual(before, after) || f.counts() != counts {
				t.Fatal("duplicate changed required state or sent bytes", err)
			}
			var once, consumed int
			if err = f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, tool.ID.String()).Scan(&once); err != nil {
				t.Fatal(err)
			}
			if err = f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_approvals WHERE operation_id=? AND consumed_at_unix_ms IS NOT NULL`, tool.ID.String()).Scan(&consumed); err != nil || once != 1 || consumed != 1 {
				t.Fatalf("required once=%d consumed=%d error=%v", once, consumed, err)
			}
			if got := f.r.recordingBudget.Snapshot(); got.ReservedRecords == 0 {
				t.Fatal("optional owner disappeared before real execution joined")
			}
		})
	}
}

func TestRuntimeNativeHTTPContinuityWithActualWriterLockAndFilledBudget(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) { runNativeStateHTTP(t, stream, "locked", false, false) })
	}
}

func exerciseAcceptanceCaptureControls(t *testing.T, r *Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	dir := t.TempDir()
	grant, err := r.CaptureRuns().Create(ctx, capturerun.CreateCommand{CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "synthetic-agent"), ExecutableLabel: "synthetic-agent", Lifetime: time.Minute, CatalogRevision: 1})
	if err != nil {
		t.Fatal("Create while optional work held", err)
	}
	if _, err = r.CaptureRuns().Attach(ctx, grant.Run.ID, grant.ControlCapability, os.Getpid()); err != nil {
		t.Fatal("Attach while optional work held", err)
	}
	if _, err = r.CaptureRuns().Heartbeat(ctx, grant.Run.ID, grant.ControlCapability, time.Minute); err != nil {
		t.Fatal("Heartbeat while optional work held", err)
	}
	if _, err = r.captureRuns.AuthorizeProxy(ctx, grant.ProxyCapability); err != nil {
		t.Fatal("AuthorizeProxy while optional work held", err)
	}
	if err = r.CaptureRuns().Finish(ctx, grant.Run.ID, grant.ControlCapability); err != nil {
		t.Fatal("Finish while optional work held", err)
	}
}

func TestRuntimeDroppedStartStillPublishesActualTerminal(t *testing.T) {
	f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "raw", choice: "builtin", edge: "send_edit"})
	stats := f.r.recordingBudget.Snapshot()
	reservation, ok := f.r.recordingBudget.TryReserve(recording.Raw, (64<<20)-stats.ReservedBytes)
	if !ok {
		t.Fatal("cannot fill shared budget")
	}
	defer reservation.Release()
	run := newOriginalForwardRun(t, f)
	first := run.start(t)
	first.waitEntry(t, f.entered)
	if f.r.recordingBudget.Snapshot().ByKind[recording.Egress].Offers[recording.DroppedCapacity].Records == 0 {
		t.Fatal("actual send start was not dropped")
	}
	reservation.Release()
	f.unblock()
	assertOriginalForwardReply(t, f, first.join(t), false)
	// No Flush occurred at the held send boundary. The real terminal is complete
	// and independently meaningful even though its actual start was dropped.
	page, err := SettledEgressTestPage(t, f.r, egressaudit.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || !page.Items[0].Attempt.Terminal() {
		t.Fatalf("terminal lost without start: %+v %v", page, err)
	}
	a := page.Items[0].Attempt
	f.mu.Lock()
	decision, known := f.starts[a.ID()]
	f.mu.Unlock()
	if !known || a.Decision() != decision || a.Outcome() != egressaudit.OutcomeCompleted || a.TargetOrigin() != "https://chatgpt.com" || a.Parent().ID == "" {
		t.Fatal("dropped start changed immutable terminal lineage", a)
	}
}

type heldRuntimeHeaderReader struct{ entered, release chan struct{} }

func (h *heldRuntimeHeaderReader) ReadOverwriteHeader(context.Context, providerauth.HeaderLookup) (string, error) {
	close(h.entered)
	<-h.release
	return "synthetic-header-copy", nil
}

func TestRuntimeActualInspectionReadAndMaintenanceDoNotHoldForwarding(t *testing.T) {
	f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "raw", choice: "builtin"})
	r := f.r
	observation := rawevidence.Observation{Context: rawevidence.Context{ObservationStamp: r.recorder.ScopeStamp(captureidentity.Reference{}), ScopeKind: rawevidence.ScopeRuntime, ExchangeID: "held-inspection", AccountID: "synthetic-account", AccountRevision: 1, CredentialEpoch: 1, UpstreamEndpointID: "synthetic-endpoint", UpstreamEndpointRevision: 1, Recording: rawevidence.RecordingFull, RetentionDays: 1}, Layer: rawevidence.LayerProviderEgress, Method: "GET", Scheme: "https", Authority: "example.test", Path: "/", Headers: http.Header{"User-Agent": []string{"synthetic-header-copy"}}, ProtectedHeaderNames: []string{"User-Agent"}, Complete: true, Representation: "http_message"}
	if _, err := r.rawEvidence.Observe(t.Context(), observation); err != nil {
		t.Fatal(err)
	}
	if err := r.rawEvidence.Flush(t.Context(), rawevidence.Watermark{}); err != nil {
		t.Fatal(err)
	}
	rows, err := r.RawEvidence().ListExchange(t.Context(), "held-inspection")
	if err != nil || len(rows) != 1 {
		t.Fatalf("real Raw inspection fixture=%d %v", len(rows), err)
	}
	source := &heldRuntimeHeaderReader{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(source.release) }) }
	defer release()
	done := make(chan error, 1)
	go func() {
		value, err := r.RawEvidence().(rawevidence.HeaderRevealer).RevealHeader(t.Context(), rawevidence.HeaderRevealRequest{RevealRequest: rawevidence.RevealRequest{EnvelopeID: rows[0].EnvelopeID, ActorID: "synthetic-owner"}, Name: "User-Agent"}, source)
		if value.Value != "" {
			err = errors.New("stale held read disclosed a credential")
		}
		done <- err
	}()
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("actual inspection source callback never entered")
	}
	// The callback is reached only after the real SQL/header read. Hold it while
	// forwarding and control run, then prove maintenance still owns that lease.
	exerciseAcceptanceCaptureControls(t, r)
	run := newOriginalForwardRun(t, f)
	assertOriginalForwardReply(t, f, run.start(t).join(t), false)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	_, err = r.recordingStorage.BeginMaintenance(ctx, r.recordingStorage.catalog.Version().Generation+1)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("maintenance bypassed actual held inspection lease: %v", err)
	}
	release()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("stale inspection completed without refusal")
		}
	case <-time.After(time.Second):
		t.Fatal("actual inspection lease did not join")
	}
	lease, err := r.recordingStorage.BeginMaintenance(t.Context(), r.recordingStorage.catalog.Version().Generation+1)
	if err != nil {
		t.Fatal(err)
	}
	exerciseAcceptanceCaptureControls(t, r)
	assertOriginalForwardReply(t, f, run.start(t).join(t), false)
	lease.Complete(true)
}

func TestRuntimeRawSessionMarkerFailureIsRecordingHealthAfterActualShutdown(t *testing.T) {
	r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	waitRecording(t, r)
	writerID := r.recordingStorage.current.Load().raw.Statistics().WriterID
	path := r.recordingStorage.options.DatabasePath
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TRIGGER task7_refuse_marker BEFORE UPDATE OF state ON runtime_raw_evidence_writer_sessions WHEN NEW.state='closed' BEGIN SELECT RAISE(ABORT,'task7 marker finalization refused'); END`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	err = r.Shutdown(t.Context())
	if err != nil {
		t.Errorf("optional marker failure escaped ownership boundary: %v", err)
	}
	select {
	case <-r.ShutdownDone():
	default:
		t.Fatal("actual owner still active")
	}
	status := r.Status()
	if status.State != RuntimeStateStopped || status.Recording.State != RecordingDegraded || status.RecordingFailure == nil || !status.RecordingFailure.Valid() || status.Recording.CleanupPending {
		t.Errorf("marker loss misreported ownership/health: %+v", status)
	}
	data, err := runtimedata.Acquire(r.paths.DataDirectory())
	if err != nil {
		t.Fatal("runtime data guard retained", err)
	}
	defer data.Release()
	host, err := instanceguard.Acquire(filepath.Join(r.paths.DataDirectory(), "server.lock"))
	if err != nil {
		t.Fatal("host guard retained", err)
	}
	defer host.Release()
	inspection, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	var state string
	if err = inspection.QueryRow(`SELECT state FROM runtime_raw_evidence_writer_sessions WHERE writer_id=?`, writerID).Scan(&state); err != nil || state != "open" {
		t.Fatalf("failed marker fabricated clean recording: %q %v", state, err)
	}
}

func TestRuntimePrivacyInitializationFailureIsRecordingHealthAfterOwnedClose(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	r, err := startWithBuilders(t.Context(), testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}), runtimeBuildOverrides{recoverEgress: func(context.Context, egressaudit.Repository, time.Time) error {
		once.Do(func() { close(entered) })
		return errors.Join(runtimepersistence.ErrRecordingPrivacyUnavailable, errors.New("synthetic initialization privacy refusal"))
	}})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if err = r.Shutdown(t.Context()); err != nil {
		t.Errorf("optional privacy failure became ownership failure: %v", err)
	}
	status := r.Status()
	if status.State != RuntimeStateStopped || status.Recording.State != RecordingDegraded || status.RecordingFailure == nil || !status.RecordingFailure.Valid() || status.Recording.CleanupPending {
		t.Fatalf("privacy loss did not remain separate and visible: %+v", status)
	}
}

type splitRawShutdownFixture struct {
	rawEvidenceRuntime
	finalization, ownership error
}

func (f splitRawShutdownFixture) ShutdownOwned(context.Context) (rawevidence.ShutdownResult, error) {
	return rawevidence.ShutdownResult{FinalizationError: f.finalization}, f.ownership
}

type unsplitRawShutdownFixture struct {
	rawEvidenceRuntime
	err error
}

func (f unsplitRawShutdownFixture) Shutdown(context.Context) error { return f.err }
func TestOptionalFinalizationCannotMaskMixedOwnershipFailure(t *testing.T) {
	marker := errors.New("marker metadata failed")
	actual := errors.New("actual owned close failed")
	owner := &recordingStorage{tracker: newStatusTracker("synthetic", hostcontract.KindDesktop, time.Now().UTC())}
	mixed := errors.Join(runtimepersistence.ErrInspectionUnavailable, actual)
	owner.closeRaw(splitRawShutdownFixture{finalization: marker, ownership: mixed})
	if !errors.Is(owner.recordingErr, marker) || !errors.Is(owner.closeErr, actual) || !errors.Is(owner.closeErr, runtimepersistence.ErrInspectionUnavailable) || errors.Is(owner.closeErr, marker) {
		t.Fatalf("mixed error provenance lost: recording=%v ownership=%v", owner.recordingErr, owner.closeErr)
	}
	owner.closeRaw(unsplitRawShutdownFixture{err: runtimepersistence.ErrInspectionUnavailable})
	if !errors.Is(owner.closeErr, runtimepersistence.ErrInspectionUnavailable) {
		t.Fatal("unknown shutdown error was filtered")
	}
}

func TestRawBuilderDoesNotInventOwnerAfterActualRepositoryCancellation(t *testing.T) {
	r, err := Start(t.Context(), testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	raw, err := buildRawEvidence(rawEvidenceBuildRequest{ctx: ctx, budget: r.recorder.budget, repository: r.recordingStorage.current.Load().store.RawEvidenceRepository(), random: rand.Reader, clock: r.clock, config: rawevidence.DefaultConfig()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("actual repository cancellation cause lost: %v", err)
	}
	if raw != nil {
		t.Fatalf("failed Raw construction invented owner: %T", raw)
	}
}
func TestRuntimeActualRawInitializationRefusalHasNoPhantomOwnership(t *testing.T) {
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	r, err := startWithBuilders(t.Context(), options, runtimeBuildOverrides{openObservations: func(ctx context.Context, options runtimepersistence.ObservationOptions) (*runtimepersistence.ObservationStore, error) {
		store, err := runtimepersistence.OpenObservations(ctx, options)
		if err != nil {
			return nil, err
		}
		db, err := sql.Open("sqlite", options.DatabasePath)
		if err != nil {
			store.Shutdown(context.Background())
			return nil, err
		}
		_, err = db.Exec(`CREATE TRIGGER reject_actual_raw_start BEFORE INSERT ON runtime_raw_evidence_writer_sessions BEGIN SELECT RAISE(ABORT,'actual Raw initialization refusal'); END`)
		closeErr := db.Close()
		if err != nil || closeErr != nil {
			store.Shutdown(context.Background())
			return nil, errors.Join(err, closeErr)
		}
		return store, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { r.Shutdown(context.Background()) }()
	deadline := time.Now().Add(5 * time.Second)
	for r.Status().Recording.State != RecordingDegraded {
		if time.Now().After(deadline) {
			t.Fatal("actual Raw initialization refusal not observed")
		}
		time.Sleep(time.Millisecond)
	}
	if err = r.Shutdown(t.Context()); err != nil {
		t.Errorf("failed constructor invented ownership: %v", err)
	}
	status := r.Status()
	if status.State != RuntimeStateStopped || status.Recording.State != RecordingDegraded || status.RecordingFailure == nil || !status.RecordingFailure.Valid() || status.Recording.CleanupPending {
		t.Fatalf("Raw initialization health/ownership: %+v", status)
	}
	for _, name := range []string{"runtime-data.lock", "server.lock"} {
		guard, err := instanceguard.Acquire(filepath.Join(options.Paths.DataDirectory(), name))
		if err != nil {
			t.Fatalf("real guard not joined: %s %v", name, err)
		}
		guard.Release()
	}
}

func TestRuntimeRepeatedActualRawInitializationFailureRetainsFirstCause(t *testing.T) {
	ready := make(chan *Runtime, 1)
	type causes struct{ first, later error }
	observed := make(chan causes, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	attempts := 0
	var first error
	r, err := startWithBuilders(t.Context(), testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}), runtimeBuildOverrides{openObservations: func(ctx context.Context, options runtimepersistence.ObservationOptions) (*runtimepersistence.ObservationStore, error) {
		attempts++
		if attempts == 2 {
			owner := <-ready
			first = owner.recordingStorage.recordingErr
			ready <- owner
		}
		if attempts == 3 {
			owner := <-ready
			observed <- causes{first, owner.recordingStorage.recordingErr}
			<-release
			return nil, context.Canceled
		}
		if _, err := os.Stat(options.DatabasePath); err == nil {
			db, err := sql.Open("sqlite", options.DatabasePath)
			if err != nil {
				return nil, err
			}
			_, err = db.Exec(`DROP TRIGGER IF EXISTS reject_repeat_raw_start`)
			closeErr := db.Close()
			if err != nil || closeErr != nil {
				return nil, errors.Join(err, closeErr)
			}
		}
		store, err := runtimepersistence.OpenObservations(ctx, options)
		if err != nil {
			return nil, err
		}
		db, err := sql.Open("sqlite", options.DatabasePath)
		if err != nil {
			store.Shutdown(context.Background())
			return nil, err
		}
		_, err = db.Exec(`CREATE TRIGGER reject_repeat_raw_start BEFORE INSERT ON runtime_raw_evidence_writer_sessions BEGIN SELECT RAISE(ABORT,'repeated actual Raw initialization refusal'); END`)
		closeErr := db.Close()
		if err != nil || closeErr != nil {
			store.Shutdown(context.Background())
			return nil, errors.Join(err, closeErr)
		}
		return store, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ready <- r
	defer func() { unblock(); shutdownRuntime(t, r) }()
	select {
	case got := <-observed:
		if got.first == nil || got.later != got.first {
			t.Errorf("repeated Raw initialization accumulated health cause: first=%v later=%v", got.first, got.later)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("two actual Raw SQL refusals not observed")
	}
	unblock()
	if err = r.Shutdown(t.Context()); err != nil {
		t.Fatal("Raw retry failure became ownership failure", err)
	}
	if status := r.Status(); status.State != RuntimeStateStopped || status.RecordingFailure == nil || status.Recording.CleanupPending {
		t.Fatalf("retry failure health/ownership: %+v", status)
	}
}

// Long batching keeps actual Raw work accepted but uncommitted until Flush.
// Filling the shared budget then denies the worker's scratch growth, without
// fabricating a settlement or a storage error.
func TestProductRuntimeStatusReportsAcceptedDiscardedObservationLoss(t *testing.T) {
	r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	repo := &countingStatusRawRepository{Repository: r.recordingStorage.current.Load().store.RawEvidenceRepository()}
	config := rawevidence.DefaultConfig()
	config.FlushInterval = time.Hour
	raw, err := rawevidence.Open(t.Context(), rawevidence.Options{Repository: repo, Budget: r.recordingBudget, Random: rand.Reader, Clock: r.clock, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Shutdown(context.Background())
	stamp := r.recorder.ScopeStamp(captureidentity.Reference{})
	observation := rawevidence.Observation{Context: rawevidence.Context{ObservationStamp: stamp, ScopeKind: rawevidence.ScopeRuntime, ExchangeID: "scratch-discard", AccountID: "synthetic-account", AccountRevision: 1, CredentialEpoch: 1, UpstreamEndpointID: "synthetic-endpoint", UpstreamEndpointRevision: 1, Recording: rawevidence.RecordingFull, RetentionDays: 1}, Layer: rawevidence.LayerProviderEgress, Method: "GET", Scheme: "https", Authority: "example.test", Path: "/", Complete: true, Representation: "http_message"}
	if got := raw.TryObserve(stamp, observation); got != recording.Accepted {
		t.Fatal("Raw not accepted", got)
	}
	stats := r.recordingBudget.Snapshot()
	occupied, ok := r.recordingBudget.TryReserve(recording.Content, (64<<20)-stats.ReservedBytes)
	if !ok {
		t.Fatal("cannot occupy remaining shared bytes")
	}
	defer occupied.Release()
	if err := raw.Flush(t.Context(), rawevidence.Watermark{}); err != nil {
		t.Fatal(err)
	}
	if repo.appends.Load() != 0 || !stamp.Current() {
		t.Fatal("scratch refusal reached writer or invalidated original scope")
	}
	stats = r.recordingBudget.Snapshot()
	if stats.ByKind[recording.Raw].Settlements[recording.Discarded].Records != 1 {
		t.Fatalf("Raw worker did not settle known loss: %+v", stats)
	}
	occupied.Release()
	status := r.Status()
	if status.Recording.Dropped != 1 || status.Recording.Unconfirmed != 0 || status.Recording.State != RecordingDegraded || status.RecordingFailure != nil {
		t.Fatalf("known worker loss hidden or fabricated write error: %+v", status)
	}
	if status.Recording.ReservedRecords != 0 || status.Recording.ReservedBytes != 0 {
		t.Fatal("settled Raw retained capacity")
	}
	// Required control remains available after the optional loss.
	dir := t.TempDir()
	if _, err := r.CaptureRuns().Create(t.Context(), capturerun.CreateCommand{CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "agent"), ExecutableLabel: "agent", CatalogRevision: 1, Lifetime: time.Minute}); err != nil {
		t.Fatal(err)
	}
}

type countingStatusRawRepository struct {
	rawevidence.Repository
	appends atomic.Int32
}

func (r *countingStatusRawRepository) AppendBatch(ctx context.Context, rows []rawevidence.StoredEnvelope, at time.Time) error {
	r.appends.Add(1)
	return r.Repository.AppendBatch(ctx, rows, at)
}
