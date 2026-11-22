package anthropicchat

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

type anthropicStreamEncoder struct {
	requestedModel string
}

func newAnthropicStreamEncoder(
	request protocolcore.Request,
) protocolpath.ClientStreamEncoder {
	return anthropicStreamEncoder{requestedModel: request.RequestedModel}
}

func (encoder anthropicStreamEncoder) Start(
	start protocolpath.StreamStart,
) ([]byte, error) {
	var encoded bytes.Buffer
	err := appendEvent(&encoded, "message_start", struct {
		Type    string `json:"type"`
		Message struct {
			ID           string             `json:"id"`
			Type         string             `json:"type"`
			Role         string             `json:"role"`
			Model        string             `json:"model"`
			Content      []any              `json:"content"`
			StopReason   *string            `json:"stop_reason"`
			StopSequence *string            `json:"stop_sequence"`
			Usage        anthropicUsageWire `json:"usage"`
			Container    any                `json:"container"`
			StopDetails  any                `json:"stop_details"`
		} `json:"message"`
	}{
		Type: "message_start",
		Message: struct {
			ID           string             `json:"id"`
			Type         string             `json:"type"`
			Role         string             `json:"role"`
			Model        string             `json:"model"`
			Content      []any              `json:"content"`
			StopReason   *string            `json:"stop_reason"`
			StopSequence *string            `json:"stop_sequence"`
			Usage        anthropicUsageWire `json:"usage"`
			Container    any                `json:"container"`
			StopDetails  any                `json:"stop_details"`
		}{
			ID:      start.ResponseID,
			Type:    "message",
			Role:    "assistant",
			Model:   encoder.requestedModel,
			Content: []any{},
			Usage: anthropicUsageWire{
				CacheCreationInputTokens: int64Pointer(0),
				CacheReadInputTokens:     int64Pointer(0),
			},
			Container:   nil,
			StopDetails: nil,
		},
	})
	return encoded.Bytes(), err
}

func (anthropicStreamEncoder) StartText(index int) ([]byte, error) {
	var encoded bytes.Buffer
	err := appendEvent(&encoded, "content_block_start", struct {
		Type         string `json:"type"`
		Index        int    `json:"index"`
		ContentBlock struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content_block"`
	}{
		Type:  "content_block_start",
		Index: index,
		ContentBlock: struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: "text", Text: ""},
	})
	return encoded.Bytes(), err
}

func (anthropicStreamEncoder) AppendText(
	index int,
	text string,
) ([]byte, error) {
	var encoded bytes.Buffer
	err := appendEvent(&encoded, "content_block_delta", struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}{
		Type:  "content_block_delta",
		Index: index,
		Delta: struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: "text_delta", Text: text},
	})
	return encoded.Bytes(), err
}

func (anthropicStreamEncoder) StopText(index int) ([]byte, error) {
	var encoded bytes.Buffer
	err := appendEvent(&encoded, "content_block_stop", struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
	}{Type: "content_block_stop", Index: index})
	return encoded.Bytes(), err
}

