package anthropicchat

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

type openAIRequestWire struct {
	Model               string                     `json:"model"`
	Messages            []openAIRequestMessageWire `json:"messages"`
	Tools               []openAIToolDefinitionWire `json:"tools,omitempty"`
	ToolChoice          any                        `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool                      `json:"parallel_tool_calls,omitempty"`
	MaxCompletionTokens *int                       `json:"max_completion_tokens,omitempty"`
	MaxTokens           *int                       `json:"max_tokens,omitempty"`
	ReasoningEffort     string                     `json:"reasoning_effort,omitempty"`
	Temperature         *float64                   `json:"temperature,omitempty"`
	TopP                *float64                   `json:"top_p,omitempty"`
	Stop                []string                   `json:"stop,omitempty"`
	Stream              bool                       `json:"stream,omitempty"`
	StreamOptions       *openAIStreamOptionsWire   `json:"stream_options,omitempty"`
	ResponseFormat      *openAIResponseFormatWire  `json:"response_format,omitempty"`
}

type openAIStreamOptionsWire struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIResponseFormatWire struct {
	Type       string               `json:"type"`
	JSONSchema openAIJSONSchemaWire `json:"json_schema"`
}

type openAIJSONSchemaWire struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type openAIRequestMessageWire struct {
	Role       string               `json:"role"`
	Content    *string              `json:"content,omitempty"`
	ToolCalls  []openAIToolCallWire `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
}

type openAIToolDefinitionWire struct {
	Type     string                   `json:"type"`
	Function openAIFunctionDefinition `json:"function"`
}

type openAIFunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type openAIToolCallWire struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionWire `json:"function"`
}

type openAIFunctionWire struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (codec *Codec) EncodeProviderRequest(
	request protocolcore.Request,
) ([]byte, protocolcore.TranslationReport, error) {
	if err := request.Validate(); err != nil {
		return nil, protocolcore.TranslationReport{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$",
			err,
		)
	}

	messages := make([]openAIRequestMessageWire, 0, len(request.Messages)+len(request.System))
	if len(request.System) > 0 {
		var systemText string
		for _, block := range request.System {
			systemText += block.Text
		}
		messages = append(messages, openAIRequestMessageWire{
			Role:    "system",
			Content: stringPointer(systemText),
		})
	}
	report := protocolcore.TranslationReport{}
	for messageIndex, message := range request.Messages {
		encoded, normalized, err := encodeMessage(message)
		if err != nil {
			return nil, report, protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				"$.messages",
				err,
			)
		}
		messages = append(messages, encoded...)
		if normalized {
			report = report.Merge(protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
				Code: protocolcore.NoticeContentOrderNormalized,
				Path: "$.messages[" + integerString(messageIndex) + "].content",
			}))
		}
	}

	tools := make([]openAIToolDefinitionWire, len(request.Tools))
	for index, tool := range request.Tools {
		tools[index] = openAIToolDefinitionWire{
			Type: "function",
			Function: openAIFunctionDefinition{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema.Bytes(),
			},
		}
	}

	var toolChoice any
	var parallelToolCalls *bool
	switch request.ToolChoice.Mode {
	case "":
	case protocolcore.ToolChoiceAuto:
		toolChoice = "auto"
	case protocolcore.ToolChoiceRequired:
		toolChoice = "required"
	case protocolcore.ToolChoiceNamed:
		toolChoice = openAINamedToolChoice(request.ToolChoice.Name)
	case protocolcore.ToolChoiceNone:
		toolChoice = "none"
	default:
		return nil, report, protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedClientInput,
			"$.tool_choice",
			errors.New("tool choice is unsupported"),
		)
	}
	if request.ToolChoice.DisableParallel {
		value := false
		parallelToolCalls = &value
	}

	wire := openAIRequestWire{
		Model:             request.EffectiveModel,
		Messages:          messages,
		Tools:             tools,
		ToolChoice:        toolChoice,
		ParallelToolCalls: parallelToolCalls,
		Temperature:       request.Temperature,
		TopP:              request.TopP,
		Stop:              append([]string(nil), request.StopSequences...),
		Stream:            request.Stream,
	}
	reasoningEffort, reasoningReport := codec.encodeProviderReasoning(request)
	wire.ReasoningEffort = reasoningEffort
	report = report.Merge(reasoningReport)
	if len(request.Context.Edits) != 0 {
		report = report.Merge(protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeContextManagementNotForwarded,
				Path: "$.context_management",
			},
		))
	}
	if request.Diagnostics.Requested {
		report = report.Merge(protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeDiagnosticsNotForwarded,
				Path: "$.diagnostics",
			},
		))
	}
	switch request.Output.Kind {
	case "":
	case protocolcore.StructuredOutputJSONSchema:
		wire.ResponseFormat = &openAIResponseFormatWire{
			Type: "json_schema",
			JSONSchema: openAIJSONSchemaWire{
				Name:   structuredOutputName(request.Output.Schema),
				Strict: true,
				Schema: request.Output.Schema.Bytes(),
			},
		}
	default:
		return nil, report, protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedClientInput,
			"$.output_config.format",
			errors.New("structured output kind is unavailable"),
		)
	}
	switch codec.providerRequest.completionTokenField {
	case CompletionTokenFieldMaxTokens:
		wire.MaxTokens = integerPointer(request.MaxOutputTokens)
	case CompletionTokenFieldMaxCompletionTokens:
		wire.MaxCompletionTokens = integerPointer(request.MaxOutputTokens)
	default:
		return nil, report, protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedClientInput,
			"$.max_tokens",
			errors.New("provider completion token field is unavailable"),
		)
	}
	if len(request.Tools) > 0 {
		switch codec.providerRequest.toolReasoningMode {
		case ToolReasoningModeOmit:
			if wire.ReasoningEffort != "" {
				report = report.Merge(reasoningDowngradeNotice())
			}
			wire.ReasoningEffort = ""
		case ToolReasoningModeNone:
			if wire.ReasoningEffort != "" &&
				wire.ReasoningEffort != "none" {
				report = report.Merge(reasoningDowngradeNotice())
			}
			wire.ReasoningEffort = "none"
		default:
			return nil, report, protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				"$.tools",
				errors.New("provider tool reasoning mode is unavailable"),
			)
		}
	}
	if request.Stream {
		wire.StreamOptions = &openAIStreamOptionsWire{IncludeUsage: true}
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, report, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$",
			err,
		)
	}
	return encoded, report, nil
}

