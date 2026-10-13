package egressnetwork

import (
	"encoding/json"
	"testing"
)

// Rejecting the new public kind or discarding its routing rules breaks import
// persistence before any private credential can be used.
func TestVLESSPolicyRoundTrip(t *testing.T) {
	var policy Policy
	if err := json.Unmarshal([]byte(`{"proxy":{"kind":"vless","credentialRef":"secret://egress-vless/fixture","credentialRevision":1},"resolver":{"kind":"system","transport":"direct"},"directDomains":"*.Github.COM.\ngithub.com\ngithub.com"}`), &policy); err != nil {
		t.Fatal(err)
	}
	normalized, err := policy.Normalize()
	if err != nil {
		t.Fatalf("configured VLESS policy: %v", err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"proxy":{"kind":"vless","credentialRef":"secret://egress-vless/fixture","credentialRevision":1},"resolver":{"kind":"system","transport":"direct"},"directDomains":"*.github.com\ngithub.com"}`
	if string(encoded) != want {
		t.Fatalf("public policy roundtrip = %s", encoded)
	}
}
