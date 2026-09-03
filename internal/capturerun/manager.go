package capturerun

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecredential"
	"github.com/vibe-agi/vibermate/internal/evidencearchive"
)

const (
	runIDPrefix         = "run."
	runIDRandomBytes    = 20
	capabilityBytes     = 32
	defaultRunLifetime  = 2 * time.Minute
	defaultMaxLifetime  = 10 * time.Minute
	proxyDigestDomain   = "vibermate:capture-run:proxy:v1:"
	controlDigestDomain = "vibermate:capture-run:control:v1:"
)

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

type Options struct {
	Repository      Repository
	Clock           Clock
	Random          io.Reader
	DefaultLifetime time.Duration
	MaxLifetime     time.Duration
	EvidenceBarrier EvidenceBarrier
	ArchiveBarrier  evidencearchive.CaptureCreationBarrier
}

// TerminalEvidence owns the prepared CaptureRun evidence boundary. Commit is
// called only after the CaptureRun terminal state is durable; Abort reopens
// request admission when that mutation fails.
type TerminalEvidence interface {
	Commit()
	Abort()
}

// EvidenceBarrier drains requests and makes a terminal CaptureRun state a
// durability boundary for every observation admitted under that run.
type EvidenceBarrier interface {
	PrepareManagedRun(context.Context, string) (TerminalEvidence, error)
}

func DefaultOptions(repository Repository) Options {
	return Options{
		Repository:      repository,
		Clock:           SystemClock{},
		Random:          rand.Reader,
		DefaultLifetime: defaultRunLifetime,
		MaxLifetime:     defaultMaxLifetime,
		ArchiveBarrier:  evidencearchive.NewBarrier(),
	}
}

type Manager struct {
	repository      Repository
	clock           Clock
	random          io.Reader
	defaultLifetime time.Duration
	maxLifetime     time.Duration
	evidenceBarrier EvidenceBarrier
	archiveBarrier  evidencearchive.CaptureCreationBarrier
	lifecycle       *lifecycleGate
	recovery        Recovery

	shutdownMu      sync.Mutex
	activeRevoked   bool
	activeRevokeErr error
}

func NewManager(ctx context.Context, options Options) (*Manager, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: startup context is nil", ErrInvalidRequest)
	}
	if options.Repository == nil ||
		options.Clock == nil ||
		options.Random == nil ||
		options.DefaultLifetime <= 0 ||
		options.MaxLifetime <= 0 ||
		options.DefaultLifetime > options.MaxLifetime ||
		options.ArchiveBarrier == nil {
		return nil, fmt.Errorf("%w: Manager dependencies are incomplete", ErrInvalidRequest)
	}
	now := options.Clock.Now().UTC()
	recovery, err := options.Repository.Recover(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("recover CaptureRuns: %w", err)
	}
	return &Manager{
		repository:      options.Repository,
		clock:           options.Clock,
		random:          options.Random,
		defaultLifetime: options.DefaultLifetime,
		maxLifetime:     options.MaxLifetime,
		evidenceBarrier: options.EvidenceBarrier,
		archiveBarrier:  options.ArchiveBarrier,
		lifecycle:       newLifecycleGate(),
		recovery:        recovery,
	}, nil
}

func (manager *Manager) Recovery() Recovery {
	if manager == nil {
		return Recovery{}
	}
	return manager.recovery
}

