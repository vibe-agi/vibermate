package openairesponses

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

type requestWire struct {
	Model             string            `json:"model"`
	Input             []json.RawMessage `json:"input"`
	MaxOutputTokens   *int64            `json:"max_output_tokens,omitempty"`
	ToolChoice        json.RawMessage   `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
	Reasoning         json.RawMessage   `json:"reasoning,omitempty"`
	Background        *bool             `json:"background,omitempty"`
	Store             *bool             `json:"store,omitempty"`
	Stream            bool              `json:"stream,omitempty"`
	Include           []string          `json:"include,omitempty"`
	PromptCacheKey    string            `json:"prompt_cache_key,omitempty"`
	Text              json.RawMessage   `json:"text,omitempty"`
	ClientMetadata    json.RawMessage   `json:"client_metadata,omitempty"`
	Tools             []json.RawMessage `json:"tools,omitempty"`
}

type inputTypeWire struct {
	Type string `json:"type"`
}

type additionalToolsWire struct {
	Type  string            `json:"type"`
	Role  string            `json:"role"`
	Tools []json.RawMessage `json:"tools"`
	ID    string            `json:"id,omitempty"`
}

type inputMessageWire struct {
	Type             string          `json:"type"`
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	Phase            string          `json:"phase,omitempty"`
	InternalMetadata json.RawMessage `json:"internal_chat_message_metadata_passthrough,omitempty"`
}

type internalMessageMetadataWire struct {
	TurnID string `json:"turn_id"`
}

type inputContentWire struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Refusal string `json:"refusal,omitempty"`
}

type functionCallWire struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Namespace        string          `json:"namespace,omitempty"`
	Arguments        string          `json:"arguments"`
	Status           string          `json:"status,omitempty"`
	InternalMetadata json.RawMessage `json:"internal_chat_message_metadata_passthrough,omitempty"`
}

type functionCallOutputWire struct {
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	CallID           string          `json:"call_id"`
	Output           json.RawMessage `json:"output"`
	Status           string          `json:"status,omitempty"`
	InternalMetadata json.RawMessage `json:"internal_chat_message_metadata_passthrough,omitempty"`
}

type customToolCallWire struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Namespace        string          `json:"namespace,omitempty"`
	Input            string          `json:"input"`
	InternalMetadata json.RawMessage `json:"internal_chat_message_metadata_passthrough,omitempty"`
}

type customToolCallOutputWire struct {
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	CallID           string          `json:"call_id"`
	Output           json.RawMessage `json:"output"`
	InternalMetadata json.RawMessage `json:"internal_chat_message_metadata_passthrough,omitempty"`
}

type toolOutputContentWire struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolTypeWire struct {
	Type string `json:"type"`
}

type functionToolWire struct {
	Type           string          `json:"type"`
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	Parameters     json.RawMessage `json:"parameters"`
	Strict         *bool           `json:"strict,omitempty"`
	OutputSchema   json.RawMessage `json:"output_schema,omitempty"`
	DeferLoading   *bool           `json:"defer_loading,omitempty"`
	AllowedCallers []string        `json:"allowed_callers,omitempty"`
}

type customToolWire struct {
	Type           string          `json:"type"`
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	Format         json.RawMessage `json:"format,omitempty"`
	DeferLoading   *bool           `json:"defer_loading,omitempty"`
	AllowedCallers []string        `json:"allowed_callers,omitempty"`
}

type customFormatWire struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax,omitempty"`
	Definition string `json:"definition,omitempty"`
}

type namespaceToolWire struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Tools       []json.RawMessage `json:"tools"`
}

type reasoningWire struct {
	Context         string `json:"context,omitempty"`
	Effort          string `json:"effort,omitempty"`
	Summary         string `json:"summary,omitempty"`
	Mode            string `json:"mode,omitempty"`
	GenerateSummary string `json:"generate_summary,omitempty"`
}

type textWire struct {
	Verbosity string          `json:"verbosity,omitempty"`
	Format    json.RawMessage `json:"format,omitempty"`
}

