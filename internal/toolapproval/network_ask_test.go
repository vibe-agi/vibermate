package toolapproval

import "testing"

// Design 06 keys aggregation on the kind, the ingress, the host, and the port,
// so a burst of connections to one host is one question answered once for all
// of them.
func TestNetworkAskAggregatesOnIngressHostAndPort(t *testing.T) {
	t.Parallel()

	base := NetworkAskRequest{
		IngressID: "run-1",
		Host:      "files.example",
		Port:      443,
	}
	same := networkAskAggregateKey(base)
	if same != networkAskAggregateKey(base) {
		t.Fatal("the same question produced two keys")
	}

	for _, different := range []NetworkAskRequest{
		{IngressID: "run-2", Host: "files.example", Port: 443},
		{IngressID: "run-1", Host: "other.example", Port: 443},
		{IngressID: "run-1", Host: "files.example", Port: 8443},
	} {
		if networkAskAggregateKey(different) == same {
			t.Fatalf("a different question shares a key: %+v", different)
		}
	}
}

// A host that merely contains another must not collide with it.
func TestNetworkAskKeysAreUnambiguous(t *testing.T) {
	t.Parallel()

	first := networkAskAggregateKey(NetworkAskRequest{
		IngressID: "run",
		Host:      "a.example",
		Port:      443,
	})
	second := networkAskAggregateKey(NetworkAskRequest{
		IngressID: "run-a",
		Host:      ".example",
		Port:      443,
	})
	if first == second {
		t.Fatal("adjacent fields ran together into the same key")
	}
}

func TestNetworkAskRequestIsValidated(t *testing.T) {
	t.Parallel()

	for _, invalid := range []NetworkAskRequest{
		{Host: "files.example", Port: 443},
		{IngressID: "run-1", Port: 443},
		{IngressID: "run-1", Host: "files.example"},
		{IngressID: "run-1", Host: "Files.Example", Port: 443},
	} {
		if err := invalid.validate(); err == nil {
			t.Fatalf("an invalid network ask was accepted: %+v", invalid)
		}
	}
	valid := NetworkAskRequest{
		IngressID: "run-1",
		Host:      "files.example",
		Port:      443,
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("a valid network ask was rejected: %v", err)
	}
}
