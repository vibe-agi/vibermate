package rawevidence

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/recording"
)

const (
	defaultMaximumQueueRecords = 2048
	defaultMaximumQueueBytes   = 64 << 20
	defaultMaximumBatchRecords = 64
	defaultMaximumBatchBytes   = 4 << 20
	defaultFlushInterval       = 100 * time.Millisecond
	writerIDBytes              = 20
)

var ErrScopeTerminal = errors.New("raw evidence capture scope is terminal")

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

type Config struct {
	MaximumQueueRecords int
	MaximumQueueBytes   int64
	MaximumBatchRecords int
	MaximumBatchBytes   int64
	FlushInterval       time.Duration
}

func DefaultConfig() Config {
	return Config{
		MaximumQueueRecords: defaultMaximumQueueRecords,
		MaximumQueueBytes:   defaultMaximumQueueBytes,
		MaximumBatchRecords: defaultMaximumBatchRecords,
		MaximumBatchBytes:   defaultMaximumBatchBytes,
		FlushInterval:       defaultFlushInterval,
	}
}

func (config Config) validate() error {
	if config.MaximumQueueRecords <= 0 || config.MaximumQueueBytes <= 0 ||
		config.MaximumBatchRecords <= 0 || config.MaximumBatchBytes <= 0 ||
		config.FlushInterval <= 0 ||
		config.MaximumBatchRecords > config.MaximumQueueRecords ||
		config.MaximumBatchBytes > config.MaximumQueueBytes {
		return errors.New("raw evidence writer configuration is invalid")
	}
	return nil
}

type Options struct {
	Budget     *recording.Budget
	Repository Repository
	Random     io.Reader
	Clock      Clock
	Config     Config
}

type queuedEnvelope struct {
	record      StoredEnvelope
	weight      int64
	reservation *recording.Reservation
	stamp       recording.Stamp
}

type flushRequest struct {
	target uint64
	result chan error
}

type Statistics struct {
	WriterID                 string
	AdmittedRecords          uint64
	DurableWatermark         uint64 // Deprecated: contiguous confirmed prefix only.
	SettledSequence          uint64
	HighestPersistedSequence uint64
	PersistedRecords         uint64
	BatchCommits             uint64
	QueueRecords             int
	QueueBytes               int64
	LastFlushDuration        time.Duration
	LastFailure              string
	MaximumUnflushedTime     time.Duration
}

type Manager struct {
	repository Repository
	clock      Clock
	random     io.Reader
	redactor   Redactor
	config     Config
	writerID   string

	budget  *recording.Budget
	records chan queuedEnvelope
	flushes chan flushRequest
	done    chan struct{}

	workerContext context.Context
	cancelWorker  context.CancelCauseFunc

	admissionMu sync.Mutex
	accepting   bool
	next        uint64
	scopeLatest map[scopeKey]uint64

	scopeMu        sync.Mutex
	scopeAccepting bool
	scopes         map[scopeKey]*scopeState
	scopeChanged   chan struct{}

	statsMu  sync.Mutex
	stats    Statistics
	recovery Recovery

	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
}

type scopeKey struct {
	kind ScopeKind
	id   string
}

type scopePhase uint8

const (
	scopeOpen scopePhase = iota
	scopePreparingTerminal
	scopeSealed
)

type scopeState struct {
	active uint64
	phase  scopePhase
}

type requestScopeLease struct {
	manager *Manager
	key     scopeKey
	once    sync.Once
}

type terminalScopeLease struct {
	manager *Manager
	key     scopeKey
	once    sync.Once
}

var _ RequestRecorder = (*Manager)(nil)

