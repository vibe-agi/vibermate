package providerauth

import "testing"

func TestDriverAndAccountIdentityAreTyped(t *testing.T) {
	t.Parallel()
	if StaticHeaderDriverRef().String() != StaticHeaderDriverValue ||
		AnthropicAPIKeyDriverRef().String() != AnthropicAPIKeyDriverValue {
		t.Fatal("built-in driver identity changed")
	}
	if _, err := NewDriverRef(" driver "); err == nil {
		t.Fatal("non-canonical driver identity was accepted")
	}
	if err := (AccountRef{ID: "account.work", Revision: 2, CredentialEpoch: 3, RealmID: "openai.platform"}).Validate(); err != nil {
		t.Fatal(err)
	}
}
