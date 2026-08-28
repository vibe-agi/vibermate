package environment

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type CandidateDigest [sha256.Size]byte

func (digest CandidateDigest) String() string { return hex.EncodeToString(digest[:]) }

func ParseCandidateDigest(value string) (CandidateDigest, error) {
	var digest CandidateDigest
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(digest) {
		return digest, fmt.Errorf("%w: candidate digest is invalid", ErrInvalidEnvironment)
	}
	copy(digest[:], decoded)
	return digest, nil
}

func (environment Environment) Clone() Environment {
	cloned := environment
	cloned.LaunchEnvironment = environment.LaunchEnvironment.Clone()
	if environment.PolicySet != nil {
		policy := *environment.PolicySet
		cloned.PolicySet = &policy
	}
	cloned.ClientEndpoints = make([]ClientEndpoint, len(environment.ClientEndpoints))
	for endpointIndex, endpoint := range environment.ClientEndpoints {
		cloned.ClientEndpoints[endpointIndex] = cloneEndpoint(endpoint)
	}
	cloned.PluginBindings = slices.Clone(environment.PluginBindings)
	cloned.RetiredChildIdentities = slices.Clone(environment.RetiredChildIdentities)
	return cloned
}

// Validate applies the complete structural graph invariants. Account catalog
// compatibility is applied by Compiler because it depends on external,
// non-secret resource evidence.
func (environment Environment) Validate() error {
	_, err := normalize(environment)
	return err
}

func cloneEndpoint(endpoint ClientEndpoint) ClientEndpoint {
	cloned := endpoint
	cloned.ProtocolPlans = make([]ClientProtocolPlan, len(endpoint.ProtocolPlans))
	for planIndex, plan := range endpoint.ProtocolPlans {
		cloned.ProtocolPlans[planIndex] = cloneProtocolPlan(plan)
	}
	return cloned
}

func cloneProtocolPlan(plan ClientProtocolPlan) ClientProtocolPlan {
	cloned := plan
	cloned.Transforms = slices.Clone(plan.Transforms)
	cloned.PluginBindings = slices.Clone(plan.PluginBindings)
	if plan.Destination.Upstream != nil {
		upstream := *plan.Destination.Upstream
		upstream.RouteSet.CandidateRouteIDs = slices.Clone(
			plan.Destination.Upstream.RouteSet.CandidateRouteIDs,
		)
		upstream.Routes = make([]UpstreamRoute, len(plan.Destination.Upstream.Routes))
		for routeIndex, route := range plan.Destination.Upstream.Routes {
			upstream.Routes[routeIndex] = cloneRoute(route)
		}
		cloned.Destination.Upstream = &upstream
	}
	return cloned
}

func cloneRoute(route UpstreamRoute) UpstreamRoute {
	cloned := route
	cloned.PluginBindings = slices.Clone(route.PluginBindings)
	cloned.ProviderTarget.Capabilities = slices.Clone(route.ProviderTarget.Capabilities)
	cloned.AccountPolicy.CandidateAccountIDs = slices.Clone(route.AccountPolicy.CandidateAccountIDs)
	cloned.AccountPolicy.AccountRevisions = cloneRevisionMap(route.AccountPolicy.AccountRevisions)
	cloned.ModelPolicy = cloneModelPolicy(route.ModelPolicy)
	return cloned
}

func cloneModelPolicy(policy ModelPolicy) ModelPolicy {
	cloned := policy
	cloned.Mappings = slices.Clone(policy.Mappings)
	return cloned
}