func Open(ctx context.Context, options Options) (*Manager, error) {
	if ctx == nil || options.Repository == nil ||
		options.Random == nil || options.Clock == nil {
		return nil, errors.New("raw evidence writer dependencies are incomplete")
	}
	if err := options.Config.validate(); err != nil {
		return nil, err
	}
	if options.Budget == nil {
		options.Budget, _ = recording.NewBudget(recording.Limits{Records: int64(options.Config.MaximumQueueRecords), Bytes: options.Config.MaximumQueueBytes})
	}
	// The salt is resolved before a writer session opens, so a database that
	// cannot supply one leaves no session behind and admits no observation.
	salt, err := options.Repository.RedactionSalt(ctx)
	if err != nil {
		return nil, fmt.Errorf("load raw evidence redaction salt: %w", err)
	}
	redactor, err := NewRedactor(salt)
	if err != nil {
		return nil, err
	}
	writerID, err := randomIdentity(options.Random, writerIDBytes)
	if err != nil {
		return nil, fmt.Errorf("create raw evidence writer identity: %w", err)
	}
	recovery, err := options.Repository.BeginWriterSession(ctx, WriterSession{
		WriterID:             writerID,
		StartedAt:            options.Clock.Now().UTC(),
		MaximumUnflushedTime: options.Config.FlushInterval,
	})
	if err != nil {
		return nil, fmt.Errorf("begin raw evidence writer session: %w", err)
	}
	workerContext, cancelWorker := context.WithCancelCause(
		context.WithoutCancel(ctx),
	)
	manager := &Manager{
		repository:     options.Repository,
		clock:          options.Clock,
		random:         options.Random,
		redactor:       redactor,
		config:         options.Config,
		writerID:       writerID,
		budget:         options.Budget,
		records:        make(chan queuedEnvelope, options.Config.MaximumQueueRecords),
		flushes:        make(chan flushRequest),
		done:           make(chan struct{}),
		workerContext:  workerContext,
		cancelWorker:   cancelWorker,
		accepting:      true,
		scopeLatest:    make(map[scopeKey]uint64),
		scopeAccepting: true,
		scopes:         make(map[scopeKey]*scopeState),
		scopeChanged:   make(chan struct{}),
		stats: Statistics{
			WriterID:             writerID,
			MaximumUnflushedTime: options.Config.FlushInterval,
		},
		recovery:     recovery,
		shutdownDone: make(chan struct{}),
	}
	go manager.run()
	return manager, nil
}

func (manager *Manager) Recovery() Recovery {
	if manager == nil {
		return Recovery{}
	}
	return manager.recovery
}

func (manager *Manager) ListExchange(
	ctx context.Context,
	exchangeID string,
) ([]EnvelopeMetadata, error) {
	if manager == nil || ctx == nil || !validIdentity(exchangeID) {
		return nil, ErrInvalidRead
	}
	records, err := manager.repository.ListExchange(ctx, exchangeID)
	if err != nil {
		return nil, err
	}
	metadata := make([]EnvelopeMetadata, 0, len(records))
	now := manager.clock.Now()
	for index := range records {
		if records[index].ExpiresAt.After(now) {
			metadata = append(metadata, MetadataOf(records[index]))
		}
	}
	return metadata, nil
}

func (manager *Manager) Reveal(
	ctx context.Context,
	request RevealRequest,
) (RevealedEnvelope, error) {
	if manager == nil || ctx == nil {
		return RevealedEnvelope{}, errors.New("raw evidence reveal is unavailable")
	}
	if err := request.Validate(); err != nil {
		return RevealedEnvelope{}, err
	}
	record, err := manager.repository.GetEnvelope(ctx, request.EnvelopeID)
	if err != nil {
		return RevealedEnvelope{}, err
	}
	payload, readErr := manager.ReadPayload(record)
	outcome := RevealSucceeded
	if readErr != nil {
		outcome = RevealUnavailable
	}
	audit := RevealAudit{
		EnvelopeID: record.EnvelopeID,
		ExchangeID: record.ExchangeID,
		ActorID:    request.ActorID,
		Outcome:    outcome,
		OccurredAt: manager.clock.Now().UTC(),
	}
	if err := manager.repository.AppendRevealAudit(ctx, audit); err != nil {
		clear(payload.Body)
		payload = Payload{}
		return RevealedEnvelope{}, fmt.Errorf("audit raw evidence reveal: %w", err)
	}
	if readErr != nil {
		return RevealedEnvelope{}, readErr
	}
	return RevealedEnvelope{Metadata: MetadataOf(record), Payload: payload}, nil
}

