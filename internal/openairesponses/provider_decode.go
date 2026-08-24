package openairesponses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

const providerUsageSource = "openai-responses"

type providerResponseWire struct {
	ID                string                         `json:"id"`
	CreatedAt         int64                          `json:"created_at"`
	Status            string                         `json:"status"`
	Error             json.RawMessage                `json:"error"`
	IncompleteDetails *providerIncompleteDetailsWire `json:"incomplete_details"`
	Model             string                         `json:"model"`
	Output            []json.RawMessage              `json:"output"`
	Usage             json.RawMessage                `json:"usage"`
}

type providerIncompleteDetailsWire struct {
	Reason string `json:"reason"`
}

type providerOutputMessageWire struct {
	ID      string            `json:"id"`
	Type    string            `json:"type"`
	Status  string            `json:"status"`
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
	Agent   *agentItemWire    `json:"agent,omitempty"`
}

type providerOutputContentWire struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Refusal string `json:"refusal,omitempty"`
}

type providerFunctionCallWire struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Status    string         `json:"status"`
	CallID    string         `json:"call_id"`
	Namespace string         `json:"namespace,omitempty"`
	Name      string         `json:"name"`
	Arguments string         `json:"arguments"`
	Agent     *agentItemWire `json:"agent,omitempty"`
}

type providerCustomToolCallWire struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	CallID    string         `json:"call_id"`
	Namespace string         `json:"namespace,omitempty"`
	Name      string         `json:"name"`
	Input     string         `json:"input"`
	Agent     *agentItemWire `json:"agent,omitempty"`
}

type providerUsageWire struct {
	InputTokens        *int64                         `json:"input_tokens"`
	InputTokenDetails  providerInputTokenDetailsWire  `json:"input_tokens_details"`
	OutputTokens       *int64                         `json:"output_tokens"`
	OutputTokenDetails providerOutputTokenDetailsWire `json:"output_tokens_details"`
}

type providerInputTokenDetailsWire struct {
	CachedTokens     *int64 `json:"cached_tokens"`
	CacheWriteTokens *int64 `json:"cache_write_tokens"`
}

type providerOutputTokenDetailsWire struct {
	ReasoningTokens *int64 `json:"reasoning_tokens"`
}

// DecodeProviderResponse decodes the provider-native Responses terminal body
// into the neutral audit model. It is deliberately tolerant of additional
// same-dialect fields: the original wire remains authoritative and is never
// reconstructed from this inspection view.
func (codec *Codec) DecodeProviderResponse(
	request protocolcore.Request,
	body []byte,
) (protocolcore.Response, protocolcore.TranslationReport, error) {
	return codec.decodeProviderResponse(request, body, nil)
}

func (codec *Codec) decodeProviderResponse(
	request protocolcore.Request,
	body []byte,
	completedOutput []json.RawMessage,
) (protocolcore.Response, protocolcore.TranslationReport, error) {
	if codec == nil || len(body) == 0 || len(body) > codec.options.MaxResponseBytes {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			invalidProvider("$", errors.New("response body has an invalid size"))
	}
	if err := request.Validate(); err != nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	if err := rejectDuplicateNames(body); err != nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			invalidProvider("$", err)
	}
	var wire providerResponseWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			invalidProvider("$", err)
	}
	// The public Responses API normally repeats completed items in the terminal
	// response snapshot. Codex's authenticated ChatGPT transport can instead
	// send an empty terminal output array after emitting the complete items in
	// response.output_item.done events. Those events are still provider-native,
	// ordered wire evidence, so use them only as the audit projection fallback.
	if len(wire.Output) == 0 && len(completedOutput) > 0 {
		wire.Output = cloneRawMessages(completedOutput)
	}
	if rawPresent(wire.Error) {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			invalidProvider("$.error", errors.New("Responses terminal contains an error"))
	}

	blocks := make([]protocolcore.ContentBlock, 0, len(wire.Output))
	extensions := make([]protocolcore.ProviderExtension, 0, len(wire.Output))
	for index, raw := range wire.Output {
		decoded, decodedExtensions, err := decodeProviderOutputItem(
			raw,
			fmt.Sprintf("$.output[%d]", index),
		)
		if err != nil {
			return protocolcore.Response{}, protocolcore.TranslationReport{}, err
		}
		blocks = append(blocks, decoded...)
		extensions = append(extensions, decodedExtensions...)
	}
	protocolEvidence, err := providerOutputProtocolEvidence(wire.Output)
	if err != nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{}, err
	}
	if len(blocks) == 0 {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedProviderData,
				"$.output",
				errors.New("Responses terminal has no auditable output"),
			)
	}

	stopReason := protocolcore.StopReasonEndTurn
	hasToolCall := false
	for _, block := range blocks {
		if block.Kind == protocolcore.BlockToolCall {
			hasToolCall = true
			break
		}
	}
	if hasToolCall {
		stopReason = protocolcore.StopReasonToolUse
	}
	switch wire.Status {
	case "completed":
	case "incomplete":
		if wire.IncompleteDetails == nil ||
			wire.IncompleteDetails.Reason != "max_output_tokens" || hasToolCall {
			return protocolcore.Response{}, protocolcore.TranslationReport{},
				protocolcore.NewFailure(
					protocolcore.ReasonUnsupportedProviderData,
					"$.incomplete_details.reason",
					errors.New("Responses incomplete reason is unsupported"),
				)
		}
		stopReason = protocolcore.StopReasonMaxTokens
	default:
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			invalidProvider("$.status", errors.New("Responses terminal status is invalid"))
	}
	usage, err := decodeProviderUsage(wire.Usage)
	if err != nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{}, err
	}
	response := protocolcore.Response{
		ID:                 wire.ID,
		CreatedAtUnix:      wire.CreatedAt,
		RequestedModel:     request.RequestedModel,
		EffectiveModel:     request.EffectiveModel,
		ReportedModel:      wire.Model,
		Blocks:             blocks,
		ProviderExtensions: extensions,
		ProtocolEvidence:   protocolEvidence,
		StopReason:         stopReason,
		Usage:              usage,
	}
	if err := response.Validate(); err != nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			invalidProvider("$", err)
	}
	return response.Clone(), protocolcore.TranslationReport{}, nil
}

