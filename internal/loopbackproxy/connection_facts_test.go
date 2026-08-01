package loopbackproxy_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
)

// ADR-0015 section 7 narrows ConnectionEvent to one client-side connection.
// A persistent connection carrying several requests must keep one stable
// record; request-level destinations belong to EgressAttempt, which is where a
// reader can see every one of them rather than only the last.
func TestConnectionRecordStaysStableAcrossSeveralRequests(t *testing.T) {
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

	// An opaque control probe followed by a semantic model request: two
	// different destinations on one connection.
	probe := writeInnerRequest(t, secured, &http.Request{
		Method: http.MethodGet,
		URL:    mustURL(t, "/api/claude_code/settings"),
		Host:   "api.anthropic.com:443",
		Header: make(http.Header),
	})
	_, _ = io.ReadAll(probe.Body)
	_ = probe.Body.Close()

	const semantic = `{"model":"client"}`
	model := writeInnerRequest(t, secured, &http.Request{
		Method:        http.MethodPost,
		URL:           mustURL(t, "/v1/messages"),
		Host:          "api.anthropic.com:443",
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(semantic)),
		ContentLength: int64(len(semantic)),
	})
	_, _ = io.ReadAll(model.Body)
	_ = model.Body.Close()

	page, err := fixture.connections.List(
		context.Background(),
		connectionevent.PageRequest{Limit: 50},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range page.Items {
		if record.RequestedHost != "api.anthropic.com" ||
			record.Port != 443 {
			t.Fatalf("connection record lost its client authority: %+v", record)
		}
		if record.RouteHost != "" &&
			record.RouteHost != "api.anthropic.com" {
			t.Fatalf(
				"a request-level destination was written onto the connection: %+v",
				record,
			)
		}
		if record.CredentialBindingID != "" {
			t.Fatalf(
				"a request-level credential was written onto the connection: %+v",
				record,
			)
		}
	}
}
