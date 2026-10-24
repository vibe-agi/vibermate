package runtimepersistence

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
)

func frozenExplicitNativePlan(t testing.TB, plan environment.ClientToolPlan) environment.ClientToolPlan {
	t.Helper()
	profile, ok := plan.Route().EgressProfile.ExplicitProfile()
	if !ok {
		t.Fatal("legacy fixture requires explicit authored flow")
	}
	resolved, err := egressprofile.RestoreResolved(profile, egressprofile.SelectionFlow, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = plan.WithResolvedEgress(resolved)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
