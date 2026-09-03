// Package systemtrust defines exact public-Root trust observations and
// deterministic trust-operation orchestration. It does not provide a live
// operating-system executor or mutate a trust store.
package systemtrust

import (
	"bytes"
	"errors"
	"fmt"
	"slices"

	"github.com/vibe-agi/vibermate/internal/certidentity"
)

var (
	ErrInvalidOperation    = errors.New("system trust operation is invalid")
	ErrInvalidObservation  = errors.New("system trust observation is invalid")
	ErrObservationUnknown  = errors.New("system trust observation is unknown")
	ErrInvalidPlan         = errors.New("system trust change plan is invalid")
	ErrPlanStale           = errors.New("system trust change plan is stale")
	ErrOperationInProgress = errors.New("system trust operation is already in progress")
	ErrCoordinatorStopping = errors.New("system trust coordinator is stopping")
	ErrCurrentRootInvalid  = errors.New("current public Root is invalid")
	ErrCommandInvalid      = errors.New("system trust command result is invalid")
)

// ExactPresence reports whether the exact DER-identified certificate object
// is present. It does not imply a trust decision.
type ExactPresence string

const (
	ExactPresencePresent ExactPresence = "present"
	ExactPresenceAbsent  ExactPresence = "absent"
	ExactPresenceUnknown ExactPresence = "unknown"
)

func (presence ExactPresence) valid() bool {
	switch presence {
	case ExactPresencePresent, ExactPresenceAbsent, ExactPresenceUnknown:
		return true
	default:
		return false
	}
}

// TrustDecision reports the bounded decision for one usage in one trust
// settings domain. It does not imply certificate-object presence.
type TrustDecision string

const (
	TrustDecisionTrusted   TrustDecision = "trusted"
	TrustDecisionUntrusted TrustDecision = "untrusted"
	TrustDecisionUnknown   TrustDecision = "unknown"
)

func (decision TrustDecision) valid() bool {
	switch decision {
	case TrustDecisionTrusted, TrustDecisionUntrusted, TrustDecisionUnknown:
		return true
	default:
		return false
	}
}

type TrustSettingsDomain string

const TrustSettingsDomainUser TrustSettingsDomain = "user"

type CertificateKeychain string

const CertificateKeychainUserSearchList CertificateKeychain = "user_search_list"

type TrustUsage string

const TrustUsageServerTLS TrustUsage = "server_tls"

// TargetScope separates the current user's trust-settings domain from the
// keychain search scope used to locate the exact certificate object.
type TargetScope struct {
	domain   TrustSettingsDomain
	keychain CertificateKeychain
	usage    TrustUsage
}

func MacOSCurrentUserTarget() TargetScope {
	return TargetScope{
		domain:   TrustSettingsDomainUser,
		keychain: CertificateKeychainUserSearchList,
		usage:    TrustUsageServerTLS,
	}
}

func (scope TargetScope) TrustSettingsDomain() TrustSettingsDomain {
	return scope.domain
}

func (scope TargetScope) CertificateKeychain() CertificateKeychain {
	return scope.keychain
}

func (scope TargetScope) Usage() TrustUsage {
	return scope.usage
}

func (scope TargetScope) valid() bool {
	return scope == MacOSCurrentUserTarget()
}

// EvidenceRevision identifies one bounded observation grammar. It is not an
// operating-system version or a claim that live platform behavior is verified.
type EvidenceRevision string

const (
	EvidenceRevisionMacOSFixtureV1  EvidenceRevision = "macos-fixture-v1"
	EvidenceRevisionMacOSSecurityV2 EvidenceRevision = "macos-security-v2"
)

func (revision EvidenceRevision) valid() bool {
	return revision == EvidenceRevisionMacOSFixtureV1 ||
		revision == EvidenceRevisionMacOSSecurityV2
}

// Observation binds both independent state axes to the exact current Root and
// fixed target. It contains no certificate path or command output.
type Observation struct {
	rootRevision certidentity.RootRevision
	rootDigest   certidentity.RootDigest
	target       TargetScope
	presence     ExactPresence
	decision     TrustDecision
	evidence     EvidenceRevision
}

