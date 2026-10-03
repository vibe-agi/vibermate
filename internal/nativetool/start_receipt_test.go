package nativetool

import "testing"

// Catches a receipt accepted by a foreign or zero issuer or with substituted facts.
func TestNativeReceiptIssuerSealsEveryFact(t *testing.T) {
	issuer, err := NewStartReceiptIssuer()
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := NewStartReceiptIssuer()
	if err != nil {
		t.Fatal(err)
	}
	facts := StartFacts{OperationID: "11111111111111111111111111111111", AttemptID: "native-attempt", AuthorityRevision: 1, OwnerEpoch: 1, BindingDigest: Digest{1}}
	receipt, err := issuer.Issue(facts)
	if err != nil || issuer.Check(receipt, facts) != nil {
		t.Fatalf("issued receipt: %v", err)
	}
	if foreign.Check(receipt, facts) == nil || issuer.Check(StartReceipt{}, facts) == nil || (&StartReceiptIssuer{}).Check(receipt, facts) == nil {
		t.Fatal("foreign or zero receipt/issuer accepted")
	}
	variants := []StartFacts{facts, facts, facts, facts, facts}
	variants[0].OperationID = "22222222222222222222222222222222"
	variants[1].AttemptID = "other-attempt"
	variants[2].AuthorityRevision = 2
	variants[3].OwnerEpoch = 2
	variants[4].BindingDigest = Digest{2}
	for _, wrong := range variants {
		if issuer.Check(receipt, wrong) == nil {
			t.Fatalf("substituted facts accepted: %+v", wrong)
		}
	}
	for _, wrong := range []StartFacts{{}, {OperationID: facts.OperationID, AttemptID: facts.AttemptID, AuthorityRevision: 1, OwnerEpoch: 1}} {
		if _, err := issuer.Issue(wrong); err == nil {
			t.Fatal("invalid facts issued")
		}
	}
}
