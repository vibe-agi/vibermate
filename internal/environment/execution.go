package environment

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

var (
	ErrCompilerUnavailable      = errors.New("Environment compiler dependencies are unavailable")
	ErrClientProtocolNotMatched = errors.New("request does not match an Environment client protocol")
	ErrClientProtocolAmbiguous  = errors.New("request matches more than one Environment client protocol")
	ErrRouteOutsideRouteSet     = errors.New("route is outside the frozen RouteSet")
	ErrUnsupportedPluginBinding = errors.New("plugin bindings are not executable in this runtime slice")
)

type ProtocolCatalog interface {
	Resolve(protocolspec.Dialect, protocolspec.Dialect) (protocolspec.CodecPlan, error)
	OperationsForDialect(protocolspec.Dialect) ([]protocolspec.ClientOperationDefinition, error)
}

type WireProfileCatalog interface {
	Resolve(wireprofile.UpstreamWireProfileRef) (wireprofile.CompiledUpstreamWireProfile, error)
}

type Compiler struct {
	accounts     AccountCatalog
	endpoints    upstreamendpoint.Catalog
	protocols    ProtocolCatalog
	wireProfiles WireProfileCatalog
}

func NewCompiler(
	accounts AccountCatalog,
	endpoints upstreamendpoint.Catalog,
	protocols ProtocolCatalog,
	wireProfiles WireProfileCatalog,
) (Compiler, error) {
	if protocols == nil || wireProfiles == nil {
		return Compiler{}, ErrCompilerUnavailable
	}
	return Compiler{
		accounts: accounts, endpoints: endpoints, protocols: protocols, wireProfiles: wireProfiles,
	}, nil
}

type CompiledAccountReference struct {
	ID                       string
	Revision                 Revision
	UpstreamEndpointID       string
	UpstreamEndpointRevision Revision
	RealmID                  string
}

type CompiledAccountPolicy struct {
	revision   Revision
	preferred  string
	candidates []CompiledAccountReference
	failover   FailoverPolicy
}

func (policy CompiledAccountPolicy) Revision() Revision             { return policy.revision }
func (policy CompiledAccountPolicy) PreferredAccountID() string     { return policy.preferred }
func (policy CompiledAccountPolicy) FailoverPolicy() FailoverPolicy { return policy.failover }
func (policy CompiledAccountPolicy) CandidateAccounts() []CompiledAccountReference {
	return slices.Clone(policy.candidates)
}

type CompiledRoutePlan struct {
	id            UpstreamRouteID
	revision      Revision
	target        ProviderTarget
	backend       protocolspec.Dialect
	codec         protocolspec.CodecPlan
	accountPolicy CompiledAccountPolicy
	modelPolicy   ModelPolicy
	wireProfile   wireprofile.CompiledUpstreamWireProfile
}

func (route CompiledRoutePlan) ID() UpstreamRouteID                   { return route.id }
func (route CompiledRoutePlan) Revision() Revision                    { return route.revision }
func (route CompiledRoutePlan) BackendProtocol() protocolspec.Dialect { return route.backend }
func (route CompiledRoutePlan) CodecPlan() protocolspec.CodecPlan     { return route.codec }
func (route CompiledRoutePlan) AccountPolicy() CompiledAccountPolicy {
	return cloneCompiledAccountPolicy(route.accountPolicy)
}

// ResolveModelMapping applies this compiled Route's exact requested-model
// mapping without exposing the control-plane policy representation to the
// Exchange hot path. An absent mapping preserves the requested identifier.
func (route CompiledRoutePlan) ResolveModelMapping(requested string) (string, bool) {
	return route.modelPolicy.ResolveMapping(requested)
}
func (route CompiledRoutePlan) WireProfile() wireprofile.CompiledUpstreamWireProfile {
	return route.wireProfile
}
func (route CompiledRoutePlan) ProviderTarget() ProviderTarget {
	return cloneProviderTarget(route.target)
}

type CompiledRouteSet struct {
	id         string
	revision   Revision
	defaultID  UpstreamRouteID
	candidates []UpstreamRouteID
	routes     map[UpstreamRouteID]CompiledRoutePlan
}

