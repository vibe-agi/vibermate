package rawevidence

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"
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
	Repository Repository
	Random     io.Reader
	Clock      Clock
	Config     Config
}

type queuedEnvelope struct {
	record StoredEnvelope
	weight int64
}

type flushRequest struct {
	target uint64
	result chan error
}

type Statistics struct {
	WriterID             string
	AdmittedRecords      uint64
	DurableWatermark     uint64
	BatchCommits         uint64
	QueueRecords         int
	QueueBytes           int64
	LastFlushDuration    time.Duration
	LastFailure          string
	MaximumUnflushedTime time.Duration
}

type Manager struct {
	repository Repository
	clock      Clock
	random     io.Reader
	redactor   Redactor
	config     Config
	writerID   string

	byteSlots   *semaphore.Weighted
	recordSlots *semaphore.Weighted
	records     chan queuedEnvelope
	flushes     chan flushRequest
	stop        chan struct{}
	done        chan struct{}

	workerContext context.Context
	cancelWorker  context.CancelCauseFunc

	admissionMu    sync.Mutex
	accepting      bool
	next           uint64
	scopeLatest    map[scopeKey]uint64
	exchangeLatest map[string]uint64

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
		byteSlots:      semaphore.NewWeighted(options.Config.MaximumQueueBytes),
		recordSlots:    semaphore.NewWeighted(int64(options.Config.MaximumQueueRecords)),
		records:        make(chan queuedEnvelope, options.Config.MaximumQueueRecords),
		flushes:        make(chan flushRequest),
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
		workerContext:  workerContext,
		cancelWorker:   cancelWorker,
		accepting:      true,
		scopeLatest:    make(map[scopeKey]uint64),
		exchangeLatest: make(map[string]uint64),
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
	// Reads from the live workbench must observe every envelope already
	// admitted for this Exchange. The writer normally batches for SSD health;
	// this narrow barrier only flushes when the requested Exchange still has a
	// queued watermark, so repeated polling of durable evidence does not create
	// additional writes.
	manager.admissionMu.Lock()
	sequence := manager.exchangeLatest[exchangeID]
	writerID := manager.writerID
	manager.admissionMu.Unlock()
	if sequence != 0 {
		if err := manager.Flush(ctx, Watermark{
			WriterID: writerID,
			Sequence: sequence,
		}); err != nil {
			return nil, fmt.Errorf("flush Raw evidence Exchange read: %w", err)
		}
		manager.admissionMu.Lock()
		if manager.exchangeLatest[exchangeID] == sequence {
			delete(manager.exchangeLatest, exchangeID)
		}
		manager.admissionMu.Unlock()
	}
	records, err := manager.repository.ListExchange(ctx, exchangeID)
	if err != nil {
		return nil, err
	}
	metadata := make([]EnvelopeMetadata, len(records))
	for index := range records {
		metadata[index] = MetadataOf(records[index])
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

func (manager *Manager) Observe(
	ctx context.Context,
	observation Observation,
) (Watermark, error) {
	if manager == nil || ctx == nil {
		return Watermark{}, errors.New("raw evidence observation is incomplete")
	}
	if err := observation.validate(); err != nil {
		return Watermark{}, err
	}
	if observation.Recording == RecordingOff {
		return Watermark{}, errors.New("raw evidence recording is disabled")
	}
	if err := manager.persistenceFailure(); err != nil {
		return Watermark{}, err
	}
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = manager.clock.Now().UTC()
	} else {
		observation.ObservedAt = observation.ObservedAt.UTC()
	}

	// Credential header values are removed before queue admission, so no
	// credential-bearing header waits in the append queue. Body bytes do wait
	// there in the clear, bounded by the queue's configured byte ceiling.
	draft, weight, err := manager.prepare(ctx, observation)
	if err != nil {
		return Watermark{}, err
	}
	if weight > manager.config.MaximumQueueBytes {
		return Watermark{}, errors.New("raw evidence envelope exceeds queue byte bound")
	}
	if err := manager.byteSlots.Acquire(ctx, weight); err != nil {
		return Watermark{}, fmt.Errorf("wait for raw evidence queue capacity: %w", err)
	}
	if err := manager.recordSlots.Acquire(ctx, 1); err != nil {
		manager.byteSlots.Release(weight)
		return Watermark{}, fmt.Errorf("wait for raw evidence record capacity: %w", err)
	}
	release := true
	defer func() {
		if release {
			manager.recordSlots.Release(1)
			manager.byteSlots.Release(weight)
		}
	}()

	manager.admissionMu.Lock()
	defer manager.admissionMu.Unlock()
	if !manager.accepting {
		return Watermark{}, errors.New("raw evidence writer is closing")
	}
	if err := ctx.Err(); err != nil {
		return Watermark{}, fmt.Errorf("admit raw evidence: %w", err)
	}
	sequence := manager.next + 1
	draft.WriterID = manager.writerID
	draft.Watermark = sequence
	draft.EnvelopeID = fmt.Sprintf("%s.%d", manager.writerID, sequence)
	if err := draft.Validate(); err != nil {
		return Watermark{}, err
	}

	// The acquired record slot covers both the channel and the worker-held
	// batch. Therefore the channel has capacity here. Publish the accounting
	// before making the item visible to the worker so a very fast commit cannot
	// decrement queue statistics before this admission increments them.
	manager.next = sequence
	manager.scopeLatest[scopeKey{
		kind: observation.ScopeKind,
		id:   observation.ScopeID,
	}] = sequence
	manager.exchangeLatest[observation.ExchangeID] = sequence
	manager.statsMu.Lock()
	manager.stats.AdmittedRecords = sequence
	manager.stats.QueueRecords++
	manager.stats.QueueBytes += weight
	manager.statsMu.Unlock()
	release = false
	manager.records <- queuedEnvelope{record: draft, weight: weight}
	return Watermark{WriterID: manager.writerID, Sequence: sequence}, nil
}

// persistenceFailure prevents a failed writer batch from turning the bounded
// queue into a series of request-path timeouts. The worker keeps retrying its
// retained batch on the flush timer; a successful retry clears LastFailure and
// admissions resume automatically.
func (manager *Manager) persistenceFailure() error {
	manager.statsMu.Lock()
	defer manager.statsMu.Unlock()
	if manager.stats.LastFailure == "" {
		return nil
	}
	return fmt.Errorf(
		"raw evidence persistence is degraded: %s",
		manager.stats.LastFailure,
	)
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
	}); err != nil {
		return err
	}

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

