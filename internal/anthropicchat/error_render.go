package anthropicchat

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// capabilityRejectedPrefix starts the tokens Claude Code recovers from. The
// token must be the whole error message, and Claude Code matches it only on
// status 400 (design F35).
const capabilityRejectedPrefix = "capability_rejected: "

// maxRetryAfterSeconds bounds the retry-after header a rendered error gives.
const maxRetryAfterSeconds = 86400

type anthropicErrorWire struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// renderedProviderError is one row of the design 8.3 table.
type renderedProviderError struct {
	status     int
	errorType  string
	message    string
	noRetry    bool
	retryAfter time.Duration
	retryKnown bool
}

// RenderProviderError renders a classified upstream rejection as the
// Anthropic error that makes Claude Code take the right recovery: compaction
// for an overlong prompt, dropping images, documents, effort, structured
// output or earlier thinking for a capability it lacks, retrying a transient
// failure, and stopping on one that retrying cannot fix (design 8.3). Only the
// class, closed-vocabulary codes, known request fields, derived integers and
// the request's own model name reach the message; provider text never does.
// An invalid class renders nothing, so the caller keeps its generic envelope.
func RenderProviderError(request protocolcore.Request, providerError protocolcore.ProviderError) protocolcore.NativeProviderError {
	return renderProviderError(request, providerError, time.Now())
}

func renderProviderError(
	request protocolcore.Request,
	providerError protocolcore.ProviderError,
	now time.Time,
) protocolcore.NativeProviderError {
	if providerError.Validate() != nil {
		return protocolcore.NativeProviderError{}
	}
	row := providerErrorRow(request, providerError, now)
	wire := anthropicErrorWire{Type: row.errorType, Message: row.message}
	payload, err := json.Marshal(wire)
	if err != nil {
		return protocolcore.NativeProviderError{}
	}
	event, err := json.Marshal(struct {
		Type  string             `json:"type"`
		Error anthropicErrorWire `json:"error"`
	}{Type: "error", Error: wire})
	if err != nil {
		return protocolcore.NativeProviderError{}
	}
	headers := http.Header{}
	if row.noRetry {
		headers.Set("X-Should-Retry", "false")
	}
	if row.retryKnown {
		headers.Set("Retry-After", strconv.Itoa(retryAfterSeconds(row.retryAfter)))
	}
	// In band, the same envelope goes out as an Anthropic error event.
	return protocolcore.NewNativeProviderError(protocolspec.DialectAnthropicMessages, payload).
		WithClientStatus(row.status).
		WithHeaders(headers).
		WithStreamEvent("error", event)
}