// BeginScope admits one authenticated proxy request before it can acquire an
// Environment request plan or create an Exchange. A terminal transition that
// wins this race prevents the request from adding a late evidence tail.
func (manager *Manager) BeginScope(
	ctx context.Context,
	kind ScopeKind,
	id string,
) (ScopeLease, error) {
	if manager == nil {
		return nil, errors.New("raw evidence scope admission is invalid")
	}
	key, err := captureScopeKey(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	manager.scopeMu.Lock()
	defer manager.scopeMu.Unlock()
	if !manager.scopeAccepting {
		return nil, errors.New("raw evidence writer is closing")
	}
	state := manager.scopeStateLocked(key)
	if state.phase != scopeOpen {
		return nil, ErrScopeTerminal
	}
	state.active++
	manager.notifyScopeLocked()
	return &requestScopeLease{manager: manager, key: key}, nil
}

// PrepareTerminalScope closes new request admission, drains every request that
// already holds a scope lease, and then flushes the final scope watermark.
func (manager *Manager) PrepareTerminalScope(
	ctx context.Context,
	kind ScopeKind,
	id string,
) (TerminalScope, error) {
	if manager == nil {
		return nil, errors.New("raw evidence terminal scope is invalid")
	}
	key, err := captureScopeKey(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	manager.scopeMu.Lock()
	var state *scopeState
	for {
		if !manager.scopeAccepting {
			manager.scopeMu.Unlock()
			return nil, errors.New("raw evidence writer is closing")
		}
		state = manager.scopeStateLocked(key)
		switch state.phase {
		case scopeOpen:
			state.phase = scopePreparingTerminal
			manager.notifyScopeLocked()
		case scopePreparingTerminal:
			changed := manager.scopeChanged
			manager.scopeMu.Unlock()
			select {
			case <-changed:
			case <-ctx.Done():
				return nil, fmt.Errorf(
					"wait for raw evidence terminal scope: %w", ctx.Err(),
				)
			}
			manager.scopeMu.Lock()
			continue
		case scopeSealed:
			manager.scopeMu.Unlock()
			return &terminalScopeLease{manager: manager, key: key}, nil
		default:
			manager.scopeMu.Unlock()
			return nil, ErrScopeTerminal
		}
		break
	}
	for state.active != 0 {
		changed := manager.scopeChanged
		manager.scopeMu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			manager.reopenPreparingScope(key)
			return nil, fmt.Errorf("drain raw evidence capture scope: %w", ctx.Err())
		}
		manager.scopeMu.Lock()
		state = manager.scopeStateLocked(key)
	}
	manager.scopeMu.Unlock()

	if err := manager.FlushScope(ctx, kind, id); err != nil {
		manager.reopenPreparingScope(key)
		return nil, err
	}
	return &terminalScopeLease{manager: manager, key: key}, nil
}

func (lease *requestScopeLease) Release() {
	if lease == nil || lease.manager == nil {
		return
	}
	lease.once.Do(func() {
		lease.manager.scopeMu.Lock()
		state := lease.manager.scopes[lease.key]
		if state != nil && state.active != 0 {
			state.active--
			lease.manager.notifyScopeLocked()
		}
		lease.manager.scopeMu.Unlock()
	})
}

func (lease *terminalScopeLease) Commit() {
	lease.finish(true)
}

func (lease *terminalScopeLease) Abort() {
	lease.finish(false)
}

func (lease *terminalScopeLease) finish(commit bool) {
	if lease == nil || lease.manager == nil {
		return
	}
	lease.once.Do(func() {
		lease.manager.scopeMu.Lock()
		state := lease.manager.scopes[lease.key]
		if state != nil && state.phase == scopePreparingTerminal {
			if commit {
				state.phase = scopeSealed
			} else {
				state.phase = scopeOpen
			}
			lease.manager.notifyScopeLocked()
		}
		lease.manager.scopeMu.Unlock()
	})
}

func captureScopeKey(
	ctx context.Context,
	kind ScopeKind,
	id string,
) (scopeKey, error) {
	if ctx == nil || (kind != ScopeManagedRun && kind != ScopeManualCapture) ||
		!validIdentity(id) {
		return scopeKey{}, errors.New("raw evidence capture scope is invalid")
	}
	if err := ctx.Err(); err != nil {
		return scopeKey{}, err
	}
	return scopeKey{kind: kind, id: id}, nil
}

func (manager *Manager) scopeStateLocked(key scopeKey) *scopeState {
	state := manager.scopes[key]
	if state == nil {
		state = &scopeState{}
		manager.scopes[key] = state
	}
	return state
}

func (manager *Manager) notifyScopeLocked() {
	close(manager.scopeChanged)
	manager.scopeChanged = make(chan struct{})
}

func (manager *Manager) reopenPreparingScope(key scopeKey) {
	manager.scopeMu.Lock()
	state := manager.scopes[key]
	if state != nil && state.phase == scopePreparingTerminal {
		state.phase = scopeOpen
		manager.notifyScopeLocked()
	}
	manager.scopeMu.Unlock()
}

// Observe is the compatibility adapter. A loss has no durability watermark and
// is not an error in the forwarding operation.
func (manager *Manager) Observe(ctx context.Context, observation Observation) (Watermark, error) {
	if manager == nil {
		return Watermark{}, nil
	}
	if ctx == nil {
		manager.budget.RecordOffer(recording.Raw, recording.DroppedInvalid, 0)
		return Watermark{}, nil
	}
	if ctx.Err() != nil {
		manager.budget.RecordOffer(recording.Raw, recording.DroppedClosed, 0)
		return Watermark{}, nil
	}
	mark, _ := manager.offer(observation, recording.Stamp{})
	return mark, nil
}

func (manager *Manager) TryObserve(stamp recording.Stamp, observation Observation) recording.OfferResult {
	if manager == nil {
		return recording.DroppedUnavailable
	}
	if !stamp.Current() {
		manager.budget.RecordOffer(recording.Raw, recording.DroppedStale, 0)
		return recording.DroppedStale
	}
	if !stampMatches(stamp, observation.Context) {
		manager.budget.RecordOffer(recording.Raw, recording.DroppedInvalid, 0)
		return recording.DroppedInvalid
	}
	_, result := manager.offer(observation, stamp)
	return result
}

func (manager *Manager) offer(observation Observation, stamp recording.Stamp) (Watermark, recording.OfferResult) {
	if manager == nil {
		return Watermark{}, recording.DroppedUnavailable
	}
	result := recording.DroppedInvalid
	var retained uint64
	defer func() { manager.budget.RecordOffer(recording.Raw, result, retained) }()
	// Charge validation, redaction, JSON encoder overlap and retained data before
	// any allocation. This conservative upper bound includes escaped JSON strings.
	weight := observationCharge(observation)
	reservation, ok := manager.budget.TryReserve(recording.Raw, weight)
	if !ok {
		result = recording.DroppedCapacity
		return Watermark{}, result
	}
	defer func() {
		if result != recording.Accepted {
			reservation.Release()
		}
	}()
	if observation.validate() != nil || (!observation.Complete && !validObservationReason(observation.IncompleteReason)) {
		return Watermark{}, result
	}
	if observation.Recording == RecordingOff {
		result = recording.DroppedUnavailable
		return Watermark{}, result
	}
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = manager.clock.Now().UTC()
	} else {
		observation.ObservedAt = observation.ObservedAt.UTC()
	}
	draft, err := manager.prepare(observation)
	if err != nil {
		return Watermark{}, result
	}
	mark, outcome := manager.publish(draft, reservation, weight, stamp)
	result = outcome
	if result == recording.Accepted {
		retained = uint64(len(draft.Body) + len(draft.PayloadMetadata))
	}
	return mark, result
}

