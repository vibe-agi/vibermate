package anthropicchat

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

type renderedError struct {
	status     int
	errorType  string
	message    string
	noRetry    bool
	retryAfter string
}

func readRendered(t *testing.T, native protocolcore.NativeProviderError) renderedError {
	t.Helper()
	dialect := protocolspec.DialectAnthropicMessages
	payload := native.ForDialect(dialect)
	if payload == nil {
		t.Fatal("nothing was rendered for the Anthropic dialect")
	}
	var wire anthropicErrorWire
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		t.Fatal(err)
	}
	// The in-band form is the same error as an Anthropic error event.
	name, data := native.StreamEvent(dialect)
	var event struct {
		Type  string             `json:"type"`
		Error anthropicErrorWire `json:"error"`
	}
	if name != "error" || json.Unmarshal(data, &event) != nil || event.Type != "error" || event.Error != wire {
		t.Fatalf("stream event = %s %s", name, data)
	}
	headers := native.HeadersForDialect(dialect)
	for name := range headers {
		if name != "X-Should-Retry" && name != "Retry-After" {
			t.Fatalf("unexpected header %s", name)
		}
	}
	if value := headers.Get("X-Should-Retry"); value != "" && value != "false" {
		t.Fatalf("x-should-retry = %q", value)
	}
	return renderedError{
		status:     native.ClientStatus(dialect),
		errorType:  wire.Type,
		message:    wire.Message,
		noRetry:    headers.Get("X-Should-Retry") == "false",
		retryAfter: headers.Get("Retry-After"),
	}
}

