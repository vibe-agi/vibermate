package environment

import (
	"reflect"
	"testing"
)

func TestLaunchAuthorityRecordsCredentialRewriteOnlyForAllUpstreamDestinations(t *testing.T) {
	t.Parallel()

	allUpstreamAggregate := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	allUpstream := mustCompile(t, allUpstreamAggregate)
	mixedAggregate := allUpstreamAggregate.Clone()
	mixedAggregate.ClientEndpoints[0].ProtocolPlans[0].Destination =
		DestinationPlan{Kind: DestinationKindOriginal}
	mixed := mustCompile(t, mixedAggregate)
	mixedBoundary, err := NewLaunchAuthorityBoundary(mixed)
	if err != nil {
		t.Fatal(err)
	}
	upstreamBoundary, err := NewLaunchAuthorityBoundary(allUpstream)
	if err != nil {
		t.Fatal(err)
	}
	if got := mixedBoundary.ManagedCredentialAuthorities(); len(got) != 0 {
		t.Fatalf("mixed endpoint enabled authority-wide rewrite: %v", got)
	}
	if got := upstreamBoundary.ManagedCredentialAuthorities(); len(got) != 1 || got[0] != "relay.example:443" {
		t.Fatalf("all-upstream endpoint rewrite scope = %v", got)
	}
}

func TestNativeLaunchAuthorityScopesKeepModelCredentialReplacement(t *testing.T) {
	value := clientToolFixture(t)
	for _, shared := range []bool{false, true} {
		if !shared {
			value.ClientToolAccess.Route.TargetOrigin = mustProviderOrigin(t, "https://native.example")
		} else {
			value.ClientToolAccess.Route.TargetOrigin = mustProviderOrigin(t, "https://chatgpt.com")
		}
		boundary, err := NewLaunchAuthorityBoundary(mustCompile(t, value))
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"chatgpt.com:443", "native.example:443"}
		if shared {
			want = []string{"chatgpt.com:443"}
		}
		if !reflect.DeepEqual(boundary.ProtectedAuthorities(), want) {
			t.Fatalf("protected: %v", boundary.ProtectedAuthorities())
		}
		if !reflect.DeepEqual(boundary.ManagedCredentialAuthorities(), []string{"chatgpt.com:443"}) {
			t.Fatalf("model credential scope: %v", boundary.ManagedCredentialAuthorities())
		}
	}
	off := value.Clone()
	off.ClientToolAccess = nil
	before, err := NewLaunchAuthorityBoundary(mustCompile(t, off))
	if err != nil {
		t.Fatal(err)
	}
	off.ClientToolAccess = &ClientToolAccessPolicy{}
	after, err := NewLaunchAuthorityBoundary(mustCompile(t, off))
	if err != nil || before != after {
		t.Fatal("explicit OFF changed launch canonical evidence")
	}
}
