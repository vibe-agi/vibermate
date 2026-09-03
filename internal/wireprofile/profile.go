// Package wireprofile owns immutable HTTP/TLS presentation plans. It does not
// select an Environment, route, provider account, or network destination.
package wireprofile

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxIDBytes                      = 128
	MaxUserAgentBytes               = 1024
	MaxEvidenceDigestBytes          = 128
	MaxRevision            Revision = 1<<63 - 1

	TransportProfileObservedClientH1Value = "observed-client-strict-h1"
	TransportProfileObservedClientH2Value = "observed-client-strict-h2"
	TransportProfileStandardH1Value       = "standard-strict-h1"
	TransportProfileStandardH2Value       = "standard-strict-h2"
	TransportProfileClaudeCodeH1Value     = "claude-code-2.1.220-darwin-arm64-strict-h1"

	UpstreamWireProfileFollowClientValue = "follow-client"
	UpstreamWireProfileClaudeCodeValue   = "claude-code"
)

var ErrInvalidProfile = errors.New("wire profile is invalid")

type Revision uint64
type TransportProfileRef struct{ value string }
type UpstreamWireProfileRef struct{ value string }

func NewTransportProfileRef(value string) (TransportProfileRef, error) {
	if err := validateID("transport profile reference", value); err != nil {
		return TransportProfileRef{}, err
	}
	return TransportProfileRef{value: value}, nil
}

func NewUpstreamWireProfileRef(value string) (UpstreamWireProfileRef, error) {
	if err := validateID("upstream wire profile reference", value); err != nil {
		return UpstreamWireProfileRef{}, err
	}
	return UpstreamWireProfileRef{value: value}, nil
}

func (ref TransportProfileRef) String() string    { return ref.value }
func (ref UpstreamWireProfileRef) String() string { return ref.value }

func FollowClientUpstreamWireProfileRef() UpstreamWireProfileRef {
	return UpstreamWireProfileRef{value: UpstreamWireProfileFollowClientValue}
}

func ClaudeCodeUpstreamWireProfileRef() UpstreamWireProfileRef {
	return UpstreamWireProfileRef{value: UpstreamWireProfileClaudeCodeValue}
}

type ApplicationProtocol string

const (
	ApplicationProtocolHTTP1 ApplicationProtocol = "http/1.1"
	ApplicationProtocolHTTP2 ApplicationProtocol = "h2"
)

func (protocol ApplicationProtocol) Valid() bool {
	return protocol == ApplicationProtocolHTTP1 || protocol == ApplicationProtocolHTTP2
}

type HTTPTransportKind string

const (
	HTTPTransportHTTP1 HTTPTransportKind = "http1"
	HTTPTransportHTTP2 HTTPTransportKind = "http2"
)

type TransportFingerprintSource string

const (
	TransportFingerprintObservedClient TransportFingerprintSource = "observed_client"
	TransportFingerprintStandard       TransportFingerprintSource = "standard"
	TransportFingerprintCaptured       TransportFingerprintSource = "captured_profile"
)

type TransportFingerprintPreset string

const TransportFingerprintPresetClaudeCodeH1 TransportFingerprintPreset = "claude_code_2_1_220_darwin_arm64_h1"

type TransportFingerprintDefinition struct {
	Ref           TransportProfileRef
	Revision      Revision
	Source        TransportFingerprintSource
	Preset        TransportFingerprintPreset
	HTTPTransport HTTPTransportKind
	ALPN          []ApplicationProtocol
	FallbackRefs  []TransportProfileRef
}

type TransportFingerprintTemplate struct {
	ref           TransportProfileRef
	revision      Revision
	source        TransportFingerprintSource
	preset        TransportFingerprintPreset
	httpTransport HTTPTransportKind
	alpn          []ApplicationProtocol
}

func (template TransportFingerprintTemplate) Ref() TransportProfileRef { return template.ref }
func (template TransportFingerprintTemplate) Revision() Revision       { return template.revision }
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

