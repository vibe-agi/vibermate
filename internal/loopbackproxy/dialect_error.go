package loopbackproxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

// ReasonHeader carries the stable vibermate reason code beside a dialect-shaped
// body. The client parses the body with its own provider schema, so the reason
// code may never replace that shape.
const ReasonHeader = "X-Vibermate-Reason"

// reasonMessages are fixed, non-echoing sentences. A rejection message never
// interpolates request content, so no client payload can travel back out
// through an error body.
var reasonMessages = map[ReasonCode]string{
	ReasonEnvironmentOperationUnsupported: "This API operation is not available " +
		"through vibermate for the selected upstream plan.",
	ReasonUnsupportedUpgrade: "vibermate cannot serve this protocol upgrade " +
		"on this connection.",
	ReasonRawEvidenceUnavailable: "vibermate could not preserve the configured " +
		"request evidence, so no provider request was sent.",
}

func reasonMessage(reason ReasonCode) string {
	if message, found := reasonMessages[reason]; found {
		return message
	}
	return "vibermate rejected this request locally."
}

// writeDialectReason emits a local policy rejection using the error envelope
// of the dialect the client believes it is talking to. A client that only
// understands its provider's error schema must be able to classify the
// rejection instead of failing on an unparseable body.
func writeDialectReason(
	writer http.ResponseWriter,
	dialect protocolspec.Dialect,
	status int,
	reason ReasonCode,
) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set(ReasonHeader, string(reason))
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(
		dialectErrorEnvelope(dialect, reason),
	)
}

func dialectErrorEnvelope(dialect protocolspec.Dialect, reason ReasonCode) any {
	message := reasonMessage(reason)
	switch dialect {
	case protocolspec.DialectOpenAIResponses:
		type openAIError struct {
			Message string  `json:"message"`
			Type    string  `json:"type"`
			Param   *string `json:"param"`
			Code    string  `json:"code"`
		}
		return struct {
			Error openAIError `json:"error"`
		}{
			Error: openAIError{
				Message: message,
				Type:    "invalid_request_error",
				Code:    string(reason),
			},
		}
	default:
		type anthropicError struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		return struct {
			Type  string         `json:"type"`
			Error anthropicError `json:"error"`
		}{
			Type: "error",
			Error: anthropicError{
				Type:    "invalid_request_error",
				Message: message,
			},
		}
	}
}

