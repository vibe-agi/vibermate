package access

import (
	"fmt"
	"slices"
)

const (
	MaxEndpointProfiles = 64
	MaxAccountBindings  = 128
	MaxRouteSets        = 64
	MaxCapabilities     = 64
	MaxPluginBindings   = 128
)

// AccessBinding is the persisted aggregate root.
type AccessBinding struct {
	ID                AccessID
	Revision          Revision
	Name              string
	Description       string
	Status            AccessStatus
	AgentEndpointID   AgentEndpointID
	DefaultRouteSetID RouteSetID
	ProfileIDs        []EndpointProfileID
	EgressPolicyID    EgressPolicyID
}

func (binding AccessBinding) Validate() error {
	if err := binding.ID.validate(); err != nil {
		return err
	}
	if err := validateRevision("AccessBinding", binding.Revision); err != nil {
		return err
	}
	if err := validateBoundedText("Access name", binding.Name, MaxAccessNameBytes, false); err != nil {
		return err
	}
	if err := validateBoundedText(
		"Access description",
		binding.Description,
		MaxDescriptionBytes,
		true,
	); err != nil {
		return err
	}
	switch binding.Status {
	case AccessStatusDraft, AccessStatusEnabled, AccessStatusDisabled:
	default:
		return fmt.Errorf("%w: Access status is invalid", ErrInvalidAccess)
	}
	if err := binding.AgentEndpointID.validate(); err != nil {
		return err
	}
	if err := binding.DefaultRouteSetID.validate(); err != nil {
		return err
	}
	if err := binding.EgressPolicyID.validate(); err != nil {
		return err
	}
	if len(binding.ProfileIDs) == 0 || len(binding.ProfileIDs) > MaxEndpointProfiles {
		return fmt.Errorf("%w: Access profile references are invalid", ErrInvalidAccess)
	}
	for _, profileID := range binding.ProfileIDs {
		if err := profileID.validate(); err != nil {
			return err
		}
	}
	return nil
}

type AgentEndpoint struct {
	ID            AgentEndpointID
	Revision      Revision
	AccessID      AccessID
	ClientOrigin  ClientOrigin
	ClientDialect Dialect
}

func (endpoint AgentEndpoint) Validate() error {
	if err := endpoint.ID.validate(); err != nil {
		return err
	}
	if err := validateRevision("AgentEndpoint", endpoint.Revision); err != nil {
		return err
	}
	if err := endpoint.AccessID.validate(); err != nil {
		return err
	}
	if err := endpoint.ClientOrigin.validate(); err != nil {
		return err
	}
	if endpoint.ClientDialect == "" {
		return fmt.Errorf("%w: client dialect is empty", ErrInvalidAccess)
	}
	return nil
}

type ModelPolicy struct {
	Revision   Revision
	Mode       ModelPolicyMode
	FixedModel ModelName
	MappingRef ModelMappingRef
}

func (policy ModelPolicy) Validate() error {
	if err := validateRevision("ModelPolicy", policy.Revision); err != nil {
		return err
	}
	switch policy.Mode {
	case ModelPolicyModePassthrough:
		if policy.FixedModel.value != "" || policy.MappingRef.value != "" {
			return fmt.Errorf("%w: passthrough ModelPolicy has extra fields", ErrInvalidAccess)
		}
	case ModelPolicyModeFixed:
		if err := policy.FixedModel.validate(); err != nil {
			return err
		}
		if policy.MappingRef.value != "" {
			return fmt.Errorf("%w: fixed ModelPolicy has a mapping reference", ErrInvalidAccess)
		}
	case ModelPolicyModeMap:
		if err := policy.MappingRef.validate(); err != nil {
			return err
		}
		if policy.FixedModel.value != "" {
			return fmt.Errorf("%w: mapped ModelPolicy has a fixed model", ErrInvalidAccess)
		}
	default:
		return fmt.Errorf("%w: ModelPolicy mode is invalid", ErrInvalidAccess)
	}
	return nil
}

