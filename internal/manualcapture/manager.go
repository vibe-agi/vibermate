package manualcapture

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecredential"
	"github.com/vibe-agi/vibermate/internal/evidencearchive"
)

const (
	manualCaptureIDPrefix  = "manual."
	manualCaptureIDBytes   = 20
	createCollisionRetries = 3
	proxyDigestDomain      = "vibermate:manual-capture:proxy:v1:"

	MinimumTemporaryLifetime = time.Minute
	DefaultTemporaryLifetime = 24 * time.Hour
	MaximumTemporaryLifetime = 7 * 24 * time.Hour
)

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

type Options struct {
	Repository           Repository
	Clock                Clock
	Random               io.Reader
	MaxTemporaryLifetime time.Duration
	EvidenceBarrier      EvidenceBarrier
	ArchiveBarrier       evidencearchive.CaptureCreationBarrier
}

// TerminalEvidence owns a prepared ManualCapture evidence boundary.
type TerminalEvidence interface {
	Commit()
	Abort()
}

// EvidenceBarrier drains active requests before explicit revocation commits.
type EvidenceBarrier interface {
	PrepareManualCapture(context.Context, string) (TerminalEvidence, error)
}

func DefaultOptions(repository Repository) Options {
	return Options{
		Repository:           repository,
		Clock:                SystemClock{},
		Random:               rand.Reader,
		MaxTemporaryLifetime: MaximumTemporaryLifetime,
		ArchiveBarrier:       evidencearchive.NewBarrier(),
	}
}

type Manager struct {
	repository           Repository
	clock                Clock
	random               io.Reader
	maxTemporaryLifetime time.Duration
	evidenceBarrier      EvidenceBarrier
	archiveBarrier       evidencearchive.CaptureCreationBarrier
	lifecycle            *lifecycleGate
	recovery             Recovery
}

func NewManager(ctx context.Context, options Options) (*Manager, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: startup context is nil", ErrInvalidCommand)
	}
	if options.Repository == nil || options.Clock == nil || options.Random == nil ||
		options.MaxTemporaryLifetime < MinimumTemporaryLifetime ||
		options.ArchiveBarrier == nil {
		return nil, fmt.Errorf("%w: Manager dependencies are incomplete", ErrInvalidCommand)
	}
	now := canonicalTime(options.Clock.Now())
	recovery, err := options.Repository.Recover(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("recover ManualCaptures: %w", err)
	}
	return &Manager{
		repository:           options.Repository,
		clock:                options.Clock,
		random:               options.Random,
		maxTemporaryLifetime: options.MaxTemporaryLifetime,
		evidenceBarrier:      options.EvidenceBarrier,
		archiveBarrier:       options.ArchiveBarrier,
		lifecycle:            newLifecycleGate(),
		recovery:             recovery,
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
) (Grant, error) {
	if manager == nil {
		return Grant{}, ErrRuntimeStopping
	}
	if err := manager.validateCreate(command); err != nil {
		return Grant{}, err
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return Grant{}, err
	}
	defer finish()
	archiveRelease, err := manager.archiveBarrier.BeginCaptureCreation(operation)
	if err != nil {
		return Grant{}, fmt.Errorf("enter Capture creation archive barrier: %w", err)
	}
	defer archiveRelease()

	for attempt := 0; attempt < createCollisionRetries; attempt++ {
		id, randomErr := newManualCaptureID(manager.random)
		if randomErr != nil {
			return Grant{}, fmt.Errorf("generate ManualCapture ID: %w", randomErr)
		}
		credentialValue, randomErr := randomProxyCredential(manager.random)
		if randomErr != nil {
			return Grant{}, fmt.Errorf("generate ManualCapture credential: %w", randomErr)
		}
		now := manager.now()
		record := DurableRecord{
			ID:                  id,
			Owner:               command.Owner,
			DisplayName:         command.DisplayName,
			ClientClass:         command.ClientClass,
			Lifetime:            command.Lifetime,
			State:               StateActive,
			CredentialRevision:  1,
			ProxyCredentialHash: credentialDigest(credentialValue),
			Observation:         ObservationWaiting,
			CreatedAt:           now,
			UpdatedAt:           now,
		}
		if command.Lifetime == LifetimeTemporary {
			record.ExpiresAt = canonicalTime(now.Add(command.ExpiresIn))
		}
		if err := record.Validate(); err != nil {
			return Grant{}, err
		}
		if err := manager.repository.Create(operation, record); err != nil {
			if errors.Is(err, ErrStateConflict) {
				continue
			}
			return Grant{}, fmt.Errorf("persist ManualCapture: %w", err)
		}
		credential, _ := NewProxyCredential(credentialValue)
		return Grant{Capture: ViewOf(record), Credential: credential}, nil
	}
	return Grant{}, fmt.Errorf("%w: random identity collision", ErrStateConflict)
}

func (manager *Manager) Rotate(
	ctx context.Context,
	command RotateCommand,
) (Grant, error) {
	if manager == nil {
		return Grant{}, ErrRuntimeStopping
	}
	if !command.Owner.Valid() || !command.ID.Valid() ||
		!command.ExpectedCredentialRevision.Valid() {
		return Grant{}, ErrInvalidCommand
	}
	if command.ExpectedCredentialRevision == MaxCredentialRevision {
		return Grant{}, ErrRevisionConflict
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return Grant{}, err
	}
	defer finish()
	credentialValue, err := randomProxyCredential(manager.random)
	if err != nil {
		return Grant{}, fmt.Errorf("generate ManualCapture credential: %w", err)
	}
	record, err := manager.repository.Rotate(
		operation,
		command.Owner,
		command.ID,
		command.ExpectedCredentialRevision,
		credentialDigest(credentialValue),
		manager.now(),
	)
	if err != nil {
		return Grant{}, err
	}
	credential, _ := NewProxyCredential(credentialValue)
	return Grant{Capture: ViewOf(record), Credential: credential}, nil
}

