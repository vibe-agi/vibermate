package environment

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// nativeConnectionCompatibility covers the entire validated native contract,
// including the embedded egress profile, without inventing a model endpoint.
func nativeConnectionCompatibility(route ClientToolRoute) (ConnectionCompatibilityDigest, error) {
	if err := route.Validate(); err != nil {
		return ConnectionCompatibilityDigest{}, err
	}
	encoded, err := json.Marshal(route)
	if err != nil {
		return ConnectionCompatibilityDigest{}, err
	}
	return ConnectionCompatibilityDigest(sha256.Sum256(append([]byte("vibermate-native-connection/v1\x00"), encoded...))), nil
}

func (snapshot EnvironmentSnapshot) clientToolRoute(origin originidentity.ProviderOrigin) (ClientToolRoute, bool) {
	policy := snapshot.aggregate.ClientToolAccess
	if policy == nil || !policy.Enabled || policy.Validate() != nil || policy.Route.TargetOrigin != origin {
		return ClientToolRoute{}, false
	}
	return *policy.Route, true
}

// ValidateClientToolLaunchAuthority checks historical launch evidence against
// a frozen native plan. It grants no transport or original-login capability.
func (snapshot EnvironmentSnapshot) ValidateClientToolLaunchAuthority(boundary LaunchAuthorityBoundary, plan ClientToolPlan) error {
	if snapshot.validate() != nil || snapshot.State() != StateActive || boundary.Validate() != nil || plan.Validate() != nil ||
		snapshot.ID() != boundary.InitialEnvironmentID() || snapshot.Revision() != boundary.InitialEnvironmentRevision() || snapshot.Digest() != boundary.InitialEnvironmentDigest() ||
		plan.EnvironmentID() != snapshot.ID() || plan.EnvironmentRevision() < snapshot.Revision() ||
		(plan.EnvironmentRevision() == snapshot.Revision() && plan.EnvironmentDigest() != snapshot.Digest()) {
		return ErrInvalidEnvironment
	}
	route, ok := snapshot.clientToolRoute(plan.Route().TargetOrigin)
	if !ok {
		return ErrClientProtocolNotMatched
	}
	initialDigest, err := nativeConnectionCompatibility(route)
	currentDigest, currentErr := nativeConnectionCompatibility(plan.Route())
	if err != nil || currentErr != nil || initialDigest != currentDigest {
		return ErrInvalidEnvironment
	}
	for _, authority := range boundary.ProtectedAuthorities() {
		if authority == route.TargetOrigin.EndpointAuthority() {
			return nil
		}
	}
	return ErrInvalidEnvironment
}

type ClientToolRouteID string

func NewClientToolRouteID(value string) (ClientToolRouteID, error) {
	if err := validateID("ClientToolRoute ID", value); err != nil {
		return "", err
	}
	return ClientToolRouteID(value), nil
}
func (id ClientToolRouteID) String() string { return string(id) }

type ClientToolCredentialOwner string
type ClientToolContract string

const (
	ClientToolCredentialOriginal  ClientToolCredentialOwner = "original_client"
	ClientToolContractCodexLegacy ClientToolContract        = "codex_legacy_2025_06_18"
	ClientToolMCPPath                                       = "/backend-api/ps/mcp"
	ChildIdentityClientToolRoute  ChildIdentityKind         = "client_tool_route"
)

// ClientToolRoute is independent of model/account routing. A configured HTTPS
// target is a bounded contract, not proof of any client's actual destination.
type ClientToolRoute struct {
	ID              ClientToolRouteID             `json:"id"`
	Revision        Revision                      `json:"revision"`
	TargetOrigin    originidentity.ProviderOrigin `json:"targetOrigin"`
	Path            string                        `json:"path"`
	CredentialOwner ClientToolCredentialOwner     `json:"credentialOwner"`
	EgressProfile   egressprofile.Choice          `json:"egressProfile"`
	Contract        ClientToolContract            `json:"contract"`
}

func (route ClientToolRoute) Validate() error {
	if validateChildID("ClientToolRoute", route.ID.String(), route.Revision) != nil || route.TargetOrigin.Validate() != nil ||
		route.TargetOrigin.Scheme() != "https" || route.TargetOrigin.BasePath() != "" || len(route.TargetOrigin.String()) > 1024 ||
		route.Path != ClientToolMCPPath || route.CredentialOwner != ClientToolCredentialOriginal || route.Contract != ClientToolContractCodexLegacy || route.EgressProfile.Validate() != nil {
		return fmt.Errorf("%w: native client tool route is invalid", ErrInvalidEnvironment)
	}
	return nil
}

type ClientToolAccessPolicy struct {
	Enabled bool             `json:"enabled"`
	Route   *ClientToolRoute `json:"route,omitempty"`
}