func cloneRevisionMap(source map[string]Revision) map[string]Revision {
	if source == nil {
		return nil
	}
	cloned := make(map[string]Revision, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

// CanonicalJSON validates and deterministically orders the full graph before
// encoding it. Persisted bytes and candidate digests share this one codec.
func CanonicalJSON(environment Environment) ([]byte, error) {
	return canonicalJSON(environment, false)
}

func canonicalSystemJSON(environment Environment) ([]byte, error) {
	return canonicalJSON(environment, true)
}

func canonicalJSON(environment Environment, allowSystem bool) ([]byte, error) {
	var normalized Environment
	var err error
	if allowSystem {
		normalized, err = normalizeSystem(environment)
	} else {
		normalized, err = normalize(environment)
	}
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("encode Environment aggregate: %w", err)
	}
	return encoded, nil
}

func DecodeCanonicalJSON(encoded []byte) (Environment, error) {
	var aggregate Environment
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&aggregate); err != nil {
		return Environment{}, fmt.Errorf("%w: decode aggregate: %w", ErrInvalidRepositoryState, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Environment{}, fmt.Errorf("%w: aggregate has trailing JSON", ErrInvalidRepositoryState)
	}
	canonical, err := CanonicalJSON(aggregate)
	if err != nil {
		return Environment{}, fmt.Errorf("%w: %w", ErrInvalidRepositoryState, err)
	}
	if !bytes.Equal(canonical, encoded) {
		return Environment{}, fmt.Errorf("%w: aggregate JSON is not canonical", ErrInvalidRepositoryState)
	}
	return aggregate.Clone(), nil
}

func Digest(environment Environment) (CandidateDigest, error) {
	encoded, err := CanonicalJSON(environment)
	if err != nil {
		return CandidateDigest{}, err
	}
	return sha256.Sum256(encoded), nil
}

func normalize(input Environment) (Environment, error) {
	return normalizeRoot(input, false)
}

func normalizeSystem(input Environment) (Environment, error) {
	return normalizeRoot(input, true)
}

func normalizeRoot(input Environment, allowSystem bool) (Environment, error) {
	value := input.Clone()
	if err := validateRoot(value, allowSystem); err != nil {
		return Environment{}, err
	}
	value.LaunchEnvironment, _ = normalizeLaunchEnvironment(value.LaunchEnvironment)
	// Observe is the canonical zero/default. Explicit Observe and an omitted
	// policy are one authority and therefore one digest.
	if value.EffectivePolicySet() == DefaultPolicySet() {
		value.PolicySet = nil
	}
	sort.Slice(value.ClientEndpoints, func(left, right int) bool {
		return value.ClientEndpoints[left].ID < value.ClientEndpoints[right].ID
	})
	sort.Slice(value.PluginBindings, func(left, right int) bool {
		return value.PluginBindings[left].ID < value.PluginBindings[right].ID
	})
	sort.Slice(value.RetiredChildIdentities, func(left, right int) bool {
		leftKey := childIdentityKey(value.RetiredChildIdentities[left].Kind, value.RetiredChildIdentities[left].ID)
		rightKey := childIdentityKey(value.RetiredChildIdentities[right].Kind, value.RetiredChildIdentities[right].ID)
		return leftKey < rightKey
	})

	origins := make(map[string]struct{}, len(value.ClientEndpoints))
	endpointIDs := make(map[ClientEndpointID]struct{}, len(value.ClientEndpoints))
	planIDs := make(map[ClientProtocolPlanID]struct{})
	routeIDs := make(map[UpstreamRouteID]struct{})
	for endpointIndex := range value.ClientEndpoints {
		endpoint := &value.ClientEndpoints[endpointIndex]
		if err := validateChildID("ClientEndpoint", string(endpoint.ID), endpoint.Revision); err != nil {
			return Environment{}, err
		}
		if _, exists := endpointIDs[endpoint.ID]; exists {
			return Environment{}, duplicate("ClientEndpoint ID", endpoint.ID.String())
		}
		endpointIDs[endpoint.ID] = struct{}{}
		if err := endpoint.ClientOrigin.Validate(); err != nil {
			return Environment{}, err
		}
		if _, exists := origins[endpoint.ClientOrigin.String()]; exists {
			return Environment{}, duplicate("ClientOrigin", endpoint.ClientOrigin.String())
		}
		origins[endpoint.ClientOrigin.String()] = struct{}{}
		if len(endpoint.ProtocolPlans) == 0 {
			return Environment{}, fmt.Errorf("%w: ClientEndpoint %q has no protocol plan", ErrInvalidEnvironment, endpoint.ID)
		}
		sort.Slice(endpoint.ProtocolPlans, func(left, right int) bool {
			return endpoint.ProtocolPlans[left].ID < endpoint.ProtocolPlans[right].ID
		})
		protocols := make(map[ClientProtocol]struct{}, len(endpoint.ProtocolPlans))
		for planIndex := range endpoint.ProtocolPlans {
			plan := &endpoint.ProtocolPlans[planIndex]
			if err := validateChildID("ClientProtocolPlan", string(plan.ID), plan.Revision); err != nil {
				return Environment{}, err
			}
			if _, exists := planIDs[plan.ID]; exists {
				return Environment{}, duplicate("ClientProtocolPlan ID", plan.ID.String())
			}
			planIDs[plan.ID] = struct{}{}
			if !validProtocol(plan.ClientProtocol) {
				return Environment{}, fmt.Errorf("%w: protocol plan %q has an invalid client protocol", ErrInvalidEnvironment, plan.ID)
			}
			if _, exists := protocols[plan.ClientProtocol]; exists {
				return Environment{}, duplicate("ClientProtocol within ClientEndpoint", string(plan.ClientProtocol))
			}
			protocols[plan.ClientProtocol] = struct{}{}
			if err := validateNamedRevision("ClientAdapterPolicy", plan.ClientAdapterPolicy.ID, plan.ClientAdapterPolicy.Revision, true); err != nil {
				return Environment{}, err
			}
			if err := plan.EgressProfile.Validate(); err != nil {
				return Environment{}, fmt.Errorf(
					"%w: protocol plan %q egress profile: %v",
					ErrInvalidEnvironment,
					plan.ID,
					err,
				)
			}
			for _, transform := range plan.Transforms {
				if err := transform.Validate(); err != nil {
					return Environment{}, fmt.Errorf(
						"%w: protocol plan %q message transform: %v",
						ErrInvalidEnvironment,
						plan.ID,
						err,
					)
				}
			}
			sort.Slice(plan.PluginBindings, func(left, right int) bool { return plan.PluginBindings[left].ID < plan.PluginBindings[right].ID })
			if err := validatePluginBindings(plan.PluginBindings); err != nil {
				return Environment{}, err
			}
			switch plan.Destination.Kind {
			case DestinationKindOriginal:
				if plan.Destination.Upstream != nil {
					return Environment{}, fmt.Errorf(
						"%w: original Destination %q contains upstream authority",
						ErrInvalidEnvironment,
						plan.ID,
					)
				}
			case DestinationKindUpstream:
				if plan.Destination.Upstream == nil {
					return Environment{}, fmt.Errorf(
						"%w: upstream Destination %q has no UpstreamPlan",
						ErrInvalidEnvironment,
						plan.ID,
					)
				}
				if err := normalizeUpstreamPlan(plan, routeIDs); err != nil {
					return Environment{}, err
				}
			default:
				return Environment{}, fmt.Errorf(
					"%w: protocol plan %q has an invalid Destination",
					ErrInvalidEnvironment,
					plan.ID,
				)
			}
		}
	}
	if err := validatePluginBindings(value.PluginBindings); err != nil {
		return Environment{}, err
	}
	if err := validateRetiredChildIdentities(value); err != nil {
		return Environment{}, err
	}
	return value, nil
}

func normalizeUpstreamPlan(
	plan *ClientProtocolPlan,
	routeIDs map[UpstreamRouteID]struct{},
) error {
	upstream := plan.Destination.Upstream
	if upstream == nil {
		return ErrInvalidEnvironment
	}
	if err := validateNamedRevision(
		"RouteSet",
		upstream.RouteSet.ID,
		upstream.RouteSet.Revision,
		true,
	); err != nil {
		return err
	}
	if len(upstream.RouteSet.CandidateRouteIDs) == 0 {
		return fmt.Errorf(
			"%w: protocol plan %q has an empty RouteSet",
			ErrInvalidEnvironment,
			plan.ID,
		)
	}
	sort.Slice(upstream.Routes, func(left, right int) bool {
		return upstream.Routes[left].ID < upstream.Routes[right].ID
	})
	defaultFound := false
	candidateIDs := make(
		map[UpstreamRouteID]struct{},
		len(upstream.RouteSet.CandidateRouteIDs),
	)
	for _, routeID := range upstream.RouteSet.CandidateRouteIDs {
		if err := validateID("RouteSet candidate route ID", routeID.String()); err != nil {
			return err
		}
		if _, duplicate := candidateIDs[routeID]; duplicate {
			return fmt.Errorf(
				"%w: RouteSet candidate %q is duplicated",
				ErrInvalidEnvironment,
				routeID,
			)
		}
		candidateIDs[routeID] = struct{}{}
	}
	for routeIndex := range upstream.Routes {
		route := &upstream.Routes[routeIndex]
		if err := validateChildID("UpstreamRoute", string(route.ID), route.Revision); err != nil {
			return err
		}
		if _, exists := routeIDs[route.ID]; exists {
			return duplicate("UpstreamRoute ID", route.ID.String())
		}
		routeIDs[route.ID] = struct{}{}
		defaultFound = defaultFound || route.ID == upstream.DefaultRouteID
		if _, candidate := candidateIDs[route.ID]; !candidate {
			return fmt.Errorf(
				"%w: route %q is outside its RouteSet",
				ErrInvalidEnvironment,
				route.ID,
			)
		}
		if err := validateRoute(route); err != nil {
			return err
		}
	}
	if len(upstream.Routes) == 0 || len(candidateIDs) != len(upstream.Routes) ||
		upstream.DefaultRouteID == "" || !defaultFound {
		return fmt.Errorf(
			"%w: protocol plan %q has a bad default route",
			ErrInvalidEnvironment,
			plan.ID,
		)
	}
	return nil
}

func validateRoot(value Environment, allowSystem bool) error {
	if err := validateID("Environment ID", value.ID.String()); err != nil {
		return err
	}
	if value.ID == SystemTransparentID && !allowSystem {
		return ErrSystemEnvironment
	}
	if value.Revision == 0 || value.Revision > MaxRevision || value.Name == "" ||
		len(value.Name) > MaxNameBytes || !utf8.ValidString(value.Name) ||
		strings.TrimSpace(value.Name) != value.Name {
		return fmt.Errorf("%w: root name or revision is invalid", ErrInvalidEnvironment)
	}
	for _, character := range value.Name {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: root name contains a control character", ErrInvalidEnvironment)
		}
	}
	if value.State != StateActive && value.State != StateDisabled {
		return fmt.Errorf("%w: state must be active or disabled", ErrInvalidEnvironment)
	}
	if err := value.ContentRecording.Validate(); err != nil {
		return err
	}
	if err := value.EffectivePolicySet().Validate(); err != nil {
		return err
	}
	if err := value.LaunchEnvironment.Validate(); err != nil {
		return err
	}
	return nil
}