func providerErrorRow(
	request protocolcore.Request,
	providerError protocolcore.ProviderError,
	now time.Time,
) renderedProviderError {
	capability := func(token string) renderedProviderError {
		return renderedProviderError{
			status: http.StatusBadRequest, errorType: "invalid_request_error",
			message: capabilityRejectedPrefix + token, noRetry: true,
		}
	}
	code := providerError.Code
	switch providerError.Class {
	case protocolcore.ProviderErrorContextWindow:
		// With both numbers known, Claude Code's prompt-too-long matcher
		// sizes compaction from their gap; the input estimate is clamped
		// above the window so the gap is never zero or negative. Without
		// both, a token asks for compaction without inventing a size.
		if providerError.LimitTokens > 0 && providerError.ActualTokens > 0 {
			actual := max(providerError.ActualTokens, providerError.LimitTokens+1)
			return renderedProviderError{
				status: http.StatusBadRequest, errorType: "invalid_request_error", noRetry: true,
				message: fmt.Sprintf("prompt is too long: %d tokens > %d maximum", actual, providerError.LimitTokens),
			}
		}
		return capability("prompt_too_long")
	case protocolcore.ProviderErrorMaxTokensOverflow:
		return capability("max_tokens_context_overflow")
	case protocolcore.ProviderErrorReasoningState:
		return capability("thinking_signature")
	case protocolcore.ProviderErrorImage:
		return capability("image_block")
	case protocolcore.ProviderErrorDocument:
		return capability("document_block")
	case protocolcore.ProviderErrorEffort:
		return capability("effort_unsupported")
	case protocolcore.ProviderErrorStructuredOutput:
		return capability("structured_outputs_unsupported")
	case protocolcore.ProviderErrorPolicy:
		return renderedProviderError{
			status: http.StatusBadRequest, errorType: "policy_blocked", noRetry: true,
			message: "OpenAI declined this request under its usage policies" + parenthesized(code) + ".",
		}
	case protocolcore.ProviderErrorInvalidRequest:
		detail := code
		if providerError.Param != "" {
			detail = strings.TrimSpace(detail + " at " + providerError.Param)
		}
		return renderedProviderError{
			status: http.StatusBadRequest, errorType: "invalid_request_error", noRetry: true,
			message: "The OpenAI service rejected the translated request" + parenthesized(detail) + ".",
		}
	case protocolcore.ProviderErrorAuthentication:
		return renderedProviderError{
			status: http.StatusUnauthorized, errorType: "authentication_error", noRetry: true,
			message: "The upstream account's credential was rejected" + parenthesized(code) +
				". Update the account in ViberMate \u2192 Accounts.",
		}
	case protocolcore.ProviderErrorPermission:
		return renderedProviderError{
			status: http.StatusForbidden, errorType: "permission_error", noRetry: true,
			message: "This account cannot use the requested upstream model or service.",
		}
	case protocolcore.ProviderErrorNotFound:
		// Never the substring "model:", which starts Claude Code's
		// model-access flow.
		requested := request.RequestedModel
		if requested == "" || strings.Contains(strings.ToLower(requested), "model:") {
			requested = "the requested model"
		}
		return renderedProviderError{
			status: http.StatusNotFound, errorType: "not_found_error", noRetry: true,
			message: "This route sends " + requested + " to an upstream model the account cannot use" +
				parenthesized(code) + ". Change the route's model mapping in ViberMate.",
		}
	case protocolcore.ProviderErrorRequestTooLarge:
		return renderedProviderError{
			status: http.StatusRequestEntityTooLarge, errorType: "request_too_large", noRetry: true,
			message: "The translated request is larger than the upstream service accepts.",
		}
	case protocolcore.ProviderErrorRateLimited:
		return renderedProviderError{
			status: http.StatusTooManyRequests, errorType: "rate_limit_error",
			message:    "The upstream service is rate limiting this account.",
			retryAfter: providerError.RetryAfter, retryKnown: providerError.RetryAfter > 0,
		}
	case protocolcore.ProviderErrorQuotaExhausted:
		return renderedProviderError{
			status: http.StatusTooManyRequests, errorType: "rate_limit_error", noRetry: true,
			message: "The OpenAI account has no remaining credit or quota" + parenthesized(code) + ".",
		}
	case protocolcore.ProviderErrorUsageLimit:
		row := renderedProviderError{
			status: http.StatusTooManyRequests, errorType: "rate_limit_error", noRetry: true,
			message:    "The ChatGPT plan's usage limit is reached.",
			retryAfter: providerError.RetryAfter, retryKnown: providerError.RetryAfter > 0,
		}
		if providerError.ResetsAt > 0 {
			resets := time.Unix(providerError.ResetsAt, 0)
			row.message = "The ChatGPT plan's usage limit is reached; it resets at " +
				resets.Local().Format("2006-01-02 15:04 MST") + "."
			if !row.retryKnown && resets.After(now) {
				row.retryAfter, row.retryKnown = resets.Sub(now), true
			}
		}
		return row
	case protocolcore.ProviderErrorOverloaded:
		return renderedProviderError{
			status: 529, errorType: "overloaded_error",
			message:    "The upstream service is overloaded.",
			retryAfter: providerError.RetryAfter, retryKnown: providerError.RetryAfter > 0,
		}
	case protocolcore.ProviderErrorTimeout:
		return renderedProviderError{
			status: http.StatusGatewayTimeout, errorType: "timeout_error",
			message: "The upstream service timed out.",
		}
	default: // ProviderErrorServer
		status := providerError.Status
		if status < 500 || status > 599 {
			status = http.StatusInternalServerError
		}
		if code == "" {
			code = "server_error"
		}
		return renderedProviderError{
			status: status, errorType: "api_error",
			message: "The upstream service failed" + parenthesized(code) + ".",
		}
	}
}

// parenthesized is " (detail)", or nothing for an empty detail.
func parenthesized(detail string) string {
	if detail == "" {
		return ""
	}
	return " (" + detail + ")"
}

// retryAfterSeconds rounds a delay up to whole seconds within 1-86400.
func retryAfterSeconds(delay time.Duration) int {
	seconds := math.Ceil(delay.Seconds())
	switch {
	case seconds < 1:
		return 1
	case seconds > maxRetryAfterSeconds:
		return maxRetryAfterSeconds
	default:
		return int(seconds)
	}
}