// Every row of design 8.3, in order.
func TestRenderProviderErrorFollowsTheDesignTable(t *testing.T) {
	t.Parallel()

	request := protocolcore.Request{RequestedModel: "claude-opus-5-5"}
	now := time.Unix(1_800_000_000, 0)
	resets := now.Add(90 * time.Minute)
	tests := []struct {
		name  string
		class protocolcore.ProviderError
		want  renderedError
	}{
		{"context window, both sizes known",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorContextWindow, Status: 400, Code: "context_length_exceeded", LimitTokens: 272000, ActualTokens: 301234},
			renderedError{400, "invalid_request_error", "prompt is too long: 301234 tokens > 272000 maximum", true, ""}},
		{"context window, estimate at or below the window",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorContextWindow, LimitTokens: 272000, ActualTokens: 200000},
			renderedError{400, "invalid_request_error", "prompt is too long: 272001 tokens > 272000 maximum", true, ""}},
		{"context window, window unknown",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorContextWindow, Status: 413, ActualTokens: 5},
			renderedError{400, "invalid_request_error", "capability_rejected: prompt_too_long", true, ""}},
		{"context window, estimate unknown",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorContextWindow, LimitTokens: 272000},
			renderedError{400, "invalid_request_error", "capability_rejected: prompt_too_long", true, ""}},
		{"max tokens overflow",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorMaxTokensOverflow, Status: 400},
			renderedError{400, "invalid_request_error", "capability_rejected: max_tokens_context_overflow", true, ""}},
		{"reasoning state",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorReasoningState, Status: 400, Code: "invalid_encrypted_content"},
			renderedError{400, "invalid_request_error", "capability_rejected: thinking_signature", true, ""}},
		{"image",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorImage, Status: 400, Code: "invalid_image", Param: "input[3].content[0].image_url"},
			renderedError{400, "invalid_request_error", "capability_rejected: image_block", true, ""}},
		{"document",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorDocument, Status: 400},
			renderedError{400, "invalid_request_error", "capability_rejected: document_block", true, ""}},
		{"effort",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorEffort, Status: 400, Param: "reasoning.effort"},
			renderedError{400, "invalid_request_error", "capability_rejected: effort_unsupported", true, ""}},
		{"structured output",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorStructuredOutput, Status: 400, Param: "text.format.schema"},
			renderedError{400, "invalid_request_error", "capability_rejected: structured_outputs_unsupported", true, ""}},
		{"policy",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorPolicy, Status: 400, Code: "cyber_policy"},
			renderedError{400, "policy_blocked", "OpenAI declined this request under its usage policies (cyber_policy).", true, ""}},
		{"policy without a code",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorPolicy, Status: 400},
			renderedError{400, "policy_blocked", "OpenAI declined this request under its usage policies.", true, ""}},
		{"invalid request",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorInvalidRequest, Status: 422, Code: "invalid_request_error", Param: "tools[3].parameters"},
			renderedError{400, "invalid_request_error", "The OpenAI service rejected the translated request (invalid_request_error at tools[3].parameters).", true, ""}},
		{"invalid request, field only",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorInvalidRequest, Status: 400, Param: "input[2].phase"},
			renderedError{400, "invalid_request_error", "The OpenAI service rejected the translated request (at input[2].phase).", true, ""}},
		{"invalid request, nothing known",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorInvalidRequest, Status: 400},
			renderedError{400, "invalid_request_error", "The OpenAI service rejected the translated request.", true, ""}},
		{"authentication",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorAuthentication, Status: 401, Code: "invalid_api_key"},
			renderedError{401, "authentication_error", "The upstream account's credential was rejected (invalid_api_key). Update the account in ViberMate \u2192 Accounts.", true, ""}},
		{"permission",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorPermission, Status: 403},
			renderedError{403, "permission_error", "This account cannot use the requested upstream model or service.", true, ""}},
		{"not found",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorNotFound, Status: 404, Code: "model_not_found"},
			renderedError{404, "not_found_error", "This route sends claude-opus-5-5 to an upstream model the account cannot use (model_not_found). Change the route's model mapping in ViberMate.", true, ""}},
		{"request too large",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorRequestTooLarge, Status: 413},
			renderedError{413, "request_too_large", "The translated request is larger than the upstream service accepts.", true, ""}},
		{"rate limited, delay known",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorRateLimited, Status: 429, Code: "rate_limit_exceeded", RetryAfter: 1500 * time.Millisecond},
			renderedError{429, "rate_limit_error", "The upstream service is rate limiting this account.", false, "2"}},
		{"rate limited, delay unknown",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorRateLimited, Status: 429},
			renderedError{429, "rate_limit_error", "The upstream service is rate limiting this account.", false, ""}},
		{"quota",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorQuotaExhausted, Status: 429, Code: "insufficient_quota"},
			renderedError{429, "rate_limit_error", "The OpenAI account has no remaining credit or quota (insufficient_quota).", true, ""}},
		{"usage limit",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorUsageLimit, Status: 429, Code: "usage_limit_reached", ResetsAt: resets.Unix()},
			renderedError{429, "rate_limit_error", "The ChatGPT plan's usage limit is reached; it resets at " + resets.Local().Format("2006-01-02 15:04 MST") + ".", true, "5400"}},
		{"usage limit, reset unknown",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorUsageLimit, Status: 429},
			renderedError{429, "rate_limit_error", "The ChatGPT plan's usage limit is reached.", true, ""}},
		{"overloaded",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorOverloaded, Status: 503, Code: "server_is_overloaded", RetryAfter: 30 * time.Second},
			renderedError{529, "overloaded_error", "The upstream service is overloaded.", false, "30"}},
		{"timeout",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorTimeout, Status: 504},
			renderedError{504, "timeout_error", "The upstream service timed out.", false, ""}},
		{"server",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorServer, Status: 502},
			renderedError{502, "api_error", "The upstream service failed (server_error).", false, ""}},
		{"server, in band",
			protocolcore.ProviderError{Class: protocolcore.ProviderErrorServer, Code: "server_error"},
			renderedError{500, "api_error", "The upstream service failed (server_error).", false, ""}},
	}
	covered := map[protocolcore.ProviderErrorClass]bool{}
	for _, test := range tests {
		covered[test.class.Class] = true
		if got := readRendered(t, renderProviderError(request, test.class, now)); got != test.want {
			t.Errorf("%s\n got: %+v\nwant: %+v", test.name, got, test.want)
		}
	}
	for _, class := range []protocolcore.ProviderErrorClass{
		protocolcore.ProviderErrorContextWindow, protocolcore.ProviderErrorMaxTokensOverflow, protocolcore.ProviderErrorReasoningState,
		protocolcore.ProviderErrorImage, protocolcore.ProviderErrorDocument, protocolcore.ProviderErrorEffort,
		protocolcore.ProviderErrorStructuredOutput, protocolcore.ProviderErrorInvalidRequest, protocolcore.ProviderErrorRequestTooLarge,
		protocolcore.ProviderErrorAuthentication, protocolcore.ProviderErrorPermission, protocolcore.ProviderErrorNotFound,
		protocolcore.ProviderErrorRateLimited, protocolcore.ProviderErrorUsageLimit, protocolcore.ProviderErrorQuotaExhausted,
		protocolcore.ProviderErrorOverloaded, protocolcore.ProviderErrorTimeout, protocolcore.ProviderErrorServer,
		protocolcore.ProviderErrorPolicy,
	} {
		if !covered[class] {
			t.Errorf("class %s has no row", class)
		}
	}
}

