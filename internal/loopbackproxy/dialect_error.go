package loopbackproxy

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/access"
)

// ReasonHeader carries the stable vibermate reason code beside a dialect-shaped
// body. The client parses the body with its own provider schema, so the reason
// code may never replace that shape.
const ReasonHeader = "X-Vibermate-Reason"

// reasonMessages are fixed, non-echoing sentences. A rejection message never
// interpolates request content, so no client payload can travel back out
// through an error body.
var reasonMessages = map[ReasonCode]string{
	ReasonProfileOperationUnsupported: "This API operation is not available " +
		"through vibermate for the selected upstream plan.",
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
	dialect access.Dialect,
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

func dialectErrorEnvelope(dialect access.Dialect, reason ReasonCode) any {
	message := reasonMessage(reason)
	switch dialect {
	case access.DialectOpenAIResponses:
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

// drainBounded discards at most limit bytes so an HTTP/1.1 connection stays
// reusable after a rejection. The bytes are never retained, so a rejected
// request body cannot reach a buffer, log, error value, or record.
func drainBounded(reader io.Reader, limit int64) {
	if reader == nil || limit <= 0 {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(reader, limit))
}