func structuredOutputName(schema protocolcore.JSONDocument) string {
	digest := sha256.Sum256(schema.Bytes())
	return fmt.Sprintf("vibermate_output_%x", digest[:12])
}

func (codec *Codec) encodeProviderReasoning(
	request protocolcore.Request,
) (string, protocolcore.TranslationReport) {
	intent := request.Reasoning
	report := protocolcore.TranslationReport{}
	if intent.Thinking != "" {
		report = report.Merge(protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeThinkingModeNotForwarded,
				Path: "$.thinking.type",
			},
		))
	}
	if intent.BudgetTokens != 0 {
		report = report.Merge(protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeThinkingBudgetNotForwarded,
				Path: "$.thinking.budget_tokens",
			},
		))
	}
	if intent.Display != "" {
		report = report.Merge(protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeThinkingDisplayNotForwarded,
				Path: "$.thinking.display",
			},
		))
	}
	if intent.TaskBudget.Present {
		report = report.Merge(protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeTaskBudgetNotForwarded,
				Path: "$.output_config.task_budget",
			},
		))
	}
	if intent.Effort != "" {
		return string(intent.Effort), report
	}
	if intent.Thinking == protocolcore.ThinkingModeDisabled {
		switch codec.providerRequest.disabledReasoning {
		case DisabledReasoningModeOmit:
			return "", report
		case DisabledReasoningModeNone:
			return "none", report
		default:
			return "", report
		}
	}
	return "", report
}

func reasoningDowngradeNotice() protocolcore.TranslationReport {
	return protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
		Code: protocolcore.NoticeReasoningEffortDowngraded,
		Path: "$.output_config.effort",
	})
}

func integerPointer(value int) *int {
	return &value
}

type openAINamedToolChoiceWire struct {
	Type     string `json:"type"`
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

func openAINamedToolChoice(name string) openAINamedToolChoiceWire {
	choice := openAINamedToolChoiceWire{Type: "function"}
	choice.Function.Name = name
	return choice
}

func encodeMessage(
	message protocolcore.Message,
) ([]openAIRequestMessageWire, bool, error) {
	switch message.Role {
	case protocolcore.RoleUser:
		var encoded []openAIRequestMessageWire
		var text string
		hasText := false
		hasTool := false
		flushText := func() {
			if !hasText {
				return
			}
			encoded = append(encoded, openAIRequestMessageWire{
				Role:    "user",
				Content: stringPointer(text),
			})
			text = ""
			hasText = false
		}
		for _, block := range message.Blocks {
			switch block.Kind {
			case protocolcore.BlockText:
				text += block.Text
				hasText = true
			case protocolcore.BlockToolResult:
				flushText()
				content := block.ToolResult.Content
				encoded = append(encoded, openAIRequestMessageWire{
					Role:       "tool",
					Content:    &content,
					ToolCallID: block.ToolResult.Key.WireID(),
				})
				hasTool = true
			default:
				return nil, false, errors.New("user message contains an unsupported block")
			}
		}
		flushText()
		return encoded, hasTool && len(encoded) > 1, nil

	case protocolcore.RoleAssistant:
		var text string
		toolCalls := make([]openAIToolCallWire, 0)
		mixed := false
		seenText := false
		seenTool := false
		for _, block := range message.Blocks {
			switch block.Kind {
			case protocolcore.BlockText:
				if seenTool {
					mixed = true
				}
				seenText = true
				text += block.Text
			case protocolcore.BlockToolCall:
				if seenText {
					mixed = true
				}
				seenTool = true
				toolCalls = append(toolCalls, openAIToolCallWire{
					ID:   block.ToolCall.Key.WireID(),
					Type: "function",
					Function: openAIFunctionWire{
						Name:      block.ToolCall.Name,
						Arguments: string(block.ToolCall.Arguments.Bytes()),
					},
				})
			default:
				return nil, false, errors.New("assistant message contains an unsupported block")
			}
		}
		var content *string
		if seenText {
			content = stringPointer(text)
		}
		return []openAIRequestMessageWire{{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		}}, mixed, nil

	default:
		return nil, false, errors.New("message role cannot be encoded for Chat")
	}
}

func stringPointer(value string) *string {
	return &value
}

func integerString(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = digits[value%10]
		value /= 10
	}
	return string(buffer[position:])
}
