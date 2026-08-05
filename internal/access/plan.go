package access

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
)

type PlanHash [sha256.Size]byte

// ProviderTargetReference is the opaque logical route identity used for
// accounting. It is not sufficient to identify a frozen network destination;
// Offline Hold probe evidence also binds the Access revision, PlanHash, and
// complete provider network identity.
func ProviderTargetReference(
	accessID AccessID,
	targetID ProviderTargetID,
) string {
	return accessID.String() + "/" + targetID.String()
}

func (hash PlanHash) String() string {
	return hex.EncodeToString(hash[:])
}

func (hash PlanHash) IsZero() bool {
	return hash == PlanHash{}
}

// ClientOperationPlan is one immutable, compiled client-wire operation. It is
// part of the active Access plan and cannot select a provider target by itself.
type ClientOperationPlan struct {
	id             ClientOperationID
	revision       Revision
	clientDialect  Dialect
	methods        []string
	pathPattern    string
	pathMatch      ClientOperationPathMatch
	kind           ClientOperationKind
	transport      ClientOperationTransport
	bodyKind       ClientOperationBodyKind
	replayClass    ClientReplayClass
	codecFeature   CodecFeature
	maxBodyBytes   int64
	allowedQueries []string
	egressBearing  bool
}

func (plan ClientOperationPlan) ID() ClientOperationID { return plan.id }
func (plan ClientOperationPlan) Revision() Revision    { return plan.revision }
func (plan ClientOperationPlan) ClientDialect() Dialect {
	return plan.clientDialect
}
func (plan ClientOperationPlan) Methods() []string {
	return slices.Clone(plan.methods)
}
func (plan ClientOperationPlan) PathPattern() string {
	return plan.pathPattern
}
func (plan ClientOperationPlan) PathMatch() ClientOperationPathMatch {
	return plan.pathMatch
}
func (plan ClientOperationPlan) Kind() ClientOperationKind {
	return plan.kind
}
func (plan ClientOperationPlan) Transport() ClientOperationTransport {
	return plan.transport
}
func (plan ClientOperationPlan) BodyKind() ClientOperationBodyKind {
	return plan.bodyKind
}
func (plan ClientOperationPlan) ReplayClass() ClientReplayClass {
	return plan.replayClass
}
func (plan ClientOperationPlan) CodecFeature() CodecFeature {
	return plan.codecFeature
}
func (plan ClientOperationPlan) MaxBodyBytes() int64 {
	return plan.maxBodyBytes
}
func (plan ClientOperationPlan) AllowedQueries() []string {
	return slices.Clone(plan.allowedQueries)
}
func (plan ClientOperationPlan) EgressBearing() bool {
	return plan.egressBearing
}

func cloneClientOperationPlan(plan ClientOperationPlan) ClientOperationPlan {
	cloned := plan
	cloned.methods = slices.Clone(plan.methods)
	cloned.allowedQueries = slices.Clone(plan.allowedQueries)
	return cloned
}

type CodecPlan struct {
	id                   CodecPairID
	revision             Revision
	clientDialect        Dialect
	providerDialect      Dialect
	clientOperations     []ClientOperationPlan
	requiredCapabilities []ProviderCapability
}

type TransportFingerprintSource string

const (
	TransportFingerprintObservedClient TransportFingerprintSource = "observed_client"
	TransportFingerprintStandard       TransportFingerprintSource = "standard"
	TransportFingerprintCaptured       TransportFingerprintSource = "captured_profile"
)

type TransportFingerprintPreset string

const TransportFingerprintPresetClaudeCodeH1 TransportFingerprintPreset = "claude_code_2_1_220_darwin_arm64_h1"

type ApplicationProtocol string

const (
	ApplicationProtocolHTTP1 ApplicationProtocol = "http/1.1"
	ApplicationProtocolHTTP2 ApplicationProtocol = "h2"
)

type HTTPTransportKind string

const (
	HTTPTransportHTTP1 HTTPTransportKind = "http1"
	HTTPTransportHTTP2 HTTPTransportKind = "http2"
)

type TransportFingerprintTemplate struct {
	ref           TransportProfileRef
	revision      Revision
	source        TransportFingerprintSource
	preset        TransportFingerprintPreset
	httpTransport HTTPTransportKind
	alpn          []ApplicationProtocol
}

func (template TransportFingerprintTemplate) Ref() TransportProfileRef {
	return template.ref
}
func (template TransportFingerprintTemplate) Revision() Revision {
	return template.revision
}
func (template TransportFingerprintTemplate) Source() TransportFingerprintSource {
	return template.source
}
func (template TransportFingerprintTemplate) Preset() TransportFingerprintPreset {
	return template.preset
}
func (template TransportFingerprintTemplate) HTTPTransport() HTTPTransportKind {
	return template.httpTransport
}
func (template TransportFingerprintTemplate) ALPN() []ApplicationProtocol {
	return slices.Clone(template.alpn)
}

