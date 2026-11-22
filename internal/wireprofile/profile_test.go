package wireprofile

import (
	"errors"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/productbuild"
)

func TestBuiltInViberMateProfilePresentsViberMateOverStandardTLS(t *testing.T) {
	t.Parallel()
	catalog, err := BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	ref, err := NewUpstreamWireProfileRef("vibermate")
	if err != nil || ref != ViberMateUpstreamWireProfileRef() {
		t.Fatalf("vibermate ref = %q, %v", ref, err)
	}
	profile, err := catalog.Resolve(ref)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Ref() != ref || profile.Revision() != 1 ||
		profile.Mode() != UpstreamWireModeOwnIdentity || profile.Mode() != "own_identity" ||
		profile.Product() != "" || len(profile.Variants()) != 2 {
		t.Fatalf("vibermate profile = %+v", profile)
	}
	wantUserAgent := "ViberMate/" + productbuild.Label()
	if ViberMateUserAgent() != wantUserAgent || !productbuild.Valid(productbuild.Label()) {
		t.Fatalf("ViberMateUserAgent() = %q, want %q", ViberMateUserAgent(), wantUserAgent)
	}
	for protocol, want := range map[ApplicationProtocol]struct {
		ref       string
		transport HTTPTransportKind
	}{
		ApplicationProtocolHTTP1: {TransportProfileStandardH1Value, HTTPTransportHTTP1},
		ApplicationProtocolHTTP2: {TransportProfileStandardH2Value, HTTPTransportHTTP2},
	} {
		variant, ok := profile.Variant(protocol)
		if !ok {
			t.Fatalf("vibermate has no %q variant", protocol)
		}
		plan := variant.TransportFingerprintPlan()
		requested := plan.Requested()
		if variant.Protocol() != protocol ||
			variant.UserAgentPolicy() != UserAgentPolicyConstant ||
			variant.SemanticUserAgent() != wantUserAgent ||
			variant.EvidenceDigest() != "" ||
			requested.Ref().String() != want.ref ||
			requested.Source() != TransportFingerprintStandard ||
			requested.Preset() != "" ||
			requested.HTTPTransport() != want.transport ||
			len(requested.ALPN()) != 1 || requested.ALPN()[0] != protocol ||
			len(plan.Fallbacks()) != 0 {
			t.Fatalf("vibermate %q variant = %+v, plan = %+v", protocol, variant, plan)
		}
	}
}

