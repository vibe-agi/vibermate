package exchange

import (
	"errors"
	"fmt"
	"slices"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func validateClientOperation(
	plan environment.RequestPlan,
	evidence ClientOperationEvidence,
	replayClass ReplayClass,
) error {
	selected := plan.Operation()
	codecPlan := plan.CodecPlan()
	found := 0
	for _, operation := range codecPlan.ClientOperations() {
		if operation.ID() == selected.ID() {
			found++
		}
	}
	if found != 1 ||
		selected.ID() != evidence.ID() ||
		selected.Revision() != evidence.Revision() ||
		selected.ClientDialect() != codecPlan.ClientDialect() ||
		selected.PathMatch() != protocolspec.ClientOperationPathExact ||
		selected.Kind() != protocolspec.ClientOperationSemantic ||
		selected.Transport() != protocolspec.ClientOperationTransportHTTP ||
		!selected.EgressBearing() ||
		selected.PathPattern() != evidence.Path() ||
		!slices.Contains(selected.Methods(), evidence.Method()) ||
		!selected.AllowsRawQuery(evidence.RawQuery()) {
		return errors.New(
			"client operation evidence does not match the frozen Environment plan",
		)
	}
	expectedReplay, err := exchangeReplayClass(selected.ReplayClass())
	if err != nil || replayClass != expectedReplay {
		return errors.New(
			"client operation replay class does not match the frozen Environment plan",
		)
	}
	return nil
}

func exchangeReplayClass(
	class protocolspec.ClientReplayClass,
) (ReplayClass, error) {
	switch class {
	case protocolspec.ClientReplaySafe:
		return ReplaySafe, nil
	case protocolspec.ClientReplayIdempotencyKeyed:
		return ReplayIdempotencyKeyed, nil
	case protocolspec.ClientReplayGenerationCostOnly:
		return ReplayGenerationCostOnly, nil
	case protocolspec.ClientReplaySideEffectPossible:
		return ReplaySideEffectPossible, nil
	case protocolspec.ClientReplayNonReplayable:
		return ReplayNonReplayable, nil
	case protocolspec.ClientReplayUnknown:
		return ReplayUnknown, nil
	default:
		return "", errors.New("client operation replay class is invalid")
	}
}

type credentialCandidate struct {
	mode    providerauth.CredentialMode
	account environment.CompiledAccountReference
}

type credentialMaterial struct {
	mode    providerauth.CredentialMode
	account providerauth.AccountRef
	secret  secretstore.Reference
	driver  providerauth.DriverRef
	release func()
}

func (material credentialMaterial) Release() {
	if material.release != nil {
		material.release()
	}
}

type frozenSelection struct {
	environmentID        environment.EnvironmentID
	environmentRevision  environment.Revision
	environmentDigest    environment.CandidateDigest
	endpointID           environment.ClientEndpointID
	endpointRevision     environment.Revision
	protocolPlanID       environment.ClientProtocolPlanID
	protocolPlanRevision environment.Revision
	routeID              environment.UpstreamRouteID
	routeRevision        environment.Revision
	accountPolicy        environment.CompiledAccountPolicy
	resolveModelMapping  func(string) (string, bool)
	original             bool
	targetRef            string
	targetRevision       environment.Revision
	targetRealm          string
	target               providertransport.Target
	provenance           providertransport.RequestProvenance
	wireProfile          wireprofile.CompiledUpstreamWireProfile
	codecPlan            protocolspec.CodecPlan
	policySet            environment.PolicySet
}

func validateFrozenRequestPlan(plan environment.RequestPlan) error {
	_, err := selectFrozenPlan(plan)
	return err
}

func validateFrozenWireVariant(
	plan environment.RequestPlan,
	clientProtocol wireprofile.ApplicationProtocol,
) error {
	if !clientProtocol.Valid() {
		return errors.New("client HTTP protocol is invalid")
	}
	expected, available := plan.WireProfile().Variant(clientProtocol)
	actual := plan.WireVariant()
	if !available || !sameWireVariant(actual, expected) {
		return errors.New("frozen wire variant does not match the client protocol")
	}
	return nil
}

func sameWireVariant(
	left wireprofile.CompiledUpstreamWireVariant,
	right wireprofile.CompiledUpstreamWireVariant,
) bool {
	if left.Protocol() != right.Protocol() ||
		left.UserAgentPolicy() != right.UserAgentPolicy() ||
		left.SemanticUserAgent() != right.SemanticUserAgent() ||
		left.EvidenceDigest() != right.EvidenceDigest() {
		return false
	}
	leftPlan := left.TransportFingerprintPlan()
	rightPlan := right.TransportFingerprintPlan()
	if !sameTransportTemplate(leftPlan.Requested(), rightPlan.Requested()) {
		return false
	}
	leftFallbacks := leftPlan.Fallbacks()
	rightFallbacks := rightPlan.Fallbacks()
	if len(leftFallbacks) != len(rightFallbacks) {
		return false
	}
	for index := range leftFallbacks {
		if !sameTransportTemplate(leftFallbacks[index], rightFallbacks[index]) {
			return false
		}
	}
	return true
}

func sameTransportTemplate(
	left wireprofile.TransportFingerprintTemplate,
	right wireprofile.TransportFingerprintTemplate,
) bool {
	return left.Ref() == right.Ref() &&
		left.Revision() == right.Revision() &&
		left.Source() == right.Source() &&
		left.Preset() == right.Preset() &&
		left.HTTPTransport() == right.HTTPTransport() &&
		slices.Equal(left.ALPN(), right.ALPN())
}

// selectFrozenPlan converts one already-resolved Environment request plan into
// the provider-facing values used for every attempt. It performs no lookup and
// therefore cannot observe a newer Environment revision mid-request.
func selectFrozenPlan(plan environment.RequestPlan) (frozenSelection, error) {
	environmentID, err := environment.NewEnvironmentID(plan.EnvironmentID().String())
	if err != nil || environmentID != plan.EnvironmentID() ||
		plan.EnvironmentRevision() == 0 ||
		plan.EnvironmentRevision() > environment.MaxRevision {
		return frozenSelection{}, errors.New("Environment request plan identity is invalid")
	}
	digest, err := environment.ParseCandidateDigest(plan.EnvironmentDigest().String())
	if err != nil || digest != plan.EnvironmentDigest() ||
		plan.EnvironmentDigest() == (environment.CandidateDigest{}) {
		return frozenSelection{}, errors.New("Environment request plan digest is invalid")
	}
	policySet := plan.PolicySet()
	if err := policySet.Validate(); err != nil {
		return frozenSelection{}, errors.New("Environment policy set is invalid")
	}
	endpoint := plan.Endpoint()
	endpointID, err := environment.NewClientEndpointID(endpoint.ID().String())
	if err != nil || endpointID != endpoint.ID() || endpoint.Revision() == 0 ||
		endpoint.Revision() > environment.MaxRevision ||
		endpoint.ClientOrigin().Validate() != nil {
		return frozenSelection{}, errors.New("Environment endpoint plan is invalid")
	}
	protocolPlan := plan.ProtocolPlan()
	protocolPlanID, err := environment.NewClientProtocolPlanID(protocolPlan.ID().String())
	if err != nil || protocolPlanID != protocolPlan.ID() ||
		protocolPlan.Revision() == 0 || protocolPlan.Revision() > environment.MaxRevision ||
		!protocolPlan.ClientDialect().Valid() {
		return frozenSelection{}, errors.New("Environment protocol plan is invalid")
	}
	codecPlan := plan.CodecPlan()
	wireProfile := plan.WireProfile()
	if !codecPlan.Valid() {
		return frozenSelection{}, errors.New("Environment codec plan is invalid")
	}
	if wireProfile.Ref().String() == "" || wireProfile.Revision() == 0 ||
		len(wireProfile.Variants()) == 0 {
		return frozenSelection{}, errors.New("Environment outbound wire profile is invalid")
	}
	selection := frozenSelection{
		environmentID: plan.EnvironmentID(), environmentRevision: plan.EnvironmentRevision(),
		environmentDigest: plan.EnvironmentDigest(), endpointID: endpoint.ID(),
		endpointRevision: endpoint.Revision(), protocolPlanID: protocolPlan.ID(),
		protocolPlanRevision: protocolPlan.Revision(),
		wireProfile:          wireProfile,
		codecPlan:            codecPlan, policySet: policySet,
	}
	switch {
	case plan.PreservesOriginalDestination():
		if _, exists := plan.UpstreamRoute(); exists {
			return frozenSelection{}, errors.New("Original Destination carries an Upstream Route")
		}
		originalOrigin, exists := plan.OriginalOrigin()
		if !exists || originalOrigin.Validate() != nil || originalOrigin.BasePath() != "" {
			return frozenSelection{}, errors.New("Original Destination origin is invalid")
		}
		target, err := providertransport.NewTarget(originalOrigin)
		if err != nil {
			return frozenSelection{}, fmt.Errorf("compile original transport target: %w", err)
		}
		provenance, err := providertransport.NewOriginalRequestProvenance(
			plan.EnvironmentID(),
			plan.EnvironmentRevision(),
			plan.EnvironmentDigest(),
		)
		if err != nil {
			return frozenSelection{}, fmt.Errorf("compile original request provenance: %w", err)
		}
		selection.original = true
		selection.targetRef = "original_destination." + endpoint.ID().String()
		selection.targetRevision = endpoint.Revision()
		selection.target = target
		selection.provenance = provenance
		if codecPlan.ClientDialect() != codecPlan.ProviderDialect() ||
			wireProfile.Ref() != wireprofile.FollowClientUpstreamWireProfileRef() {
			return frozenSelection{}, errors.New("Original Destination is not identity-preserving")
		}
	case plan.UsesUpstreamDestination():
		route, exists := plan.UpstreamRoute()
		if !exists {
			return frozenSelection{}, errors.New("Upstream Destination has no frozen Route")
		}
		routeID, err := environment.NewUpstreamRouteID(route.ID().String())
		if err != nil || routeID != route.ID() || route.Revision() == 0 ||
			route.Revision() > environment.MaxRevision || !route.BackendProtocol().Valid() ||
			!route.CodecPlan().Valid() {
			return frozenSelection{}, errors.New("Environment route plan is invalid")
		}
		targetResource := route.ProviderTarget()
		if err := validateIdentity("ProviderTarget ID", targetResource.ID); err != nil ||
			targetResource.Revision == 0 || targetResource.Revision > environment.MaxRevision ||
			targetResource.Origin.Validate() != nil || targetResource.RealmID == "" {
			return frozenSelection{}, errors.New("Environment provider target is invalid")
		}
		target, err := providertransport.NewTarget(targetResource.Origin)
		if err != nil {
			return frozenSelection{}, fmt.Errorf("compile provider transport target: %w", err)
		}
		provenance, err := providertransport.NewUpstreamRequestProvenance(
			plan.EnvironmentID(),
			plan.EnvironmentRevision(),
			plan.EnvironmentDigest(),
			route.ID(),
			route.Revision(),
		)
		if err != nil {
			return frozenSelection{}, fmt.Errorf("compile provider request provenance: %w", err)
		}
		selection.routeID = route.ID()
		selection.routeRevision = route.Revision()
		selection.accountPolicy = route.AccountPolicy()
		selection.resolveModelMapping = route.ResolveModelMapping
		selection.targetRef = targetResource.ID
		selection.targetRevision = targetResource.Revision
		selection.targetRealm = targetResource.RealmID
		selection.target = target
		selection.provenance = provenance
	default:
		return frozenSelection{}, errors.New("Environment Destination is unsupported")
	}
	return selection, nil
}

func (selection frozenSelection) mappedModel(requested string) (string, bool) {
	if selection.resolveModelMapping == nil {
		return "", false
	}
	return selection.resolveModelMapping(requested)
}

func (selection frozenSelection) credentialCandidates() ([]credentialCandidate, error) {
	if selection.original {
		return []credentialCandidate{{mode: providerauth.CredentialClientPassthrough}}, nil
	}
	policy := selection.accountPolicy
	configured := policy.CandidateAccounts()
	if len(configured) == 0 || policy.PreferredAccountID() == "" {
		return nil, errors.New("upstream route has no account candidates")
	}
	ordered := make([]credentialCandidate, 0, len(configured))
	seen := make(map[string]struct{}, len(configured))
	appendCandidate := func(candidate environment.CompiledAccountReference) error {
		if candidate.ID == "" || candidate.Revision == 0 || candidate.Revision > environment.MaxRevision ||
			candidate.UpstreamEndpointID != selection.targetRef ||
			candidate.UpstreamEndpointRevision != selection.targetRevision ||
			candidate.RealmID == "" || candidate.RealmID != selection.targetRealm {
			return errors.New("managed account candidate is invalid")
		}
		if _, duplicate := seen[candidate.ID]; duplicate {
			return errors.New("managed account candidate is duplicated")
		}
		seen[candidate.ID] = struct{}{}
		ordered = append(ordered, credentialCandidate{mode: providerauth.CredentialManaged, account: candidate})
		return nil
	}
	preferredFound := false
	for _, candidate := range configured {
		if candidate.ID == policy.PreferredAccountID() {
			if err := appendCandidate(candidate); err != nil {
				return nil, err
			}
			preferredFound = true
			break
		}
	}
	if !preferredFound {
		return nil, errors.New("preferred account is outside the frozen candidate set")
	}
	for _, candidate := range configured {
		if candidate.ID == policy.PreferredAccountID() {
			continue
		}
		if err := appendCandidate(candidate); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}
