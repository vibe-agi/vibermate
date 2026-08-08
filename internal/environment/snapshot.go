package environment

import (
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
	ClientOrigin        originidentity.ClientOrigin
	ClientEndpointID    ClientEndpointID
	CompatibilityDigest ConnectionCompatibilityDigest
}

func (compiler Compiler) Compile(input Environment) (EnvironmentSnapshot, error) {
	normalized, err := normalize(input)
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
	encoded, err := CanonicalJSON(normalized)
	if err != nil {
		return EnvironmentSnapshot{}, err
	}
	snapshot := EnvironmentSnapshot{
		aggregate: normalized.Clone(), digest: digestBytes(encoded),
		byOrigin:        make(map[string]int, len(normalized.ClientEndpoints)),
		compiledOrigins: compiledOrigins, compiled: compiled,
	}
	for index, endpoint := range normalized.ClientEndpoints {
		snapshot.byOrigin[endpoint.ClientOrigin.String()] = index
	}
	return snapshot, nil
}

func digestBytes(encoded []byte) CandidateDigest {
	return CandidateDigest(sha256Sum(encoded))
}

// Kept behind a small wrapper so snapshot.go does not expose hash internals.
func sha256Sum(encoded []byte) [32]byte {
	return sha256.Sum256(encoded)
}

func SystemTransparentSnapshot() EnvironmentSnapshot {
	digest := sha256.Sum256([]byte("vibermate/environment/system_transparent/v1"))
	return EnvironmentSnapshot{
		aggregate: Environment{
			ID:       SystemTransparentID,
			Name:     "System Transparent",
			State:    StateActive,
			Revision: 1,
		},
		digest:   CandidateDigest(digest),
		byOrigin: make(map[string]int), compiledOrigins: make(map[string]int),
		system: true,
	}
}

func (snapshot EnvironmentSnapshot) ID() EnvironmentID       { return snapshot.aggregate.ID }
func (snapshot EnvironmentSnapshot) Name() string            { return snapshot.aggregate.Name }
func (snapshot EnvironmentSnapshot) State() State            { return snapshot.aggregate.State }
func (snapshot EnvironmentSnapshot) Revision() Revision      { return snapshot.aggregate.Revision }
func (snapshot EnvironmentSnapshot) Digest() CandidateDigest { return snapshot.digest }
func (snapshot EnvironmentSnapshot) SystemOwned() bool       { return snapshot.system }
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
	if err := snapshot.validate(); err != nil {
		return ConnectionBinding{}, err
	}
	if snapshot.State() != StateActive || origin.Validate() != nil {
		return ConnectionBinding{}, ErrInvalidEnvironment
	}
	endpoint, intercepted := snapshot.LookupCompiledClientOrigin(origin)
	if !intercepted {
		return ConnectionBinding{Mode: ConnectionModeBlind, ClientOrigin: origin}, nil
	}
	return ConnectionBinding{
		Mode: ConnectionModeSemantic, ClientOrigin: origin,
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
		operation protocolspec.ClientOperationPlan
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
	route, err := matches[0].protocol.routeSet.DefaultRoute()
	if err != nil {
		return RequestPlan{}, err
	}
	variant, exists := route.wireProfile.Variant(facts.DownstreamProtocol)
	if !exists {
		return RequestPlan{}, fmt.Errorf(
			"%w: routeId=%q downstreamProtocol=%q",
			wireprofile.ErrInvalidProfile,
			route.ID(),
			facts.DownstreamProtocol,
		)
	}
	return RequestPlan{
		environmentID: snapshot.ID(), environmentRevision: snapshot.Revision(),
		environmentDigest: snapshot.Digest(), endpoint: endpoint,
		protocol: cloneCompiledProtocol(matches[0].protocol), operation: matches[0].operation,
		route: route, wireVariant: variant,
	}, nil
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

// ClassifyConnectionTransition compares only downstream interception and
// codec compatibility. Routes, accounts, plugins, budgets, and egress can hot
// switch at the next request because each request freezes those values anew.
func ClassifyConnectionTransition(current, target EnvironmentSnapshot, binding ConnectionBinding) (CompatibilityClassification, error) {
	if err := ValidateConnectionBinding(current, binding); err != nil {
		return CompatibilityReconnectRequired, err
	}
	if err := target.validate(); err != nil {
		return CompatibilityReconnectRequired, err
	}
	if target.State() != StateActive {
		return CompatibilityReconnectRequired, nil
	}
	switch binding.Mode {
	case ConnectionModeBlind:
		if _, intercepted := target.LookupClientOrigin(binding.ClientOrigin); intercepted {
			return CompatibilityReconnectRequired, nil
		}
		return CompatibilityHotSwitch, nil
	case ConnectionModeSemantic:
		newEndpoint, newOK := target.LookupCompiledClientOrigin(binding.ClientOrigin)
		if !newOK || newEndpoint.ID() != binding.ClientEndpointID ||
			newEndpoint.CompatibilityDigest() != binding.CompatibilityDigest {
			return CompatibilityReconnectRequired, nil
		}
		return CompatibilityHotSwitch, nil
	default:
		return CompatibilityReconnectRequired, ErrInvalidEnvironment
	}
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
		if binding.ClientEndpointID != "" || binding.CompatibilityDigest != (ConnectionCompatibilityDigest{}) {
			return ErrInvalidEnvironment
		}
		if _, intercepted := snapshot.LookupClientOrigin(binding.ClientOrigin); intercepted {
			return ErrInvalidEnvironment
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
	if binding.ClientOrigin.Validate() != nil {
		return ErrInvalidEnvironment
	}
	switch binding.Mode {
	case ConnectionModeBlind:
		if binding.ClientEndpointID != "" || binding.CompatibilityDigest != (ConnectionCompatibilityDigest{}) {
			return ErrInvalidEnvironment
		}
	case ConnectionModeSemantic:
		if validateID("ClientEndpoint ID", binding.ClientEndpointID.String()) != nil ||
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
		expected := SystemTransparentSnapshot()
		if snapshot.ID() != SystemTransparentID || !snapshot.BlindOnly() || snapshot.State() != StateActive ||
			snapshot.Revision() != expected.Revision() || snapshot.Digest() != expected.Digest() {
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