func newObservation(
	root publicRoot,
	presence ExactPresence,
	decision TrustDecision,
	evidence EvidenceRevision,
) Observation {
	return Observation{
		rootRevision: root.identity.Revision(),
		rootDigest:   root.identity.Digest(),
		target:       MacOSCurrentUserTarget(),
		presence:     presence,
		decision:     decision,
		evidence:     evidence,
	}
}

func (observation Observation) RootRevision() certidentity.RootRevision {
	return observation.rootRevision
}

func (observation Observation) RootDigest() certidentity.RootDigest {
	return observation.rootDigest
}

func (observation Observation) Target() TargetScope {
	return observation.target
}

func (observation Observation) Presence() ExactPresence {
	return observation.presence
}

func (observation Observation) TrustDecision() TrustDecision {
	return observation.decision
}

func (observation Observation) EvidenceRevision() EvidenceRevision {
	return observation.evidence
}

func (observation Observation) Valid() bool {
	if !observation.rootRevision.Valid() || !observation.rootDigest.Valid() ||
		!observation.target.valid() || !observation.presence.valid() ||
		!observation.decision.valid() || !observation.evidence.valid() {
		return false
	}
	if observation.presence == ExactPresenceAbsent &&
		observation.decision == TrustDecisionTrusted {
		return false
	}
	return true
}

func (observation Observation) usable() bool {
	return observation.Valid() &&
		observation.presence != ExactPresenceUnknown &&
		observation.decision != TrustDecisionUnknown
}

type Operation string

const (
	OperationInstall Operation = "install"
	OperationRemove  Operation = "remove"
)

func (operation Operation) valid() bool {
	return operation == OperationInstall || operation == OperationRemove
}

// Step is a typed logical operation. It is not an executable command string.
type Step string

const (
	StepEnsureExactCertificateAndUserTrust Step = "ensure_exact_certificate_and_user_trust"
	StepRemoveExactUserTrustSettings       Step = "remove_exact_user_trust_settings"
	StepDeleteExactCertificate             Step = "delete_exact_certificate"
	StepInspectExactRoot                   Step = "inspect_exact_root"
)

func (step Step) mutation() bool {
	switch step {
	case StepEnsureExactCertificateAndUserTrust,
		StepRemoveExactUserTrustSettings,
		StepDeleteExactCertificate:
		return true
	default:
		return false
	}
}

type ManualFallback string

const (
	ManualFallbackNone              ManualFallback = "none"
	ManualFallbackExactRootRecovery ManualFallback = "exact_root_recovery"
)

// ChangePlan is an immutable, short-lived description of one exact Root
// operation. It is not authorization and is always revalidated at execution.
type ChangePlan struct {
	operation               Operation
	rootRevision            certidentity.RootRevision
	rootDigest              certidentity.RootDigest
	certificateDER          []byte
	target                  TargetScope
	desired                 Observation
	precondition            Observation
	steps                   []Step
	requiresOSAuthorization bool
	manualFallback          ManualFallback
}

func (plan ChangePlan) Operation() Operation {
	return plan.operation
}

func (plan ChangePlan) RootRevision() certidentity.RootRevision {
	return plan.rootRevision
}

func (plan ChangePlan) RootDigest() certidentity.RootDigest {
	return plan.rootDigest
}

func (plan ChangePlan) CertificateDER() []byte {
	return bytes.Clone(plan.certificateDER)
}

func (plan ChangePlan) Target() TargetScope {
	return plan.target
}

func (plan ChangePlan) DesiredObservation() Observation {
	return plan.desired
}

func (plan ChangePlan) Precondition() Observation {
	return plan.precondition
}

func (plan ChangePlan) Steps() []Step {
	return slices.Clone(plan.steps)
}

func (plan ChangePlan) RequiresOSAuthorization() bool {
	return plan.requiresOSAuthorization
}

func (plan ChangePlan) ManualFallback() ManualFallback {
	return plan.manualFallback
}

