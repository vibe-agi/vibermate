package main

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// upstreamError is the provider's error object, reduced to what the design
// needs: type, code, param and whether the message has numbers. The message
// itself is kept truncated, and not at all on 401 and 403, whose message
// can echo the key.
type upstreamError struct {
	InBand            bool    `json:"in_band"`
	Event             string  `json:"event,omitempty"`
	Type              string  `json:"type,omitempty"`
	Code              string  `json:"code,omitempty"`
	Param             string  `json:"param,omitempty"`
	MessageHasNumbers bool    `json:"message_has_numbers"`
	MessageNumbers    []int64 `json:"message_numbers,omitempty"`
	Message           string  `json:"message,omitempty"`
	MessageOmitted    string  `json:"message_omitted,omitempty"`
}

// classification is what ViberMate's real classifier and Anthropic renderer
// make of a rejection (design 8.2, 8.3): what Claude Code would be shown.
type classification struct {
	Class             string `json:"class"`
	Code              string `json:"code,omitempty"`
	Param             string `json:"param,omitempty"`
	LimitTokens       int    `json:"limit_tokens,omitempty"`
	ActualTokens      int    `json:"actual_tokens,omitempty"`
	RetryAfterSeconds int64  `json:"retry_after_seconds,omitempty"`
	ClientStatus      int    `json:"client_status,omitempty"`
	ClientErrorType   string `json:"client_error_type,omitempty"`
	ClientMessage     string `json:"client_message,omitempty"`
}

// usageReport is the terminal's usage; a nil field was absent.
type usageReport struct {
	InputTokens      *int64 `json:"input_tokens"`
	CachedTokens     *int64 `json:"cached_tokens"`
	CacheWriteTokens *int64 `json:"cache_write_tokens,omitempty"`
	OutputTokens     *int64 `json:"output_tokens"`
	ReasoningTokens  *int64 `json:"reasoning_tokens"`
}

// translation is what the registered path made of the real stream: the
// stream Claude Code would have received.
type translation struct {
	OK         bool     `json:"ok"`
	Error      string   `json:"error,omitempty"`
	StopReason string   `json:"stop_reason,omitempty"`
	Blocks     []string `json:"client_blocks,omitempty"`
	ToolCalls  int      `json:"tool_calls,omitempty"`
	Notices    []string `json:"notices,omitempty"`
}

var messageNumber = regexp.MustCompile(`\d[\d,]*`)

func (r *runner) upstreamMessage(target *upstreamError, status int, message string) {
	// Numbers are read after the key is removed, so no digit of it can
	// reach the report.
	message = keyShapes.ReplaceAllString(r.scrub.string(message), redacted)
	numbers := messageNumber.FindAllString(message, -1)
	target.MessageHasNumbers = len(numbers) != 0
	for _, number := range numbers {
		if len(target.MessageNumbers) == 8 {
			break
		}
		if value, err := strconv.ParseInt(strings.ReplaceAll(number, ",", ""), 10, 64); err == nil {
			target.MessageNumbers = append(target.MessageNumbers, value)
		}
	}
	if status == 401 || status == 403 {
		if message != "" {
			target.MessageOmitted = "not kept on 401 and 403, where it can echo the key"
		}
		return
	}
	target.Message = r.scrub.upstream(message, 300)
}

// lenientString reads a JSON string, a number or null as text.
func lenientString(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	if trimmed := strings.TrimSpace(string(raw)); trimmed != "" && trimmed != "null" {
		return trimmed
	}
	return ""
}

type wireError struct {
	Message json.RawMessage `json:"message"`
	Type    json.RawMessage `json:"type"`
	Param   json.RawMessage `json:"param"`
	Code    json.RawMessage `json:"code"`
}

func (r *runner) errorFrom(wire wireError, status int, inBand bool, event string) *upstreamError {
	result := &upstreamError{
		InBand: inBand,
		Event:  event,
		Type:   r.scrub.upstream(lenientString(wire.Type), 80),
		Code:   r.scrub.upstream(lenientString(wire.Code), 80),
		Param:  r.scrub.upstream(lenientString(wire.Param), 120),
	}
	r.upstreamMessage(result, status, lenientString(wire.Message))
	return result
}

func (r *runner) httpError(status int, body []byte) *upstreamError {
	var envelope struct {
		Error  *wireError      `json:"error"`
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		if envelope.Error != nil {
			return r.errorFrom(*envelope.Error, status, false, "")
		}
		if detail := lenientString(envelope.Detail); detail != "" {
			return r.errorFrom(wireError{Message: envelope.Detail}, status, false, "")
		}
	}
	result := &upstreamError{Type: "(not a JSON error object)"}
	r.upstreamMessage(result, status, string(body))
	return result
}