// providerOutputProtocolEvidence preserves only bounded identifiers needed to
// join a network response to an Agent client's local authority. The complete
// provider body remains available through raw evidence; this projection never
// reconstructs it or retains arbitrary provider fields.
func providerOutputProtocolEvidence(
	output []json.RawMessage,
) ([]protocolcore.ProtocolEvidenceValue, error) {
	values := make([]protocolcore.ProtocolEvidenceValue, 0, len(output)*4)
	for index, raw := range output {
		var identity struct {
			ID       string `json:"id"`
			CallID   string `json:"call_id"`
			Metadata struct {
				TurnID string `json:"turn_id"`
			} `json:"metadata"`
			InternalMetadata struct {
				TurnID string `json:"turn_id"`
			} `json:"internal_chat_message_metadata_passthrough"`
		}
		if err := json.Unmarshal(raw, &identity); err != nil {
			return nil, invalidProvider(
				fmt.Sprintf("$.output[%d]", index),
				err,
			)
		}
		prefix := fmt.Sprintf("openai_responses.output.%04d.", index)
		if identity.ID != "" {
			values = append(values, protocolcore.ProtocolEvidenceValue{
				Name: prefix + "id", Value: identity.ID,
			})
		}
		if identity.CallID != "" {
			values = append(values, protocolcore.ProtocolEvidenceValue{
				Name: prefix + "call_id", Value: identity.CallID,
			})
		}
		if identity.Metadata.TurnID != "" {
			values = append(values, protocolcore.ProtocolEvidenceValue{
				Name: prefix + "metadata.turn_id", Value: identity.Metadata.TurnID,
			})
		}
		if identity.InternalMetadata.TurnID != "" {
			values = append(values, protocolcore.ProtocolEvidenceValue{
				Name:  prefix + "internal_chat_message_metadata_passthrough.turn_id",
				Value: identity.InternalMetadata.TurnID,
			})
		}
	}
	slices.SortFunc(values, func(left, right protocolcore.ProtocolEvidenceValue) int {
		return strings.Compare(left.Name, right.Name)
	})
	if err := protocolcore.ValidateProtocolEvidence(values); err != nil {
		return nil, invalidProvider("$.output", err)
	}
	return values, nil
}