func (codec *Codec) DecodeClientRequest(
	body []byte,
) (protocolcore.Request, protocolcore.TranslationReport, error) {
	if codec == nil ||
		len(body) == 0 ||
		len(body) > codec.options.MaxRequestBytes {
		return protocolcore.Request{}, protocolcore.TranslationReport{},
			invalidClient("$", errors.New("request body has an invalid size"))
	}

	var wire requestWire
	if err := decodeStrict(body, &wire); err != nil {
		return protocolcore.Request{}, protocolcore.TranslationReport{},
			invalidClient("$", err)
	}
	if wire.Store != nil && *wire.Store {
		return protocolcore.Request{}, protocolcore.TranslationReport{},
			invalidClient(
				"$.store",
				errors.New("stored Responses state is unsupported"),
			)
	}
	if wire.Background != nil && *wire.Background {
		return protocolcore.Request{}, protocolcore.TranslationReport{},
			invalidClient(
				"$.background",
				errors.New("background Responses execution is unsupported"),
			)
	}
	maxOutputTokens := 0
	if wire.MaxOutputTokens != nil {
		if *wire.MaxOutputTokens <= 0 || *wire.MaxOutputTokens > int64(math.MaxInt) {
			return protocolcore.Request{}, protocolcore.TranslationReport{},
				invalidClient(
					"$.max_output_tokens",
					errors.New("maximum output token count is invalid"),
				)
		}
		maxOutputTokens = int(*wire.MaxOutputTokens)
	}

	report := protocolcore.TranslationReport{}
	messages := make([]protocolcore.Message, 0, len(wire.Input))
	tools := make([]protocolcore.ToolDefinition, 0, len(wire.Tools))
	namespaces := make([]protocolcore.ToolNamespace, 0)
	for index, raw := range wire.Input {
		decodedMessages, decodedTools, decodedNamespaces, itemReport, err :=
			codec.decodeInputItem(index, raw)
		if err != nil {
			return protocolcore.Request{}, report, err
		}
		messages = append(messages, decodedMessages...)
		tools = append(tools, decodedTools...)
		namespaces = append(namespaces, decodedNamespaces...)
		report = report.Merge(itemReport)
	}
	for index, raw := range wire.Tools {
		tool, namespace, err := codec.decodeTool(
			raw,
			fmt.Sprintf("$.tools[%d]", index),
			true,
		)
		if err != nil {
			return protocolcore.Request{}, report, err
		}
		if namespace != nil {
			namespaces = append(namespaces, *namespace)
		} else {
			tools = append(tools, tool)
		}
	}

	toolChoice, err := decodeToolChoice(
		wire.ToolChoice,
		wire.ParallelToolCalls,
	)
	if err != nil {
		return protocolcore.Request{}, report, err
	}
	reasoning, reasoningReport, err := decodeReasoning(wire.Reasoning)
	if err != nil {
		return protocolcore.Request{}, report, err
	}
	report = report.Merge(reasoningReport)
	verbosity, textReport, err := decodeText(wire.Text)
	if err != nil {
		return protocolcore.Request{}, report, err
	}
	report = report.Merge(textReport)
	includeReport, err := decodeInclude(wire.Include)
	if err != nil {
		return protocolcore.Request{}, report, err
	}
	report = report.Merge(includeReport)
	if wire.PromptCacheKey != "" {
		if err := validateBoundedString(
			wire.PromptCacheKey,
			512,
			false,
		); err != nil {
			return protocolcore.Request{}, report,
				invalidClient("$.prompt_cache_key", err)
		}
		report = report.Merge(notice(
			protocolcore.NoticePromptCacheKeyNotForwarded,
			"$.prompt_cache_key",
		))
	}
	if rawPresent(wire.ClientMetadata) {
		if _, err := protocolcore.NewJSONObject(
			wire.ClientMetadata,
			MaxMetadataBytes,
		); err != nil {
			return protocolcore.Request{}, report,
				invalidClient("$.client_metadata", err)
		}
		report = report.Merge(notice(
			protocolcore.NoticeClientMetadataNotForwarded,
			"$.client_metadata",
		))
	}

	request := protocolcore.Request{
		RequestedModel:  wire.Model,
		EffectiveModel:  wire.Model,
		MaxOutputTokens: maxOutputTokens,
		Stream:          wire.Stream,
		Messages:        messages,
		Tools:           tools,
		ToolNamespaces:  namespaces,
		ToolChoice:      toolChoice,
		Reasoning:       reasoning,
		OutputVerbosity: verbosity,
	}
	if err := request.Validate(); err != nil {
		return protocolcore.Request{}, report, invalidClient("$", err)
	}
	return request.Clone(), report, nil
}

