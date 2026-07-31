package anthropicchat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

type anthropicRequestWire struct {
	Model         string                        `json:"model"`
	MaxTokens     int                           `json:"max_tokens"`
	Messages      []anthropicMessageWire        `json:"messages"`
	System        json.RawMessage               `json:"system,omitempty"`
	Metadata      json.RawMessage               `json:"metadata,omitempty"`
	StopSequences []string                      `json:"stop_sequences,omitempty"`
	Stream        bool                          `json:"stream,omitempty"`
	Temperature   *float64                      `json:"temperature,omitempty"`
	TopP          *float64                      `json:"top_p,omitempty"`
	TopK          *int                          `json:"top_k,omitempty"`
	Tools         []anthropicToolDefinitionWire `json:"tools,omitempty"`
	ToolChoice    *anthropicToolChoiceWire      `json:"tool_choice,omitempty"`
	Thinking      json.RawMessage               `json:"thinking,omitempty"`
	OutputConfig  json.RawMessage               `json:"output_config,omitempty"`
	Context       json.RawMessage               `json:"context_management,omitempty"`
	Diagnostics   json.RawMessage               `json:"diagnostics,omitempty"`
	ServiceTier   string                        `json:"service_tier,omitempty"`
}

type anthropicMessageWire struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTextBlockWire struct {
	Type         string          `json:"type"`
	Text         string          `json:"text"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type anthropicToolUseBlockWire struct {
	Type         string          `json:"type"`
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Input        json.RawMessage `json:"input"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type anthropicToolResultBlockWire struct {
	Type         string          `json:"type"`
	ToolUseID    string          `json:"tool_use_id"`
	Content      json.RawMessage `json:"content,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type anthropicToolDefinitionWire struct {
	Name                string          `json:"name"`
	Description         string          `json:"description,omitempty"`
	InputSchema         json.RawMessage `json:"input_schema"`
	CacheControl        json.RawMessage `json:"cache_control,omitempty"`
	Type                string          `json:"type,omitempty"`
	EagerInputStreaming *bool           `json:"eager_input_streaming,omitempty"`
}

type anthropicToolChoiceWire struct {
	Type               string `json:"type"`
	Name               string `json:"name,omitempty"`
	DisableParallelUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

type anthropicThinkingWire struct {
	Type         string `json:"type"`
	BudgetTokens *int   `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"`
}

type anthropicOutputConfigWire struct {
	Effort     string          `json:"effort,omitempty"`
	Format     json.RawMessage `json:"format,omitempty"`
	TaskBudget json.RawMessage `json:"task_budget,omitempty"`
}

type anthropicJSONOutputFormatWire struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

type anthropicTaskBudgetWire struct {
	Type      string `json:"type"`
	Total     int    `json:"total"`
	Remaining *int   `json:"remaining,omitempty"`
}

type anthropicContextManagementWire struct {
	Edits []json.RawMessage `json:"edits,omitempty"`
}

type anthropicClearThinkingWire struct {
	Type string          `json:"type"`
	Keep json.RawMessage `json:"keep,omitempty"`
}

type anthropicThinkingTurnsWire struct {
	Type  string `json:"type"`
	Value int    `json:"value,omitempty"`
}

type anthropicDiagnosticsWire struct {
	PreviousMessageID json.RawMessage `json:"previous_message_id,omitempty"`
}

func (codec *Codec) DecodeClientRequest(
	body []byte,
) (protocolcore.Request, protocolcore.TranslationReport, error) {
	if len(body) == 0 || len(body) > codec.options.MaxRequestBytes {
		return protocolcore.Request{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$",
				errors.New("request body has an invalid size"),
			)
	}

	var wire anthropicRequestWire
	if err := decodeStrict(body, &wire); err != nil {
		return protocolcore.Request{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	report := protocolcore.TranslationReport{}

	system, systemReport, err := codec.decodeSystem(wire.System)
	if err != nil {
		return protocolcore.Request{}, report, err
	}
	report = report.Merge(systemReport)

	messages := make([]protocolcore.Message, len(wire.Messages))
	for index, message := range wire.Messages {
		decoded, messageReport, decodeErr := codec.decodeMessage(index, message)
		if decodeErr != nil {
			return protocolcore.Request{}, report, decodeErr
		}
		messages[index] = decoded
		report = report.Merge(messageReport)
	}

	tools := make([]protocolcore.ToolDefinition, len(wire.Tools))
	for index, tool := range wire.Tools {
		decoded, toolReport, decodeErr := codec.decodeToolDefinition(index, tool)
		if decodeErr != nil {
			return protocolcore.Request{}, report, decodeErr
		}
		tools[index] = decoded
		report = report.Merge(toolReport)
	}

	toolChoice, err := decodeToolChoice(wire.ToolChoice)
	if err != nil {
		return protocolcore.Request{}, report, err
	}
	reasoning, output, err := decodeOutputConfiguration(
		wire.Thinking,
		wire.OutputConfig,
	)
	if err != nil {
		return protocolcore.Request{}, report, err
	}
	contextIntent, err := decodeContextManagement(wire.Context)
	if err != nil {
		return protocolcore.Request{}, report, err
	}
	diagnostics, err := decodeDiagnostics(wire.Diagnostics)
	if err != nil {
		return protocolcore.Request{}, report, err
	}
	if rawPresent(wire.Metadata) {
		if !json.Valid(wire.Metadata) {
			return protocolcore.Request{}, report, protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$.metadata",
				errors.New("metadata is invalid JSON"),
			)
		}
		report = report.Merge(protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
			Code: protocolcore.NoticeMetadataNotForwarded,
			Path: "$.metadata",
		}))
	}
	if wire.TopK != nil {
		report = report.Merge(protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
			Code: protocolcore.NoticeTopKNotForwarded,
			Path: "$.top_k",
		}))
	}
	if wire.ServiceTier != "" {
		report = report.Merge(protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
			Code: protocolcore.NoticeServiceTierNotForwarded,
			Path: "$.service_tier",
		}))
	}

	request := protocolcore.Request{
		RequestedModel:  wire.Model,
		EffectiveModel:  wire.Model,
		MaxOutputTokens: wire.MaxTokens,
		Stream:          wire.Stream,
		System:          system,
		Messages:        messages,
		Tools:           tools,
		ToolChoice:      toolChoice,
		Reasoning:       reasoning,
		Context:         contextIntent,
		Diagnostics:     diagnostics,
		Output:          output,
		Temperature:     wire.Temperature,
		TopP:            wire.TopP,
		StopSequences:   append([]string(nil), wire.StopSequences...),
	}
	if err := request.Validate(); err != nil {
		return protocolcore.Request{}, report, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$",
			err,
		)
	}
	return request.Clone(), report, nil
}

