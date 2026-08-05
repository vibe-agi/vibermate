package access

import "fmt"

const (
	originalPassthroughProfileIDValue  = "original-passthrough"
	originalPassthroughTargetIDValue   = "original-client-origin"
	originalPassthroughRouteSetIDValue = "default-route"
)

// OriginalPassthroughProfileID is stable within every Access aggregate. The
// owning Access ID remains the outer identity, so no cross-Access object is
// introduced by using the same local resource name.
func OriginalPassthroughProfileID() EndpointProfileID {
	return EndpointProfileID{value: originalPassthroughProfileIDValue}
}

// OriginalPassthroughTargetID identifies the target derived from the owning
// AgentEndpoint. Callers cannot provide or retarget it.
func OriginalPassthroughTargetID() ProviderTargetID {
	return ProviderTargetID{value: originalPassthroughTargetIDValue}
}

// OriginalPassthroughRouteSetID is used when the control caller creates an
// Access without supplying any managed route configuration.
func OriginalPassthroughRouteSetID() RouteSetID {
	return RouteSetID{value: originalPassthroughRouteSetIDValue}
}

// BuildOriginalPassthrough derives the system profile and exact target from
// the Access-owned AgentEndpoint. It creates no account, SecretRef, or auth
// driver authority.
func BuildOriginalPassthrough(
	accessID AccessID,
	endpoint AgentEndpoint,
) (EndpointProfile, ProviderTarget, error) {
	if err := accessID.validate(); err != nil {
		return EndpointProfile{}, ProviderTarget{}, err
	}
	if err := endpoint.Validate(); err != nil {
		return EndpointProfile{}, ProviderTarget{}, err
	}
	if endpoint.AccessID != accessID {
		return EndpointProfile{}, ProviderTarget{}, fmt.Errorf(
			"%w: original passthrough endpoint identity differs",
			ErrInvalidAccess,
		)
	}
	origin, err := NewProviderOrigin(endpoint.ClientOrigin.String())
	if err != nil {
		return EndpointProfile{}, ProviderTarget{}, err
	}
	profile := EndpointProfile{
		ID:                     OriginalPassthroughProfileID(),
		Revision:               endpoint.Revision,
		AccessID:               accessID,
		Kind:                   EndpointProfileOriginalPassthrough,
		CredentialSource:       CredentialSourceClientPassthrough,
		ProcessingMode:         ProfileProcessingObserveOnly,
		Name:                   "Current client login",
		BackendDialect:         endpoint.ClientDialect,
		TargetID:               OriginalPassthroughTargetID(),
		UpstreamWireProfileRef: FollowClientUpstreamWireProfileRef(),
		DefaultModelPolicy: ModelPolicy{
			Revision: endpoint.Revision,
			Mode:     ModelPolicyModePassthrough,
		},
	}
	target := ProviderTarget{
		ID:        OriginalPassthroughTargetID(),
		Revision:  endpoint.Revision,
		AccessID:  accessID,
		ProfileID: profile.ID,
		Origin:    origin,
		Protocol:  endpoint.ClientDialect,
		Capabilities: []ProviderCapability{
			ProviderCapabilityMessages,
			ProviderCapabilityStreaming,
			ProviderCapabilityToolCalls,
		},
	}
	if err := profile.Validate(); err != nil {
		return EndpointProfile{}, ProviderTarget{}, err
	}
	if err := target.Validate(); err != nil {
		return EndpointProfile{}, ProviderTarget{}, err
	}
	return profile, target, nil
}

// AttachOriginalPassthrough adds the one Core-owned profile to an aggregate
// that already contains only managed profiles. It is used by typed internal
// builders; external callers never submit the system profile fields.
func AttachOriginalPassthrough(aggregate Aggregate) (Aggregate, error) {
	owned := aggregate.Clone()
	for _, profile := range owned.Profiles {
		if profile.ID == OriginalPassthroughProfileID() ||
			profile.Kind == EndpointProfileOriginalPassthrough {
			return Aggregate{}, fmt.Errorf(
				"%w: original passthrough profile is already present",
				ErrInvalidAccess,
			)
		}
	}
	for _, target := range owned.ProviderTargets {
		if target.ID == OriginalPassthroughTargetID() {
			return Aggregate{}, fmt.Errorf(
				"%w: original passthrough target is already present",
				ErrInvalidAccess,
			)
		}
	}
	profile, target, err := BuildOriginalPassthrough(
		owned.Binding.ID,
		owned.AgentEndpoint,
	)
	if err != nil {
		return Aggregate{}, err
	}
	owned.Binding.ProfileIDs = append(owned.Binding.ProfileIDs, profile.ID)
	owned.Profiles = append(owned.Profiles, profile)
	owned.ProviderTargets = append(owned.ProviderTargets, target)
	return owned, nil
}

// RefreshOriginalPassthrough re-derives the Core-owned profile after an
// AgentEndpoint change. It refuses aggregates whose system identity is absent
// or duplicated so an update cannot silently repair an unrelated corruption.
func RefreshOriginalPassthrough(aggregate Aggregate) (Aggregate, error) {
	owned := aggregate.Clone()
	profileIndex := -1
	targetIndex := -1
	profileRefs := 0
	for index, profile := range owned.Profiles {
		if profile.ID == OriginalPassthroughProfileID() {
			if profileIndex >= 0 {
				return Aggregate{}, fmt.Errorf(
					"%w: original passthrough profile is duplicated",
					ErrInvalidAccess,
				)
			}
			profileIndex = index
		}
	}
	for index, target := range owned.ProviderTargets {
		if target.ID == OriginalPassthroughTargetID() {
			if targetIndex >= 0 {
				return Aggregate{}, fmt.Errorf(
					"%w: original passthrough target is duplicated",
					ErrInvalidAccess,
				)
			}
			targetIndex = index
		}
	}
	for _, profileID := range owned.Binding.ProfileIDs {
		if profileID == OriginalPassthroughProfileID() {
			profileRefs++
		}
	}
	if profileIndex < 0 || targetIndex < 0 || profileRefs != 1 {
		return Aggregate{}, fmt.Errorf(
			"%w: original passthrough identity is incomplete",
			ErrInvalidAccess,
		)
	}
	profile, target, err := BuildOriginalPassthrough(
		owned.Binding.ID,
		owned.AgentEndpoint,
	)
	if err != nil {
		return Aggregate{}, err
	}
	owned.Profiles[profileIndex] = profile
	owned.ProviderTargets[targetIndex] = target
	return owned, nil
}
