package protocolcore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

type Reason string

const (
	ReasonInvalidClientRequest    Reason = "invalid_client_request"
	ReasonUnsupportedClientInput  Reason = "unsupported_client_input"
	ReasonInvalidProviderResponse Reason = "invalid_provider_response"
	ReasonProviderResponseFailed  Reason = "provider_response_failed"
	ReasonUnsupportedProviderData Reason = "unsupported_provider_data"
	ReasonMalformedEventStream    Reason = "malformed_event_stream"
	ReasonTruncatedEventStream    Reason = "truncated_event_stream"
	ReasonStreamStateViolation    Reason = "stream_state_violation"
	ReasonStreamLimitExceeded     Reason = "stream_limit_exceeded"
	ReasonToolCallIncomplete      Reason = "tool_call_incomplete"
	ReasonOperationCanceled       Reason = "operation_canceled"
)

type Failure struct {
	Reason            Reason
	Path              string
	ProviderErrorCode string
	NativeError       NativeProviderError `json:"-"`
	// Provider is the neutral class of an upstream rejection that a
	// cross-dialect path classified; nil when nothing was classified.
	Provider *ProviderError `json:"-"`
	cause    error
}

// ProviderErrorClass is the neutral meaning of an upstream rejection, so a
// client side can render it in its own dialect without provider text.
type ProviderErrorClass string

const (
	ProviderErrorContextWindow     ProviderErrorClass = "context_window"      // input alone exceeds the window
	ProviderErrorMaxTokensOverflow ProviderErrorClass = "max_tokens_overflow" // input plus max_output_tokens exceeds it
	ProviderErrorReasoningState    ProviderErrorClass = "reasoning_state"     // invalid_encrypted_content
	ProviderErrorImage             ProviderErrorClass = "image"
	ProviderErrorDocument          ProviderErrorClass = "document"
	ProviderErrorEffort            ProviderErrorClass = "effort"
	ProviderErrorStructuredOutput  ProviderErrorClass = "structured_output"
	ProviderErrorInvalidRequest    ProviderErrorClass = "invalid_request"
	ProviderErrorRequestTooLarge   ProviderErrorClass = "request_too_large"
	ProviderErrorAuthentication    ProviderErrorClass = "authentication"
	ProviderErrorPermission        ProviderErrorClass = "permission"
	ProviderErrorNotFound          ProviderErrorClass = "not_found"
	ProviderErrorRateLimited       ProviderErrorClass = "rate_limited"
	ProviderErrorUsageLimit        ProviderErrorClass = "usage_limit" // a plan window that resets later
	ProviderErrorQuotaExhausted    ProviderErrorClass = "quota"       // credit; retrying cannot help
	ProviderErrorOverloaded        ProviderErrorClass = "overloaded"
	ProviderErrorTimeout           ProviderErrorClass = "timeout"
	ProviderErrorServer            ProviderErrorClass = "server"
	ProviderErrorPolicy            ProviderErrorClass = "policy"
)

func (class ProviderErrorClass) Valid() bool {
	switch class {
	case ProviderErrorContextWindow, ProviderErrorMaxTokensOverflow, ProviderErrorReasoningState,
		ProviderErrorImage, ProviderErrorDocument, ProviderErrorEffort, ProviderErrorStructuredOutput,
		ProviderErrorInvalidRequest, ProviderErrorRequestTooLarge, ProviderErrorAuthentication,
		ProviderErrorPermission, ProviderErrorNotFound, ProviderErrorRateLimited, ProviderErrorUsageLimit,
		ProviderErrorQuotaExhausted, ProviderErrorOverloaded, ProviderErrorTimeout, ProviderErrorServer,
		ProviderErrorPolicy:
		return true
	default:
		return false
	}
}

// MaxProviderRetryAfter bounds a retry delay taken from provider metadata.
const MaxProviderRetryAfter = 24 * time.Hour

// ProviderError carries only a class, closed-vocabulary codes, known field
// names and derived integers. It never holds provider text.
type ProviderError struct {
	Class  ProviderErrorClass
	Status int    // provider HTTP status; 0 for an in-stream failure
	Code   string // KnownClassifiedErrorCode only
	Param  string // a known request field name only
	// RetryAfter comes from headers or numeric body fields, never from prose.
	RetryAfter   time.Duration
	ResetsAt     int64 // unix seconds; 0 when unknown
	LimitTokens  int   // context window when known; 0 when unknown
	ActualTokens int   // estimated input tokens when known; 0 when unknown
}