func (manager *Manager) ActiveCount(ctx context.Context) (int, error) {
	if manager == nil {
		return 0, ErrRuntimeStopping
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer finish()
	recovery, err := manager.repository.Recover(operation, manager.clock.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("reconcile active CaptureRuns: %w", err)
	}
	return recovery.ActiveCount, nil
}

func (manager *Manager) Create(
	ctx context.Context,
	command CreateCommand,
) (LaunchGrant, error) {
	if command.Lifetime == 0 {
		command.Lifetime = manager.defaultLifetime
	}
	command.Adapter = cloneAdapter(command.Adapter)
	if err := command.validate(manager.maxLifetime); err != nil {
		return LaunchGrant{}, err
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return LaunchGrant{}, err
	}
	defer finish()
	archiveRelease, err := manager.archiveBarrier.BeginCaptureCreation(operation)
	if err != nil {
		return LaunchGrant{}, fmt.Errorf("enter Capture creation archive barrier: %w", err)
	}
	defer archiveRelease()

	runID, err := newRunID(manager.random)
	if err != nil {
		return LaunchGrant{}, fmt.Errorf("generate CaptureRun ID: %w", err)
	}
	proxyValue, err := randomProxyCapability(manager.random)
	if err != nil {
		return LaunchGrant{}, fmt.Errorf("generate proxy capability: %w", err)
	}
	controlValue, err := randomValue(manager.random, capabilityBytes)
	if err != nil {
		return LaunchGrant{}, fmt.Errorf("generate control capability: %w", err)
	}
	now := manager.clock.Now().UTC()
	record := DurableRecord{
		ID:                          runID,
		ProxyCapabilityHash:         capabilityDigest(proxyDigestDomain, proxyValue),
		ControlCapabilityHash:       capabilityDigest(controlDigestDomain, controlValue),
		CWD:                         command.CWD,
		CanonicalExecutablePath:     command.CanonicalExecutablePath,
		Runtime:                     command.Runtime,
		RuntimeUserID:               command.RuntimeUserID,
		RuntimeUsername:             command.RuntimeUsername,
		LoginSessionID:              command.LoginSessionID,
		DeviceName:                  command.DeviceName,
		ExecutableLabel:             command.ExecutableLabel,
		CatalogRevision:             command.CatalogRevision,
		Adapter:                     cloneAdapter(command.Adapter),
		Recognition:                 NormalizedRecognition(command.Recognition),
		MachineID:                   command.Workspace.MachineID(),
		MachineRegistrationRevision: command.Workspace.RegistrationRevision(),
		WorkspaceID:                 command.Workspace.WorkspaceID(),
		WorkspaceLabel:              command.Workspace.WorkspaceLabel(),
		WorkspaceEvidence:           command.Workspace.Evidence(),
		WorkspaceDerivationRevision: command.Workspace.DerivationRevision(),
		State:                       StateCreated,
		// A run is waiting until authenticated traffic actually arrives.
		Observation: ObservationWaitingForTraffic,
		CreatedAt:   now,
		ExpiresAt:   now.Add(command.Lifetime),
		UpdatedAt:   now,
	}
	if err := record.Validate(); err != nil {
		return LaunchGrant{}, err
	}
	if err := manager.repository.Create(operation, record); err != nil {
		return LaunchGrant{}, fmt.Errorf("persist CaptureRun: %w", err)
	}
	proxy, _ := NewProxyCapability(proxyValue)
	control, _ := NewControlCapability(controlValue)
	return LaunchGrant{
		Run:               ViewOf(record),
		ProxyCapability:   proxy,
		ControlCapability: control,
	}, nil
}

func (manager *Manager) AuthorizeProxy(
	ctx context.Context,
	capability ProxyCapability,
) (Evidence, error) {
	if _, err := NewProxyCapability(capability.value); err != nil {
		return Evidence{}, err
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return Evidence{}, err
	}
	defer finish()
	record, err := manager.repository.AuthorizeProxy(
		operation,
		capabilityDigest(proxyDigestDomain, capability.value),
		manager.clock.Now().UTC(),
	)
	if err != nil {
		return Evidence{}, err
	}
	if err := record.Validate(); err != nil || !record.State.active() {
		return Evidence{}, errors.Join(ErrCapabilityRejected, err)
	}
	return evidenceOf(record), nil
}

func (manager *Manager) Attach(
	ctx context.Context,
	runID string,
	capability ControlCapability,
	processID int,
) (View, error) {
	if err := validateID(runID); err != nil {
		return View{}, err
	}
	if err := validateCapability(capability.value); err != nil {
		return View{}, err
	}
	if processID <= 0 {
		return View{}, fmt.Errorf("%w: process ID must be positive", ErrInvalidRequest)
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return View{}, err
	}
	defer finish()
	record, err := manager.repository.Attach(
		operation,
		runID,
		capabilityDigest(controlDigestDomain, capability.value),
		processID,
		manager.clock.Now().UTC(),
	)
	if err != nil {
		return View{}, err
	}
	return ViewOf(record), record.Validate()
}

func (manager *Manager) Heartbeat(
	ctx context.Context,
	runID string,
	capability ControlCapability,
	lifetime time.Duration,
) (View, error) {
	if err := validateID(runID); err != nil {
		return View{}, err
	}
	if err := validateCapability(capability.value); err != nil {
		return View{}, err
	}
	if lifetime == 0 {
		lifetime = manager.defaultLifetime
	}
	if lifetime <= 0 || lifetime > manager.maxLifetime {
		return View{}, fmt.Errorf("%w: heartbeat lifetime is invalid", ErrInvalidRequest)
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return View{}, err
	}
	defer finish()
	now := manager.clock.Now().UTC()
	record, err := manager.repository.Heartbeat(
		operation,
		runID,
		capabilityDigest(controlDigestDomain, capability.value),
		now,
		now.Add(lifetime),
	)
	if err != nil {
		return View{}, err
	}
	return ViewOf(record), record.Validate()
}

func (manager *Manager) Finish(
	ctx context.Context,
	runID string,
	capability ControlCapability,
) error {
	if err := validateID(runID); err != nil {
		return err
	}
	if err := validateCapability(capability.value); err != nil {
		return err
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	var terminal TerminalEvidence
	if manager.evidenceBarrier != nil {
		terminal, err = manager.evidenceBarrier.PrepareManagedRun(operation, runID)
		if err != nil {
			return fmt.Errorf("prepare CaptureRun evidence: %w", err)
		}
	}
	err = manager.repository.Finish(
		operation,
		runID,
		capabilityDigest(controlDigestDomain, capability.value),
		manager.clock.Now().UTC(),
	)
	if err != nil {
		if terminal != nil {
			terminal.Abort()
		}
		return err
	}
	if terminal != nil {
		terminal.Commit()
	}
	return nil
}

func (manager *Manager) BeginShutdown() {
	if manager != nil {
		manager.lifecycle.closeAdmission()
	}
}

func (manager *Manager) Drain(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	return manager.lifecycle.drain(ctx)
}

// Shutdown is retryable after a deadline: admission stays closed, and a later
// call can finish drain and durable revocation before SQLite closes.
func (manager *Manager) Shutdown(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	manager.BeginShutdown()
	if err := manager.Drain(ctx); err != nil {
		return err
	}
	manager.shutdownMu.Lock()
	defer manager.shutdownMu.Unlock()
	if manager.activeRevoked {
		return manager.activeRevokeErr
	}
	_, err := manager.repository.RevokeActive(ctx, manager.clock.Now().UTC())
	if err == nil {
		manager.activeRevoked = true
	}
	manager.activeRevokeErr = err
	return err
}

func randomValue(source io.Reader, size int) (string, error) {
	data := make([]byte, size)
	if _, err := io.ReadFull(source, data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func newRunID(source io.Reader) (string, error) {
	value, err := randomValue(source, runIDRandomBytes)
	if err != nil {
		return "", err
	}
	value = runIDPrefix + value
	if err := validateID(value); err != nil {
		return "", err
	}
	return value, nil
}

func randomProxyCapability(source io.Reader) (string, error) {
	entropy := make([]byte, capturecredential.EntropyBytes)
	if _, err := io.ReadFull(source, entropy); err != nil {
		return "", err
	}
	credential, err := capturecredential.New(
		capturecredential.KindManagedRun,
		entropy,
	)
	if err != nil {
		return "", err
	}
	return credential.Value(), nil
}

func capabilityDigest(domain, value string) CapabilityDigest {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write([]byte(value))
	var digest CapabilityDigest
	copy(digest[:], hash.Sum(nil))
	return digest
}

// ListRuns is the read a control API exposes. It returns what a run is and
// whether anything was seen through it, and never a capability.
func (manager *Manager) ListRuns(
	ctx context.Context,
	request PageRequest,
) (Page, error) {
	if manager == nil {
		return Page{}, errors.New("CaptureRun manager is nil")
	}
	if request.Cursor != nil && !request.Cursor.Valid() {
		return Page{}, ErrInvalidRequest
	}
	// Heartbeats are the durable liveness authority. Recovery previously ran
	// only when the daemon started, which allowed a launcher that disappeared
	// between restarts to remain in the operator's Running list indefinitely.
	// Reconcile expired leases immediately before the human-facing projection so
	// every refresh converges without inventing process-liveness heuristics.
	if _, err := manager.repository.Recover(ctx, manager.clock.Now().UTC()); err != nil {
		return Page{}, fmt.Errorf("recover expired CaptureRuns before list: %w", err)
	}
	return manager.repository.List(ctx, request)
}

// GetRun returns one redacted run without accepting or returning a capability.
func (manager *Manager) GetRun(ctx context.Context, runID string) (View, error) {
	if manager == nil || validateID(runID) != nil {
		return View{}, ErrInvalidRequest
	}
	return manager.repository.Get(ctx, runID)
}
