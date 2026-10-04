package providertransport

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestRequestProducesOnlyExplicitCleartextHTTP1Adjustment(t *testing.T) {
	for _, testCase := range []struct {
		name, origin string
		client, want wireprofile.ApplicationProtocol
		adjustment   string
	}{
		{"loopback h2", "http://127.0.0.1:12345", wireprofile.ApplicationProtocolHTTP2, wireprofile.ApplicationProtocolHTTP1, "cleartext_http1"},
		{"private h2", "http://192.168.1.5:12345", wireprofile.ApplicationProtocolHTTP2, wireprofile.ApplicationProtocolHTTP1, "cleartext_http1"},
		{"loopback h1", "http://127.0.0.1:12345", wireprofile.ApplicationProtocolHTTP1, wireprofile.ApplicationProtocolHTTP1, ""},
		{"TLS h2", "https://provider.example", wireprofile.ApplicationProtocolHTTP2, wireprofile.ApplicationProtocolHTTP2, ""},
		{"loopback TLS h2", "https://127.0.0.1:12345", wireprofile.ApplicationProtocolHTTP2, wireprofile.ApplicationProtocolHTTP2, ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			options := validRequestOptions(t)
			origin, err := originidentity.ParseProviderOrigin(testCase.origin)
			if err != nil {
				t.Fatal(err)
			}
			options.Target, err = NewTarget(origin)
			if err != nil {
				t.Fatal(err)
			}
			options.ClientProtocol = testCase.client
			request, err := NewRequest(options)
			if err != nil {
				t.Fatal(err)
			}
			actual := request.WirePresentationEvidence()
			if actual.ClientProtocol != testCase.client || actual.UpstreamProtocol != testCase.want || actual.ProtocolAdjustment != testCase.adjustment {
				t.Fatalf("presentation=%+v", actual)
			}
			preselection := NewWirePresentationEvidence(options.WireProfile, testCase.client)
			if preselection.ProtocolAdjustment != "" || preselection.UpstreamProtocol != testCase.client {
				t.Fatal("preselection invented target-specific adjustment")
			}
		})
	}
}