func decodeProviderOutputItem(
	raw json.RawMessage,
	path string,
) ([]protocolcore.ContentBlock, []protocolcore.ProviderExtension, error) {
	kind, err := peekType(raw)
	if err != nil {
		return nil, nil, invalidProvider(path, err)
	}
	switch kind {
	case "reasoning":
		message, err := decodeResponsesReasoningItem(raw, path, false)
		if err != nil {
			return nil, nil, err
		}
		if message.Agent != nil {
			blocks := make([]protocolcore.ContentBlock, len(message.Blocks))
			for index, block := range message.Blocks {
				blocks[index] = block.Clone()
				blocks[index].Agent = cloneAgentMessageContext(message.Agent)
			}
			return blocks, nil, nil
		}
		extensions := make([]protocolcore.ProviderExtension, 0, len(message.Blocks))
		for _, block := range message.Blocks {
			extensions = append(extensions, block.ProviderExtension.Clone())
		}
		return nil, extensions, nil
	case "message":
		var wire providerOutputMessageWire
		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, nil, invalidProvider(path, err)
		}
		if wire.Role != "assistant" ||
			(wire.Status != "" && wire.Status != "completed") ||
			len(wire.Content) == 0 {
			return nil, nil, invalidProvider(path, errors.New("Responses message output is invalid"))
		}
		blocks := make([]protocolcore.ContentBlock, 0, len(wire.Content))
		context, err := decodeOptionalAgentContext(wire.Agent)
		if err != nil {
			return nil, nil, invalidProvider(path+".agent", err)
		}
		for index, rawContent := range wire.Content {
			contentPath := fmt.Sprintf("%s.content[%d]", path, index)
			var content providerOutputContentWire
			if err := json.Unmarshal(rawContent, &content); err != nil {
				return nil, nil, invalidProvider(contentPath, err)
			}
			var block protocolcore.ContentBlock
			switch content.Type {
			case "output_text":
				block, err = protocolcore.NewTextBlock(content.Text)
			case "refusal":
				block, err = protocolcore.NewRefusalBlock(content.Refusal)
			default:
				return nil, nil, protocolcore.NewFailure(
					protocolcore.ReasonUnsupportedProviderData,
					contentPath+".type",
					errors.New("Responses output content type is unsupported"),
				)
			}
			if err != nil {
				return nil, nil, invalidProvider(contentPath, err)
			}
			block.Agent = cloneAgentMessageContext(context)
			blocks = append(blocks, block)
		}
		return blocks, nil, nil
	case "agent_message":
		message, _, err := decodeAgentMessageItem(raw, path, true)
		if err != nil {
			return nil, nil, err
		}
		blocks := make([]protocolcore.ContentBlock, len(message.Blocks))
		for index, block := range message.Blocks {
			blocks[index] = block.Clone()
			blocks[index].Agent = cloneAgentMessageContext(message.Agent)
		}
		return blocks, nil, nil
	case "multi_agent_call":
		message, _, err := decodeMultiAgentCallItem(raw, path, true)
		if err != nil {
			return nil, nil, err
		}
		block := message.Blocks[0].Clone()
		block.Agent = cloneAgentMessageContext(message.Agent)
		return []protocolcore.ContentBlock{block}, nil, nil
	case "multi_agent_call_output":
		message, _, err := decodeMultiAgentCallOutputItem(raw, path, true)
		if err != nil {
			return nil, nil, err
		}
		block := message.Blocks[0].Clone()
		block.Agent = cloneAgentMessageContext(message.Agent)
		return []protocolcore.ContentBlock{block}, nil, nil
	case "function_call":
		var wire providerFunctionCallWire
		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, nil, invalidProvider(path, err)
		}
		if wire.Status != "" && wire.Status != "completed" {
			return nil, nil, invalidProvider(path+".status", errors.New("function call is incomplete"))
		}
		arguments, err := protocolcore.NewJSONObject(
			[]byte(wire.Arguments),
			protocolcore.MaxToolJSONBytes,
		)
		if err != nil {
			return nil, nil, invalidProvider(path+".arguments", err)
		}
		call, err := newToolCall(
			protocolcore.ToolKindFunction,
			wire.ID,
			wire.CallID,
			wire.Namespace,
			wire.Name,
			arguments,
			"",
		)
		if err != nil {
			return nil, nil, invalidProvider(path, err)
		}
		block, err := protocolcore.NewToolCallBlock(call)
		if err != nil {
			return nil, nil, invalidProvider(path, err)
		}
		context, err := decodeOptionalAgentContext(wire.Agent)
		if err != nil {
			return nil, nil, invalidProvider(path+".agent", err)
		}
		block.Agent = cloneAgentMessageContext(context)
		return []protocolcore.ContentBlock{block}, nil, nil
	case "custom_tool_call":
		var wire providerCustomToolCallWire
		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, nil, invalidProvider(path, err)
		}
		call, err := newToolCall(
			protocolcore.ToolKindCustom,
			wire.ID,
			wire.CallID,
			wire.Namespace,
			wire.Name,
			protocolcore.JSONDocument{},
			wire.Input,
		)
		if err != nil {
			return nil, nil, invalidProvider(path, err)
		}
		block, err := protocolcore.NewToolCallBlock(call)
		if err != nil {
			return nil, nil, invalidProvider(path, err)
		}
		context, err := decodeOptionalAgentContext(wire.Agent)
		if err != nil {
			return nil, nil, invalidProvider(path+".agent", err)
		}
		block.Agent = cloneAgentMessageContext(context)
		return []protocolcore.ContentBlock{block}, nil, nil
	default:
		return nil, nil, protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedProviderData,
			path+".type",
			errors.New("Responses output item type is unsupported"),
		)
	}
}

