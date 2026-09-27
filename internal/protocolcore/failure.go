package protocolcore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

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
	cause             error
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
	eventName string
	eventData json.RawMessage
	headers   http.Header
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
	if len(raw) == 0 || len(raw) > 64<<10 || !json.Valid(raw) || bytes.TrimSpace(raw)[0] != '{' {
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
