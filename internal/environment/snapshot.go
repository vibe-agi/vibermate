package environment

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

const SystemTransparentID EnvironmentID = "system_transparent"

type EnvironmentSnapshot struct {
	aggregate       Environment
	digest          CandidateDigest
	byOrigin        map[string]int
	compiledOrigins map[string]int
	compiled        []CompiledEndpointPlan
	system          bool
}

type ClientEndpointSnapshot struct{ endpoint ClientEndpoint }

type ConnectionMode string

type ConnectionCompatibilityDigest [sha256.Size]byte

func (digest ConnectionCompatibilityDigest) String() string {
	return hex.EncodeToString(digest[:])
}

const (
	ConnectionModeBlind    ConnectionMode = "blind"
	ConnectionModeSemantic ConnectionMode = "semantic"
)

// ConnectionBinding is the frozen downstream shape that determines whether
// an existing connection can safely serve a later request after an
// Environment assignment or revision changes.
type ConnectionBinding struct {
	Mode                ConnectionMode
	ProviderOrigin      originidentity.ProviderOrigin
	ClientOrigin        originidentity.ClientOrigin
	ClientEndpointID    ClientEndpointID
	CompatibilityDigest ConnectionCompatibilityDigest
}

func (compiler Compiler) Compile(input Environment) (EnvironmentSnapshot, error) {
	return compiler.compile(input, false)
}

func (compiler Compiler) compile(input Environment, system bool) (EnvironmentSnapshot, error) {
	var normalized Environment
	var err error
	if system {
		normalized, err = normalizeSystem(input)
	} else {
		normalized, err = normalize(input)
	}
	if err != nil {
		return EnvironmentSnapshot{}, err
	}
	if err := validateAccounts(normalized, compiler.accounts); err != nil {
		return EnvironmentSnapshot{}, err
	}
	compiled, compiledOrigins, err := compileExecution(compiler, normalized)
	if err != nil {
		return EnvironmentSnapshot{}, err
	}
	var encoded []byte
	if system {
		encoded, err = canonicalSystemJSON(normalized)
	} else {
		encoded, err = CanonicalJSON(normalized)
	}
	if err != nil {
		return EnvironmentSnapshot{}, err
	}
	snapshot := EnvironmentSnapshot{
		aggregate: normalized.Clone(), digest: digestBytes(encoded),
		byOrigin:        make(map[string]int, len(normalized.ClientEndpoints)),
		compiledOrigins: compiledOrigins, compiled: compiled,
	}
	snapshot.system = system
	for index, endpoint := range normalized.ClientEndpoints {
		snapshot.byOrigin[endpoint.ClientOrigin.String()] = index
	}
	return snapshot, nil
}

// CompileSystemTransparent builds Core's capture-by-default Environment. It
// intercepts only the explicit Agent API entries ViberMate can parse and sends
// each request back to its original destination without changing the client's
// authentication or model. Every unrelated destination remains a blind
// tunnel.
func (compiler Compiler) CompileSystemTransparent() (EnvironmentSnapshot, error) {
	definition, err := systemTransparentDefinition()
	if err != nil {
		return EnvironmentSnapshot{}, err
	}
	return compiler.compile(definition, true)
}

func systemTransparentDefinition() (Environment, error) {
	type entry struct {
		endpointID string
		planID     string
		origin     string
		protocol   ClientProtocol
	}
	entries := []entry{
		{
			endpointID: "endpoint.system.anthropic", planID: "plan.system.anthropic.messages",
			origin: "https://api.anthropic.com", protocol: ClientProtocolAnthropicMessages,
		},
		{
			endpointID: "endpoint.system.openai", planID: "plan.system.openai.responses",
			origin: "https://api.openai.com", protocol: ClientProtocolOpenAIResponses,
		},
		{
			endpointID: "endpoint.system.chatgpt", planID: "plan.system.chatgpt.responses",
			origin: "https://chatgpt.com", protocol: ClientProtocolOpenAIResponses,
		},
	}
	endpoints := make([]ClientEndpoint, 0, len(entries))
	for _, entry := range entries {
		origin, err := originidentity.ParseClientOrigin(entry.origin)
		if err != nil {
			return Environment{}, fmt.Errorf("%w: system client origin", err)
		}
		endpoints = append(endpoints, ClientEndpoint{
			ID: ClientEndpointID(entry.endpointID), Revision: 1, ClientOrigin: origin,
			ProtocolPlans: []ClientProtocolPlan{{
				ID: ClientProtocolPlanID(entry.planID), Revision: 1,
				ClientProtocol: entry.protocol,
				ClientAdapterPolicy: ClientAdapterPolicy{
					ID: "adapter." + entry.planID, Revision: 1,
				},
				Destination: DestinationPlan{Kind: DestinationKindOriginal},
			}},
		})
	}
	return Environment{
		ID: SystemTransparentID, Name: "System Transparent", State: StateActive,
		Revision: 1, ClientEndpoints: endpoints,
		ContentRecording: DefaultContentRecordingPolicy(),
	}, nil
}