func cloneAgentMessageContext(
	context *protocolcore.AgentMessageContext,
) *protocolcore.AgentMessageContext {
	if context == nil {
		return nil
	}
	cloned := *context
	return &cloned
}

func decodeProviderUsage(raw json.RawMessage) (protocolcore.Usage, error) {
	if !rawPresent(raw) {
		return protocolcore.Usage{}, nil
	}
	var wire providerUsageWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return protocolcore.Usage{}, invalidProvider("$.usage", err)
	}
	for path, value := range map[string]*int64{
		"$.usage.input_tokens":                            wire.InputTokens,
		"$.usage.input_tokens_details.cached_tokens":      wire.InputTokenDetails.CachedTokens,
		"$.usage.input_tokens_details.cache_write_tokens": wire.InputTokenDetails.CacheWriteTokens,
		"$.usage.output_tokens":                           wire.OutputTokens,
		"$.usage.output_tokens_details.reasoning_tokens":  wire.OutputTokenDetails.ReasoningTokens,
	} {
		if value != nil && *value < 0 {
			return protocolcore.Usage{}, invalidProvider(path, errors.New("usage count is negative"))
		}
	}
	usage := protocolcore.Usage{}
	known := func(value int64) protocolcore.UsageValue {
		return protocolcore.UsageValue{Tokens: value, Known: true, Source: providerUsageSource}
	}
	if wire.InputTokens != nil && wire.InputTokenDetails.CachedTokens != nil {
		if *wire.InputTokenDetails.CachedTokens > *wire.InputTokens {
			return protocolcore.Usage{}, invalidProvider(
				"$.usage.input_tokens_details.cached_tokens",
				errors.New("cached usage exceeds input usage"),
			)
		}
		usage.InputUncached = known(*wire.InputTokens - *wire.InputTokenDetails.CachedTokens)
		usage.CacheRead = known(*wire.InputTokenDetails.CachedTokens)
	}
	if wire.InputTokenDetails.CacheWriteTokens != nil {
		usage.CacheWrite = known(*wire.InputTokenDetails.CacheWriteTokens)
	}
	if wire.OutputTokens != nil {
		usage.Output = known(*wire.OutputTokens)
	}
	if wire.OutputTokenDetails.ReasoningTokens != nil {
		usage.Reasoning = known(*wire.OutputTokenDetails.ReasoningTokens)
	}
	if err := usage.Validate(); err != nil {
		return protocolcore.Usage{}, invalidProvider("$.usage", err)
	}
	return usage, nil
}

func invalidProvider(path string, cause error) error {
	return protocolcore.NewFailure(
		protocolcore.ReasonInvalidProviderResponse,
		path,
		cause,
	)
}

type ProviderStream struct {
	mu sync.Mutex

	codec    *Codec
	request  protocolcore.Request
	decoder  *ssewire.Decoder
	wire     bytes.Buffer
	terminal *protocolcore.Response
	output   map[int]json.RawMessage
	progress uint64
	failed   bool
	finished bool
}

func (codec *Codec) NewProviderStream(request protocolcore.Request) (*ProviderStream, error) {
	if codec == nil {
		return nil, errors.New("Responses codec is nil")
	}
	if err := request.Validate(); err != nil {
		return nil, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	if !request.Stream {
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$.stream",
			errors.New("request is not configured for streaming"),
		)
	}
	options := ssewire.DefaultOptions()
	options.MaxLineBytes = codec.options.MaxResponseBytes
	options.MaxEventBytes = codec.options.MaxResponseBytes
	options.MaxPendingBytes = codec.options.MaxResponseBytes
	decoder, err := ssewire.NewDecoder(options)
	if err != nil {
		return nil, err
	}
	return &ProviderStream{
		codec:   codec,
		request: request.Clone(),
		decoder: decoder,
		output:  make(map[int]json.RawMessage),
	}, nil
}