func (codec *Codec) decodeInputItem(
	index int,
	raw json.RawMessage,
) (
	[]protocolcore.Message,
	[]protocolcore.ToolDefinition,
	[]protocolcore.ToolNamespace,
	protocolcore.TranslationReport,
	error,
) {
	path := fmt.Sprintf("$.input[%d]", index)
	kind, err := peekType(raw)
	if err != nil {
		return nil, nil, nil, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	switch kind {
	case "message":
		message, report, err := decodeInputMessage(raw, path)
		return singleton(message), nil, nil, report, err
	case "function_call":
		message, report, err := decodeFunctionCall(raw, path)
		return singleton(message), nil, nil, report, err
	case "function_call_output":
		message, report, err := decodeFunctionCallOutput(raw, path)
		return singleton(message), nil, nil, report, err
	case "custom_tool_call":
		message, report, err := decodeCustomToolCall(raw, path)
		return singleton(message), nil, nil, report, err
	case "custom_tool_call_output":
		message, report, err := decodeCustomToolCallOutput(raw, path)
		return singleton(message), nil, nil, report, err
	case "additional_tools":
		var wire additionalToolsWire
		if err := decodeStrict(raw, &wire); err != nil {
			return nil, nil, nil, protocolcore.TranslationReport{},
				invalidClient(path, err)
		}
		if wire.Role != "developer" || len(wire.Tools) == 0 {
			return nil, nil, nil, protocolcore.TranslationReport{},
				invalidClient(path, errors.New("additional tools item is invalid"))
		}
		tools := make([]protocolcore.ToolDefinition, 0, len(wire.Tools))
		namespaces := make([]protocolcore.ToolNamespace, 0)
		for toolIndex, rawTool := range wire.Tools {
			tool, namespace, err := codec.decodeTool(
				rawTool,
				fmt.Sprintf("%s.tools[%d]", path, toolIndex),
				true,
			)
			if err != nil {
				return nil, nil, nil, protocolcore.TranslationReport{}, err
			}
			if namespace != nil {
				namespaces = append(namespaces, *namespace)
			} else {
				tools = append(tools, tool)
			}
		}
		return nil, tools, namespaces, notice(
			protocolcore.NoticeToolPlacementNormalized,
			path,
		), nil
	default:
		return nil, nil, nil, protocolcore.TranslationReport{},
			invalidClient(
				path+".type",
				errors.New("Responses input item type is unsupported"),
			)
	}
}

func decodeInputMessage(
	raw json.RawMessage,
	path string,
) (
	protocolcore.Message,
	protocolcore.TranslationReport,
	error,
) {
	var wire inputMessageWire
	if err := decodeStrict(raw, &wire); err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	var role protocolcore.Role
	switch wire.Role {
	case "system":
		role = protocolcore.RoleSystem
	case "developer":
		role = protocolcore.RoleDeveloper
	case "user":
		role = protocolcore.RoleUser
	case "assistant":
		role = protocolcore.RoleAssistant
	default:
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(
				path+".role",
				errors.New("Responses message role is unsupported"),
			)
	}
	if wire.Phase != "" {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(
				path+".phase",
				errors.New("Responses assistant phase is unsupported"),
			)
	}
	metadataReport, err := decodeInternalMessageMetadata(
		wire.InternalMetadata,
		path+".internal_chat_message_metadata_passthrough",
	)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, err
	}
	blocks, err := decodeMessageContent(wire.Content, role, path+".content")
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, err
	}
	message := protocolcore.Message{Role: role, Blocks: blocks}
	if err := message.Validate(); err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	return message, metadataReport, nil
}

func decodeInternalMessageMetadata(
	raw json.RawMessage,
	path string,
) (protocolcore.TranslationReport, error) {
	if !rawPresent(raw) {
		return protocolcore.TranslationReport{}, nil
	}
	if _, err := protocolcore.NewJSONObject(raw, MaxMetadataBytes); err != nil {
		return protocolcore.TranslationReport{}, invalidClient(path, err)
	}
	var wire internalMessageMetadataWire
	if err := decodeStrict(raw, &wire); err != nil {
		return protocolcore.TranslationReport{}, invalidClient(path, err)
	}
	if err := validateBoundedString(wire.TurnID, 512, false); err != nil {
		return protocolcore.TranslationReport{},
			invalidClient(path+".turn_id", err)
	}
	return notice(
		protocolcore.NoticeInternalMessageMetadataNotForwarded,
		path,
	), nil
}