func (plan ChangePlan) Valid() bool {
	certificateDigest, digestErr := certidentity.DigestRootCertificate(
		plan.certificateDER,
	)
	wantSteps, wantPresence, wantDecision, shapeErr := planShape(
		plan.operation,
		plan.precondition.presence,
		plan.precondition.decision,
	)
	if !plan.operation.valid() || !plan.rootRevision.Valid() ||
		!plan.rootDigest.Valid() || len(plan.certificateDER) == 0 ||
		digestErr != nil || certificateDigest != plan.rootDigest ||
		!plan.target.valid() || !plan.desired.Valid() ||
		!plan.precondition.usable() ||
		plan.desired.rootRevision != plan.rootRevision ||
		plan.desired.rootDigest != plan.rootDigest ||
		plan.precondition.rootRevision != plan.rootRevision ||
		plan.precondition.rootDigest != plan.rootDigest ||
		plan.desired.target != plan.target ||
		plan.precondition.target != plan.target ||
		plan.desired.evidence != plan.precondition.evidence ||
		shapeErr != nil || !slices.Equal(plan.steps, wantSteps) ||
		plan.desired.presence != wantPresence ||
		plan.desired.decision != wantDecision {
		return false
	}
	hasMutation := false
	for _, step := range plan.steps {
		hasMutation = hasMutation || step.mutation()
	}
	if hasMutation != plan.requiresOSAuthorization {
		return false
	}
	if hasMutation {
		return plan.manualFallback == ManualFallbackExactRootRecovery
	}
	return plan.manualFallback == ManualFallbackNone
}

type ResultStatus string

const (
	ResultStatusApplied       ResultStatus = "applied"
	ResultStatusUserCancelled ResultStatus = "user_cancelled"
	ResultStatusNeedsManual   ResultStatus = "needs_manual"
	ResultStatusFailed        ResultStatus = "failed"
)

type Reason string

const (
	ReasonAlreadySatisfied      Reason = "already_satisfied"
	ReasonOperationInProgress   Reason = "operation_in_progress"
	ReasonPlanStale             Reason = "plan_stale"
	ReasonObservationUnknown    Reason = "observation_unknown"
	ReasonCallerCancelled       Reason = "caller_cancelled"
	ReasonUserCancelled         Reason = "user_cancelled"
	ReasonPermissionDenied      Reason = "permission_denied"
	ReasonCommandTimeout        Reason = "command_timeout"
	ReasonCommandFailed         Reason = "command_failed"
	ReasonCommandIndeterminate  Reason = "command_indeterminate"
	ReasonPostconditionMismatch Reason = "postcondition_mismatch"
	ReasonShuttingDown          Reason = "shutting_down"
	ReasonReconciliationUnknown Reason = "reconciliation_unknown"
	ReasonApplied               Reason = "applied"
)

type OperationResult struct {
	operation    Operation
	status       ResultStatus
	reason       Reason
	completed    bool
	rootRevision certidentity.RootRevision
	rootDigest   certidentity.RootDigest
	observation  Observation
}

func (result OperationResult) Operation() Operation {
	return result.operation
}

func (result OperationResult) Status() ResultStatus {
	return result.status
}

func (result OperationResult) Reason() Reason {
	return result.reason
}

func (result OperationResult) Completed() bool {
	return result.completed
}

func (result OperationResult) RootRevision() certidentity.RootRevision {
	return result.rootRevision
}

func (result OperationResult) RootDigest() certidentity.RootDigest {
	return result.rootDigest
}

func (result OperationResult) Observation() Observation {
	return result.observation
}

func statusForReason(reason Reason) ResultStatus {
	switch reason {
	case ReasonApplied, ReasonAlreadySatisfied:
		return ResultStatusApplied
	case ReasonUserCancelled:
		return ResultStatusUserCancelled
	case ReasonPermissionDenied:
		return ResultStatusNeedsManual
	default:
		return ResultStatusFailed
	}
}

type OperationError struct {
	reason Reason
	cause  error
}

func (failure *OperationError) Error() string {
	if failure == nil {
		return "system trust operation failed"
	}
	return fmt.Sprintf("system trust operation failed: %s", failure.reason)
}

func (failure *OperationError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func (failure *OperationError) Reason() Reason {
	if failure == nil {
		return ""
	}
	return failure.reason
}

func operationFailure(reason Reason, cause error) error {
	return &OperationError{reason: reason, cause: cause}
}