func (manager *Manager) Revoke(
	ctx context.Context,
	command RevokeCommand,
) (View, error) {
	if manager == nil {
		return View{}, ErrRuntimeStopping
	}
	if !command.Owner.Valid() || !command.ID.Valid() ||
		!command.ExpectedCredentialRevision.Valid() {
		return View{}, ErrInvalidCommand
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return View{}, err
	}
	defer finish()
	var terminal TerminalEvidence
	if manager.evidenceBarrier != nil {
		terminal, err = manager.evidenceBarrier.PrepareManualCapture(
			operation,
			command.ID.String(),
		)
		if err != nil {
			return View{}, fmt.Errorf("prepare ManualCapture evidence: %w", err)
		}
	}
	record, err := manager.repository.Revoke(
		operation,
		command.Owner,
		command.ID,
		command.ExpectedCredentialRevision,
		manager.now(),
	)
	if err != nil {
		if terminal != nil {
			terminal.Abort()
		}
		return View{}, err
	}
	if terminal != nil {
		terminal.Commit()
	}
	return ViewOf(record), nil
}

func (manager *Manager) AuthorizeProxy(
	ctx context.Context,
	credential ProxyCredential,
) (Evidence, error) {
	if manager == nil {
		return Evidence{}, ErrCredentialRejected
	}
	if _, err := NewProxyCredential(credential.value); err != nil {
		return Evidence{}, err
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return Evidence{}, err
	}
	defer finish()
	record, err := manager.repository.AuthorizeProxy(
		operation,
		credentialDigest(credential.value),
		manager.now(),
	)
	if err != nil {
		return Evidence{}, err
	}
	if err := record.Validate(); err != nil || record.State != StateActive {
		return Evidence{}, errors.Join(ErrCredentialRejected, err)
	}
	evidence := Evidence{
		ManualCaptureID:    record.ID,
		CredentialRevision: record.CredentialRevision,
		DisplayName:        record.DisplayName,
		Owner:              record.Owner,
	}
	if !evidence.Valid() {
		return Evidence{}, ErrCredentialRejected
	}
	return evidence, nil
}

func (manager *Manager) Get(
	ctx context.Context,
	owner OwnerScope,
	id ID,
) (View, error) {
	if manager == nil {
		return View{}, ErrRuntimeStopping
	}
	if !owner.Valid() || !id.Valid() {
		return View{}, ErrInvalidCommand
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return View{}, err
	}
	defer finish()
	record, err := manager.repository.Get(operation, owner, id, manager.now())
	if err != nil {
		return View{}, err
	}
	return ViewOf(record), nil
}

func (manager *Manager) List(
	ctx context.Context,
	request PageRequest,
) (Page, error) {
	if manager == nil {
		return Page{}, ErrRuntimeStopping
	}
	request = request.Normalized()
	if !request.Owner.Valid() ||
		(request.Cursor != nil && !request.Cursor.Valid()) {
		return Page{}, ErrInvalidCommand
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return Page{}, err
	}
	defer finish()
	records, err := manager.repository.List(
		operation,
		request,
		manager.now(),
	)
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: make([]View, 0, len(records))}
	for _, record := range records {
		page.Items = append(page.Items, ViewOf(record))
	}
	return page, nil
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

// Shutdown closes and drains manager operations but deliberately preserves
// active captures. Their declared lifetime, explicit rotation, or explicit
// revocation—not a daemon generation—owns their validity.
func (manager *Manager) Shutdown(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	manager.BeginShutdown()
	if err := manager.Drain(ctx); err != nil {
		return err
	}
	return nil
}

func (manager *Manager) validateCreate(command CreateCommand) error {
	if !command.Owner.Valid() || !validDisplayName(command.DisplayName) ||
		!command.ClientClass.Valid() || !command.Lifetime.Valid() {
		return ErrInvalidCommand
	}
	switch command.Lifetime {
	case LifetimeTemporary:
		if command.ExpiresIn < MinimumTemporaryLifetime ||
			command.ExpiresIn > manager.maxTemporaryLifetime {
			return ErrInvalidCommand
		}
	case LifetimeUntilRevoked:
		if command.ExpiresIn != 0 {
			return ErrInvalidCommand
		}
	}
	return nil
}

func (manager *Manager) now() time.Time {
	return canonicalTime(manager.clock.Now())
}

func canonicalTime(value time.Time) time.Time {
	return time.UnixMilli(value.UTC().UnixMilli()).UTC()
}

func randomValue(source io.Reader, size int) (string, error) {
	data := make([]byte, size)
	if _, err := io.ReadFull(source, data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func newManualCaptureID(source io.Reader) (ID, error) {
	value, err := randomValue(source, manualCaptureIDBytes)
	if err != nil {
		return ID{}, err
	}
	return ParseID(manualCaptureIDPrefix + value)
}

func randomProxyCredential(source io.Reader) (string, error) {
	entropy := make([]byte, capturecredential.EntropyBytes)
	if _, err := io.ReadFull(source, entropy); err != nil {
		return "", err
	}
	credential, err := capturecredential.New(
		capturecredential.KindManualCapture,
		entropy,
	)
	if err != nil {
		return "", err
	}
	return credential.Value(), nil
}

func credentialDigest(value string) CredentialDigest {
	hash := sha256.New()
	_, _ = hash.Write([]byte(proxyDigestDomain))
	_, _ = hash.Write([]byte(value))
	var digest CredentialDigest
	copy(digest[:], hash.Sum(nil))
	return digest
}
