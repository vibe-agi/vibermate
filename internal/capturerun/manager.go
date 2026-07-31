package capturerun

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"
)

const (
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
}

func DefaultOptions(repository Repository) Options {
	return Options{
		Repository:      repository,
		Clock:           SystemClock{},
		Random:          rand.Reader,
		DefaultLifetime: defaultRunLifetime,
		MaxLifetime:     defaultMaxLifetime,
	}
}

type Manager struct {
	repository      Repository
	clock           Clock
	random          io.Reader
	defaultLifetime time.Duration
	maxLifetime     time.Duration
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
		options.DefaultLifetime > options.MaxLifetime {
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

	runID, err := randomValue(manager.random, runIDRandomBytes)
	if err != nil {
		return LaunchGrant{}, fmt.Errorf("generate CaptureRun ID: %w", err)
	}
	proxyValue, err := randomValue(manager.random, capabilityBytes)
	if err != nil {
		return LaunchGrant{}, fmt.Errorf("generate proxy capability: %w", err)
	}
	controlValue, err := randomValue(manager.random, capabilityBytes)
	if err != nil {
		return LaunchGrant{}, fmt.Errorf("generate control capability: %w", err)
	}
	now := manager.clock.Now().UTC()
	record := DurableRecord{
		ID:                    runID,
		ProxyCapabilityHash:   capabilityDigest(proxyDigestDomain, proxyValue),
		ControlCapabilityHash: capabilityDigest(controlDigestDomain, controlValue),
		CWD:                   command.CWD,
		ExecutableLabel:       filepath.Base(command.ExecutablePath),
		CatalogRevision:       command.CatalogRevision,
		Adapter:               cloneAdapter(command.Adapter),
		State:                 StateCreated,
		CreatedAt:             now,
		ExpiresAt:             now.Add(command.Lifetime),
		UpdatedAt:             now,
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
		Run:               viewOf(record),
		ProxyCapability:   proxy,
		ControlCapability: control,
	}, nil
}

func (manager *Manager) AuthorizeProxy(
	ctx context.Context,
	capability ProxyCapability,
) (Evidence, error) {
	if err := validateCapability(capability.value); err != nil {
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
	return viewOf(record), record.Validate()
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
	return viewOf(record), record.Validate()
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
	return manager.repository.Finish(
		operation,
		runID,
		capabilityDigest(controlDigestDomain, capability.value),
		manager.clock.Now().UTC(),
	)
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

func capabilityDigest(domain, value string) CapabilityDigest {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write([]byte(value))
	var digest CapabilityDigest
	copy(digest[:], hash.Sum(nil))
	return digest
}