func digestBytes(encoded []byte) CandidateDigest {
	return CandidateDigest(sha256Sum(encoded))
}

// Kept behind a small wrapper so snapshot.go does not expose hash internals.
func sha256Sum(encoded []byte) [32]byte {
	return sha256.Sum256(encoded)
}

func (snapshot EnvironmentSnapshot) ID() EnvironmentID       { return snapshot.aggregate.ID }
func (snapshot EnvironmentSnapshot) Name() string            { return snapshot.aggregate.Name }
func (snapshot EnvironmentSnapshot) State() State            { return snapshot.aggregate.State }
func (snapshot EnvironmentSnapshot) Revision() Revision      { return snapshot.aggregate.Revision }
func (snapshot EnvironmentSnapshot) Digest() CandidateDigest { return snapshot.digest }
func (snapshot EnvironmentSnapshot) SystemOwned() bool       { return snapshot.system }
func (snapshot EnvironmentSnapshot) ContentRecording() ContentRecordingPolicy {
	return snapshot.aggregate.ContentRecording
}
func (snapshot EnvironmentSnapshot) LaunchEnvironment() LaunchEnvironmentPolicy {
	return snapshot.aggregate.LaunchEnvironment.Clone()
}
func (snapshot EnvironmentSnapshot) BlindOnly() bool {
	return len(snapshot.aggregate.ClientEndpoints) == 0
}
func (snapshot EnvironmentSnapshot) Aggregate() Environment { return snapshot.aggregate.Clone() }

func (snapshot EnvironmentSnapshot) ClientEndpoints() []ClientEndpointSnapshot {
	result := make([]ClientEndpointSnapshot, len(snapshot.aggregate.ClientEndpoints))
	for index, endpoint := range snapshot.aggregate.ClientEndpoints {
		result[index] = ClientEndpointSnapshot{endpoint: cloneEndpoint(endpoint)}
	}
	return result
}

func (snapshot EnvironmentSnapshot) LookupClientOrigin(origin originidentity.ClientOrigin) (ClientEndpointSnapshot, bool) {
	if origin.Validate() != nil {
		return ClientEndpointSnapshot{}, false
	}
	index, exists := snapshot.byOrigin[origin.String()]
	if !exists {
		return ClientEndpointSnapshot{}, false
	}
	return ClientEndpointSnapshot{endpoint: cloneEndpoint(snapshot.aggregate.ClientEndpoints[index])}, true
}

func (snapshot EnvironmentSnapshot) LookupCompiledClientOrigin(origin originidentity.ClientOrigin) (CompiledEndpointPlan, bool) {
	if origin.Validate() != nil {
		return CompiledEndpointPlan{}, false
	}
	index, exists := snapshot.compiledOrigins[origin.String()]
	if !exists || index < 0 || index >= len(snapshot.compiled) {
		return CompiledEndpointPlan{}, false
	}
	return cloneCompiledEndpoint(snapshot.compiled[index]), true
}

// BeginConnection resolves exact-origin interception once and freezes only
// the downstream compatibility contract. A protocol plan is deliberately not
// selected here: one endpoint may accept multiple client dialects, and the
// method/path/transport evidence needed to select one exists only after the
// HTTP request arrives.
func (snapshot EnvironmentSnapshot) BeginConnection(origin originidentity.ClientOrigin) (ConnectionBinding, error) {
	providerOrigin, err := originidentity.ParseProviderOrigin(origin.String())
	if err != nil {
		return ConnectionBinding{}, ErrInvalidEnvironment
	}
	return snapshot.beginConnection(providerOrigin, origin)
}