func decodeDiagnostics(
	raw json.RawMessage,
) (protocolcore.DiagnosticsIntent, error) {
	if !rawPresent(raw) {
		return protocolcore.DiagnosticsIntent{}, nil
	}
	var wire anthropicDiagnosticsWire
	if err := decodeStrict(raw, &wire); err != nil {
		return protocolcore.DiagnosticsIntent{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$.diagnostics",
			err,
		)
	}
	intent := protocolcore.DiagnosticsIntent{Requested: true}
	if rawPresent(wire.PreviousMessageID) {
		if err := json.Unmarshal(wire.PreviousMessageID, &intent.PreviousMessageID); err != nil {
			return protocolcore.DiagnosticsIntent{}, protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$.diagnostics.previous_message_id",
				err,
			)
		}
		intent.HasPrevious = true
	}
	if err := intent.Validate(); err != nil {
		return protocolcore.DiagnosticsIntent{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$.diagnostics.previous_message_id",
			err,
		)
	}
	return intent, nil
}

func decodeContextManagement(
	raw json.RawMessage,
) (protocolcore.ContextManagementIntent, error) {
	if !rawPresent(raw) {
		return protocolcore.ContextManagementIntent{}, nil
	}
	var wire anthropicContextManagementWire
	if err := decodeStrict(raw, &wire); err != nil {
		return protocolcore.ContextManagementIntent{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$.context_management",
			err,
		)
	}
	if len(wire.Edits) > 16 {
		return protocolcore.ContextManagementIntent{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$.context_management.edits",
			errors.New("context edit count is invalid"),
		)
	}
	intent := protocolcore.ContextManagementIntent{
		Edits: make([]protocolcore.ContextEdit, len(wire.Edits)),
	}
	for index, rawEdit := range wire.Edits {
		var edit anthropicClearThinkingWire
		if err := decodeStrict(rawEdit, &edit); err != nil {
			return protocolcore.ContextManagementIntent{}, protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				fmt.Sprintf("$.context_management.edits[%d]", index),
				err,
			)
		}
		if edit.Type != "clear_thinking_20251015" {
			return protocolcore.ContextManagementIntent{}, protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				fmt.Sprintf("$.context_management.edits[%d].type", index),
				errors.New("context edit type cannot be represented by the configured provider dialect"),
			)
		}
		decoded, err := decodeClearThinkingEdit(edit.Keep)
		if err != nil {
			return protocolcore.ContextManagementIntent{}, protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				fmt.Sprintf("$.context_management.edits[%d].keep", index),
				err,
			)
		}
		intent.Edits[index] = decoded
	}
	return intent, nil
}