// Claude Code sizes compaction from this message with
// /prompt is too long[^0-9]*(\d+)\s*tokens?\s*>\s*(\d+)/ and starts it only
// on status 400; a capability token must be the whole message.
func TestRenderedRecoveryMessagesMatchClaudeCode(t *testing.T) {
	t.Parallel()

	pattern := regexp.MustCompile(`prompt is too long[^0-9]*(\d+)\s*tokens?\s*>\s*(\d+)`)
	for _, sizes := range [][2]int{{301234, 272000}, {1, 272000}, {272000, 272000}} {
		rendered := readRendered(t, RenderProviderError(protocolcore.Request{}, protocolcore.ProviderError{
			Class: protocolcore.ProviderErrorContextWindow, ActualTokens: sizes[0], LimitTokens: sizes[1],
		}))
		match := pattern.FindStringSubmatch(rendered.message)
		if match == nil || rendered.status != http.StatusBadRequest {
			t.Fatalf("%q does not match Claude Code's matcher", rendered.message)
		}
		actual, _ := strconv.Atoi(match[1])
		limit, _ := strconv.Atoi(match[2])
		if limit != sizes[1] || actual <= limit || actual < sizes[0] {
			t.Fatalf("%q: gap %d > %d", rendered.message, actual, limit)
		}
	}
	for _, class := range []protocolcore.ProviderErrorClass{
		protocolcore.ProviderErrorMaxTokensOverflow, protocolcore.ProviderErrorReasoningState,
		protocolcore.ProviderErrorImage, protocolcore.ProviderErrorDocument,
		protocolcore.ProviderErrorEffort, protocolcore.ProviderErrorStructuredOutput,
	} {
		rendered := readRendered(t, RenderProviderError(protocolcore.Request{}, protocolcore.ProviderError{Class: class, Status: 422, Code: "invalid_image"}))
		if rendered.status != http.StatusBadRequest || !regexp.MustCompile(`^capability_rejected: [a-z_]+$`).MatchString(rendered.message) {
			t.Fatalf("%s: %+v", class, rendered)
		}
	}
	// "model:" starts Claude Code's model-access flow, so it never appears,
	// not even through the client's own model name.
	for _, model := range []string{"claude-opus-5-5", "custom/model:latest", "MODEL:x", ""} {
		rendered := readRendered(t, RenderProviderError(protocolcore.Request{RequestedModel: model}, protocolcore.ProviderError{
			Class: protocolcore.ProviderErrorNotFound, Status: 404, Code: "model_not_found",
		}))
		if strings.Contains(strings.ToLower(rendered.message), "model:") {
			t.Fatalf("%q", rendered.message)
		}
	}
}