type EndpointProfile struct {
	ID                      EndpointProfileID
	Revision                Revision
	AccessID                AccessID
	Kind                    EndpointProfileKind
	CredentialSource        CredentialSource
	ProcessingMode          ProfileProcessingMode
	Name                    string
	Description             string
	BackendDialect          Dialect
	TargetID                ProviderTargetID
	UpstreamWireProfileRef  UpstreamWireProfileRef
	DefaultModelPolicy      ModelPolicy
	AccountBindingIDs       []AccountBindingID
	DefaultAccountBindingID AccountBindingID
}

func (profile EndpointProfile) Validate() error {
	if err := profile.ID.validate(); err != nil {
		return err
	}
	if err := validateRevision("EndpointProfile", profile.Revision); err != nil {
		return err
	}
	if err := profile.AccessID.validate(); err != nil {
		return err
	}
	switch profile.Kind {
	case EndpointProfileOriginalPassthrough:
		if profile.CredentialSource != CredentialSourceClientPassthrough ||
			profile.ProcessingMode != ProfileProcessingObserveOnly {
			return fmt.Errorf(
				"%w: original passthrough profile authority is inconsistent",
				ErrInvalidAccess,
			)
		}
	case EndpointProfileManaged:
		if profile.CredentialSource != CredentialSourceManagedAccount ||
			profile.ProcessingMode != ProfileProcessingManaged {
			return fmt.Errorf(
				"%w: managed profile authority is inconsistent",
				ErrInvalidAccess,
			)
		}
	default:
		return fmt.Errorf("%w: EndpointProfile kind is invalid", ErrInvalidAccess)
	}
	if err := validateBoundedText("EndpointProfile name", profile.Name, MaxAccessNameBytes, false); err != nil {
		return err
	}
	if err := validateBoundedText(
		"EndpointProfile description",
		profile.Description,
		MaxDescriptionBytes,
		true,
	); err != nil {
		return err
	}
	if profile.BackendDialect == "" {
		return fmt.Errorf("%w: backend dialect is empty", ErrInvalidAccess)
	}
	if err := profile.TargetID.validate(); err != nil {
		return err
	}
	if err := profile.UpstreamWireProfileRef.validate(); err != nil {
		return err
	}
	if err := profile.DefaultModelPolicy.Validate(); err != nil {
		return err
	}
	if len(profile.AccountBindingIDs) > MaxAccountBindings {
		return fmt.Errorf("%w: profile account references are invalid", ErrInvalidAccess)
	}
	for _, accountID := range profile.AccountBindingIDs {
		if err := accountID.validate(); err != nil {
			return err
		}
	}
	if profile.Kind == EndpointProfileOriginalPassthrough {
		if len(profile.AccountBindingIDs) != 0 ||
			profile.DefaultAccountBindingID.String() != "" ||
			profile.DefaultModelPolicy.Mode != ModelPolicyModePassthrough {
			return fmt.Errorf(
				"%w: original passthrough profile contains managed fields",
				ErrInvalidAccess,
			)
		}
		return nil
	}
	if len(profile.AccountBindingIDs) == 0 {
		return fmt.Errorf("%w: managed profile has no account", ErrInvalidAccess)
	}
	return profile.DefaultAccountBindingID.validate()
}

type ProviderTarget struct {
	ID           ProviderTargetID
	Revision     Revision
	AccessID     AccessID
	ProfileID    EndpointProfileID
	Origin       ProviderOrigin
	Protocol     Dialect
	Capabilities []ProviderCapability
}

func (target ProviderTarget) Validate() error {
	if err := target.ID.validate(); err != nil {
		return err
	}
	if err := validateRevision("ProviderTarget", target.Revision); err != nil {
		return err
	}
	if err := target.AccessID.validate(); err != nil {
		return err
	}
	if err := target.ProfileID.validate(); err != nil {
		return err
	}
	if err := target.Origin.validate(); err != nil {
		return err
	}
	if target.Protocol == "" {
		return fmt.Errorf("%w: ProviderTarget protocol is empty", ErrInvalidAccess)
	}
	if len(target.Capabilities) == 0 || len(target.Capabilities) > MaxCapabilities {
		return fmt.Errorf("%w: ProviderTarget capabilities are invalid", ErrInvalidAccess)
	}
	for _, capability := range target.Capabilities {
		if capability == "" {
			return fmt.Errorf("%w: ProviderTarget capability is empty", ErrInvalidAccess)
		}
	}
	return nil
}