func (manager *Manager) publish(draft StoredEnvelope, reservation *recording.Reservation, weight int64, stamp recording.Stamp) (Watermark, recording.OfferResult) {
	draft.RecordingFence = stamp.Fence()
	draft.WriterID = manager.writerID
	draft.Watermark = 1
	draft.EnvelopeID = manager.writerID + ".1"
	if draft.Validate() != nil {
		return Watermark{}, recording.DroppedInvalid
	}
	manager.admissionMu.Lock()
	defer manager.admissionMu.Unlock()
	if !manager.accepting {
		return Watermark{}, recording.DroppedClosed
	}
	if stamp.Fence().ArchiveEpoch != 0 && !stamp.Current() {
		return Watermark{}, recording.DroppedStale
	}
	manager.scopeMu.Lock()
	state := manager.scopes[scopeKey{kind: draft.ScopeKind, id: draft.ScopeID}]
	sealed := state != nil && state.phase == scopeSealed
	manager.scopeMu.Unlock()
	if sealed {
		return Watermark{}, recording.DroppedClosed
	}
	sequence := manager.next + 1
	draft.WriterID = manager.writerID
	draft.Watermark = sequence
	draft.EnvelopeID = fmt.Sprintf("%s.%d", manager.writerID, sequence)
	manager.statsMu.Lock()
	defer manager.statsMu.Unlock()
	select {
	case manager.records <- queuedEnvelope{record: draft, weight: weight, reservation: reservation, stamp: stamp}:
		manager.next = sequence
		manager.scopeLatest[scopeKey{kind: draft.ScopeKind, id: draft.ScopeID}] = sequence
		manager.stats.AdmittedRecords++
		manager.stats.QueueRecords++
		manager.stats.QueueBytes += weight
		return Watermark{WriterID: manager.writerID, Sequence: sequence}, recording.Accepted
	default:
		return Watermark{}, recording.DroppedCapacity
	}
}

