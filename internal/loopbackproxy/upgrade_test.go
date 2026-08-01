package loopbackproxy_test

import (
	"io"
	"net/http"
	"testing"
)

// A client that sends an upgrade believes it negotiated a protocol. Answering
// it as an ordinary request means the proxy speaks HTTP at something expecting
// frames, and the client discovers that only after it starts sending them.
func TestUnservableUpgradeIsRefusedRatherThanDegraded(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixture(t)
	defer fixture.Close(t)

	secured := fixture.ConnectTLS(
		t,
		fixture.grant.ProxyCapability.Value(),
		"api.anthropic.com:443",
		"api.anthropic.com",
	)
	defer secured.Close()

	response := writeInnerRequest(t, secured, &http.Request{
		Method: http.MethodGet,
		URL:    mustURL(t, "/api/claude_code/not_catalogued"),
		Host:   "api.anthropic.com:443",
		Header: http.Header{
			"Upgrade":               []string{"websocket"},
			"Connection":            []string{"Upgrade"},
			"Sec-WebSocket-Version": []string{"13"},
		},
	})
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode == http.StatusAccepted {
		t.Fatalf(
			"an upgrade was answered as an ordinary request: status=%d body=%s",
			response.StatusCode,
			body,
		)
	}
	if got := fixture.original.Count(); got != 0 {
		t.Fatalf("an unservable upgrade reached the original origin %d times", got)
	}
}

// A catalogued no-payload probe without an upgrade still works, so the refusal
// is scoped to the upgrade rather than to the path.
func TestCataloguedProbeWithoutAnUpgradeStillWorks(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixture(t)
	defer fixture.Close(t)

	secured := fixture.ConnectTLS(
		t,
		fixture.grant.ProxyCapability.Value(),
		"api.anthropic.com:443",
		"api.anthropic.com",
	)
	defer secured.Close()

	response := writeInnerRequest(t, secured, &http.Request{
		Method: http.MethodGet,
		URL:    mustURL(t, "/api/claude_code/settings"),
		Host:   "api.anthropic.com:443",
		Header: make(http.Header),
	})
	_, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("catalogued probe status = %d", response.StatusCode)
	}
}
