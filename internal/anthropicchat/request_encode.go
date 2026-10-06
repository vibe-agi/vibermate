package anthropicchat

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
	"unsafe"

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
	Strict      *bool           `json:"strict,omitempty"`
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
	if err := codec.ValidateRequest(request); err != nil {
		return nil, protocolcore.TranslationReport{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$",
			err,
		)
	}
	budget := codec.requestBudget()
	if budget != nil {
		cost, err := protocolcore.MeasureRequest(request)
		if err != nil {
			return nil, protocolcore.TranslationReport{}, err
		}
		if err := budget.Reserve(cost); err != nil {
			return nil, protocolcore.TranslationReport{}, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
		}
	}
	toolCatalog, err := buildProviderToolCatalog(request)
	if err != nil {
		return nil, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				"$.tools",
				err,
			)
	}

	messages := make([]openAIRequestMessageWire, 0, len(request.Messages)+len(request.System))
	if len(request.System) > 0 {
		var systemText strings.Builder
		for _, block := range request.System {
			systemText.WriteString(block.Text)
		}
		messages = append(messages, openAIRequestMessageWire{
			Role:    "system",
			Content: stringPointer(systemText.String()),
		})
	}
	var report protocolcore.TranslationReportBuilder
	for messageIndex, message := range request.Messages {
		encoded, normalized, err := encodeMessage(
			message,
			toolCatalog,
			codec.providerRequest.instructionRoleMode,
		)
		if err != nil {
			return nil, report.Build(), protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				"$.messages",
				err,
			)
		}
		messages = append(messages, encoded...)
		messageReport, err := messageEncodingReport(messageIndex, message, budget)
		if err != nil {
			return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$.messages", err)
		}
		if err := protocolcore.AppendReportWithin(&report, messageReport, budget); err != nil {
			return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
		}
		if normalized {
			if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
				Code: protocolcore.NoticeContentOrderNormalized,
				Path: "$.messages[" + integerString(messageIndex) + "].content",
			}), budget); err != nil {
				return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
			}
		}
		if message.Role == protocolcore.RoleDeveloper &&
			codec.providerRequest.instructionRoleMode ==
				InstructionRoleNormalizeDeveloperToSystem {
			if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(
				protocolcore.TranslationNotice{
					Code: protocolcore.NoticeDeveloperRoleNormalized,
					Path: "$.messages[" + integerString(messageIndex) + "].role",
				},
			), budget); err != nil {
				return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
			}
		}
	}

	tools := make([]openAIToolDefinitionWire, len(toolCatalog.entries))
	for index, entry := range toolCatalog.entries {
		tool := entry.definition
		parameters := tool.InputSchema.Bytes()
		if tool.EffectiveKind() == protocolcore.ToolKindCustom {
			parameters = []byte(customToolInputSchema)
		}
		var strict *bool
		if tool.StrictKnown {
			value := tool.Strict
			strict = &value
		}
		tools[index] = openAIToolDefinitionWire{
			Type: "function",
			Function: openAIFunctionDefinition{
				Name:        entry.providerName,
				Description: tool.Description,
				Parameters:  parameters,
				Strict:      strict,
			},
		}
		if entry.identity.namespace != "" {
			if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(
				protocolcore.TranslationNotice{
					Code: protocolcore.NoticeToolNamespaceEncoded,
					Path: entry.path,
				},
			), budget); err != nil {
				return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
			}
		}
		if tool.EffectiveKind() == protocolcore.ToolKindCustom &&
			tool.CustomFormat.Kind ==
				protocolcore.CustomToolFormatGrammar {
			if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(
				protocolcore.TranslationNotice{
					Code: protocolcore.NoticeCustomToolGrammarNotForwarded,
					Path: entry.path + ".format",
				},
			), budget); err != nil {
				return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
			}
		}
		if tool.EagerInputStreaming {
			if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(
				protocolcore.TranslationNotice{
					Code: protocolcore.NoticeEagerToolInputStreamingNotForwarded,
					Path: entry.path + ".eager_input_streaming",
				},
			), budget); err != nil {
				return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
			}
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
		entry, err := toolCatalog.namedEntry(request.ToolChoice.Name)
		if err != nil {
			return nil, report.Build(), protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				"$.tool_choice",
				err,
			)
		}
		toolChoice = openAINamedToolChoice(entry.providerName)
	case protocolcore.ToolChoiceNone:
		toolChoice = "none"
	default:
		return nil, report.Build(), protocolcore.NewFailure(
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
	if err := protocolcore.AppendReportWithin(&report, reasoningReport, budget); err != nil {
		return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	if len(request.Context.Edits) != 0 {
		if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeContextManagementNotForwarded,
				Path: "$.context_management",
			},
		), budget); err != nil {
			return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
		}
	}
	if request.Diagnostics.Requested {
		if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeDiagnosticsNotForwarded,
				Path: "$.diagnostics",
			},
		), budget); err != nil {
			return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
		}
	}
	if request.OutputVerbosity != "" {
		if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeTextVerbosityNotForwarded,
				Path: "$.text.verbosity",
			},
		), budget); err != nil {
			return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
		}
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
		return nil, report.Build(), protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedClientInput,
			"$.output_config.format",
			errors.New("structured output kind is unavailable"),
		)
	}
	if request.MaxOutputTokens > 0 {
		switch codec.providerRequest.completionTokenField {
		case CompletionTokenFieldMaxTokens:
			wire.MaxTokens = integerPointer(request.MaxOutputTokens)
		case CompletionTokenFieldMaxCompletionTokens:
			wire.MaxCompletionTokens = integerPointer(request.MaxOutputTokens)
		default:
			return nil, report.Build(), protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				"$.max_tokens",
				errors.New("provider completion token field is unavailable"),
			)
		}
	}
	if len(toolCatalog.entries) > 0 {
		switch codec.providerRequest.toolReasoningMode {
		case ToolReasoningModeOmit:
			if wire.ReasoningEffort != "" {
				if err := protocolcore.AppendReportWithin(&report, reasoningDowngradeNotice(), budget); err != nil {
					return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
				}
			}
			wire.ReasoningEffort = ""
		case ToolReasoningModeNone:
			if wire.ReasoningEffort != "" &&
				wire.ReasoningEffort != "none" {
				if err := protocolcore.AppendReportWithin(&report, reasoningDowngradeNotice(), budget); err != nil {
					return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
				}
			}
			wire.ReasoningEffort = "none"
		default:
			return nil, report.Build(), protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				"$.tools",
				errors.New("provider tool reasoning mode is unavailable"),
			)
		}
	}
	if request.Stream {
		wire.StreamOptions = &openAIStreamOptionsWire{IncludeUsage: true}
	}
	if budget != nil {
		// Count actual JSON escaping before Marshal allocates the full body.
		counter := providerWireCounter{limit: uint64(codec.options.MaxRequestBytes)}
		if err := counter.value(reflect.ValueOf(wire)); err != nil {
			return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
		}
		if err := budget.Reserve(protocolcore.ResourceCost{PayloadBytes: counter.used, StructureBytes: uint64(unsafe.Sizeof([]byte(nil)))}); err != nil {
			return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
		}
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, report.Build(), protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$",
			err,
		)
	}
	if budget != nil && len(encoded) > codec.options.MaxRequestBytes {
		return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", errors.New("encoded provider request exceeds the configured wire limit"))
	}
	return encoded, report.Build(), nil
}

