package protocolcore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

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
