package environment

import "github.com/vibe-agi/vibermate/internal/egressprofile"

func (plan RequestPlan) EgressChoice() egressprofile.Choice { return plan.egressProfile }
func (plan RequestPlan) WithResolvedEgress(value egressprofile.Resolved) (RequestPlan, error) {
	if plan.clientTool != nil || plan.environmentDigest == (CandidateDigest{}) || validateResolvedChoice(plan.egressProfile, value) != nil ||
		(plan.resolvedEgress != (egressprofile.Resolved{}) && plan.resolvedEgress != value) {
		return RequestPlan{}, ErrInvalidEnvironment
	}
	plan.resolvedEgress = value
	return plan, nil
}
func (plan RequestPlan) ResolvedEgress() (egressprofile.Resolved, error) {
	if plan.clientTool != nil || plan.environmentDigest == (CandidateDigest{}) || validateResolvedChoice(plan.egressProfile, plan.resolvedEgress) != nil {
		return egressprofile.Resolved{}, ErrInvalidEnvironment
	}
	return plan.resolvedEgress, nil
}
func (plan ClientToolPlan) WithResolvedEgress(value egressprofile.Resolved) (ClientToolPlan, error) {
	if plan.Validate() != nil || validateResolvedChoice(plan.route.EgressProfile, value) != nil ||
		(plan.resolvedEgress != (egressprofile.Resolved{}) && plan.resolvedEgress != value) {
		return ClientToolPlan{}, ErrInvalidEnvironment
	}
	plan.resolvedEgress = value
	return plan, nil
}
func (plan ClientToolPlan) ResolvedEgress() (egressprofile.Resolved, error) {
	if plan.Validate() != nil || validateResolvedChoice(plan.route.EgressProfile, plan.resolvedEgress) != nil {
		return egressprofile.Resolved{}, ErrInvalidEnvironment
	}
	return plan.resolvedEgress, nil
}

func validateResolvedChoice(choice egressprofile.Choice, resolved egressprofile.Resolved) error {
	if choice.Validate() != nil || resolved.Validate() != nil {
		return ErrInvalidEnvironment
	}
	if profile, explicit := choice.ExplicitProfile(); explicit {
		if resolved.Source() != egressprofile.SelectionFlow || resolved.Profile() != profile {
			return ErrInvalidEnvironment
		}
	} else if resolved.Source() != egressprofile.SelectionGlobal && resolved.Source() != egressprofile.SelectionBuiltin {
		return ErrInvalidEnvironment
	}
	return nil
}
