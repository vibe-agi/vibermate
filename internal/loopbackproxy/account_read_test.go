package loopbackproxy_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestAccountQueryPreservesExplicitOriginalDestination(t *testing.T) {
	fixture := newProxyFixtureForDialectWithPolicyAndRawEvidence(t, protocolspec.DialectOpenAIResponses, nil,
		allowEverythingTestPolicy(t), nil, environment.SystemTransparentID)
	defer fixture.Close(t)
	secured := fixture.ConnectTLS(t, fixture.grant.ProxyCapability.Value(), "chatgpt.com:443", "chatgpt.com")
	defer secured.Close()
	response := writeInnerRequest(t, secured, &http.Request{Method: "GET", URL: mustURL(t, "/backend-api/wham/usage"),
		Host: "chatgpt.com:443", Header: http.Header{"Authorization": {"Bearer original-A"}}})
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted || fixture.original.Count() != 1 ||
		fixture.original.Request().Headers().Get("Authorization") != "Bearer original-A" {
		t.Fatal("explicit Original Destination did not preserve its account query and credential")
	}
	if len(fixture.exchanges.Requests()) != 0 {
		t.Fatal("account query became a model Exchange")
	}
}