func decodeClearThinkingEdit(raw json.RawMessage) (protocolcore.ContextEdit, error) {
	var keep string
	if json.Unmarshal(raw, &keep) == nil {
		if keep != "all" {
			return protocolcore.ContextEdit{}, errors.New("clear-thinking retention is unsupported")
		}
		return protocolcore.ContextEdit{
			Kind:    protocolcore.ContextEditClearThinking,
			KeepAll: true,
		}, nil
	}
	var wire anthropicThinkingTurnsWire
	if err := decodeStrict(raw, &wire); err != nil {
		return protocolcore.ContextEdit{}, err
	}
	switch wire.Type {
	case "all":
		if wire.Value != 0 {
			return protocolcore.ContextEdit{}, errors.New("all-thinking retention has a value")
		}
		return protocolcore.ContextEdit{
			Kind:    protocolcore.ContextEditClearThinking,
			KeepAll: true,
		}, nil
	case "thinking_turns":
		if wire.Value <= 0 {
			return protocolcore.ContextEdit{}, errors.New("thinking-turn retention is invalid")
		}
		return protocolcore.ContextEdit{
			Kind:              protocolcore.ContextEditClearThinking,
			KeepThinkingTurns: wire.Value,
		}, nil
	default:
		return protocolcore.ContextEdit{}, errors.New("clear-thinking retention is unsupported")
	}
}