// BeginProviderConnection resolves an ordinary proxy destination. Only an
// exact HTTPS ClientOrigin already present in the Environment is semantic;
// private cleartext and every other destination remain blind.
func (snapshot EnvironmentSnapshot) BeginProviderConnection(
	origin originidentity.ProviderOrigin,
) (ConnectionBinding, error) {
	if err := snapshot.validate(); err != nil {
		return ConnectionBinding{}, err
	}
	if snapshot.State() != StateActive || origin.Validate() != nil ||
		origin.BasePath() != "" {
		return ConnectionBinding{}, ErrInvalidEnvironment
	}
	if origin.Scheme() == "https" {
		clientOrigin, err := originidentity.ParseClientOrigin(origin.String())
		if err == nil {
			return snapshot.beginConnection(origin, clientOrigin)
		}
	}
	return ConnectionBinding{
		Mode: ConnectionModeBlind, ProviderOrigin: origin,
	}, nil
}

// BeginClientTargetConnection binds an explicit destination selected by a
// verified Agent to an existing canonical Client Flow. The caller owns proof
// that the pair came from the Capture's frozen client target.
func (snapshot EnvironmentSnapshot) BeginClientTargetConnection(
	origin originidentity.ProviderOrigin,
	canonical originidentity.ClientOrigin,
) (ConnectionBinding, error) {
	if err := snapshot.validate(); err != nil {
		return ConnectionBinding{}, err
	}
	if snapshot.State() != StateActive || origin.Validate() != nil ||
		origin.BasePath() != "" || canonical.Validate() != nil {
		return ConnectionBinding{}, ErrInvalidEnvironment
	}
	return snapshot.beginConnection(origin, canonical)
}

func (snapshot EnvironmentSnapshot) beginConnection(
	providerOrigin originidentity.ProviderOrigin,
	clientOrigin originidentity.ClientOrigin,
) (ConnectionBinding, error) {
	if err := snapshot.validate(); err != nil {
		return ConnectionBinding{}, err
	}
	if snapshot.State() != StateActive || providerOrigin.Validate() != nil ||
		providerOrigin.BasePath() != "" || clientOrigin.Validate() != nil {
		return ConnectionBinding{}, ErrInvalidEnvironment
	}
	endpoint, intercepted := snapshot.LookupCompiledClientOrigin(clientOrigin)
	if !intercepted {
		return ConnectionBinding{
			Mode: ConnectionModeBlind, ProviderOrigin: providerOrigin,
		}, nil
	}
	return ConnectionBinding{
		Mode: ConnectionModeSemantic, ProviderOrigin: providerOrigin,
		ClientOrigin:     clientOrigin,
		ClientEndpointID: endpoint.ID(), CompatibilityDigest: endpoint.CompatibilityDigest(),
	}, nil
}