func (set CompiledRouteSet) ID() string                      { return set.id }
func (set CompiledRouteSet) Revision() Revision              { return set.revision }
func (set CompiledRouteSet) DefaultRouteID() UpstreamRouteID { return set.defaultID }
func (set CompiledRouteSet) CandidateRouteIDs() []UpstreamRouteID {
	return slices.Clone(set.candidates)
}
func (set CompiledRouteSet) Select(id UpstreamRouteID) (CompiledRoutePlan, error) {
	if !slices.Contains(set.candidates, id) {
		return CompiledRoutePlan{}, fmt.Errorf("%w: routeId=%q", ErrRouteOutsideRouteSet, id)
	}
	route, exists := set.routes[id]
	if !exists {
		return CompiledRoutePlan{}, fmt.Errorf("%w: routeId=%q", ErrRouteOutsideRouteSet, id)
	}
	return cloneCompiledRoute(route), nil
}
func (set CompiledRouteSet) DefaultRoute() (CompiledRoutePlan, error) {
	return set.Select(set.defaultID)
}

type CompiledProtocolPlan struct {
	id                  ClientProtocolPlanID
	revision            Revision
	dialect             protocolspec.Dialect
	adapterPolicy       ClientAdapterPolicy
	operations          []protocolspec.ClientOperationDefinition
	destinationKind     DestinationKind
	egressProfile       egressprofile.ProfileRevision
	transformPipeline   messagetransform.Pipeline
	originalCodec       protocolspec.CodecPlan
	originalWireProfile wireprofile.CompiledUpstreamWireProfile
	upstreamRouteSet    *CompiledRouteSet
}

func (plan CompiledProtocolPlan) ID() ClientProtocolPlanID            { return plan.id }
func (plan CompiledProtocolPlan) Revision() Revision                  { return plan.revision }
func (plan CompiledProtocolPlan) ClientDialect() protocolspec.Dialect { return plan.dialect }
func (plan CompiledProtocolPlan) ClientAdapterPolicy() ClientAdapterPolicy {
	return plan.adapterPolicy
}
func (plan CompiledProtocolPlan) Operations() []protocolspec.ClientOperationDefinition {
	return slices.Clone(plan.operations)
}
func (plan CompiledProtocolPlan) DestinationKind() DestinationKind {
	return plan.destinationKind
}
func (plan CompiledProtocolPlan) EgressPolicy() egressnetwork.Policy {
	return plan.egressProfile.Policy
}
func (plan CompiledProtocolPlan) EgressProfile() egressprofile.ProfileRevision {
	return plan.egressProfile
}
func (plan CompiledProtocolPlan) TransformPipeline() messagetransform.Pipeline {
	return plan.transformPipeline
}
func (plan CompiledProtocolPlan) UpstreamRouteSet() (CompiledRouteSet, bool) {
	if plan.upstreamRouteSet == nil {
		return CompiledRouteSet{}, false
	}
	return cloneCompiledRouteSet(*plan.upstreamRouteSet), true
}

type CompiledEndpointPlan struct {
	id                  ClientEndpointID
	revision            Revision
	origin              originidentity.ClientOrigin
	compatibilityDigest ConnectionCompatibilityDigest
	plans               []CompiledProtocolPlan
}

func (endpoint CompiledEndpointPlan) ID() ClientEndpointID { return endpoint.id }
func (endpoint CompiledEndpointPlan) Revision() Revision   { return endpoint.revision }
func (endpoint CompiledEndpointPlan) ClientOrigin() originidentity.ClientOrigin {
	return endpoint.origin
}
func (endpoint CompiledEndpointPlan) CompatibilityDigest() ConnectionCompatibilityDigest {
	return endpoint.compatibilityDigest
}
func (endpoint CompiledEndpointPlan) ProtocolPlans() []CompiledProtocolPlan {
	result := make([]CompiledProtocolPlan, len(endpoint.plans))
	for index, plan := range endpoint.plans {
		result[index] = cloneCompiledProtocol(plan)
	}
	return result
}

type RequestFacts struct {
	Target             protocolspec.RequestTarget
	DownstreamProtocol wireprofile.ApplicationProtocol
}

type RequestPlan struct {
	environmentID       EnvironmentID
	environmentRevision Revision
	environmentDigest   CandidateDigest
	contentRecording    ContentRecordingPolicy
	policySet           PolicySet
	endpoint            CompiledEndpointPlan
	protocol            CompiledProtocolPlan
	operation           protocolspec.ClientOperationDefinition
	destinationKind     DestinationKind
	egressProfile       egressprofile.ProfileRevision
	transformPipeline   messagetransform.Pipeline
	codecPlan           protocolspec.CodecPlan
	wireProfile         wireprofile.CompiledUpstreamWireProfile
	upstreamRoute       *CompiledRoutePlan
	wireVariant         wireprofile.CompiledUpstreamWireVariant
	originalOrigin      originidentity.ProviderOrigin
}

