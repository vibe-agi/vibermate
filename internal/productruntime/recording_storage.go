package productruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

// One fixed lifecycle owner retains every optional handle until real I/O has
// returned. Worker queues and synchronous disclosure/repair share this owner.
type recordingStorage struct {
	nextExpiry      time.Time
	layout          runtimedata.StorageLayout
	sources         []runtimepersistence.InspectionSource
	approvalReady   chan struct{}
	legacyAuthority toolapproval.LegacyApprovalRemover
	startupClaim    *recordingCleanupClaim
	startupToken    *runtimepersistence.InspectionMaintenance
	startupErr      error

	clearAuthority      toolapproval.ArchiveClearAuthority
	cleanupPending      atomic.Bool
	cleanupWake         chan struct{}
	provisioning        string
	dataGuard           *instanceguard.Guard
	publish             func(context.Context, string, string, *instanceguard.Guard) error
	closeErr            error
	recordingErr        error
	unsafeMaintenance   atomic.Bool
	paused              atomic.Bool
	maintenanceRequests chan recordingMaintenanceRequest
	visibility          recording.InspectionVisibility
	control             *runtimepersistence.ControlStore
	publishedMirror     recording.InspectionStamp
	mu                  sync.Mutex
	current             atomic.Pointer[recordingSinks]
	active              int
	closing             bool
	changed             chan struct{}
	ctx                 context.Context
	cancel              context.CancelFunc
	done                chan struct{}
	recorder            *observationRecorder
	catalog             *runtimepersistence.InspectionCatalog
	tracker             *statusTracker
	options             runtimepersistence.ObservationOptions
	root                string
	random              io.Reader
	clock               Clock
	open                func(context.Context, runtimepersistence.ObservationOptions) (*runtimepersistence.ObservationStore, error)
	recover             func(context.Context, egressaudit.Repository, time.Time) error
}
type recordingSinks struct {
	store    *runtimepersistence.ObservationStore
	activity activityRuntime
	content  exchangeContentRuntime
	raw      rawEvidenceRuntime
}
type recordingProvision struct {
	Schema      string `json:"schema"`
	LayoutID    string `json:"layoutId"`
	SourceID    string `json:"sourceId"`
	Provisioned bool   `json:"provisioned"`
}

