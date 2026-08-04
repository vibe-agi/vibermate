package proxyclient

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

	"github.com/vibe-agi/vibermate/internal/controlprincipal"
)

const (
	identityEntropyBytes      = 20
	identityCollisionRetries  = 3
	enrollmentDigestDomain    = "vibermate:client-enrollment:v1:"
	controlDigestDomain       = "vibermate:enrolled-control:v1:"
	MinimumEnrollmentLifetime = time.Minute
	DefaultEnrollmentLifetime = 15 * time.Minute
	MaximumEnrollmentLifetime = 24 * time.Hour
)

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

type Options struct {
	Repository            Repository
	Clock                 Clock
	Random                io.Reader
	MaxEnrollmentLifetime time.Duration
}

func DefaultOptions(repository Repository) Options {
	return Options{
		Repository:            repository,
		Clock:                 SystemClock{},
		Random:                rand.Reader,
		MaxEnrollmentLifetime: MaximumEnrollmentLifetime,
	}
}

type Manager struct {
	repository            Repository
	clock                 Clock
	random                io.Reader
	randomMu              sync.Mutex
	maxEnrollmentLifetime time.Duration
	lifecycle             *lifecycleGate
}

func NewManager(options Options) (*Manager, error) {
	if options.Repository == nil || options.Clock == nil || options.Random == nil ||
		options.MaxEnrollmentLifetime < MinimumEnrollmentLifetime {
		return nil, fmt.Errorf("%w: Manager dependencies are incomplete", ErrInvalidCommand)
	}
	return &Manager{
		repository:            options.Repository,
		clock:                 options.Clock,
		random:                options.Random,
		maxEnrollmentLifetime: options.MaxEnrollmentLifetime,
		lifecycle:             newLifecycleGate(),
	}, nil
}

