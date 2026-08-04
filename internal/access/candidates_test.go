package access_test

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
)

// A plan that names one upstream compiles one candidate, and it is the one
// every existing accessor already returns.
func TestASingleCandidatePlanCompilesOne(t *testing.T) {
	t.Parallel()

	compiler := testCompiler(t)
	accessID := newAccessID(t, "access-candidates")
	snapshot, err := compiler.Compile(testAggregate(t, accessID, 1, "Candidates"))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CandidateCount() != 1 {
		t.Fatalf("candidates = %d", snapshot.CandidateCount())
	}
	candidate, found := snapshot.Candidate(0)
	if !found {
		t.Fatal("the first candidate is missing")
	}
	if candidate.Target().HTTPAuthority() !=
		snapshot.ProviderTargets()[0].HTTPAuthority() {
		t.Fatalf("candidate target = %+v", candidate.Target())
	}
	if candidate.CodecPlan().ID() != snapshot.CodecPlan().ID() {
		t.Fatal("the first candidate does not carry the plan's codec")
	}
	if _, beyond := snapshot.Candidate(1); beyond {
		t.Fatal("a one-candidate plan answered for a second")
	}
}

func TestDisabledAccountMayBeStagedButCannotEnterRouteSet(t *testing.T) {
	t.Parallel()

	options := testCatalogOptions(t)
	options.Capabilities.MaxEndpointProfiles = 2
	options.Capabilities.MaxAccountBindings = 2
	options.Capabilities.AllowMultipleRouteCandidates = true
	catalog, err := access.NewCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := access.NewCompiler(catalog)
	if err != nil {
		t.Fatal(err)
	}
	aggregate := testAggregate(
		t,
		newAccessID(t, "access-staged-candidate"),
		1,
		"Staged candidate",
	)
	profileID := mustEndpointProfileID(t, "access-staged-candidate-profile-2")
	targetID := mustProviderTargetID(t, "access-staged-candidate-target-2")
	accountID := mustAccountBindingID(t, "access-staged-candidate-account-2")
	secretRef, err := access.NewSecretRef("secret://provider/access-staged-candidate-2")
	if err != nil {
		t.Fatal(err)
	}
	profile := aggregate.Profiles[0]
	profile.ID = profileID
	profile.Name = "Staged"
	profile.TargetID = targetID
	profile.AccountBindingIDs = []access.AccountBindingID{accountID}
	profile.DefaultAccountBindingID = accountID
	target := aggregate.ProviderTargets[0]
	target.ID = targetID
	target.ProfileID = profileID
	account := aggregate.AccountBindings[0]
	account.ID = accountID
	account.ProfileID = profileID
	account.Label = "Staged"
	account.SecretRef = secretRef
	account.Enabled = false
	aggregate.Binding.ProfileIDs = append(aggregate.Binding.ProfileIDs, profileID)
	aggregate.Profiles = append(aggregate.Profiles, profile)
	aggregate.ProviderTargets = append(aggregate.ProviderTargets, target)
	aggregate.AccountBindings = append(aggregate.AccountBindings, account)

	staged, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatalf("compile staged candidate: %v", err)
	}
	if staged.CandidateCount() != 1 {
		t.Fatalf("staged candidate count = %d", staged.CandidateCount())
	}

	routedDisabled := aggregate.Clone()
	routedDisabled.RouteSets[0].CandidateProfileIDs = append(
		routedDisabled.RouteSets[0].CandidateProfileIDs,
		profileID,
	)
	if _, err := compiler.Compile(routedDisabled); err == nil ||
		!strings.Contains(err.Error(), "disabled default account") {
		t.Fatalf("compile routed disabled candidate error = %v", err)
	}

	routedEnabled := routedDisabled.Clone()
	routedEnabled.AccountBindings[1].Enabled = true
	active, err := compiler.Compile(routedEnabled)
	if err != nil {
		t.Fatalf("compile routed enabled candidate: %v", err)
	}
	if active.CandidateCount() != 2 {
		t.Fatalf("active candidate count = %d", active.CandidateCount())
	}
}
