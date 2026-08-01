package systemtrust

import (
	"bytes"
	"errors"

	"github.com/vibe-agi/vibermate/internal/certidentity"
)

func buildPlan(
	operation Operation,
	root publicRoot,
	observation Observation,
) (ChangePlan, error) {
	if !operation.valid() {
		return ChangePlan{}, ErrInvalidOperation
	}
	if !root.valid() {
		return ChangePlan{}, ErrCurrentRootInvalid
	}
	if !observation.Valid() {
		return ChangePlan{}, ErrInvalidObservation
	}
	if !observation.usable() {
		return ChangePlan{}, ErrObservationUnknown
	}
	if observation.rootRevision != root.identity.Revision() ||
		observation.rootDigest != root.identity.Digest() ||
		observation.target != MacOSSystemTarget() {
		return ChangePlan{}, ErrPlanStale
	}
	steps, desiredPresence, desiredDecision, err := planShape(
		operation,
		observation.presence,
		observation.decision,
	)
	if err != nil {
		return ChangePlan{}, err
	}
	desired := newObservation(
		root,
		desiredPresence,
		desiredDecision,
		observation.evidence,
	)
	hasMutation := len(steps) != 0
	fallback := ManualFallbackNone
	if hasMutation {
		fallback = ManualFallbackExactRootRecovery
	}
	plan := ChangePlan{
		operation:               operation,
		rootRevision:            root.identity.Revision(),
		rootDigest:              root.identity.Digest(),
		certificateDER:          bytes.Clone(root.certificateDER),
		target:                  MacOSSystemTarget(),
		desired:                 desired,
		precondition:            observation,
		steps:                   steps,
		requiresOSAuthorization: hasMutation,
		manualFallback:          fallback,
	}
	if !plan.Valid() {
		return ChangePlan{}, ErrInvalidPlan
	}
	return plan, nil
}

func planShape(
	operation Operation,
	presence ExactPresence,
	decision TrustDecision,
) ([]Step, ExactPresence, TrustDecision, error) {
	if !operation.valid() || !presence.valid() || !decision.valid() ||
		presence == ExactPresenceUnknown || decision == TrustDecisionUnknown ||
		(presence == ExactPresenceAbsent && decision == TrustDecisionTrusted) {
		return nil, "", "", ErrObservationUnknown
	}
	switch operation {
	case OperationInstall:
		switch {
		case presence == ExactPresencePresent && decision == TrustDecisionTrusted:
			return nil, ExactPresencePresent, TrustDecisionTrusted, nil
		case decision == TrustDecisionUntrusted:
			return []Step{
				StepEnsureExactCertificateAndAdminTrust,
				StepInspectExactRoot,
			}, ExactPresencePresent, TrustDecisionTrusted, nil
		}
	case OperationRemove:
		switch {
		case presence == ExactPresencePresent && decision == TrustDecisionTrusted:
			return []Step{
				StepRemoveExactAdminTrustSettings,
				StepInspectExactRoot,
				StepDeleteExactCertificate,
				StepInspectExactRoot,
			}, ExactPresenceAbsent, TrustDecisionUntrusted, nil
		case presence == ExactPresencePresent && decision == TrustDecisionUntrusted:
			return []Step{
				StepDeleteExactCertificate,
				StepInspectExactRoot,
			}, ExactPresenceAbsent, TrustDecisionUntrusted, nil
		case presence == ExactPresenceAbsent && decision == TrustDecisionUntrusted:
			return nil, ExactPresenceAbsent, TrustDecisionUntrusted, nil
		}
	}
	return nil, "", "", errors.Join(ErrInvalidPlan, ErrInvalidObservation)
}

func planMatchesRoot(plan ChangePlan, root publicRoot) bool {
	if !plan.Valid() || !root.valid() ||
		plan.rootRevision != root.identity.Revision() ||
		plan.rootDigest != root.identity.Digest() ||
		!bytes.Equal(plan.certificateDER, root.certificateDER) {
		return false
	}
	digest, err := certidentity.DigestRootCertificate(plan.certificateDER)
	return err == nil && digest == plan.rootDigest
}