func decodeMessageContent(
	raw json.RawMessage,
	role protocolcore.Role,
	path string,
) ([]protocolcore.ContentBlock, error) {
	if len(raw) == 0 {
		return nil, invalidClient(path, errors.New("message content is missing"))
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		block, blockErr := protocolcore.NewTextBlock(text)
		if blockErr != nil {
			return nil, invalidClient(path, blockErr)
		}
		return []protocolcore.ContentBlock{block}, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) == 0 {
		return nil, invalidClient(
			path,
			errors.New("message content must be text or a nonempty array"),
		)
	}
	blocks := make([]protocolcore.ContentBlock, len(parts))
	for index, rawPart := range parts {
		var part inputContentWire
		if err := decodeStrict(rawPart, &part); err != nil {
			return nil, invalidClient(
				fmt.Sprintf("%s[%d]", path, index),
				err,
			)
		}
		var block protocolcore.ContentBlock
		var err error
		switch part.Type {
		case "input_text", "output_text":
			if part.Refusal != "" {
				return nil, invalidClient(
					fmt.Sprintf("%s[%d]", path, index),
					errors.New("text content contains a refusal"),
				)
			}
			block, err = protocolcore.NewTextBlock(part.Text)
		case "refusal":
			if role != protocolcore.RoleAssistant {
				return nil, invalidClient(
					fmt.Sprintf("%s[%d].type", path, index),
					errors.New("refusal content requires an assistant role"),
				)
			}
			if part.Text != "" {
				return nil, invalidClient(
					fmt.Sprintf("%s[%d]", path, index),
					errors.New("refusal content contains text"),
				)
			}
			block, err = protocolcore.NewRefusalBlock(part.Refusal)
		default:
			return nil, invalidClient(
				fmt.Sprintf("%s[%d].type", path, index),
				errors.New("Responses content type is unsupported"),
			)
		}
		if err != nil {
			return nil, invalidClient(fmt.Sprintf("%s[%d]", path, index), err)
		}
		blocks[index] = block
	}
	return blocks, nil
}

func decodeFunctionCall(
	raw json.RawMessage,
	path string,
) (
	protocolcore.Message,
	protocolcore.TranslationReport,
	error,
) {
	var wire functionCallWire
	if err := decodeStrict(raw, &wire); err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	metadataReport, err := decodeInternalMessageMetadata(
		wire.InternalMetadata,
		path+".internal_chat_message_metadata_passthrough",
	)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, err
	}
	if wire.Status != "" && wire.Status != "completed" {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(
				path+".status",
				errors.New("function call is not complete"),
			)
	}
	arguments, err := protocolcore.NewJSONObject(
		[]byte(wire.Arguments),
		protocolcore.MaxToolJSONBytes,
	)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path+".arguments", err)
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
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	block, err := protocolcore.NewToolCallBlock(call)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	return protocolcore.Message{
		Role:   protocolcore.RoleAssistant,
		Blocks: []protocolcore.ContentBlock{block},
	}, metadataReport, nil
}

func decodeCustomToolCall(
	raw json.RawMessage,
	path string,
) (
	protocolcore.Message,
	protocolcore.TranslationReport,
	error,
) {
	var wire customToolCallWire
	if err := decodeStrict(raw, &wire); err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	metadataReport, err := decodeInternalMessageMetadata(
		wire.InternalMetadata,
		path+".internal_chat_message_metadata_passthrough",
	)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, err
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
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	block, err := protocolcore.NewToolCallBlock(call)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	return protocolcore.Message{
		Role:   protocolcore.RoleAssistant,
		Blocks: []protocolcore.ContentBlock{block},
	}, metadataReport, nil
}

func decodeFunctionCallOutput(
	raw json.RawMessage,
	path string,
) (
	protocolcore.Message,
	protocolcore.TranslationReport,
	error,
) {
	var wire functionCallOutputWire
	if err := decodeStrict(raw, &wire); err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	metadataReport, err := decodeInternalMessageMetadata(
		wire.InternalMetadata,
		path+".internal_chat_message_metadata_passthrough",
	)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, err
	}
	if wire.Status != "" && wire.Status != "completed" {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(
				path+".status",
				errors.New("function call output is not complete"),
			)
	}
	message, outputReport, err := decodeToolOutput(
		wire.CallID,
		wire.Output,
		path,
	)
	return message, metadataReport.Merge(outputReport), err
}