func (stream *ProviderStream) Feed(_ context.Context, fragment []byte) ([]byte, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.failed || stream.finished {
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonStreamStateViolation,
			"$",
			errors.New("Responses stream is not writable"),
		)
	}
	if stream.wire.Len()+len(fragment) > stream.codec.options.MaxResponseBytes {
		stream.failed = true
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonStreamLimitExceeded,
			"$",
			errors.New("Responses stream exceeds the configured byte limit"),
		)
	}
	_, _ = stream.wire.Write(fragment)
	events, err := stream.decoder.Feed(fragment)
	if err != nil {
		stream.failed = true
		return nil, protocolcore.NewFailure(protocolcore.ReasonMalformedEventStream, "$", err)
	}
	for _, event := range events {
		if bytes.Equal(bytes.TrimSpace(event.Data), []byte("[DONE]")) {
			if stream.terminal == nil {
				stream.failed = true
				return nil, protocolcore.NewFailure(
					protocolcore.ReasonStreamStateViolation,
					"$",
					errors.New("Responses DONE marker precedes the terminal event"),
				)
			}
			continue
		}
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(event.Data, &header); err != nil || header.Type == "" {
			stream.failed = true
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonMalformedEventStream,
				"$",
				errors.New("Responses SSE event is invalid"),
			)
		}
		if err := rejectDuplicateNames(event.Data); err != nil {
			stream.failed = true
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonMalformedEventStream,
				"$",
				err,
			)
		}
		if event.Name != "message" && event.Name != header.Type {
			stream.failed = true
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonMalformedEventStream,
				"$",
				errors.New("Responses SSE event name does not match its payload"),
			)
		}
		stream.progress++
		switch header.Type {
		case "response.output_item.done":
			var completed struct {
				OutputIndex *int            `json:"output_index"`
				Item        json.RawMessage `json:"item"`
			}
			if err := json.Unmarshal(event.Data, &completed); err != nil ||
				completed.OutputIndex == nil ||
				*completed.OutputIndex < 0 ||
				*completed.OutputIndex >= protocolcore.MaxContentBlocks ||
				!rawPresent(completed.Item) {
				stream.failed = true
				return nil, protocolcore.NewFailure(
					protocolcore.ReasonMalformedEventStream,
					"$.item",
					errors.New("Responses completed output item is invalid"),
				)
			}
			if _, duplicate := stream.output[*completed.OutputIndex]; duplicate {
				stream.failed = true
				return nil, protocolcore.NewFailure(
					protocolcore.ReasonStreamStateViolation,
					"$.output_index",
					errors.New("Responses completed output index is duplicated"),
				)
			}
			if _, _, err := decodeProviderOutputItem(
				completed.Item,
				fmt.Sprintf("$.output[%d]", *completed.OutputIndex),
			); err != nil {
				stream.failed = true
				return nil, err
			}
			stream.output[*completed.OutputIndex] = bytes.Clone(completed.Item)
		case "response.completed", "response.incomplete":
			if stream.terminal != nil {
				stream.failed = true
				return nil, protocolcore.NewFailure(
					protocolcore.ReasonStreamStateViolation,
					"$",
					errors.New("Responses stream has duplicate terminal events"),
				)
			}
			var terminal struct {
				Response json.RawMessage `json:"response"`
			}
			if err := json.Unmarshal(event.Data, &terminal); err != nil || !rawPresent(terminal.Response) {
				stream.failed = true
				return nil, protocolcore.NewFailure(
					protocolcore.ReasonMalformedEventStream,
					"$.response",
					errors.New("Responses terminal event is invalid"),
				)
			}
			completedOutput, err := stream.completedOutput()
			if err != nil {
				stream.failed = true
				return nil, err
			}
			response, _, err := stream.codec.decodeProviderResponse(
				stream.request,
				terminal.Response,
				completedOutput,
			)
			if err != nil {
				stream.failed = true
				return nil, err
			}
			stream.terminal = &response
		case "response.failed":
			stream.failed = true
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonInvalidProviderResponse,
				"$",
				errors.New("Responses stream failed"),
			)
		}
	}
	// The same-dialect managed path holds the complete stream until its terminal
	// tool calls are approved. Original passthrough has already written these
	// exact bytes to the client and uses this stream for audit only.
	return nil, nil
}

func (stream *ProviderStream) completedOutput() ([]json.RawMessage, error) {
	if len(stream.output) == 0 {
		return nil, nil
	}
	output := make([]json.RawMessage, len(stream.output))
	for index := range output {
		raw, present := stream.output[index]
		if !present {
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonStreamStateViolation,
				"$.output_index",
				errors.New("Responses completed output items are not contiguous"),
			)
		}
		output[index] = bytes.Clone(raw)
	}
	return output, nil
}

