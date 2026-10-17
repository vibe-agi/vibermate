package connectionevent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/recording"
)

const connectionIDBytes = 20

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

type Options struct {
	Reader   Reader
	Observer Observer
	Scopes   recording.ScopeIssuer
	Clock    Clock
	Random   io.Reader
}

func DefaultOptions(reader Reader) Options {
	observer, _ := reader.(Observer)
	scopes, _ := reader.(recording.ScopeIssuer)
	return Options{Reader: reader, Observer: observer, Scopes: scopes, Clock: SystemClock{}, Random: rand.Reader}
}

type Attempt struct {
	Source        Source
	RequestedHost string
	Port          uint16
}

type DecisionEvidence struct {
	InspectionPurpose      InspectionPurpose
	Source                 Source
	Decision               Decision
	RuleID                 string
	RouteHost              string
	EnvironmentID          environment.EnvironmentID
	EnvironmentName        string
	EnvironmentRevision    environment.Revision
	ClientEndpointID       environment.ClientEndpointID
	ClientEndpointRevision environment.Revision
	CredentialBindingID    string
	EgressScope            EgressScope
	EgressSource           EgressSource
	EgressRuleID           string
	EgressSelectorRunID    string
	EgressProxyID          string
	EgressPolicyRevision   uint64
	Decryption             Decryption
	ErrorClass             string
}

type ConnectedEvidence struct {
	ObservedSNI         string
	RouteHost           string
	IP                  string
	CredentialBindingID string
}

type TerminalEvidence struct {
	Outcome    Outcome
	ErrorClass string
	BytesUp    uint64
	BytesDown  uint64
}

type Runtime interface {
	Start(context.Context, Attempt) (*Connection, error)
	Reader
	Shutdown(context.Context) error
}

type Manager struct {
	reader   Reader
	observer Observer
	scopes   recording.ScopeIssuer
	clock    Clock
	random   io.Reader

	mu       sync.Mutex
	randomMu sync.Mutex
	closing  bool
	active   int
	changed  chan struct{}
}

func New(ctx context.Context, options Options) (*Manager, error) {
	if ctx == nil {
		return nil, errors.New("ConnectionEvent recovery context is nil")
	}
	if options.Reader == nil ||
		options.Clock == nil ||
		options.Random == nil {
		return nil, errors.New("ConnectionEvent dependencies are incomplete")
	}
	manager := &Manager{
		reader: options.Reader, observer: options.Observer, scopes: options.Scopes,
		clock:   options.Clock,
		random:  options.Random,
		changed: make(chan struct{}),
	}
	return manager, nil
}

func (manager *Manager) Start(
	ctx context.Context,
	attempt Attempt,
) (*Connection, error) {
	_, finish, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	var global recording.Stamp
	if manager.scopes != nil {
		global = manager.scopes.ScopeStamp(captureidentity.Reference{})
	}
	identifier := make([]byte, connectionIDBytes)
	manager.randomMu.Lock()
	_, randomErr := io.ReadFull(manager.random, identifier)
	manager.randomMu.Unlock()
	if randomErr != nil {
		return nil, fmt.Errorf("generate ConnectionEvent ID: %w", randomErr)
	}
	event := Event{
		ConnectionID:     base64.RawURLEncoding.EncodeToString(identifier),
		IngressID:        attempt.Source.IngressID,
		SourceLabel:      attempt.Source.Label,
		SourceConfidence: attempt.Source.Confidence,
		RequestedHost:    attempt.RequestedHost,
		Port:             attempt.Port,
		Decryption:       DecryptionNone,
		Phase:            PhaseAttempted,
		StartedAt:        manager.clock.Now().UTC(),
	}
	return &Connection{manager: manager, event: event, attempted: event, global: global}, nil
}

func (manager *Manager) List(
	ctx context.Context,
	request PageRequest,
) (Page, error) {
	if err := request.Validate(); err != nil {
		return Page{}, err
	}
	operation, finish, err := manager.begin(ctx)
	if err != nil {
		return Page{}, err
	}
	defer finish()
	page, err := manager.reader.List(operation, request)
	if err != nil {
		return Page{}, err
	}
	page.Items = append([]Record{}, page.Items...)
	return page, nil
}

