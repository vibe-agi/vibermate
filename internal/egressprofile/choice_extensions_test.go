package egressprofile

import (
	"encoding/json"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"strings"
	"testing"
)

// Rejecting the candidate's optional fields breaks both authored flows and
// Store reloading of effective native bindings.
func TestChoiceExtendedPolicyRoundTrip(t *testing.T) {
	for _, proxy := range []egressnetwork.ProxyPolicy{
		{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:1080"},
		{Kind: egressnetwork.ProxyVLESS, CredentialRef: "secret://egress-vless/test", CredentialRevision: 1},
	} {
		t.Run(string(proxy.Kind), func(t *testing.T) {
			p := Direct()
			p.ID = "profile.extensions"
			p.Policy.Proxy = proxy
			p.Policy.DirectDomains = "example.com\n*.internal.example"
			var err error
			p.Policy, err = p.Policy.Normalize()
			if err != nil {
				t.Fatal(err)
			}
			c, err := Explicit(p)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			var got Choice
			if err = json.Unmarshal(data, &got); err != nil || got != c {
				t.Fatalf("extended roundtrip: %s: %v", data, err)
			}
			for _, field := range []string{"directDomains", "credentialRef", "credentialRevision"} {
				if !strings.Contains(string(data), `"`+field+`":`) {
					continue
				}
				values := map[string]any{"directDomains": p.Policy.DirectDomains, "credentialRef": proxy.CredentialRef, "credentialRevision": proxy.CredentialRevision}
				value, _ := json.Marshal(values[field])
				member := `"` + field + `":` + string(value)
				escaped := fmt.Sprintf(`"\u%04x%s":`, field[0], field[1:]) + string(value)
				for _, replacement := range []string{`"` + field + `":null`, `"` + strings.ToUpper(field[:1]) + field[1:] + `":` + string(value), member + "," + member, member + "," + escaped, `"unknownOptional":` + string(value)} {
					bad := strings.Replace(string(data), member, replacement, 1)
					if json.Unmarshal([]byte(bad), &got) == nil {
						t.Errorf("accepted malformed optional member: %s", bad)
					}
				}
			}
		})
	}
}