func (plan RequestPlan) EnvironmentID() EnvironmentID       { return plan.environmentID }
func (plan RequestPlan) EnvironmentRevision() Revision      { return plan.environmentRevision }
func (plan RequestPlan) EnvironmentDigest() CandidateDigest { return plan.environmentDigest }
func (plan RequestPlan) ContentRecording() ContentRecordingPolicy {
	return plan.contentRecording
}
func (plan RequestPlan) PolicySet() PolicySet           { return plan.policySet }
func (plan RequestPlan) Endpoint() CompiledEndpointPlan { return cloneCompiledEndpoint(plan.endpoint) }
func (plan RequestPlan) ProtocolPlan() CompiledProtocolPlan {
	return cloneCompiledProtocol(plan.protocol)
}
func (plan RequestPlan) Operation() protocolspec.ClientOperationDefinition { return plan.operation }
func (plan RequestPlan) PreservesOriginalDestination() bool {
	return plan.destinationKind == DestinationKindOriginal
}
func (plan RequestPlan) UsesUpstreamDestination() bool {
	return plan.destinationKind == DestinationKindUpstream
}
func (plan RequestPlan) EgressPolicy() egressnetwork.Policy { return plan.egressProfile.Policy }
func (plan RequestPlan) EgressProfile() egressprofile.ProfileRevision {
	return plan.egressProfile
}
func (plan RequestPlan) TransformPipeline() messagetransform.Pipeline {
	return plan.transformPipeline
}
func (plan RequestPlan) CodecPlan() protocolspec.CodecPlan { return plan.codecPlan }
func (plan RequestPlan) WireProfile() wireprofile.CompiledUpstreamWireProfile {
	return plan.wireProfile
}
func (plan RequestPlan) UpstreamRoute() (CompiledRoutePlan, bool) {
	if plan.upstreamRoute == nil {
		return CompiledRoutePlan{}, false
	}
	return cloneCompiledRoute(*plan.upstreamRoute), true
}
func (plan RequestPlan) WireVariant() wireprofile.CompiledUpstreamWireVariant {
	return plan.wireVariant
}
func (plan RequestPlan) OriginalOrigin() (originidentity.ProviderOrigin, bool) {
	if !plan.PreservesOriginalDestination() || plan.originalOrigin.Validate() != nil {
		return originidentity.ProviderOrigin{}, false
	}
	return plan.originalOrigin, true
}

