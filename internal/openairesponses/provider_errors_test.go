package openairesponses_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// errorBody is an OpenAI error body. Its type is invalid_request_error, the
// code a class carries when the error's own code is unknown.
func errorBody(code, param, message string) []byte {
	member := func(name, value string) string {
		if value == "" {
			return `"` + name + `":null`
		}
		return `"` + name + `":"` + value + `"`
	}
	return []byte(`{"error":{` + member("message", message) + `,"type":"invalid_request_error",` +
		member("param", param) + `,` + member("code", code) + `}}`)
}

// limitedRequest sends max_output_tokens; streamRequest's 128000 does not.
func limitedRequest(t testing.TB) protocolcore.Request {
	t.Helper()
	return decodeClaude(t, []byte(`{"model":"claude-opus-5-5","max_tokens":1024,`+streamTools+`,"messages":[{"role":"user","content":"list files"}]}`))
}

func TestClassifierTable(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	open := streamRequest(t, "", false)
	estimate, err := side.CountInputTokens(protocolpath.ProviderContext{}, open)
	if err != nil || estimate <= 0 {
		t.Fatalf("estimate = %d, %v", estimate, err)
	}
	const overflow = "This model's maximum context length is 128000 tokens. However, you requested 130000 tokens (120000 in the messages, 10000 in the completion)."
	for _, test := range []struct {
		name    string
		request protocolcore.Request
		status  int
		body    []byte
		want    protocolcore.ProviderError
	}{
		{"input plus a sent max_output_tokens overflows", limitedRequest(t), 400, errorBody("context_length_exceeded", "input", overflow),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorMaxTokensOverflow, Status: 400, Code: "context_length_exceeded", Param: "input"}},
		{"the input alone overflows a stated window", open, 400, errorBody("context_length_exceeded", "input", overflow),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorContextWindow, Status: 400, Code: "context_length_exceeded", Param: "input",
				LimitTokens: 128000, ActualTokens: estimate}},
		{"the Responses wording has no numbers", limitedRequest(t), 400,
			errorBody("context_length_exceeded", "input", "Your input exceeds the context window of this model. Please adjust your input and try again."),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorContextWindow, Status: 400, Code: "context_length_exceeded", Param: "input"}},
		{"a message naming the context window", open, 400, errorBody("", "", "Input exceeds the context window of 32768 tokens"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorContextWindow, Status: 400, Code: "invalid_request_error", LimitTokens: 32768, ActualTokens: estimate}},
		{"413 naming the context window", open, 413, errorBody("", "", "Request exceeds the context window"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorContextWindow, Status: 413, Code: "invalid_request_error"}},
		{"invalid encrypted content", open, 400, errorBody("invalid_encrypted_content", "input[3].encrypted_content", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorReasoningState, Status: 400, Code: "invalid_encrypted_content", Param: "input[3].encrypted_content"}},
		{"an image code", open, 400, errorBody("invalid_image_format", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorImage, Status: 400, Code: "invalid_image_format"}},
		{"any code naming an image", open, 400, errorBody("invalid_base64_image", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorImage, Status: 400, Code: "invalid_base64_image"}},
		{"a param naming an image", open, 400, errorBody("", "input[1].content[0].image_url", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorImage, Status: 400, Code: "invalid_request_error", Param: "input[1].content[0].image_url"}},
		{"a code naming a file", open, 400, errorBody("unsupported_file", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorDocument, Status: 400, Code: "invalid_request_error"}},
		{"a param naming a file", open, 400, errorBody("", "input[2].output[1].file_data", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorDocument, Status: 400, Code: "invalid_request_error", Param: "input[2].output[1].file_data"}},
		{"effort", open, 400, errorBody("unsupported_value", "reasoning.effort", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorEffort, Status: 400, Code: "invalid_request_error", Param: "reasoning.effort"}},
		{"structured output", open, 400, errorBody("invalid_value", "text.format.schema", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorStructuredOutput, Status: 400, Code: "invalid_request_error", Param: "text.format.schema"}},
		{"cyber policy", open, 400, errorBody("cyber_policy", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorPolicy, Status: 400, Code: "cyber_policy"}},
		{"an invalid prompt", open, 400, errorBody("invalid_prompt", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorPolicy, Status: 400, Code: "invalid_prompt"}},
		{"another 400", open, 400, errorBody("unknown_parameter", "input[0].secret_member", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorInvalidRequest, Status: 400, Code: "invalid_request_error"}},
		{"a 422", open, 422, []byte(`{"detail":"x"}`),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorInvalidRequest, Status: 422}},
		{"401", open, 401, errorBody("invalid_api_key", "", "Incorrect API key provided: sk-abc"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorAuthentication, Status: 401, Code: "invalid_api_key"}},
		{"403", open, 403, errorBody("", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorPermission, Status: 403, Code: "invalid_request_error"}},
		{"404 model not found", open, 404, errorBody("model_not_found", "model", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorNotFound, Status: 404, Code: "model_not_found", Param: "model"}},
		{"404 without a code", open, 404, []byte(`not json`),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorNotFound, Status: 404}},
		{"413", open, 413, errorBody("", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorRequestTooLarge, Status: 413, Code: "invalid_request_error"}},
		{"429 out of quota", open, 429, errorBody("insufficient_quota", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorQuotaExhausted, Status: 429, Code: "insufficient_quota"}},
		{"429 out of credit", open, 429, errorBody("credit_balance_exhausted", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorQuotaExhausted, Status: 429, Code: "credit_balance_exhausted"}},
		{"429 rate limited", open, 429, errorBody("rate_limit_exceeded", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorRateLimited, Status: 429, Code: "rate_limit_exceeded"}},
		{"429 slow down", open, 429, errorBody("slow_down", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorRateLimited, Status: 429, Code: "slow_down"}},
		{"503 overloaded", open, 503, errorBody("server_is_overloaded", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorOverloaded, Status: 503, Code: "server_is_overloaded"}},
		{"503 policy capacity", open, 503, errorBody("policy_capacity_unavailable", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorOverloaded, Status: 503, Code: "policy_capacity_unavailable"}},
		{"529", open, 529, nil,
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorOverloaded, Status: 529}},
		{"504", open, 504, nil,
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorTimeout, Status: 504}},
		{"500", open, 500, errorBody("server_error", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorServer, Status: 500, Code: "server_error"}},
		{"502", open, 502, nil,
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorServer, Status: 502}},
		{"503 without an overload code", open, 503, nil,
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorServer, Status: 503}},
		{"a server failure is never a capability", open, 500, errorBody("invalid_image", "input[0].content[0].image_url", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorServer, Status: 500, Code: "invalid_image", Param: "input[0].content[0].image_url"}},
		{"a request timeout is a timeout", open, 408, errorBody("", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorTimeout, Status: 408, Code: "invalid_request_error"}},
		{"a conflict is a server failure", open, 409, errorBody("", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorServer, Status: 409, Code: "invalid_request_error"}},
		{"another client error", open, 410, errorBody("", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorInvalidRequest, Status: 410, Code: "invalid_request_error"}},
		{"451", open, 451, errorBody("", "", "x"),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorInvalidRequest, Status: 451, Code: "invalid_request_error"}},
		{"a string error", open, 400, []byte(`{"error":"Input exceeds the context window"}`),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorContextWindow, Status: 400}},
		{"an invalid status", open, 99, []byte(`{"error":{"code":"server_error"}}`),
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorServer, Code: "server_error"}},
	} {
		got := side.ClassifyRejection(test.request, test.status, nil, test.body)
		if got != test.want {
			t.Errorf("%s:\n got %+v\nwant %+v", test.name, got, test.want)
		}
		if err := got.Validate(); err != nil {
			t.Errorf("%s: class does not validate: %v", test.name, err)
		}
	}
}

