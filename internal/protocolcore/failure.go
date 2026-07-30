package protocolcore

import (
	"errors"
	"fmt"
)

type Reason string

const (
	ReasonInvalidClientRequest    Reason = "invalid_client_request"
	ReasonUnsupportedClientInput  Reason = "unsupported_client_input"
	ReasonInvalidProviderResponse Reason = "invalid_provider_response"
	ReasonUnsupportedProviderData Reason = "unsupported_provider_data"
	ReasonMalformedEventStream    Reason = "malformed_event_stream"
	ReasonTruncatedEventStream    Reason = "truncated_event_stream"
	ReasonStreamStateViolation    Reason = "stream_state_violation"
	ReasonStreamLimitExceeded     Reason = "stream_limit_exceeded"
	ReasonToolCallIncomplete      Reason = "tool_call_incomplete"
	ReasonOperationCanceled       Reason = "operation_canceled"
)

type Failure struct {
	Reason Reason
	Path   string
	cause  error
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