func cloneRawMessages(source []json.RawMessage) []json.RawMessage {
	cloned := make([]json.RawMessage, len(source))
	for index, raw := range source {
		cloned[index] = bytes.Clone(raw)
	}
	return cloned
}

func (stream *ProviderStream) SemanticProgress() uint64 {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.progress
}

func (stream *ProviderStream) FinishDecoded(
	_ context.Context,
) (protocolpath.PendingTerminal, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.failed || stream.finished {
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonStreamStateViolation,
			"$",
			errors.New("Responses stream cannot be finished"),
		)
	}
	if err := stream.decoder.Finish(); err != nil {
		stream.failed = true
		return nil, protocolcore.NewFailure(protocolcore.ReasonTruncatedEventStream, "$", err)
	}
	if stream.terminal == nil {
		stream.failed = true
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonTruncatedEventStream,
			"$",
			errors.New("Responses stream has no terminal event"),
		)
	}
	release := stream.wire.Bytes()
	release, err := rewriteCompatibleStreamForClient(
		release,
		stream.request.RequestedModel,
		stream.request.EffectiveModel,
		stream.codec.options.MaxResponseBytes,
	)
	if err != nil {
		stream.failed = true
		return nil, err
	}
	stream.finished = true
	return newProviderPendingTerminal(release, stream.terminal.Clone()), nil
}

func rewriteCompatibleStreamForClient(
	wire []byte,
	requestedModel string,
	effectiveModel string,
	maxBytes int,
) ([]byte, error) {
	options := ssewire.DefaultOptions()
	options.MaxLineBytes = maxBytes
	options.MaxEventBytes = maxBytes
	options.MaxPendingBytes = maxBytes
	decoder, err := ssewire.NewDecoder(options)
	if err != nil {
		return nil, err
	}
	events, err := decoder.Feed(wire)
	if err != nil {
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonMalformedEventStream,
			"$",
			err,
		)
	}
	if err := decoder.Finish(); err != nil {
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonTruncatedEventStream,
			"$",
			err,
		)
	}
	privateIndexes, privateItemIDs, err := privateReasoningOutput(events)
	if err != nil {
		return nil, err
	}
	var rewritten bytes.Buffer
	for _, event := range events {
		clientEvent, keep, rewriteErr := rewriteCompatibleResponseEvent(
			event,
			requestedModel,
			effectiveModel,
			privateIndexes,
			privateItemIDs,
		)
		if rewriteErr != nil {
			return nil, rewriteErr
		}
		if !keep {
			continue
		}
		encoded, encodeErr := ssewire.Encode(clientEvent)
		if encodeErr != nil {
			return nil, encodeErr
		}
		if rewritten.Len()+len(encoded) > maxBytes {
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonStreamLimitExceeded,
				"$",
				errors.New("Responses client stream exceeds the configured byte limit"),
			)
		}
		_, _ = rewritten.Write(encoded)
	}
	return bytes.Clone(rewritten.Bytes()), nil
}

