package anthropicchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

type messagesStreamBlock struct {
	kind               string
	text               bytes.Buffer
	id                 string
	name               string
	initialInput       json.RawMessage
	partialInput       bytes.Buffer
	extensionFragments [][]byte
	stopped            bool
	// A block this dialect does not model is a provider action: its native
	// start and every delta are kept (bounded) for the tool decision.
	action       bool
	actionStart  json.RawMessage
	actionDeltas []json.RawMessage
	actionBytes  int
}

// AnthropicProviderStream inventories native SSE while streaming text and
// provider-side evidence. The first client tool call and subsequent events stay
// behind the terminal tool decision, so no client action crosses before approval.
type AnthropicProviderStream struct {
	mu sync.Mutex

	codec   *Codec
	request protocolcore.Request
	decoder *ssewire.Decoder
	wire    bytes.Buffer
	held    bytes.Buffer

	responseID       string
	reportedModel    string
	usage            messagesUsageWire
	stopReason       string
	stopSequence     *string
	blocks           map[int]*messagesStreamBlock
	messageStarted   bool
	barrier          bool
	done             bool
	failed           bool
	finished         bool
	semanticProgress uint64
}

func (codec *Codec) NewAnthropicProviderStream(
	request protocolcore.Request,
) (*AnthropicProviderStream, error) {
	if err := request.Validate(); err != nil {
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$",
			err,
		)
	}
	if !request.Stream {
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$.stream",
			errors.New("request is not configured for streaming"),
		)
	}
	decoder, err := ssewire.NewDecoder(codec.options.SSE)
	if err != nil {
		return nil, err
	}
	return &AnthropicProviderStream{
		codec:   codec,
		request: request.Clone(),
		decoder: decoder,
		blocks:  make(map[int]*messagesStreamBlock),
	}, nil
}

func (stream *AnthropicProviderStream) Feed(
	ctx context.Context,
	fragment []byte,
) ([]byte, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if err := stream.checkMutable(ctx); err != nil {
		return nil, err
	}
	if len(fragment) == 0 {
		return nil, nil
	}
	if stream.wire.Len()+len(fragment) > stream.codec.options.MaxResponseBytes {
		stream.failed = true
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonStreamLimitExceeded,
			"$",
			errors.New("Anthropic-compatible stream exceeds the response byte limit"),
		)
	}
	_, _ = stream.wire.Write(fragment)
	events, err := stream.decoder.Feed(fragment)
	if err != nil {
		stream.failed = true
		return nil, wrapSSEError(err)
	}
	var safe bytes.Buffer
	for index, event := range events {
		if err := contextError(ctx); err != nil {
			stream.failed = true
			return nil, err
		}
		if stream.done {
			stream.failed = true
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonStreamStateViolation,
				fmt.Sprintf("$event[%d]", index),
				errors.New("event arrived after message_stop"),
			)
		}
		if err := stream.consumeEvent(event, index); err != nil {
			stream.failed = true
			// Read boundaries are arbitrary. Preserve earlier safe events from
			// this chunk; bytes behind a tool barrier remain withheld on failure.
			return bytes.Clone(safe.Bytes()), err
		}
		destination := &safe
		if stream.barrier {
			destination = &stream.held
		}
		clientEvent, err := stream.clientEvent(event)
		if err != nil {
			stream.failed = true
			return nil, err
		}
		if err := appendCompatibleEvent(destination, clientEvent); err != nil {
			stream.failed = true
			return nil, err
		}
	}
	// Text and non-actionable content can stream immediately. The event that
	// introduces the first tool call, plus every event after it, remains behind
	// the complete-tool approval barrier.
	return bytes.Clone(safe.Bytes()), nil
}