func decodeCustomToolCallOutput(
	raw json.RawMessage,
	path string,
) (
	protocolcore.Message,
	protocolcore.TranslationReport,
	error,
) {
	var wire customToolCallOutputWire
	if err := decodeStrict(raw, &wire); err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	metadataReport, err := decodeInternalMessageMetadata(
		wire.InternalMetadata,
		path+".internal_chat_message_metadata_passthrough",
	)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, err
	}
	message, outputReport, err := decodeToolOutput(
		wire.CallID,
		wire.Output,
		path,
	)
	return message, metadataReport.Merge(outputReport), err
}

func decodeToolOutput(
	callID string,
	raw json.RawMessage,
	path string,
) (
	protocolcore.Message,
	protocolcore.TranslationReport,
	error,
) {
	output, report, err := decodeToolOutputText(raw, path+".output")
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, err
	}
	key, err := protocolcore.NewCallKey("openai-responses-call", callID)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path+".call_id", err)
	}
	block, err := protocolcore.NewToolResultBlock(protocolcore.ToolResult{
		Key:     key,
		Content: output,
	})
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	return protocolcore.Message{
		Role:   protocolcore.RoleTool,
		Blocks: []protocolcore.ContentBlock{block},
	}, report, nil
}

func decodeToolOutputText(
	raw json.RawMessage,
	path string,
) (string, protocolcore.TranslationReport, error) {
	var output string
	if json.Unmarshal(raw, &output) == nil {
		return output, protocolcore.TranslationReport{}, nil
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(raw, &rawItems); err != nil ||
		len(rawItems) == 0 {
		return "", protocolcore.TranslationReport{}, invalidClient(
			path,
			errors.New(
				"tool output must be text or a nonempty content array",
			),
		)
	}
	var normalized strings.Builder
	for index, rawItem := range rawItems {
		var item toolOutputContentWire
		if err := decodeStrict(rawItem, &item); err != nil {
			return "", protocolcore.TranslationReport{},
				invalidClient(
					fmt.Sprintf("%s[%d]", path, index),
					err,
				)
		}
		if item.Type != "input_text" {
			return "", protocolcore.TranslationReport{},
				invalidClient(
					fmt.Sprintf("%s[%d].type", path, index),
					errors.New(
						"tool output content type is unsupported",
					),
				)
		}
		if index > 0 {
			normalized.WriteByte('\n')
		}
		normalized.WriteString(item.Text)
	}
	return normalized.String(), notice(
		protocolcore.NoticeToolOutputContentNormalized,
		path,
	), nil
}

func newToolCall(
	kind protocolcore.ToolKind,
	itemID string,
	callID string,
	namespace string,
	name string,
	arguments protocolcore.JSONDocument,
	input string,
) (protocolcore.ToolCall, error) {
	key, err := protocolcore.NewCallKey("openai-responses-call", callID)
	if err != nil {
		return protocolcore.ToolCall{}, err
	}
	var itemKey protocolcore.CallKey
	if itemID != "" {
		itemKey, err = protocolcore.NewCallKey(
			"openai-responses-item",
			itemID,
		)
		if err != nil {
			return protocolcore.ToolCall{}, err
		}
	}
	call := protocolcore.ToolCall{
		Kind:      kind,
		Key:       key,
		ItemKey:   itemKey,
		Namespace: namespace,
		Name:      name,
		Arguments: arguments,
		Input:     input,
	}
	if err := call.Validate(); err != nil {
		return protocolcore.ToolCall{}, err
	}
	return call, nil
}

func (codec *Codec) decodeTool(
	raw json.RawMessage,
	path string,
	allowNamespace bool,
) (
	protocolcore.ToolDefinition,
	*protocolcore.ToolNamespace,
	error,
) {
	kind, err := peekType(raw)
	if err != nil {
		return protocolcore.ToolDefinition{}, nil, invalidClient(path, err)
	}
	switch kind {
	case "function":
		tool, err := decodeFunctionTool(raw, path)
		return tool, nil, err
	case "custom":
		tool, err := decodeCustomTool(raw, path)
		return tool, nil, err
	case "namespace":
		if !allowNamespace {
			return protocolcore.ToolDefinition{}, nil, invalidClient(
				path+".type",
				errors.New("nested tool namespaces are unsupported"),
			)
		}
		var wire namespaceToolWire
		if err := decodeStrict(raw, &wire); err != nil {
			return protocolcore.ToolDefinition{}, nil, invalidClient(path, err)
		}
		namespace := protocolcore.ToolNamespace{
			Name:        wire.Name,
			Description: wire.Description,
			Tools:       make([]protocolcore.ToolDefinition, len(wire.Tools)),
		}
		for index, rawTool := range wire.Tools {
			tool, nested, err := codec.decodeTool(
				rawTool,
				fmt.Sprintf("%s.tools[%d]", path, index),
				false,
			)
			if err != nil {
				return protocolcore.ToolDefinition{}, nil, err
			}
			if nested != nil {
				return protocolcore.ToolDefinition{}, nil, invalidClient(
					path,
					errors.New("nested tool namespace is invalid"),
				)
			}
			namespace.Tools[index] = tool
		}
		if err := namespace.Validate(); err != nil {
			return protocolcore.ToolDefinition{}, nil, invalidClient(path, err)
		}
		return protocolcore.ToolDefinition{}, &namespace, nil
	default:
		return protocolcore.ToolDefinition{}, nil, invalidClient(
			path+".type",
			errors.New("Responses tool type is unsupported"),
		)
	}
}

func decodeFunctionTool(
	raw json.RawMessage,
	path string,
) (protocolcore.ToolDefinition, error) {
	var wire functionToolWire
	if err := decodeStrict(raw, &wire); err != nil {
		return protocolcore.ToolDefinition{}, invalidClient(path, err)
	}
	if rawPresent(wire.OutputSchema) ||
		(wire.DeferLoading != nil && *wire.DeferLoading) ||
		len(wire.AllowedCallers) != 0 {
		return protocolcore.ToolDefinition{}, invalidClient(
			path,
			errors.New("function tool capability is unsupported"),
		)
	}
	schema, err := protocolcore.NewJSONObject(
		wire.Parameters,
		protocolcore.MaxToolJSONBytes,
	)
	if err != nil {
		return protocolcore.ToolDefinition{}, invalidClient(
			path+".parameters",
			err,
		)
	}
	tool := protocolcore.ToolDefinition{
		Kind:        protocolcore.ToolKindFunction,
		Name:        wire.Name,
		Description: wire.Description,
		InputSchema: schema,
	}
	if wire.Strict != nil {
		tool.StrictKnown = true
		tool.Strict = *wire.Strict
	}
	if err := tool.Validate(); err != nil {
		return protocolcore.ToolDefinition{}, invalidClient(path, err)
	}
	return tool, nil
}

func decodeCustomTool(
	raw json.RawMessage,
	path string,
) (protocolcore.ToolDefinition, error) {
	var wire customToolWire
	if err := decodeStrict(raw, &wire); err != nil {
		return protocolcore.ToolDefinition{}, invalidClient(path, err)
	}
	if (wire.DeferLoading != nil && *wire.DeferLoading) ||
		len(wire.AllowedCallers) != 0 {
		return protocolcore.ToolDefinition{}, invalidClient(
			path,
			errors.New("custom tool capability is unsupported"),
		)
	}
	format := protocolcore.CustomToolFormat{
		Kind: protocolcore.CustomToolFormatText,
	}
	if rawPresent(wire.Format) {
		var decoded customFormatWire
		if err := decodeStrict(wire.Format, &decoded); err != nil {
			return protocolcore.ToolDefinition{}, invalidClient(
				path+".format",
				err,
			)
		}
		format = protocolcore.CustomToolFormat{
			Kind:       protocolcore.CustomToolFormatKind(decoded.Type),
			Syntax:     decoded.Syntax,
			Definition: decoded.Definition,
		}
	}
	tool := protocolcore.ToolDefinition{
		Kind:         protocolcore.ToolKindCustom,
		Name:         wire.Name,
		Description:  wire.Description,
		CustomFormat: format,
	}
	if err := tool.Validate(); err != nil {
		return protocolcore.ToolDefinition{}, invalidClient(path, err)
	}
	return tool, nil
}

func decodeToolChoice(
	raw json.RawMessage,
	parallel *bool,
) (protocolcore.ToolChoice, error) {
	choice := protocolcore.ToolChoice{}
	if rawPresent(raw) {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return protocolcore.ToolChoice{}, invalidClient(
				"$.tool_choice",
				errors.New("object tool choices are unsupported"),
			)
		}
		switch value {
		case "auto":
			choice.Mode = protocolcore.ToolChoiceAuto
		case "required":
			choice.Mode = protocolcore.ToolChoiceRequired
		case "none":
			choice.Mode = protocolcore.ToolChoiceNone
		default:
			return protocolcore.ToolChoice{}, invalidClient(
				"$.tool_choice",
				errors.New("tool choice is unsupported"),
			)
		}
	}
	if parallel != nil && !*parallel {
		choice.DisableParallel = true
	}
	return choice, nil
}