func TestStreamFailureClassification(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	request := streamRequest(t, "", true)
	for _, test := range []struct {
		name  string
		error string
		want  protocolcore.ProviderErrorClass
	}{
		{"rate limited", `{"code":"rate_limit_exceeded","message":"x"}`, protocolcore.ProviderErrorRateLimited},
		{"out of quota", `{"code":"insufficient_quota","message":"x"}`, protocolcore.ProviderErrorQuotaExhausted},
		{"overloaded", `{"code":"server_is_overloaded","message":"x"}`, protocolcore.ProviderErrorOverloaded},
		{"server error", `{"code":"server_error","message":"x"}`, protocolcore.ProviderErrorServer},
		{"no code at all", `{"message":"x"}`, protocolcore.ProviderErrorServer},
		{"an unknown code", `{"code":"vendor_specific","message":"x"}`, protocolcore.ProviderErrorServer},
		{"an invalid request", `{"type":"invalid_request_error","message":"x"}`, protocolcore.ProviderErrorInvalidRequest},
		{"reasoning state", `{"code":"invalid_encrypted_content","message":"x"}`, protocolcore.ProviderErrorReasoningState},
		{"context window", `{"code":"context_length_exceeded","message":"x"}`, protocolcore.ProviderErrorContextWindow},
		{"an image", `{"code":"invalid_image","message":"x"}`, protocolcore.ProviderErrorImage},
		{"policy", `{"code":"bio_policy","message":"x"}`, protocolcore.ProviderErrorPolicy},
		{"model not found", `{"code":"model_not_found","message":"x"}`, protocolcore.ProviderErrorNotFound},
		{"an api key", `{"code":"invalid_api_key","message":"x"}`, protocolcore.ProviderErrorAuthentication},
	} {
		got := side.ClassifyRejection(request, 0, nil, []byte(test.error))
		if got.Class != test.want || got.Status != 0 || got.Validate() != nil {
			t.Errorf("%s: %+v, want %s", test.name, got, test.want)
		}
	}
}