// readEvents reads the terminal, its usage and any in-band error.
func (r *runner) readEvents(result *probeResult, record *exchangeRecord) {
	for _, event := range record.events {
		switch event.name {
		case "response.completed", "response.incomplete", "response.failed":
			result.Terminal = event.name
			var wire struct {
				Response struct {
					Status            string `json:"status"`
					IncompleteDetails *struct {
						Reason string `json:"reason"`
					} `json:"incomplete_details"`
					Error *wireError `json:"error"`
					Usage *struct {
						InputTokens        *int64 `json:"input_tokens"`
						InputTokensDetails *struct {
							CachedTokens     *int64 `json:"cached_tokens"`
							CacheWriteTokens *int64 `json:"cache_write_tokens"`
						} `json:"input_tokens_details"`
						OutputTokens        *int64 `json:"output_tokens"`
						OutputTokensDetails *struct {
							ReasoningTokens *int64 `json:"reasoning_tokens"`
						} `json:"output_tokens_details"`
					} `json:"usage"`
				} `json:"response"`
			}
			if json.Unmarshal(event.data, &wire) != nil {
				continue
			}
			result.ResponseStatus = r.scrub.upstream(wire.Response.Status, 40)
			if details := wire.Response.IncompleteDetails; details != nil {
				result.IncompleteReason = r.scrub.upstream(details.Reason, 80)
			}
			if wire.Response.Error != nil {
				result.Error = r.errorFrom(*wire.Response.Error, 0, true, event.name)
			}
			if usage := wire.Response.Usage; usage != nil {
				report := &usageReport{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens}
				if details := usage.InputTokensDetails; details != nil {
					report.CachedTokens, report.CacheWriteTokens = details.CachedTokens, details.CacheWriteTokens
				}
				if details := usage.OutputTokensDetails; details != nil {
					report.ReasoningTokens = details.ReasoningTokens
				}
				result.Usage = report
			}
		case "error":
			var wire struct {
				wireError
				Error *wireError `json:"error"`
			}
			if json.Unmarshal(event.data, &wire) != nil {
				continue
			}
			inner := wire.wireError
			if wire.Error != nil {
				inner = *wire.Error
			}
			if result.Error == nil {
				result.Error = r.errorFrom(inner, 0, true, "error")
			}
		}
	}
}

// translate feeds the recorded stream through the registered path, as the
// Exchange would, and keeps the client bytes for a replayed turn.
func (r *runner) translate(ctx context.Context, result *probeResult, record *exchangeRecord) {
	b := result.built
	report := &translation{}
	result.Translation = report
	fail := func(err error) {
		report.Error = r.scrub.upstream(err.Error(), 300)
		var failure *protocolcore.Failure
		if errors.As(err, &failure) && failure.Provider != nil && result.Classification == nil {
			result.Classification = r.classify(b, *failure.Provider)
		}
	}
	stream, err := b.path.NewStreamFor(b.context, b.request)
	if err != nil {
		fail(err)
		return
	}
	live, err := stream.Feed(ctx, record.stream)
	if err != nil {
		fail(err)
		return
	}
	if !stream.TerminalReceived() {
		report.Error = "the stream ended without a terminal event"
		return
	}
	terminal, err := stream.FinishDecoded(ctx)
	if err != nil {
		fail(err)
		return
	}
	release, err := terminal.Approve()
	if err != nil {
		fail(err)
		return
	}
	result.clientStream = append(live, release...)
	decoded := terminal.DecodedResponse()
	report.OK = true
	report.StopReason = string(decoded.StopReason)
	report.ToolCalls = len(terminal.ToolIntents())
	for _, block := range decoded.Blocks {
		report.Blocks = append(report.Blocks, string(block.Kind))
	}
	for _, notice := range terminal.TranslationReport().Notices() {
		report.Notices = append(report.Notices, string(notice.Code)+" "+notice.Path)
	}
}

func (r *runner) classify(b *built, provider protocolcore.ProviderError) *classification {
	result := &classification{
		Class:             string(provider.Class),
		Code:              provider.Code,
		Param:             provider.Param,
		LimitTokens:       provider.LimitTokens,
		ActualTokens:      provider.ActualTokens,
		RetryAfterSeconds: int64(provider.RetryAfter.Seconds()),
	}
	native, err := b.path.RenderProviderError(b.request, provider)
	if err != nil {
		return result
	}
	result.ClientStatus = native.ClientStatus(protocolspec.DialectAnthropicMessages)
	// The payload is Anthropic's error object; an HTTP body wraps it.
	var rendered struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Error   *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(native.ForDialect(protocolspec.DialectAnthropicMessages), &rendered) == nil {
		if rendered.Error != nil {
			rendered.Type, rendered.Message = rendered.Error.Type, rendered.Error.Message
		}
		result.ClientErrorType = rendered.Type
		result.ClientMessage = r.scrub.upstream(rendered.Message, 300)
	}
	return result
}

// observe turns an exchange into the probe's findings.
func (r *runner) observe(ctx context.Context, result *probeResult, record *exchangeRecord) {
	result.exchange = record
	result.Status = record.status
	result.TransportError = record.transportError
	result.Timing = record.timing()
	result.Headers = r.scrub.allowedHeaders(record.header)
	result.Cancelled = record.cancelled
	switch {
	case record.status >= 400:
		result.Error = r.httpError(record.status, record.body)
		if b := result.built; b != nil {
			if _, provider, ok := b.path.TranslateProviderRejection(b.request, record.status, record.header, record.body); ok {
				result.Classification = r.classify(b, provider)
			}
		}
	case record.status == 200 && len(record.events) != 0:
		r.readEvents(result, record)
		if result.built != nil && !record.cancelled && record.transportError == "" {
			r.translate(ctx, result, record)
		}
	}
}