// providerWireCounter counts only the fixed, acyclic outgoing Chat wire types
// above. It never creates a serialized representation or decoded JSON tree.
// RawMessage compact/HTML escaping and string UTF-8 replacement mirror the
// encoding/json encoder used by the immediately following Marshal.
type providerWireCounter struct{ used, limit uint64 }

func (counter *providerWireCounter) add(bytes uint64) error {
	if bytes > counter.limit-counter.used {
		return errors.New("encoded provider request exceeds the configured wire limit")
	}
	counter.used += bytes
	return nil
}

func (counter *providerWireCounter) quoted(value string) error {
	if err := counter.add(2); err != nil {
		return err
	}
	for index := 0; index < len(value); {
		character := value[index]
		if character < utf8.RuneSelf {
			size := uint64(1)
			switch character {
			case '\\', '"', '\b', '\f', '\n', '\r', '\t':
				size = 2
			case '<', '>', '&':
				size = 6
			default:
				if character < 0x20 {
					size = 6
				}
			}
			if err := counter.add(size); err != nil {
				return err
			}
			index++
			continue
		}
		runeValue, width := utf8.DecodeRuneInString(value[index:])
		size := uint64(width)
		if runeValue == utf8.RuneError && width == 1 || runeValue == '\u2028' || runeValue == '\u2029' {
			size = 6
		}
		if err := counter.add(size); err != nil {
			return err
		}
		index += width
	}
	return nil
}