func compileExecution(
	compiler Compiler,
	aggregate Environment,
) ([]CompiledEndpointPlan, map[string]int, error) {
	if compiler.protocols == nil || compiler.wireProfiles == nil {
		return nil, nil, ErrCompilerUnavailable
	}
	if len(aggregate.PluginBindings) != 0 {
		return nil, nil, ErrUnsupportedPluginBinding
	}
	compiled := make([]CompiledEndpointPlan, 0, len(aggregate.ClientEndpoints))
	byOrigin := make(map[string]int, len(aggregate.ClientEndpoints))
	for _, endpoint := range aggregate.ClientEndpoints {
		compiledEndpoint := CompiledEndpointPlan{
			id: endpoint.ID, revision: endpoint.Revision, origin: endpoint.ClientOrigin,
			plans: make([]CompiledProtocolPlan, 0, len(endpoint.ProtocolPlans)),
		}
		for _, plan := range endpoint.ProtocolPlans {
			if len(plan.PluginBindings) != 0 {
				return nil, nil, ErrUnsupportedPluginBinding
			}
			clientDialect, err := clientDialect(plan.ClientProtocol)
			if err != nil {
				return nil, nil, err
			}
			operations, err := compiler.protocols.OperationsForDialect(clientDialect)
			if err != nil {
				return nil, nil, fmt.Errorf("compile protocol plan %q operations: %w", plan.ID, err)
			}
			transformPolicies := make([]messagetransform.Policy, len(plan.Transforms))
			for index, transform := range plan.Transforms {
				transformPolicies[index] = transform.Policy
			}
			transformPipeline, err := messagetransform.CompilePipeline(
				transformPolicies, messagetransform.DefaultLimits(),
			)
			if err != nil {
				return nil, nil, fmt.Errorf(
					"compile protocol plan %q message transform: %w",
					plan.ID,
					err,
				)
			}
			compiledPlan := CompiledProtocolPlan{
				id: plan.ID, revision: plan.Revision, dialect: clientDialect,
				adapterPolicy:     plan.ClientAdapterPolicy,
				operations:        operations,
				destinationKind:   plan.Destination.Kind,
				egressProfile:     plan.EgressProfile,
				transformPipeline: transformPipeline,
			}
			switch plan.Destination.Kind {
			case DestinationKindOriginal:
				compiledPlan.originalCodec, err = compiler.protocols.Resolve(
					clientDialect,
					clientDialect,
				)
				if err != nil {
					return nil, nil, fmt.Errorf(
						"compile original Destination %q codec: %w",
						plan.ID,
						err,
					)
				}
				compiledPlan.originalWireProfile, err = compiler.wireProfiles.Resolve(
					wireprofile.FollowClientUpstreamWireProfileRef(),
				)
				if err != nil {
					return nil, nil, fmt.Errorf(
						"compile original Destination %q wire profile: %w",
						plan.ID,
						err,
					)
				}
			case DestinationKindUpstream:
				upstream := plan.Destination.Upstream
				if upstream == nil {
					return nil, nil, ErrInvalidEnvironment
				}
				routeSet := CompiledRouteSet{
					id:         upstream.RouteSet.ID,
					revision:   upstream.RouteSet.Revision,
					defaultID:  upstream.DefaultRouteID,
					candidates: slices.Clone(upstream.RouteSet.CandidateRouteIDs),
					routes:     make(map[UpstreamRouteID]CompiledRoutePlan, len(upstream.Routes)),
				}
				for _, route := range upstream.Routes {
					compiledRoute, err := compileRoute(
						compiler,
						route,
						clientDialect,
					)
					if err != nil {
						return nil, nil, err
					}
					routeSet.routes[route.ID] = compiledRoute
				}
				if _, err := routeSet.DefaultRoute(); err != nil {
					return nil, nil, err
				}
				compiledPlan.upstreamRouteSet = &routeSet
			default:
				return nil, nil, ErrInvalidEnvironment
			}
			compiledEndpoint.plans = append(compiledEndpoint.plans, compiledPlan)
		}
		compatibilityDigest, err := compileConnectionCompatibilityDigest(compiledEndpoint)
		if err != nil {
			return nil, nil, err
		}
		compiledEndpoint.compatibilityDigest = compatibilityDigest
		byOrigin[endpoint.ClientOrigin.String()] = len(compiled)
		compiled = append(compiled, compiledEndpoint)
	}
	return compiled, byOrigin, nil
}

// compileConnectionCompatibilityDigest hashes the downstream contract frozen
// into connection evidence. Provider routes, accounts, models, plugins,
// budgets, and egress are excluded because the Capture assignment already
// freezes the complete Environment revision that owns them.
func compileConnectionCompatibilityDigest(endpoint CompiledEndpointPlan) (ConnectionCompatibilityDigest, error) {
	type operationContract struct {
		ID               string                                `json:"id"`
		Revision         protocolspec.Revision                 `json:"revision"`
		Methods          []string                              `json:"methods"`
		PathPattern      string                                `json:"pathPattern"`
		PathMatch        protocolspec.ClientOperationPathMatch `json:"pathMatch"`
		Kind             protocolspec.ClientOperationKind      `json:"kind"`
		Transport        protocolspec.ClientOperationTransport `json:"transport"`
		BodyKind         protocolspec.ClientOperationBodyKind  `json:"bodyKind"`
		MaxBodyBytes     int64                                 `json:"maxBodyBytes"`
		AllowedQueries   []string                              `json:"allowedQueries"`
		AllowedQueryKeys []string                              `json:"allowedQueryKeys"`
	}
	type protocolContract struct {
		Dialect         protocolspec.Dialect `json:"dialect"`
		AdapterID       string               `json:"adapterId"`
		AdapterRevision Revision             `json:"adapterRevision"`
		Operations      []operationContract  `json:"operations"`
	}
	contracts := make([]protocolContract, 0, len(endpoint.plans))
	for _, plan := range endpoint.plans {
		contract := protocolContract{
			Dialect: plan.dialect, AdapterID: plan.adapterPolicy.ID,
			AdapterRevision: plan.adapterPolicy.Revision,
			Operations:      make([]operationContract, 0, len(plan.operations)),
		}
		for _, operation := range plan.operations {
			contract.Operations = append(contract.Operations, operationContract{
				ID: operation.ID().String(), Revision: operation.Revision(),
				Methods: operation.Methods(), PathPattern: operation.PathPattern(),
				PathMatch: operation.PathMatch(), Kind: operation.Kind(),
				Transport: operation.Transport(), BodyKind: operation.BodyKind(),
				MaxBodyBytes: operation.MaxBodyBytes(), AllowedQueries: operation.AllowedQueries(),
				AllowedQueryKeys: operation.AllowedQueryKeys(),
			})
		}
		contracts = append(contracts, contract)
	}
	encoded, err := json.Marshal(contracts)
	if err != nil {
		return ConnectionCompatibilityDigest{}, fmt.Errorf("encode connection compatibility: %w", err)
	}
	return ConnectionCompatibilityDigest(sha256.Sum256(encoded)), nil
}