func privateReasoningOutput(
	events []ssewire.Event,
) (map[int]struct{}, map[string]struct{}, error) {
	indexes := make(map[int]struct{})
	itemIDs := make(map[string]struct{})
	for _, event := range events {
		if bytes.Equal(bytes.TrimSpace(event.Data), []byte("[DONE]")) {
			continue
		}
		var root map[string]json.RawMessage
		if err := json.Unmarshal(event.Data, &root); err != nil || root == nil {
			return nil, nil, protocolcore.NewFailure(
				protocolcore.ReasonMalformedEventStream,
				"$",
				errors.New("Responses SSE event is invalid"),
			)
		}
		var eventType string
		if err := json.Unmarshal(root["type"], &eventType); err != nil || eventType == "" {
			return nil, nil, protocolcore.NewFailure(
				protocolcore.ReasonMalformedEventStream,
				"$.type",
				errors.New("Responses SSE event type is invalid"),
			)
		}
		if strings.HasPrefix(eventType, "response.reasoning_") {
			if index, ok, err := responseEventOutputIndex(root); err != nil {
				return nil, nil, err
			} else if ok {
				indexes[index] = struct{}{}
			}
			if itemID, ok, err := responseEventItemID(root); err != nil {
				return nil, nil, err
			} else if ok {
				itemIDs[itemID] = struct{}{}
			}
		}
		if itemRaw, present := root["item"]; present && rawPresent(itemRaw) {
			itemType, itemID, err := responseOutputItemIdentity(itemRaw)
			if err != nil {
				return nil, nil, err
			}
			if itemType == "reasoning" {
				if index, ok, err := responseEventOutputIndex(root); err != nil {
					return nil, nil, err
				} else if ok {
					indexes[index] = struct{}{}
				}
				if itemID != "" {
					itemIDs[itemID] = struct{}{}
				}
			}
		}
		responseRaw, present := root["response"]
		if !present || !rawPresent(responseRaw) {
			continue
		}
		var response struct {
			Output []json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(responseRaw, &response); err != nil {
			return nil, nil, protocolcore.NewFailure(
				protocolcore.ReasonMalformedEventStream,
				"$.response",
				errors.New("Responses SSE response is invalid"),
			)
		}
		for index, itemRaw := range response.Output {
			itemType, itemID, err := responseOutputItemIdentity(itemRaw)
			if err != nil {
				return nil, nil, err
			}
			if itemType != "reasoning" {
				continue
			}
			indexes[index] = struct{}{}
			if itemID != "" {
				itemIDs[itemID] = struct{}{}
			}
		}
	}
	return indexes, itemIDs, nil
}

func rewriteCompatibleResponseEvent(
	event ssewire.Event,
	requestedModel string,
	effectiveModel string,
	privateIndexes map[int]struct{},
	privateItemIDs map[string]struct{},
) (ssewire.Event, bool, error) {
	if bytes.Equal(bytes.TrimSpace(event.Data), []byte("[DONE]")) {
		return event, true, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(event.Data, &root); err != nil || root == nil {
		return ssewire.Event{}, false, protocolcore.NewFailure(
			protocolcore.ReasonMalformedEventStream,
			"$",
			errors.New("Responses SSE event is invalid"),
		)
	}
	var eventType string
	if err := json.Unmarshal(root["type"], &eventType); err != nil || eventType == "" {
		return ssewire.Event{}, false, protocolcore.NewFailure(
			protocolcore.ReasonMalformedEventStream,
			"$.type",
			errors.New("Responses SSE event type is invalid"),
		)
	}
	if strings.HasPrefix(eventType, "response.reasoning_") {
		return ssewire.Event{}, false, nil
	}
	if itemRaw, present := root["item"]; present && rawPresent(itemRaw) {
		itemType, _, err := responseOutputItemIdentity(itemRaw)
		if err != nil {
			return ssewire.Event{}, false, err
		}
		if itemType == "reasoning" {
			return ssewire.Event{}, false, nil
		}
	}
	if itemID, present, err := responseEventItemID(root); err != nil {
		return ssewire.Event{}, false, err
	} else if present {
		if _, private := privateItemIDs[itemID]; private {
			return ssewire.Event{}, false, nil
		}
	}
	modified := false
	if outputIndex, present, err := responseEventOutputIndex(root); err != nil {
		return ssewire.Event{}, false, err
	} else if present {
		if _, private := privateIndexes[outputIndex]; private {
			return ssewire.Event{}, false, nil
		}
		portableIndex := compactOutputIndex(outputIndex, privateIndexes)
		if portableIndex != outputIndex {
			encoded, err := json.Marshal(portableIndex)
			if err != nil {
				return ssewire.Event{}, false, err
			}
			root["output_index"] = encoded
			modified = true
		}
	}
	responseRaw, present := root["response"]
	if !present || !rawPresent(responseRaw) {
		if !modified {
			return event, true, nil
		}
		return marshalCompatibleResponseEvent(event, root)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(responseRaw, &response); err != nil || response == nil {
		return ssewire.Event{}, false, protocolcore.NewFailure(
			protocolcore.ReasonMalformedEventStream,
			"$.response",
			errors.New("Responses SSE response is invalid"),
		)
	}
	if outputRaw, present := response["output"]; present {
		var output []json.RawMessage
		if err := json.Unmarshal(outputRaw, &output); err != nil {
			return ssewire.Event{}, false, protocolcore.NewFailure(
				protocolcore.ReasonMalformedEventStream,
				"$.response.output",
				errors.New("Responses SSE response output is invalid"),
			)
		}
		portable := make([]json.RawMessage, 0, len(output))
		for _, itemRaw := range output {
			itemType, _, err := responseOutputItemIdentity(itemRaw)
			if err != nil {
				return ssewire.Event{}, false, err
			}
			if itemType == "reasoning" {
				modified = true
				continue
			}
			portable = append(portable, itemRaw)
		}
		if len(portable) != len(output) {
			encoded, err := json.Marshal(portable)
			if err != nil {
				return ssewire.Event{}, false, err
			}
			response["output"] = encoded
		}
	}
	modelRaw, present := response["model"]
	if present && requestedModel != effectiveModel {
		var reportedModel string
		if err := json.Unmarshal(modelRaw, &reportedModel); err != nil || reportedModel == "" {
			return ssewire.Event{}, false, protocolcore.NewFailure(
				protocolcore.ReasonMalformedEventStream,
				"$.response.model",
				errors.New("Responses SSE response model is invalid"),
			)
		}
		if reportedModel != requestedModel {
			model, err := json.Marshal(requestedModel)
			if err != nil {
				return ssewire.Event{}, false, err
			}
			response["model"] = model
			modified = true
		}
	}
	encodedResponse, err := json.Marshal(response)
	if err != nil {
		return ssewire.Event{}, false, err
	}
	root["response"] = encodedResponse
	if !modified {
		return event, true, nil
	}
	return marshalCompatibleResponseEvent(event, root)
}

func marshalCompatibleResponseEvent(
	event ssewire.Event,
	root map[string]json.RawMessage,
) (ssewire.Event, bool, error) {
	encodedEvent, err := json.Marshal(root)
	if err != nil {
		return ssewire.Event{}, false, err
	}
	rewritten := event.Clone()
	rewritten.Data = encodedEvent
	return rewritten, true, nil
}

func responseOutputItemIdentity(raw json.RawMessage) (string, string, error) {
	var item struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(raw, &item); err != nil || item.Type == "" {
		return "", "", protocolcore.NewFailure(
			protocolcore.ReasonMalformedEventStream,
			"$.item",
			errors.New("Responses output item is invalid"),
		)
	}
	return item.Type, item.ID, nil
}

func responseEventOutputIndex(
	root map[string]json.RawMessage,
) (int, bool, error) {
	raw, present := root["output_index"]
	if !present {
		return 0, false, nil
	}
	var index int
	if err := json.Unmarshal(raw, &index); err != nil || index < 0 ||
		index >= protocolcore.MaxContentBlocks {
		return 0, false, protocolcore.NewFailure(
			protocolcore.ReasonMalformedEventStream,
			"$.output_index",
			errors.New("Responses output index is invalid"),
		)
	}
	return index, true, nil
}

func responseEventItemID(
	root map[string]json.RawMessage,
) (string, bool, error) {
	raw, present := root["item_id"]
	if !present {
		return "", false, nil
	}
	var itemID string
	if err := json.Unmarshal(raw, &itemID); err != nil || itemID == "" {
		return "", false, protocolcore.NewFailure(
			protocolcore.ReasonMalformedEventStream,
			"$.item_id",
			errors.New("Responses item ID is invalid"),
		)
	}
	return itemID, true, nil
}

func compactOutputIndex(index int, removed map[int]struct{}) int {
	portable := index
	for candidate := range removed {
		if candidate < index {
			portable--
		}
	}
	return portable
}

type providerPendingTerminal struct {
	mu       sync.Mutex
	release  []byte
	response protocolcore.Response
	intents  []protocolcore.ToolIntent
	decided  bool
}

func newProviderPendingTerminal(
	release []byte,
	response protocolcore.Response,
) *providerPendingTerminal {
	intents := make([]protocolcore.ToolIntent, 0)
	ordinal := 0
	for _, block := range response.Blocks {
		if block.Kind != protocolcore.BlockToolCall {
			continue
		}
		intents = append(intents, protocolcore.ToolIntent{
			ResponseID: response.ID,
			Ordinal:    ordinal,
			Call:       block.ToolCall.Clone(),
		})
		ordinal++
	}
	return &providerPendingTerminal{
		release:  bytes.Clone(release),
		response: response.Clone(),
		intents:  intents,
	}
}

func (terminal *providerPendingTerminal) ToolIntents() []protocolcore.ToolIntent {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	intents := make([]protocolcore.ToolIntent, len(terminal.intents))
	for index, intent := range terminal.intents {
		intents[index] = intent.Clone()
	}
	return intents
}

func (terminal *providerPendingTerminal) DecodedResponse() protocolcore.Response {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.response.Clone()
}

func (*providerPendingTerminal) TranslationReport() protocolcore.TranslationReport {
	return protocolcore.TranslationReport{}
}

func (terminal *providerPendingTerminal) Approve() ([]byte, error) {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.decided {
		return nil, errors.New("Responses terminal was already decided")
	}
	terminal.decided = true
	return bytes.Clone(terminal.release), nil
}

func (terminal *providerPendingTerminal) Reject() error {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.decided {
		return errors.New("Responses terminal was already decided")
	}
	terminal.decided = true
	terminal.release = nil
	return nil
}