type ProviderAccountBinding struct {
	ID            AccountBindingID
	Revision      Revision
	AccessID      AccessID
	ProfileID     EndpointProfileID
	Label         string
	SecretRef     SecretRef
	AuthDriverRef AuthDriverRef
	Enabled       bool
}

func (binding ProviderAccountBinding) Validate() error {
	if err := binding.ID.validate(); err != nil {
		return err
	}
	if err := validateRevision("ProviderAccountBinding", binding.Revision); err != nil {
		return err
	}
	if err := binding.AccessID.validate(); err != nil {
		return err
	}
	if err := binding.ProfileID.validate(); err != nil {
		return err
	}
	if err := validateBoundedText("account label", binding.Label, MaxLabelBytes, false); err != nil {
		return err
	}
	if err := binding.SecretRef.validate(); err != nil {
		return err
	}
	return binding.AuthDriverRef.validate()
}

// FallbackPolicy says whether a failed attempt may be tried against the next
// candidate, and is closed on purpose.
//
// Design 02 §12 permits an automatic fallback only before a first byte has
// been committed downstream, with no unresolved tool call, and only when the
// policy explicitly allows the duplicate billing and possible upstream side
// effects a second attempt brings. A timeout, a 429, or a 5xx does not prove
// the upstream did not process the request, so choosing this is the
// permission; the transport's opinion is not.
type FallbackPolicy string

const (
	// FallbackDisabled attempts one candidate and reports what happened.
	FallbackDisabled FallbackPolicy = "disabled"
	// FallbackPreFirstByteIdempotentOnly allows the next candidate only while
	// nothing has reached the client and the request may be sent again.
	FallbackPreFirstByteIdempotentOnly FallbackPolicy = "pre_first_byte_idempotent_only"
)

func (policy FallbackPolicy) valid() bool {
	switch policy {
	case FallbackDisabled, FallbackPreFirstByteIdempotentOnly:
		return true
	default:
		return false
	}
}

// Allows reports whether this policy permits a further attempt at all.
func (policy FallbackPolicy) Allows() bool {
	return policy == FallbackPreFirstByteIdempotentOnly
}

type RouteSet struct {
	ID                  RouteSetID
	Revision            Revision
	AccessID            AccessID
	CandidateProfileIDs []EndpointProfileID
	// Fallback is unstated on a route set written before it existed, and an
	// unstated policy allows nothing.
	Fallback FallbackPolicy
}

// FallbackMode reads an unstated policy as disabled.
func (routeSet RouteSet) FallbackMode() FallbackPolicy {
	if routeSet.Fallback == "" {
		return FallbackDisabled
	}
	return routeSet.Fallback
}

func (routeSet RouteSet) Validate() error {
	if err := routeSet.ID.validate(); err != nil {
		return err
	}
	if err := validateRevision("RouteSet", routeSet.Revision); err != nil {
		return err
	}
	if err := routeSet.AccessID.validate(); err != nil {
		return err
	}
	if len(routeSet.CandidateProfileIDs) == 0 ||
		len(routeSet.CandidateProfileIDs) > MaxEndpointProfiles {
		return fmt.Errorf("%w: RouteSet candidates are invalid", ErrInvalidAccess)
	}
	seen := make(map[EndpointProfileID]struct{}, len(routeSet.CandidateProfileIDs))
	for _, profileID := range routeSet.CandidateProfileIDs {
		if err := profileID.validate(); err != nil {
			return err
		}
		if _, duplicate := seen[profileID]; duplicate {
			return fmt.Errorf(
				"%w: RouteSet candidate %q is repeated",
				ErrInvalidAccess,
				profileID.String(),
			)
		}
		seen[profileID] = struct{}{}
	}
	if !routeSet.FallbackMode().valid() {
		return fmt.Errorf("%w: RouteSet fallback is invalid", ErrInvalidAccess)
	}
	// A policy that allows a further attempt with nothing further to try is a
	// promise the plan cannot keep.
	if routeSet.FallbackMode().Allows() &&
		len(routeSet.CandidateProfileIDs) < 2 {
		return fmt.Errorf(
			"%w: RouteSet allows fallback with one candidate",
			ErrInvalidAccess,
		)
	}
	return nil
}