func TestOwnIdentityProfileRefusesAnyOtherPresentation(t *testing.T) {
	t.Parallel()
	ref := func(value string) TransportProfileRef {
		result, err := NewTransportProfileRef(value)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	// A standard transport is still refused when it can fall back to a
	// replayed ClientHello.
	transports := append(BuiltInTransportFingerprintDefinitions(), TransportFingerprintDefinition{
		Ref: ref("standard-with-fallback-h1"), Revision: 1, Source: TransportFingerprintStandard,
		HTTPTransport: HTTPTransportHTTP1, ALPN: []ApplicationProtocol{ApplicationProtocolHTTP1},
		FallbackRefs: []TransportProfileRef{ref(TransportProfileObservedClientH1Value)},
	})
	valid := func() UpstreamWireProfileDefinition {
		return UpstreamWireProfileDefinition{
			Ref: ViberMateUpstreamWireProfileRef(), Revision: 1, Mode: UpstreamWireModeOwnIdentity,
			Variants: []UpstreamWireVariantDefinition{{
				Protocol: ApplicationProtocolHTTP1, TransportProfileRef: ref(TransportProfileStandardH1Value),
				UserAgentPolicy: UserAgentPolicyConstant, SemanticUserAgent: "ViberMate/v1.2.3",
			}},
		}
	}
	if _, err := NewCatalog(transports, []UpstreamWireProfileDefinition{valid()}); err != nil {
		t.Fatalf("valid own-identity profile refused: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*UpstreamWireProfileDefinition)
	}{
		{"names a product", func(value *UpstreamWireProfileDefinition) {
			value.Product = UpstreamWireProductClaudeCode
		}},
		{"follows the client User-Agent", func(value *UpstreamWireProfileDefinition) {
			value.Variants[0].UserAgentPolicy = UserAgentPolicyFollowClient
			value.Variants[0].SemanticUserAgent = ""
		}},
		{"omits the User-Agent", func(value *UpstreamWireProfileDefinition) {
			value.Variants[0].UserAgentPolicy = UserAgentPolicyOmit
			value.Variants[0].SemanticUserAgent = ""
		}},
		{"sends another product's User-Agent", func(value *UpstreamWireProfileDefinition) {
			value.Variants[0].SemanticUserAgent = "claude-cli/2.1.220 (external, sdk-cli)"
		}},
		{"sends a bare product token", func(value *UpstreamWireProfileDefinition) {
			value.Variants[0].SemanticUserAgent = "ViberMate/"
		}},
		{"carries product evidence", func(value *UpstreamWireProfileDefinition) {
			value.Variants[0].EvidenceDigest = strings.Repeat("a", 64)
		}},
		{"replays the client ClientHello", func(value *UpstreamWireProfileDefinition) {
			value.Variants[0].TransportProfileRef = ref(TransportProfileObservedClientH1Value)
		}},
		{"replays a captured ClientHello", func(value *UpstreamWireProfileDefinition) {
			value.Variants[0].TransportProfileRef = ref(TransportProfileClaudeCodeH1Value)
		}},
		{"falls back to another transport", func(value *UpstreamWireProfileDefinition) {
			value.Variants[0].TransportProfileRef = ref("standard-with-fallback-h1")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			definition := valid()
			test.mutate(&definition)
			_, err := NewCatalog(transports, []UpstreamWireProfileDefinition{definition})
			if !errors.Is(err, ErrInvalidProfile) {
				t.Fatalf("own-identity profile that %s compiled: %v", test.name, err)
			}
		})
	}
}

func TestBuiltInCatalogListsEveryProfileOnce(t *testing.T) {
	t.Parallel()
	catalog, err := BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, definition := range BuiltInUpstreamWireProfileDefinitions() {
		refs = append(refs, definition.Ref.String())
	}
	if strings.Join(refs, ",") != "follow-client,claude-code,vibermate" {
		t.Fatalf("built-in wire profiles = %v", refs)
	}
	for _, value := range refs {
		ref, err := NewUpstreamWireProfileRef(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.Resolve(ref); err != nil {
			t.Fatalf("built-in profile %q does not resolve: %v", value, err)
		}
	}
}

func TestBuiltInFollowClientIsTheDefaultPassthroughProfile(t *testing.T) {
	t.Parallel()
	catalog, err := BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := catalog.Resolve(FollowClientUpstreamWireProfileRef())
	if err != nil || profile.Mode() != UpstreamWireModeFollowClient || len(profile.Variants()) != 2 {
		t.Fatalf("profile = %+v, %v", profile, err)
	}
	for _, protocol := range []ApplicationProtocol{ApplicationProtocolHTTP1, ApplicationProtocolHTTP2} {
		variant, ok := profile.Variant(protocol)
		if !ok || variant.UserAgentPolicy() != UserAgentPolicyFollowClient ||
			variant.TransportFingerprintPlan().Requested().Source() != TransportFingerprintObservedClient {
			t.Fatalf("passthrough variant %q = %+v", protocol, variant)
		}
	}
}

func TestCatalogResolvesAStandaloneTransportPlanDefensively(t *testing.T) {
	t.Parallel()
	catalog, err := BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	ref, err := NewTransportProfileRef(TransportProfileStandardH1Value)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := catalog.ResolveTransport(ref)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Requested().Ref() != ref ||
		plan.Requested().HTTPTransport() != HTTPTransportHTTP1 ||
		plan.Requested().Source() != TransportFingerprintStandard {
		t.Fatalf("standalone transport plan = %+v", plan)
	}
	alpn := plan.Requested().ALPN()
	alpn[0] = "mutated"
	again, err := catalog.ResolveTransport(ref)
	if err != nil {
		t.Fatal(err)
	}
	if again.Requested().ALPN()[0] != ApplicationProtocolHTTP1 {
		t.Fatal("caller mutated the catalog transport plan")
	}
}

func TestCompiledProfileDefensivelyCopiesCollections(t *testing.T) {
	t.Parallel()
	catalog, err := BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := catalog.Resolve(FollowClientUpstreamWireProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	variants := profile.Variants()
	variants[0] = CompiledUpstreamWireVariant{}
	alpn := profile.Variants()[0].TransportFingerprintPlan().Requested().ALPN()
	alpn[0] = "mutated"
	if len(profile.Variants()) != 2 || !profile.Variants()[0].Protocol().Valid() ||
		!profile.Variants()[0].TransportFingerprintPlan().Requested().ALPN()[0].Valid() {
		t.Fatal("caller mutated compiled wire profile")
	}
}