func (plan CompiledTransportFingerprintPlan) Requested() TransportFingerprintTemplate {
	return cloneTemplate(plan.requested)
}
func (plan CompiledTransportFingerprintPlan) Fallbacks() []TransportFingerprintTemplate {
	result := make([]TransportFingerprintTemplate, len(plan.fallbacks))
	for index, template := range plan.fallbacks {
		result[index] = cloneTemplate(template)
	}
	return result
}

type UpstreamWireMode string

const (
	UpstreamWireModeFollowClient   UpstreamWireMode = "follow_client"
	UpstreamWireModeEmulateProduct UpstreamWireMode = "emulate_product"
)

type UpstreamWireProduct string

const UpstreamWireProductClaudeCode UpstreamWireProduct = "claude_code"

type UserAgentPolicy string

const (
	UserAgentPolicyOmit         UserAgentPolicy = "omit"
	UserAgentPolicyFollowClient UserAgentPolicy = "follow_client"
	UserAgentPolicyConstant     UserAgentPolicy = "constant"
)

type UpstreamWireProfileDefinition struct {
	Ref      UpstreamWireProfileRef
	Revision Revision
	Mode     UpstreamWireMode
	Product  UpstreamWireProduct
	Variants []UpstreamWireVariantDefinition
}

type UpstreamWireVariantDefinition struct {
	Protocol            ApplicationProtocol
	TransportProfileRef TransportProfileRef
	UserAgentPolicy     UserAgentPolicy
	SemanticUserAgent   string
	EvidenceDigest      string
}

type CompiledUpstreamWireVariant struct {
	protocol          ApplicationProtocol
	transportPlan     CompiledTransportFingerprintPlan
	userAgentPolicy   UserAgentPolicy
	semanticUserAgent string
	evidenceDigest    string
}

func (variant CompiledUpstreamWireVariant) Protocol() ApplicationProtocol { return variant.protocol }
func (variant CompiledUpstreamWireVariant) TransportFingerprintPlan() CompiledTransportFingerprintPlan {
	return cloneTransportPlan(variant.transportPlan)
}
func (variant CompiledUpstreamWireVariant) UserAgentPolicy() UserAgentPolicy {
	return variant.userAgentPolicy
}
func (variant CompiledUpstreamWireVariant) SemanticUserAgent() string {
	return variant.semanticUserAgent
}
func (variant CompiledUpstreamWireVariant) EvidenceDigest() string { return variant.evidenceDigest }

type CompiledUpstreamWireProfile struct {
	ref      UpstreamWireProfileRef
	revision Revision
	mode     UpstreamWireMode
	product  UpstreamWireProduct
	variants []CompiledUpstreamWireVariant
}

func (profile CompiledUpstreamWireProfile) Ref() UpstreamWireProfileRef  { return profile.ref }
func (profile CompiledUpstreamWireProfile) Revision() Revision           { return profile.revision }
func (profile CompiledUpstreamWireProfile) Mode() UpstreamWireMode       { return profile.mode }
func (profile CompiledUpstreamWireProfile) Product() UpstreamWireProduct { return profile.product }
func (profile CompiledUpstreamWireProfile) Variant(protocol ApplicationProtocol) (CompiledUpstreamWireVariant, bool) {
	for _, variant := range profile.variants {
		if variant.protocol == protocol {
			return cloneVariant(variant), true
		}
	}
	return CompiledUpstreamWireVariant{}, false
}
func (profile CompiledUpstreamWireProfile) Variants() []CompiledUpstreamWireVariant {
	result := make([]CompiledUpstreamWireVariant, len(profile.variants))
	for index, variant := range profile.variants {
		result[index] = cloneVariant(variant)
	}
	return result
}

type Catalog struct {
	transports map[TransportProfileRef]TransportFingerprintDefinition
	profiles   map[UpstreamWireProfileRef]CompiledUpstreamWireProfile
}

