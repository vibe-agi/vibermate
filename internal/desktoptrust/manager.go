// Package desktoptrust is the Desktop Host adapter for the platform-neutral
// systemtrust plan. It owns the operating-system process boundary; the
// ProductRuntime and remote Server never depend on it.
package desktoptrust

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/certidentity"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/systemtrust"
)

const (
	defaultCommandTimeout        = systemtrust.DefaultCommandTimeout
	defaultReconciliationTimeout = systemtrust.DefaultReconciliationTimeout
	productionCommandTimeout     = 2 * time.Minute
	productionObservationTimeout = 8 * time.Second
	productionReconcileTimeout   = 10 * time.Second
)

// RootProvider is the narrow public Root projection exposed by ProductRuntime.
// It contains no private key and no authority mutation method.
type RootProvider interface {
	LocalRootIdentity() localca.RootIdentity
	LocalRootCertificate() localca.RootCertificate
}

type Options struct {
	OwnerContext          context.Context
	Root                  RootProvider
	Executor              systemtrust.CommandExecutor
	Clock                 localca.Clock
	CommandTimeout        time.Duration
	ObservationTimeout    time.Duration
	ReconciliationTimeout time.Duration
	ResetRequest          func(context.Context, localca.RootIdentity) error
	ReplaceAdmission      func(context.Context) (func(), error)
}

// Status is the user-safe projection of one current Root and the exact macOS
// trust observation. It contains no certificate bytes, path, command output,
// or private material.
type Status struct {
	RootRevision       uint64
	Fingerprint        string
	Algorithm          string
	NotBefore          time.Time
	NotAfter           time.Time
	RootValid          bool
	CertificatePresent systemtrust.ExactPresence
	TrustDecision      systemtrust.TrustDecision
	EvidenceRevision   systemtrust.EvidenceRevision
	ObservedAt         time.Time
	Available          bool
	Reason             string
}

type ActionResult struct {
	Status          Status
	ResultStatus    systemtrust.ResultStatus
	Reason          systemtrust.Reason
	Completed       bool
	RestartRequired bool
}

// Material is the exact public certificate needed by the local macOS guide.
// It carries neither a path nor private signing material.
type Material struct {
	RootRevision   uint64
	Fingerprint    string
	CertificateDER []byte
}

// Controller is the narrow Host-to-control seam. Implementations may be
// backed by a platform adapter, but callers never receive a command executor.
type Controller interface {
	Status(context.Context) (Status, error)
	Material(context.Context) (Material, error)
	Replace(context.Context) (ActionResult, error)
	Shutdown(context.Context) error
}

var (
	ErrRootResetUnavailable     = errors.New("local Root reset is unavailable")
	ErrRootResetActiveCaptures  = errors.New("local Root reset requires no active captures")
	ErrRootResetPending         = errors.New("local Root reset is already pending")
	ErrRootResetRequiresRemoval = errors.New("local Root reset requires the current Root to be removed")
)

// ErrorReason exposes only a stable language-neutral reason to the Host
// control layer; systemtrust's operation type remains behind this adapter.
func ErrorReason(err error) string {
	return reasonForError(err)
}

func IsUnsupported(err error) bool {
	return errors.Is(err, systemtrust.ErrUnsupportedPlatform)
}

func IsActiveCaptureError(err error) bool {
	return errors.Is(err, ErrRootResetActiveCaptures)
}

func IsResetPending(err error) bool {
	return errors.Is(err, ErrRootResetPending)
}

func IsResetRemovalRequired(err error) bool {
	return errors.Is(err, ErrRootResetRequiresRemoval)
}

type Manager struct {
	rootProvider       RootProvider
	coordinator        *systemtrust.Coordinator
	clock              localca.Clock
	observationTimeout time.Duration
	resetRequest       func(context.Context, localca.RootIdentity) error
	replaceAdmission   func(context.Context) (func(), error)

	replaceMu      sync.Mutex
	replaceActive  bool
	restartPending bool
	replaceRelease func()
}