// writeExchangeFailure preserves matching native provider errors for the client;
// locally generated failures use a fixed, dialect-shaped envelope. It never
// serializes err: an Exchange error may wrap transport or provider details that
// are not part of the client contract. The stable reason remains visible in a
// response header and in the bounded message/code fields clients already know
// how to render.
func writeExchangeFailure(
	writer http.ResponseWriter,
	dialect protocolspec.Dialect,
	err error,
) {
	reason := exchange.ReasonOf(err)
	if reason == "" {
		reason = exchange.ReasonProviderTransportFailed
	}
	for name, values := range exchangeFailureMetadata(dialect, err) {
		writer.Header()[name] = values
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set(ReasonHeader, string(reason))
	if exchangeFailureShouldNotRetry(reason) {
		// The Anthropic SDK recognizes this as a terminal configuration failure.
		// It must not turn a missing managed-route credential into a second model
		// request merely because a generic 5xx looks transient.
		writer.Header().Set("X-Should-Retry", "false")
	}
	writer.WriteHeader(exchangeStatus(err))
	_ = json.NewEncoder(writer).Encode(
		exchangeFailureEnvelope(dialect, reason, err),
	)
}

// writeExchangeFailureDownstream keeps locally generated Exchange failures on
// the same client-downstream evidence boundary as provider-backed responses.
func writeExchangeFailureDownstream(
	ctx context.Context,
	downstream exchange.Downstream,
	dialect protocolspec.Dialect,
	err error,
) error {
	reason := exchange.ReasonOf(err)
	if reason == "" {
		reason = exchange.ReasonProviderTransportFailed
	}
	headers := http.Header{
		"Content-Type":  {"application/json"},
		"Cache-Control": {"no-store"},
		ReasonHeader:    {string(reason)},
	}
	for name, values := range exchangeFailureMetadata(dialect, err) {
		headers[name] = values
	}
	if exchangeFailureShouldNotRetry(reason) {
		headers.Set("X-Should-Retry", "false")
	}
	envelope, envelopeErr := exchange.NewResponseEnvelope(
		exchange.ResponseModeJSON,
		exchangeStatus(err),
		headers,
	)
	if envelopeErr != nil {
		return envelopeErr
	}
	body, marshalErr := json.Marshal(exchangeFailureEnvelope(dialect, reason, err))
	if marshalErr != nil {
		return marshalErr
	}
	body = append(body, '\n')
	if beginErr := downstream.Begin(ctx, envelope); beginErr != nil {
		return beginErr
	}
	written, writeErr := downstream.Write(ctx, body)
	if writeErr != nil {
		return writeErr
	}
	if written != len(body) {
		return io.ErrShortWrite
	}
	return nil
}

func exchangeFailureMetadata(dialect protocolspec.Dialect, err error) http.Header {
	var failure *exchange.Failure
	if errors.As(err, &failure) {
		return failure.NativeError.HeadersForDialect(dialect)
	}
	return nil
}

func exchangeFailureEnvelope(dialect protocolspec.Dialect, reason exchange.ReasonCode, err error) any {
	var failure *exchange.Failure
	if errors.As(err, &failure) {
		if native := failure.NativeError.ForDialect(dialect); len(native) > 0 {
			return map[string]any{"error": native}
		}
	}
	return exchangeErrorEnvelope(dialect, reason, providerCodeOf(err))
}

func exchangeErrorEnvelope(
	dialect protocolspec.Dialect,
	reason exchange.ReasonCode,
	providerCode string,
) any {
	message := exchangeReasonMessage(reason)
	code := protocolcore.KnownProviderErrorCode(providerCode)
	errorType := exchangeErrorType(reason)
	if code != "" {
		message += " Upstream error: " + code + "."
		// Only Anthropic's documented error types belong in its type field.
		switch code {
		case "invalid_request_error", "authentication_error", "permission_error", "not_found_error", "request_too_large", "rate_limit_error", "api_error", "overloaded_error":
			errorType = code
		}
	} else {
		code = string(reason)
	}
	switch dialect {
	case protocolspec.DialectOpenAIResponses:
		type openAIError struct {
			Message string  `json:"message"`
			Type    string  `json:"type"`
			Param   *string `json:"param"`
			Code    string  `json:"code"`
		}
		return struct {
			Error openAIError `json:"error"`
		}{
			Error: openAIError{
				Message: message,
				Type:    errorType,
				Code:    code,
			},
		}
	default:
		type anthropicError struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		return struct {
			Type  string         `json:"type"`
			Error anthropicError `json:"error"`
		}{
			Type: "error",
			Error: anthropicError{
				Type:    errorType,
				Message: message,
			},
		}
	}
}

func providerCodeOf(err error) string {
	var failure *exchange.Failure
	if errors.As(err, &failure) {
		return protocolcore.KnownProviderErrorCode(failure.ProviderErrorCode)
	}
	return ""
}

func exchangeErrorType(reason exchange.ReasonCode) string {
	switch reason {
	case exchange.ReasonProviderCredentialUnavailable:
		return "authentication_error"
	case exchange.ReasonInvalidExchangeRequest,
		exchange.ReasonUnsupportedClientInput:
		return "invalid_request_error"
	default:
		return "api_error"
	}
}

// Streaming failures need the same dialect boundary as ordinary HTTP errors.
// In particular Codex handles response.failed, not our old custom error event.
// Local failures use fixed classified messages; compatible native failures
// retain their client semantics separately from privacy-safe diagnostics.
func encodeExchangeStreamFailure(dialect protocolspec.Dialect, notice exchange.FailureNotice) ([]byte, error) {
	if name, data := notice.NativeError.StreamEvent(dialect); name != "" {
		return ssewire.Encode(ssewire.Event{Name: name, Data: data})
	}
	name := "error"
	payload := exchangeErrorEnvelope(dialect, notice.ReasonCode, notice.ProviderErrorCode)
	switch dialect {
	case protocolspec.DialectAnthropicMessages:
	case protocolspec.DialectOpenAIResponses:
		name = "response.failed"
		message := exchangeReasonMessage(notice.ReasonCode)
		code := protocolcore.KnownProviderErrorCode(notice.ProviderErrorCode)
		if code == "" {
			code = "server_error"
		} else {
			message += " Upstream error: " + code + "."
		}
		if notice.ProtocolReason != "" {
			message += " Protocol: " + string(notice.ProtocolReason) + "."
		}
		var clientError any = map[string]string{"code": code, "message": message}
		if native := notice.NativeError.ForDialect(dialect); len(native) > 0 {
			clientError = native
		}
		payload = map[string]any{
			"type": name,
			"response": map[string]any{
				"object": "response", "status": "failed", "output": []any{},
				"error": clientError,
			},
		}
	default:
		return nil, errors.New("unsupported streaming failure dialect")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return ssewire.Encode(ssewire.Event{Name: name, Data: data})
}

func exchangeReasonMessage(reason exchange.ReasonCode) string {
	switch reason {
	case exchange.ReasonProviderCredentialUnavailable:
		return "ViberMate has no provider credential configured for the selected route (" +
			string(reason) + ")."
	case exchange.ReasonMessageTransformFailed:
		return "ViberMate could not apply the configured message transform (" +
			string(reason) + ")."
	default:
		return "ViberMate could not complete this request (" + string(reason) + ")."
	}
}

func exchangeFailureShouldNotRetry(reason exchange.ReasonCode) bool {
	switch reason {
	case exchange.ReasonProviderCredentialUnavailable,
		exchange.ReasonMessageTransformFailed:
		return true
	default:
		return false
	}
}

// drainBounded discards at most limit bytes so an HTTP/1.1 connection stays
// reusable after a rejection. The bytes are never retained, so a rejected
// request body cannot reach a buffer, log, error value, or record.
func drainBounded(reader io.Reader, limit int64) {
	if reader == nil || limit <= 0 {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(reader, limit))
}