// FlushScope waits for every envelope in one capture scope that was admitted
// before this call. It does not wait for unrelated captures that continue to
// produce traffic after the scope reaches its terminal transition.
func (manager *Manager) FlushScope(
	ctx context.Context,
	kind ScopeKind,
	id string,
) error {
	if manager == nil || ctx == nil ||
		(kind != ScopeManagedRun && kind != ScopeManualCapture) ||
		!validIdentity(id) {
		return errors.New("raw evidence scope flush request is invalid")
	}
	key := scopeKey{kind: kind, id: id}
	manager.admissionMu.Lock()
	sequence := manager.scopeLatest[key]
	writerID := manager.writerID
	manager.admissionMu.Unlock()
	if sequence == 0 {
		return nil
	}
	if err := manager.Flush(ctx, Watermark{
		WriterID: writerID,
		Sequence: sequence,
	}); err != nil && manager.settledSequence() < sequence {
		return err
	}

	// Failed or ambiguous appends that returned are settled too. This scope
	// barrier fences actual writer I/O; it must not promise recording durability.
	// Bound lifecycle memory without losing a newer admission that raced the
	// flush snapshot. A later observation keeps its own watermark for the next
	// terminal or shutdown barrier.
	manager.admissionMu.Lock()
	if manager.scopeLatest[key] == sequence {
		delete(manager.scopeLatest, key)
	}
	manager.admissionMu.Unlock()
	return nil
}

// Flush is an administrative settlement barrier, not a durability guarantee. A zero
// watermark means every record admitted before this call.
func (manager *Manager) Flush(ctx context.Context, watermark Watermark) error {
	if manager == nil || ctx == nil {
		return errors.New("raw evidence flush request is incomplete")
	}
	manager.admissionMu.Lock()
	target := manager.next
	if watermark.Sequence != 0 {
		if watermark.WriterID != manager.writerID || watermark.Sequence > target {
			manager.admissionMu.Unlock()
			return errors.New("raw evidence watermark is not owned by this writer")
		}
		target = watermark.Sequence
	}
	// The watermark snapshot is complete once target is known. Do not retain
	// the admission lock while waiting for the single writer goroutine: a slow
	// repository commit or another flush must not make new observations wait on
	// a non-context-aware mutex. Later admissions have higher watermarks and do
	// not change the set of writer operations this settlement barrier joins.
	manager.admissionMu.Unlock()
	if target == 0 || manager.settledSequence() >= target {
		return nil
	}
	request := flushRequest{target: target, result: make(chan error, 1)}
	select {
	case manager.flushes <- request:
	case <-ctx.Done():
		return fmt.Errorf("request raw evidence flush: %w", ctx.Err())
	case <-manager.workerContext.Done():
		return errors.New("raw evidence writer stopped")
	}
	select {
	case err := <-request.result:
		return err
	case <-ctx.Done():
		return fmt.Errorf("wait for raw evidence flush: %w", ctx.Err())
	case <-manager.workerContext.Done():
		return errors.New("raw evidence writer stopped")
	}
}

func (manager *Manager) Statistics() Statistics {
	if manager == nil {
		return Statistics{}
	}
	manager.statsMu.Lock()
	defer manager.statsMu.Unlock()
	return manager.stats
}