func decodeOutputConfiguration(
	thinkingRaw json.RawMessage,
	outputConfigRaw json.RawMessage,
) (
	protocolcore.ReasoningIntent,
	protocolcore.StructuredOutputIntent,
	error,
) {
	var intent protocolcore.ReasoningIntent
	var structuredOutput protocolcore.StructuredOutputIntent
	if rawPresent(outputConfigRaw) {
		var output anthropicOutputConfigWire
		if err := decodeStrict(outputConfigRaw, &output); err != nil {
			return protocolcore.ReasoningIntent{},
				protocolcore.StructuredOutputIntent{},
				protocolcore.NewFailure(
					protocolcore.ReasonInvalidClientRequest,
					"$.output_config",
					err,
				)
		}
		if rawPresent(output.Format) {
			var format anthropicJSONOutputFormatWire
			if err := decodeStrict(output.Format, &format); err != nil {
				return protocolcore.ReasoningIntent{},
					protocolcore.StructuredOutputIntent{},
					protocolcore.NewFailure(
						protocolcore.ReasonInvalidClientRequest,
						"$.output_config.format",
						err,
					)
			}
			if format.Type != "json_schema" {
				return protocolcore.ReasoningIntent{},
					protocolcore.StructuredOutputIntent{},
					protocolcore.NewFailure(
						protocolcore.ReasonUnsupportedClientInput,
						"$.output_config.format.type",
						errors.New("structured output kind cannot be represented by the configured provider dialect"),
					)
			}
			decoded, err := protocolcore.NewJSONSchemaOutput(format.Schema)
			if err != nil {
				return protocolcore.ReasoningIntent{},
					protocolcore.StructuredOutputIntent{},
					protocolcore.NewFailure(
						protocolcore.ReasonInvalidClientRequest,
						"$.output_config.format.schema",
						err,
					)
			}
			structuredOutput = decoded
		}
		switch output.Effort {
		case "":
		case "low":
			intent.Effort = protocolcore.ReasoningEffortLow
		case "medium":
			intent.Effort = protocolcore.ReasoningEffortMedium
		case "high":
			intent.Effort = protocolcore.ReasoningEffortHigh
		case "xhigh":
			intent.Effort = protocolcore.ReasoningEffortXHigh
		case "max":
			intent.Effort = protocolcore.ReasoningEffortMax
		default:
			return protocolcore.ReasoningIntent{},
				protocolcore.StructuredOutputIntent{},
				protocolcore.NewFailure(
					protocolcore.ReasonInvalidClientRequest,
					"$.output_config.effort",
					errors.New("reasoning effort is unsupported"),
				)
		}
		if rawPresent(output.TaskBudget) {
			var budget anthropicTaskBudgetWire
			if err := decodeStrict(output.TaskBudget, &budget); err != nil {
				return protocolcore.ReasoningIntent{},
					protocolcore.StructuredOutputIntent{},
					protocolcore.NewFailure(
						protocolcore.ReasonInvalidClientRequest,
						"$.output_config.task_budget",
						err,
					)
			}
			if budget.Type != "tokens" || budget.Total <= 0 {
				return protocolcore.ReasoningIntent{},
					protocolcore.StructuredOutputIntent{},
					protocolcore.NewFailure(
						protocolcore.ReasonInvalidClientRequest,
						"$.output_config.task_budget",
						errors.New("task budget is invalid"),
					)
			}
			intent.TaskBudget = protocolcore.TaskBudget{
				Present:     true,
				TotalTokens: budget.Total,
			}
			if budget.Remaining != nil {
				intent.TaskBudget.RemainingKnown = true
				intent.TaskBudget.RemainingTokens = *budget.Remaining
			}
			if err := intent.TaskBudget.Validate(); err != nil {
				return protocolcore.ReasoningIntent{},
					protocolcore.StructuredOutputIntent{},
					protocolcore.NewFailure(
						protocolcore.ReasonInvalidClientRequest,
						"$.output_config.task_budget",
						err,
					)
			}
		}
	}
	if !rawPresent(thinkingRaw) {
		return intent, structuredOutput, nil
	}
	var thinking anthropicThinkingWire
	if err := decodeStrict(thinkingRaw, &thinking); err != nil {
		return protocolcore.ReasoningIntent{},
			protocolcore.StructuredOutputIntent{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$.thinking",
				err,
			)
	}
	switch thinking.Type {
	case "disabled":
		intent.Thinking = protocolcore.ThinkingModeDisabled
	case "adaptive":
		intent.Thinking = protocolcore.ThinkingModeAdaptive
	case "enabled":
		intent.Thinking = protocolcore.ThinkingModeEnabled
	default:
		return protocolcore.ReasoningIntent{},
			protocolcore.StructuredOutputIntent{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$.thinking.type",
				errors.New("thinking mode is unsupported"),
			)
	}
	if thinking.BudgetTokens != nil {
		intent.BudgetTokens = *thinking.BudgetTokens
	}
	switch thinking.Display {
	case "":
	case "summarized":
		intent.Display = protocolcore.ThinkingDisplaySummarized
	case "omitted":
		intent.Display = protocolcore.ThinkingDisplayOmitted
	default:
		return protocolcore.ReasoningIntent{},
			protocolcore.StructuredOutputIntent{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$.thinking.display",
				errors.New("thinking display is unsupported"),
			)
	}
	return intent, structuredOutput, nil
}

func (codec *Codec) decodeSystem(
	raw json.RawMessage,
) ([]protocolcore.ContentBlock, protocolcore.TranslationReport, error) {
	if !rawPresent(raw) {
		return nil, protocolcore.TranslationReport{}, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		block, blockErr := protocolcore.NewTextBlock(text)
		if blockErr != nil {
			return nil, protocolcore.TranslationReport{}, protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$.system",
				blockErr,
			)
		}
		return []protocolcore.ContentBlock{block}, protocolcore.TranslationReport{}, nil
	}

	var rawBlocks []json.RawMessage
	if err := json.Unmarshal(raw, &rawBlocks); err != nil {
		return nil, protocolcore.TranslationReport{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$.system",
			err,
		)
	}
	blocks := make([]protocolcore.ContentBlock, len(rawBlocks))
	report := protocolcore.TranslationReport{}
	for index, rawBlock := range rawBlocks {
		var blockWire anthropicTextBlockWire
		if err := decodeStrict(rawBlock, &blockWire); err != nil {
			return nil, report, protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				fmt.Sprintf("$.system[%d]", index),
				err,
			)
		}
		if blockWire.Type != "text" {
			return nil, report, protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				fmt.Sprintf("$.system[%d].type", index),
				errors.New("system block type is unsupported"),
			)
		}
		block, err := protocolcore.NewTextBlock(blockWire.Text)
		if err != nil {
			return nil, report, protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				fmt.Sprintf("$.system[%d].text", index),
				err,
			)
		}
		blocks[index] = block
		if rawPresent(blockWire.CacheControl) {
			report = report.Merge(cacheNotice(fmt.Sprintf("$.system[%d].cache_control", index)))
		}
	}
	return blocks, report, nil
}