func decodeReasoning(
	raw json.RawMessage,
) (
	protocolcore.ReasoningIntent,
	protocolcore.TranslationReport,
	error,
) {
	if !rawPresent(raw) {
		return protocolcore.ReasoningIntent{},
			protocolcore.TranslationReport{}, nil
	}
	var wire reasoningWire
	if err := decodeStrict(raw, &wire); err != nil {
		return protocolcore.ReasoningIntent{},
			protocolcore.TranslationReport{},
			invalidClient("$.reasoning", err)
	}
	if wire.GenerateSummary != "" {
		return protocolcore.ReasoningIntent{},
			protocolcore.TranslationReport{},
			invalidClient(
				"$.reasoning.generate_summary",
				errors.New("deprecated reasoning summary is unsupported"),
			)
	}
	intent := protocolcore.ReasoningIntent{
		Context:   protocolcore.ReasoningContext(wire.Context),
		Effort:    protocolcore.ReasoningEffort(wire.Effort),
		Summary:   protocolcore.ReasoningSummary(wire.Summary),
		Execution: protocolcore.ReasoningExecutionMode(wire.Mode),
	}
	report := protocolcore.TranslationReport{}
	if intent.Context != "" {
		report = report.Merge(notice(
			protocolcore.NoticeReasoningContextNotForwarded,
			"$.reasoning.context",
		))
	}
	return intent, report, nil
}