func (stream *AnthropicProviderStream) clientEvent(
	event ssewire.Event,
) (ssewire.Event, error) {
	if stream.request.RequestedModel == stream.request.EffectiveModel {
		return event, nil
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(event.Data, &envelope) != nil || envelope.Type != "message_start" {
		return event, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(event.Data, &root); err != nil || root == nil {
		return ssewire.Event{}, errors.New("Anthropic message_start is invalid")
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(root["message"], &message); err != nil || message == nil {
		return ssewire.Event{}, errors.New("Anthropic message_start message is invalid")
	}
	var reportedModel string
	if err := json.Unmarshal(message["model"], &reportedModel); err != nil ||
		reportedModel == "" {
		return ssewire.Event{}, errors.New("Anthropic message_start model is invalid")
	}
	if reportedModel == stream.request.RequestedModel {
		return event, nil
	}
	model, err := json.Marshal(stream.request.RequestedModel)
	if err != nil {
		return ssewire.Event{}, err
	}
	message["model"] = model
	encodedMessage, err := json.Marshal(message)
	if err != nil {
		return ssewire.Event{}, err
	}
	root["message"] = encodedMessage
	encodedEvent, err := json.Marshal(root)
	if err != nil {
		return ssewire.Event{}, err
	}
	rewritten := event
	rewritten.Data = encodedEvent
	return rewritten, nil
}

func (stream *AnthropicProviderStream) SemanticProgress() uint64 {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.semanticProgress
}

func (stream *AnthropicProviderStream) TerminalReceived() bool {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.done && !stream.failed
}

func (stream *AnthropicProviderStream) FinishDecoded(
	ctx context.Context,
) (protocolpath.PendingTerminal, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if err := stream.checkMutable(ctx); err != nil {
		return nil, err
	}
	stream.finished = true
	if err := stream.decoder.Finish(); err != nil {
		stream.failed = true
		return nil, wrapSSEError(err)
	}
	if !stream.messageStarted || !stream.done {
		stream.failed = true
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonStreamStateViolation,
			"$",
			errors.New("Anthropic-compatible stream has no complete message"),
		)
	}
	wire := messagesProviderResponseWire{
		ID:           stream.responseID,
		Type:         "message",
		Role:         "assistant",
		Model:        stream.reportedModel,
		StopReason:   stream.stopReason,
		StopSequence: stream.stopSequence,
		Usage:        stream.usage,
	}
	indices := make([]int, 0, len(stream.blocks))
	for index := range stream.blocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	actionDeltas := make(map[int][]json.RawMessage)
	for _, index := range indices {
		block := stream.blocks[index]
		if !block.stopped {
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonStreamStateViolation,
				fmt.Sprintf("$.content[%d]", index),
				errors.New("content block did not stop"),
			)
		}
		var encoded []byte
		var err error
		switch block.kind {
		case "text":
			encoded, err = json.Marshal(struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{Type: "text", Text: block.text.String()})
		case "tool_use":
			input := bytes.TrimSpace(block.partialInput.Bytes())
			if len(input) == 0 {
				input = bytes.TrimSpace(block.initialInput)
			}
			if len(input) == 0 {
				input = []byte("{}")
			}
			if !json.Valid(input) {
				return nil, protocolcore.NewFailure(
					protocolcore.ReasonToolCallIncomplete,
					fmt.Sprintf("$.content[%d].input", index),
					errors.New("tool input is incomplete JSON"),
				)
			}
			encoded, err = json.Marshal(struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}{Type: "tool_use", ID: block.id, Name: block.name, Input: input})
		default:
			if !block.action {
				continue
			}
			actionDeltas[len(wire.Content)] = block.actionDeltas
			encoded = block.actionStart
		}
		if err != nil {
			return nil, err
		}
		wire.Content = append(wire.Content, encoded)
	}
	response, err := decodeMessagesResponse(
		stream.request,
		wire,
		stream.codec.options.MaxToolArgumentBytes,
		actionDeltas,
	)
	if err != nil {
		return nil, err
	}
	for _, index := range indices {
		block := stream.blocks[index]
		kind, recognized := messagesExtensionKind(block.kind)
		if !recognized {
			continue
		}
		extension, extensionErr := protocolcore.NewProviderExtension(
			protocolcore.ProviderExtensionSourceAnthropicMessages,
			kind,
			fmt.Sprintf("$.content[%d]", index),
			block.extensionFragments,
		)
		if extensionErr != nil {
			return nil, messagesProviderFailure(fmt.Sprintf("$.content[%d]", index), extensionErr)
		}
		response.ProviderExtensions = append(response.ProviderExtensions, extension)
	}
	if err := response.Validate(); err != nil {
		return nil, messagesProviderFailure("$", err)
	}
	intents := make([]protocolcore.ToolIntent, 0)
	for _, block := range response.Blocks {
		if block.Kind != protocolcore.BlockToolCall {
			continue
		}
		intent := protocolcore.ToolIntent{
			ResponseID: response.ID,
			Ordinal:    len(intents),
			Call:       block.ToolCall.Clone(),
		}
		if err := intent.Validate(); err != nil {
			return nil, err
		}
		intents = append(intents, intent)
	}
	return newPendingTerminal(
		stream.held.Bytes(),
		response,
		intents,
		protocolcore.TranslationReport{},
	), nil
}

func (stream *AnthropicProviderStream) checkMutable(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if stream.failed {
		return errors.New("Anthropic-compatible stream has failed")
	}
	if stream.finished {
		return ErrStreamAlreadyFinished
	}
	return nil
}

