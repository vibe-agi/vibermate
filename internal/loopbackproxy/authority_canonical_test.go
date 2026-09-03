package loopbackproxy_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
)

// RFC 3986 makes a host case-insensitive and a trailing dot is the root form
// of the same name. Refusing a canonically equivalent authority looks to a
// user like the proxy is broken, and the design requires canonicalization
// rather than rejection.
func TestEquivalentAuthorityFormsMatchTheSameEndpoint(t *testing.T) {
	t.Parallel()

	for _, authority := range []string{
		"API.anthropic.com:443",
		"api.anthropic.com.:443",
		"Api.Anthropic.Com:443",
	} {
		t.Run(authority, func(t *testing.T) {
			t.Parallel()

			fixture := newProxyFixture(t)
			defer fixture.Close(t)

			secured := fixture.ConnectTLS(
				t,
				fixture.grant.ProxyCapability.Value(),
				authority,
				"api.anthropic.com",
			)
			defer secured.Close()

			const payload = `{"model":"client"}`
			response := writeInnerRequest(t, secured, &http.Request{
				Method: http.MethodPost,
				URL:    mustURL(t, "/v1/messages"),
				Host:   "api.anthropic.com:443",
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body:          io.NopCloser(strings.NewReader(payload)),
				ContentLength: int64(len(payload)),
			})
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf(
					"equivalent authority status=%d body=%s",
					response.StatusCode,
					body,
				)
			}
		})
	}
}

// Canonicalization must never widen the match. Two distinct hosts stay
// distinct, and a suffix or wildcard is still not a ClientEndpoint.
func TestCanonicalizationDoesNotWidenTheMatch(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixture(t)
	defer fixture.Close(t)

	for _, authority := range []string{
		"evil-api.anthropic.com:443",
		"api.anthropic.com.evil.test:443",
		"api.anthropic.com:8443",
	} {
		connection, response := fixture.Connect(
			t,
			fixture.grant.ProxyCapability.Value(),
			authority,
		)
		_ = response.Body.Close()
		_ = connection.Close()
	}
	page, err := fixture.connections.List(
		context.Background(),
		connectionevent.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	// A non-endpoint authority is forwarded blind. The invariant is that it is
	// never decrypted, not that it is refused.
	for _, record := range page.Items {
		if record.Decryption == connectionevent.DecryptionMITM {
			t.Fatalf("a non-endpoint authority was decrypted: %+v", record)
		}
	}
}