func (manager *Manager) CreateBinding(
	ctx context.Context,
	command CreateBindingCommand,
) (BindingView, error) {
	if manager == nil {
		return BindingView{}, ErrRuntimeStopping
	}
	if !validDisplayName(command.DisplayName) || !command.Policy.Valid() {
		return BindingView{}, ErrInvalidCommand
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return BindingView{}, err
	}
	defer finish()
	for attempt := 0; attempt < identityCollisionRetries; attempt++ {
		idText, err := manager.randomIdentity("binding_")
		if err != nil {
			return BindingView{}, fmt.Errorf("generate ProxyClientBinding ID: %w", err)
		}
		id, _ := ParseBindingID(idText)
		now := manager.now()
		record := BindingRecord{
			ID:          id,
			Revision:    1,
			State:       BindingActive,
			DisplayName: command.DisplayName,
			Policy:      command.Policy.Clone(),
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := manager.repository.CreateBinding(operation, record); err != nil {
			if errors.Is(err, ErrStateConflict) {
				continue
			}
			return BindingView{}, err
		}
		return BindingViewOf(record), nil
	}
	return BindingView{}, fmt.Errorf("%w: random binding identity collision", ErrStateConflict)
}

func (manager *Manager) CreateEnrollment(
	ctx context.Context,
	command CreateEnrollmentCommand,
) (EnrollmentGrant, error) {
	if manager == nil {
		return EnrollmentGrant{}, ErrRuntimeStopping
	}
	if !command.BindingID.Valid() || !command.ExpectedBindingRevision.Valid() ||
		command.ExpiresIn < MinimumEnrollmentLifetime ||
		command.ExpiresIn > manager.maxEnrollmentLifetime {
		return EnrollmentGrant{}, ErrInvalidCommand
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return EnrollmentGrant{}, err
	}
	defer finish()
	for attempt := 0; attempt < identityCollisionRetries; attempt++ {
		idText, err := manager.randomIdentity("enrollment_")
		if err != nil {
			return EnrollmentGrant{}, fmt.Errorf("generate client enrollment ID: %w", err)
		}
		id, _ := ParseEnrollmentID(idText)
		credential, err := manager.randomEnrollmentCredential()
		if err != nil {
			return EnrollmentGrant{}, err
		}
		now := manager.now()
		record := EnrollmentRecord{
			ID:               id,
			BindingID:        command.BindingID,
			BindingRevision:  command.ExpectedBindingRevision,
			State:            EnrollmentActive,
			CredentialDigest: enrollmentDigest(credential.value),
			CreatedAt:        now,
			ExpiresAt:        canonicalTime(now.Add(command.ExpiresIn)),
			UpdatedAt:        now,
		}
		if err := manager.repository.CreateEnrollment(operation, record); err != nil {
			if errors.Is(err, ErrStateConflict) {
				continue
			}
			return EnrollmentGrant{}, err
		}
		return EnrollmentGrant{
			Enrollment: EnrollmentViewOf(record),
			Credential: credential,
		}, nil
	}
	return EnrollmentGrant{}, fmt.Errorf(
		"%w: random enrollment identity collision",
		ErrStateConflict,
	)
}

func (manager *Manager) CompleteEnrollment(
	ctx context.Context,
	command CompleteEnrollmentCommand,
) (CompletionGrant, error) {
	if manager == nil {
		return CompletionGrant{}, ErrRuntimeStopping
	}
	if !command.EnrollmentID.Valid() ||
		!validCredential(command.Credential.value, "enroll_") ||
		!command.MachineID.Valid() || !validDisplayName(command.DisplayName) {
		return CompletionGrant{}, ErrInvalidCommand
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return CompletionGrant{}, err
	}
	defer finish()
	for attempt := 0; attempt < identityCollisionRetries; attempt++ {
		machineIDText, err := manager.randomIdentity("machine_")
		if err != nil {
			return CompletionGrant{}, fmt.Errorf("generate MachineRegistration ID: %w", err)
		}
		principalIDText, err := manager.randomIdentity("principal_")
		if err != nil {
			return CompletionGrant{}, fmt.Errorf("generate ControlPrincipal ID: %w", err)
		}
		machineRegistrationID, _ := ParseMachineRegistrationID(machineIDText)
		principalID, _ := ParsePrincipalID(principalIDText)
		controlCredential, err := manager.randomControlCredential()
		if err != nil {
			return CompletionGrant{}, err
		}
		now := manager.now()
		candidate := CompletionCandidate{
			EnrollmentID:     command.EnrollmentID,
			EnrollmentDigest: enrollmentDigest(command.Credential.value),
			Machine: MachineRecord{
				ID:          machineRegistrationID,
				MachineID:   command.MachineID,
				Revision:    1,
				State:       MachineActive,
				DisplayName: command.DisplayName,
				CreatedAt:   now,
				UpdatedAt:   now,
			},
			Principal: PrincipalRecord{
				ID:                    principalID,
				MachineRegistrationID: machineRegistrationID,
				CredentialRevision:    1,
				CredentialDigest:      controlDigest(controlCredential.value),
				State:                 PrincipalActive,
				CreatedAt:             now,
				UpdatedAt:             now,
			},
			CompletedAt: now,
		}
		result, err := manager.repository.CompleteEnrollment(operation, candidate)
		if err != nil {
			if errors.Is(err, ErrStateConflict) {
				continue
			}
			return CompletionGrant{}, err
		}
		if result.Outcome != CompletionCommitted || result.Record.Validate() != nil {
			return CompletionGrant{}, ErrCommitIndeterminate
		}
		principal, err := principalOf(result.Record)
		if err != nil {
			return CompletionGrant{}, err
		}
		return CompletionGrant{
			Machine:    MachineViewOf(result.Record.Machine),
			Principal:  principal,
			Credential: controlCredential,
		}, nil
	}
	return CompletionGrant{}, fmt.Errorf(
		"%w: random enrolled identity collision",
		ErrStateConflict,
	)
}

func (manager *Manager) Authenticate(
	ctx context.Context,
	credential ControlCredential,
) (controlprincipal.Principal, error) {
	if manager == nil || !validCredential(credential.value, "control_") {
		return controlprincipal.Principal{}, ErrControlRejected
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return controlprincipal.Principal{}, err
	}
	defer finish()
	record, err := manager.repository.Authenticate(
		operation,
		controlDigest(credential.value),
	)
	if err != nil {
		return controlprincipal.Principal{}, err
	}
	return principalOf(record)
}

func (manager *Manager) RevokeBinding(
	ctx context.Context,
	command RevokeBindingCommand,
) (BindingView, error) {
	if manager == nil {
		return BindingView{}, ErrRuntimeStopping
	}
	if !command.BindingID.Valid() || !command.ExpectedRevision.Valid() {
		return BindingView{}, ErrInvalidCommand
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return BindingView{}, err
	}
	defer finish()
	record, err := manager.repository.RevokeBinding(
		operation,
		command.BindingID,
		command.ExpectedRevision,
		manager.now(),
	)
	if err != nil {
		return BindingView{}, err
	}
	return BindingViewOf(record), nil
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

func (manager *Manager) Shutdown(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	manager.BeginShutdown()
	return manager.Drain(ctx)
}

func principalOf(record AuthenticationRecord) (controlprincipal.Principal, error) {
	if err := record.Validate(); err != nil {
		return controlprincipal.Principal{}, errors.Join(ErrControlRejected, err)
	}
	return controlprincipal.New(controlprincipal.Attributes{
		ID:                    record.Principal.ID.String(),
		Kind:                  controlprincipal.KindEnrolledClient,
		ProxyClientBindingID:  record.Binding.ID.String(),
		MachineRegistrationID: record.Machine.ID.String(),
		CredentialRevision: controlprincipal.CredentialRevision(
			record.Principal.CredentialRevision,
		),
		AllowedGrantKinds: record.Principal.AllowedGrantKinds,
	})
}

func (manager *Manager) randomIdentity(prefix string) (string, error) {
	bytes := make([]byte, identityEntropyBytes)
	if err := manager.readRandom(bytes); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(bytes), nil
}

func (manager *Manager) randomEnrollmentCredential() (EnrollmentCredential, error) {
	value, err := manager.randomCredential("enroll_")
	if err != nil {
		return EnrollmentCredential{}, fmt.Errorf("generate enrollment credential: %w", err)
	}
	return EnrollmentCredential{value: value}, nil
}

func (manager *Manager) randomControlCredential() (ControlCredential, error) {
	value, err := manager.randomCredential("control_")
	if err != nil {
		return ControlCredential{}, fmt.Errorf("generate control credential: %w", err)
	}
	return ControlCredential{value: value}, nil
}

func (manager *Manager) randomCredential(prefix string) (string, error) {
	bytes := make([]byte, CredentialBytes)
	if err := manager.readRandom(bytes); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(bytes), nil
}

func (manager *Manager) readRandom(destination []byte) error {
	manager.randomMu.Lock()
	defer manager.randomMu.Unlock()
	_, err := io.ReadFull(manager.random, destination)
	return err
}

func (manager *Manager) now() time.Time { return canonicalTime(manager.clock.Now()) }

func canonicalTime(value time.Time) time.Time { return value.UTC().Truncate(time.Millisecond) }

func enrollmentDigest(value string) EnrollmentDigest {
	return sha256.Sum256([]byte(enrollmentDigestDomain + value))
}

func controlDigest(value string) ControlDigest {
	return sha256.Sum256([]byte(controlDigestDomain + value))
}