func validateChildID(label, id string, revision Revision) error {
	if err := validateID(label+" ID", id); err != nil {
		return err
	}
	if revision == 0 || revision > MaxRevision {
		return fmt.Errorf("%w: %s %q revision is invalid", ErrInvalidEnvironment, label, id)
	}
	return nil
}

func validateNamedRevision(label, id string, revision Revision, required bool) error {
	if !required && id == "" && revision == 0 {
		return nil
	}
	if err := validateID(label+" ID", id); err != nil {
		return err
	}
	if revision == 0 || revision > MaxRevision {
		return fmt.Errorf("%w: %s revision is invalid", ErrInvalidEnvironment, label)
	}
	return nil
}

func validateRoute(route *UpstreamRoute) error {
	if err := validateID("backend protocol", route.BackendProtocol); err != nil {
		return fmt.Errorf("%w: route %q backend protocol is invalid", ErrInvalidEnvironment, route.ID)
	}
	if err := validateNamedRevision("ProviderTarget", route.ProviderTarget.ID, route.ProviderTarget.Revision, true); err != nil {
		return err
	}
	if err := route.ProviderTarget.Origin.Validate(); err != nil {
		return err
	}
	if err := validateID("ProviderAuthRealm ID", route.ProviderTarget.RealmID); err != nil {
		return err
	}
	sort.Slice(route.ProviderTarget.Capabilities, func(left, right int) bool {
		return route.ProviderTarget.Capabilities[left] < route.ProviderTarget.Capabilities[right]
	})
	if len(route.ProviderTarget.Capabilities) == 0 {
		return fmt.Errorf("%w: route %q target has no declared capabilities", ErrInvalidEnvironment, route.ID)
	}
	for index, capability := range route.ProviderTarget.Capabilities {
		if !capability.Valid() || index > 0 && capability == route.ProviderTarget.Capabilities[index-1] {
			return fmt.Errorf("%w: route %q target capabilities are invalid", ErrInvalidEnvironment, route.ID)
		}
	}
	if err := validateID("wire profile reference", route.WireProfileRef); err != nil {
		return fmt.Errorf("%w: route %q has an incomplete target", ErrInvalidEnvironment, route.ID)
	}
	policy := &route.AccountPolicy
	if policy.Revision == 0 || policy.Revision > MaxRevision ||
		(policy.FailoverPolicy != FailoverOff && policy.FailoverPolicy != FailoverAccountScopedSafe) {
		return fmt.Errorf("%w: route %q account policy is invalid", ErrInvalidEnvironment, route.ID)
	}
	if hasDuplicateUnsortedString(policy.CandidateAccountIDs) {
		return fmt.Errorf("%w: route %q account policy contains duplicates", ErrInvalidEnvironment, route.ID)
	}
	if len(policy.CandidateAccountIDs) == 0 ||
		!slices.Contains(policy.CandidateAccountIDs, policy.PreferredAccountID) {
		return fmt.Errorf("%w: upstream route %q has incomplete account references", ErrInvalidEnvironment, route.ID)
	}
	for _, id := range policy.CandidateAccountIDs {
		if err := validateID("ProviderAccount ID", id); err != nil || policy.AccountRevisions[id] == 0 {
			return fmt.Errorf("%w: upstream route %q has an invalid account reference", ErrInvalidEnvironment, route.ID)
		}
	}
	if len(policy.AccountRevisions) != len(policy.CandidateAccountIDs) {
		return fmt.Errorf("%w: upstream route %q has mutable account aliases", ErrInvalidEnvironment, route.ID)
	}
	sort.Slice(route.ModelPolicy.Mappings, func(left, right int) bool {
		if route.ModelPolicy.Mappings[left].RequestedModel != route.ModelPolicy.Mappings[right].RequestedModel {
			return route.ModelPolicy.Mappings[left].RequestedModel < route.ModelPolicy.Mappings[right].RequestedModel
		}
		return route.ModelPolicy.Mappings[left].UpstreamModel < route.ModelPolicy.Mappings[right].UpstreamModel
	})
	switch route.ModelPolicy.Mode {
	case ModelModePassthrough:
		if len(route.ModelPolicy.Mappings) != 0 {
			return fmt.Errorf("%w: route %q model policy has unexpected mappings", ErrInvalidEnvironment, route.ID)
		}
	case ModelModeMap:
		if len(route.ModelPolicy.Mappings) == 0 {
			return fmt.Errorf("%w: route %q model map is empty", ErrInvalidEnvironment, route.ID)
		}
		for index, mapping := range route.ModelPolicy.Mappings {
			if err := validateModelIdentifier("requested model", mapping.RequestedModel); err != nil {
				return fmt.Errorf("%w: route %q mapping %d has an invalid requested model", ErrInvalidEnvironment, route.ID, index)
			}
			if err := validateModelIdentifier("upstream model", mapping.UpstreamModel); err != nil {
				return fmt.Errorf("%w: route %q mapping %d has an invalid upstream model", ErrInvalidEnvironment, route.ID, index)
			}
			if index > 0 && route.ModelPolicy.Mappings[index-1].RequestedModel == mapping.RequestedModel {
				return fmt.Errorf("%w: route %q model map repeats requested model %q", ErrInvalidEnvironment, route.ID, mapping.RequestedModel)
			}
		}
	default:
		return fmt.Errorf("%w: route %q model policy is invalid", ErrInvalidEnvironment, route.ID)
	}
	if route.ModelPolicy.Revision == 0 || route.ModelPolicy.Revision > MaxRevision {
		return fmt.Errorf("%w: route %q model policy revision is invalid", ErrInvalidEnvironment, route.ID)
	}
	sort.Slice(route.PluginBindings, func(left, right int) bool { return route.PluginBindings[left].ID < route.PluginBindings[right].ID })
	return validatePluginBindings(route.PluginBindings)
}

