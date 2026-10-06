package environment

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestResolvedPlanPreservesAuthoredScopeAndRejectsDifferentSelection(t *testing.T) {
	value := clientToolFixture(t)
	value.ClientEndpoints[0].ProtocolPlans[0].EgressProfile = egressprofile.Inherit()
	value.ClientToolAccess.Route.EgressProfile = egressprofile.Inherit()
	snapshot := mustCompile(t, value)
	model, err := snapshot.ResolveRequest(mustOrigin(t, "https://chatgpt.com"), RequestFacts{
		Target:             protocolspec.RequestTarget{Method: "POST", Path: "/v1/messages", Transport: protocolspec.ClientOperationTransportHTTP},
		DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := snapshot.BeginProviderConnection(value.ClientToolAccess.Route.TargetOrigin)
	if err != nil {
		t.Fatal(err)
	}
	request, err := snapshot.ResolveClientToolRequest(binding, RequestFacts{Target: protocolspec.RequestTarget{Method: "POST", Path: ClientToolMCPPath, Transport: protocolspec.ClientOperationTransportHTTP}})
	if err != nil {
		t.Fatal(err)
	}
	native, ok := request.ClientToolPlan()
	if !ok {
		t.Fatal("missing native arm")
	}
	selected, err := egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionBuiltin, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = model.ResolvedEgress(); err == nil {
		t.Fatal("unfrozen model plan executable")
	}
	if _, err = native.ResolvedEgress(); err == nil {
		t.Fatal("unfrozen native plan executable")
	}
	frozen, err := model.WithResolvedEgress(selected)
	if err != nil {
		t.Fatalf("freeze legitimate model selection: %v", err)
	}
	nativeFrozen, err := native.WithResolvedEgress(selected)
	if err != nil {
		t.Fatalf("freeze legitimate native selection: %v", err)
	}
	if got, err := frozen.ResolvedEgress(); err != nil || got != selected || frozen.EnvironmentDigest() != snapshot.Digest() || frozen.EgressChoice() != egressprofile.Inherit() {
		t.Fatal("model authored/effective conflated")
	}
	if got, err := nativeFrozen.ResolvedEgress(); err != nil || got != selected || nativeFrozen.EnvironmentDigest() != snapshot.Digest() || nativeFrozen.Route() != native.Route() {
		t.Fatal("native authored/effective conflated")
	}
	foreign, _ := egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionAccount, 0)
	if _, err := native.WithResolvedEgress(foreign); err == nil {
		t.Fatal("native borrowed account exit")
	}
	if _, err := model.WithResolvedEgress(foreign); err == nil {
		t.Fatal("original plan borrowed account exit")
	}
	if _, err := nativeFrozen.WithResolvedEgress(foreign); err == nil {
		t.Fatal("frozen native selection replaced")
	}
}

func explicitEgress(t *testing.T, profile egressprofile.ProfileRevision) egressprofile.Choice {
	t.Helper()
	choice, err := egressprofile.Explicit(profile)
	if err != nil {
		t.Fatal(err)
	}
	return choice
}

func TestSystemTransparentCurrentInheritsAndHistoricalAssignmentStaysExact(t *testing.T) {
	manager := mustManager(t, newMemoryRepository(), nil)
	current, err := manager.Get(context.Background(), SystemTransparentID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision() != 2 {
		t.Fatalf("current builtin revision=%d, want new inherit revision 2", current.Revision())
	}
	for _, endpoint := range current.Aggregate().ClientEndpoints {
		for _, plan := range endpoint.ProtocolPlans {
			if plan.EgressProfile != egressprofile.Inherit() {
				t.Fatal("current builtin remains explicit Direct")
			}
		}
	}
	old, err := manager.ResolveRevision(context.Background(), SystemTransparentID, 1)
	if err != nil {
		t.Fatalf("old assignment unavailable: %v", err)
	}
	if old.Digest().String() != "e67a3f983b3521732f79fdb6de38415b67d5091ac5a943c5a7cca0af7b5e6e6b" {
		t.Fatal("old builtin relabeled")
	}
	if _, err = manager.GetRevision(context.Background(), SystemTransparentID, 3); err == nil {
		t.Fatal("unknown builtin revision accepted")
	}
	if _, err = testCompiler(t, nil).Restore(old.Aggregate()); err == nil {
		t.Fatal("system configuration entered ordinary Restore")
	}
}

func mutateEgress(t *testing.T, choice egressprofile.Choice, mutate func(*egressprofile.ProfileRevision)) egressprofile.Choice {
	t.Helper()
	profile, ok := choice.ExplicitProfile()
	if !ok {
		t.Fatal("expected explicit fixture")
	}
	// Drift is authored on a normal published profile, not a counterfeit builtin.
	if profile.ID == egressprofile.DirectID {
		profile.ID = "profile.synthetic"
	}
	mutate(&profile)
	return explicitEgress(t, profile)
}

// Fixed938 bytes captured before the representation change: this catches
// rewriting published explicit choices or reusing their digest for inheritance.
func TestLegacyEgressCanonicalFixtures(t *testing.T) {
	for _, tc := range []struct{ kind, digest string }{
		{"direct", "b4a3fa886a0cc26f3fe23f90ce3399a026529078622824f01ca58b99d59265fd"},
		{"proxy", "08df4f99e6e16aa7c8fdecd1e2e484b1b6d7036bc7003f7998c7430a5b864eec"},
		{"system", "e67a3f983b3521732f79fdb6de38415b67d5091ac5a943c5a7cca0af7b5e6e6b"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/egress_legacy_938_" + tc.kind + ".json")
			if err != nil {
				t.Fatal(err)
			}
			want := bytes.TrimSuffix(raw, []byte("\n"))
			var snapshot EnvironmentSnapshot
			if tc.kind == "system" {
				snapshot, err = testCompiler(t, nil).CompileSystemTransparentRevision(1)
			} else {
				var candidate Environment
				candidate, err = DecodeCanonicalJSON(want)
				if err == nil {
					snapshot, err = testCompiler(t, nil).Restore(candidate)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []byte
			if tc.kind == "system" {
				got, err = canonicalSystemJSON(snapshot.Aggregate())
			} else {
				got, err = CanonicalJSON(snapshot.Aggregate())
			}
			if err != nil || !bytes.Equal(got, want) || snapshot.Digest().String() != tc.digest {
				t.Fatalf("published %s bytes/digest changed: digest=%s err=%v", tc.kind, snapshot.Digest(), err)
			}
		})
	}
}

// Catches rejecting legitimate inheritance, or rewriting it into Direct
// during authored compilation before the actual operation is classified.
func TestAuthoredInheritanceCompilesWithoutResolvingOrRewriting(t *testing.T) {
	candidate := clientToolFixture(t)
	legacy, err := CanonicalJSON(candidate)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := json.Marshal(egressprofile.Direct())
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.ReplaceAll(legacy, append([]byte(`"egressProfile":`), direct...), []byte(`"egressProfile":{"mode":"inherit"}`))
	if bytes.Equal(want, legacy) {
		t.Fatal("fixture did not replace authored exits")
	}
	decoded, err := DecodeCanonicalJSON(want)
	if err != nil {
		t.Fatalf("valid authored inheritance rejected before runtime selection: %v", err)
	}
	snapshot, err := testCompiler(t, accountCatalogFor(decoded)).Compile(decoded)
	if err != nil {
		t.Fatalf("compile inherited choices: %v", err)
	}
	got, err := CanonicalJSON(snapshot.Aggregate())
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("authored inheritance rewritten: %v", err)
	}
	oldDigest, err := Digest(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Digest() == oldDigest {
		t.Fatal("new authored choice reused old digest")
	}
}