// ResolveRequest freezes the complete Environment-to-route handoff for one
// request. Protocol selection happens only after exact-origin admission and
// requires one unique method/path/query/transport match.
func (snapshot EnvironmentSnapshot) ResolveRequest(
	origin originidentity.ClientOrigin,
	facts RequestFacts,
) (RequestPlan, error) {
	if err := snapshot.validate(); err != nil {
		return RequestPlan{}, err
	}
	if !facts.DownstreamProtocol.Valid() {
		return RequestPlan{}, wireprofile.ErrInvalidProfile
	}
	endpoint, exists := snapshot.LookupCompiledClientOrigin(origin)
	if !exists {
		return RequestPlan{}, fmt.Errorf("%w: clientOrigin=%q", ErrEnvironmentNotFound, origin.String())
	}
	type match struct {
		protocol  CompiledProtocolPlan
		operation protocolspec.ClientOperationDefinition
	}
	var matches []match
	var knownMismatch bool
	for _, protocol := range endpoint.plans {
		operation, err := protocolspec.SelectOperation(protocol.operations, facts.Target)
		switch {
		case err == nil:
			matches = append(matches, match{protocol: protocol, operation: operation})
		case errors.Is(err, protocolspec.ErrOperationContractMismatch), errors.Is(err, protocolspec.ErrAmbiguousOperation):
			knownMismatch = true
		case errors.Is(err, protocolspec.ErrOperationNotCatalogued):
		case err != nil:
			return RequestPlan{}, err
		}
	}
	if len(matches) == 0 {
		if knownMismatch {
			return RequestPlan{}, errors.Join(ErrClientProtocolNotMatched, protocolspec.ErrOperationContractMismatch)
		}
		return RequestPlan{}, errors.Join(ErrClientProtocolNotMatched, protocolspec.ErrOperationNotCatalogued)
	}
	if len(matches) != 1 {
		return RequestPlan{}, ErrClientProtocolAmbiguous
	}
	protocol := matches[0].protocol
	codec := protocol.originalCodec
	wireProfile := protocol.originalWireProfile
	var upstreamRoute *CompiledRoutePlan
	if protocol.destinationKind == DestinationKindUpstream {
		if protocol.upstreamRouteSet == nil {
			return RequestPlan{}, ErrInvalidEnvironment
		}
		route, err := protocol.upstreamRouteSet.DefaultRoute()
		if err != nil {
			return RequestPlan{}, err
		}
		upstreamRoute = &route
		codec = route.codec
		wireProfile = route.wireProfile
	}
	if !codec.Valid() || wireProfile.Ref().String() == "" {
		return RequestPlan{}, ErrInvalidEnvironment
	}
	variant, exists := wireProfile.Variant(facts.DownstreamProtocol)
	if !exists {
		routeID := UpstreamRouteID("")
		if upstreamRoute != nil {
			routeID = upstreamRoute.ID()
		}
		return RequestPlan{}, fmt.Errorf(
			"%w: routeId=%q downstreamProtocol=%q",
			wireprofile.ErrInvalidProfile,
			routeID,
			facts.DownstreamProtocol,
		)
	}
	return RequestPlan{
		environmentID: snapshot.ID(), environmentRevision: snapshot.Revision(),
		environmentDigest: snapshot.Digest(),
		contentRecording:  snapshot.aggregate.ContentRecording,
		policySet:         snapshot.aggregate.EffectivePolicySet(),
		endpoint:          endpoint,
		protocol:          cloneCompiledProtocol(protocol),
		operation:         matches[0].operation,
		destinationKind:   protocol.destinationKind,
		egressPolicy:      protocol.egressPolicy,
		transformProgram:  protocol.transformProgram,
		codecPlan:         codec,
		wireProfile:       wireProfile,
		upstreamRoute:     upstreamRoute,
		wireVariant:       variant,
		originalOrigin:    providerOriginFromClient(origin),
	}, nil
}

// ResolveConnectionRequest carries the actual client destination into the
// frozen request plan while using the canonical Client Flow only for protocol
// selection.
func (snapshot EnvironmentSnapshot) ResolveConnectionRequest(
	binding ConnectionBinding,
	facts RequestFacts,
) (RequestPlan, error) {
	if err := ValidateConnectionBinding(snapshot, binding); err != nil ||
		binding.Mode != ConnectionModeSemantic {
		return RequestPlan{}, ErrClientProtocolNotMatched
	}
	plan, err := snapshot.ResolveRequest(binding.ClientOrigin, facts)
	if err != nil {
		return RequestPlan{}, err
	}
	plan.originalOrigin = binding.ProviderOrigin
	return plan, nil
}

func providerOriginFromClient(origin originidentity.ClientOrigin) originidentity.ProviderOrigin {
	providerOrigin, err := originidentity.ParseProviderOrigin(origin.String())
	if err != nil {
		panic("validated ClientOrigin did not form a ProviderOrigin")
	}
	return providerOrigin
}

func (snapshot EnvironmentSnapshot) clone() EnvironmentSnapshot {
	cloned := snapshot
	cloned.aggregate = snapshot.aggregate.Clone()
	cloned.byOrigin = make(map[string]int, len(snapshot.byOrigin))
	for origin, index := range snapshot.byOrigin {
		cloned.byOrigin[origin] = index
	}
	cloned.compiledOrigins = make(map[string]int, len(snapshot.compiledOrigins))
	for origin, index := range snapshot.compiledOrigins {
		cloned.compiledOrigins[origin] = index
	}
	cloned.compiled = make([]CompiledEndpointPlan, len(snapshot.compiled))
	for index, endpoint := range snapshot.compiled {
		cloned.compiled[index] = cloneCompiledEndpoint(endpoint)
	}
	return cloned
}

