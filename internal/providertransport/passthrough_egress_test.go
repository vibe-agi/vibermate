package providertransport

import (
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"testing"
)

func testFlowResolved(t testing.TB, profile egressprofile.ProfileRevision) egressprofile.Resolved {
	t.Helper()
	resolved, err := egressprofile.RestoreResolved(profile, egressprofile.SelectionFlow, 0)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestModelPassthroughKeepsFrozenGlobalProvenanceWithoutAccount(t *testing.T) {
	options := validRequestOptions(t)
	original := testOriginalRequestPlan(t)
	target, err := NewTarget(original.providerOrigin)
	if err != nil {
		t.Fatal(err)
	}
	options.Target, options.Provenance, options.WireProfile = target, original.provenance, original.wireProfile
	options.CredentialMode, options.PassthroughOrigin = providerauth.CredentialClientPassthrough, original.providerOrigin
	options.AccountRef, options.SecretRef, options.AuthDriverRef = providerauth.AccountRef{}, secretstore.Reference{}, providerauth.DriverRef{}
	options.ResolvedEgress, err = egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionGlobal, 8)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := NewRequest(options)
	if err != nil {
		t.Fatal(err)
	}
	actual := providerEgressDecision(frozen)
	if actual.SelectionSource != egressprofile.SelectionGlobal || actual.DefaultRevision != 8 || actual.AccountID != "" || actual.AccountSettingsRevision != 0 {
		t.Fatalf("model passthrough invented flow/account or lost actual global selection: %+v", actual)
	}
}