func decodeText(
	raw json.RawMessage,
) (protocolcore.TextVerbosity, protocolcore.TranslationReport, error) {
	if !rawPresent(raw) {
		return "", protocolcore.TranslationReport{}, nil
	}
	var wire textWire
	if err := decodeStrict(raw, &wire); err != nil {
		return "", protocolcore.TranslationReport{},
			invalidClient("$.text", err)
	}
	if rawPresent(wire.Format) {
		return "", protocolcore.TranslationReport{},
			invalidClient(
				"$.text.format",
				errors.New("Responses text format is unsupported"),
			)
	}
	verbosity := protocolcore.TextVerbosity(wire.Verbosity)
	report := protocolcore.TranslationReport{}
	if verbosity != "" {
		report = notice(
			protocolcore.NoticeTextVerbosityNotForwarded,
			"$.text.verbosity",
		)
	}
	return verbosity, report, nil
}

func decodeInclude(
	values []string,
) (protocolcore.TranslationReport, error) {
	if len(values) == 0 {
		return protocolcore.TranslationReport{}, nil
	}
	seen := make(map[string]struct{}, len(values))
	report := protocolcore.TranslationReport{}
	for index, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return protocolcore.TranslationReport{}, invalidClient(
				fmt.Sprintf("$.include[%d]", index),
				errors.New("Responses include value is duplicated"),
			)
		}
		seen[value] = struct{}{}
		if value != "reasoning.encrypted_content" {
			return protocolcore.TranslationReport{}, invalidClient(
				fmt.Sprintf("$.include[%d]", index),
				errors.New("Responses include value is unsupported"),
			)
		}
		report = report.Merge(notice(
			protocolcore.NoticeReasoningIncludeNotForwarded,
			fmt.Sprintf("$.include[%d]", index),
		))
	}
	return report, nil
}

func notice(
	code protocolcore.NoticeCode,
	path string,
) protocolcore.TranslationReport {
	return protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
		Code: code,
		Path: path,
	})
}

func singleton(message protocolcore.Message) []protocolcore.Message {
	if len(message.Blocks) == 0 {
		return nil
	}
	return []protocolcore.Message{message}
}

func validateBoundedString(value string, maxBytes int, allowEmpty bool) error {
	if !allowEmpty && value == "" {
		return errors.New("value is empty")
	}
	if !utf8.ValidString(value) {
		return errors.New("value is not valid UTF-8")
	}
	if len(value) > maxBytes {
		return errors.New("value exceeds its byte limit")
	}
	if strings.TrimSpace(value) != value {
		return errors.New("value has surrounding whitespace")
	}
	return nil
}