func (endpoint ClientEndpointSnapshot) ID() ClientEndpointID { return endpoint.endpoint.ID }
func (endpoint ClientEndpointSnapshot) Revision() Revision   { return endpoint.endpoint.Revision }
func (endpoint ClientEndpointSnapshot) ClientOrigin() originidentity.ClientOrigin {
	return endpoint.endpoint.ClientOrigin
}
func (endpoint ClientEndpointSnapshot) ProtocolPlans() []ClientProtocolPlan {
	result := make([]ClientProtocolPlan, len(endpoint.endpoint.ProtocolPlans))
	for index, plan := range endpoint.endpoint.ProtocolPlans {
		result[index] = cloneProtocolPlan(plan)
	}
	return result
}

// ValidateConnectionBinding proves that a recorded downstream connection was
// actually valid for the immutable Environment snapshot it names.
func ValidateConnectionBinding(snapshot EnvironmentSnapshot, binding ConnectionBinding) error {
	if err := snapshot.validate(); err != nil {
		return err
	}
	if snapshot.State() != StateActive || validateConnectionBindingShape(binding) != nil {
		return ErrInvalidEnvironment
	}
	switch binding.Mode {
	case ConnectionModeBlind:
		if binding.ClientOrigin.String() != "" || binding.ClientEndpointID != "" ||
			binding.CompatibilityDigest != (ConnectionCompatibilityDigest{}) {
			return ErrInvalidEnvironment
		}
		if binding.ProviderOrigin.Scheme() == "https" {
			clientOrigin, err := originidentity.ParseClientOrigin(binding.ProviderOrigin.String())
			if err == nil {
				if _, intercepted := snapshot.LookupClientOrigin(clientOrigin); intercepted {
					return ErrInvalidEnvironment
				}
			}
		}
		return nil
	case ConnectionModeSemantic:
		if binding.ClientEndpointID == "" || binding.CompatibilityDigest == (ConnectionCompatibilityDigest{}) {
			return ErrInvalidEnvironment
		}
		endpoint, exists := snapshot.LookupCompiledClientOrigin(binding.ClientOrigin)
		if !exists || endpoint.ID() != binding.ClientEndpointID ||
			endpoint.CompatibilityDigest() != binding.CompatibilityDigest {
			return ErrInvalidEnvironment
		}
		return nil
	default:
		return ErrInvalidEnvironment
	}
}

func validateConnectionBindingShape(binding ConnectionBinding) error {
	if binding.ProviderOrigin.Validate() != nil || binding.ProviderOrigin.BasePath() != "" {
		return ErrInvalidEnvironment
	}
	switch binding.Mode {
	case ConnectionModeBlind:
		if binding.ClientOrigin.String() != "" || binding.ClientEndpointID != "" ||
			binding.CompatibilityDigest != (ConnectionCompatibilityDigest{}) {
			return ErrInvalidEnvironment
		}
	case ConnectionModeSemantic:
		if binding.ClientOrigin.Validate() != nil ||
			validateID("ClientEndpoint ID", binding.ClientEndpointID.String()) != nil ||
			binding.CompatibilityDigest == (ConnectionCompatibilityDigest{}) {
			return ErrInvalidEnvironment
		}
	default:
		return ErrInvalidEnvironment
	}
	return nil
}

func (snapshot EnvironmentSnapshot) validate() error {
	if snapshot.system {
		expected, definitionErr := systemTransparentDefinition()
		expected, normalizeErr := normalizeSystem(expected)
		expectedJSON, expectedErr := canonicalSystemJSON(expected)
		actualJSON, actualErr := canonicalSystemJSON(snapshot.aggregate)
		if definitionErr != nil || normalizeErr != nil || expectedErr != nil || actualErr != nil ||
			!bytes.Equal(actualJSON, expectedJSON) || digestBytes(expectedJSON) != snapshot.digest ||
			len(snapshot.compiled) != len(snapshot.aggregate.ClientEndpoints) ||
			len(snapshot.compiledOrigins) != len(snapshot.compiled) {
			return fmt.Errorf("%w: invalid system_transparent snapshot", ErrInvalidEnvironment)
		}
		return nil
	}
	encoded, structuralErr := CanonicalJSON(snapshot.aggregate)
	if structuralErr != nil || digestBytes(encoded) != snapshot.digest ||
		len(snapshot.compiled) != len(snapshot.aggregate.ClientEndpoints) ||
		len(snapshot.compiledOrigins) != len(snapshot.compiled) {
		return fmt.Errorf("%w: snapshot integrity failed", ErrInvalidEnvironment)
	}
	return nil
}