// Flush waits until the requested admitted watermark is durable. A zero
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
	// not change what this flush promises to make durable.
	manager.admissionMu.Unlock()
	if target == 0 || manager.durableWatermark() >= target {
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
	manager.scopeMu.Lock()
	manager.scopeAccepting = false
	manager.notifyScopeLocked()
	for manager.activeScopeRequestsLocked() != 0 {
		changed := manager.scopeChanged
		manager.scopeMu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			manager.cancelWorker(ctx.Err())
			manager.shutdownErr = fmt.Errorf(
				"drain raw evidence capture scopes: %w", ctx.Err(),
			)
			close(manager.shutdownDone)
			return
		}
		manager.scopeMu.Lock()
	}
	manager.scopeMu.Unlock()
	manager.admissionMu.Lock()
	manager.accepting = false
	manager.admissionMu.Unlock()
	flushErr := manager.Flush(ctx, Watermark{})
	close(manager.stop)
	select {
	case <-manager.done:
	case <-ctx.Done():
		manager.cancelWorker(ctx.Err())
		<-manager.done
	}
	manager.cancelWorker(errors.New("raw evidence writer stopped"))
	if flushErr == nil {
		flushErr = manager.repository.CloseWriterSession(
			ctx, manager.writerID, manager.clock.Now().UTC(),
		)
	}
	manager.shutdownErr = flushErr
	close(manager.shutdownDone)
}

func (manager *Manager) activeScopeRequestsLocked() uint64 {
	var active uint64
	for _, state := range manager.scopes {
		active += state.active
	}
	return active
}