func (manager *Manager) Timeline(
	ctx context.Context,
	connectionID string,
) (Timeline, error) {
	if err := validateIdentity("connection ID", connectionID, false); err != nil {
		return Timeline{}, err
	}
	operation, finish, err := manager.begin(ctx)
	if err != nil {
		return Timeline{}, err
	}
	defer finish()
	timeline, err := manager.reader.Timeline(operation, connectionID)
	if err != nil {
		return Timeline{}, err
	}
	timeline.Events = append([]Record{}, timeline.Events...)
	return timeline, nil
}

func (manager *Manager) Shutdown(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("ConnectionEvent shutdown context is nil")
	}
	manager.mu.Lock()
	if !manager.closing {
		manager.closing = true
		manager.notifyLocked()
	}
	for manager.active != 0 {
		changed := manager.changed
		manager.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
		manager.mu.Lock()
	}
	manager.mu.Unlock()
	return nil
}

func (manager *Manager) begin(
	ctx context.Context,
) (context.Context, func(), error) {
	if manager == nil || ctx == nil {
		return nil, nil, ErrInvalidEvent
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	manager.mu.Lock()
	if manager.closing {
		manager.mu.Unlock()
		return nil, nil, ErrRuntimeStopping
	}
	manager.active++
	manager.notifyLocked()
	manager.mu.Unlock()
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			manager.mu.Lock()
			manager.active--
			manager.notifyLocked()
			manager.mu.Unlock()
		})
	}, nil
}

func (manager *Manager) notifyLocked() {
	close(manager.changed)
	manager.changed = make(chan struct{})
}

type Connection struct {
	manager *Manager

	mu        sync.Mutex
	event     Event
	attempted Event
	stamp     recording.Stamp
	global    recording.Stamp
	bound     bool
	terminal  bool
}

func (connection *Connection) ID() string {
	if connection == nil {
		return ""
	}
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return connection.event.ConnectionID
}

func (connection *Connection) Decide(
	ctx context.Context,
	evidence DecisionEvidence,
) error {
	if connection == nil || connection.manager == nil {
		return ErrInvalidEvent
	}
	switch evidence.Decision {
	case DecisionAllow, DecisionDeny, DecisionAsk:
	default:
		return ErrInvalidEvent
	}
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.terminal ||
		(connection.event.Phase != PhaseAttempted &&
			connection.event.Phase != PhaseAsked) {
		return ErrInvalidPhase
	}
	candidate := connection.event
	candidate.IngressID = evidence.Source.IngressID
	candidate.SourceLabel = evidence.Source.Label
	candidate.SourceConfidence = evidence.Source.Confidence
	candidate.Decision = evidence.Decision
	candidate.RuleID = evidence.RuleID
	candidate.RouteHost = evidence.RouteHost
	candidate.EnvironmentID = evidence.EnvironmentID
	candidate.EnvironmentName = evidence.EnvironmentName
	candidate.EnvironmentRevision = evidence.EnvironmentRevision
	candidate.ClientEndpointID = evidence.ClientEndpointID
	candidate.ClientEndpointRevision = evidence.ClientEndpointRevision
	candidate.InspectionPurpose = evidence.InspectionPurpose
	candidate.CredentialBindingID = evidence.CredentialBindingID
	candidate.EgressScope = evidence.EgressScope
	candidate.EgressSource = evidence.EgressSource
	candidate.EgressRuleID = evidence.EgressRuleID
	candidate.EgressSelectorRunID = evidence.EgressSelectorRunID
	candidate.EgressProxyID = evidence.EgressProxyID
	candidate.EgressPolicyRevision = evidence.EgressPolicyRevision
	candidate.Decryption = evidence.Decryption
	candidate.Phase = PhaseDecided
	if evidence.Decision == DecisionAsk {
		candidate.Phase = PhaseAsked
	}
	if evidence.Decision == DecisionDeny {
		candidate.EndedAt = connection.manager.clock.Now().UTC()
		candidate.Outcome = OutcomeDenied
		candidate.ErrorClass = evidence.ErrorClass
	}
	if err := connection.appendLocked(ctx, candidate); err != nil {
		return err
	}
	if evidence.Decision == DecisionDeny {
		connection.terminal = true
	}
	return nil
}