type CompiledTransportFingerprintPlan struct {
	requested TransportFingerprintTemplate
	fallbacks []TransportFingerprintTemplate
}

type UpstreamWireMode string

const (
	UpstreamWireModeFollowClient   UpstreamWireMode = "follow_client"
	UpstreamWireModeEmulateProduct UpstreamWireMode = "emulate_product"
)

type UpstreamWireProduct string

const UpstreamWireProductClaudeCode UpstreamWireProduct = "claude_code"

type CompiledUpstreamWireVariant struct {
	protocol          ApplicationProtocol
	transportPlan     CompiledTransportFingerprintPlan
	userAgentPolicy   UserAgentPolicy
	semanticUserAgent string
	evidenceDigest    string
}

type CompiledUpstreamWireProfile struct {
	ref      UpstreamWireProfileRef
	revision Revision
	mode     UpstreamWireMode
	product  UpstreamWireProduct
	variants []CompiledUpstreamWireVariant
}

func (profile CompiledUpstreamWireProfile) Ref() UpstreamWireProfileRef {
	return profile.ref
}
func (profile CompiledUpstreamWireProfile) Revision() Revision {
	return profile.revision
}
func (profile CompiledUpstreamWireProfile) Mode() UpstreamWireMode {
	return profile.mode
}
func (profile CompiledUpstreamWireProfile) Product() UpstreamWireProduct {
	return profile.product
}
func (profile CompiledUpstreamWireProfile) Variant(
	protocol ApplicationProtocol,
) (CompiledUpstreamWireVariant, bool) {
	for _, variant := range profile.variants {
		if variant.protocol == protocol {
			return cloneUpstreamWireVariant(variant), true
		}
	}
	return CompiledUpstreamWireVariant{}, false
}
func (profile CompiledUpstreamWireProfile) Variants() []CompiledUpstreamWireVariant {
	variants := make([]CompiledUpstreamWireVariant, len(profile.variants))
	for index, variant := range profile.variants {
		variants[index] = cloneUpstreamWireVariant(variant)
	}
	return variants
}

func (variant CompiledUpstreamWireVariant) Protocol() ApplicationProtocol {
	return variant.protocol
}
func (variant CompiledUpstreamWireVariant) TransportFingerprintPlan() CompiledTransportFingerprintPlan {
	return CompiledTransportFingerprintPlan{
		requested: cloneTransportTemplate(variant.transportPlan.requested),
		fallbacks: variant.transportPlan.Fallbacks(),
	}
}
func (variant CompiledUpstreamWireVariant) UserAgentPolicy() UserAgentPolicy {
	return variant.userAgentPolicy
}
func (variant CompiledUpstreamWireVariant) SemanticUserAgent() string {
	return variant.semanticUserAgent
}
func (variant CompiledUpstreamWireVariant) EvidenceDigest() string {
	return variant.evidenceDigest
}

func cloneUpstreamWireProfile(
	profile CompiledUpstreamWireProfile,
) CompiledUpstreamWireProfile {
	cloned := profile
	cloned.variants = profile.Variants()
	return cloned
}

func cloneUpstreamWireVariant(
	variant CompiledUpstreamWireVariant,
) CompiledUpstreamWireVariant {
	cloned := variant
	cloned.transportPlan = variant.TransportFingerprintPlan()
	return cloned
}

func (plan CompiledTransportFingerprintPlan) Requested() TransportFingerprintTemplate {
	return cloneTransportTemplate(plan.requested)
}
func (plan CompiledTransportFingerprintPlan) Fallbacks() []TransportFingerprintTemplate {
	fallbacks := make([]TransportFingerprintTemplate, len(plan.fallbacks))
	for index, template := range plan.fallbacks {
		fallbacks[index] = cloneTransportTemplate(template)
	}
	return fallbacks
}

func cloneTransportTemplate(
	template TransportFingerprintTemplate,
) TransportFingerprintTemplate {
	cloned := template
	cloned.alpn = slices.Clone(template.alpn)
	return cloned
}

func (plan CodecPlan) ID() CodecPairID          { return plan.id }
func (plan CodecPlan) Revision() Revision       { return plan.revision }
func (plan CodecPlan) ClientDialect() Dialect   { return plan.clientDialect }
func (plan CodecPlan) ProviderDialect() Dialect { return plan.providerDialect }
func (plan CodecPlan) ClientOperations() []ClientOperationPlan {
	operations := make([]ClientOperationPlan, len(plan.clientOperations))
	for index, operation := range plan.clientOperations {
		operations[index] = cloneClientOperationPlan(operation)
	}
	return operations
}
func (plan CodecPlan) RequiredCapabilities() []ProviderCapability {
	return slices.Clone(plan.requiredCapabilities)
}

