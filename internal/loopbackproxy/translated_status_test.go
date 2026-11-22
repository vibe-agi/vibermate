package loopbackproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// writeBothWays writes one Exchange failure through the plain writer and
// through the downstream boundary, which must agree.
func writeBothWays(t *testing.T, dialect protocolspec.Dialect, err error) []*httptest.ResponseRecorder {
	t.Helper()
	plain := httptest.NewRecorder()
	writeExchangeFailure(plain, dialect, err)
	downstream := httptest.NewRecorder()
	if writeErr := writeExchangeFailureDownstream(context.Background(), newHTTPDownstream(downstream, httpDownstreamOptions{}), dialect, err); writeErr != nil {
		t.Fatal(writeErr)
	}
	return []*httptest.ResponseRecorder{plain, downstream}
}

func renderedFailure(t *testing.T, reason exchange.ReasonCode, providerStatus int, class protocolcore.ProviderError) *exchange.Failure {
	t.Helper()
	request := protocolcore.Request{RequestedModel: "claude-opus-5-5"}
	native := anthropicchat.RenderProviderError(request, class)
	if native.ForDialect(protocolspec.DialectAnthropicMessages) == nil {
		t.Fatalf("class %+v rendered nothing", class)
	}
	return &exchange.Failure{
		Code: reason, ExchangeID: "exchange-fixture", ProviderStatus: providerStatus,
		NativeError: native, Provider: &class,
	}
}

// A composed path's client side names the status its rendering needs, and
// the loopback answers with it whatever the failure's reason would give:
// Claude Code recovers from a capability token only on 400 (design 8.3).
func TestRenderedClientStatusWinsForTheClientsDialect(t *testing.T) {
	for _, test := range []struct {
		name           string
		reason         exchange.ReasonCode
		providerStatus int
		class          protocolcore.ProviderError
		status         int
		message        string
		retryAfter     string
	}{
		{
			name: "in-band reasoning failure before the envelope", reason: exchange.ReasonProviderResponseFailed,
			class:  protocolcore.ProviderError{Class: protocolcore.ProviderErrorReasoningState, Code: "invalid_encrypted_content"},
			status: http.StatusBadRequest, message: "capability_rejected: thinking_signature",
		},
		{
			name: "provider admission refusal", reason: exchange.ReasonUnsupportedClientInput,
			class:  protocolcore.ProviderError{Class: protocolcore.ProviderErrorImage},
			status: http.StatusBadRequest, message: "capability_rejected: image_block",
		},
		{
			name: "503 overload", reason: exchange.ReasonProviderStatusRejected, providerStatus: 503,
			class:  protocolcore.ProviderError{Class: protocolcore.ProviderErrorOverloaded, Status: 503, Code: "server_is_overloaded"},
			status: 529, message: "The upstream service is overloaded.",
		},
		{
			name: "429 with retry-after", reason: exchange.ReasonProviderStatusRejected, providerStatus: 429,
			class:  protocolcore.ProviderError{Class: protocolcore.ProviderErrorRateLimited, Status: 429, RetryAfter: 7e9},
			status: 429, message: "The upstream service is rate limiting this account.", retryAfter: "7",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure := renderedFailure(t, test.reason, test.providerStatus, test.class)
			for _, writer := range writeBothWays(t, protocolspec.DialectAnthropicMessages, failure) {
				var body struct {
					Type  string
					Error struct{ Type, Message string }
				}
				if err := json.Unmarshal(writer.Body.Bytes(), &body); err != nil {
					t.Fatalf("body = %s: %v", writer.Body.String(), err)
				}
				if writer.Code != test.status || body.Type != "error" || body.Error.Message != test.message ||
					writer.Header().Get("Retry-After") != test.retryAfter ||
					writer.Header().Get(ReasonHeader) != string(test.reason) {
					t.Fatalf("status=%d headers=%v body=%s", writer.Code, writer.Header(), writer.Body.String())
				}
			}
		})
	}
}

// The policy row carries the Exchange ID as request_id; the loopback writes
// that envelope as it is.
func TestRenderedPolicyRefusalKeepsItsRequestID(t *testing.T) {
	failure := renderedFailure(t, exchange.ReasonProviderStatusRejected, 400,
		protocolcore.ProviderError{Class: protocolcore.ProviderErrorPolicy, Status: 400, Code: "cyber_policy"})
	payload := failure.NativeError.ForDialect(protocolspec.DialectAnthropicMessages)
	envelope := `{"type":"error","request_id":"exchange-fixture","error":` + string(payload) + `}`
	failure.NativeError = failure.NativeError.WithHTTPBody([]byte(envelope))
	for _, writer := range writeBothWays(t, protocolspec.DialectAnthropicMessages, failure) {
		if writer.Code != http.StatusBadRequest || strings.TrimSpace(writer.Body.String()) != envelope ||
			writer.Header().Get("X-Should-Retry") != "false" {
			t.Fatalf("status=%d headers=%v body=%s", writer.Code, writer.Header(), writer.Body.String())
		}
	}
}

// A native error of another dialect never chooses the status: a Responses
// client keeps the reason's status and the generic envelope, and an
// Anthropic failure without a rendering (the Anthropic to Chat path) keeps
// the provider's status and the generic envelope as before.
func TestClientStatusIsReadOnlyForTheClientsOwnDialect(t *testing.T) {
	failure := renderedFailure(t, exchange.ReasonProviderResponseFailed, 0,
		protocolcore.ProviderError{Class: protocolcore.ProviderErrorReasoningState})
	for _, writer := range writeBothWays(t, protocolspec.DialectOpenAIResponses, failure) {
		if writer.Code != http.StatusBadGateway || strings.Contains(writer.Body.String(), "capability_rejected") ||
			writer.Header().Get("X-Should-Retry") != "" {
			t.Fatalf("status=%d headers=%v body=%s", writer.Code, writer.Header(), writer.Body.String())
		}
	}

	chat := &exchange.Failure{
		Code: exchange.ReasonProviderStatusRejected, ExchangeID: "exchange-fixture", ProviderStatus: 400,
		ProviderErrorCode: "context_length_exceeded",
	}
	for _, writer := range writeBothWays(t, protocolspec.DialectAnthropicMessages, chat) {
		if writer.Code != http.StatusBadRequest ||
			!strings.Contains(writer.Body.String(), `Upstream error: context_length_exceeded.`) ||
			strings.Contains(writer.Body.String(), "capability_rejected") {
			t.Fatalf("status=%d body=%s", writer.Code, writer.Body.String())
		}
	}
	if got := exchangeStatus(protocolspec.DialectAnthropicMessages, chat); got != http.StatusBadRequest {
		t.Fatalf("status = %d", got)
	}
}
