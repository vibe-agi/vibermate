package loopbackproxy_test

import (
	"context"
	"testing"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
)

// The design grades ingress attribution as verified, configured, or unknown.
// A CaptureRun whose compound release was digest-verified is the strongest
// evidence the proxy can obtain, so it must not be reported as merely
// configured; leaving `verified` unreachable would make the grade meaningless.
func TestConnectionSourceConfidenceDerivesFromAdapterEvidence(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name     string
		fixture  func(*testing.T) *proxyFixture
		expected connectionevent.SourceConfidence
	}{
		{
			name:     "verified compound release",
			fixture:  newResponsesProxyFixture,
			expected: connectionevent.SourceConfidenceVerified,
		},
		{
			name:     "generic client",
			fixture:  newGenericResponsesProxyFixture,
			expected: connectionevent.SourceConfidenceConfigured,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixture := testCase.fixture(t)
			defer fixture.Close(t)

			secured := fixture.ConnectTLS(
				t,
				fixture.grant.ProxyCapability.Value(),
				"api.openai.com:443",
				"api.openai.com",
			)
			defer secured.Close()

			page, err := fixture.connections.List(
				context.Background(),
				connectionevent.PageRequest{Limit: 20},
			)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, record := range page.Items {
				if record.Phase != connectionevent.PhaseDecided &&
					record.Phase != connectionevent.PhaseConnected {
					continue
				}
				found = true
				if record.SourceConfidence != testCase.expected {
					t.Fatalf(
						"source confidence = %q, want %q",
						record.SourceConfidence,
						testCase.expected,
					)
				}
			}
			if !found {
				t.Fatal("no decided connection record was written")
			}
		})
	}
}
