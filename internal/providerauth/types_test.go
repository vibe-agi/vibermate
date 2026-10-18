package providerauth

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/cyberaccess"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
)

func TestDriverAndAccountIdentityAreTyped(t *testing.T) {
	t.Parallel()
	if StaticHeaderDriverRef().String() != StaticHeaderDriverValue ||
		AnthropicAPIKeyDriverRef().String() != AnthropicAPIKeyDriverValue {
		t.Fatal("built-in driver identity changed")
	}
	if _, err := NewDriverRef(" driver "); err == nil {
		t.Fatal("non-canonical driver identity was accepted")
	}
	account := AccountRef{ID: "account.work", Revision: 2, CredentialEpoch: 3, SettingsRevision: 1, EgressProfile: egressprofile.Direct(), RealmID: "openai.platform"}
	account.ResolvedEgress, _ = egressprofile.RestoreResolved(account.EgressProfile, egressprofile.SelectionAccount, 0)
	if err := account.Validate(); err != nil {
		t.Fatal(err)
	}
	missingRevision := account
	missingRevision.SettingsRevision = 0
	missingProfile := account
	missingProfile.EgressProfile = egressprofile.ProfileRevision{}
	missingResolved := account
	missingResolved.ResolvedEgress = egressprofile.Resolved{}
	mismatchedResolved := account
	mismatchedResolved.EgressProfile.DisplayName = "not the frozen effective profile"
	invalidPreference := account
	invalidPreference.CyberAccessProgram = cyberaccess.Preference("unknown")
	for _, incomplete := range []AccountRef{missingRevision, missingProfile, missingResolved, mismatchedResolved, invalidPreference} {
		if incomplete.Validate() == nil {
			t.Fatal("managed account without explicit settings was accepted")
		}
	}
}
