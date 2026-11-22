package protocolcore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestProviderFailureKeepsOnlyClosedStructuralCodes(t *testing.T) {
	for _, test := range []struct{ raw, want string }{
		{`{"code":"context_length_exceeded","message":"private prompt","param":"secret"}`, "context_length_exceeded"},
		{`{"code":"invalid_encrypted_content","message":"private ciphertext"}`, "invalid_encrypted_content"},
		{`{"code":null,"type":"overloaded_error","message":"private account"}`, "overloaded_error"},
		{`{"code":"private","type":"private","message":"rate_limit_exceeded"}`, ""},
		{`{"code":123,"message":"private"}`, ""},
		{`null`, ""},
	} {
		failure := NewProviderFailure("$.error", []byte(test.raw))
		wrapped := fmt.Errorf("decode: %w", failure)
		if got := ProviderErrorCodeOf(wrapped); got != test.want || strings.Contains(failure.Error(), "private") || ReasonOf(wrapped) != ReasonProviderResponseFailed {
			t.Fatalf("code=%q reason=%s failure=%v", got, ReasonOf(wrapped), failure)
		}
	}
}

func TestNativeErrorDeliveryIsSeparateFromDiagnosticVocabulary(t *testing.T) {
	raw := []byte(`{"code":"future_native_code","message":"private provider context","details":{"retry_after":12}}`)
	failure := NewNativeProviderFailure("$.error", protocolspec.DialectOpenAIResponses, raw)
	body := []byte(`{"error":` + string(raw) + `,"request_id":"private-native-request-id"}`)
	wantBody := string(body)
	failure.NativeError = failure.NativeError.WithHTTPBody(body)
	body[0] = 'x'
	failure.NativeError = failure.NativeError.WithHeaders(http.Header{"X-Request-Id": {"private-request-id"}})
	if ProviderErrorCodeOf(failure) != "" {
		t.Fatal("unknown code entered diagnostics")
	}
	if got := failure.NativeError.ForDialect(protocolspec.DialectOpenAIResponses); string(got) != string(raw) {
		t.Fatalf("native error changed: %s", got)
	}
	if got := failure.NativeError.ForDialect(protocolspec.DialectAnthropicMessages); got != nil {
		t.Fatal("native error crossed dialects")
	}
	if got := failure.NativeError.HTTPBodyForDialect(protocolspec.DialectOpenAIResponses); string(got) != wantBody {
		t.Fatalf("native HTTP envelope changed: %s", got)
	} else {
		got[0] = 'x'
	}
	if got := failure.NativeError.HTTPBodyForDialect(protocolspec.DialectOpenAIResponses); string(got) != wantBody {
		t.Fatal("native HTTP envelope did not own its bytes")
	}
	if got := failure.NativeError.HTTPBodyForDialect(protocolspec.DialectAnthropicMessages); got != nil {
		t.Fatal("native HTTP envelope crossed dialects")
	}
	unrelated := NewNativeProviderError(protocolspec.DialectOpenAIResponses, raw).
		WithHTTPBody([]byte(`{"error":{"message":"different error"}}`))
	if len(unrelated.HTTPBodyForDialect(protocolspec.DialectOpenAIResponses)) != 0 {
		t.Fatal("native HTTP envelope accepted a different error payload")
	}
	encoded, err := json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{string(encoded), failure.Error(), fmt.Sprint(failure.NativeError), fmt.Sprintf("%#v", failure)} {
		if strings.Contains(value, "private") || strings.Contains(value, "future_native_code") {
			t.Fatalf("diagnostic leaked native payload: %s", value)
		}
	}
}