type CompiledProviderTarget struct {
	target        ProviderTarget
	basePath      string
	httpAuthority string
	networkHost   string
	tlsServerName string
	port          uint16
	transportKind ProviderTransportKind
}

func (target CompiledProviderTarget) Target() ProviderTarget {
	cloned := target.target
	cloned.Capabilities = slices.Clone(target.target.Capabilities)
	return cloned
}
func (target CompiledProviderTarget) BasePath() string      { return target.basePath }
func (target CompiledProviderTarget) HTTPAuthority() string { return target.httpAuthority }
func (target CompiledProviderTarget) NetworkHost() string   { return target.networkHost }
func (target CompiledProviderTarget) TLSServerName() string { return target.tlsServerName }
func (target CompiledProviderTarget) Port() uint16          { return target.port }
func (target CompiledProviderTarget) TransportKind() ProviderTransportKind {
	return target.transportKind
}

type CompiledPluginPlan struct {
	revision   Revision
	mode       PluginPlanMode
	bindingIDs []PluginBindingID
}

func (plan CompiledPluginPlan) Revision() Revision   { return plan.revision }
func (plan CompiledPluginPlan) Mode() PluginPlanMode { return plan.mode }
func (plan CompiledPluginPlan) BindingIDs() []PluginBindingID {
	return slices.Clone(plan.bindingIDs)
}

type DependencyKind string

const (
	DependencyAccessBinding         DependencyKind = "access_binding"
	DependencyAgentEndpoint         DependencyKind = "agent_endpoint"
	DependencyEndpointProfile       DependencyKind = "endpoint_profile"
	DependencyProviderTarget        DependencyKind = "provider_target"
	DependencyAccountBinding        DependencyKind = "account_binding"
	DependencyModelPolicy           DependencyKind = "model_policy"
	DependencyRouteSet              DependencyKind = "route_set"
	DependencyAccessEgressPolicy    DependencyKind = "access_egress_policy"
	DependencyPluginPlan            DependencyKind = "plugin_plan"
	DependencyCodecPair             DependencyKind = "codec_pair"
	DependencyAuthDriver            DependencyKind = "auth_driver"
	DependencyEgressCapability      DependencyKind = "egress_capability"
	DependencyPluginPlanCapability  DependencyKind = "plugin_plan_capability"
	DependencyModelPolicyCapability DependencyKind = "model_policy_capability"
	DependencyTransportFingerprint  DependencyKind = "transport_fingerprint"
	DependencyUpstreamWireProfile   DependencyKind = "upstream_wire_profile"
	DependencyClientOperation       DependencyKind = "client_operation"
)

type DependencyRevision struct {
	Kind     DependencyKind
	ID       string
	Revision Revision
}

// AccessPlanSnapshot is the only active process-local Access configuration.
// Its fields are private and every collection getter returns a defensive copy.
// CompiledCandidate is one RouteSet candidate, frozen. Candidate zero is what
// every request starts with; a later one is only ever reached when the plan's
// fallback policy allowed a further attempt.
type CompiledCandidate struct {
	profileID   EndpointProfileID
	kind        EndpointProfileKind
	target      CompiledProviderTarget
	codecPlan   CodecPlan
	wireProfile CompiledUpstreamWireProfile
}

func (candidate CompiledCandidate) ProfileID() EndpointProfileID {
	return candidate.profileID
}

func (candidate CompiledCandidate) Kind() EndpointProfileKind {
	return candidate.kind
}

func (candidate CompiledCandidate) Target() CompiledProviderTarget {
	compiled := candidate.target
	compiled.target.Capabilities = slices.Clone(candidate.target.target.Capabilities)
	return compiled
}

func (candidate CompiledCandidate) CodecPlan() CodecPlan {
	plan := candidate.codecPlan
	plan.clientOperations = candidate.codecPlan.ClientOperations()
	plan.requiredCapabilities = slices.Clone(plan.requiredCapabilities)
	return plan
}

func (candidate CompiledCandidate) UpstreamWireProfile() CompiledUpstreamWireProfile {
	return cloneUpstreamWireProfile(candidate.wireProfile)
}

type AccessPlanSnapshot struct {
	aggregate       Aggregate
	candidates      []CompiledCandidate
	compiledTargets []CompiledProviderTarget
	codecPlan       CodecPlan
	wireProfile     CompiledUpstreamWireProfile
	pluginPlan      CompiledPluginPlan
	dependencies    []DependencyRevision
	planHash        PlanHash
}

func (snapshot AccessPlanSnapshot) AccessID() AccessID {
	return snapshot.aggregate.Binding.ID
}

func (snapshot AccessPlanSnapshot) Revision() Revision {
	return snapshot.aggregate.Binding.Revision
}

func (snapshot AccessPlanSnapshot) PlanHash() PlanHash {
	return snapshot.planHash
}