func (manager *Manager) Shutdown(ctx context.Context) error {
	if manager == nil || ctx == nil {
		return errors.New("raw evidence shutdown context is incomplete")
	}
	manager.shutdownOnce.Do(func() {
		// Seal synchronously: a canceled caller must not stop the worker before
		// producers have lost publication authority.
		manager.admissionMu.Lock()
		manager.accepting = false
		manager.admissionMu.Unlock()
		manager.scopeMu.Lock()
		manager.scopeAccepting = false
		manager.notifyScopeLocked()
		manager.scopeMu.Unlock()
		go manager.executeShutdown(ctx)
	})
	select {
	case <-manager.shutdownDone:
		return manager.shutdownErr
	case <-ctx.Done():
		manager.cancelWorker(ctx.Err())
		return fmt.Errorf("wait for raw evidence shutdown: %w", ctx.Err())
	}
}

func (manager *Manager) executeShutdown(ctx context.Context) {
	manager.cancelWorker(errors.New("raw evidence writer stopped"))
	<-manager.done // Actual I/O ownership survives a caller timeout.
	manager.shutdownErr = manager.repository.CloseWriterSession(ctx, manager.writerID, manager.clock.Now().UTC())
	close(manager.shutdownDone)
}

func (manager *Manager) prepare(observation Observation) (StoredEnvelope, error) {
	var body []byte
	if observation.Recording == RecordingFull && !observation.Unavailable {
		body = make([]byte, len(observation.Body))
		copy(body, observation.Body)
	}
	if observation.HasContentLength {
		observation.Headers = observation.Headers.Clone()
		if observation.Headers == nil {
			observation.Headers = make(map[string][]string)
		}
		observation.Headers.Set("Content-Length", strconv.FormatInt(observation.ContentLength, 10))
	}
	frames := observation.Frames
	state := PayloadCaptured
	digestScope := DigestFull
	reason := ""
	if observation.Unavailable {
		state = PayloadUnavailable
		digestScope = DigestUnavailable
		reason = observation.IncompleteReason
	} else if !observation.Complete {
		state = PayloadTruncated
		digestScope = DigestObservedPrefix
		reason = observation.IncompleteReason
	}
	totalBodyBytes := observation.TotalBodyBytes
	if totalBodyBytes == 0 {
		totalBodyBytes = int64(len(observation.Body))
	}
	digest := observation.BodySHA256
	if observation.Unavailable {
		digest = [sha256.Size]byte{}
	} else if !observation.DigestAvailable {
		digest = sha256.Sum256(observation.Body)
	} else if observation.FullDigestAvailable {
		digestScope = DigestFull
	}
	var payloadMetadata []byte
	var redactedFields []string
	if state == PayloadUnavailable {
		body = nil
		frames = nil
	} else if observation.Recording == RecordingMetadataOnly {
		state = PayloadMetadataOnly
		reason = "recording_metadata_only"
		body = nil
		frames = nil
	} else {
		built, redacted, err := payloadOf(
			observation, frames, manager.redactor,
		)
		if err != nil {
			return StoredEnvelope{}, err
		}
		if payloadMetadata, err = built.MarshalMetadata(); err != nil {
			return StoredEnvelope{}, err
		}
		redactedFields = redacted
	}
	record := StoredEnvelope{
		Layer:                    observation.Layer,
		ScopeKind:                observation.ScopeKind,
		ScopeID:                  strings.Clone(observation.ScopeID),
		ExchangeID:               strings.Clone(observation.ExchangeID),
		ConnectionID:             strings.Clone(observation.ConnectionID),
		AttemptID:                strings.Clone(observation.AttemptID),
		EnvironmentID:            strings.Clone(observation.EnvironmentID),
		EnvironmentRevision:      observation.EnvironmentRevision,
		EnvironmentDigest:        strings.Clone(observation.EnvironmentDigest),
		ClientEndpointID:         strings.Clone(observation.ClientEndpointID),
		ClientEndpointRevision:   observation.ClientEndpointRevision,
		UpstreamEndpointID:       strings.Clone(observation.UpstreamEndpointID),
		UpstreamEndpointRevision: observation.UpstreamEndpointRevision,
		ProtocolPlanID:           strings.Clone(observation.ProtocolPlanID),
		ProtocolPlanRevision:     observation.ProtocolPlanRevision,
		RouteID:                  strings.Clone(observation.RouteID),
		RouteRevision:            observation.RouteRevision,
		AccountID:                strings.Clone(observation.AccountID),
		AccountRevision:          observation.AccountRevision,
		CredentialEpoch:          observation.CredentialEpoch,
		ObservedAt:               observation.ObservedAt,
		ExpiresAt: observation.ObservedAt.AddDate(
			0, 0, int(observation.RetentionDays),
		),
		Method:                   strings.Clone(observation.Method),
		StatusCode:               observation.StatusCode,
		Scheme:                   strings.Clone(observation.Scheme),
		Authority:                strings.Clone(observation.Authority),
		Path:                     strings.Clone(observation.Path),
		RawQuery:                 strings.Clone(observation.RawQuery),
		ContentType:              strings.Clone(observation.ContentType),
		ContentEncoding:          strings.Clone(observation.ContentEncoding),
		Representation:           strings.Clone(observation.Representation),
		Canonicalization:         httpCanonicalization,
		HeaderCount:              headerValueCount(observation.Headers),
		TrailerCount:             headerValueCount(observation.Trailers),
		BodyBytes:                totalBodyBytes,
		BodySHA256:               digest,
		DigestScope:              digestScope,
		PayloadState:             state,
		PayloadReason:            reason,
		RedactedCredentialFields: redactedFields,
		PayloadMetadata:          payloadMetadata,
		Body:                     body,
	}
	return record, nil
}