func (codec *Codec) decodeMessage(
	messageIndex int,
	wire anthropicMessageWire,
) (protocolcore.Message, protocolcore.TranslationReport, error) {
	role := protocolcore.Role(wire.Role)
	if role != protocolcore.RoleUser && role != protocolcore.RoleAssistant {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				fmt.Sprintf("$.messages[%d].role", messageIndex),
				errors.New("message role is unsupported"),
			)
	}

	var text string
	if err := json.Unmarshal(wire.Content, &text); err == nil {
		block, blockErr := protocolcore.NewTextBlock(text)
		if blockErr != nil {
			return protocolcore.Message{}, protocolcore.TranslationReport{},
				protocolcore.NewFailure(
					protocolcore.ReasonInvalidClientRequest,
					fmt.Sprintf("$.messages[%d].content", messageIndex),
					blockErr,
				)
		}
		message := protocolcore.Message{Role: role, Blocks: []protocolcore.ContentBlock{block}}
		if err := message.Validate(); err != nil {
			return protocolcore.Message{}, protocolcore.TranslationReport{},
				protocolcore.NewFailure(
					protocolcore.ReasonInvalidClientRequest,
					fmt.Sprintf("$.messages[%d]", messageIndex),
					err,
				)
		}
		return message, protocolcore.TranslationReport{}, nil
	}

	var rawBlocks []json.RawMessage
	if err := json.Unmarshal(wire.Content, &rawBlocks); err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				fmt.Sprintf("$.messages[%d].content", messageIndex),
				err,
			)
	}
	if len(rawBlocks) == 0 {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				fmt.Sprintf("$.messages[%d].content", messageIndex),
				errors.New("message content is empty"),
			)
	}
	blocks := make([]protocolcore.ContentBlock, 0, len(rawBlocks))
	report := protocolcore.TranslationReport{}
	for blockIndex, rawBlock := range rawBlocks {
		path := fmt.Sprintf("$.messages[%d].content[%d]", messageIndex, blockIndex)
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(rawBlock, &header); err != nil {
			return protocolcore.Message{}, report,
				protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
		}
		switch header.Type {
		case "text":
			var blockWire anthropicTextBlockWire
			if err := decodeStrict(rawBlock, &blockWire); err != nil {
				return protocolcore.Message{}, report,
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
			}
			block, err := protocolcore.NewTextBlock(blockWire.Text)
			if err != nil {
				return protocolcore.Message{}, report,
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path+".text", err)
			}
			blocks = append(blocks, block)
			if rawPresent(blockWire.CacheControl) {
				report = report.Merge(cacheNotice(path + ".cache_control"))
			}

		case "tool_use":
			if role != protocolcore.RoleAssistant {
				return protocolcore.Message{}, report, protocolcore.NewFailure(
					protocolcore.ReasonInvalidClientRequest,
					path,
					errors.New("tool_use block is not in an assistant message"),
				)
			}
			var blockWire anthropicToolUseBlockWire
			if err := decodeStrict(rawBlock, &blockWire); err != nil {
				return protocolcore.Message{}, report,
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
			}
			key, err := protocolcore.NewCallKey(CallNamespace, blockWire.ID)
			if err != nil {
				return protocolcore.Message{}, report,
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path+".id", err)
			}
			arguments, err := protocolcore.NewJSONObject(
				blockWire.Input,
				codec.options.MaxToolArgumentBytes,
			)
			if err != nil {
				return protocolcore.Message{}, report,
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path+".input", err)
			}
			block, err := protocolcore.NewToolCallBlock(protocolcore.ToolCall{
				Key:       key,
				Name:      blockWire.Name,
				Arguments: arguments,
			})
			if err != nil {
				return protocolcore.Message{}, report,
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
			}
			blocks = append(blocks, block)
			if rawPresent(blockWire.CacheControl) {
				report = report.Merge(cacheNotice(path + ".cache_control"))
			}

		case "tool_result":
			if role != protocolcore.RoleUser {
				return protocolcore.Message{}, report, protocolcore.NewFailure(
					protocolcore.ReasonInvalidClientRequest,
					path,
					errors.New("tool_result block is not in a user message"),
				)
			}
			var blockWire anthropicToolResultBlockWire
			if err := decodeStrict(rawBlock, &blockWire); err != nil {
				return protocolcore.Message{}, report,
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
			}
			key, err := protocolcore.NewCallKey(CallNamespace, blockWire.ToolUseID)
			if err != nil {
				return protocolcore.Message{}, report,
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path+".tool_use_id", err)
			}
			content, err := decodeToolResultContent(blockWire.Content, path+".content")
			if err != nil {
				return protocolcore.Message{}, report, err
			}
			block, err := protocolcore.NewToolResultBlock(protocolcore.ToolResult{
				Key:     key,
				Content: content,
				IsError: blockWire.IsError,
			})
			if err != nil {
				return protocolcore.Message{}, report,
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
			}
			blocks = append(blocks, block)
			if rawPresent(blockWire.CacheControl) {
				report = report.Merge(cacheNotice(path + ".cache_control"))
			}

		default:
			return protocolcore.Message{}, report, protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				path+".type",
				errors.New("content block type is unsupported"),
			)
		}
	}
	message := protocolcore.Message{Role: role, Blocks: blocks}
	if err := message.Validate(); err != nil {
		return protocolcore.Message{}, report, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			fmt.Sprintf("$.messages[%d]", messageIndex),
			err,
		)
	}
	return message.Clone(), report, nil
}

