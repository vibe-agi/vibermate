package anthropicchat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

type anthropicRequestWire struct {
	Model         string                   `json:"model"`
	MaxTokens     int                      `json:"max_tokens"`
	Messages      []json.RawMessage        `json:"messages"`
	System        json.RawMessage          `json:"system,omitempty"`
	Metadata      json.RawMessage          `json:"metadata,omitempty"`
	StopSequences []string                 `json:"stop_sequences,omitempty"`
	Stream        bool                     `json:"stream,omitempty"`
	Temperature   *float64                 `json:"temperature,omitempty"`
	TopP          *float64                 `json:"top_p,omitempty"`
	TopK          *int                     `json:"top_k,omitempty"`
	Tools         []json.RawMessage        `json:"tools,omitempty"`
	ToolChoice    *anthropicToolChoiceWire `json:"tool_choice,omitempty"`
	Thinking      json.RawMessage          `json:"thinking,omitempty"`
	OutputConfig  json.RawMessage          `json:"output_config,omitempty"`
	Context       json.RawMessage          `json:"context_management,omitempty"`
	Diagnostics   json.RawMessage          `json:"diagnostics,omitempty"`
	ServiceTier   string                   `json:"service_tier,omitempty"`
}

type anthropicMessageWire struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTextBlockWire struct {
	Type         string          `json:"type"`
	Text         string          `json:"text"`
	Citations    json.RawMessage `json:"citations,omitempty"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type anthropicToolUseBlockWire struct {
	Type         string          `json:"type"`
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Input        json.RawMessage `json:"input"`
	Caller       json.RawMessage `json:"caller,omitempty"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type anthropicToolCallerWire struct {
	Type   string `json:"type"`
	ToolID string `json:"tool_id,omitempty"`
}

type anthropicToolResultBlockWire struct {
	Type         string          `json:"type"`
	ToolUseID    string          `json:"tool_use_id"`
	Content      json.RawMessage `json:"content,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type anthropicToolReferenceBlockWire struct {
	Type     string `json:"type"`
	ToolName string `json:"tool_name"`
}

// anthropicTranslatedRequestWire adds the root members only the translated
// profile models. The strict profile keeps declaring them as unknown fields.
type anthropicTranslatedRequestWire struct {
	anthropicRequestWire
	Safeguards []json.RawMessage `json:"safeguards,omitempty"`
}

// anthropicTranslatedToolDefinitionWire adds defer_loading, which only the
// translated profile models.
type anthropicTranslatedToolDefinitionWire struct {
	anthropicToolDefinitionWire
	DeferLoading *bool `json:"defer_loading,omitempty"`
}

type anthropicThinkingBlockWire struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

type anthropicRedactedThinkingBlockWire struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type anthropicImageBlockWire struct {
	Type         string          `json:"type"`
	Source       json.RawMessage `json:"source"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type anthropicDocumentBlockWire struct {
	Type         string          `json:"type"`
	Source       json.RawMessage `json:"source"`
	Title        *string         `json:"title,omitempty"`
	Context      json.RawMessage `json:"context,omitempty"`
	Citations    json.RawMessage `json:"citations,omitempty"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type anthropicDocumentCitationsWire struct {
	Enabled bool `json:"enabled"`
}

type anthropicMediaSourceWire struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
	FileID    string `json:"file_id,omitempty"`
}

type requestDecodeMode uint8

const (
	requestDecodeStrict requestDecodeMode = iota
	requestDecodeNative
	// requestDecodeTranslated decodes JSON as strictly as requestDecodeStrict
	// and applies the translated client input profile.
	requestDecodeTranslated
)

// maxSafeguardEntries bounds the safeguards a request may list. Claude Code
// sends one.
const maxSafeguardEntries = 64

// clientAttributionPrefix starts the system block Claude Code adds for
// Anthropic billing attribution.
const clientAttributionPrefix = "x-anthropic-billing-header:"

// Native JSON is forwarded from the original source, not regenerated from the
// inspection view. Unknown nested fields therefore stay on the wire. Known
// fields keep their type/semantic validation; duplicate names are rejected once
// at the native request boundary before any projection is used for decisions.
func (mode requestDecodeMode) decode(value []byte, destination any) error {
	if mode == requestDecodeNative {
		return json.Unmarshal(value, destination)
	}
	return decodeStrict(value, destination)
}

func (mode requestDecodeMode) fields(value []byte, destination any, path string, budget *protocolcore.ResourceBudget) (protocolcore.TranslationReport, error) {
	if mode == requestDecodeNative {
		return protocolcore.TranslationReport{}, mode.decode(value, destination)
	}
	return decodeTolerantWithin(value, destination, path, budget)
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

// DecodeClientRequest decodes a client request with the codec's client input
// profile.
func (codec *Codec) DecodeClientRequest(
	body []byte,
) (protocolcore.Request, protocolcore.TranslationReport, error) {
	mode := requestDecodeStrict
	if codec.options.ClientInput.translated {
		mode = requestDecodeTranslated
	}
	return codec.decodeClientRequest(body, mode, false)
}

// DecodeCompatibleClientRequest produces the neutral inspection view for an
// Anthropic-to-Anthropic path whose original wire remains authoritative. It
// can therefore retain provider-native history as an explicit JSON summary
// without claiming that a cross-dialect encoder can reproduce that history.
func (codec *Codec) DecodeCompatibleClientRequest(
	body []byte,
) (protocolcore.Request, protocolcore.TranslationReport, error) {
	return codec.decodeClientRequest(body, requestDecodeNative, false)
}

// decodeClientRequest decodes one Messages request. countTokens decodes a
// count_tokens body, which has no max_tokens.
func (codec *Codec) decodeClientRequest(
	body []byte,
	mode requestDecodeMode,
	countTokens bool,
) (protocolcore.Request, protocolcore.TranslationReport, error) {
	if len(body) == 0 || len(body) > codec.options.MaxRequestBytes {
		return protocolcore.Request{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$",
				errors.New("request body has an invalid size"),
			)
	}

	budget := codec.requestBudget()
	if err := budget.ReserveJSON(body); err != nil {
		return protocolcore.Request{}, protocolcore.TranslationReport{}, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	if err := rejectDuplicateJSONNames(body); err != nil {
		return protocolcore.Request{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	var translatedWire anthropicTranslatedRequestWire
	var destination any = &translatedWire.anthropicRequestWire
	if mode == requestDecodeTranslated {
		destination = &translatedWire
	}
	unknownReport, err := mode.fields(body, destination, "$", budget)
	if err != nil {
		return protocolcore.Request{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	wire := translatedWire.anthropicRequestWire
	var report protocolcore.TranslationReportBuilder
	if err := protocolcore.AppendReportWithin(&report, unknownReport, budget); err != nil {
		return protocolcore.Request{}, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	unservedSafeguards, safeguardReport, err := decodeSafeguards(translatedWire.Safeguards, budget)
	if err != nil {
		return protocolcore.Request{}, report.Build(), err
	}
	if err := protocolcore.AppendReportWithin(&report, safeguardReport, budget); err != nil {
		return protocolcore.Request{}, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}

	system, systemReport, err := codec.decodeSystem(wire.System, mode, budget)
	if err != nil {
		return protocolcore.Request{}, report.Build(), err
	}
	if err := protocolcore.AppendReportWithin(&report, systemReport, budget); err != nil {
		return protocolcore.Request{}, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}

	messages := make([]protocolcore.Message, 0, len(wire.Messages))
	for index, rawMessage := range wire.Messages {
		// A message carries options beside its role and content that this
		// dialect does not model, such as the per-message output_config Claude
		// Code 2.1.3xx sends with a mid-conversation system message. Like a
		// tool definition, the message object is tolerant and declares each
		// unknown field; its content blocks stay strict, because an unknown
		// field inside a block changes what that block means. Nearly every
		// message is exactly role and content, and the body's duplicate names
		// were refused above, so those decode in one pass; only a message that
		// carries something else pays for the tolerant one.
		var message anthropicMessageWire
		if mode == requestDecodeNative || decodeKnownFields(rawMessage, &message) != nil {
			message = anthropicMessageWire{}
			messagePath := fmt.Sprintf("$.messages[%d]", index)
			unknownMessage, unknownErr := mode.fields(rawMessage, &message, messagePath, budget)
			if unknownErr != nil {
				return protocolcore.Request{}, report.Build(), protocolcore.NewFailure(
					protocolcore.ReasonInvalidClientRequest,
					messagePath,
					unknownErr,
				)
			}
			if err := protocolcore.AppendReportWithin(&report, unknownMessage, budget); err != nil {
				return protocolcore.Request{}, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
			}
		}
		decoded, messageReport, decodeErr := codec.decodeMessage(
			index,
			message,
			mode,
			budget,
		)
		if decodeErr != nil {
			return protocolcore.Request{}, report.Build(), decodeErr
		}
		// Only the translated profile drops blocks, and a message whose every
		// block was dropped (server-tool history alone) leaves the
		// conversation rather than becoming an empty message.
		if len(decoded.Blocks) != 0 {
			messages = append(messages, decoded)
		}
		if err := protocolcore.AppendReportWithin(&report, messageReport, budget); err != nil {
			return protocolcore.Request{}, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
		}
	}

	tools := make([]protocolcore.ToolDefinition, len(wire.Tools))
	for index, rawTool := range wire.Tools {
		// A tool definition carries options this dialect does not model —
		// Claude Code's `defer_loading` among them — and refusing the request
		// over one made the client unusable. Design 07 §2.3 keeps the unknown
		// field visible instead of letting a typed unmarshal erase it, and
		// §3.2 permits the loss because it is declared. The translated
		// profile models defer_loading itself.
		var translatedTool anthropicTranslatedToolDefinitionWire
		var toolDestination any = &translatedTool.anthropicToolDefinitionWire
		if mode == requestDecodeTranslated {
			toolDestination = &translatedTool
		}
		toolPath := fmt.Sprintf("$.tools[%d]", index)
		unknownTool, unknownErr := mode.fields(rawTool, toolDestination, toolPath, budget)
		tool := translatedTool.anthropicToolDefinitionWire
		if unknownErr != nil {
			return protocolcore.Request{}, report.Build(), protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				toolPath,
				unknownErr,
			)
		}
		if err := protocolcore.AppendReportWithin(&report, unknownTool, budget); err != nil {
			return protocolcore.Request{}, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
		}
		decoded, toolReport, decodeErr := codec.decodeToolDefinition(index, tool, mode)
		if decodeErr != nil {
			return protocolcore.Request{}, report.Build(), decodeErr
		}
		decoded.DeferLoading = translatedTool.DeferLoading != nil && *translatedTool.DeferLoading
		tools[index] = decoded
		if err := protocolcore.AppendReportWithin(&report, toolReport, budget); err != nil {
			return protocolcore.Request{}, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
		}
	}

	toolChoice, err := decodeToolChoice(wire.ToolChoice)
	if err != nil {
		return protocolcore.Request{}, report.Build(), err
	}
	reasoning, output, displayReport, err := decodeOutputConfiguration(
		wire.Thinking,
		wire.OutputConfig,
		mode,
	)
	if err != nil {
		return protocolcore.Request{}, report.Build(), err
	}
	if err := protocolcore.AppendReportWithin(&report, displayReport, budget); err != nil {
		return protocolcore.Request{}, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	contextIntent, err := decodeContextManagement(wire.Context, mode)
	if err != nil {
		return protocolcore.Request{}, report.Build(), err
	}
	diagnostics, err := decodeDiagnostics(wire.Diagnostics, mode)
	if err != nil {
		return protocolcore.Request{}, report.Build(), err
	}
	if rawPresent(wire.Metadata) {
		if !json.Valid(wire.Metadata) {
			return protocolcore.Request{}, report.Build(), protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$.metadata",
				errors.New("metadata is invalid JSON"),
			)
		}
		if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
			Code: protocolcore.NoticeMetadataNotForwarded,
			Path: "$.metadata",
		}), budget); err != nil {
			return protocolcore.Request{}, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
		}
	}
	if wire.TopK != nil {
		if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
			Code: protocolcore.NoticeTopKNotForwarded,
			Path: "$.top_k",
		}), budget); err != nil {
			return protocolcore.Request{}, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
		}
	}
	if wire.ServiceTier != "" {
		if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
			Code: protocolcore.NoticeServiceTierNotForwarded,
			Path: "$.service_tier",
		}), budget); err != nil {
			return protocolcore.Request{}, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
		}
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
		// Set only by the translated profile; nil otherwise.
		UnservedSafeguards: unservedSafeguards,
	}
	if countTokens && request.MaxOutputTokens == 0 &&
		request.Reasoning.Thinking == protocolcore.ThinkingModeEnabled {
		// A count_tokens body has no max_tokens, yet Claude Code sends
		// thinking {type:"enabled", budget_tokens} there for models without
		// adaptive thinking. The neutral model requires an enabled budget
		// below the output limit, so the limit is set just above the budget.
		// Nothing is sent upstream and a count never reads the limit.
		request.MaxOutputTokens = request.Reasoning.BudgetTokens + 1
	}
	if err := codec.ValidateRequest(request); err != nil {
		return protocolcore.Request{}, report.Build(), protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$",
			err,
		)
	}
	return request.Clone(), report.Build(), nil
}

// decodeSafeguards reads the translated profile's root safeguards: server-side
// checks the client asks the API to run, such as Claude Code auto mode's
// dangerous_tool_use classifier. A translated route runs none of them, so
// each entry is declared and a known kind is recorded as unserved, for the
// response to report honestly. Nothing else of an entry is kept: its
// classifier context never leaves the machine.
func decodeSafeguards(
	entries []json.RawMessage,
	budget *protocolcore.ResourceBudget,
) ([]protocolcore.SafeguardKind, protocolcore.TranslationReport, error) {
	if len(entries) == 0 {
		return nil, protocolcore.TranslationReport{}, nil
	}
	if len(entries) > maxSafeguardEntries {
		return nil, protocolcore.TranslationReport{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$.safeguards",
			errors.New("safeguard count exceeds its limit"),
		)
	}
	var kinds []protocolcore.SafeguardKind
	var report protocolcore.TranslationReportBuilder
	for index, entry := range entries {
		path := fmt.Sprintf("$.safeguards[%d]", index)
		var fields map[string]json.RawMessage
		var kind string
		if json.Unmarshal(entry, &fields) != nil || fields == nil ||
			json.Unmarshal(fields["type"], &kind) != nil || kind == "" {
			return nil, report.Build(), protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				path,
				errors.New("safeguard is not an object with a type"),
			)
		}
		if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
			Code: protocolcore.NoticeSafeguardsNotServed,
			Path: path,
		}), budget); err != nil {
			return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
		}
		// Only a known kind is reported back; an unknown one is declared and
		// otherwise left alone, since no status can be claimed for it.
		known := protocolcore.SafeguardKind(kind)
		if known == protocolcore.SafeguardDangerousToolUse && !slices.Contains(kinds, known) {
			kinds = append(kinds, known)
		}
	}
	return kinds, report.Build(), nil
}

func decodeDiagnostics(
	raw json.RawMessage,
	mode requestDecodeMode,
) (protocolcore.DiagnosticsIntent, error) {
	if !rawPresent(raw) {
		return protocolcore.DiagnosticsIntent{}, nil
	}
	var wire anthropicDiagnosticsWire
	if err := mode.decode(raw, &wire); err != nil {
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
	mode requestDecodeMode,
) (protocolcore.ContextManagementIntent, error) {
	if !rawPresent(raw) {
		return protocolcore.ContextManagementIntent{}, nil
	}
	var wire anthropicContextManagementWire
	if err := mode.decode(raw, &wire); err != nil {
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
		if err := mode.decode(rawEdit, &edit); err != nil {
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
		decoded, err := decodeClearThinkingEdit(edit.Keep, mode)
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

func decodeClearThinkingEdit(raw json.RawMessage, mode requestDecodeMode) (protocolcore.ContextEdit, error) {
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
	if err := mode.decode(raw, &wire); err != nil {
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
	mode requestDecodeMode,
) (
	protocolcore.ReasoningIntent,
	protocolcore.StructuredOutputIntent,
	protocolcore.TranslationReport,
	error,
) {
	var intent protocolcore.ReasoningIntent
	var structuredOutput protocolcore.StructuredOutputIntent
	if rawPresent(outputConfigRaw) {
		var output anthropicOutputConfigWire
		if err := mode.decode(outputConfigRaw, &output); err != nil {
			return protocolcore.ReasoningIntent{},
				protocolcore.StructuredOutputIntent{},
				protocolcore.TranslationReport{},
				protocolcore.NewFailure(
					protocolcore.ReasonInvalidClientRequest,
					"$.output_config",
					err,
				)
		}
		if rawPresent(output.Format) {
			var format anthropicJSONOutputFormatWire
			if err := mode.decode(output.Format, &format); err != nil {
				return protocolcore.ReasoningIntent{},
					protocolcore.StructuredOutputIntent{},
					protocolcore.TranslationReport{},
					protocolcore.NewFailure(
						protocolcore.ReasonInvalidClientRequest,
						"$.output_config.format",
						err,
					)
			}
			if format.Type != "json_schema" {
				return protocolcore.ReasoningIntent{},
					protocolcore.StructuredOutputIntent{},
					protocolcore.TranslationReport{},
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
					protocolcore.TranslationReport{},
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
				protocolcore.TranslationReport{},
				protocolcore.NewFailure(
					protocolcore.ReasonInvalidClientRequest,
					"$.output_config.effort",
					errors.New("reasoning effort is unsupported"),
				)
		}
		if rawPresent(output.TaskBudget) {
			var budget anthropicTaskBudgetWire
			if err := mode.decode(output.TaskBudget, &budget); err != nil {
				return protocolcore.ReasoningIntent{},
					protocolcore.StructuredOutputIntent{},
					protocolcore.TranslationReport{},
					protocolcore.NewFailure(
						protocolcore.ReasonInvalidClientRequest,
						"$.output_config.task_budget",
						err,
					)
			}
			if budget.Type != "tokens" || budget.Total <= 0 {
				return protocolcore.ReasoningIntent{},
					protocolcore.StructuredOutputIntent{},
					protocolcore.TranslationReport{},
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
					protocolcore.TranslationReport{},
					protocolcore.NewFailure(
						protocolcore.ReasonInvalidClientRequest,
						"$.output_config.task_budget",
						err,
					)
			}
		}
	}
	if !rawPresent(thinkingRaw) {
		return intent, structuredOutput, protocolcore.TranslationReport{}, nil
	}
	var thinking anthropicThinkingWire
	if err := mode.decode(thinkingRaw, &thinking); err != nil {
		return protocolcore.ReasoningIntent{},
			protocolcore.StructuredOutputIntent{},
			protocolcore.TranslationReport{},
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
			protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$.thinking.type",
				errors.New("thinking mode is unsupported"),
			)
	}
	if thinking.BudgetTokens != nil {
		intent.BudgetTokens = *thinking.BudgetTokens
	}
	// Display only chooses how thinking is shown to the client; it never
	// changes what the model does. An unknown value is therefore declared
	// and left out rather than refused: a native route forwards the
	// original bytes anyway, and no other dialect has the setting.
	var report protocolcore.TranslationReport
	switch thinking.Display {
	case "":
	case "summarized":
		intent.Display = protocolcore.ThinkingDisplaySummarized
	case "omitted":
		intent.Display = protocolcore.ThinkingDisplayOmitted
	case "updates":
		intent.Display = protocolcore.ThinkingDisplayUpdates
	default:
		report = protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
			Code: protocolcore.NoticeThinkingDisplayNotForwarded,
			Path: "$.thinking.display",
		})
	}
	return intent, structuredOutput, report, nil
}

func (codec *Codec) decodeSystem(
	raw json.RawMessage,
	mode requestDecodeMode,
	budget *protocolcore.ResourceBudget,
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
	blocks := make([]protocolcore.ContentBlock, 0, len(rawBlocks))
	var report protocolcore.TranslationReportBuilder
	for index, rawBlock := range rawBlocks {
		var blockWire anthropicTextBlockWire
		if err := mode.decode(rawBlock, &blockWire); err != nil {
			return nil, report.Build(), protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				fmt.Sprintf("$.system[%d]", index),
				err,
			)
		}
		if blockWire.Type != "text" {
			return nil, report.Build(), protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				fmt.Sprintf("$.system[%d].type", index),
				errors.New("system block type is unsupported"),
			)
		}
		block, err := protocolcore.NewTextBlock(blockWire.Text)
		if err != nil {
			return nil, report.Build(), protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				fmt.Sprintf("$.system[%d].text", index),
				err,
			)
		}
		if mode == requestDecodeTranslated && clientAttributionBlock(blockWire.Text) {
			// Anthropic billing attribution means nothing to another
			// provider, changes on every request (so it would make every
			// request a prompt-cache miss) and carries the prompt ID. The
			// raw client evidence still records it (design 6.2).
			if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
				Code: protocolcore.NoticeClientAttributionNotForwarded,
				Path: fmt.Sprintf("$.system[%d]", index),
			}), budget); err != nil {
				return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$.system", err)
			}
			continue
		}
		blocks = append(blocks, block)
		if rawPresent(blockWire.CacheControl) {
			if err := protocolcore.AppendReportWithin(&report, cacheNotice(fmt.Sprintf("$.system[%d].cache_control", index)), budget); err != nil {
				return nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$.system", err)
			}
		}
	}
	return blocks, report.Build(), nil
}

// clientAttributionBlock reports Claude Code's billing attribution block: a
// single line that starts with x-anthropic-billing-header:. A block with
// more lines is kept, since it is not attribution alone.
func clientAttributionBlock(text string) bool {
	return strings.HasPrefix(text, clientAttributionPrefix) &&
		!strings.ContainsAny(strings.TrimSuffix(text, "\n"), "\r\n")
}

// decodeInstructionMessage carries a system or developer message. The IR
// requires instruction content to be text, which is also all this dialect can
// express in one. The blocks' cache breakpoints and citations have no place
// in the joined text, so each one is declared rather than dropped silently.
func (codec *Codec) decodeInstructionMessage(
	messageIndex int,
	role protocolcore.Role,
	wire anthropicMessageWire,
	budget *protocolcore.ResourceBudget,
) (protocolcore.Message, protocolcore.TranslationReport, error) {
	fail := func(err error) (
		protocolcore.Message,
		protocolcore.TranslationReport,
		error,
	) {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				fmt.Sprintf("$.messages[%d]", messageIndex),
				err,
			)
	}
	var text string
	var report protocolcore.TranslationReportBuilder
	if err := json.Unmarshal(wire.Content, &text); err != nil {
		var blocks []anthropicTextBlockWire
		if blockErr := json.Unmarshal(wire.Content, &blocks); blockErr != nil {
			return fail(errors.New("instruction content is not text"))
		}
		for blockIndex, block := range blocks {
			if block.Type != "text" {
				return protocolcore.Message{}, protocolcore.TranslationReport{},
					protocolcore.NewFailure(
						protocolcore.ReasonUnsupportedClientInput,
						fmt.Sprintf("$.messages[%d].content", messageIndex),
						errors.New("instruction block type is unsupported"),
					)
			}
			text += block.Text
			path := fmt.Sprintf("$.messages[%d].content[%d]", messageIndex, blockIndex)
			if rawPresent(block.Citations) {
				if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
					Code: protocolcore.NoticeCitationsNotForwarded,
					Path: path + ".citations",
				}), budget); err != nil {
					return fail(err)
				}
			}
			if rawPresent(block.CacheControl) {
				if err := protocolcore.AppendReportWithin(&report, cacheNotice(path+".cache_control"), budget); err != nil {
					return fail(err)
				}
			}
		}
	}
	block, err := protocolcore.NewTextBlock(text)
	if err != nil {
		return fail(err)
	}
	message := protocolcore.Message{
		Role:   role,
		Blocks: []protocolcore.ContentBlock{block},
	}
	if err := message.Validate(); err != nil {
		return fail(err)
	}
	return message, report.Build(), nil
}

func (codec *Codec) decodeMessage(
	messageIndex int,
	wire anthropicMessageWire,
	mode requestDecodeMode,
	budget *protocolcore.ResourceBudget,
) (protocolcore.Message, protocolcore.TranslationReport, error) {
	role := protocolcore.Role(wire.Role)
	switch role {
	case protocolcore.RoleUser, protocolcore.RoleAssistant:
	case protocolcore.RoleSystem, protocolcore.RoleDeveloper:
		// An instruction can arrive inside the message list rather than only
		// as the top-level system parameter. Design 07 §3.2 normalizes it to
		// the same IR role and keeps it where it was: hoisting it to the front
		// would change what the model was told and when.
		return codec.decodeInstructionMessage(messageIndex, role, wire, budget)
	default:
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
	var report protocolcore.TranslationReportBuilder
	for blockIndex, rawBlock := range rawBlocks {
		path := fmt.Sprintf("$.messages[%d].content[%d]", messageIndex, blockIndex)
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(rawBlock, &header); err != nil {
			return protocolcore.Message{}, report.Build(),
				protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
		}
		if mode == requestDecodeTranslated {
			translated, handled, err := codec.decodeTranslatedBlock(role, header.Type, rawBlock, path, budget, &report)
			if err != nil {
				return protocolcore.Message{}, report.Build(), err
			}
			if handled {
				if translated != nil {
					blocks = append(blocks, *translated)
				}
				continue
			}
		}
		switch header.Type {
		case "text":
			var blockWire anthropicTextBlockWire
			if err := mode.decode(rawBlock, &blockWire); err != nil {
				return protocolcore.Message{}, report.Build(),
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
			}
			block, err := protocolcore.NewTextBlock(blockWire.Text)
			if err != nil {
				return protocolcore.Message{}, report.Build(),
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path+".text", err)
			}
			blocks = append(blocks, block)
			if rawPresent(blockWire.Citations) {
				if err := validateAnthropicCitations(blockWire.Citations); err != nil {
					return protocolcore.Message{}, report.Build(),
						protocolcore.NewFailure(
							protocolcore.ReasonInvalidClientRequest,
							path+".citations",
							err,
						)
				}
				if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(
					protocolcore.TranslationNotice{
						Code: protocolcore.NoticeCitationsNotForwarded,
						Path: path + ".citations",
					},
				), budget); err != nil {
					return protocolcore.Message{}, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$.messages", err)
				}
			}
			if rawPresent(blockWire.CacheControl) {
				if err := protocolcore.AppendReportWithin(&report, cacheNotice(path+".cache_control"), budget); err != nil {
					return protocolcore.Message{}, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$.messages", err)
				}
			}

		case "tool_use":
			if role != protocolcore.RoleAssistant {
				return protocolcore.Message{}, report.Build(), protocolcore.NewFailure(
					protocolcore.ReasonInvalidClientRequest,
					path,
					errors.New("tool_use block is not in an assistant message"),
				)
			}
			var blockWire anthropicToolUseBlockWire
			if err := mode.decode(rawBlock, &blockWire); err != nil {
				return protocolcore.Message{}, report.Build(),
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
			}
			key, err := protocolcore.NewCallKey(CallNamespace, blockWire.ID)
			if err != nil {
				return protocolcore.Message{}, report.Build(),
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path+".id", err)
			}
			arguments, err := protocolcore.NewJSONObject(
				blockWire.Input,
				codec.options.MaxToolArgumentBytes,
			)
			if err != nil {
				return protocolcore.Message{}, report.Build(),
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path+".input", err)
			}
			block, err := protocolcore.NewToolCallBlock(protocolcore.ToolCall{
				Key:       key,
				Name:      blockWire.Name,
				Arguments: arguments,
			})
			if err != nil {
				return protocolcore.Message{}, report.Build(),
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
			}
			blocks = append(blocks, block)
			if rawPresent(blockWire.Caller) {
				if err := validateAnthropicToolCaller(blockWire.Caller, mode); err != nil {
					return protocolcore.Message{}, report.Build(),
						protocolcore.NewFailure(
							protocolcore.ReasonInvalidClientRequest,
							path+".caller",
							err,
						)
				}
				if err := protocolcore.AppendReportWithin(&report, protocolcore.NewTranslationReport(
					protocolcore.TranslationNotice{
						Code: protocolcore.NoticeToolCallerNotForwarded,
						Path: path + ".caller",
					},
				), budget); err != nil {
					return protocolcore.Message{}, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$.messages", err)
				}
			}
			if rawPresent(blockWire.CacheControl) {
				if err := protocolcore.AppendReportWithin(&report, cacheNotice(path+".cache_control"), budget); err != nil {
					return protocolcore.Message{}, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$.messages", err)
				}
			}

		case "tool_result":
			if role != protocolcore.RoleUser {
				return protocolcore.Message{}, report.Build(), protocolcore.NewFailure(
					protocolcore.ReasonInvalidClientRequest,
					path,
					errors.New("tool_result block is not in a user message"),
				)
			}
			var blockWire anthropicToolResultBlockWire
			if err := mode.decode(rawBlock, &blockWire); err != nil {
				return protocolcore.Message{}, report.Build(),
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
			}
			key, err := protocolcore.NewCallKey(CallNamespace, blockWire.ToolUseID)
			if err != nil {
				return protocolcore.Message{}, report.Build(),
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path+".tool_use_id", err)
			}
			var content string
			var parts *protocolcore.ToolResultParts
			var native [][]byte
			if mode == requestDecodeTranslated {
				var partsReport protocolcore.TranslationReport
				content, parts, partsReport, err = decodeTranslatedToolResultContent(
					blockWire.Content,
					path+".content",
					budget,
				)
				if err == nil {
					if appendErr := protocolcore.AppendReportWithin(&report, partsReport, budget); appendErr != nil {
						err = protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$.messages", appendErr)
					}
				}
			} else {
				content, native, err = decodeToolResultContent(
					blockWire.Content,
					path+".content",
					mode,
				)
			}
			if err != nil {
				return protocolcore.Message{}, report.Build(), err
			}
			block, err := protocolcore.NewToolResultBlock(protocolcore.ToolResult{
				Key:     key,
				Content: content,
				IsError: blockWire.IsError,
				Parts:   parts,
			})
			if err != nil {
				return protocolcore.Message{}, report.Build(),
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
			}
			blocks = append(blocks, block)
			if len(native) != 0 {
				opaque, err := nativeHistoryBlock(path+".content", native)
				if err != nil {
					return protocolcore.Message{}, report.Build(), err
				}
				blocks = append(blocks, opaque)
			}
			if rawPresent(blockWire.CacheControl) {
				if err := protocolcore.AppendReportWithin(&report, cacheNotice(path+".cache_control"), budget); err != nil {
					return protocolcore.Message{}, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$.messages", err)
				}
			}

		case "thinking", "redacted_thinking":
			if role != protocolcore.RoleAssistant {
				return protocolcore.Message{}, report.Build(), protocolcore.NewFailure(
					protocolcore.ReasonInvalidClientRequest,
					path,
					errors.New("provider thinking block is not in an assistant message"),
				)
			}
			kind := protocolcore.ProviderExtensionThinking
			if header.Type == "redacted_thinking" {
				kind = protocolcore.ProviderExtensionRedactedThinking
			}
			extension, err := protocolcore.NewProviderExtension(
				protocolcore.ProviderExtensionSourceAnthropicMessages,
				kind,
				path,
				[][]byte{rawBlock},
			)
			if err != nil {
				return protocolcore.Message{}, report.Build(), protocolcore.NewFailure(
					protocolcore.ReasonInvalidClientRequest,
					path,
					err,
				)
			}
			block, err := protocolcore.NewProviderExtensionBlock(extension)
			if err != nil {
				return protocolcore.Message{}, report.Build(), protocolcore.NewFailure(
					protocolcore.ReasonInvalidClientRequest,
					path,
					err,
				)
			}
			blocks = append(blocks, block)

		default:
			if mode == requestDecodeNative && header.Type != "" {
				block, err := nativeHistoryBlock(path, [][]byte{rawBlock})
				if err != nil {
					return protocolcore.Message{}, report.Build(), err
				}
				blocks = append(blocks, block)
				continue
			}
			return protocolcore.Message{}, report.Build(), protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				path+".type",
				errors.New("content block type is unsupported"),
			)
		}
	}
	if len(blocks) == 0 && mode == requestDecodeTranslated {
		// Every block was dropped as server-tool history; the caller leaves
		// the message out.
		return protocolcore.Message{Role: role}, report.Build(), nil
	}
	message := protocolcore.Message{Role: role, Blocks: blocks}
	if err := message.Validate(); err != nil {
		return protocolcore.Message{}, report.Build(), protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			fmt.Sprintf("$.messages[%d]", messageIndex),
			err,
		)
	}
	return message.Clone(), report.Build(), nil
}

// Native history is evidence, not an executable tool intent or text. Only the
// same-dialect path uses this view; its source body remains authoritative.
func nativeHistoryBlock(path string, fragments [][]byte) (protocolcore.ContentBlock, error) {
	extension, err := protocolcore.NewProviderExtension(
		protocolcore.ProviderExtensionSourceAnthropicMessages,
		protocolcore.ProviderExtensionOpaqueItem, path, fragments,
	)
	if err != nil {
		return protocolcore.ContentBlock{}, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
	}
	return protocolcore.NewProviderExtensionBlock(extension)
}

func validateAnthropicCitations(raw json.RawMessage) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var citations []json.RawMessage
	if err := json.Unmarshal(raw, &citations); err != nil || citations == nil ||
		len(citations) > 1024 {
		return errors.New("citations are invalid")
	}
	for _, rawCitation := range citations {
		if err := validateAnthropicCitation(rawCitation); err != nil {
			return err
		}
	}
	return nil
}

func validateAnthropicCitation(raw json.RawMessage) error {
	var citation map[string]json.RawMessage
	if len(raw) > protocolcore.MaxTextBytes ||
		json.Unmarshal(raw, &citation) != nil || citation == nil {
		return errors.New("citation is invalid")
	}
	var kind string
	if json.Unmarshal(citation["type"], &kind) != nil || kind == "" ||
		len(kind) > 128 || !utf8.ValidString(kind) || strings.TrimSpace(kind) != kind {
		return errors.New("citation type is invalid")
	}
	for _, character := range kind {
		if unicode.IsControl(character) {
			return errors.New("citation type is invalid")
		}
	}
	return nil
}

func validateAnthropicToolCaller(raw json.RawMessage, mode requestDecodeMode) error {
	var caller anthropicToolCallerWire
	if err := mode.decode(raw, &caller); err != nil {
		return err
	}
	switch caller.Type {
	case "direct":
		if caller.ToolID != "" {
			return errors.New("direct tool caller contains a tool ID")
		}
	case "code_execution_20250825", "code_execution_20260120":
		if caller.ToolID == "" {
			return errors.New("server tool caller is missing its tool ID")
		}
	default:
		return errors.New("tool caller type is unsupported")
	}
	return nil
}

func (codec *Codec) decodeToolDefinition(
	index int,
	wire anthropicToolDefinitionWire,
	mode requestDecodeMode,
) (protocolcore.ToolDefinition, protocolcore.TranslationReport, error) {
	path := fmt.Sprintf("$.tools[%d]", index)
	if wire.Type != "" && wire.Type != "custom" {
		if mode == requestDecodeNative {
			// The original body owns the native configuration. Keep its identity
			// for named choices and tool decisions without inventing a schema.
			tool := protocolcore.ToolDefinition{
				Kind: protocolcore.ToolKindNative, Name: wire.Name, NativeType: wire.Type,
			}
			if err := tool.Validate(); err != nil {
				return protocolcore.ToolDefinition{}, protocolcore.TranslationReport{},
					protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
			}
			return tool, protocolcore.TranslationReport{}, nil
		}
		return protocolcore.ToolDefinition{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				path+".type",
				errors.New("native tool types are unsupported"),
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

func decodeToolResultContent(
	raw json.RawMessage,
	path string,
	mode requestDecodeMode,
) (string, [][]byte, error) {
	if !rawPresent(raw) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", nil, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
	}
	var combined bytes.Buffer
	var native [][]byte
	for index, rawBlock := range blocks {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(rawBlock, &header); err != nil {
			return "", nil, protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				fmt.Sprintf("%s[%d]", path, index),
				err,
			)
		}
		if header.Type == "tool_reference" && mode == requestDecodeNative {
			var reference anthropicToolReferenceBlockWire
			if err := mode.decode(rawBlock, &reference); err != nil ||
				reference.Type != "tool_reference" ||
				!validToolReferenceName(reference.ToolName) {
				if err == nil {
					err = errors.New("tool reference is invalid")
				}
				return "", nil, protocolcore.NewFailure(
					protocolcore.ReasonInvalidClientRequest,
					fmt.Sprintf("%s[%d]", path, index),
					err,
				)
			}
			native = append(native, rawBlock)
			continue
		}
		if mode == requestDecodeNative && header.Type != "" && header.Type != "text" {
			native = append(native, rawBlock)
			continue
		}
		var block anthropicTextBlockWire
		if err := mode.decode(rawBlock, &block); err != nil {
			return "", nil, protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				fmt.Sprintf("%s[%d]", path, index),
				errors.New("tool result contains a non-text block"),
			)
		}
		if block.Type != "text" || (mode == requestDecodeStrict && rawPresent(block.CacheControl)) {
			return "", nil, protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				fmt.Sprintf("%s[%d]", path, index),
				errors.New("tool result content cannot be represented losslessly"),
			)
		}
		combined.WriteString(block.Text)
	}
	return combined.String(), native, nil
}

func validToolReferenceName(value string) bool {
	if value == "" || len(value) > protocolcore.MaxToolNameBytes ||
		!utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func cacheNotice(path string) protocolcore.TranslationReport {
	return protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
		Code: protocolcore.NoticeCacheControlNotForwarded,
		Path: path,
	})
}

// decodeTranslatedBlock decodes the content blocks the translated profile
// handles differently from the strict one. handled is false for every other
// block, which the shared decoder then handles; a handled block that is
// dropped returns no block.
func (codec *Codec) decodeTranslatedBlock(
	role protocolcore.Role,
	kind string,
	raw []byte,
	path string,
	budget *protocolcore.ResourceBudget,
	report *protocolcore.TranslationReportBuilder,
) (*protocolcore.ContentBlock, bool, error) {
	switch {
	case kind == "image" || kind == "document":
		if role != protocolcore.RoleUser {
			return nil, true, protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				path,
				errors.New("media block is not in a user message"),
			)
		}
		media, mediaReport, err := decodeMedia(kind, raw, path)
		if err != nil {
			return nil, true, err
		}
		block, err := protocolcore.NewMediaBlock(media)
		if err != nil {
			return nil, true, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
		}
		if err := protocolcore.AppendReportWithin(report, mediaReport, budget); err != nil {
			return nil, true, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$.messages", err)
		}
		return &block, true, nil
	case kind == "thinking" || kind == "redacted_thinking":
		if role != protocolcore.RoleAssistant {
			return nil, true, protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				path,
				errors.New("provider thinking block is not in an assistant message"),
			)
		}
		block, err := decodeReasoningBlock(kind, raw, path)
		if err != nil {
			return nil, true, err
		}
		return &block, true, nil
	case role == protocolcore.RoleAssistant && serverToolHistory(kind):
		// A conversation that began on a native Anthropic route keeps the
		// server tools' calls and results in its history. Another provider
		// cannot replay them, and refusing them would fail every later turn.
		if err := protocolcore.AppendReportWithin(report, protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
			Code: protocolcore.NoticeServerToolHistoryNotForwarded,
			Path: path,
		}), budget); err != nil {
			return nil, true, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$.messages", err)
		}
		return nil, true, nil
	default:
		return nil, false, nil
	}
}

// serverToolHistory names the blocks a server tool leaves in an assistant
// turn: its call (server_tool_use, or mcp_tool_use for the MCP connector) and
// every *_tool_result except the client's own tool_result.
func serverToolHistory(kind string) bool {
	switch {
	case kind == "server_tool_use", kind == "mcp_tool_use":
		return true
	case kind == "tool_result":
		return false
	default:
		return strings.HasSuffix(kind, "_tool_result") && len(kind) > len("_tool_result")
	}
}

// decodeReasoningBlock decodes a replayed thinking or redacted_thinking block
// as positioned reasoning. A signature that is a seal ViberMate minted for a
// Responses reasoning item becomes that seal; any other signature is
// Anthropic's own and stays opaque, for an Anthropic provider only.
func decodeReasoningBlock(kind string, raw []byte, path string) (protocolcore.ContentBlock, error) {
	var reasoning protocolcore.Reasoning
	if kind == "redacted_thinking" {
		var wire anthropicRedactedThinkingBlockWire
		if err := decodeStrict(raw, &wire); err != nil {
			return protocolcore.ContentBlock{}, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
		}
		reasoning = protocolcore.Reasoning{
			Redacted: true,
			Seal:     protocolcore.ReasoningSeal{Format: protocolcore.SealAnthropicRedacted, Opaque: wire.Data},
		}
	} else {
		var wire anthropicThinkingBlockWire
		if err := decodeStrict(raw, &wire); err != nil {
			return protocolcore.ContentBlock{}, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
		}
		reasoning.Text = wire.Thinking
		// Only Responses seals are ever minted into an Anthropic signature;
		// a vma1. seal there is not ViberMate's and stays opaque too.
		if seal, source, ok := protocolcore.ParseReasoningSeal(wire.Signature); ok &&
			seal.Format == protocolcore.SealResponsesEncrypted {
			reasoning.TextSource, reasoning.Seal = source, seal
		} else if wire.Signature != "" {
			reasoning.Seal = protocolcore.ReasoningSeal{Format: protocolcore.SealAnthropicSignature, Opaque: wire.Signature}
		}
	}
	block, err := protocolcore.NewReasoningBlock(reasoning)
	if err != nil {
		return protocolcore.ContentBlock{}, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
	}
	return block, nil
}

// decodeMedia decodes an image or document block, in a user message or a tool
// result, without decoding its payload.
func decodeMedia(kind string, raw []byte, path string) (protocolcore.Media, protocolcore.TranslationReport, error) {
	invalid := func(at string, err error) (protocolcore.Media, protocolcore.TranslationReport, error) {
		return protocolcore.Media{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, at, err)
	}
	var notices []protocolcore.TranslationNotice
	var source json.RawMessage
	media := protocolcore.Media{Kind: protocolcore.MediaImage}
	if kind == "image" {
		var wire anthropicImageBlockWire
		if err := decodeStrict(raw, &wire); err != nil {
			return invalid(path, err)
		}
		source = wire.Source
		if rawPresent(wire.CacheControl) {
			notices = append(notices, protocolcore.TranslationNotice{Code: protocolcore.NoticeCacheControlNotForwarded, Path: path + ".cache_control"})
		}
	} else {
		var wire anthropicDocumentBlockWire
		if err := decodeStrict(raw, &wire); err != nil {
			return invalid(path, err)
		}
		media.Kind, source = protocolcore.MediaDocument, wire.Source
		if wire.Title != nil {
			media.Title = *wire.Title
		}
		// A document's context is text the model reads beside the document.
		// The neutral model has no place for it, and dropping model-visible
		// text would change the answer, so it is refused, not declared.
		if rawPresent(wire.Context) {
			return protocolcore.Media{}, protocolcore.TranslationReport{}, protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				path+".context",
				errors.New("document context cannot be represented by the configured provider dialect"),
			)
		}
		if rawPresent(wire.Citations) {
			var citations anthropicDocumentCitationsWire
			if err := decodeStrict(wire.Citations, &citations); err != nil {
				return invalid(path+".citations", err)
			}
			if citations.Enabled {
				notices = append(notices, protocolcore.TranslationNotice{Code: protocolcore.NoticeCitationsNotForwarded, Path: path + ".citations"})
			}
		}
		if rawPresent(wire.CacheControl) {
			notices = append(notices, protocolcore.TranslationNotice{Code: protocolcore.NoticeCacheControlNotForwarded, Path: path + ".cache_control"})
		}
	}
	var header struct {
		Type string `json:"type"`
	}
	if !rawPresent(source) || json.Unmarshal(source, &header) != nil {
		return invalid(path+".source", errors.New("media source is not an object"))
	}
	supported := map[string]bool{"base64": true, "url": true, "file": true}
	if media.Kind == protocolcore.MediaDocument {
		supported["text"] = true
	}
	if !supported[header.Type] {
		// Content-block documents and future source kinds have no neutral
		// form.
		return protocolcore.Media{}, protocolcore.TranslationReport{}, protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedClientInput,
			path+".source.type",
			errors.New("media source type cannot be represented by the configured provider dialect"),
		)
	}
	var wire anthropicMediaSourceWire
	if err := decodeStrict(source, &wire); err != nil {
		return invalid(path+".source", err)
	}
	// Each source type names exactly its own payload member.
	payloads := 0
	for _, value := range []string{wire.Data, wire.URL, wire.FileID} {
		if value != "" {
			payloads++
		}
	}
	switch header.Type {
	case "base64":
		if payloads != 1 || wire.Data == "" || wire.MediaType == "" {
			return invalid(path+".source", errors.New("base64 media source is incomplete"))
		}
		media.MediaType, media.Data = wire.MediaType, wire.Data
	case "text":
		if payloads != 1 || wire.Data == "" || wire.MediaType != "text/plain" {
			return invalid(path+".source", errors.New("text document source is incomplete"))
		}
		media.MediaType, media.Text = wire.MediaType, wire.Data
	case "url":
		if payloads != 1 || wire.URL == "" || wire.MediaType != "" {
			return invalid(path+".source", errors.New("URL media source is invalid"))
		}
		media.URL = wire.URL
	case "file":
		if payloads != 1 || wire.FileID == "" || wire.MediaType != "" {
			return invalid(path+".source", errors.New("file media source is invalid"))
		}
		media.FileID, media.FileSource = wire.FileID, protocolspec.DialectAnthropicMessages
	}
	if err := media.Validate(); err != nil {
		return invalid(path+".source", err)
	}
	return media, protocolcore.NewTranslationReport(notices...), nil
}

// decodeTranslatedToolResultContent keeps a tool result's block list as
// ordered parts: text, images, documents and tool_reference loads. Content is
// the concatenation of the text parts, exactly as the strict decoder builds
// it, so every reader of Content keeps its meaning. A string or an empty list
// has no parts.
func decodeTranslatedToolResultContent(
	raw json.RawMessage,
	path string,
	budget *protocolcore.ResourceBudget,
) (string, *protocolcore.ToolResultParts, protocolcore.TranslationReport, error) {
	if !rawPresent(raw) {
		return "", nil, protocolcore.TranslationReport{}, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil, protocolcore.TranslationReport{}, nil
	}
	var rawParts []json.RawMessage
	if err := json.Unmarshal(raw, &rawParts); err != nil {
		return "", nil, protocolcore.TranslationReport{}, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path, err)
	}
	if len(rawParts) == 0 {
		return "", nil, protocolcore.TranslationReport{}, nil
	}
	if len(rawParts) > protocolcore.MaxToolResultParts {
		return "", nil, protocolcore.TranslationReport{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			path,
			errors.New("tool result part count exceeds its limit"),
		)
	}
	parts := make([]protocolcore.ToolResultPart, 0, len(rawParts))
	var combined strings.Builder
	var report protocolcore.TranslationReportBuilder
	for index, rawPart := range rawParts {
		partPath := fmt.Sprintf("%s[%d]", path, index)
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(rawPart, &header); err != nil {
			return "", nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, partPath, err)
		}
		var notices protocolcore.TranslationReport
		switch header.Type {
		case "text":
			var wire anthropicTextBlockWire
			if err := decodeStrict(rawPart, &wire); err != nil {
				return "", nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, partPath, err)
			}
			var declared []protocolcore.TranslationNotice
			if rawPresent(wire.Citations) {
				declared = append(declared, protocolcore.TranslationNotice{Code: protocolcore.NoticeCitationsNotForwarded, Path: partPath + ".citations"})
			}
			if rawPresent(wire.CacheControl) {
				declared = append(declared, protocolcore.TranslationNotice{Code: protocolcore.NoticeCacheControlNotForwarded, Path: partPath + ".cache_control"})
			}
			notices = protocolcore.NewTranslationReport(declared...)
			parts = append(parts, protocolcore.ToolResultPart{Kind: protocolcore.ToolResultPartText, Text: wire.Text})
			combined.WriteString(wire.Text)
		case "image", "document":
			media, mediaReport, err := decodeMedia(header.Type, rawPart, partPath)
			if err != nil {
				return "", nil, report.Build(), err
			}
			notices = mediaReport
			parts = append(parts, protocolcore.ToolResultPart{Kind: protocolcore.ToolResultPartMedia, Media: &media})
		case "tool_reference":
			var reference anthropicToolReferenceBlockWire
			if err := decodeStrict(rawPart, &reference); err != nil || !validToolReferenceName(reference.ToolName) {
				if err == nil {
					err = errors.New("tool reference is invalid")
				}
				return "", nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, partPath, err)
			}
			parts = append(parts, protocolcore.ToolResultPart{Kind: protocolcore.ToolResultPartToolReference, ToolName: reference.ToolName})
		default:
			return "", nil, report.Build(), protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				partPath+".type",
				errors.New("tool result block type cannot be represented by the configured provider dialect"),
			)
		}
		if err := protocolcore.AppendReportWithin(&report, notices, budget); err != nil {
			return "", nil, report.Build(), protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$.messages", err)
		}
	}
	return combined.String(), &protocolcore.ToolResultParts{Items: parts}, report.Build(), nil
}
