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
	wireProfile    access.CompiledUpstreamWireProfile
	codecPlan      access.CodecPlan
}

// errCandidatesExhausted reports that a further attempt was allowed and there
// was nothing further to try.
var errCandidatesExhausted = errors.New("RouteSet candidates are exhausted")

// fallbackPlan reports the default RouteSet's attempt policy and how many
// candidates the compiled plan holds.
func fallbackPlan(
	snapshot access.AccessPlanSnapshot,
) (access.FallbackPolicy, int) {
	binding := snapshot.Binding()
	for _, candidate := range snapshot.RouteSets() {
		if candidate.ID == binding.DefaultRouteSetID {
			return candidate.FallbackMode(), snapshot.CandidateCount()
		}
	}
	return access.FallbackDisabled, 0
}

// orderedCandidateProfileIDs places a workspace-selected profile first while
// preserving the configured RouteSet order for any later fallback attempt.
// A zero primary selects the RouteSet's first candidate.
func orderedCandidateProfileIDs(
	snapshot access.AccessPlanSnapshot,
	primary access.EndpointProfileID,
) ([]access.EndpointProfileID, error) {
	binding := snapshot.Binding()
	var configured []access.EndpointProfileID
	for _, routeSet := range snapshot.RouteSets() {
		if routeSet.ID != binding.DefaultRouteSetID {
			continue
		}
		if configured != nil {
			return nil, errors.New("default RouteSet is duplicated")
		}
		configured = slices.Clone(routeSet.CandidateProfileIDs)
	}
	if len(configured) == 0 {
		return nil, errors.New("default RouteSet is unsupported")
	}
	if primary.String() == "" {
		return configured, nil
	}
	if !slices.Contains(configured, primary) {
		return nil, errors.New("workspace profile is outside the default RouteSet")
	}
	ordered := make([]access.EndpointProfileID, 0, len(configured))
	ordered = append(ordered, primary)
	for _, profileID := range configured {
		if profileID != primary {
			ordered = append(ordered, profileID)
		}
	}
	return ordered, nil
}

// selectFrozenPlan freezes one named candidate of the default RouteSet.
func selectFrozenPlan(
	snapshot access.AccessPlanSnapshot,
	profileID access.EndpointProfileID,
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
	if endpoint.ID != binding.AgentEndpointID ||
		endpoint.AccessID != binding.ID {
		return frozenSelection{}, errors.New("AgentEndpoint is unsupported")
	}
	if snapshot.EgressPolicy().Mode != access.EgressModeDirect {
		return frozenSelection{}, errors.New("Access egress policy is unsupported")
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
		len(routeSet.CandidateProfileIDs) == 0 {
		return frozenSelection{}, errors.New("default RouteSet is unsupported")
	}
	if !slices.Contains(routeSet.CandidateProfileIDs, profileID) {
		return frozenSelection{}, errCandidatesExhausted
	}
	foundCompiled := false
	var compiledSelection access.CompiledCandidate
	for candidateIndex := 0; candidateIndex < snapshot.CandidateCount(); candidateIndex++ {
		candidate, ok := snapshot.Candidate(candidateIndex)
		if !ok || candidate.ProfileID() != profileID {
			continue
		}
		if foundCompiled {
			return frozenSelection{}, errors.New("compiled candidate is duplicated")
		}
		foundCompiled = true
		compiledSelection = candidate
	}
	if !foundCompiled {
		return frozenSelection{}, errors.New("compiled candidate is missing")
	}
	codecPlan := compiledSelection.CodecPlan()
	wireProfile := compiledSelection.UpstreamWireProfile()
	if endpoint.ClientDialect != codecPlan.ClientDialect() ||
		wireProfile.Ref().String() == "" ||
		wireProfile.Revision() == 0 ||
		len(wireProfile.Variants()) == 0 {
		return frozenSelection{}, errors.New(
			"Access upstream wire profile is unsupported",
		)
	}
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
		profile.UpstreamWireProfileRef != wireProfile.Ref() ||
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
		(account.AuthDriverRef != access.StaticHeaderAuthDriverRef() &&
			account.AuthDriverRef != access.AnthropicAPIKeyAuthDriverRef()) {
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
		wireProfile:   wireProfile,
		codecPlan:     codecPlan,
	}, nil
}
