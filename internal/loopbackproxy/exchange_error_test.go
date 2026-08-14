package loopbackproxy_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/exchange"
)

func TestManagedCredentialFailureUsesTerminalAnthropicEnvelope(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixture(t)
	defer fixture.Close(t)
	assertManagedCredentialFailure(t, fixture, "api.anthropic.com:443", "/v1/messages")
}

func TestManagedCredentialFailureUsesTerminalResponsesEnvelope(t *testing.T) {
	t.Parallel()

	fixture := newResponsesProxyFixture(t)
	defer fixture.Close(t)
	assertManagedCredentialFailure(t, fixture, "api.openai.com:443", "/v1/responses")
}

func TestProviderStatusRejectionPreservesAnthropicRetryStatus(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixture(t)
	defer fixture.Close(t)
	fixture.exchanges.FailWith(&exchange.Failure{
		Code:           exchange.ReasonProviderStatusRejected,
		ExchangeID:     "fixture-exchange",
		ProviderStatus: 529,
	})
	secured := fixture.ConnectTLS(
		t,
		fixture.grant.ProxyCapability.Value(),
		"api.anthropic.com:443",
		"api.anthropic.com",
	)
	defer secured.Close()

	payload := `{"model":"client","messages":[]}`
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
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	if response.StatusCode != 529 ||
		response.Header.Get("X-Vibermate-Reason") !=
			string(exchange.ReasonProviderStatusRejected) ||
		!bytes.Contains(body, []byte(`"type":"api_error"`)) ||
		!bytes.Contains(body, []byte(exchange.ReasonProviderStatusRejected)) {
		t.Fatalf(
			"provider rejection response status=%d headers=%v body=%s",
			response.StatusCode,
			response.Header,
			body,
		)
	}
}

func assertManagedCredentialFailure(
	t *testing.T,
	fixture *proxyFixture,
	authority string,
	path string,
) {
	t.Helper()

	const privateCause = "PRIVATE-CAUSE-MUST-NOT-CROSS-THE-CLIENT-BOUNDARY"
	fixture.exchanges.FailWith(errors.Join(
		&exchange.Failure{
			Code:       exchange.ReasonProviderCredentialUnavailable,
			ExchangeID: "fixture-exchange",
		},
		errors.New(privateCause),
	))
	host := strings.TrimSuffix(authority, ":443")
	secured := fixture.ConnectTLS(
		t,
		fixture.grant.ProxyCapability.Value(),
		authority,
		host,
	)
	defer secured.Close()

	payload := `{"model":"client","messages":[]}`
	response := writeInnerRequest(t, secured, &http.Request{
		Method: http.MethodPost,
		URL:    mustURL(t, path),
		Host:   authority,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body:          io.NopCloser(strings.NewReader(payload)),
		ContentLength: int64(len(payload)),
	})
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	if response.StatusCode != http.StatusUnauthorized ||
		response.Header.Get("X-Vibermate-Reason") !=
			string(exchange.ReasonProviderCredentialUnavailable) ||
		response.Header.Get("X-Should-Retry") != "false" ||
		!bytes.Contains(
			body,
			[]byte(exchange.ReasonProviderCredentialUnavailable),
		) ||
		!bytes.Contains(body, []byte(`"type":"authentication_error"`)) {
		t.Fatalf(
			"managed credential response status=%d headers=%v body=%s",
			response.StatusCode,
			response.Header,
			body,
		)
	}
	if bytes.Contains(body, []byte(privateCause)) ||
		bytes.Contains(body, []byte("fixture-exchange")) {
		t.Fatalf("managed credential response exposed private evidence: %s", body)
	}
	if requests := fixture.exchanges.Requests(); len(requests) != 1 {
		t.Fatalf("managed credential request count=%d, want=1", len(requests))
	}
}