func TestClassifierKeepsNoProviderText(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	secret := "org-SECRET-123 sk-SECRET"
	for _, body := range [][]byte{
		errorBody("SECRET_code", "input[0].SECRET", secret),
		[]byte(`{"error":{"message":"` + secret + `","type":"SECRET","code":"SECRET","param":"SECRET"}}`),
		[]byte(`{"error":"` + secret + `"}`),
	} {
		got := side.ClassifyRejection(streamRequest(t, "", false), 400, http.Header{"Retry-After": {"soon " + secret}}, body)
		if strings.Contains(strings.ToLower(got.Code+got.Param), "secret") || got.RetryAfter != 0 {
			t.Fatalf("provider text was kept: %+v", got)
		}
	}
}

func TestRetryAfterReadsMetadataOnly(t *testing.T) {
	t.Parallel()

	side, clock := newStreamSide(t)
	request := streamRequest(t, "", false)
	date := func(offset time.Duration) string { return clock.now.Add(offset).UTC().Format(http.TimeFormat) }
	for _, test := range []struct {
		name   string
		header http.Header
		body   string
		want   time.Duration
	}{
		{"milliseconds round up", http.Header{"Retry-After-Ms": {"1500"}}, "", 2 * time.Second},
		{"milliseconds come first", http.Header{"Retry-After-Ms": {"200"}, "Retry-After": {"30"}}, "", time.Second},
		{"seconds", http.Header{"Retry-After": {"7"}}, "", 7 * time.Second},
		{"zero is clamped up", http.Header{"Retry-After": {"0"}}, "", time.Second},
		{"a day at most", http.Header{"Retry-After": {"100000"}}, "", 86400 * time.Second},
		{"an HTTP date", http.Header{"Retry-After": {date(90 * time.Second)}}, "", 90 * time.Second},
		{"a past HTTP date", http.Header{"Retry-After": {date(-time.Hour)}}, "", time.Second},
		{"seconds come before rate-limit resets", http.Header{"Retry-After": {"3"}, "X-Ratelimit-Reset-Tokens": {"6m0s"}}, "", 3 * time.Second},
		{"the larger rate-limit reset", http.Header{"X-Ratelimit-Reset-Requests": {"1s"}, "X-Ratelimit-Reset-Tokens": {"6m0s"}}, "", 360 * time.Second},
		{"milliseconds of a reset round up", http.Header{"X-Ratelimit-Reset-Tokens": {"250ms"}}, "", time.Second},
		{"fractional reset seconds", http.Header{"X-Ratelimit-Reset-Requests": {"17.28s"}}, "", 18 * time.Second},
		{"an unreadable header falls through", http.Header{"Retry-After": {"soon"}, "X-Ratelimit-Reset-Requests": {"2s"}}, "", 2 * time.Second},
		{"body seconds", nil, `{"error":{"code":"rate_limit_exceeded","resets_in_seconds":42}}`, 42 * time.Second},
		{"body reset time", nil, `{"error":{"code":"rate_limit_exceeded","resets_at":` + itoa(clock.now.Unix()+100) + `}}`, 100 * time.Second},
		{"root reset time", nil, `{"error":{"code":"rate_limit_exceeded"},"resets_at":` + itoa(clock.now.Unix()+5) + `}`, 5 * time.Second},
		{"headers come before the body", http.Header{"Retry-After": {"9"}}, `{"error":{"resets_in_seconds":42}}`, 9 * time.Second},
		{"prose is never parsed", nil, `{"error":{"code":"rate_limit_exceeded","message":"Please try again in 20s."}}`, 0},
		{"nothing", nil, `{"error":{"code":"rate_limit_exceeded"}}`, 0},
	} {
		got := side.ClassifyRejection(request, 429, test.header, []byte(test.body))
		if got.RetryAfter != test.want || got.Validate() != nil {
			t.Errorf("%s: retry after %v, want %v", test.name, got.RetryAfter, test.want)
		}
	}
}