func (counter *providerWireCounter) raw(value []byte) error {
	if len(value) == 0 {
		return counter.add(4)
	} // nil RawMessage encodes as null.
	inString, escaped := false, false
	for index := 0; index < len(value); index++ {
		character := value[index]
		if !inString && strings.ContainsRune(" \t\r\n", rune(character)) {
			continue
		}
		size := uint64(1)
		if inString && !escaped {
			if character == '<' || character == '>' || character == '&' {
				size = 6
			}
			if character == 0xe2 && index+2 < len(value) && value[index+1] == 0x80 && (value[index+2] == 0xa8 || value[index+2] == 0xa9) {
				size = 6
				index += 2
			}
		}
		if err := counter.add(size); err != nil {
			return err
		}
		if escaped {
			escaped = false
			continue
		}
		if inString && character == '\\' {
			escaped = true
			continue
		}
		if character == '"' {
			inString = !inString
		}
	}
	return nil
}

func providerWireEmpty(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return value.Len() == 0
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return value.IsZero()
	case reflect.Interface, reflect.Pointer:
		return value.IsNil()
	}
	return false
}

func (counter *providerWireCounter) value(value reflect.Value) error {
	if !value.IsValid() {
		return counter.add(4)
	}
	if value.Type() == reflect.TypeOf(json.RawMessage(nil)) {
		return counter.raw(value.Bytes())
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return counter.add(4)
		}
		return counter.value(value.Elem())
	case reflect.String:
		return counter.quoted(value.String())
	case reflect.Bool:
		if value.Bool() {
			return counter.add(4)
		}
		return counter.add(5)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var buffer [64]byte
		return counter.add(uint64(len(strconv.AppendInt(buffer[:0], value.Int(), 10))))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		var buffer [64]byte
		return counter.add(uint64(len(strconv.AppendUint(buffer[:0], value.Uint(), 10))))
	case reflect.Float32, reflect.Float64:
		var buffer [64]byte
		format := byte('f')
		absolute := math.Abs(value.Float())
		if absolute != 0 && (absolute < 1e-6 || absolute >= 1e21) {
			format = 'e'
		}
		number := strconv.AppendFloat(buffer[:0], value.Float(), format, -1, value.Type().Bits())
		length := len(number)
		if format == 'e' && length >= 4 && number[length-4] == 'e' && number[length-3] == '-' && number[length-2] == '0' {
			length--
		}
		return counter.add(uint64(length))
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return counter.add(4)
		}
		if err := counter.add(2); err != nil {
			return err
		}
		for index := 0; index < value.Len(); index++ {
			if index > 0 {
				if err := counter.add(1); err != nil {
					return err
				}
			}
			if err := counter.value(value.Index(index)); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		if err := counter.add(2); err != nil {
			return err
		}
		fields := 0
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "-" || strings.Contains(options, "omitempty") && providerWireEmpty(value.Field(index)) {
				continue
			}
			if name == "" {
				name = field.Name
			}
			if fields > 0 {
				if err := counter.add(1); err != nil {
					return err
				}
			}
			fields++
			if err := counter.quoted(name); err != nil {
				return err
			}
			if err := counter.add(1); err != nil {
				return err
			}
			if err := counter.value(value.Field(index)); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if value.IsNil() {
			return counter.add(4)
		}
		if err := counter.add(2); err != nil {
			return err
		}
		iterator := value.MapRange()
		entries := 0
		for iterator.Next() {
			if iterator.Key().Kind() != reflect.String {
				return errors.New("provider wire map key is unsupported")
			}
			if entries > 0 {
				if err := counter.add(1); err != nil {
					return err
				}
			}
			entries++
			if err := counter.quoted(iterator.Key().String()); err != nil {
				return err
			}
			if err := counter.add(1); err != nil {
				return err
			}
			if err := counter.value(iterator.Value()); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("provider request wire value is unsupported")
	}
}