func compileRoute(
	compiler Compiler,
	route UpstreamRoute,
	client protocolspec.Dialect,
) (CompiledRoutePlan, error) {
	if len(route.PluginBindings) != 0 {
		return CompiledRoutePlan{}, ErrUnsupportedPluginBinding
	}
	backend, err := backendDialect(route.BackendProtocol)
	if err != nil {
		return CompiledRoutePlan{}, fmt.Errorf("compile route %q: %w", route.ID, err)
	}
	if compiler.endpoints != nil {
		if err := validateUpstreamEndpointSnapshot(compiler.endpoints, route); err != nil {
			return CompiledRoutePlan{}, err
		}
	}
	codec, err := compiler.protocols.Resolve(client, backend)
	if err != nil {
		return CompiledRoutePlan{}, fmt.Errorf("compile route %q codec: %w", route.ID, err)
	}
	for _, required := range codec.RequiredCapabilities() {
		if !slices.Contains(route.ProviderTarget.Capabilities, required) {
			return CompiledRoutePlan{}, fmt.Errorf("%w: route %q target lacks %q", ErrInvalidEnvironment, route.ID, required)
		}
	}
	wireRef, err := wireprofile.NewUpstreamWireProfileRef(route.WireProfileRef)
	if err != nil {
		return CompiledRoutePlan{}, fmt.Errorf("compile route %q wire profile: %w", route.ID, err)
	}
	compiledWire, err := compiler.wireProfiles.Resolve(wireRef)
	if err != nil {
		return CompiledRoutePlan{}, fmt.Errorf("compile route %q wire profile: %w", route.ID, err)
	}
	accountPolicy, err := compileAccountPolicy(compiler.accounts, route)
	if err != nil {
		return CompiledRoutePlan{}, err
	}
	return CompiledRoutePlan{
		id: route.ID, revision: route.Revision,
		target: cloneProviderTarget(route.ProviderTarget), backend: backend,
		codec: codec, accountPolicy: accountPolicy, modelPolicy: cloneModelPolicy(route.ModelPolicy),
		wireProfile: compiledWire,
	}, nil
}

func compileAccountPolicy(catalog AccountCatalog, route UpstreamRoute) (CompiledAccountPolicy, error) {
	policy := route.AccountPolicy
	compiled := CompiledAccountPolicy{
		revision: policy.Revision, preferred: policy.PreferredAccountID,
		failover: policy.FailoverPolicy,
	}
	if catalog == nil {
		return CompiledAccountPolicy{}, fmt.Errorf("%w: upstream route %q has no account catalog", ErrInvalidEnvironment, route.ID)
	}
	for _, accountID := range policy.CandidateAccountIDs {
		account, exists := catalog.LookupAccount(accountID)
		if !exists {
			return CompiledAccountPolicy{}, fmt.Errorf("%w: managed account %q disappeared", ErrInvalidEnvironment, accountID)
		}
		compiled.candidates = append(compiled.candidates, CompiledAccountReference{
			ID: account.ID, Revision: account.Revision,
			UpstreamEndpointID:       account.UpstreamEndpointID,
			UpstreamEndpointRevision: account.UpstreamEndpointRevision,
			RealmID:                  account.RealmID,
		})
	}
	return compiled, nil
}