func validateModelIdentifier(label, value string) error {
	if value == "" || len(value) > MaxNameBytes || !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is not canonical", ErrInvalidEnvironment, label)
	}
	for _, character := range value {
		if unicode.IsControl(character) || character == '\ufeff' {
			return fmt.Errorf("%w: %s contains a control character", ErrInvalidEnvironment, label)
		}
	}
	return nil
}

func validatePluginBindings(bindings []PluginBinding) error {
	seen := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if err := validateNamedRevision("PluginBinding", binding.ID, binding.Revision, true); err != nil {
			return fmt.Errorf("%w: PluginBinding is invalid", ErrInvalidEnvironment)
		}
		if err := validateID("Plugin ID", binding.PluginID); err != nil {
			return fmt.Errorf("%w: PluginBinding is invalid", ErrInvalidEnvironment)
		}
		if _, exists := seen[binding.ID]; exists {
			return duplicate("PluginBinding ID", binding.ID)
		}
		seen[binding.ID] = struct{}{}
	}
	return nil
}

func childIdentityKey(kind ChildIdentityKind, id string) string {
	return string(kind) + "\x00" + id
}

func validateRetiredChildIdentities(value Environment) error {
	if len(value.RetiredChildIdentities) > MaxRetiredChildIdentities {
		return fmt.Errorf("%w: retired child identity history exceeds its bound", ErrInvalidEnvironment)
	}
	active := currentChildIdentities(value)
	seen := make(map[string]struct{}, len(value.RetiredChildIdentities))
	for _, identity := range value.RetiredChildIdentities {
		key := childIdentityKey(identity.Kind, identity.ID)
		if _, exists := seen[key]; exists {
			return duplicate("retired child identity", key)
		}
		seen[key] = struct{}{}
		if _, exists := active[key]; exists {
			return fmt.Errorf("%w: active child %q is also retired", ErrInvalidEnvironment, identity.ID)
		}
		if identity.RetiredAtRevision == 0 || identity.RetiredAtRevision > value.Revision {
			return fmt.Errorf("%w: retired child %q has an invalid revision", ErrInvalidEnvironment, identity.ID)
		}
		switch identity.Kind {
		case ChildIdentityClientEndpoint:
			if err := validateID("retired ClientEndpoint ID", identity.ID); err != nil || identity.ParentID != value.ID.String() {
				return fmt.Errorf("%w: retired ClientEndpoint identity is invalid", ErrInvalidEnvironment)
			}
		case ChildIdentityClientProtocolPlan:
			if err := validateID("retired ClientProtocolPlan ID", identity.ID); err != nil {
				return err
			}
			if err := validateID("retired ClientProtocolPlan parent ID", identity.ParentID); err != nil {
				return err
			}
		case ChildIdentityUpstreamRoute:
			if err := validateID("retired UpstreamRoute ID", identity.ID); err != nil {
				return err
			}
			if err := validateID("retired UpstreamRoute parent ID", identity.ParentID); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: retired child identity kind is invalid", ErrInvalidEnvironment)
		}
	}
	return nil
}

