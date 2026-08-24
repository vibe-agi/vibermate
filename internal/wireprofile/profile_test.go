package wireprofile

import "testing"

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