func (provider ProviderError) Validate() error {
	if !provider.Class.Valid() {
		return errors.New("provider error class is unsupported")
	}
	if provider.Status != 0 && (provider.Status < 100 || provider.Status > 599) {
		return errors.New("provider error status is invalid")
	}
	if KnownClassifiedErrorCode(provider.Code) != provider.Code {
		return errors.New("provider error code is not in the closed vocabulary")
	}
	if !validProviderErrorParam(provider.Param) {
		return errors.New("provider error field is invalid")
	}
	if provider.RetryAfter < 0 || provider.RetryAfter > MaxProviderRetryAfter {
		return errors.New("provider retry delay is invalid")
	}
	if provider.ResetsAt < 0 || provider.LimitTokens < 0 || provider.ActualTokens < 0 {
		return errors.New("provider error counts are negative")
	}
	return nil
}

// validProviderErrorParam admits a request field path such as
// "reasoning.effort" or "input[3].content[0].image_url", never free text.
func validProviderErrorParam(value string) bool {
	if len(value) > 128 {
		return false
	}
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9', character == '_', character == '.',
			character == '[', character == ']', character == '-':
		default:
			return false
		}
	}
	return true
}

// KnownClassifiedErrorCode is the closed vocabulary a ProviderError may
// carry: every KnownProviderErrorCode plus the codes translated rejection
// classification names. It is separate on purpose: KnownProviderErrorCode
// also reaches the client-visible error envelopes of the existing paths, so
// widening it would change what their clients receive.
func KnownClassifiedErrorCode(code string) string {
	if known := KnownProviderErrorCode(code); known != "" {
		return known
	}
	switch code {
	case "model_not_found", "invalid_api_key", "server_is_overloaded", "slow_down",
		"policy_capacity_unavailable", "usage_not_included", "content_policy_violation",
		"cyber_policy", "credit_balance_exhausted":
		return code
	default:
		return ""
	}
}

// KnownProviderErrorCode is a closed diagnostic vocabulary, not a provider
// message sanitizer. Unknown values must never enter diagnostics or storage.
// Native client delivery is separate: changing a native error changes retries.
func KnownProviderErrorCode(code string) string {
	switch code {
	case "server_error", "rate_limit_exceeded", "invalid_prompt",
		"data_residency_mismatch", "bio_policy", "vector_store_timeout",
		"invalid_image", "invalid_image_format", "invalid_base64_image",
		"invalid_image_url", "image_too_large", "image_too_small",
		"image_parse_error", "image_content_policy_violation", "invalid_image_mode",
		"image_file_too_large", "unsupported_image_media_type", "empty_image_file",
		"failed_to_download_image", "image_file_not_found",
		"context_length_exceeded", "context_window_exceeded", "invalid_encrypted_content",
		"usage_limit_reached", "insufficient_quota",
		"invalid_request_error", "authentication_error", "permission_error",
		"not_found_error", "request_too_large", "rate_limit_error",
		"api_error", "overloaded_error":
		return code
	default:
		return ""
	}
}

// ProviderErrorCode decodes only the code/type of an error object. Message,
// parameter values, identifiers, and all other provider fields are discarded.
func ProviderErrorCode(raw []byte) string {
	var wire struct {
		Code string `json:"code"`
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &wire) != nil {
		return ""
	}
	if code := KnownProviderErrorCode(wire.Code); code != "" {
		return code
	}
	return KnownProviderErrorCode(wire.Type)
}

func NewProviderFailure(path string, raw []byte) *Failure {
	failure := NewFailure(ReasonProviderResponseFailed, path, errors.New("provider reported a failed response"))
	failure.ProviderErrorCode = ProviderErrorCode(raw)
	return failure
}

// NativeProviderError is an ephemeral client-delivery value, never evidence.
// Its payload cannot be JSON-serialized or printed through normal diagnostics.
// Only the matching wire dialect may retrieve it for the downstream response.
type NativeProviderError struct {
	dialect   protocolspec.Dialect
	payload   json.RawMessage
	httpBody  json.RawMessage
	eventName string
	eventData json.RawMessage
	headers   http.Header
	// clientStatus is the HTTP status a client side chose for this error;
	// zero keeps the provider's status.
	clientStatus int
}

