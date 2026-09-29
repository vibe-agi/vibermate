package openairesponses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
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
	if wire.Status == "failed" || rawPresent(wire.Error) {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewNativeProviderFailure("$.error", protocolspec.DialectOpenAIResponses, wire.Error)
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
		// A reasoning-only native terminal is valid output. Keep its audit
		// blocks opaque rather than inventing text or rejecting native bytes.
		for _, extension := range extensions {
			block, err := protocolcore.NewProviderExtensionBlock(extension)
			if err != nil {
				return protocolcore.Response{}, protocolcore.TranslationReport{}, invalidProvider("$.output", err)
			}
			blocks = append(blocks, block)
		}
		extensions = nil
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
		if wire.IncompleteDetails == nil || wire.IncompleteDetails.Reason == "" {
			return protocolcore.Response{}, protocolcore.TranslationReport{},
				invalidProvider("$.incomplete_details", errors.New("Responses incomplete terminal has no reason"))
		}
		// Tool calls in an incomplete response still pass the decision gate;
		// the stop reason records why the provider ended it.
		switch wire.IncompleteDetails.Reason {
		case "max_output_tokens":
			stopReason = protocolcore.StopReasonMaxTokens
		case "content_filter":
			stopReason = protocolcore.StopReasonRefusal
		default:
			stopReason = protocolcore.StopReasonIncomplete
		}
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
		// A message cut off by an incomplete terminal is itself incomplete.
		if wire.Role != "assistant" ||
			(wire.Status != "" && wire.Status != "completed" && wire.Status != "incomplete") ||
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
		if isOpaqueResponsesProviderOutputItem(kind) {
			block, err := newResponsesExtensionBlock(
				protocolcore.ProviderExtensionOpaqueItem,
				path,
				raw,
			)
			if err != nil {
				return nil, nil, invalidProvider(path, err)
			}
			return []protocolcore.ContentBlock{block}, nil, nil
		}
		block, err := providerActionBlock(kind, path, raw)
		if err != nil {
			return nil, nil, invalidProvider(path, err)
		}
		return []protocolcore.ContentBlock{block}, nil, nil
	}
}

// providerActionBlock carries an unmodelled output item as an unproven client
// action. Its call key is the item's own call_id or id, so an approval and the
// client's later result name the same item.
func providerActionBlock(kind, path string, raw json.RawMessage) (protocolcore.ContentBlock, error) {
	var identity struct {
		ID     string `json:"id"`
		CallID string `json:"call_id"`
	}
	if err := json.Unmarshal(raw, &identity); err != nil {
		return protocolcore.ContentBlock{}, err
	}
	callID := identity.CallID
	if callID == "" {
		callID = identity.ID
	}
	if callID == "" {
		return protocolcore.ContentBlock{}, errors.New("Responses output item has neither call_id nor id")
	}
	item, err := protocolcore.NewJSONObject(raw, protocolcore.MaxToolJSONBytes)
	if err != nil {
		return protocolcore.ContentBlock{}, err
	}
	call, err := newToolCall(protocolcore.ToolKindProviderAction, identity.ID, callID, "", kind, item, "")
	if err != nil {
		return protocolcore.ContentBlock{}, err
	}
	return protocolcore.NewToolCallBlock(call)
}

func isOpaqueResponsesProviderOutputItem(kind string) bool {
	// Only items whose work already ran on the provider belong here: they are
	// results, not something the client can execute. Every other unmodelled
	// item is an unproven action (see providerActionBlock).
	switch kind {
	case "tool_search_call",
		"tool_search_output",
		"web_search_call",
		"image_generation_call",
		"mcp_call",
		"mcp_list_tools",
		"code_interpreter_call",
		"file_search_call",
		"compaction",
		"compaction_summary",
		"context_compaction":
		return true
	default:
		return false
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

	codec               *Codec
	request             protocolcore.Request
	decoder             *ssewire.Decoder
	held                bytes.Buffer
	wireBytes           int
	clientBytes         int
	barrier             bool
	terminal            *protocolcore.Response
	notificationFailure *protocolcore.Failure
	output              map[int]json.RawMessage
	progress            uint64
	failed              bool
	finished            bool
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
	if stream.wireBytes+len(fragment) > stream.codec.options.MaxResponseBytes {
		stream.failed = true
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonStreamLimitExceeded,
			"$",
			errors.New("Responses stream exceeds the configured byte limit"),
		)
	}
	stream.wireBytes += len(fragment)
	events, err := stream.decoder.Feed(fragment)
	if err != nil {
		stream.failed = true
		return nil, protocolcore.NewFailure(protocolcore.ReasonMalformedEventStream, "$", err)
	}
	var safe bytes.Buffer
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
			encoded, err := stream.encodeClientEvent(event)
			if err != nil {
				stream.failed = true
				return nil, err
			}
			_, _ = stream.held.Write(encoded)
			continue
		}
		if stream.terminal != nil {
			stream.failed = true
			return nil, protocolcore.NewFailure(protocolcore.ReasonStreamStateViolation, "$",
				errors.New("event arrived after the Responses terminal event"))
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
			// Approval must cover the calls already present in held item.done
			// frames, not a contradictory terminal snapshot supplied afterwards.
			approvedCalls := make(map[protocolcore.CallKey]protocolcore.ToolCall)
			for _, block := range response.Blocks {
				if block.Kind == protocolcore.BlockToolCall {
					approvedCalls[block.ToolCall.Key] = block.ToolCall
				}
			}
			for _, raw := range completedOutput {
				blocks, _, err := decodeProviderOutputItem(raw, "$.output")
				if err != nil {
					stream.failed = true
					return nil, err
				}
				for _, block := range blocks {
					if block.Kind == protocolcore.BlockToolCall && !reflect.DeepEqual(approvedCalls[block.ToolCall.Key], block.ToolCall) {
						stream.failed = true
						return nil, invalidProvider("$.output", errors.New("terminal tool calls disagree with completed output items"))
					}
				}
			}
			stream.terminal = &response
		case "response.failed", "error":
			// Responses errors carry code at the event root; response.failed
			// carries it inside response.error. Some providers use error.error.
			var failure struct {
				Error    json.RawMessage `json:"error"`
				Response struct {
					Error json.RawMessage `json:"error"`
				} `json:"response"`
			}
			_ = json.Unmarshal(event.Data, &failure) // framing/JSON validated above
			raw := event.Data
			if header.Type == "response.failed" {
				raw = failure.Response.Error
			} else if rawPresent(failure.Error) {
				raw = failure.Error
			}
			failureErr := protocolcore.NewNativeProviderFailure("$", protocolspec.DialectOpenAIResponses, raw)
			if header.Type == "error" {
				// Native clients may ignore a notification and keep reading.
				// Only response.failed is an explicit failed terminal. Deliver
				// this non-executable event even while tool output is held, but
				// retain its error for EOF without a subsequent terminal.
				stream.notificationFailure = failureErr
				encoded, err := stream.encodeClientEvent(event)
				if err != nil {
					stream.failed = true
					return nil, err
				}
				_, _ = safe.Write(encoded)
				continue
			}
			stream.failed = true
			failureErr.NativeError = failureErr.NativeError.WithStreamEvent(header.Type, event.Data)
			return safe.Bytes(), failureErr
		}
		// Preserve event order: after the first potentially actionable item,
		// all subsequent events (including success) stay behind approval. The
		// positive non-actionable set also makes new event kinds fail closed.
		stream.barrier = stream.barrier || !nonActionableResponseEvent(header.Type, event.Data)
		clientEvent, err := stream.clientEvent(event)
		if err != nil {
			stream.failed = true
			return nil, err
		}
		encoded, err := stream.encodeClientEvent(clientEvent)
		if err != nil {
			stream.failed = true
			return nil, err
		}
		if stream.barrier {
			_, _ = stream.held.Write(encoded)
		} else {
			_, _ = safe.Write(encoded)
		}
	}
	return bytes.Clone(safe.Bytes()), nil
}

