package openairesponses

import (
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

// Upstream rejections are classified here into the neutral provider error
// classes a client side renders in its own dialect (design 8.2). Provider
// text is read only to classify: the class carries closed-vocabulary codes,
// known request fields and derived integers, never the message.

// maxClassifiedErrorBytes bounds the error body the classifier reads.
const maxClassifiedErrorBytes = 1 << 20

// maxRetryAfterSeconds clamps a retry delay to one day (design 8.4).
const maxRetryAfterSeconds = 86400

// providerErrorFields are the members of an OpenAI error object the
// classifier reads, plus the numeric reset fields some services add.
type providerErrorFields struct {
	message string
	kind    string
	code    string
	param   string
	// resetsInSeconds and resetsAt are numeric body fields, read from the
	// error object or the root.
	resetsInSeconds *float64
	resetsAt        *float64
}

type providerErrorObjectWire struct {
	Message         json.RawMessage `json:"message"`
	Type            json.RawMessage `json:"type"`
	Code            json.RawMessage `json:"code"`
	Param           json.RawMessage `json:"param"`
	ResetsInSeconds json.RawMessage `json:"resets_in_seconds"`
	ResetsAt        json.RawMessage `json:"resets_at"`
}

// parseProviderError reads an error body: {"error":{...}}, or an error object
// at the root, as response.failed and the error event carry it. A string
// error is its message. Members of another JSON type are ignored.
func parseProviderError(body []byte) providerErrorFields {
	if len(body) == 0 || len(body) > maxClassifiedErrorBytes {
		return providerErrorFields{}
	}
	var root struct {
		providerErrorObjectWire
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &root) != nil {
		return providerErrorFields{}
	}
	object := root.providerErrorObjectWire
	if rawPresent(root.Error) {
		var nested providerErrorObjectWire
		if json.Unmarshal(root.Error, &nested) == nil {
			object = nested
		} else {
			object = providerErrorObjectWire{Message: root.Error}
		}
	}
	fields := providerErrorFields{
		message: jsonString(object.Message),
		kind:    jsonString(object.Type),
		code:    jsonString(object.Code),
		param:   jsonString(object.Param),
	}
	fields.resetsInSeconds = jsonNumber(object.ResetsInSeconds)
	if fields.resetsInSeconds == nil {
		fields.resetsInSeconds = jsonNumber(root.ResetsInSeconds)
	}
	fields.resetsAt = jsonNumber(object.ResetsAt)
	if fields.resetsAt == nil {
		fields.resetsAt = jsonNumber(root.ResetsAt)
	}
	return fields
}

func jsonString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func jsonNumber(raw json.RawMessage) *float64 {
	var value float64
	if !rawPresent(raw) || json.Unmarshal(raw, &value) != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

// ClassifyRejection classifies an upstream HTTP rejection (design 8.2). The
// first matching row wins; codes and fields name a capability only on a
// client error, so a server failure is never mistaken for one.
func (side *ProviderSide) ClassifyRejection(
	request protocolcore.Request,
	status int,
	header http.Header,
	body []byte,
) protocolcore.ProviderError {
	return side.classify(request, status, header, parseProviderError(body))
}

// streamFailure is the failure of a response that failed after its headers:
// response.failed, an error event, or a failed unary body. The class is
// classified with no HTTP status, and no native error of the provider's
// dialect is kept, since only the client's dialect may reach the client.
func (side *ProviderSide) streamFailure(request protocolcore.Request, path string, raw json.RawMessage) *protocolcore.Failure {
	class := side.classify(request, 0, nil, parseProviderError(raw))
	failure := protocolcore.NewProviderFailure(path, raw)
	failure.Provider = &class
	return failure
}

var contextWindowLimit = regexp.MustCompile(`(?:maximum context length|context window|context length) (?:is|of) (\d{1,9})`)

func (side *ProviderSide) classify(
	request protocolcore.Request,
	status int,
	header http.Header,
	fields providerErrorFields,
) protocolcore.ProviderError {
	if status < 100 || status > 599 {
		status = 0
	}
	message := strings.ToLower(fields.message)
	hasCode := func(codes ...string) bool {
		for _, code := range codes {
			if fields.code == code || fields.kind == code {
				return true
			}
		}
		return false
	}
	codeNames := func(fragments ...string) bool {
		for _, fragment := range fragments {
			if strings.Contains(fields.code, fragment) {
				return true
			}
		}
		return false
	}
	paramNames := func(fragments ...string) bool {
		for _, fragment := range fragments {
			if strings.Contains(fields.param, fragment) {
				return true
			}
		}
		return false
	}
	result := protocolcore.ProviderError{
		Status:     status,
		Code:       knownClassifiedCode(fields.code, fields.kind),
		Param:      protocolcore.KnownProviderErrorParam(fields.param),
		RetryAfter: side.retryAfter(header, fields),
	}
	clientError := status == 0 || (status >= 400 && status < 500)
	contextWindow := hasCode("context_length_exceeded", "context_window_exceeded") ||
		strings.Contains(message, "context window") || strings.Contains(message, "context length") ||
		strings.Contains(message, "maximum context")
	switch {
	case clientError && contextWindow && sendsMaxOutputTokens(request) &&
		(strings.Contains(message, "max_output_tokens") || strings.Contains(message, "completion")):
		result.Class = protocolcore.ProviderErrorMaxTokensOverflow
	case clientError && contextWindow:
		result.Class = protocolcore.ProviderErrorContextWindow
		if match := contextWindowLimit.FindStringSubmatch(message); match != nil {
			result.LimitTokens, _ = strconv.Atoi(match[1])
		}
		if result.LimitTokens > 0 {
			// The input size the client compacts by is this side's own
			// estimate of what it sent, never a number from provider text.
			if estimate, err := side.CountInputTokens(protocolpath.ProviderContext{}, request); err == nil {
				result.ActualTokens = estimate
			}
		}
	case clientError && hasCode("invalid_encrypted_content"):
		result.Class = protocolcore.ProviderErrorReasoningState
	case clientError && (codeNames("image") || paramNames("image_url", "input_image")):
		result.Class = protocolcore.ProviderErrorImage
	case clientError && (codeNames("file", "pdf") || paramNames("file_data", "file_url", "input_file")):
		result.Class = protocolcore.ProviderErrorDocument
	case clientError && strings.HasPrefix(fields.param, "reasoning.effort"):
		result.Class = protocolcore.ProviderErrorEffort
	case clientError && strings.HasPrefix(fields.param, "text.format"):
		result.Class = protocolcore.ProviderErrorStructuredOutput
	case clientError && hasCode("cyber_policy", "bio_policy", "content_policy_violation", "invalid_prompt"):
		result.Class = protocolcore.ProviderErrorPolicy
	case status == 400 || status == 422:
		result.Class = protocolcore.ProviderErrorInvalidRequest
	case status == 401 || hasCode("invalid_api_key"):
		result.Class = protocolcore.ProviderErrorAuthentication
	case status == 403:
		result.Class = protocolcore.ProviderErrorPermission
	case status == 404 || (status == 0 && hasCode("model_not_found")):
		result.Class = protocolcore.ProviderErrorNotFound
	case status == 413:
		result.Class = protocolcore.ProviderErrorRequestTooLarge
	case (status == 429 || status == 0) && hasCode("insufficient_quota", "credit_balance_exhausted"):
		result.Class = protocolcore.ProviderErrorQuotaExhausted
	case status == 429 || (status == 0 && hasCode("rate_limit_exceeded")):
		result.Class = protocolcore.ProviderErrorRateLimited
	case status == 529 || ((status == 503 || status == 0) &&
		hasCode("server_is_overloaded", "slow_down", "policy_capacity_unavailable")):
		result.Class = protocolcore.ProviderErrorOverloaded
	case status == 504 || status == 408:
		// A request timeout is transient like a gateway timeout, and is
		// rendered as one, so the client retries it as it would a 408.
		result.Class = protocolcore.ProviderErrorTimeout
	case status >= 500 || status == 409:
		// A conflict is transient too: rendered as a server failure, it is
		// retried as the client would retry a 409.
		result.Class = protocolcore.ProviderErrorServer
	case status != 0:
		// Any other client error is still the upstream refusing the
		// translated request.
		result.Class = protocolcore.ProviderErrorInvalidRequest
	case hasCode("invalid_request_error"):
		result.Class = protocolcore.ProviderErrorInvalidRequest
	default:
		// A failure after the headers with nothing better to go on is the
		// upstream's own failure, which the client may retry.
		result.Class = protocolcore.ProviderErrorServer
	}
	return result
}

// knownClassifiedCode is the closed-vocabulary code a provider error
// carries: the error's code when it is known, else its type when that is.
func knownClassifiedCode(code, kind string) string {
	if known := protocolcore.KnownClassifiedErrorCode(code); known != "" {
		return known
	}
	return protocolcore.KnownClassifiedErrorCode(kind)
}

// retryAfter reads a retry delay from metadata only (design 8.4), in order:
// retry-after-ms; retry-after in seconds or as an HTTP date; the larger of
// the two x-ratelimit-reset durations; the numeric resets_in_seconds or
// resets_at in the body. It is rounded up to whole seconds and clamped to
// 1-86400. A sentence such as "try again in 20s" is never parsed.
func (side *ProviderSide) retryAfter(header http.Header, fields providerErrorFields) time.Duration {
	now := side.now()
	if value := strings.TrimSpace(header.Get("Retry-After-Ms")); value != "" {
		if milliseconds, err := strconv.ParseFloat(value, 64); err == nil && finite(milliseconds) {
			return clampRetrySeconds(milliseconds / 1000)
		}
	}
	if value := strings.TrimSpace(header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.ParseFloat(value, 64); err == nil && finite(seconds) {
			return clampRetrySeconds(seconds)
		}
		if date, err := http.ParseTime(value); err == nil {
			return clampRetrySeconds(date.Sub(now).Seconds())
		}
	}
	reset, found := time.Duration(0), false
	for _, name := range []string{"X-Ratelimit-Reset-Requests", "X-Ratelimit-Reset-Tokens"} {
		if duration, err := time.ParseDuration(strings.TrimSpace(header.Get(name))); err == nil && duration >= 0 {
			reset, found = max(reset, duration), true
		}
	}
	if found {
		return clampRetrySeconds(reset.Seconds())
	}
	if fields.resetsInSeconds != nil {
		return clampRetrySeconds(*fields.resetsInSeconds)
	}
	if fields.resetsAt != nil {
		return clampRetrySeconds(*fields.resetsAt - float64(now.UnixNano())/1e9)
	}
	return 0
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func clampRetrySeconds(seconds float64) time.Duration {
	rounded := math.Ceil(seconds)
	switch {
	case rounded < 1:
		rounded = 1
	case rounded > maxRetryAfterSeconds:
		rounded = maxRetryAfterSeconds
	}
	return time.Duration(rounded) * time.Second
}