type AccessEgressPolicy struct {
	ID       EgressPolicyID
	Revision Revision
	AccessID AccessID
	Mode     EgressMode
}

func (policy AccessEgressPolicy) Validate() error {
	if err := policy.ID.validate(); err != nil {
		return err
	}
	if err := validateRevision("AccessEgressPolicy", policy.Revision); err != nil {
		return err
	}
	if err := policy.AccessID.validate(); err != nil {
		return err
	}
	if policy.Mode == "" {
		return fmt.Errorf("%w: egress mode is empty", ErrInvalidAccess)
	}
	return nil
}

type PluginPlan struct {
	Revision   Revision
	AccessID   AccessID
	Mode       PluginPlanMode
	BindingIDs []PluginBindingID
}

func (plan PluginPlan) Validate() error {
	if err := validateRevision("PluginPlan", plan.Revision); err != nil {
		return err
	}
	if err := plan.AccessID.validate(); err != nil {
		return err
	}
	if plan.Mode == "" {
		return fmt.Errorf("%w: plugin plan mode is empty", ErrInvalidAccess)
	}
	if len(plan.BindingIDs) > MaxPluginBindings {
		return fmt.Errorf("%w: plugin binding count exceeds the limit", ErrInvalidAccess)
	}
	for _, bindingID := range plan.BindingIDs {
		if err := bindingID.validate(); err != nil {
			return err
		}
	}
	return nil
}

// Aggregate is the only persisted Access configuration model.
type Aggregate struct {
	Binding         AccessBinding
	AgentEndpoint   AgentEndpoint
	Profiles        []EndpointProfile
	ProviderTargets []ProviderTarget
	AccountBindings []ProviderAccountBinding
	RouteSets       []RouteSet
	EgressPolicy    AccessEgressPolicy
	PluginPlan      PluginPlan
}

func (aggregate Aggregate) Validate() error {
	if err := aggregate.Binding.Validate(); err != nil {
		return err
	}
	if err := aggregate.AgentEndpoint.Validate(); err != nil {
		return err
	}
	if len(aggregate.Profiles) == 0 || len(aggregate.Profiles) > MaxEndpointProfiles {
		return fmt.Errorf("%w: EndpointProfile count is invalid", ErrInvalidAccess)
	}
	if len(aggregate.ProviderTargets) == 0 ||
		len(aggregate.ProviderTargets) > MaxEndpointProfiles {
		return fmt.Errorf("%w: ProviderTarget count is invalid", ErrInvalidAccess)
	}
	if len(aggregate.AccountBindings) > MaxAccountBindings {
		return fmt.Errorf("%w: account binding count is invalid", ErrInvalidAccess)
	}
	if len(aggregate.RouteSets) == 0 || len(aggregate.RouteSets) > MaxRouteSets {
		return fmt.Errorf("%w: RouteSet count is invalid", ErrInvalidAccess)
	}
	for _, profile := range aggregate.Profiles {
		if err := profile.Validate(); err != nil {
			return err
		}
	}
	for _, target := range aggregate.ProviderTargets {
		if err := target.Validate(); err != nil {
			return err
		}
	}
	for _, account := range aggregate.AccountBindings {
		if err := account.Validate(); err != nil {
			return err
		}
	}
	for _, routeSet := range aggregate.RouteSets {
		if err := routeSet.Validate(); err != nil {
			return err
		}
	}
	if err := aggregate.EgressPolicy.Validate(); err != nil {
		return err
	}
	return aggregate.PluginPlan.Validate()
}