func (manager *Manager) run() {
	defer close(manager.done)
	timer := time.NewTimer(manager.config.FlushInterval)
	defer timer.Stop()
	batch := make([]queuedEnvelope, 0, manager.config.MaximumBatchRecords)
	var batchBytes int64
	defer func() {
		manager.settle(batch, batchBytes, recording.Discarded, nil)
		for {
			select {
			case item := <-manager.records:
				manager.settle([]queuedEnvelope{item}, item.weight, recording.Discarded, nil)
			default:
				return
			}
		}
	}()
	for {
		select {
		case item := <-manager.records:
			if len(batch) > 0 && batchBytes+item.weight > manager.config.MaximumBatchBytes {
				manager.commit(&batch, &batchBytes)
			}
			batch = append(batch, item)
			batchBytes += item.weight
			if len(batch) >= manager.config.MaximumBatchRecords ||
				batchBytes >= manager.config.MaximumBatchBytes {
				manager.commit(&batch, &batchBytes)
				resetTimer(timer, manager.config.FlushInterval)
			}
		case request := <-manager.flushes:
			err := manager.drainRecords(&batch, &batchBytes)
			if err == nil {
				err = manager.commit(&batch, &batchBytes)
			}
			if err == nil && manager.settledSequence() < request.target {
				err = errors.New("raw evidence flush did not reach its watermark")
			}
			request.result <- err
			resetTimer(timer, manager.config.FlushInterval)
		case <-timer.C:
			manager.commit(&batch, &batchBytes)
			timer.Reset(manager.config.FlushInterval)
		case <-manager.workerContext.Done():
			return
		}
	}
}

func (manager *Manager) drainRecords(
	batch *[]queuedEnvelope,
	batchBytes *int64,
) error {
	for {
		select {
		case item := <-manager.records:
			if len(*batch) > 0 && *batchBytes+item.weight > manager.config.MaximumBatchBytes {
				manager.commit(batch, batchBytes)
			}
			*batch = append(*batch, item)
			*batchBytes += item.weight
			if len(*batch) >= manager.config.MaximumBatchRecords ||
				*batchBytes >= manager.config.MaximumBatchBytes {
				if err := manager.commit(batch, batchBytes); err != nil {
					return err
				}
			}
		default:
			return nil
		}
	}
}