func (anthropicStreamEncoder) ToolCall(
	index int,
	call protocolcore.ToolCall,
) ([]byte, error) {
	if call.EffectiveKind() != protocolcore.ToolKindFunction {
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedProviderData,
			"$.content",
			errors.New("Anthropic Messages cannot encode a custom tool call"),
		)
	}
	var encoded bytes.Buffer
	if err := appendEvent(&encoded, "content_block_start", struct {
		Type         string `json:"type"`
		Index        int    `json:"index"`
		ContentBlock struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content_block"`
	}{
		Type:  "content_block_start",
		Index: index,
		ContentBlock: struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}{
			Type:  "tool_use",
			ID:    call.Key.WireID(),
			Name:  call.Name,
			Input: json.RawMessage("{}"),
		},
	}); err != nil {
		return nil, err
	}
	if err := appendEvent(&encoded, "content_block_delta", struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Type        string `json:"type"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}{
		Type:  "content_block_delta",
		Index: index,
		Delta: struct {
			Type        string `json:"type"`
			PartialJSON string `json:"partial_json"`
		}{
			Type:        "input_json_delta",
			PartialJSON: string(call.Arguments.Bytes()),
		},
	}); err != nil {
		return nil, err
	}
	if err := appendEvent(&encoded, "content_block_stop", struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
	}{Type: "content_block_stop", Index: index}); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func (anthropicStreamEncoder) Terminal(
	response protocolcore.Response,
) ([]byte, error) {
	var encoded bytes.Buffer
	if err := appendTerminalEvents(
		&encoded,
		response.StopReason,
		response.Usage,
		nil,
	); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func (anthropicStreamEncoder) TranslationReport() protocolcore.TranslationReport {
	return protocolcore.TranslationReport{}
}

// NewClientStreamEncoder returns the Anthropic client stream encoder of a
// composed path (design 4.4, M11). For text and tool calls it writes exactly
// the events the Chat path's encoder writes. It also writes thinking blocks,
// whose signature is the reasoning's seal, sent as signature_delta just
// before content_block_stop as Anthropic does, and reports every unserved
// safeguard in the terminal message_delta. Unlike the Chat path's encoder it
// refuses calls out of order: Start first and once, one open block at a time
// with contiguous indexes from 0, and nothing after the terminal. Refusals
// are left to the caller, which renders them as text.
func NewClientStreamEncoder(request protocolcore.Request) (protocolpath.ClientStreamEncoder, error) {
	if request.RequestedModel == "" {
		return nil, errors.New("client stream encoder needs the requested model")
	}
	return &clientStreamEncoder{
		inner:      anthropicStreamEncoder{requestedModel: request.RequestedModel},
		omitted:    request.Reasoning.Display == protocolcore.ThinkingDisplayOmitted,
		safeguards: append([]protocolcore.SafeguardKind(nil), request.UnservedSafeguards...),
		open:       -1,
	}, nil
}

type clientBlockKind uint8

const (
	clientBlockText clientBlockKind = iota + 1
	clientBlockReasoning
)

type clientStreamEncoder struct {
	mu sync.Mutex

	inner      anthropicStreamEncoder
	omitted    bool
	safeguards []protocolcore.SafeguardKind

	started    bool
	terminated bool
	failed     bool
	next       int
	open       int
	openKind   clientBlockKind
	notices    []protocolcore.TranslationNotice
}

var _ protocolpath.ReasoningStreamEncoder = (*clientStreamEncoder)(nil)

func (encoder *clientStreamEncoder) violation(message string) error {
	encoder.failed = true
	return protocolcore.NewFailure(protocolcore.ReasonStreamStateViolation, "$", errors.New(message))
}

// writable checks that a content event may be written now.
func (encoder *clientStreamEncoder) writable() error {
	switch {
	case encoder.failed:
		return protocolcore.NewFailure(protocolcore.ReasonStreamStateViolation, "$", errors.New("client stream encoder already failed"))
	case !encoder.started:
		return encoder.violation("content was encoded before the stream started")
	case encoder.terminated:
		return encoder.violation("content was encoded after the terminal")
	}
	return nil
}

func (encoder *clientStreamEncoder) startBlock(index int, kind clientBlockKind) error {
	if err := encoder.writable(); err != nil {
		return err
	}
	if encoder.open >= 0 {
		return encoder.violation("a block started while another was open")
	}
	if index != encoder.next {
		return encoder.violation("block indexes are not contiguous")
	}
	encoder.next++
	encoder.open, encoder.openKind = index, kind
	return nil
}

func (encoder *clientStreamEncoder) inBlock(index int, kind clientBlockKind) error {
	if err := encoder.writable(); err != nil {
		return err
	}
	if encoder.open != index || encoder.openKind != kind {
		return encoder.violation("content was encoded for a block that is not open")
	}
	return nil
}

// result turns an inner encoder failure into a sticky one.
func (encoder *clientStreamEncoder) result(encoded []byte, err error) ([]byte, error) {
	if err != nil {
		encoder.failed = true
		return nil, err
	}
	return encoded, nil
}

func (encoder *clientStreamEncoder) Start(start protocolpath.StreamStart) ([]byte, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	if encoder.failed {
		return nil, protocolcore.NewFailure(protocolcore.ReasonStreamStateViolation, "$", errors.New("client stream encoder already failed"))
	}
	if encoder.started {
		return nil, encoder.violation("the stream was started twice")
	}
	encoder.started = true
	return encoder.result(encoder.inner.Start(start))
}

func (encoder *clientStreamEncoder) StartText(index int) ([]byte, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	if err := encoder.startBlock(index, clientBlockText); err != nil {
		return nil, err
	}
	return encoder.result(encoder.inner.StartText(index))
}

func (encoder *clientStreamEncoder) AppendText(index int, text string) ([]byte, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	if err := encoder.inBlock(index, clientBlockText); err != nil {
		return nil, err
	}
	return encoder.result(encoder.inner.AppendText(index, text))
}

func (encoder *clientStreamEncoder) StopText(index int) ([]byte, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	if err := encoder.inBlock(index, clientBlockText); err != nil {
		return nil, err
	}
	encoder.open = -1
	return encoder.result(encoder.inner.StopText(index))
}

func (encoder *clientStreamEncoder) ToolCall(index int, call protocolcore.ToolCall) ([]byte, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	if err := encoder.writable(); err != nil {
		return nil, err
	}
	if encoder.open >= 0 {
		return nil, encoder.violation("a tool call was encoded while a block was open")
	}
	if index != encoder.next {
		return nil, encoder.violation("block indexes are not contiguous")
	}
	if err := call.Validate(); err != nil {
		encoder.failed = true
		return nil, protocolcore.NewFailure(protocolcore.ReasonInvalidProviderResponse, "$.content", err)
	}
	encoder.next++
	return encoder.result(encoder.inner.ToolCall(index, call))
}

// StartReasoning opens a thinking block with empty thinking and signature,
// as Anthropic does.
func (encoder *clientStreamEncoder) StartReasoning(index int) ([]byte, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	if err := encoder.startBlock(index, clientBlockReasoning); err != nil {
		return nil, err
	}
	var encoded bytes.Buffer
	err := appendEvent(&encoded, "content_block_start", struct {
		Type         string                       `json:"type"`
		Index        int                          `json:"index"`
		ContentBlock anthropicThinkingContentWire `json:"content_block"`
	}{
		Type:         "content_block_start",
		Index:        index,
		ContentBlock: anthropicThinkingContentWire{Type: "thinking"},
	})
	return encoder.result(encoded.Bytes(), err)
}

// AppendReasoning writes a thinking_delta. An empty one is a heartbeat that
// keeps Claude Code's event watchdog alive while reasoning is not shown; with
// display omitted no reasoning text may be written.
func (encoder *clientStreamEncoder) AppendReasoning(index int, text string) ([]byte, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	if err := encoder.inBlock(index, clientBlockReasoning); err != nil {
		return nil, err
	}
	if encoder.omitted && text != "" {
		return nil, encoder.violation("reasoning text was encoded although display is omitted")
	}
	return encoder.result(encodeThinkingDelta(index, anthropicThinkingDeltaWire{Type: "thinking_delta", Thinking: text}))
}

// StopReasoning writes the seal as signature_delta, then content_block_stop.
// The reasoning text was already streamed; it is never written here. A
// redacted block cannot follow a thinking block's start, so it is refused.
func (encoder *clientStreamEncoder) StopReasoning(index int, value protocolcore.Reasoning) ([]byte, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	if err := encoder.inBlock(index, clientBlockReasoning); err != nil {
		return nil, err
	}
	if err := value.Validate(); err != nil {
		encoder.failed = true
		return nil, protocolcore.NewFailure(protocolcore.ReasonInvalidProviderResponse, "$.content", err)
	}
	if value.Redacted {
		encoder.failed = true
		return nil, protocolcore.NewFailure(protocolcore.ReasonUnsupportedProviderData, "$.content",
			errors.New("redacted reasoning cannot close a streamed thinking block"))
	}
	signature, err := anthropicSignature(value)
	if err != nil {
		encoder.failed = true
		return nil, protocolcore.NewFailure(protocolcore.ReasonUnsupportedProviderData, "$.content", err)
	}
	var encoded bytes.Buffer
	if signature != "" {
		delta, err := encodeThinkingDelta(index, anthropicSignatureDeltaWire{Type: "signature_delta", Signature: signature})
		if err != nil {
			encoder.failed = true
			return nil, err
		}
		encoded.Write(delta)
	}
	stop, err := encoder.inner.StopText(index)
	if err != nil {
		encoder.failed = true
		return nil, err
	}
	encoded.Write(stop)
	encoder.open = -1
	return encoded.Bytes(), nil
}

// Terminal writes message_delta and message_stop: the mapped stop reason,
// the usage, and the unserved safeguards.
func (encoder *clientStreamEncoder) Terminal(response protocolcore.Response) ([]byte, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	if err := encoder.writable(); err != nil {
		return nil, err
	}
	if encoder.open >= 0 {
		return nil, encoder.violation("the terminal was encoded while a block was open")
	}
	if err := response.StopReason.Validate(); err != nil {
		encoder.failed = true
		return nil, protocolcore.NewFailure(protocolcore.ReasonInvalidProviderResponse, "$.stop_reason", err)
	}
	encoder.terminated = true
	if _, approximated := anthropicStopReason(response.StopReason); approximated {
		encoder.notices = append(encoder.notices, protocolcore.TranslationNotice{
			Code: protocolcore.NoticeStopReasonApproximated,
			Path: "$.stop_reason",
		})
	}
	var encoded bytes.Buffer
	err := appendTerminalEvents(&encoded, response.StopReason, response.Usage, encoder.safeguards)
	return encoder.result(encoded.Bytes(), err)
}

func (encoder *clientStreamEncoder) TranslationReport() protocolcore.TranslationReport {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	return protocolcore.NewTranslationReport(encoder.notices...)
}

type anthropicThinkingDeltaWire struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
}

type anthropicSignatureDeltaWire struct {
	Type      string `json:"type"`
	Signature string `json:"signature"`
}

// encodeThinkingDelta writes one content_block_delta of a thinking block.
func encodeThinkingDelta(index int, delta any) ([]byte, error) {
	var encoded bytes.Buffer
	err := appendEvent(&encoded, "content_block_delta", struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta any    `json:"delta"`
	}{Type: "content_block_delta", Index: index, Delta: delta})
	return encoded.Bytes(), err
}