func validateUpstreamEndpointSnapshot(
	catalog upstreamendpoint.Catalog,
	route UpstreamRoute,
) error {
	if catalog == nil {
		return fmt.Errorf("%w: managed route %q has no UpstreamEndpoint catalog", ErrInvalidEnvironment, route.ID)
	}
	endpoint, exists := catalog.LookupEndpoint(route.ProviderTarget.ID)
	if !exists || endpoint.State != upstreamendpoint.StateActive ||
		endpoint.Revision != uint64(route.ProviderTarget.Revision) ||
		endpoint.Origin != route.ProviderTarget.Origin ||
		endpoint.RealmID != route.ProviderTarget.RealmID ||
		!sameCapabilities(endpoint.Capabilities, route.ProviderTarget.Capabilities) ||
		!slices.Contains(endpoint.BackendProtocols, route.BackendProtocol) {
		return fmt.Errorf(
			"%w: route %q does not freeze its declared UpstreamEndpoint",
			ErrInvalidEnvironment,
			route.ID,
		)
	}
	return nil
}

func sameCapabilities(
	left []protocolspec.ProviderCapability,
	right []protocolspec.ProviderCapability,
) bool {
	if len(left) != len(right) {
		return false
	}
	for _, capability := range left {
		if !slices.Contains(right, capability) {
			return false
		}
	}
	return true
}

func clientDialect(protocol ClientProtocol) (protocolspec.Dialect, error) {
	switch protocol {
	case ClientProtocolAnthropicMessages:
		return protocolspec.DialectAnthropicMessages, nil
	case ClientProtocolOpenAIResponses:
		return protocolspec.DialectOpenAIResponses, nil
	case ClientProtocolOpenAIChat:
		return protocolspec.DialectOpenAIChat, nil
	default:
		return "", protocolspec.ErrUnknownDialect
	}
}

func backendDialect(protocol string) (protocolspec.Dialect, error) {
	switch protocol {
	case string(ClientProtocolAnthropicMessages), string(protocolspec.DialectAnthropicMessages):
		return protocolspec.DialectAnthropicMessages, nil
	case string(ClientProtocolOpenAIResponses), string(protocolspec.DialectOpenAIResponses):
		return protocolspec.DialectOpenAIResponses, nil
	case string(ClientProtocolOpenAIChat), string(protocolspec.DialectOpenAIChat):
		return protocolspec.DialectOpenAIChat, nil
	default:
		return "", protocolspec.ErrUnknownDialect
	}
}

func cloneProviderTarget(target ProviderTarget) ProviderTarget {
	cloned := target
	cloned.Capabilities = slices.Clone(target.Capabilities)
	return cloned
}

func cloneCompiledAccountPolicy(policy CompiledAccountPolicy) CompiledAccountPolicy {
	cloned := policy
	cloned.candidates = slices.Clone(policy.candidates)
	return cloned
}

func cloneCompiledRoute(route CompiledRoutePlan) CompiledRoutePlan {
	cloned := route
	cloned.target = cloneProviderTarget(route.target)
	cloned.accountPolicy = cloneCompiledAccountPolicy(route.accountPolicy)
	cloned.modelPolicy = cloneModelPolicy(route.modelPolicy)
	return cloned
}

func cloneCompiledRouteSet(set CompiledRouteSet) CompiledRouteSet {
	cloned := set
	cloned.candidates = slices.Clone(set.candidates)
	cloned.routes = make(map[UpstreamRouteID]CompiledRoutePlan, len(set.routes))
	for id, route := range set.routes {
		cloned.routes[id] = cloneCompiledRoute(route)
	}
	return cloned
}

func cloneCompiledProtocol(plan CompiledProtocolPlan) CompiledProtocolPlan {
	cloned := plan
	cloned.operations = slices.Clone(plan.operations)
	if plan.upstreamRouteSet != nil {
		routeSet := cloneCompiledRouteSet(*plan.upstreamRouteSet)
		cloned.upstreamRouteSet = &routeSet
	}
	return cloned
}

func cloneCompiledEndpoint(endpoint CompiledEndpointPlan) CompiledEndpointPlan {
	cloned := endpoint
	cloned.plans = make([]CompiledProtocolPlan, len(endpoint.plans))
	for index, plan := range endpoint.plans {
		cloned.plans[index] = cloneCompiledProtocol(plan)
	}
	return cloned
}