// WithClientStatus records the status the rendering client side requires,
// for example 400 for a recovery token its client matches only on 400.
func (native NativeProviderError) WithClientStatus(status int) NativeProviderError {
	if native.dialect != "" && status >= 400 && status <= 599 {
		native.clientStatus = status
	}
	return native
}

// ClientStatus returns the chosen status for a client of dialect, or 0.
func (native NativeProviderError) ClientStatus(dialect protocolspec.Dialect) int {
	if native.dialect != dialect {
		return 0
	}
	return native.clientStatus
}

// WithHTTPBody retains the matching error envelope, including native request
// IDs and future fields. It is bounded transient delivery, never diagnostics.
func (native NativeProviderError) WithHTTPBody(body []byte) NativeProviderError {
	if native.dialect == "" || len(native.payload) == 0 || len(body) > 64<<10 {
		return native
	}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil ||
		!bytes.Equal(bytes.TrimSpace(envelope.Error), bytes.TrimSpace(native.payload)) {
		return native
	}
	native.httpBody = bytes.Clone(body)
	return native
}

func (native NativeProviderError) HTTPBodyForDialect(dialect protocolspec.Dialect) json.RawMessage {
	if native.dialect != dialect {
		return nil
	}
	return bytes.Clone(native.httpBody)
}

// WithHeaders accepts metadata already filtered by the managed HTTP boundary.
// Like the error payload it is transient client delivery, never diagnostics.
func (native NativeProviderError) WithHeaders(headers http.Header) NativeProviderError {
	if native.dialect != "" {
		native.headers = headers.Clone()
	}
	return native
}

func (native NativeProviderError) HeadersForDialect(dialect protocolspec.Dialect) http.Header {
	if native.dialect != dialect {
		return nil
	}
	return native.headers.Clone()
}

// WithStreamEvent preserves native failure event semantics (for example the
// Responses `error` event has distinct handling from `response.failed`). Only
// failure events are admitted, never actionable output or terminal success.
func (native NativeProviderError) WithStreamEvent(name string, data []byte) NativeProviderError {
	if (name != "error" && name != "response.failed") || len(data) > 64<<10 || !json.Valid(data) {
		return native
	}
	var kind struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &kind) != nil || kind.Type != name {
		return native
	}
	native.eventName, native.eventData = name, bytes.Clone(data)
	return native
}

func (native NativeProviderError) StreamEvent(dialect protocolspec.Dialect) (string, []byte) {
	if native.dialect != dialect {
		return "", nil
	}
	return native.eventName, bytes.Clone(native.eventData)
}

func NewNativeProviderError(dialect protocolspec.Dialect, raw []byte) NativeProviderError {
	if dialect == "" || len(raw) == 0 || len(raw) > 64<<10 || !json.Valid(raw) || bytes.TrimSpace(raw)[0] != '{' {
		return NativeProviderError{}
	}
	return NativeProviderError{dialect: dialect, payload: bytes.Clone(raw)}
}

func (native NativeProviderError) ForDialect(dialect protocolspec.Dialect) json.RawMessage {
	if native.dialect != dialect {
		return nil
	}
	return bytes.Clone(native.payload)
}

func (NativeProviderError) String() string               { return "<native provider error>" }
func (NativeProviderError) GoString() string             { return "<native provider error>" }
func (NativeProviderError) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

func NewNativeProviderFailure(path string, dialect protocolspec.Dialect, raw []byte) *Failure {
	failure := NewProviderFailure(path, raw)
	failure.NativeError = NewNativeProviderError(dialect, raw)
	return failure
}

func NativeProviderErrorOf(err error) NativeProviderError {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.NativeError
	}
	return NativeProviderError{}
}

func ProviderErrorCodeOf(err error) string {
	var failure *Failure
	if errors.As(err, &failure) {
		return KnownProviderErrorCode(failure.ProviderErrorCode)
	}
	return ""
}

func NewFailure(reason Reason, path string, cause error) *Failure {
	if cause == nil {
		cause = errors.New("protocol operation failed")
	}
	return &Failure{
		Reason: reason,
		Path:   path,
		cause:  cause,
	}
}

func (failure *Failure) Error() string {
	if failure == nil {
		return "<nil>"
	}
	if failure.Path == "" {
		return fmt.Sprintf("%s: %v", failure.Reason, failure.cause)
	}
	return fmt.Sprintf("%s at %s: %v", failure.Reason, failure.Path, failure.cause)
}

func (failure *Failure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func ReasonOf(err error) Reason {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Reason
	}
	return ""
}