func (stream *AnthropicProviderStream) consumeEvent(
	event ssewire.Event,
	index int,
) error {
	var envelope struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	// Checked before any field is read: every later decision must see the
	// same members the client will parse.
	if err := rejectDuplicateJSONNames(event.Data); err != nil {
		return protocolcore.NewFailure(
			protocolcore.ReasonMalformedEventStream,
			fmt.Sprintf("$event[%d].data", index),
			err,
		)
	}
	if err := json.Unmarshal(event.Data, &envelope); err != nil {
		return protocolcore.NewFailure(
			protocolcore.ReasonMalformedEventStream,
			fmt.Sprintf("$event[%d].data", index),
			err,
		)
	}
	if event.Name != "message" && event.Name != envelope.Type {
		return protocolcore.NewFailure(
			protocolcore.ReasonMalformedEventStream,
			fmt.Sprintf("$event[%d].event", index),
			errors.New("SSE event name does not match its payload type"),
		)
	}
	switch envelope.Type {
	case "ping":
		// The provider's keepalive while it is still producing the message
		// (for example during long thinking). It renews stream liveness.
		stream.semanticProgress++
		return nil
	case "error":
		failure := protocolcore.NewNativeProviderFailure(
			fmt.Sprintf("$event[%d]", index), protocolspec.DialectAnthropicMessages, envelope.Error,
		)
		failure.NativeError = failure.NativeError.WithStreamEvent("error", event.Data)
		return failure
	case "message_start":
		if stream.messageStarted {
			return stream.stateFailure(index, "message_start is duplicated")
		}
		var payload struct {
			Message struct {
				ID    string            `json:"id"`
				Type  string            `json:"type"`
				Role  string            `json:"role"`
				Model string            `json:"model"`
				Usage messagesUsageWire `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return stream.eventFailure(index, err)
		}
		if payload.Message.Type != "message" || payload.Message.Role != "assistant" {
			return stream.stateFailure(index, "message_start identity is invalid")
		}
		stream.responseID = payload.Message.ID
		stream.reportedModel = payload.Message.Model
		stream.usage = payload.Message.Usage
		stream.messageStarted = true
	case "content_block_start":
		if !stream.messageStarted {
			return stream.stateFailure(index, "content block arrived before message_start")
		}
		var payload struct {
			Index        int             `json:"index"`
			ContentBlock json.RawMessage `json:"content_block"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return stream.eventFailure(index, err)
		}
		if payload.Index < 0 {
			return stream.stateFailure(index, "content block index is negative")
		}
		if _, duplicate := stream.blocks[payload.Index]; duplicate {
			return stream.stateFailure(index, "content block index is duplicated")
		}
		var blockHeader struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload.ContentBlock, &blockHeader); err != nil {
			return stream.eventFailure(index, err)
		}
		block := &messagesStreamBlock{kind: blockHeader.Type}
		switch blockHeader.Type {
		case "text":
			var content struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(payload.ContentBlock, &content); err != nil {
				return stream.eventFailure(index, err)
			}
			_, _ = block.text.WriteString(content.Text)
		case "tool_use":
			var content struct {
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(payload.ContentBlock, &content); err != nil {
				return stream.eventFailure(index, err)
			}
			block.id = content.ID
			block.name = content.Name
			block.initialInput = bytes.Clone(content.Input)
			stream.barrier = true
		default:
			if _, recognized := messagesExtensionKind(blockHeader.Type); !recognized {
				block.action = true
				block.actionStart = bytes.Clone(payload.ContentBlock)
				block.actionBytes = len(payload.ContentBlock)
				stream.barrier = true
				break
			}
			block.extensionFragments = append(block.extensionFragments, bytes.Clone(payload.ContentBlock))
		}
		stream.blocks[payload.Index] = block
	case "content_block_delta":
		var payload struct {
			Index int `json:"index"`
			Delta struct {
				Type        string          `json:"type"`
				Text        string          `json:"text"`
				PartialJSON string          `json:"partial_json"`
				Thinking    string          `json:"thinking,omitempty"`
				Signature   string          `json:"signature,omitempty"`
				Citation    json.RawMessage `json:"citation,omitempty"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return stream.eventFailure(index, err)
		}
		block, exists := stream.blocks[payload.Index]
		if !exists || block.stopped {
			return stream.stateFailure(index, "content delta has no open block")
		}
		if block.action {
			var raw struct {
				Delta json.RawMessage `json:"delta"`
			}
			if err := json.Unmarshal(event.Data, &raw); err != nil || len(raw.Delta) == 0 {
				return stream.stateFailure(index, "provider action delta is invalid")
			}
			block.actionBytes += len(raw.Delta)
			if block.actionBytes > stream.codec.options.MaxToolArgumentBytes {
				return protocolcore.NewFailure(
					protocolcore.ReasonStreamLimitExceeded,
					fmt.Sprintf("$event[%d].data.delta", index),
					errors.New("provider action exceeds the configured limit"),
				)
			}
			block.actionDeltas = append(block.actionDeltas, bytes.Clone(raw.Delta))
			break
		}
		switch payload.Delta.Type {
		case "text_delta":
			if block.kind != "text" {
				return stream.stateFailure(index, "text delta targets a non-text block")
			}
			_, _ = block.text.WriteString(payload.Delta.Text)
		case "citations_delta":
			if block.kind != "text" {
				return stream.stateFailure(index, "citation delta targets a non-text block")
			}
			if err := validateAnthropicCitation(payload.Delta.Citation); err != nil {
				return stream.eventFailure(index, err)
			}
		case "input_json_delta":
			if block.kind == "server_tool_use" {
				// Provider-executed input remains opaque evidence. It must not
				// become a local ToolIntent or activate the client-tool barrier.
				block.extensionFragments = append(block.extensionFragments, bytes.Clone(event.Data))
				break
			}
			if block.kind != "tool_use" {
				return stream.stateFailure(index, "tool delta targets a non-tool block")
			}
			if block.partialInput.Len()+len(payload.Delta.PartialJSON) >
				stream.codec.options.MaxToolArgumentBytes {
				return protocolcore.NewFailure(
					protocolcore.ReasonStreamLimitExceeded,
					fmt.Sprintf("$event[%d].data.delta.partial_json", index),
					errors.New("tool input exceeds the configured limit"),
				)
			}
			_, _ = block.partialInput.WriteString(payload.Delta.PartialJSON)
		case "thinking_delta", "signature_delta":
			if block.kind != "thinking" && block.kind != "redacted_thinking" {
				return stream.stateFailure(index, "thinking delta targets a different block")
			}
			fragment, err := json.Marshal(payload.Delta)
			if err != nil {
				return stream.eventFailure(index, err)
			}
			block.extensionFragments = append(block.extensionFragments, fragment)
		default:
			return stream.stateFailure(index, "content delta type is unsupported")
		}
	case "content_block_stop":
		var payload struct {
			Index int `json:"index"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return stream.eventFailure(index, err)
		}
		block, exists := stream.blocks[payload.Index]
		if !exists || block.stopped {
			return stream.stateFailure(index, "content block stop is invalid")
		}
		block.stopped = true
	case "message_delta":
		var payload struct {
			Delta struct {
				StopReason   string  `json:"stop_reason"`
				StopSequence *string `json:"stop_sequence"`
			} `json:"delta"`
			Usage messagesUsageWire `json:"usage"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return stream.eventFailure(index, err)
		}
		stream.stopReason = payload.Delta.StopReason
		stream.stopSequence = payload.Delta.StopSequence
		stream.mergeUsage(payload.Usage)
	case "message_stop":
		if !stream.messageStarted {
			return stream.stateFailure(index, "message_stop arrived before message_start")
		}
		stream.done = true
	default:
		return stream.stateFailure(index, "Anthropic SSE event type is unsupported")
	}
	stream.semanticProgress++
	return nil
}

func (stream *AnthropicProviderStream) mergeUsage(update messagesUsageWire) {
	if update.InputTokens != nil {
		stream.usage.InputTokens = update.InputTokens
	}
	if update.OutputTokens != nil {
		stream.usage.OutputTokens = update.OutputTokens
	}
	if update.CacheCreationInputTokens != nil {
		stream.usage.CacheCreationInputTokens = update.CacheCreationInputTokens
	}
	if update.CacheReadInputTokens != nil {
		stream.usage.CacheReadInputTokens = update.CacheReadInputTokens
	}
}

func (*AnthropicProviderStream) eventFailure(index int, err error) error {
	return protocolcore.NewFailure(
		protocolcore.ReasonMalformedEventStream,
		fmt.Sprintf("$event[%d].data", index),
		err,
	)
}

func (*AnthropicProviderStream) stateFailure(index int, message string) error {
	return protocolcore.NewFailure(
		protocolcore.ReasonStreamStateViolation,
		fmt.Sprintf("$event[%d]", index),
		errors.New(message),
	)
}

func appendCompatibleEvent(destination *bytes.Buffer, event ssewire.Event) error {
	if destination == nil {
		return errors.New("compatible SSE destination is nil")
	}
	if event.Name != "" && event.Name != "message" {
		if _, err := fmt.Fprintf(destination, "event: %s\n", event.Name); err != nil {
			return err
		}
	}
	if event.ID != "" {
		if _, err := fmt.Fprintf(destination, "id: %s\n", event.ID); err != nil {
			return err
		}
	}
	if event.Retry != nil {
		if _, err := fmt.Fprintf(destination, "retry: %d\n", *event.Retry); err != nil {
			return err
		}
	}
	for _, line := range bytes.Split(event.Data, []byte{'\n'}) {
		if _, err := destination.WriteString("data: "); err != nil {
			return err
		}
		if _, err := destination.Write(line); err != nil {
			return err
		}
		if err := destination.WriteByte('\n'); err != nil {
			return err
		}
	}
	return destination.WriteByte('\n')
}