func (manager *Manager) prepare(
	ctx context.Context,
	observation Observation,
) (StoredEnvelope, int64, error) {
	body := slices.Clone(observation.Body)
	frames := slices.Clone(observation.Frames)
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
		digest = sha256.Sum256(body)
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
			return StoredEnvelope{}, 0, err
		}
		if payloadMetadata, err = built.MarshalMetadata(); err != nil {
			return StoredEnvelope{}, 0, err
		}
		redactedFields = redacted
	}
	record := StoredEnvelope{
		Layer:                    observation.Layer,
		ScopeKind:                observation.ScopeKind,
		ScopeID:                  observation.ScopeID,
		ExchangeID:               observation.ExchangeID,
		ConnectionID:             observation.ConnectionID,
		AttemptID:                observation.AttemptID,
		EnvironmentID:            observation.EnvironmentID,
		EnvironmentRevision:      observation.EnvironmentRevision,
		EnvironmentDigest:        observation.EnvironmentDigest,
		ClientEndpointID:         observation.ClientEndpointID,
		ClientEndpointRevision:   observation.ClientEndpointRevision,
		UpstreamEndpointID:       observation.UpstreamEndpointID,
		UpstreamEndpointRevision: observation.UpstreamEndpointRevision,
		ProtocolPlanID:           observation.ProtocolPlanID,
		ProtocolPlanRevision:     observation.ProtocolPlanRevision,
		RouteID:                  observation.RouteID,
		RouteRevision:            observation.RouteRevision,
		AccountID:                observation.AccountID,
		AccountRevision:          observation.AccountRevision,
		CredentialEpoch:          observation.CredentialEpoch,
		ObservedAt:               observation.ObservedAt,
		ExpiresAt: observation.ObservedAt.AddDate(
			0, 0, int(observation.RetentionDays),
		),
		Method:                   observation.Method,
		StatusCode:               observation.StatusCode,
		Scheme:                   observation.Scheme,
		Authority:                observation.Authority,
		Path:                     observation.Path,
		RawQuery:                 observation.RawQuery,
		ContentType:              observation.ContentType,
		ContentEncoding:          observation.ContentEncoding,
		Representation:           observation.Representation,
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
	weight := int64(len(payloadMetadata) + len(body) + 1024)
	return record, weight, nil
}

func (manager *Manager) run() {
	defer close(manager.done)
	timer := time.NewTimer(manager.config.FlushInterval)
	defer timer.Stop()
	batch := make([]queuedEnvelope, 0, manager.config.MaximumBatchRecords)
	var batchBytes int64
	for {
		select {
		case item := <-manager.records:
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
			if err == nil && manager.durableWatermark() < request.target {
				err = errors.New("raw evidence flush did not reach its watermark")
			}
			request.result <- err
			resetTimer(timer, manager.config.FlushInterval)
		case <-timer.C:
			manager.commit(&batch, &batchBytes)
			timer.Reset(manager.config.FlushInterval)
		case <-manager.stop:
			if manager.drainRecords(&batch, &batchBytes) == nil {
				manager.commit(&batch, &batchBytes)
			}
			return
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
	if err != nil {
		manager.stats.LastFailure = err.Error()
		manager.statsMu.Unlock()
		return err
	}
	manager.stats.BatchCommits++
	manager.stats.DurableWatermark = records[len(records)-1].Watermark
	manager.stats.LastFailure = ""
	manager.stats.QueueRecords -= len(*batch)
	manager.stats.QueueBytes -= *batchBytes
	manager.statsMu.Unlock()
	for _, item := range *batch {
		manager.recordSlots.Release(1)
		manager.byteSlots.Release(item.weight)
	}
	*batch = (*batch)[:0]
	*batchBytes = 0
	return nil
}

func (manager *Manager) durableWatermark() uint64 {
	manager.statsMu.Lock()
	defer manager.statsMu.Unlock()
	return manager.stats.DurableWatermark
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
	if manager == nil {
		return Payload{}, errors.New("raw evidence reader is unavailable")
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