// The current source identity is a bounded deterministic function of the
// already durable layout identity; the provision receipt records that choice.
func recordingSourceID(layout runtimedata.StorageLayout) string {
	digest := sha256.Sum256([]byte(layout.ID))
	return "current-" + hex.EncodeToString(digest[:16])
}
func newRecordingStorage(owner context.Context, root string, layout runtimedata.StorageLayout, dataGuard *instanceguard.Guard, control *runtimepersistence.ControlStore, q *observationRecorder, tracker *statusTracker, limits *exchangecontent.SourceLimits, random io.Reader, clock Clock, overrides runtimeBuildOverrides) (*recordingStorage, error) {
	sources := make([]runtimepersistence.InspectionSource, 0, len(layout.Archives)+1)
	for _, a := range layout.Archives {
		sources = append(sources, runtimepersistence.InspectionSource{ID: a.ID, RelativePath: a.RelativeDirectory + "/runtime.db", Database: a.Database})
	}
	sources = append(sources, runtimepersistence.InspectionSource{ID: recordingSourceID(layout), RelativePath: layout.Observation.RelativePath, Database: runtimepersistence.SchemaState{Identity: layout.Observation.Identity, Revision: layout.Observation.Revision}, Current: true})
	state, stateErr := control.RecordingCleanupState(owner)
	startupReceipt, receiptExists, receiptErr := runtimedata.ReadRecordingCleanupReceipt(root)
	var claim *recordingCleanupClaim
	startupErr := errors.Join(stateErr, receiptErr)
	if stateErr == nil && (state.RequestedRevision != state.CompletedRevision || state.ActiveRevision != 0) {
		target, generation, cutoff, claimErr := control.ClaimRecordingCleanup(owner)
		startupErr = errors.Join(startupErr, claimErr)
		if claimErr == nil {
			claim = &recordingCleanupClaim{target: target, generation: generation, cutoff: cutoff, resumed: state.ActiveRevision != 0}
			state.SourceGeneration = generation
		}
	} else if receiptExists {
		startupErr = errors.Join(startupErr, runtimedata.ErrBackupInvalid)
	}
	generation := state.SourceGeneration
	if generation == 0 {
		generation = 1
		startupErr = errors.Join(startupErr, recording.ErrInspectionUnavailable)
	}
	catalogOptions := runtimepersistence.InspectionCatalogOptions{Root: root, LayoutID: layout.ID, Sources: sources, Generation: generation}
	var catalog *runtimepersistence.InspectionCatalog
	var token *runtimepersistence.InspectionMaintenance
	var err error
	if claim != nil || startupErr != nil {
		catalog, token, err = runtimepersistence.NewRecoveringInspectionCatalog(catalogOptions)
	} else {
		catalog, err = runtimepersistence.NewInspectionCatalog(catalogOptions)
	}
	if err != nil {
		return nil, err
	}
	if claim != nil && receiptExists && receiptErr == nil && (startupReceipt.LayoutID != layout.ID || startupReceipt.SourceSet != catalog.Version().SourceSet || startupReceipt.TargetRevision != claim.target || startupReceipt.SourceGeneration != claim.generation || startupReceipt.CutoffMillis != claim.cutoff) {
		startupErr = errors.Join(startupErr, runtimedata.ErrBackupInvalid)
	}
	ctx, cancel := context.WithCancel(owner)
	s := &recordingStorage{nextExpiry: clock.Now().UTC().Add(time.Minute), layout: layout, sources: sources, approvalReady: make(chan struct{}), startupClaim: claim, startupToken: token, startupErr: startupErr, provisioning: layout.ObservationProvisioning, dataGuard: dataGuard, publish: runtimedata.MarkObservationProvisioned, maintenanceRequests: make(chan recordingMaintenanceRequest), cleanupWake: make(chan struct{}, 1), visibility: control, control: control, ctx: ctx, cancel: cancel, done: make(chan struct{}), changed: make(chan struct{}), root: root, recorder: q, tracker: tracker, catalog: catalog, random: random, clock: clock, open: runtimepersistence.OpenObservations, recover: recoverEgressAttempts,
		options: runtimepersistence.ObservationOptions{DatabasePath: filepath.Join(root, layout.Observation.RelativePath), LayoutID: layout.ID, ArchiveID: recordingSourceID(layout), ContentLimits: limits, BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout}}
	if overrides.publishObservations != nil {
		s.publish = overrides.publishObservations
	}
	if overrides.openObservations != nil {
		s.open = overrides.openObservations
	}
	if overrides.recoverEgress != nil {
		s.recover = overrides.recoverEgress
	}
	if startupErr != nil || receiptExists || state.RequestedRevision != state.CompletedRevision || state.ActiveRevision != 0 {
		s.markCleanupPending()
	}
	// These immutable facades never hold a control transaction or connection.
	if q.connections == nil {
		q.connections = s
	}
	if q.egress == nil {
		q.egress = s
	}
	if q.acp == nil {
		q.acp = s
	}
	if q.capture == nil {
		q.capture = s
	}
	if q.nativeHistory == nil {
		q.nativeHistory = s
	}
	if q.nativeContent == nil {
		q.nativeContent = s
	}
	q.available = s.available
	go s.run()
	return s, nil
}
func (s *recordingStorage) available() bool { return s.current.Load() != nil && !s.paused.Load() }
func (s *recordingStorage) acquire() (*recordingSinks, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.current.Load()
	if s.paused.Load() {
		return nil, nil, recording.ErrObservationDiscarded
	}
	if s.closing || v == nil {
		return nil, nil, recording.ErrInspectionUnavailable
	}
	s.active++
	return v, func() { s.mu.Lock(); s.active--; close(s.changed); s.changed = make(chan struct{}); s.mu.Unlock() }, nil
}
func (s *recordingStorage) run() {
	defer close(s.done)
	// Recovery/open are optional and retry on this same fixed goroutine.
	for s.ctx.Err() == nil {
		sinks, err := s.initialize()
		if err != nil {
			s.tracker.recordingState(RecordingDegraded)
			if errors.Is(err, runtimepersistence.ErrRecordingPrivacyUnavailable) {
				s.recordingErr = errors.Join(s.recordingErr, err)
				s.tracker.failRecording("raw_evidence", err)
				s.invalidateRecordingSource()
				break
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-s.ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		s.mu.Lock()
		if !s.closing {
			s.current.Store(sinks)
			s.tracker.recordingState(RecordingAvailable)
		}
		s.mu.Unlock()
		ticker := time.NewTicker(time.Second)
	work:
		for {
			select {
			case <-s.ctx.Done():
				break work
			case <-ticker.C:
				if !s.unsafeMaintenance.Load() {
					_ = s.requestRoutineRecordingCleanup(s.ctx, s.clock.Now().UTC())
					s.performPendingCleanup(sinks)
					if s.unsafeMaintenance.Load() {
						continue
					}
					if err := s.refreshMirror(sinks.store); err != nil {
						s.tracker.recordingState(RecordingDegraded)
						if errors.Is(err, runtimepersistence.ErrRecordingPrivacyUnavailable) {
							s.invalidateRecordingSource()
						}
					} else {
						s.tracker.recordingState(RecordingAvailable)
					}
				}
			case <-s.cleanupWake:
				if !s.unsafeMaintenance.Load() {
					s.performPendingCleanup(sinks)
					if s.unsafeMaintenance.Load() {
						continue
					}
					if err := s.refreshMirror(sinks.store); err != nil {
						s.tracker.recordingState(RecordingDegraded)
						if errors.Is(err, runtimepersistence.ErrRecordingPrivacyUnavailable) {
							s.invalidateRecordingSource()
						}
					}
				}
			case request := <-s.maintenanceRequests:
				s.maintain(sinks, request)
			}
		}
		ticker.Stop()
		s.current.Store(nil)
		// Stop both finite workers; an uncooperative sink stays owned here.
		_ = s.recorder.Shutdown(context.Background())
		s.closeRaw(sinks.raw)
		s.mu.Lock()
		for s.active != 0 {
			changed := s.changed
			s.mu.Unlock()
			<-changed
			s.mu.Lock()
		}
		s.mu.Unlock()
		s.closeErr = errors.Join(s.closeErr, sinks.activity.Shutdown(context.Background()))
		s.closeErr = errors.Join(s.closeErr, sinks.content.Shutdown(context.Background()))
		s.closeErr = errors.Join(s.closeErr, sinks.store.Shutdown(context.Background()))
		break
	}
	_ = s.recorder.Shutdown(context.Background())
	if s.startupToken != nil {
		// initialize has returned all actual I/O. Close admission before the
		// never-admitted recovery token is released, then join normally below.
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		_ = s.catalog.Close(canceled)
		_ = s.startupToken.Close()
		s.startupToken = nil
	}
	// Catalog.Close cancels reads immediately, and retains leases until actual
	// external credential/audit I/O returns, including cancellation-ignoring I/O.
	s.closeErr = errors.Join(s.closeErr, s.catalog.Close(context.Background()))
	if s.closeErr == nil && s.recordingErr == nil {
		s.tracker.recordingState(RecordingDisabled)
	} else {
		s.tracker.recordingState(RecordingDegraded)
	}
	if s.closeErr != nil {
		s.tracker.recordingCleanupPending()
	}
}
func (s *recordingStorage) initialize() (_ *recordingSinks, result error) {
	if s.startupErr != nil {
		return nil, s.startupErr
	}
	if s.provisioning != "unprovisioned" && s.provisioning != "provisioned" {
		return nil, recording.ErrInspectionUnavailable
	}
	statePath := filepath.Join(s.root, "recording-state.json")
	state := recordingProvision{Schema: "vibermate-recording-provision/v1", LayoutID: s.options.LayoutID, SourceID: s.options.ArchiveID}
	data, err := readRecordingProvision(statePath)
	if s.provisioning == "provisioned" && err != nil {
		return nil, recording.ErrInspectionUnavailable
	}
	if err == nil {
		var persisted recordingProvision
		var fields map[string]json.RawMessage
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if json.Unmarshal(data, &fields) != nil || len(fields) != 4 || decoder.Decode(&persisted) != nil || persisted.Schema != state.Schema || persisted.LayoutID != state.LayoutID || persisted.SourceID != state.SourceID {
			return nil, recording.ErrInspectionUnavailable
		}
		if flag, ok := fields["provisioned"]; !ok || (string(flag) != "true" && string(flag) != "false") {
			return nil, recording.ErrInspectionUnavailable
		}
		if s.provisioning == "provisioned" && !persisted.Provisioned {
			return nil, recording.ErrInspectionUnavailable
		}
		state = persisted
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if info, e := os.Lstat(s.options.DatabasePath); e == nil && !info.Mode().IsRegular() {
		return nil, recording.ErrInspectionUnavailable
	}
	if state.Provisioned {
		if info, e := os.Lstat(s.options.DatabasePath); e != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return nil, recording.ErrInspectionUnavailable
		}
	}
	s.publishedMirror = recording.InspectionStamp{}
	store, err := s.open(s.ctx, s.options)
	if err != nil {
		return nil, err
	}
	var raw rawEvidenceRuntime
	defer func() {
		if result != nil {
			if raw != nil {
				s.closeRaw(raw)
			}
			s.closeErr = errors.Join(s.closeErr, store.Shutdown(context.Background()))
		}
	}()
	if err = s.refreshMirror(store); err != nil {
		return nil, err
	}
	if s.startupClaim != nil {
		if err = s.waitApprovalAuthority(); err != nil {
			return nil, err
		}
		if err = s.executeRecordingCleanup(store, s.startupToken, *s.startupClaim); err != nil {
			s.startupErr = err
			s.invalidateRecordingSource()
			return nil, err
		}
		if err = s.refreshMirror(store); err != nil {
			s.startupErr = err
			s.invalidateRecordingSource()
			return nil, err
		}
		if err = s.startupToken.Close(); err != nil {
			return nil, err
		}
		s.startupToken = nil
		s.startupClaim = nil
	}
	if err = s.recover(s.ctx, store.EgressAttemptRepository(), s.clock.Now().UTC()); err != nil {
		return nil, err
	}
	raw, err = buildRawEvidence(rawEvidenceBuildRequest{ctx: s.ctx, budget: s.recorder.budget, repository: store.RawEvidenceRepository(), random: s.random, clock: s.clock, config: rawevidence.DefaultConfig()})
	if err != nil {
		return nil, err
	}
	activities, err := buildActivity(activityBuildRequest{repository: store.ActivityRepository(), clock: s.clock, random: s.random})
	if err != nil {
		return nil, err
	}
	content, err := buildExchangeContent(exchangeContentBuildRequest{ctx: s.ctx, repository: store.ExchangeContentRepository(), clock: s.clock, contentLimits: s.options.ContentLimits})
	if err != nil {
		_ = activities.Shutdown(context.Background())
		return nil, err
	}
	state.Provisioned = true
	if err = writeRecordingProvision(statePath, state); err != nil {
		_ = activities.Shutdown(context.Background())
		_ = content.Shutdown(context.Background())
		return nil, err
	}
	if err = s.publish(s.ctx, s.root, s.options.LayoutID, s.dataGuard); err != nil {
		_ = activities.Shutdown(context.Background())
		_ = content.Shutdown(context.Background())
		return nil, err
	}
	s.provisioning = "provisioned"
	return &recordingSinks{store: store, raw: raw, activity: activities, content: content}, nil
}
func writeRecordingProvision(path string, state recordingProvision) error {
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600) || err != nil && !errors.Is(err, os.ErrNotExist) {
		return recording.ErrInspectionUnavailable
	}
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".recording-state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = errors.Join(f.Sync(), f.Close()); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
func (s *recordingStorage) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.current.Store(nil)
	s.mu.Unlock()
	s.cancel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_ = s.catalog.Close(canceled)
	select {
	case <-s.done:
		return s.closeErr
	case <-ctx.Done():
		s.tracker.recordingCleanupPending()
		return ctx.Err()
	}
}
func (s *recordingStorage) PutConnection(ctx context.Context, scope recording.Scope, e connectionevent.Event) error {
	v, done, err := s.acquire()
	if err != nil {
		return err
	}
	defer done()
	return v.store.ConnectionEventRepository().(connectionevent.ScopedStore).PutConnection(ctx, scope, e)
}
func (s *recordingStorage) PutScopedAttempt(ctx context.Context, scope recording.Scope, a egressaudit.Attempt) error {
	v, done, err := s.acquire()
	if err != nil {
		return err
	}
	defer done()
	return v.store.EgressAttemptStore().(egressaudit.ScopedAttemptStore).PutScopedAttempt(ctx, scope, a)
}
func (s *recordingStorage) PutACP(ctx context.Context, a acpobservation.Record) error {
	v, done, err := s.acquire()
	if err != nil {
		return err
	}
	defer done()
	return v.store.PutACP(ctx, a)
}
func (s *recordingStorage) PutCaptureObservation(ctx context.Context, scope recording.Scope, a recording.CaptureObservation) error {
	v, done, err := s.acquire()
	if err != nil {
		return err
	}
	defer done()
	return v.store.PutCaptureObservation(ctx, scope, a)
}
func (s *recordingStorage) PutNativeHistory(ctx context.Context, a nativetool.RecordedHistory) error {
	v, done, err := s.acquire()
	if err != nil {
		return err
	}
	defer done()
	return v.store.PutNativeHistory(ctx, a)
}
func (s *recordingStorage) PutNativeContent(ctx context.Context, a nativetool.ContentObservation) error {
	v, done, err := s.acquire()
	if err != nil {
		return err
	}
	defer done()
	return v.store.PutNativeContent(ctx, a)
}
func (s *recordingStorage) PutCollectedUsage(ctx context.Context, a runtimeusage.CollectedObservation) error {
	v, done, err := s.acquire()
	if err != nil {
		return err
	}
	defer done()
	return v.store.PutCollectedUsage(ctx, a)
}
func (s *recordingStorage) AppendLocatedRevealAudit(ctx context.Context, a rawevidence.LocatedRevealAudit) error {
	v, done, err := s.acquire()
	if err != nil {
		return err
	}
	defer done()
	return v.store.AppendLocatedRevealAudit(ctx, a)
}
func (s *recordingStorage) PutScopedConversationIdentity(ctx context.Context, scope recording.Scope, id string, a agentconversation.ClientIdentity) error {
	v, done, err := s.acquire()
	if err != nil {
		return err
	}
	defer done()
	return v.store.ConversationIdentityRepository().(activity.ScopedConversationIdentityWriter).PutScopedConversationIdentity(ctx, scope, id, a)
}
func (s *recordingStorage) GetConversationIdentity(context.Context, string) (agentconversation.ClientIdentity, error) {
	return agentconversation.ClientIdentity{}, recording.ErrInspectionUnavailable
}
func (s *recordingStorage) PutConversationIdentity(context.Context, string, agentconversation.ClientIdentity) error {
	return recording.ErrObservationDiscarded
}

func (s *recordingStorage) RepairConversation(ctx context.Context, repair activity.ConversationRepair) error {
	if !repair.Root.Current || repair.Root.SourceID != s.options.ArchiveID {
		return recording.ErrObservationDiscarded
	}
	if err := s.catalog.Check(repair.Root.Token.Sources); err != nil {
		return err
	}
	if err := s.visibility.RecheckInspectionStamp(ctx, repair.Root.Token.Control); err != nil {
		return err
	}
	v, done, err := s.acquire()
	if err != nil {
		return err
	}
	err = v.store.RepairConversation(ctx, repair)
	done()
	if err != nil {
		return err
	}
	if err = s.catalog.Check(repair.Root.Token.Sources); err != nil {
		return err
	}
	return s.visibility.RecheckInspectionStamp(ctx, repair.Root.Token.Control)
}

func readRecordingProvision(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 4096 {
		return nil, recording.ErrInspectionUnavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, recording.ErrInspectionUnavailable
	}
	b, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(b) > 4096 {
		return nil, recording.ErrInspectionUnavailable
	}
	return b, nil
}

// This runs only on the existing fixed lifecycle owner. Polling durable control
// versions also recovers a missed wake; required mutations never wait for it.
func (s *recordingStorage) refreshMirror(store *runtimepersistence.ObservationStore) error {
	snapshot, err := s.control.ReadInspectionSnapshot(s.ctx, nil)
	if err != nil {
		return err
	}
	if s.publishedMirror.Valid() {
		if err = store.CheckRecordingWriter(s.ctx); err != nil {
			return err
		}
	}
	if snapshot.Stamp == s.publishedMirror {
		return nil
	}
	if err = store.PublishInspectionMirror(s.ctx, s.control); err != nil {
		return err
	}
	s.publishedMirror = snapshot.Stamp
	return nil
}

func (s *recordingStorage) invalidateRecordingSource() {
	s.unsafeMaintenance.Store(true)
	s.paused.Store(true)
	s.recorder.mu.Lock()
	s.recorder.sealed = true
	s.recorder.mu.Unlock()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_ = s.catalog.Close(canceled)
	s.tracker.recordingState(RecordingDegraded)
}

// Only this provenance-known Raw API separates marker persistence from worker
// ownership. Unknown/fake implementations keep their complete shutdown error;
// no sentinel filtering can discard a mixed actual close/wait failure.
func (s *recordingStorage) closeRaw(raw rawEvidenceRuntime) {
	if port, ok := raw.(interface {
		ShutdownOwned(context.Context) (rawevidence.ShutdownResult, error)
	}); ok {
		result, ownershipErr := port.ShutdownOwned(context.Background())
		s.closeErr = errors.Join(s.closeErr, ownershipErr)
		if result.FinalizationError != nil {
			s.recordingErr = errors.Join(s.recordingErr, result.FinalizationError)
			s.tracker.failRecording("raw_evidence", result.FinalizationError)
		}
		return
	}
	s.closeErr = errors.Join(s.closeErr, raw.Shutdown(context.Background()))
}