func itoa(value int64) string { return strconv.FormatInt(value, 10) }

func renderAnthropic(t testing.TB, request protocolcore.Request, class protocolcore.ProviderError) string {
	t.Helper()
	native := anthropicchat.RenderProviderError(request, class)
	return string(native.ForDialect(protocolspec.DialectAnthropicMessages))
}

// Claude Code 2.1.329 retries a 408 and a 409 from the API, and never a 400
// (its retry predicate, RRs in the bundle: x-should-retry "false" stops only
// a 429, 529 or 503). An upstream 408 or 409 therefore renders with a status
// Claude Code still retries, while other unlisted client errors stay final.
func TestTransientClientErrorsRenderAsRetryable(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	request := streamRequest(t, "", false)
	for _, test := range []struct {
		status     int
		wantStatus int
		wantType   string
		noRetry    bool
	}{
		{408, http.StatusGatewayTimeout, "timeout_error", false},
		{409, http.StatusInternalServerError, "api_error", false},
		{410, http.StatusBadRequest, "invalid_request_error", true},
		{451, http.StatusBadRequest, "invalid_request_error", true},
	} {
		class := side.ClassifyRejection(request, test.status, nil, errorBody("", "", "x"))
		native := anthropicchat.RenderProviderError(request, class)
		dialect := protocolspec.DialectAnthropicMessages
		body := string(native.ForDialect(dialect))
		noRetry := native.HeadersForDialect(dialect).Get("X-Should-Retry") == "false"
		if native.ClientStatus(dialect) != test.wantStatus || !strings.Contains(body, `"type":"`+test.wantType+`"`) || noRetry != test.noRetry {
			t.Errorf("%d rendered as %d %s (x-should-retry false: %v)", test.status, native.ClientStatus(dialect), body, noRetry)
		}
	}
}

// TestRenderedClassesReachClaudeCodeAsItsRecoveryTokens checks the two
// halves together: each capability class this side gives renders as the
// token the Anthropic client side's table names.
func TestRenderedClassesReachClaudeCodeAsItsRecoveryTokens(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	request := streamRequest(t, "", false)
	for body, token := range map[string]string{
		string(errorBody("invalid_encrypted_content", "", "x")):    "capability_rejected: thinking_signature",
		string(errorBody("invalid_image", "", "x")):                "capability_rejected: image_block",
		string(errorBody("", "input[0].content[1].file_url", "x")): "capability_rejected: document_block",
		string(errorBody("", "reasoning.effort", "x")):             "capability_rejected: effort_unsupported",
		string(errorBody("", "text.format", "x")):                  "capability_rejected: structured_outputs_unsupported",
		string(errorBody("context_length_exceeded", "", "x")):      "capability_rejected: prompt_too_long",
	} {
		class := side.ClassifyRejection(request, 400, nil, []byte(body))
		rendered := renderAnthropic(t, request, class)
		if !strings.Contains(rendered, `"message":"`+token+`"`) {
			t.Errorf("%s rendered as %s", body, rendered)
		}
	}
}
