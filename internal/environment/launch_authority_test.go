package environment

import "testing"

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