func (policy ClientToolAccessPolicy) Validate() error {
	if !policy.Enabled {
		if policy.Route != nil {
			return fmt.Errorf("%w: disabled native permission has a route", ErrInvalidEnvironment)
		}
		return nil
	}
	if policy.Route == nil {
		return fmt.Errorf("%w: native permission has no route", ErrInvalidEnvironment)
	}
	return policy.Route.Validate()
}
func cloneClientToolAccess(policy *ClientToolAccessPolicy) *ClientToolAccessPolicy {
	if policy == nil {
		return nil
	}
	copy := *policy
	if policy.Route != nil {
		route := *policy.Route
		copy.Route = &route
	}
	return &copy
}

// ClientToolPlan can be constructed only from a validated immutable snapshot.
// It carries no model codec, selected account, workspace or send capability.
type ClientToolPlan struct {
	resolvedEgress      egressprofile.Resolved
	environmentID       EnvironmentID
	environmentRevision Revision
	environmentDigest   CandidateDigest
	route               ClientToolRoute
	contentRecording    ContentRecordingPolicy
	policySet           PolicySet
}

func (plan ClientToolPlan) EnvironmentID() EnvironmentID             { return plan.environmentID }
func (plan ClientToolPlan) EnvironmentRevision() Revision            { return plan.environmentRevision }
func (plan ClientToolPlan) EnvironmentDigest() CandidateDigest       { return plan.environmentDigest }
func (plan ClientToolPlan) Route() ClientToolRoute                   { return plan.route }
func (plan ClientToolPlan) ContentRecording() ContentRecordingPolicy { return plan.contentRecording }
func (plan ClientToolPlan) PolicySet() PolicySet                     { return plan.policySet }
func (plan ClientToolPlan) Validate() error {
	if validateChildID("Environment", plan.environmentID.String(), plan.environmentRevision) != nil || plan.environmentDigest == (CandidateDigest{}) ||
		plan.route.Validate() != nil || plan.contentRecording.Validate() != nil || plan.policySet.Validate() != nil {
		return ErrInvalidEnvironment
	}
	return nil
}
func (plan RequestPlan) ClientToolPlan() (ClientToolPlan, bool) {
	if plan.clientTool == nil {
		return ClientToolPlan{}, false
	}
	return *plan.clientTool, true
}

func (snapshot EnvironmentSnapshot) ResolveClientToolRequest(binding ConnectionBinding, facts RequestFacts) (RequestPlan, error) {
	if ValidateConnectionBinding(snapshot, binding) != nil || (binding.Mode != ConnectionModeClientTools && binding.Mode != ConnectionModeSemantic) || snapshot.aggregate.ClientToolAccess == nil || !snapshot.aggregate.ClientToolAccess.Enabled {
		return RequestPlan{}, ErrClientProtocolNotMatched
	}
	route := *snapshot.aggregate.ClientToolAccess.Route
	target := facts.Target
	if binding.ProviderOrigin != route.TargetOrigin || target.Validate() != nil || target.Path != route.Path || target.RawPath != "" || target.RawQuery != "" ||
		target.Transport != protocolspec.ClientOperationTransportHTTP || (target.Method != "POST" && target.Method != "GET" && target.Method != "DELETE") {
		return RequestPlan{}, ErrClientProtocolNotMatched
	}
	native := ClientToolPlan{environmentID: snapshot.ID(), environmentRevision: snapshot.Revision(), environmentDigest: snapshot.Digest(), route: route, contentRecording: snapshot.ContentRecording(), policySet: snapshot.aggregate.EffectivePolicySet()}
	if native.Validate() != nil {
		return RequestPlan{}, ErrInvalidEnvironment
	}
	return RequestPlan{environmentDigestText: snapshot.digestText, environmentName: snapshot.Name(), environmentID: native.environmentID, environmentRevision: native.environmentRevision, environmentDigest: native.environmentDigest, contentRecording: native.contentRecording, policySet: native.policySet, clientTool: &native}, nil
}

func validateClientToolTransition(old, new Environment) error {
	if new.ClientToolAccess == nil || new.ClientToolAccess.Route == nil {
		return nil
	}
	next := *new.ClientToolAccess.Route
	if old.ClientToolAccess == nil || old.ClientToolAccess.Route == nil || old.ClientToolAccess.Route.ID != next.ID {
		if next.Revision != 1 {
			return newChildRevision("ClientToolRoute", next.ID.String())
		}
		return nil
	}
	previous := *old.ClientToolAccess.Route
	if previous.TargetOrigin != next.TargetOrigin || previous.Path != next.Path || previous.CredentialOwner != next.CredentialOwner || previous.Contract != next.Contract {
		return mutableAlias("ClientToolRoute", next.ID.String())
	}
	oldRevision, newRevision := previous.Revision, next.Revision
	previous.Revision = 0
	next.Revision = 0
	return requireRevisionChange("ClientToolRoute", next.ID.String(), oldRevision, newRevision, reflect.DeepEqual(previous, next))
}