func (connection *Connection) Connected(
	ctx context.Context,
	evidence ConnectedEvidence,
) error {
	if connection == nil || connection.manager == nil {
		return ErrInvalidEvent
	}
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.terminal ||
		connection.event.Decision != DecisionAllow ||
		(connection.event.Phase != PhaseDecided &&
			connection.event.Phase != PhaseConnected) {
		return ErrInvalidPhase
	}
	candidate := connection.event
	if evidence.ObservedSNI != "" {
		candidate.ObservedSNI = evidence.ObservedSNI
	}
	if evidence.RouteHost != "" {
		candidate.RouteHost = evidence.RouteHost
	}
	if evidence.IP != "" {
		candidate.IP = evidence.IP
	}
	if evidence.CredentialBindingID != "" {
		candidate.CredentialBindingID = evidence.CredentialBindingID
	}
	candidate.Phase = PhaseConnected
	return connection.appendLocked(ctx, candidate)
}

func (connection *Connection) Finish(
	ctx context.Context,
	evidence TerminalEvidence,
) error {
	if connection == nil || connection.manager == nil {
		return ErrInvalidEvent
	}
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.terminal {
		return nil
	}
	candidate := connection.event
	candidate.BytesUp = evidence.BytesUp
	candidate.BytesDown = evidence.BytesDown
	candidate.EndedAt = connection.manager.clock.Now().UTC()
	candidate.Outcome = evidence.Outcome
	candidate.ErrorClass = evidence.ErrorClass
	switch evidence.Outcome {
	case OutcomeCompleted, OutcomeCanceled:
		candidate.Phase = PhaseClosed
	case OutcomeFailed:
		candidate.Phase = PhaseFailed
	default:
		return ErrInvalidEvent
	}
	if err := connection.appendLocked(ctx, candidate); err != nil {
		return err
	}
	connection.terminal = true
	return nil
}

func (connection *Connection) appendLocked(
	ctx context.Context,
	candidate Event,
) error {
	_, finish, err := connection.manager.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	connection.event = candidate
	if connection.manager.observer != nil {
		if connection.bound {
			connection.manager.observer.ObserveConnection(connection.stamp, candidate)
		} else if !candidate.EndedAt.IsZero() && connection.manager.scopes != nil {
			connection.manager.observer.ObserveConnection(connection.global, candidate)
		}
	}
	return nil
}

var _ Runtime = (*Manager)(nil)

// ObservationScope supplies coordinates only after the caller has established
// trusted Capture admission. The stamp itself conveys no authority.
func (c *Connection) ObservationScope(capture captureidentity.Reference) recording.Stamp {
	if c == nil || c.manager == nil || c.manager.scopes == nil {
		return recording.Stamp{}
	}
	return c.manager.scopes.ScopeStamp(capture)
}
func (c *Connection) BindObservationScope(stamp recording.Stamp) error {
	if c == nil || !stamp.Current() || !stamp.HasCapture() {
		return ErrInvalidEvent
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bound {
		if c.stamp.SameScope(stamp) {
			return nil
		}
		return ErrInvalidEvent
	}
	if c.terminal {
		return ErrInvalidPhase
	}
	c.bound = true
	c.stamp = stamp
	if c.manager.observer != nil {
		c.manager.observer.ObserveConnection(stamp, c.attempted)
	}
	c.attempted = Event{}
	c.global = recording.Stamp{}
	return nil
}
func (c *Connection) ObservationStamp() recording.Stamp {
	if c == nil {
		return recording.Stamp{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stamp
}
