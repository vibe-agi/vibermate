package exchange

import (
	"errors"
	"fmt"
	"slices"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

func validateClientOperation(
	codecPlan access.CodecPlan,
	evidence ClientOperationEvidence,
	replayClass ReplayClass,
) error {
	var selected access.ClientOperationPlan
	found := false
	for _, operation := range codecPlan.ClientOperations() {
		if operation.ID() != evidence.ID() {
			continue
		}
		if found {
			return errors.New("client operation is duplicated in the Access plan")
		}
		selected = operation
		found = true
	}
	if !found ||
		selected.Revision() != evidence.Revision() ||
		selected.ClientDialect() != codecPlan.ClientDialect() ||
		selected.PathMatch() != access.ClientOperationPathExact ||
		selected.Kind() != access.ClientOperationSemantic ||
		selected.Transport() != access.ClientOperationTransportHTTP ||
		!selected.EgressBearing() ||
		selected.PathPattern() != evidence.Path() ||
		!slices.Contains(selected.Methods(), evidence.Method()) ||
		(evidence.RawQuery() != "" &&
			!slices.Contains(
				selected.AllowedQueries(),
				evidence.RawQuery(),
			)) {
		return errors.New(
			"client operation evidence does not match the Access plan",
		)
	}
	expectedReplay, err := exchangeReplayClass(selected.ReplayClass())
	if err != nil || replayClass != expectedReplay {
		return errors.New(
			"client operation replay class does not match the Access plan",
		)
	}
	return nil
}

func exchangeReplayClass(
	class access.ClientReplayClass,
) (ReplayClass, error) {
	switch class {
	case access.ClientReplaySafe:
		return ReplaySafe, nil
	case access.ClientReplayIdempotencyKeyed:
		return ReplayIdempotencyKeyed, nil
	case access.ClientReplayGenerationCostOnly:
		return ReplayGenerationCostOnly, nil
	case access.ClientReplaySideEffectPossible:
		return ReplaySideEffectPossible, nil
	case access.ClientReplayNonReplayable:
		return ReplayNonReplayable, nil
	case access.ClientReplayUnknown:
		return ReplayUnknown, nil
	default:
		return "", errors.New("client operation replay class is invalid")
	}
}

type frozenSelection struct {
	accessID       access.AccessID
	revision       access.Revision
	planHash       access.PlanHash
	effectiveModel string
	targetRef      string
	target         providertransport.Target
	accountID      access.AccountBindingID
	secretRef      access.SecretRef
	authDriverRef  access.AuthDriverRef
	transportPlan  access.CompiledTransportFingerprintPlan
	codecPlan      access.CodecPlan
}

func selectFrozenPlan(
	snapshot access.AccessPlanSnapshot,
) (frozenSelection, error) {
	binding := snapshot.Binding()
	if binding.Status != access.AccessStatusEnabled {
		return frozenSelection{}, errors.New("Access plan is not enabled")
	}
	if binding.ID != snapshot.AccessID() ||
		binding.Revision != snapshot.Revision() ||
		snapshot.PlanHash().IsZero() {
		return frozenSelection{}, errors.New("Access plan identity is inconsistent")
	}

	endpoint := snapshot.AgentEndpoint()
	codecPlan := snapshot.CodecPlan()
	if endpoint.ID != binding.AgentEndpointID ||
		endpoint.AccessID != binding.ID ||
		endpoint.ClientDialect != codecPlan.ClientDialect() {
		return frozenSelection{}, errors.New("AgentEndpoint is unsupported")
	}
	if snapshot.EgressPolicy().Mode != access.EgressModeDirect {
		return frozenSelection{}, errors.New("Access egress policy is unsupported")
	}
	transportPlan := snapshot.TransportFingerprintPlan()
	requestedTransport := transportPlan.Requested()
	if requestedTransport.Ref().String() == "" ||
		requestedTransport.Revision() == 0 ||
		requestedTransport.HTTPTransport() != access.HTTPTransportHTTP1 ||
		len(requestedTransport.ALPN()) == 0 {
		return frozenSelection{}, errors.New(
			"Access transport fingerprint plan is unsupported",
		)
	}
	pluginPlan := snapshot.PluginPlan()
	if pluginPlan.Mode() != access.PluginPlanModePassThrough ||
		len(pluginPlan.BindingIDs()) != 0 {
		return frozenSelection{}, errors.New("Access plugin plan is unsupported")
	}

	routeSets := snapshot.RouteSets()
	var routeSet access.RouteSet
	foundRoute := false
	for _, candidate := range routeSets {
		if candidate.ID == binding.DefaultRouteSetID {
			if foundRoute {
				return frozenSelection{}, errors.New("default RouteSet is duplicated")
			}
			routeSet = candidate
			foundRoute = true
		}
	}
	if !foundRoute ||
		routeSet.AccessID != binding.ID ||
		len(routeSet.CandidateProfileIDs) != 1 {
		return frozenSelection{}, errors.New("default RouteSet is unsupported")
	}

	profileID := routeSet.CandidateProfileIDs[0]
	profiles := snapshot.EndpointProfiles()
	var profile access.EndpointProfile
	foundProfile := false
	for _, candidate := range profiles {
		if candidate.ID == profileID {
			if foundProfile {
				return frozenSelection{}, errors.New("EndpointProfile is duplicated")
			}
			profile = candidate
			foundProfile = true
		}
	}
	if !foundProfile ||
		profile.AccessID != binding.ID ||
		profile.BackendDialect != codecPlan.ProviderDialect() ||
		profile.DefaultModelPolicy.Mode != access.ModelPolicyModeFixed ||
		profile.DefaultModelPolicy.FixedModel.String() == "" ||
		len(profile.AccountBindingIDs) != 1 {
		return frozenSelection{}, errors.New("EndpointProfile is unsupported")
	}

	accountID := profile.DefaultAccountBindingID
	if profile.AccountBindingIDs[0] != accountID {
		return frozenSelection{}, errors.New("default account is not the sole profile account")
	}
	accounts := snapshot.AccountBindings()
	var account access.ProviderAccountBinding
	foundAccount := false
	for _, candidate := range accounts {
		if candidate.ID == accountID {
			if foundAccount {
				return frozenSelection{}, errors.New("account binding is duplicated")
			}
			account = candidate
			foundAccount = true
		}
	}
	if !foundAccount ||
		!account.Enabled ||
		account.AccessID != binding.ID ||
		account.ProfileID != profile.ID ||
		account.AuthDriverRef != access.StaticHeaderAuthDriverRef() {
		return frozenSelection{}, errors.New("provider account binding is unsupported")
	}

	targets := snapshot.ProviderTargets()
	var compiledTarget access.CompiledProviderTarget
	foundTarget := false
	for _, candidate := range targets {
		resource := candidate.Target()
		if resource.ID == profile.TargetID {
			if foundTarget {
				return frozenSelection{}, errors.New("ProviderTarget is duplicated")
			}
			compiledTarget = candidate
			foundTarget = true
		}
	}
	if !foundTarget {
		return frozenSelection{}, errors.New("ProviderTarget is unresolved")
	}
	targetResource := compiledTarget.Target()
	if targetResource.AccessID != binding.ID ||
		targetResource.ProfileID != profile.ID ||
		targetResource.Protocol != codecPlan.ProviderDialect() {
		return frozenSelection{}, errors.New("ProviderTarget ownership is inconsistent")
	}
	target, err := providertransport.NewTarget(compiledTarget)
	if err != nil {
		return frozenSelection{}, fmt.Errorf("compile provider transport target: %w", err)
	}

	return frozenSelection{
		accessID:       binding.ID,
		revision:       binding.Revision,
		planHash:       snapshot.PlanHash(),
		effectiveModel: profile.DefaultModelPolicy.FixedModel.String(),
		targetRef: access.ProviderTargetReference(
			binding.ID,
			targetResource.ID,
		),
		target:        target,
		accountID:     account.ID,
		secretRef:     account.SecretRef,
		authDriverRef: account.AuthDriverRef,
		transportPlan: transportPlan,
		codecPlan:     codecPlan,
	}, nil
}