func NewCatalog(
	transports []TransportFingerprintDefinition,
	profiles []UpstreamWireProfileDefinition,
) (Catalog, error) {
	catalog := Catalog{
		transports: make(map[TransportProfileRef]TransportFingerprintDefinition, len(transports)),
		profiles:   make(map[UpstreamWireProfileRef]CompiledUpstreamWireProfile, len(profiles)),
	}
	for _, definition := range transports {
		normalized, err := normalizeTransport(definition)
		if err != nil {
			return Catalog{}, err
		}
		if _, duplicate := catalog.transports[normalized.Ref]; duplicate {
			return Catalog{}, ErrInvalidProfile
		}
		catalog.transports[normalized.Ref] = normalized
	}
	for _, definition := range profiles {
		compiled, err := catalog.compileProfile(definition)
		if err != nil {
			return Catalog{}, err
		}
		if _, duplicate := catalog.profiles[compiled.ref]; duplicate {
			return Catalog{}, ErrInvalidProfile
		}
		catalog.profiles[compiled.ref] = compiled
	}
	return catalog, nil
}

func BuiltInCatalog() (Catalog, error) {
	return NewCatalog(BuiltInTransportFingerprintDefinitions(), BuiltInUpstreamWireProfileDefinitions())
}

func (catalog Catalog) Resolve(ref UpstreamWireProfileRef) (CompiledUpstreamWireProfile, error) {
	profile, exists := catalog.profiles[ref]
	if !exists {
		return CompiledUpstreamWireProfile{}, fmt.Errorf("%w: profile %q", ErrInvalidProfile, ref.String())
	}
	return cloneProfile(profile), nil
}

// ResolveTransport compiles one catalog transport profile without inventing
// an application-wire profile. Runtime-owned requests such as Endpoint model
// discovery use this narrower boundary because they have no Agent presentation
// identity to emulate.
func (catalog Catalog) ResolveTransport(
	ref TransportProfileRef,
) (CompiledTransportFingerprintPlan, error) {
	definition, exists := catalog.transports[ref]
	if !exists {
		return CompiledTransportFingerprintPlan{}, fmt.Errorf(
			"%w: transport profile %q",
			ErrInvalidProfile,
			ref.String(),
		)
	}
	plan, err := catalog.compileTransportPlan(definition)
	if err != nil {
		return CompiledTransportFingerprintPlan{}, err
	}
	return cloneTransportPlan(plan), nil
}

func (catalog Catalog) compileProfile(definition UpstreamWireProfileDefinition) (CompiledUpstreamWireProfile, error) {
	if validateID("upstream wire profile reference", definition.Ref.value) != nil ||
		definition.Revision == 0 || definition.Revision > MaxRevision || len(definition.Variants) == 0 {
		return CompiledUpstreamWireProfile{}, ErrInvalidProfile
	}
	switch definition.Mode {
	case UpstreamWireModeFollowClient:
		if definition.Product != "" {
			return CompiledUpstreamWireProfile{}, ErrInvalidProfile
		}
	case UpstreamWireModeEmulateProduct:
		if definition.Product == "" {
			return CompiledUpstreamWireProfile{}, ErrInvalidProfile
		}
	default:
		return CompiledUpstreamWireProfile{}, ErrInvalidProfile
	}
	variants := make([]CompiledUpstreamWireVariant, 0, len(definition.Variants))
	seen := make(map[ApplicationProtocol]struct{}, len(definition.Variants))
	for _, variant := range definition.Variants {
		if !variant.Protocol.Valid() {
			return CompiledUpstreamWireProfile{}, ErrInvalidProfile
		}
		if _, duplicate := seen[variant.Protocol]; duplicate {
			return CompiledUpstreamWireProfile{}, ErrInvalidProfile
		}
		seen[variant.Protocol] = struct{}{}
		transport, exists := catalog.transports[variant.TransportProfileRef]
		if !exists || transport.ALPN[0] != variant.Protocol {
			return CompiledUpstreamWireProfile{}, ErrInvalidProfile
		}
		plan, err := catalog.compileTransportPlan(transport)
		if err != nil {
			return CompiledUpstreamWireProfile{}, err
		}
		switch variant.UserAgentPolicy {
		case UserAgentPolicyOmit, UserAgentPolicyFollowClient:
			if variant.SemanticUserAgent != "" {
				return CompiledUpstreamWireProfile{}, ErrInvalidProfile
			}
		case UserAgentPolicyConstant:
			if !validPresentationText(variant.SemanticUserAgent, MaxUserAgentBytes) {
				return CompiledUpstreamWireProfile{}, ErrInvalidProfile
			}
		default:
			return CompiledUpstreamWireProfile{}, ErrInvalidProfile
		}
		if len(variant.EvidenceDigest) > MaxEvidenceDigestBytes ||
			strings.ContainsAny(variant.EvidenceDigest, " \t\r\n") {
			return CompiledUpstreamWireProfile{}, ErrInvalidProfile
		}
		variants = append(variants, CompiledUpstreamWireVariant{
			protocol: variant.Protocol, transportPlan: plan,
			userAgentPolicy: variant.UserAgentPolicy, semanticUserAgent: variant.SemanticUserAgent,
			evidenceDigest: variant.EvidenceDigest,
		})
	}
	sort.Slice(variants, func(left, right int) bool { return variants[left].protocol < variants[right].protocol })
	return CompiledUpstreamWireProfile{
		ref: definition.Ref, revision: definition.Revision, mode: definition.Mode,
		product: definition.Product, variants: variants,
	}, nil
}