func messageEncodingReport(
	messageIndex int,
	message protocolcore.Message,
	budget *protocolcore.ResourceBudget,
) (protocolcore.TranslationReport, error) {
	var report protocolcore.TranslationReportBuilder
	for blockIndex, block := range message.Blocks {
		if block.Kind != protocolcore.BlockToolCall {
			continue
		}
		path := fmt.Sprintf(
			"$.messages[%d].blocks[%d]",
			messageIndex,
			blockIndex,
		)
		if !block.ToolCall.ItemKey.IsZero() {
			if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(
				protocolcore.TranslationNotice{
					Code: protocolcore.NoticeToolItemIdentityNotForwarded,
					Path: path + ".item_id",
				},
			), budget); err != nil {
				return report.Build(), err
			}
		}
		if block.ToolCall.EffectiveKind() ==
			protocolcore.ToolKindCustom {
			if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(
				protocolcore.TranslationNotice{
					Code: protocolcore.NoticeCustomToolKindEncoded,
					Path: path + ".kind",
				},
			), budget); err != nil {
				return report.Build(), err
			}
		}
	}
	return report.Build(), nil
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
	if intent.Context != "" {
		report = report.Merge(protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeReasoningContextNotForwarded,
				Path: "$.reasoning.context",
			},
		))
	}
	if intent.Summary != "" {
		report = report.Merge(protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeReasoningSummaryNotForwarded,
				Path: "$.reasoning.summary",
			},
		))
	}
	if intent.Execution != "" {
		report = report.Merge(protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeReasoningExecutionNotForwarded,
				Path: "$.reasoning.mode",
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
	toolCatalog providerToolCatalog,
	instructionRoleMode InstructionRoleMode,
) ([]openAIRequestMessageWire, bool, error) {
	switch message.Role {
	case protocolcore.RoleSystem, protocolcore.RoleDeveloper:
		var text strings.Builder
		for _, block := range message.Blocks {
			if block.Kind != protocolcore.BlockText {
				return nil, false, errors.New(
					"instruction message contains an unsupported block",
				)
			}
			text.WriteString(block.Text)
		}
		role := string(message.Role)
		if message.Role == protocolcore.RoleDeveloper &&
			instructionRoleMode ==
				InstructionRoleNormalizeDeveloperToSystem {
			role = string(protocolcore.RoleSystem)
		}
		return []openAIRequestMessageWire{{
			Role:    role,
			Content: stringPointer(text.String()),
		}}, false, nil

	case protocolcore.RoleUser:
		var encoded []openAIRequestMessageWire
		var text strings.Builder
		hasText := false
		hasTool := false
		flushText := func() {
			if !hasText {
				return
			}
			encoded = append(encoded, openAIRequestMessageWire{
				Role:    "user",
				Content: stringPointer(text.String()),
			})
			text.Reset()
			hasText = false
		}
		for _, block := range message.Blocks {
			switch block.Kind {
			case protocolcore.BlockText:
				text.WriteString(block.Text)
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
		var text strings.Builder
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
				text.WriteString(block.Text)
			case protocolcore.BlockToolCall:
				if seenText {
					mixed = true
				}
				seenTool = true
				entry, err := toolCatalog.providerEntryForCall(block.ToolCall)
				if err != nil {
					return nil, false, err
				}
				arguments, err := entry.providerArguments(block.ToolCall)
				if err != nil {
					return nil, false, err
				}
				toolCalls = append(toolCalls, openAIToolCallWire{
					ID:   block.ToolCall.Key.WireID(),
					Type: "function",
					Function: openAIFunctionWire{
						Name:      entry.providerName,
						Arguments: string(arguments),
					},
				})
			default:
				return nil, false, errors.New("assistant message contains an unsupported block")
			}
		}
		var content *string
		if seenText {
			content = stringPointer(text.String())
		}
		return []openAIRequestMessageWire{{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		}}, mixed, nil

	case protocolcore.RoleTool:
		encoded := make([]openAIRequestMessageWire, len(message.Blocks))
		for index, block := range message.Blocks {
			if block.Kind != protocolcore.BlockToolResult {
				return nil, false, errors.New(
					"tool message contains an unsupported block",
				)
			}
			content := block.ToolResult.Content
			encoded[index] = openAIRequestMessageWire{
				Role:       "tool",
				Content:    &content,
				ToolCallID: block.ToolResult.Key.WireID(),
			}
		}
		return encoded, len(encoded) > 1, nil

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