func TestProviderErrorValidation(t *testing.T) {
	t.Parallel()
	valid := []ProviderError{
		{Class: ProviderErrorContextWindow, Status: 400, Code: "context_length_exceeded", Param: "input", LimitTokens: 272000, ActualTokens: 300000},
		{Class: ProviderErrorReasoningState, Status: 400, Code: "invalid_encrypted_content", Param: "input[3].encrypted_content"},
		{Class: ProviderErrorRateLimited, Status: 429, Code: "rate_limit_exceeded", RetryAfter: 2 * time.Second},
		{Class: ProviderErrorNotFound, Status: 404, Code: "model_not_found"},
		{Class: ProviderErrorOverloaded, Status: 0, Code: "server_is_overloaded"},
		{Class: ProviderErrorUsageLimit, Status: 429, Code: "usage_limit_reached", ResetsAt: 1795000000},
		{Class: ProviderErrorEffort, Status: 400, Param: "reasoning.effort"},
		{Class: ProviderErrorServer, Status: 502},
	}
	for index, providerError := range valid {
		if err := providerError.Validate(); err != nil {
			t.Fatalf("valid provider error %d refused: %v", index, err)
		}
	}
	for name, providerError := range map[string]ProviderError{
		"unknown class":      {Class: "teapot"},
		"empty class":        {},
		"status too low":     {Class: ProviderErrorServer, Status: 99},
		"status too high":    {Class: ProviderErrorServer, Status: 600},
		"unknown code":       {Class: ProviderErrorInvalidRequest, Code: "Your prompt mentions private text"},
		"param with prose":   {Class: ProviderErrorInvalidRequest, Param: "the input field"},
		"param too long":     {Class: ProviderErrorInvalidRequest, Param: strings.Repeat("p", 129)},
		"negative retry":     {Class: ProviderErrorRateLimited, RetryAfter: -time.Second},
		"retry beyond a day": {Class: ProviderErrorRateLimited, RetryAfter: 25 * time.Hour},
		"negative tokens":    {Class: ProviderErrorContextWindow, LimitTokens: -1},
		"negative reset":     {Class: ProviderErrorUsageLimit, ResetsAt: -1},
	} {
		if err := providerError.Validate(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// The classified vocabulary widens the diagnostic one without changing it:
// KnownProviderErrorCode also reaches client-visible error envelopes.
func TestClassifiedErrorCodesExtendOnlyTheirOwnVocabulary(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"model_not_found", "invalid_api_key", "server_is_overloaded", "slow_down",
		"policy_capacity_unavailable", "usage_not_included", "content_policy_violation", "cyber_policy", "credit_balance_exhausted"} {
		if KnownClassifiedErrorCode(code) != code {
			t.Fatalf("KnownClassifiedErrorCode(%q) refused it", code)
		}
		if KnownProviderErrorCode(code) != "" {
			t.Fatalf("KnownProviderErrorCode(%q) widened", code)
		}
	}
	for _, code := range []string{"context_length_exceeded", "invalid_encrypted_content", "insufficient_quota", "invalid_image", "rate_limit_exceeded", "usage_limit_reached"} {
		if KnownClassifiedErrorCode(code) != code || KnownProviderErrorCode(code) != code {
			t.Fatalf("existing code %q is not in both vocabularies", code)
		}
	}
	if KnownClassifiedErrorCode("arbitrary provider text") != "" {
		t.Fatal("classified vocabulary is open")
	}
}

func TestNativeProviderErrorClientStatus(t *testing.T) {
	t.Parallel()
	native := NewNativeProviderError(protocolspec.DialectAnthropicMessages, []byte(`{"type":"invalid_request_error","message":"capability_rejected: thinking_signature"}`))
	if native.ClientStatus(protocolspec.DialectAnthropicMessages) != 0 {
		t.Fatal("a native error without a chosen status reported one")
	}
	chosen := native.WithClientStatus(http.StatusBadRequest)
	if chosen.ClientStatus(protocolspec.DialectAnthropicMessages) != http.StatusBadRequest {
		t.Fatal("chosen client status lost")
	}
	if chosen.ClientStatus(protocolspec.DialectOpenAIResponses) != 0 {
		t.Fatal("client status leaked to another dialect")
	}
	if native.ClientStatus(protocolspec.DialectAnthropicMessages) != 0 {
		t.Fatal("WithClientStatus mutated its receiver")
	}
	for _, status := range []int{0, 200, 399, 600} {
		if chosen.WithClientStatus(status).ClientStatus(protocolspec.DialectAnthropicMessages) != http.StatusBadRequest {
			t.Fatalf("status %d replaced the chosen one", status)
		}
	}
	if (NativeProviderError{}).WithClientStatus(400).ClientStatus("") != 0 {
		t.Fatal("an empty native error accepted a client status")
	}
}

func TestFailureProviderClassIsNotSerialized(t *testing.T) {
	t.Parallel()
	failure := NewFailure(ReasonProviderResponseFailed, "$", nil)
	failure.Provider = &ProviderError{Class: ProviderErrorRateLimited, Status: 429}
	encoded, err := json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "rate_limited") || strings.Contains(string(encoded), `"Provider"`) || strings.Contains(string(encoded), "429") {
		t.Fatalf("provider class serialized: %s", encoded)
	}
}