func (catalog Catalog) compileTransportPlan(definition TransportFingerprintDefinition) (CompiledTransportFingerprintPlan, error) {
	requested := templateFromDefinition(definition)
	fallbacks := make([]TransportFingerprintTemplate, 0, len(definition.FallbackRefs))
	for _, ref := range definition.FallbackRefs {
		fallback, exists := catalog.transports[ref]
		if !exists || fallback.HTTPTransport != definition.HTTPTransport {
			return CompiledTransportFingerprintPlan{}, ErrInvalidProfile
		}
		fallbacks = append(fallbacks, templateFromDefinition(fallback))
	}
	return CompiledTransportFingerprintPlan{requested: requested, fallbacks: fallbacks}, nil
}

func normalizeTransport(definition TransportFingerprintDefinition) (TransportFingerprintDefinition, error) {
	if validateID("transport profile reference", definition.Ref.value) != nil ||
		definition.Revision == 0 || definition.Revision > MaxRevision || len(definition.ALPN) != 1 {
		return TransportFingerprintDefinition{}, ErrInvalidProfile
	}
	expected := ApplicationProtocolHTTP1
	switch definition.HTTPTransport {
	case HTTPTransportHTTP1:
	case HTTPTransportHTTP2:
		expected = ApplicationProtocolHTTP2
	default:
		return TransportFingerprintDefinition{}, ErrInvalidProfile
	}
	if definition.ALPN[0] != expected {
		return TransportFingerprintDefinition{}, ErrInvalidProfile
	}
	switch definition.Source {
	case TransportFingerprintObservedClient, TransportFingerprintStandard:
		if definition.Preset != "" {
			return TransportFingerprintDefinition{}, ErrInvalidProfile
		}
	case TransportFingerprintCaptured:
		if definition.Preset == "" {
			return TransportFingerprintDefinition{}, ErrInvalidProfile
		}
	default:
		return TransportFingerprintDefinition{}, ErrInvalidProfile
	}
	definition.ALPN = slices.Clone(definition.ALPN)
	definition.FallbackRefs = slices.Clone(definition.FallbackRefs)
	return definition, nil
}