func New(options Options) (*Manager, error) {
	if options.OwnerContext == nil || options.Root == nil || options.Executor == nil {
		return nil, errors.New("Desktop trust dependencies are incomplete")
	}
	commandTimeout := options.CommandTimeout
	if commandTimeout <= 0 {
		commandTimeout = defaultCommandTimeout
	}
	reconciliationTimeout := options.ReconciliationTimeout
	if reconciliationTimeout <= 0 {
		reconciliationTimeout = defaultReconciliationTimeout
	}
	observationTimeout := options.ObservationTimeout
	if observationTimeout <= 0 {
		observationTimeout = reconciliationTimeout
	}
	source, err := systemtrust.NewPublicRootSnapshotSource(
		options.Root.LocalRootIdentity(),
		options.Root.LocalRootCertificate(),
	)
	if err != nil {
		return nil, fmt.Errorf("seal current public Root: %w", err)
	}
	adapter, err := systemtrust.NewProductionMacOSAdapter(options.Executor)
	if err != nil {
		return nil, err
	}
	coordinator, err := systemtrust.NewCoordinator(
		source,
		adapter,
		systemtrust.CoordinatorOptions{
			OwnerContext:          options.OwnerContext,
			CommandTimeout:        commandTimeout,
			ReconciliationTimeout: reconciliationTimeout,
		},
	)
	if err != nil {
		return nil, err
	}
	clock := options.Clock
	if clock == nil {
		clock = localca.SystemClock{}
	}
	return &Manager{
		rootProvider:       options.Root,
		coordinator:        coordinator,
		clock:              clock,
		observationTimeout: observationTimeout,
		resetRequest:       options.ResetRequest,
		replaceAdmission:   options.ReplaceAdmission,
	}, nil
}

type ProductionOptions struct {
	OwnerContext     context.Context
	Root             RootProvider
	Clock            localca.Clock
	ResetRequest     func(context.Context, localca.RootIdentity) error
	ReplaceAdmission func(context.Context) (func(), error)
}

// NewOptionalProduction constructs the one Desktop platform adapter. A
// non-macOS build simply omits this Host-only capability.
func NewOptionalProduction(options ProductionOptions) (*Manager, error) {
	executor, err := NewProductionCommandExecutor()
	if err != nil {
		if errors.Is(err, systemtrust.ErrUnsupportedPlatform) {
			return nil, nil
		}
		return nil, err
	}
	return New(Options{
		OwnerContext:          options.OwnerContext,
		Root:                  options.Root,
		Executor:              executor,
		Clock:                 options.Clock,
		ResetRequest:          options.ResetRequest,
		ReplaceAdmission:      options.ReplaceAdmission,
		CommandTimeout:        productionCommandTimeout,
		ObservationTimeout:    productionObservationTimeout,
		ReconciliationTimeout: productionReconcileTimeout,
	})
}

func (manager *Manager) Status(ctx context.Context) (Status, error) {
	if manager == nil || manager.coordinator == nil ||
		manager.rootProvider == nil || ctx == nil {
		return Status{}, errors.New("Desktop trust manager is unavailable")
	}
	identity := manager.rootProvider.LocalRootIdentity()
	status := Status{
		RootRevision:       uint64(identity.Revision()),
		Fingerprint:        identity.Fingerprint(),
		Algorithm:          string(identity.Algorithm()),
		NotBefore:          identity.NotBefore(),
		NotAfter:           identity.NotAfter(),
		RootValid:          identity.Valid(),
		CertificatePresent: systemtrust.ExactPresenceUnknown,
		TrustDecision:      systemtrust.TrustDecisionUnknown,
		ObservedAt:         manager.clock.Now().UTC(),
	}
	if !status.RootValid {
		status.Reason = "root_invalid"
		return status, systemtrust.ErrCurrentRootInvalid
	}
	observationContext, cancel := context.WithTimeout(ctx, manager.observationTimeout)
	defer cancel()
	observation, err := manager.coordinator.Observe(observationContext)
	if err != nil {
		status.Reason = reasonForError(err)
		return status, nil
	}
	status.CertificatePresent = observation.Presence()
	status.TrustDecision = observation.TrustDecision()
	status.EvidenceRevision = observation.EvidenceRevision()
	status.Available = observation.Valid()
	if !status.Available {
		status.Reason = "observation_unknown"
	}
	return status, nil
}