func (snapshot AccessPlanSnapshot) Binding() AccessBinding {
	binding := snapshot.aggregate.Binding
	binding.ProfileIDs = slices.Clone(binding.ProfileIDs)
	return binding
}

func (snapshot AccessPlanSnapshot) AgentEndpoint() AgentEndpoint {
	return snapshot.aggregate.AgentEndpoint
}

func (snapshot AccessPlanSnapshot) EndpointProfiles() []EndpointProfile {
	profiles := make([]EndpointProfile, len(snapshot.aggregate.Profiles))
	for index, profile := range snapshot.aggregate.Profiles {
		profiles[index] = profile
		profiles[index].AccountBindingIDs = slices.Clone(profile.AccountBindingIDs)
	}
	return profiles
}

// CandidateCount is how many upstreams this plan can be attempted against.
func (snapshot AccessPlanSnapshot) CandidateCount() int {
	return len(snapshot.candidates)
}

// Candidate freezes one attempt's upstream, codec, and transport.
func (snapshot AccessPlanSnapshot) Candidate(index int) (CompiledCandidate, bool) {
	if index < 0 || index >= len(snapshot.candidates) {
		return CompiledCandidate{}, false
	}
	return snapshot.candidates[index], true
}

func (snapshot AccessPlanSnapshot) ProviderTargets() []CompiledProviderTarget {
	targets := make([]CompiledProviderTarget, len(snapshot.compiledTargets))
	for index, target := range snapshot.compiledTargets {
		targets[index] = target
		targets[index].target.Capabilities = slices.Clone(target.target.Capabilities)
	}
	return targets
}

func (snapshot AccessPlanSnapshot) AccountBindings() []ProviderAccountBinding {
	return slices.Clone(snapshot.aggregate.AccountBindings)
}

func (snapshot AccessPlanSnapshot) RouteSets() []RouteSet {
	routeSets := make([]RouteSet, len(snapshot.aggregate.RouteSets))
	for index, routeSet := range snapshot.aggregate.RouteSets {
		routeSets[index] = routeSet
		routeSets[index].CandidateProfileIDs = slices.Clone(routeSet.CandidateProfileIDs)
	}
	return routeSets
}

func (snapshot AccessPlanSnapshot) EgressPolicy() AccessEgressPolicy {
	return snapshot.aggregate.EgressPolicy
}

func (snapshot AccessPlanSnapshot) CodecPlan() CodecPlan {
	plan := snapshot.codecPlan
	plan.clientOperations = snapshot.codecPlan.ClientOperations()
	plan.requiredCapabilities = slices.Clone(plan.requiredCapabilities)
	return plan
}

func (snapshot AccessPlanSnapshot) UpstreamWireProfile() CompiledUpstreamWireProfile {
	return cloneUpstreamWireProfile(snapshot.wireProfile)
}

func (snapshot AccessPlanSnapshot) PluginPlan() CompiledPluginPlan {
	plan := snapshot.pluginPlan
	plan.bindingIDs = slices.Clone(plan.bindingIDs)
	return plan
}

func (snapshot AccessPlanSnapshot) DependencyRevisions() []DependencyRevision {
	return slices.Clone(snapshot.dependencies)
}

func (snapshot AccessPlanSnapshot) validate() error {
	if err := snapshot.aggregate.Validate(); err != nil {
		return err
	}
	if snapshot.planHash.IsZero() {
		return fmt.Errorf("%w: plan hash is empty", ErrInvalidAccessPlan)
	}
	if len(snapshot.compiledTargets) == 0 || len(snapshot.dependencies) == 0 {
		return fmt.Errorf("%w: compiled plan is incomplete", ErrInvalidAccessPlan)
	}
	if len(snapshot.codecPlan.clientOperations) == 0 {
		return fmt.Errorf(
			"%w: client operation plan is empty",
			ErrInvalidAccessPlan,
		)
	}
	if snapshot.wireProfile.ref.String() == "" ||
		snapshot.wireProfile.revision == 0 ||
		len(snapshot.wireProfile.variants) == 0 {
		return fmt.Errorf(
			"%w: upstream wire profile is incomplete",
			ErrInvalidAccessPlan,
		)
	}
	return nil
}

func (snapshot AccessPlanSnapshot) clone() AccessPlanSnapshot {
	cloned := snapshot
	cloned.aggregate = snapshot.aggregate.Clone()
	cloned.compiledTargets = snapshot.ProviderTargets()
	cloned.codecPlan = snapshot.CodecPlan()
	cloned.wireProfile = snapshot.UpstreamWireProfile()
	cloned.pluginPlan = snapshot.PluginPlan()
	cloned.dependencies = slices.Clone(snapshot.dependencies)
	return cloned
}

func newPlanHash(canonical []byte) PlanHash {
	return sha256.Sum256(canonical)
}