func currentChildIdentities(value Environment) map[string]RetiredChildIdentity {
	identities := make(map[string]RetiredChildIdentity)
	for _, endpoint := range value.ClientEndpoints {
		identity := RetiredChildIdentity{
			Kind: ChildIdentityClientEndpoint, ID: endpoint.ID.String(), ParentID: value.ID.String(),
		}
		identities[childIdentityKey(identity.Kind, identity.ID)] = identity
		for _, plan := range endpoint.ProtocolPlans {
			identity = RetiredChildIdentity{
				Kind: ChildIdentityClientProtocolPlan, ID: plan.ID.String(), ParentID: endpoint.ID.String(),
			}
			identities[childIdentityKey(identity.Kind, identity.ID)] = identity
			for _, route := range destinationRoutes(plan.Destination) {
				identity = RetiredChildIdentity{
					Kind: ChildIdentityUpstreamRoute, ID: route.ID.String(), ParentID: plan.ID.String(),
				}
				identities[childIdentityKey(identity.Kind, identity.ID)] = identity
			}
		}
	}
	return identities
}

func duplicate(label, value string) error {
	return fmt.Errorf("%w: duplicate %s %q", ErrInvalidEnvironment, label, value)
}

func validProtocol(value ClientProtocol) bool {
	return value == ClientProtocolAnthropicMessages || value == ClientProtocolOpenAIResponses || value == ClientProtocolOpenAIChat
}