func (manager *Manager) Material(ctx context.Context) (Material, error) {
	if manager == nil || manager.rootProvider == nil || ctx == nil {
		return Material{}, errors.New("Desktop trust manager is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return Material{}, context.Cause(ctx)
	}
	identity := manager.rootProvider.LocalRootIdentity()
	certificate := manager.rootProvider.LocalRootCertificate()
	der := certificate.CertificateDER()
	digest, err := certidentity.DigestRootCertificate(der)
	if err != nil || !identity.Valid() || digest != identity.Digest() {
		return Material{}, systemtrust.ErrCurrentRootInvalid
	}
	return Material{
		RootRevision:   uint64(identity.Revision()),
		Fingerprint:    identity.Fingerprint(),
		CertificateDER: bytes.Clone(der),
	}, nil
}

// Replace records a one-shot reset request only after observation proves that
// the exact old Root has already been removed through the macOS GUI. The new
// Root is generated only by the next daemon generation, so no live signer can
// mix old and new identities.
func (manager *Manager) Replace(ctx context.Context) (ActionResult, error) {
	if manager == nil || manager.resetRequest == nil ||
		manager.replaceAdmission == nil {
		return ActionResult{}, ErrRootResetUnavailable
	}
	manager.replaceMu.Lock()
	pending := manager.restartPending || manager.replaceActive
	if !pending {
		manager.replaceActive = true
	}
	manager.replaceMu.Unlock()
	if pending {
		return ActionResult{}, ErrRootResetPending
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		manager.replaceMu.Lock()
		manager.replaceActive = false
		manager.replaceMu.Unlock()
	}()
	release, err := manager.replaceAdmission(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	if release == nil {
		return ActionResult{}, ErrRootResetUnavailable
	}
	retained := false
	defer func() {
		if !retained {
			release()
		}
	}()
	status, err := manager.Status(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	if !status.Available ||
		status.CertificatePresent != systemtrust.ExactPresenceAbsent ||
		status.TrustDecision != systemtrust.TrustDecisionUntrusted {
		return ActionResult{
			Status:       status,
			ResultStatus: systemtrust.ResultStatusNeedsManual,
			Reason:       systemtrust.ReasonPostconditionMismatch,
		}, ErrRootResetRequiresRemoval
	}
	identity := manager.rootProvider.LocalRootIdentity()
	if err := manager.resetRequest(ctx, identity); err != nil {
		return ActionResult{
			Status:       status,
			ResultStatus: systemtrust.ResultStatusFailed,
			Reason:       systemtrust.ReasonCommandIndeterminate,
		}, errors.Join(ErrRootResetUnavailable, err)
	}
	// Removing old trust makes the old MITM path unreachable, but the Root
	// identity has not changed until the next daemon generation. Do not claim
	// the replacement itself is complete at this intermediate state.
	result := ActionResult{
		Status:          status,
		ResultStatus:    systemtrust.ResultStatusApplied,
		Reason:          systemtrust.ReasonApplied,
		RestartRequired: true,
	}
	manager.replaceMu.Lock()
	manager.replaceActive = false
	manager.restartPending = true
	manager.replaceRelease = release
	manager.replaceMu.Unlock()
	retained = true
	finished = true
	return result, nil
}

func reasonFromError(err error) systemtrust.Reason {
	var operationErr *systemtrust.OperationError
	if errors.As(err, &operationErr) && operationErr.Reason() != "" {
		return operationErr.Reason()
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return systemtrust.ReasonObservationUnknown
	case errors.Is(err, systemtrust.ErrObservationUnknown):
		return systemtrust.ReasonObservationUnknown
	case errors.Is(err, systemtrust.ErrCurrentRootInvalid):
		return systemtrust.ReasonPlanStale
	default:
		return systemtrust.ReasonCommandIndeterminate
	}
}

func reasonForError(err error) string {
	if err == nil {
		return ""
	}
	return string(reasonFromError(err))
}

func (manager *Manager) Shutdown(ctx context.Context) error {
	if manager == nil || manager.coordinator == nil {
		return nil
	}
	err := manager.coordinator.Shutdown(ctx)
	manager.replaceMu.Lock()
	release := manager.replaceRelease
	manager.replaceRelease = nil
	manager.replaceActive = false
	manager.restartPending = false
	manager.replaceMu.Unlock()
	if release != nil {
		release()
	}
	return err
}