// A ProviderError cannot hold provider text, and the renderer adds only fixed
// sentences, closed-vocabulary codes, known request fields, integers and the
// request's model. Every rendered message, with those removed, is one of the
// fixed sentences.
func TestRenderedErrorsCarryNoProviderText(t *testing.T) {
	t.Parallel()

	fixed := regexp.MustCompile(`^(capability_rejected: [a-z_]+|prompt is too long: N tokens > N maximum|` +
		`OpenAI declined this request under its usage policies( \(CODE\))?\.|` +
		`The OpenAI service rejected the translated request( \((CODE )?(at PARAM)?\))?\.|` +
		`The upstream account's credential was rejected( \(CODE\))?\. Update the account in ViberMate \x{2192} Accounts\.|` +
		`This account cannot use the requested upstream model or service\.|` +
		`This route sends MODEL to an upstream model the account cannot use( \(CODE\))?\. Change the route's model mapping in ViberMate\.|` +
		`The translated request is larger than the upstream service accepts\.|` +
		`The upstream service is rate limiting this account\.|` +
		`The OpenAI account has no remaining credit or quota( \(CODE\))?\.|` +
		`The ChatGPT plan's usage limit is reached(; it resets at [0-9: -]+[A-Za-z+0-9-]*)?\.|` +
		`The upstream service is overloaded\.|The upstream service timed out\.|` +
		`The upstream service failed \(CODE\)\.)$`)
	digits := regexp.MustCompile(`[0-9]+`)
	code, param, model := "insufficient_quota", "input[12].content[3].file_data", "claude-sonnet-5-5"
	for _, class := range []protocolcore.ProviderErrorClass{
		protocolcore.ProviderErrorContextWindow, protocolcore.ProviderErrorMaxTokensOverflow, protocolcore.ProviderErrorReasoningState,
		protocolcore.ProviderErrorImage, protocolcore.ProviderErrorDocument, protocolcore.ProviderErrorEffort,
		protocolcore.ProviderErrorStructuredOutput, protocolcore.ProviderErrorInvalidRequest, protocolcore.ProviderErrorRequestTooLarge,
		protocolcore.ProviderErrorAuthentication, protocolcore.ProviderErrorPermission, protocolcore.ProviderErrorNotFound,
		protocolcore.ProviderErrorRateLimited, protocolcore.ProviderErrorUsageLimit, protocolcore.ProviderErrorQuotaExhausted,
		protocolcore.ProviderErrorOverloaded, protocolcore.ProviderErrorTimeout, protocolcore.ProviderErrorServer,
		protocolcore.ProviderErrorPolicy,
	} {
		for _, providerError := range []protocolcore.ProviderError{
			{Class: class},
			{Class: class, Status: 500, Code: code, Param: param, RetryAfter: time.Second, ResetsAt: 1_800_000_000, LimitTokens: 10, ActualTokens: 20},
		} {
			rendered := readRendered(t, RenderProviderError(protocolcore.Request{RequestedModel: model}, providerError))
			message := strings.NewReplacer(code, "CODE", "server_error", "CODE", param, "PARAM", model, "MODEL").Replace(rendered.message)
			if class != protocolcore.ProviderErrorUsageLimit {
				message = digits.ReplaceAllString(message, "N")
			}
			if !fixed.MatchString(message) {
				t.Fatalf("%s: %q is not a fixed sentence", class, rendered.message)
			}
		}
	}
	// An invalid class renders nothing, so the caller keeps its envelope.
	for _, invalid := range []protocolcore.ProviderError{
		{Class: "upstream says hello"},
		{Class: protocolcore.ProviderErrorServer, Code: "provider text"},
		{Class: protocolcore.ProviderErrorInvalidRequest, Param: "sk-proj-AbC123"},
	} {
		if native := RenderProviderError(protocolcore.Request{}, invalid); native.ForDialect(protocolspec.DialectAnthropicMessages) != nil {
			t.Fatalf("%+v rendered", invalid)
		}
	}
}

func TestRetryAfterIsWholeSecondsWithinBounds(t *testing.T) {
	t.Parallel()

	for delay, want := range map[time.Duration]int{
		time.Nanosecond: 1, 999 * time.Millisecond: 1, time.Second: 1, 1001 * time.Millisecond: 2,
		6 * time.Minute: 360, 48 * time.Hour: 86400,
	} {
		if got := retryAfterSeconds(delay); got != want {
			t.Fatalf("retryAfterSeconds(%s) = %d, want %d", delay, got, want)
		}
	}
}