func (manager *Manager) commit(
	batch *[]queuedEnvelope,
	batchBytes *int64,
) error {
	if len(*batch) == 0 {
		return nil
	}
	for _, item := range *batch {
		if item.stamp.Fence().ArchiveEpoch != 0 && !item.stamp.Current() {
			manager.settle(*batch, *batchBytes, recording.Discarded, nil)
			clear(*batch)
			*batch = (*batch)[:0]
			*batchBytes = 0
			return nil
		}
	}
	// Body codecs/driver bindings and chunk manifests use bounded temporary
	// workspace. Reserve it against an existing owner, without a phantom offer.
	scratch := int64(0)
	for _, item := range *batch {
		scratch = max(scratch, int64(8192+min(len(item.record.Body), 64<<10)*4+len(item.record.Body)/8+len(item.record.PayloadMetadata)*2))
	}
	if !(*batch)[0].reservation.TryGrow(scratch) {
		manager.settle(*batch, *batchBytes, recording.Discarded, nil)
		clear(*batch)
		*batch = (*batch)[:0]
		*batchBytes = 0
		return nil
	}
	defer (*batch)[0].reservation.Shrink(scratch)
	sort.Slice(*batch, func(left, right int) bool {
		return (*batch)[left].record.Watermark < (*batch)[right].record.Watermark
	})
	records := make([]StoredEnvelope, len(*batch))
	for index := range *batch {
		records[index] = (*batch)[index].record
	}
	started := time.Now()
	err := manager.repository.AppendBatch(
		manager.workerContext, records, manager.clock.Now().UTC(),
	)
	duration := time.Since(started)
	manager.statsMu.Lock()
	manager.stats.LastFlushDuration = duration
	manager.statsMu.Unlock()
	outcome := recording.Persisted
	if err != nil {
		outcome = recording.Unconfirmed
		if errors.Is(err, recording.ErrObservationDiscarded) {
			outcome = recording.Discarded
		}
	}
	clear(records)
	manager.settle(*batch, *batchBytes, outcome, err)
	clear(*batch)
	*batch = (*batch)[:0]
	*batchBytes = 0
	return err
}

func (manager *Manager) settle(batch []queuedEnvelope, weight int64, outcome recording.Settlement, err error) {
	if len(batch) == 0 {
		return
	}
	manager.statsMu.Lock()
	last := batch[len(batch)-1].record.Watermark
	if outcome == recording.Persisted {
		manager.stats.BatchCommits++
		manager.stats.PersistedRecords += uint64(len(batch))
		manager.stats.HighestPersistedSequence = last
		for _, item := range batch {
			if manager.stats.DurableWatermark+1 == item.record.Watermark {
				manager.stats.DurableWatermark = item.record.Watermark
			}
		}
		manager.stats.LastFailure = ""
	} else if err != nil {
		manager.stats.LastFailure = "append_unconfirmed"
	}
	manager.stats.QueueRecords -= len(batch)
	manager.stats.QueueBytes -= weight
	manager.statsMu.Unlock()
	manager.admissionMu.Lock()
	for _, item := range batch {
		key := scopeKey{kind: item.record.ScopeKind, id: item.record.ScopeID}
		if manager.scopeLatest[key] <= item.record.Watermark {
			delete(manager.scopeLatest, key)
		}
	}
	manager.admissionMu.Unlock()
	for index := range batch {
		retained := uint64(len(batch[index].record.Body) + len(batch[index].record.PayloadMetadata))
		reservation := batch[index].reservation
		batch[index] = queuedEnvelope{}
		manager.budget.RecordSettlement(recording.Raw, outcome, retained)
		reservation.Release()
	}
	manager.statsMu.Lock()
	manager.stats.SettledSequence = max(manager.stats.SettledSequence, last)
	manager.statsMu.Unlock()
}

func (manager *Manager) settledSequence() uint64 {
	manager.statsMu.Lock()
	defer manager.statsMu.Unlock()
	return manager.stats.SettledSequence
}

// ReadPayload returns the stored payload. It needs no key: INV-STORE-DISCLOSED
// forbids application-layer field encryption, and the credential header values
// a payload once carried were removed at the observation boundary. What remains
// is the exchange as it went over the wire, including any secret the client put
// in a body or a query. That is why access is still audited: auditing who read
// evidence was never the same thing as decrypting it.
func (manager *Manager) ReadPayload(
	record StoredEnvelope,
) (Payload, error) {
	if manager == nil || manager.clock == nil {
		return Payload{}, errors.New("raw evidence reader is unavailable")
	}
	// Visibility expires immediately even when physical maintenance is delayed.
	if !record.ExpiresAt.After(manager.clock.Now()) {
		return Payload{}, ErrEnvelopeNotFound
	}
	if record.PayloadState != PayloadCaptured &&
		record.PayloadState != PayloadTruncated {
		return Payload{}, fmt.Errorf(
			"%w: recording policy did not retain it", ErrPayloadUnavailable,
		)
	}
	return DecodePayload(record.PayloadMetadata, record.Body)
}

func randomIdentity(source io.Reader, size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(source, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func headerValueCount(headers map[string][]string) int {
	count := 0
	for _, values := range headers {
		count += len(values)
	}
	return count
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}