func BuiltInTransportFingerprintDefinitions() []TransportFingerprintDefinition {
	ref := func(value string) TransportProfileRef {
		result, _ := NewTransportProfileRef(value)
		return result
	}
	return []TransportFingerprintDefinition{
		{Ref: ref(TransportProfileObservedClientH1Value), Revision: 1, Source: TransportFingerprintObservedClient, HTTPTransport: HTTPTransportHTTP1, ALPN: []ApplicationProtocol{ApplicationProtocolHTTP1}},
		{Ref: ref(TransportProfileObservedClientH2Value), Revision: 1, Source: TransportFingerprintObservedClient, HTTPTransport: HTTPTransportHTTP2, ALPN: []ApplicationProtocol{ApplicationProtocolHTTP2}},
		{Ref: ref(TransportProfileStandardH1Value), Revision: 1, Source: TransportFingerprintStandard, HTTPTransport: HTTPTransportHTTP1, ALPN: []ApplicationProtocol{ApplicationProtocolHTTP1}},
		{Ref: ref(TransportProfileStandardH2Value), Revision: 1, Source: TransportFingerprintStandard, HTTPTransport: HTTPTransportHTTP2, ALPN: []ApplicationProtocol{ApplicationProtocolHTTP2}},
		{Ref: ref(TransportProfileClaudeCodeH1Value), Revision: 1, Source: TransportFingerprintCaptured, Preset: TransportFingerprintPresetClaudeCodeH1, HTTPTransport: HTTPTransportHTTP1, ALPN: []ApplicationProtocol{ApplicationProtocolHTTP1}},
	}
}

func BuiltInUpstreamWireProfileDefinitions() []UpstreamWireProfileDefinition {
	transport := func(value string) TransportProfileRef { result, _ := NewTransportProfileRef(value); return result }
	return []UpstreamWireProfileDefinition{
		{Ref: FollowClientUpstreamWireProfileRef(), Revision: 1, Mode: UpstreamWireModeFollowClient, Variants: []UpstreamWireVariantDefinition{
			{Protocol: ApplicationProtocolHTTP1, TransportProfileRef: transport(TransportProfileObservedClientH1Value), UserAgentPolicy: UserAgentPolicyFollowClient},
			{Protocol: ApplicationProtocolHTTP2, TransportProfileRef: transport(TransportProfileObservedClientH2Value), UserAgentPolicy: UserAgentPolicyFollowClient},
		}},
		{Ref: ClaudeCodeUpstreamWireProfileRef(), Revision: 1, Mode: UpstreamWireModeEmulateProduct, Product: UpstreamWireProductClaudeCode, Variants: []UpstreamWireVariantDefinition{{
			Protocol: ApplicationProtocolHTTP1, TransportProfileRef: transport(TransportProfileClaudeCodeH1Value),
			UserAgentPolicy: UserAgentPolicyConstant, SemanticUserAgent: "claude-cli/2.1.220 (external, sdk-cli)",
			EvidenceDigest: "5b4322f09d5dbf5cb3a56c3724a01a9c795f57d4b2e9178c2242b9e524543393",
		}}},
	}
}

func templateFromDefinition(definition TransportFingerprintDefinition) TransportFingerprintTemplate {
	return TransportFingerprintTemplate{
		ref: definition.Ref, revision: definition.Revision, source: definition.Source,
		preset: definition.Preset, httpTransport: definition.HTTPTransport, alpn: slices.Clone(definition.ALPN),
	}
}
func cloneTemplate(value TransportFingerprintTemplate) TransportFingerprintTemplate {
	value.alpn = slices.Clone(value.alpn)
	return value
}
func cloneTransportPlan(value CompiledTransportFingerprintPlan) CompiledTransportFingerprintPlan {
	return CompiledTransportFingerprintPlan{requested: cloneTemplate(value.requested), fallbacks: value.Fallbacks()}
}
func cloneVariant(value CompiledUpstreamWireVariant) CompiledUpstreamWireVariant {
	value.transportPlan = cloneTransportPlan(value.transportPlan)
	return value
}
func cloneProfile(value CompiledUpstreamWireProfile) CompiledUpstreamWireProfile {
	value.variants = value.Variants()
	return value
}

func validateID(label, value string) error {
	if value == "" || len(value) > MaxIDBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s is not canonical", ErrInvalidProfile, label)
	}
	for _, character := range value {
		if unicode.IsControl(character) || character > unicode.MaxASCII ||
			!(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') &&
				character != '-' && character != '_' && character != '.' {
			return ErrInvalidProfile
		}
	}
	return nil
}

func validPresentationText(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