func hasDuplicateString(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index] == values[index-1] {
			return true
		}
	}
	return false
}

func hasDuplicateUnsortedString(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func validateAccounts(aggregate Environment, catalog AccountCatalog) error {
	for _, endpoint := range aggregate.ClientEndpoints {
		for _, plan := range endpoint.ProtocolPlans {
			for _, route := range destinationRoutes(plan.Destination) {
				policy := route.AccountPolicy
				if catalog == nil {
					return fmt.Errorf("%w: upstream route %q has no account catalog", ErrInvalidEnvironment, route.ID)
				}
				for _, accountID := range policy.CandidateAccountIDs {
					account, exists := catalog.LookupAccount(accountID)
					if !exists || !account.Active || account.ID != accountID ||
						account.Revision != policy.AccountRevisions[accountID] ||
						account.UpstreamEndpointID != route.ProviderTarget.ID ||
						account.UpstreamEndpointRevision != route.ProviderTarget.Revision ||
						account.RealmID != route.ProviderTarget.RealmID ||
						!slices.Contains(account.BackendProtocols, route.BackendProtocol) {
						return fmt.Errorf("%w: route %q account %q is incompatible", ErrInvalidEnvironment, route.ID, accountID)
					}
				}
			}
		}
	}
	return nil
}

func destinationRoutes(destination DestinationPlan) []UpstreamRoute {
	if destination.Kind != DestinationKindUpstream || destination.Upstream == nil {
		return nil
	}
	return destination.Upstream.Routes
}