func (codec *Codec) decodeToolDefinition(
	index int,
	wire anthropicToolDefinitionWire,
) (protocolcore.ToolDefinition, protocolcore.TranslationReport, error) {
	path := fmt.Sprintf("$.tools[%d]", index)
	if wire.Type != "" {
		return protocolcore.ToolDefinition{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				path+".type",
				errors.New("server-side tool types are unsupported"),
			)
	}
	schema, err := protocolcore.NewJSONObject(wire.InputSchema, codec.options.MaxRequestBytes)
	if err != nil {
		return protocolcore.ToolDefinition{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path+".input_schema", err)
	}
	tool := protocolcore.ToolDefinition{
		Name:                wire.Name,
		Description:         wire.Description,
		InputSchema:         schema,
		EagerInputStreaming: wire.EagerInputStreaming != nil && *wire.EagerInputStreaming,
	}
	if err := tool.Validate(); err != nil {
		return protocolcore.ToolDefinition{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
	}
	report := protocolcore.TranslationReport{}
	if rawPresent(wire.CacheControl) {
		report = cacheNotice(path + ".cache_control")
	}
	return tool.Clone(), report, nil
}

func decodeToolChoice(
	wire *anthropicToolChoiceWire,
) (protocolcore.ToolChoice, error) {
	if wire == nil {
		return protocolcore.ToolChoice{}, nil
	}
	choice := protocolcore.ToolChoice{
		Name:            wire.Name,
		DisableParallel: wire.DisableParallelUse,
	}
	switch wire.Type {
	case "auto":
		choice.Mode = protocolcore.ToolChoiceAuto
	case "any":
		choice.Mode = protocolcore.ToolChoiceRequired
	case "tool":
		choice.Mode = protocolcore.ToolChoiceNamed
	case "none":
		choice.Mode = protocolcore.ToolChoiceNone
	default:
		return protocolcore.ToolChoice{}, protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedClientInput,
			"$.tool_choice.type",
			errors.New("tool choice type is unsupported"),
		)
	}
	return choice, nil
}

func decodeToolResultContent(raw json.RawMessage, path string) (string, error) {
	if !rawPresent(raw) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
	}
	var combined bytes.Buffer
	for index, rawBlock := range blocks {
		var block anthropicTextBlockWire
		if err := decodeStrict(rawBlock, &block); err != nil {
			return "", protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				fmt.Sprintf("%s[%d]", path, index),
				errors.New("tool result contains a non-text block"),
			)
		}
		if block.Type != "text" || rawPresent(block.CacheControl) {
			return "", protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				fmt.Sprintf("%s[%d]", path, index),
				errors.New("tool result content cannot be represented losslessly"),
			)
		}
		combined.WriteString(block.Text)
	}
	return combined.String(), nil
}

func cacheNotice(path string) protocolcore.TranslationReport {
	return protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
		Code: protocolcore.NoticeCacheControlNotForwarded,
		Path: path,
	})
}