func (aggregate Aggregate) Clone() Aggregate {
	cloned := aggregate
	cloned.Binding.ProfileIDs = slices.Clone(aggregate.Binding.ProfileIDs)
	cloned.Profiles = make([]EndpointProfile, len(aggregate.Profiles))
	for index, profile := range aggregate.Profiles {
		cloned.Profiles[index] = profile
		cloned.Profiles[index].AccountBindingIDs = slices.Clone(profile.AccountBindingIDs)
	}
	cloned.ProviderTargets = make([]ProviderTarget, len(aggregate.ProviderTargets))
	for index, target := range aggregate.ProviderTargets {
		cloned.ProviderTargets[index] = target
		cloned.ProviderTargets[index].Capabilities = slices.Clone(target.Capabilities)
	}
	cloned.AccountBindings = slices.Clone(aggregate.AccountBindings)
	cloned.RouteSets = make([]RouteSet, len(aggregate.RouteSets))
	for index, routeSet := range aggregate.RouteSets {
		cloned.RouteSets[index] = routeSet
		cloned.RouteSets[index].CandidateProfileIDs = slices.Clone(routeSet.CandidateProfileIDs)
	}
	cloned.PluginPlan.BindingIDs = slices.Clone(aggregate.PluginPlan.BindingIDs)
	return cloned
}

func (aggregate Aggregate) Equal(other Aggregate) bool {
	if aggregate.Binding.ID != other.Binding.ID ||
		aggregate.Binding.Revision != other.Binding.Revision ||
		aggregate.Binding.Name != other.Binding.Name ||
		aggregate.Binding.Description != other.Binding.Description ||
		aggregate.Binding.Status != other.Binding.Status ||
		aggregate.Binding.AgentEndpointID != other.Binding.AgentEndpointID ||
		aggregate.Binding.DefaultRouteSetID != other.Binding.DefaultRouteSetID ||
		aggregate.Binding.EgressPolicyID != other.Binding.EgressPolicyID ||
		!slices.Equal(aggregate.Binding.ProfileIDs, other.Binding.ProfileIDs) ||
		aggregate.AgentEndpoint != other.AgentEndpoint ||
		aggregate.EgressPolicy != other.EgressPolicy ||
		aggregate.PluginPlan.Revision != other.PluginPlan.Revision ||
		aggregate.PluginPlan.AccessID != other.PluginPlan.AccessID ||
		aggregate.PluginPlan.Mode != other.PluginPlan.Mode ||
		!slices.Equal(aggregate.PluginPlan.BindingIDs, other.PluginPlan.BindingIDs) ||
		len(aggregate.Profiles) != len(other.Profiles) ||
		len(aggregate.ProviderTargets) != len(other.ProviderTargets) ||
		len(aggregate.AccountBindings) != len(other.AccountBindings) ||
		len(aggregate.RouteSets) != len(other.RouteSets) {
		return false
	}
	for index := range aggregate.Profiles {
		left, right := aggregate.Profiles[index], other.Profiles[index]
		if left.ID != right.ID ||
			left.Revision != right.Revision ||
			left.AccessID != right.AccessID ||
			left.Kind != right.Kind ||
			left.CredentialSource != right.CredentialSource ||
			left.ProcessingMode != right.ProcessingMode ||
			left.Name != right.Name ||
			left.Description != right.Description ||
			left.BackendDialect != right.BackendDialect ||
			left.TargetID != right.TargetID ||
			left.DefaultModelPolicy != right.DefaultModelPolicy ||
			left.DefaultAccountBindingID != right.DefaultAccountBindingID ||
			!slices.Equal(left.AccountBindingIDs, right.AccountBindingIDs) {
			return false
		}
	}
	for index := range aggregate.ProviderTargets {
		left, right := aggregate.ProviderTargets[index], other.ProviderTargets[index]
		if left.ID != right.ID ||
			left.Revision != right.Revision ||
			left.AccessID != right.AccessID ||
			left.ProfileID != right.ProfileID ||
			left.Origin != right.Origin ||
			left.Protocol != right.Protocol ||
			!slices.Equal(left.Capabilities, right.Capabilities) {
			return false
		}
	}
	if !slices.Equal(aggregate.AccountBindings, other.AccountBindings) {
		return false
	}
	for index := range aggregate.RouteSets {
		left, right := aggregate.RouteSets[index], other.RouteSets[index]
		if left.ID != right.ID ||
			left.Revision != right.Revision ||
			left.AccessID != right.AccessID ||
			!slices.Equal(left.CandidateProfileIDs, right.CandidateProfileIDs) {
			return false
		}
	}
	return true
}

func validateRevision(label string, revision Revision) error {
	if revision == 0 || revision > MaxRevision {
		return fmt.Errorf("%w: %s revision is invalid", ErrInvalidAccess, label)
	}
	return nil
}
