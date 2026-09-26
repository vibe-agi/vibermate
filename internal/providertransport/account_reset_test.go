package providertransport

import "testing"

func TestOneBankedCreditKeepsItsBackendRequestIdentityAcrossRestarts(t *testing.T) {
	first, err := ResetRequestID("account-b", "credit-one")
	if err != nil || !resetRequestIDPattern.MatchString(first) {
		t.Fatalf("stable redemption UUID = %q, %v", first, err)
	}
	for range 3 {
		again, err := ResetRequestID("account-b", "credit-one")
		if err != nil || again != first {
			t.Fatal("one credit minted a different request ID")
		}
	}
	for _, pair := range [][2]string{{"account-a", "credit-one"}, {"account-b", "credit-two"}} {
		other, err := ResetRequestID(pair[0], pair[1])
		if err != nil || other == first {
			t.Fatal("different account or credit shared redemption ID")
		}
	}
	for _, credit := range []string{"", "bad\ncredit"} {
		if _, err := ResetRequestID("account-b", credit); err == nil {
			t.Fatal("invalid credit reached a backend request")
		}
	}
}