func (stream *ProviderStream) encodeClientEvent(event ssewire.Event) ([]byte, error) {
	encoded, err := ssewire.Encode(event)
	if err != nil {
		return nil, err
	}
	// Normalization can expand a frame (for example an inherited SSE id).
	// Bound the downstream representation as well as received wire bytes.
	if len(encoded) > stream.codec.options.MaxResponseBytes-stream.clientBytes {
		return nil, protocolcore.NewFailure(protocolcore.ReasonStreamLimitExceeded, "$",
			errors.New("Responses client stream exceeds the configured byte limit"))
	}
	stream.clientBytes += len(encoded)
	return encoded, nil
}

func nonActionableResponseEvent(kind string, data []byte) bool {
	switch kind {
	case "response.created", "response.in_progress":
		var envelope struct {
			Response struct {
				Output []json.RawMessage `json:"output"`
				Error  json.RawMessage   `json:"error"`
			} `json:"response"`
		}
		return json.Unmarshal(data, &envelope) == nil &&
			len(envelope.Response.Output) == 0 && !rawPresent(envelope.Response.Error)
	case "response.output_item.added", "response.output_item.done":
		var envelope struct {
			Item struct {
				Type string `json:"type"`
			} `json:"item"`
		}
		return json.Unmarshal(data, &envelope) == nil &&
			(envelope.Item.Type == "message" || envelope.Item.Type == "reasoning")
	case "response.output_text.delta", "response.output_text.done",
		"response.refusal.delta", "response.refusal.done",
		"response.reasoning_text.delta", "response.reasoning_text.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.content_part.added", "response.content_part.done":
		return true
	default:
		return false
	}
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

func (stream *ProviderStream) TerminalReceived() bool {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.terminal != nil && !stream.failed
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
		if stream.notificationFailure != nil {
			// Do not repeat the already delivered notification. The downstream
			// error boundary emits a failed terminal with the native error.
			return nil, stream.notificationFailure
		}
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonTruncatedEventStream,
			"$",
			errors.New("Responses stream has no terminal event"),
		)
	}
	stream.finished = true
	return newProviderPendingTerminal(stream.held.Bytes(), stream.terminal.Clone()), nil
}

// Same-dialect replay state and event indexes belong to the native client.
// Only an explicitly configured model alias is rewritten. SSE framing may be
// normalized, but the native payload is otherwise preserved verbatim.
func (stream *ProviderStream) clientEvent(event ssewire.Event) (ssewire.Event, error) {
	if stream.request.RequestedModel == stream.request.EffectiveModel {
		return event, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(event.Data, &root); err != nil {
		return ssewire.Event{}, err
	}
	if raw := root["response"]; rawPresent(raw) {
		var response map[string]json.RawMessage
		if err := json.Unmarshal(raw, &response); err != nil {
			return ssewire.Event{}, err
		}
		if model, present := response["model"]; present {
			var reported string
			if err := json.Unmarshal(model, &reported); err != nil || reported == "" {
				return ssewire.Event{}, protocolcore.NewFailure(protocolcore.ReasonMalformedEventStream, "$.response.model", errors.New("Responses SSE response model is invalid"))
			}
			if reported != stream.request.RequestedModel {
				response["model"], _ = json.Marshal(stream.request.RequestedModel)
				root["response"], _ = json.Marshal(response)
				event.Data, _ = json.Marshal(root)
			}
		}
	}
	return event, nil
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
