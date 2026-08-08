package loopbackproxy_test

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

const (
	canaryPrompt     = "CANARY-PROMPT-8f31c0a2-do-not-leave-this-process"
	canaryCredential = "Bearer CANARY-CREDENTIAL-4d7be115-do-not-leave-this-process"
)

func countTokensBody() string {
	return `{"model":"claude","messages":[{"role":"user","content":"` +
		canaryPrompt + `"}]}`
}

// anthropicErrorEnvelope is the shape a client that believes it is talking to
// api.anthropic.com will try to parse. A local policy rejection that returns a
// foreign envelope makes the client fail for the wrong reason, which would
// also make the fixed-client fixture measure the wrong thing.
type anthropicErrorEnvelope struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func assertNoClientPayloadEscaped(t *testing.T, fixture *proxyFixture) {
	t.Helper()

	if got := fixture.original.Count(); got != 0 {
		t.Fatalf("original-origin transport was invoked %d times", got)
	}
	if got := len(fixture.exchanges.Requests()); got != 0 {
		t.Fatalf("payload-bearing rejection created %d Exchanges", got)
	}
	recorded := fixture.original.Request()
	if bytes.Contains(recorded.Body(), []byte(canaryPrompt)) {
		t.Fatal("canary prompt reached the original-origin request")
	}
	if strings.Contains(
		recorded.Headers().Get("Authorization"),
		"CANARY-CREDENTIAL",
	) {
		t.Fatal("canary credential reached the original-origin request")
	}
}

func assertDialectRejection(t *testing.T, response *http.Response) []byte {
	t.Helper()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("rejection status = %d body=%s", response.StatusCode, body)
	}
	if got := response.Header.Get("X-Vibermate-Reason"); got !=
		"environment_operation_unsupported" {
		t.Fatalf("rejection reason header = %q", got)
	}
	var envelope anthropicErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("rejection body is not JSON: %v body=%s", err, body)
	}
	if envelope.Type != "error" || envelope.Error.Type == "" {
		t.Fatalf("rejection body is not an Anthropic error envelope: %s", body)
	}
	if bytes.Contains(body, []byte(canaryPrompt)) ||
		bytes.Contains(body, []byte("CANARY-CREDENTIAL")) {
		t.Fatalf("rejection body echoed client payload: %s", body)
	}
	return body
}

func postCountTokens(
	t *testing.T,
	secured *tls.Conn,
	target string,
) *http.Response {
	t.Helper()

	payload := countTokensBody()
	return writeInnerRequest(t, secured, &http.Request{
		Method: http.MethodPost,
		URL:    mustURL(t, target),
		Host:   "api.anthropic.com:443",
		Header: http.Header{
			"Content-Type":  []string{"application/json"},
			"Authorization": []string{canaryCredential},
		},
		Body:          io.NopCloser(strings.NewReader(payload)),
		ContentLength: int64(len(payload)),
	})
}

func TestPayloadBearingAuxiliaryIsRejectedLocally(t *testing.T) {
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

	assertDialectRejection(
		t,
		postCountTokens(t, secured, "/v1/messages/count_tokens"),
	)
	assertNoClientPayloadEscaped(t, fixture)
}

func TestPayloadBearingAuxiliaryBetaVariantIsRejectedLocally(t *testing.T) {
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

	assertDialectRejection(
		t,
		postCountTokens(t, secured, "/v1/messages/count_tokens?beta=true"),
	)
	assertNoClientPayloadEscaped(t, fixture)
}

// An uncatalogued body-bearing request has no proven payload class, so it may
// not be forwarded with the client's own credentials either.
func TestUncataloguedBodyBearingRequestIsRejectedLocally(t *testing.T) {
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

	response := postCountTokens(t, secured, "/api/claude_code/unknown_write")
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity ||
		!bytes.Contains(body, []byte(`"reasonCode":"path_unsupported"`)) {
		t.Fatalf("uncatalogued body status=%d body=%s", response.StatusCode, body)
	}
	assertNoClientPayloadEscaped(t, fixture)
}

// A catalogued control probe is a proven no-payload operation, so it keeps the
// client's own credentials on the way back to the inbound origin.
func TestCataloguedControlProbeReachesTheOriginalOrigin(t *testing.T) {
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
		Header: http.Header{"Authorization": []string{"Bearer client-owned"}},
	})
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted || string(body) != "original" {
		t.Fatalf("control-plane GET status=%d body=%q", response.StatusCode, body)
	}
	if got := fixture.original.Count(); got != 1 {
		t.Fatalf("control-plane GET reached original transport %d times", got)
	}
	frozen := fixture.original.Request()
	if got := frozen.PayloadClass(); got != protocolspec.OperationPayloadNone {
		t.Fatalf("frozen original request payload class = %q", got)
	}
	if len(frozen.Body()) != 0 {
		t.Fatal("a no-payload probe carried a body")
	}
}

// An uncatalogued bodyless GET still has no typed operation contract. Body
// absence does not widen the exact Environment allowlist.
func TestUncataloguedBodylessGETIsRejectedLocally(t *testing.T) {
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
		Header: http.Header{"Authorization": []string{"Bearer client-owned"}},
	})
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity ||
		!bytes.Contains(body, []byte(`"reasonCode":"path_unsupported"`)) {
		t.Fatalf("uncatalogued GET status=%d body=%q", response.StatusCode, body)
	}
	if fixture.original.Count() != 0 {
		t.Fatal("uncatalogued GET reached the original origin")
	}
}

// A rejection must leave the connection usable so a client that continues is
// not forced into a reconnect it did not ask for.
func TestConnectionStaysUsableAfterAPayloadBearingRejection(t *testing.T) {
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

	assertDialectRejection(
		t,
		postCountTokens(t, secured, "/v1/messages/count_tokens"),
	)

	const followUp = `{"model":"client"}`
	response := writeInnerRequest(t, secured, &http.Request{
		Method:        http.MethodPost,
		URL:           mustURL(t, "/v1/messages"),
		Host:          "api.anthropic.com:443",
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(followUp)),
		ContentLength: int64(len(followUp)),
	})
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK ||
		string(body) != `{"result":"proxied"}` {
		t.Fatalf(
			"follow-up semantic status=%d body=%q",
			response.StatusCode,
			body,
		)
	}
	requests := fixture.exchanges.Requests()
	if len(requests) != 1 || string(requests[0].Body()) != followUp {
		t.Fatalf("follow-up Exchange requests = %+v", requests)
	}
	for _, request := range requests {
		if bytes.Contains(request.Body(), []byte(canaryPrompt)) {
			t.Fatal("canary prompt entered an Exchange")
		}
	}
}
