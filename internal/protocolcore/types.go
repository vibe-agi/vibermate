package protocolcore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxModelBytes                 = 512
	MaxMessageCount               = 4096
	MaxContentBlocks              = 4096
	MaxTextBytes                  = 8 << 20
	MaxProviderExtensions         = 256
	MaxProviderExtensionFragments = 1 << 20
	MaxProviderExtensionBytes     = 16 << 20
	MaxToolCount                  = 256
	MaxToolNameBytes              = 256
	MaxToolNamespaceBytes         = 256
	MaxToolJSONBytes              = 4 << 20
	MaxCustomToolFormatBytes      = 1 << 20
	MaxOutputSchemaBytes          = 4 << 20
	MaxStopSequenceCount          = 32
	MaxStopSequenceBytes          = 1024
	MaxProtocolEvidenceValues     = 8192
	MaxProtocolEvidenceNameBytes  = 128
	MaxProtocolEvidenceValueBytes = 512
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type BlockKind string

const (
	BlockText              BlockKind = "text"
	BlockRefusal           BlockKind = "refusal"
	BlockToolCall          BlockKind = "tool_call"
	BlockToolResult        BlockKind = "tool_result"
	BlockProviderExtension BlockKind = "provider_extension"
)

type StopReason string

const (
	StopReasonEndTurn      StopReason = "end_turn"
	StopReasonMaxTokens    StopReason = "max_tokens"
	StopReasonToolUse      StopReason = "tool_use"
	StopReasonStopSequence StopReason = "stop_sequence"
)

// JSONDocument owns one complete JSON value.
type JSONDocument struct {
	value []byte
}

func NewJSONObject(value []byte, maxBytes int) (JSONDocument, error) {
	if maxBytes <= 0 {
		return JSONDocument{}, errors.New("JSON byte limit must be positive")
	}
	if len(value) == 0 || len(value) > maxBytes {
		return JSONDocument{}, errors.New("JSON object has an invalid size")
	}
	if !json.Valid(value) {
		return JSONDocument{}, errors.New("JSON object is invalid")
	}
	if err := rejectDuplicateJSONNames(value); err != nil {
		return JSONDocument{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return JSONDocument{}, errors.New("JSON value is not an object")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return JSONDocument{}, errors.New("JSON object has trailing data")
	}
	owned := bytes.Clone(value)
	return JSONDocument{value: owned}, nil
}

func (document JSONDocument) Bytes() []byte {
	return bytes.Clone(document.value)
}

func (document JSONDocument) IsZero() bool {
	return len(document.value) == 0
}

type CallKey struct {
	source string
	wireID string
}

func NewCallKey(source, wireID string) (CallKey, error) {
	if err := validateIdentifier("tool call source", source, 64); err != nil {
		return CallKey{}, err
	}
	if err := validateIdentifier("tool call wire ID", wireID, 512); err != nil {
		return CallKey{}, err
	}
	return CallKey{source: source, wireID: wireID}, nil
}

func (key CallKey) Source() string {
	return key.source
}

func (key CallKey) WireID() string {
	return key.wireID
}

func (key CallKey) IsZero() bool {
	return key.source == "" || key.wireID == ""
}

type ToolCall struct {
	Kind      ToolKind
	Key       CallKey
	ItemKey   CallKey
	Namespace string
	Name      string
	Arguments JSONDocument
	Input     string
}

func (call ToolCall) Validate() error {
	if call.Key.IsZero() {
		return errors.New("tool call key is empty")
	}
	if !call.ItemKey.IsZero() {
		if err := validateIdentifier(
			"tool call item source",
			call.ItemKey.Source(),
			64,
		); err != nil {
			return err
		}
	}
	if call.Namespace != "" {
		if err := validateIdentifier(
			"tool namespace",
			call.Namespace,
			MaxToolNamespaceBytes,
		); err != nil {
			return err
		}
	}
	if err := validateIdentifier("tool name", call.Name, MaxToolNameBytes); err != nil {
		return err
	}
	switch call.EffectiveKind() {
	case ToolKindFunction:
		if call.Arguments.IsZero() {
			return errors.New("function tool call arguments are empty")
		}
		if call.Input != "" {
			return errors.New("function tool call contains custom input")
		}
	case ToolKindCustom:
		if !call.Arguments.IsZero() {
			return errors.New("custom tool call contains JSON arguments")
		}
		if err := validateText(
			"custom tool call input",
			call.Input,
			MaxToolJSONBytes,
			true,
		); err != nil {
			return err
		}
	default:
		return errors.New("tool call kind is unsupported")
	}
	return nil
}

func (call ToolCall) EffectiveKind() ToolKind {
	if call.Kind == "" {
		return ToolKindFunction
	}
	return call.Kind
}

func (call ToolCall) Clone() ToolCall {
	cloned := call
	if !call.Arguments.IsZero() {
		cloned.Arguments = JSONDocument{value: call.Arguments.Bytes()}
	}
	return cloned
}

type ToolResult struct {
	Key       CallKey
	Namespace string
	Name      string
	Content   string
	IsError   bool
}

// ToolIntent is created only after a complete provider tool call has been
// decoded and its arguments have been validated as one JSON object.
type ToolIntent struct {
	ResponseID string
	Ordinal    int
	Call       ToolCall
}

func (intent ToolIntent) Validate() error {
	if err := validateIdentifier("tool intent response ID", intent.ResponseID, 512); err != nil {
		return err
	}
	if intent.Ordinal < 0 {
		return errors.New("tool intent ordinal is negative")
	}
	return intent.Call.Validate()
}

func (intent ToolIntent) Clone() ToolIntent {
	cloned := intent
	cloned.Call = intent.Call.Clone()
	return cloned
}

func (result ToolResult) Validate() error {
	if result.Key.IsZero() {
		return errors.New("tool result key is empty")
	}
	if (result.Namespace == "") != (result.Name == "") {
		return errors.New("tool result identity is incomplete")
	}
	if result.Namespace != "" {
		if err := validateIdentifier(
			"tool result namespace",
			result.Namespace,
			MaxToolNamespaceBytes,
		); err != nil {
			return err
		}
		if err := validateIdentifier("tool result name", result.Name, MaxToolNameBytes); err != nil {
			return err
		}
	}
	return validateText("tool result content", result.Content, MaxTextBytes, true)
}

type ContentBlock struct {
	Kind              BlockKind
	Text              string
	Refusal           string
	ToolCall          ToolCall
	ToolResult        ToolResult
	ProviderExtension ProviderExtension
	// Agent is block-scoped for provider output, where a single response may
	// contain items produced by different agents. Request-side agent evidence
	// remains message-scoped on Message.Agent.
	Agent *AgentMessageContext
}

func NewTextBlock(text string) (ContentBlock, error) {
	if err := validateText("text block", text, MaxTextBytes, true); err != nil {
		return ContentBlock{}, err
	}
	return ContentBlock{Kind: BlockText, Text: text}, nil
}

func NewRefusalBlock(refusal string) (ContentBlock, error) {
	if err := validateText("refusal block", refusal, MaxTextBytes, false); err != nil {
		return ContentBlock{}, err
	}
	return ContentBlock{Kind: BlockRefusal, Refusal: refusal}, nil
}

func NewToolCallBlock(call ToolCall) (ContentBlock, error) {
	if err := call.Validate(); err != nil {
		return ContentBlock{}, err
	}
	return ContentBlock{Kind: BlockToolCall, ToolCall: call.Clone()}, nil
}

func NewToolResultBlock(result ToolResult) (ContentBlock, error) {
	if err := result.Validate(); err != nil {
		return ContentBlock{}, err
	}
	return ContentBlock{Kind: BlockToolResult, ToolResult: result}, nil
}

func NewProviderExtensionBlock(extension ProviderExtension) (ContentBlock, error) {
	if err := extension.Validate(); err != nil {
		return ContentBlock{}, err
	}
	return ContentBlock{
		Kind:              BlockProviderExtension,
		ProviderExtension: extension.Clone(),
	}, nil
}

func (block ContentBlock) Validate() error {
	if block.Agent != nil {
		if err := block.Agent.Validate(); err != nil {
			return err
		}
	}
	switch block.Kind {
	case BlockText:
		return validateText("text block", block.Text, MaxTextBytes, true)
	case BlockRefusal:
		return validateText("refusal block", block.Refusal, MaxTextBytes, false)
	case BlockToolCall:
		return block.ToolCall.Validate()
	case BlockToolResult:
		return block.ToolResult.Validate()
	case BlockProviderExtension:
		return block.ProviderExtension.Validate()
	default:
		return errors.New("content block kind is unsupported")
	}
}

func (block ContentBlock) Clone() ContentBlock {
	cloned := block
	cloned.ToolCall = block.ToolCall.Clone()
	cloned.ProviderExtension = block.ProviderExtension.Clone()
	if block.Agent != nil {
		context := *block.Agent
		cloned.Agent = &context
	}
	return cloned
}

type Message struct {
	Role   Role
	Blocks []ContentBlock
	// Agent identifies authoritative inter-agent provenance carried by the
	// source protocol. It is intentionally message-scoped: an agent message,
	// call, or call result still has ordinary content blocks, while this field
	// preserves who produced it and (for agent messages) its directed edge.
	Agent *AgentMessageContext
}

type AgentMessageContext struct {
	AgentName string
	Author    string
	Recipient string
}

func (context AgentMessageContext) Validate() error {
	if context.AgentName == "" && context.Author == "" && context.Recipient == "" {
		return errors.New("agent message context is empty")
	}
	for label, value := range map[string]string{
		"agent name":      context.AgentName,
		"agent author":    context.Author,
		"agent recipient": context.Recipient,
	} {
		if value == "" {
			continue
		}
		if err := validateIdentifier(label, value, 512); err != nil {
			return err
		}
	}
	if (context.Author == "") != (context.Recipient == "") {
		return errors.New("agent message direction is incomplete")
	}
	return nil
}

func (message Message) Validate() error {
	switch message.Role {
	case RoleSystem, RoleDeveloper, RoleUser, RoleAssistant, RoleTool:
	default:
		return errors.New("message role is unsupported")
	}
	if len(message.Blocks) == 0 || len(message.Blocks) > MaxContentBlocks {
		return errors.New("message content block count is invalid")
	}
	if message.Agent != nil {
		if err := message.Agent.Validate(); err != nil {
			return err
		}
	}
	for index, block := range message.Blocks {
		if err := block.Validate(); err != nil {
			return fmt.Errorf("content block %d: %w", index, err)
		}
		switch message.Role {
		case RoleSystem, RoleDeveloper:
			if block.Kind != BlockText {
				return errors.New("instruction message contains a non-text block")
			}
		case RoleUser:
			if block.Kind != BlockText && block.Kind != BlockToolResult {
				return errors.New("user message contains an unsupported block")
			}
		case RoleAssistant:
			if block.Kind != BlockText &&
				block.Kind != BlockRefusal &&
				block.Kind != BlockToolCall &&
				block.Kind != BlockProviderExtension {
				return errors.New("assistant message contains an unsupported block")
			}
		case RoleTool:
			if block.Kind != BlockToolResult {
				return errors.New("tool message contains a non-result block")
			}
		}
	}
	return nil
}

func (message Message) Clone() Message {
	cloned := message
	if message.Agent != nil {
		context := *message.Agent
		cloned.Agent = &context
	}
	cloned.Blocks = make([]ContentBlock, len(message.Blocks))
	for index, block := range message.Blocks {
		cloned.Blocks[index] = block.Clone()
	}
	return cloned
}

type ToolKind string

const (
	ToolKindFunction ToolKind = "function"
	ToolKindCustom   ToolKind = "custom"
)

type CustomToolFormatKind string

const (
	CustomToolFormatText    CustomToolFormatKind = "text"
	CustomToolFormatGrammar CustomToolFormatKind = "grammar"
)

type CustomToolFormat struct {
	Kind       CustomToolFormatKind
	Syntax     string
	Definition string
}

func (format CustomToolFormat) Validate() error {
	switch format.Kind {
	case CustomToolFormatText:
		if format.Syntax != "" || format.Definition != "" {
			return errors.New("text custom-tool format contains a grammar")
		}
	case CustomToolFormatGrammar:
		if format.Syntax != "lark" && format.Syntax != "regex" {
			return errors.New("custom-tool grammar syntax is unsupported")
		}
		if err := validateText(
			"custom-tool grammar",
			format.Definition,
			MaxCustomToolFormatBytes,
			false,
		); err != nil {
			return err
		}
	default:
		return errors.New("custom-tool format kind is unsupported")
	}
	return nil
}

func (format CustomToolFormat) IsZero() bool {
	return format.Kind == "" &&
		format.Syntax == "" &&
		format.Definition == ""
}

type ToolDefinition struct {
	Kind                ToolKind
	Name                string
	Description         string
	InputSchema         JSONDocument
	CustomFormat        CustomToolFormat
	StrictKnown         bool
	Strict              bool
	EagerInputStreaming bool
}

func (definition ToolDefinition) Validate() error {
	if err := validateIdentifier("tool name", definition.Name, MaxToolNameBytes); err != nil {
		return err
	}
	if err := validateText("tool description", definition.Description, MaxTextBytes, true); err != nil {
		return err
	}
	switch definition.EffectiveKind() {
	case ToolKindFunction:
		if definition.InputSchema.IsZero() {
			return errors.New("function tool input schema is empty")
		}
		if !definition.CustomFormat.IsZero() {
			return errors.New("function tool contains a custom format")
		}
		if !definition.StrictKnown && definition.Strict {
			return errors.New("unknown function strictness is true")
		}
	case ToolKindCustom:
		if !definition.InputSchema.IsZero() {
			return errors.New("custom tool contains an input schema")
		}
		if definition.StrictKnown || definition.Strict {
			return errors.New("custom tool contains function strictness")
		}
		if err := definition.CustomFormat.Validate(); err != nil {
			return err
		}
	default:
		return errors.New("tool definition kind is unsupported")
	}
	return nil
}

func (definition ToolDefinition) EffectiveKind() ToolKind {
	if definition.Kind == "" {
		return ToolKindFunction
	}
	return definition.Kind
}

func (definition ToolDefinition) Clone() ToolDefinition {
	cloned := definition
	if !definition.InputSchema.IsZero() {
		cloned.InputSchema = JSONDocument{value: definition.InputSchema.Bytes()}
	}
	return cloned
}

type ToolNamespace struct {
	Name        string
	Description string
	Tools       []ToolDefinition
}

func (namespace ToolNamespace) Validate() error {
	if err := validateIdentifier(
		"tool namespace",
		namespace.Name,
		MaxToolNamespaceBytes,
	); err != nil {
		return err
	}
	if err := validateText(
		"tool namespace description",
		namespace.Description,
		MaxTextBytes,
		false,
	); err != nil {
		return err
	}
	if len(namespace.Tools) == 0 || len(namespace.Tools) > MaxToolCount {
		return errors.New("tool namespace member count is invalid")
	}
	names := make(map[string]struct{}, len(namespace.Tools))
	for index, tool := range namespace.Tools {
		if err := tool.Validate(); err != nil {
			return fmt.Errorf("tool namespace member %d: %w", index, err)
		}
		if _, duplicate := names[tool.Name]; duplicate {
			return errors.New("tool namespace member name is duplicated")
		}
		names[tool.Name] = struct{}{}
	}
	return nil
}

func (namespace ToolNamespace) Clone() ToolNamespace {
	cloned := namespace
	cloned.Tools = make([]ToolDefinition, len(namespace.Tools))
	for index, tool := range namespace.Tools {
		cloned.Tools[index] = tool.Clone()
	}
	return cloned
}

type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceNamed    ToolChoiceMode = "named"
	ToolChoiceNone     ToolChoiceMode = "none"
)

type ToolChoice struct {
	Mode            ToolChoiceMode
	Name            string
	DisableParallel bool
}

func (choice ToolChoice) Validate(toolCount int) error {
	switch choice.Mode {
	case "":
		return nil
	case ToolChoiceAuto, ToolChoiceNone:
		if choice.Name != "" {
			return errors.New("tool choice has an unexpected name")
		}
	case ToolChoiceRequired:
		if toolCount == 0 || choice.Name != "" {
			return errors.New("required tool choice is invalid")
		}
	case ToolChoiceNamed:
		if toolCount == 0 {
			return errors.New("named tool choice has no tool definitions")
		}
		if err := validateIdentifier("tool choice name", choice.Name, MaxToolNameBytes); err != nil {
			return err
		}
	default:
		return errors.New("tool choice mode is unsupported")
	}
	return nil
}

type ThinkingMode string

const (
	ThinkingModeDisabled ThinkingMode = "disabled"
	ThinkingModeAdaptive ThinkingMode = "adaptive"
	ThinkingModeEnabled  ThinkingMode = "enabled"
)

type ThinkingDisplay string

const (
	ThinkingDisplaySummarized ThinkingDisplay = "summarized"
	ThinkingDisplayOmitted    ThinkingDisplay = "omitted"
)

type ReasoningEffort string

const (
	ReasoningEffortNone    ReasoningEffort = "none"
	ReasoningEffortMinimal ReasoningEffort = "minimal"
	ReasoningEffortLow     ReasoningEffort = "low"
	ReasoningEffortMedium  ReasoningEffort = "medium"
	ReasoningEffortHigh    ReasoningEffort = "high"
	ReasoningEffortXHigh   ReasoningEffort = "xhigh"
	ReasoningEffortMax     ReasoningEffort = "max"
)

type ReasoningContext string

const (
	ReasoningContextAuto        ReasoningContext = "auto"
	ReasoningContextCurrentTurn ReasoningContext = "current_turn"
	ReasoningContextAllTurns    ReasoningContext = "all_turns"
)

type ReasoningSummary string

const (
	ReasoningSummaryAuto     ReasoningSummary = "auto"
	ReasoningSummaryConcise  ReasoningSummary = "concise"
	ReasoningSummaryDetailed ReasoningSummary = "detailed"
)

type ReasoningExecutionMode string

const (
	ReasoningExecutionStandard ReasoningExecutionMode = "standard"
	ReasoningExecutionPro      ReasoningExecutionMode = "pro"
)

// ReasoningIntent is the provider-neutral request intent. Thinking controls
// whether the source dialect asks for reasoning blocks; Effort independently
// controls the requested amount of model work.
type ReasoningIntent struct {
	Thinking     ThinkingMode
	BudgetTokens int
	Display      ThinkingDisplay
	Context      ReasoningContext
	Effort       ReasoningEffort
	Summary      ReasoningSummary
	Execution    ReasoningExecutionMode
	TaskBudget   TaskBudget
}

type TaskBudget struct {
	Present         bool
	TotalTokens     int
	RemainingKnown  bool
	RemainingTokens int
}

func (budget TaskBudget) Validate() error {
	if !budget.Present {
		if budget.TotalTokens != 0 ||
			budget.RemainingKnown ||
			budget.RemainingTokens != 0 {
			return errors.New("absent task budget contains configuration")
		}
		return nil
	}
	if budget.TotalTokens <= 0 {
		return errors.New("task budget total is invalid")
	}
	if budget.RemainingKnown &&
		(budget.RemainingTokens < 0 ||
			budget.RemainingTokens > budget.TotalTokens) {
		return errors.New("task budget remaining value is invalid")
	}
	if !budget.RemainingKnown && budget.RemainingTokens != 0 {
		return errors.New("unknown task budget remaining value is nonzero")
	}
	return nil
}

func (intent ReasoningIntent) Validate(maxOutputTokens int) error {
	switch intent.Effort {
	case "",
		ReasoningEffortNone,
		ReasoningEffortMinimal,
		ReasoningEffortLow,
		ReasoningEffortMedium,
		ReasoningEffortHigh,
		ReasoningEffortXHigh,
		ReasoningEffortMax:
	default:
		return errors.New("reasoning effort is unsupported")
	}
	switch intent.Context {
	case "",
		ReasoningContextAuto,
		ReasoningContextCurrentTurn,
		ReasoningContextAllTurns:
	default:
		return errors.New("reasoning context is unsupported")
	}
	switch intent.Summary {
	case "",
		ReasoningSummaryAuto,
		ReasoningSummaryConcise,
		ReasoningSummaryDetailed:
	default:
		return errors.New("reasoning summary is unsupported")
	}
	switch intent.Execution {
	case "",
		ReasoningExecutionStandard,
		ReasoningExecutionPro:
	default:
		return errors.New("reasoning execution mode is unsupported")
	}
	switch intent.Display {
	case "", ThinkingDisplaySummarized, ThinkingDisplayOmitted:
	default:
		return errors.New("thinking display is unsupported")
	}
	switch intent.Thinking {
	case "":
		if intent.BudgetTokens != 0 || intent.Display != "" {
			return errors.New("absent thinking contains configuration")
		}
	case ThinkingModeDisabled:
		if intent.BudgetTokens != 0 || intent.Display != "" {
			return errors.New("disabled thinking contains configuration")
		}
	case ThinkingModeAdaptive:
		if intent.BudgetTokens != 0 {
			return errors.New("adaptive thinking contains a token budget")
		}
	case ThinkingModeEnabled:
		if maxOutputTokens <= 0 ||
			intent.BudgetTokens < 1024 ||
			intent.BudgetTokens >= maxOutputTokens {
			return errors.New("enabled thinking token budget is invalid")
		}
	default:
		return errors.New("thinking mode is unsupported")
	}
	return intent.TaskBudget.Validate()
}

type TextVerbosity string

const (
	TextVerbosityLow    TextVerbosity = "low"
	TextVerbosityMedium TextVerbosity = "medium"
	TextVerbosityHigh   TextVerbosity = "high"
)

type ContextEditKind string

const (
	ContextEditClearThinking ContextEditKind = "clear_thinking"
)

type ContextEdit struct {
	Kind              ContextEditKind
	KeepAll           bool
	KeepThinkingTurns int
}

func (edit ContextEdit) Validate() error {
	switch edit.Kind {
	case ContextEditClearThinking:
		if edit.KeepAll == (edit.KeepThinkingTurns > 0) {
			return errors.New("clear-thinking retention is invalid")
		}
	default:
		return errors.New("context edit kind is unsupported")
	}
	return nil
}

type ContextManagementIntent struct {
	Edits []ContextEdit
}

func (intent ContextManagementIntent) Validate() error {
	if len(intent.Edits) > 16 {
		return errors.New("context edit count is invalid")
	}
	for index, edit := range intent.Edits {
		if err := edit.Validate(); err != nil {
			return fmt.Errorf("context edit %d: %w", index, err)
		}
	}
	return nil
}

func (intent ContextManagementIntent) Clone() ContextManagementIntent {
	return ContextManagementIntent{Edits: slices.Clone(intent.Edits)}
}

type DiagnosticsIntent struct {
	Requested         bool
	HasPrevious       bool
	PreviousMessageID string
}

func (intent DiagnosticsIntent) Validate() error {
	if !intent.Requested {
		if intent.HasPrevious || intent.PreviousMessageID != "" {
			return errors.New("absent diagnostics contains configuration")
		}
		return nil
	}
	if intent.HasPrevious {
		return validateIdentifier(
			"diagnostics previous message ID",
			intent.PreviousMessageID,
			512,
		)
	}
	if intent.PreviousMessageID != "" {
		return errors.New("diagnostics previous message ID is not marked present")
	}
	return nil
}

type StructuredOutputKind string

const (
	StructuredOutputJSONSchema StructuredOutputKind = "json_schema"
)

// StructuredOutputIntent is the provider-neutral constrained-output request.
// Its schema owns the source bytes and exposes only copied data.
type StructuredOutputIntent struct {
	Kind   StructuredOutputKind
	Schema JSONDocument
}

func NewJSONSchemaOutput(schema []byte) (StructuredOutputIntent, error) {
	var canonical bytes.Buffer
	if err := json.Compact(&canonical, schema); err != nil {
		return StructuredOutputIntent{}, errors.New("output schema is invalid JSON")
	}
	document, err := NewJSONObject(canonical.Bytes(), MaxOutputSchemaBytes)
	if err != nil {
		return StructuredOutputIntent{}, err
	}
	return StructuredOutputIntent{
		Kind:   StructuredOutputJSONSchema,
		Schema: document,
	}, nil
}

func (intent StructuredOutputIntent) Validate() error {
	switch intent.Kind {
	case "":
		if !intent.Schema.IsZero() {
			return errors.New("absent structured output contains a schema")
		}
	case StructuredOutputJSONSchema:
		if intent.Schema.IsZero() {
			return errors.New("JSON-schema output has no schema")
		}
	default:
		return errors.New("structured output kind is unsupported")
	}
	return nil
}

func (intent StructuredOutputIntent) Clone() StructuredOutputIntent {
	cloned := intent
	if !intent.Schema.IsZero() {
		cloned.Schema = JSONDocument{value: intent.Schema.Bytes()}
	}
	return cloned
}

type Request struct {
	RequestedModel string
	EffectiveModel string
	// MaxOutputTokens is zero only when the source dialect omitted a limit.
	MaxOutputTokens int
	Stream          bool
	System          []ContentBlock
	Messages        []Message
	Tools           []ToolDefinition
	ToolNamespaces  []ToolNamespace
	ToolChoice      ToolChoice
	Reasoning       ReasoningIntent
	Context         ContextManagementIntent
	Diagnostics     DiagnosticsIntent
	Output          StructuredOutputIntent
	OutputVerbosity TextVerbosity
	Temperature     *float64
	TopP            *float64
	StopSequences   []string
	// ProtocolEvidence retains bounded, non-secret client protocol identifiers
	// used for exact Conversation association. Names are explicitly namespaced
	// (for example openai_responses.turn_id); raw wire remains authoritative.
	ProtocolEvidence []ProtocolEvidenceValue
}

// ProtocolEvidenceValue preserves one client-protocol identifier without
// forcing heterogeneous Agent clients into a false shared wire model.
type ProtocolEvidenceValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (value ProtocolEvidenceValue) Validate() error {
	if !validProtocolEvidenceName(value.Name) {
		return errors.New("protocol evidence name is invalid")
	}
	if err := validateIdentifier(
		"protocol evidence value",
		value.Value,
		MaxProtocolEvidenceValueBytes,
	); err != nil {
		return err
	}
	return nil
}

func ValidateProtocolEvidence(values []ProtocolEvidenceValue) error {
	if len(values) > MaxProtocolEvidenceValues {
		return errors.New("protocol evidence count is invalid")
	}
	seenNames := make(map[string]struct{}, len(values))
	previous := ""
	for index, value := range values {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("protocol evidence %d: %w", index, err)
		}
		if index > 0 && value.Name <= previous {
			return errors.New("protocol evidence is not canonically ordered")
		}
		if _, duplicate := seenNames[value.Name]; duplicate {
			return errors.New("protocol evidence name is duplicated")
		}
		seenNames[value.Name] = struct{}{}
		previous = value.Name
	}
	return nil
}

func validProtocolEvidenceName(value string) bool {
	if value == "" || len(value) > MaxProtocolEvidenceNameBytes ||
		strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func (request Request) Validate() error {
	if err := validateIdentifier("requested model", request.RequestedModel, MaxModelBytes); err != nil {
		return err
	}
	if err := validateIdentifier("effective model", request.EffectiveModel, MaxModelBytes); err != nil {
		return err
	}
	if request.MaxOutputTokens < 0 {
		return errors.New("maximum output tokens cannot be negative")
	}
	if len(request.System) > MaxContentBlocks {
		return errors.New("system content block count is invalid")
	}
	for index, block := range request.System {
		if block.Kind != BlockText {
			return fmt.Errorf("system content block %d is not text", index)
		}
		if err := block.Validate(); err != nil {
			return fmt.Errorf("system content block %d: %w", index, err)
		}
	}
	if len(request.Messages) == 0 || len(request.Messages) > MaxMessageCount {
		return errors.New("message count is invalid")
	}
	providerExtensionCount := 0
	providerExtensionBytes := 0
	for index, message := range request.Messages {
		if err := message.Validate(); err != nil {
			return fmt.Errorf("message %d: %w", index, err)
		}
		for _, block := range message.Blocks {
			if block.Kind != BlockProviderExtension {
				continue
			}
			providerExtensionCount++
			providerExtensionBytes += block.ProviderExtension.byteSize()
			if providerExtensionCount > MaxProviderExtensions ||
				providerExtensionBytes > MaxProviderExtensionBytes {
				return errors.New("provider extensions exceed the request limit")
			}
		}
	}
	if len(request.Tools) > MaxToolCount ||
		len(request.ToolNamespaces) > MaxToolCount {
		return errors.New("tool definition count is invalid")
	}
	toolNames := make(map[string]struct{}, len(request.Tools))
	totalTools := len(request.Tools)
	for index, tool := range request.Tools {
		if err := tool.Validate(); err != nil {
			return fmt.Errorf("tool definition %d: %w", index, err)
		}
		if _, duplicate := toolNames[tool.Name]; duplicate {
			return errors.New("tool definition name is duplicated")
		}
		toolNames[tool.Name] = struct{}{}
	}
	namespaceNames := make(map[string]struct{}, len(request.ToolNamespaces))
	for index, namespace := range request.ToolNamespaces {
		if err := namespace.Validate(); err != nil {
			return fmt.Errorf("tool namespace %d: %w", index, err)
		}
		if _, duplicate := namespaceNames[namespace.Name]; duplicate {
			return errors.New("tool namespace name is duplicated")
		}
		namespaceNames[namespace.Name] = struct{}{}
		totalTools += len(namespace.Tools)
		if totalTools > MaxToolCount {
			return errors.New("total tool definition count is invalid")
		}
	}
	if err := request.ToolChoice.Validate(totalTools); err != nil {
		return err
	}
	if err := request.Reasoning.Validate(request.MaxOutputTokens); err != nil {
		return err
	}
	if err := request.Context.Validate(); err != nil {
		return err
	}
	if err := request.Diagnostics.Validate(); err != nil {
		return err
	}
	if err := request.Output.Validate(); err != nil {
		return err
	}
	switch request.OutputVerbosity {
	case "", TextVerbosityLow, TextVerbosityMedium, TextVerbosityHigh:
	default:
		return errors.New("text verbosity is unsupported")
	}
	if request.ToolChoice.Mode == ToolChoiceNamed {
		if _, exists := toolNames[request.ToolChoice.Name]; !exists {
			return errors.New("named tool choice is unresolved")
		}
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 1) {
		return errors.New("temperature is outside the supported range")
	}
	if request.TopP != nil && (*request.TopP < 0 || *request.TopP > 1) {
		return errors.New("top_p is outside the supported range")
	}
	if len(request.StopSequences) > MaxStopSequenceCount {
		return errors.New("stop sequence count is invalid")
	}
	for _, sequence := range request.StopSequences {
		if err := validateText("stop sequence", sequence, MaxStopSequenceBytes, false); err != nil {
			return err
		}
	}
	if err := ValidateProtocolEvidence(request.ProtocolEvidence); err != nil {
		return err
	}
	return nil
}

func (request Request) Clone() Request {
	cloned := request
	cloned.System = cloneBlocks(request.System)
	cloned.Messages = make([]Message, len(request.Messages))
	for index, message := range request.Messages {
		cloned.Messages[index] = message.Clone()
	}
	cloned.Tools = make([]ToolDefinition, len(request.Tools))
	for index, tool := range request.Tools {
		cloned.Tools[index] = tool.Clone()
	}
	cloned.ToolNamespaces = make([]ToolNamespace, len(request.ToolNamespaces))
	for index, namespace := range request.ToolNamespaces {
		cloned.ToolNamespaces[index] = namespace.Clone()
	}
	cloned.StopSequences = slices.Clone(request.StopSequences)
	cloned.ProtocolEvidence = slices.Clone(request.ProtocolEvidence)
	cloned.Context = request.Context.Clone()
	cloned.Output = request.Output.Clone()
	if request.Temperature != nil {
		value := *request.Temperature
		cloned.Temperature = &value
	}
	if request.TopP != nil {
		value := *request.TopP
		cloned.TopP = &value
	}
	return cloned
}

func (request Request) WithEffectiveModel(model string) (Request, error) {
	cloned := request.Clone()
	cloned.EffectiveModel = model
	if err := cloned.Validate(); err != nil {
		return Request{}, err
	}
	return cloned, nil
}

type UsageValue struct {
	Tokens int64
	Known  bool
	Source string
}

func (value UsageValue) Validate() error {
	if value.Tokens < 0 {
		return errors.New("usage token count is negative")
	}
	if value.Known && value.Source == "" {
		return errors.New("known usage has no source")
	}
	if !value.Known && (value.Tokens != 0 || value.Source != "") {
		return errors.New("unknown usage contains a value")
	}
	return nil
}

type Usage struct {
	InputUncached UsageValue
	CacheWrite    UsageValue
	CacheRead     UsageValue
	Output        UsageValue
	Reasoning     UsageValue
}

func (usage Usage) Validate() error {
	values := []UsageValue{
		usage.InputUncached,
		usage.CacheWrite,
		usage.CacheRead,
		usage.Output,
		usage.Reasoning,
	}
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return err
		}
	}
	if usage.Reasoning.Known &&
		usage.Output.Known &&
		usage.Reasoning.Tokens > usage.Output.Tokens {
		return errors.New("reasoning usage exceeds output usage")
	}
	return nil
}

type ProviderExtensionSource string
type ProviderExtensionKind string

const (
	ProviderExtensionSourceOpenAIChat        ProviderExtensionSource = "openai-chat"
	ProviderExtensionSourceOpenAIResponses   ProviderExtensionSource = "openai-responses"
	ProviderExtensionSourceAnthropicMessages ProviderExtensionSource = "anthropic-messages"

	ProviderExtensionReasoningContent             ProviderExtensionKind = "reasoning_content"
	ProviderExtensionReasoningSummary             ProviderExtensionKind = "reasoning_summary"
	ProviderExtensionReasoningEncryptedContent    ProviderExtensionKind = "reasoning_encrypted_content"
	ProviderExtensionAgentMessageEncryptedContent ProviderExtensionKind = "agent_message_encrypted_content"
	ProviderExtensionAgentMessageImage            ProviderExtensionKind = "agent_message_image"
	ProviderExtensionAgentMessageFile             ProviderExtensionKind = "agent_message_file"
	ProviderExtensionAgentMessageScreenshot       ProviderExtensionKind = "agent_message_screenshot"
	ProviderExtensionReasoningUsage               ProviderExtensionKind = "reasoning_usage"
	ProviderExtensionThinking                     ProviderExtensionKind = "thinking"
	ProviderExtensionRedactedThinking             ProviderExtensionKind = "redacted_thinking"
)

// ProviderExtension preserves provider-specific JSON values without
// reserializing them. Fragments are immutable snapshots of the original wire
// values and are not a promise that another dialect can represent them.
type ProviderExtension struct {
	source    ProviderExtensionSource
	kind      ProviderExtensionKind
	path      string
	fragments [][]byte
}

func NewProviderExtension(
	source ProviderExtensionSource,
	kind ProviderExtensionKind,
	path string,
	fragments [][]byte,
) (ProviderExtension, error) {
	extension := ProviderExtension{
		source: source,
		kind:   kind,
		path:   path,
	}
	extension.fragments = cloneByteSlices(fragments)
	if err := extension.Validate(); err != nil {
		return ProviderExtension{}, err
	}
	return extension, nil
}

func (extension ProviderExtension) Source() ProviderExtensionSource {
	return extension.source
}

func (extension ProviderExtension) Kind() ProviderExtensionKind {
	return extension.kind
}

func (extension ProviderExtension) Path() string {
	return extension.path
}

func (extension ProviderExtension) Fragments() [][]byte {
	return cloneByteSlices(extension.fragments)
}

func (extension ProviderExtension) Validate() error {
	if err := validateIdentifier(
		"provider extension source",
		string(extension.source),
		128,
	); err != nil {
		return err
	}
	switch extension.source {
	case ProviderExtensionSourceOpenAIChat:
		if extension.kind != ProviderExtensionReasoningContent &&
			extension.kind != ProviderExtensionReasoningUsage {
			return errors.New("provider extension kind is unsupported for OpenAI Chat")
		}
	case ProviderExtensionSourceOpenAIResponses:
		if extension.kind != ProviderExtensionReasoningContent &&
			extension.kind != ProviderExtensionReasoningSummary &&
			extension.kind != ProviderExtensionReasoningEncryptedContent &&
			extension.kind != ProviderExtensionAgentMessageEncryptedContent &&
			extension.kind != ProviderExtensionAgentMessageImage &&
			extension.kind != ProviderExtensionAgentMessageFile &&
			extension.kind != ProviderExtensionAgentMessageScreenshot {
			return errors.New("provider extension kind is unsupported for OpenAI Responses")
		}
	case ProviderExtensionSourceAnthropicMessages:
		if extension.kind != ProviderExtensionThinking &&
			extension.kind != ProviderExtensionRedactedThinking {
			return errors.New("provider extension kind is unsupported for Anthropic Messages")
		}
	default:
		return errors.New("provider extension source is unsupported")
	}
	if err := validateText("provider extension path", extension.path, 1024, false); err != nil {
		return err
	}
	if len(extension.fragments) == 0 ||
		len(extension.fragments) > MaxProviderExtensionFragments {
		return errors.New("provider extension fragment count is invalid")
	}
	totalBytes := 0
	for index, fragment := range extension.fragments {
		if len(fragment) == 0 || !json.Valid(fragment) {
			return fmt.Errorf("provider extension fragment %d is not valid JSON", index)
		}
		totalBytes += len(fragment)
		if totalBytes > MaxProviderExtensionBytes {
			return errors.New("provider extension exceeds the byte limit")
		}
	}
	return nil
}

func (extension ProviderExtension) Clone() ProviderExtension {
	cloned := extension
	cloned.fragments = cloneByteSlices(extension.fragments)
	return cloned
}

func (extension ProviderExtension) byteSize() int {
	total := 0
	for _, fragment := range extension.fragments {
		total += len(fragment)
	}
	return total
}

type Response struct {
	ID                 string
	CreatedAtUnix      int64
	RequestedModel     string
	EffectiveModel     string
	ReportedModel      string
	Blocks             []ContentBlock
	ProviderExtensions []ProviderExtension
	// ProtocolEvidence retains bounded, non-secret provider response
	// identifiers used for exact Agent-session association. It is separate
	// from Request.ProtocolEvidence because the two sides have different
	// authorities and must remain distinguishable in durable evidence.
	ProtocolEvidence []ProtocolEvidenceValue
	StopReason       StopReason
	StopSequence     string
	Usage            Usage
}

func (response Response) Validate() error {
	if err := validateIdentifier("response ID", response.ID, 512); err != nil {
		return err
	}
	if response.CreatedAtUnix < 0 {
		return errors.New("response creation time is negative")
	}
	if err := validateIdentifier("requested model", response.RequestedModel, MaxModelBytes); err != nil {
		return err
	}
	if err := validateIdentifier("effective model", response.EffectiveModel, MaxModelBytes); err != nil {
		return err
	}
	if err := validateIdentifier("reported model", response.ReportedModel, MaxModelBytes); err != nil {
		return err
	}
	if len(response.Blocks) == 0 || len(response.Blocks) > MaxContentBlocks {
		return errors.New("response content block count is invalid")
	}
	toolKeys := make(map[CallKey]struct{})
	resultKeys := make(map[CallKey]struct{})
	hasToolCall := false
	providerExtensionCount := len(response.ProviderExtensions)
	providerExtensionBytes := 0
	for index, block := range response.Blocks {
		if block.Kind != BlockText &&
			block.Kind != BlockRefusal &&
			block.Kind != BlockToolCall &&
			block.Kind != BlockToolResult &&
			block.Kind != BlockProviderExtension {
			return fmt.Errorf("response content block %d has an unsupported kind", index)
		}
		if err := block.Validate(); err != nil {
			return fmt.Errorf("response content block %d: %w", index, err)
		}
		if block.Kind == BlockToolCall {
			hasToolCall = true
			if _, duplicate := toolKeys[block.ToolCall.Key]; duplicate {
				return errors.New("response tool call identity is duplicated")
			}
			toolKeys[block.ToolCall.Key] = struct{}{}
		}
		if block.Kind == BlockToolResult {
			if _, duplicate := resultKeys[block.ToolResult.Key]; duplicate {
				return errors.New("response tool result identity is duplicated")
			}
			resultKeys[block.ToolResult.Key] = struct{}{}
		}
		if block.Kind == BlockProviderExtension {
			providerExtensionCount++
			providerExtensionBytes += block.ProviderExtension.byteSize()
		}
	}
	if providerExtensionCount > MaxProviderExtensions {
		return errors.New("provider extension count is invalid")
	}
	extensionKeys := make(map[string]struct{}, len(response.ProviderExtensions))
	extensionBytes := providerExtensionBytes
	for index, extension := range response.ProviderExtensions {
		if err := extension.Validate(); err != nil {
			return fmt.Errorf("provider extension %d: %w", index, err)
		}
		extensionBytes += extension.byteSize()
		if extensionBytes > MaxProviderExtensionBytes {
			return errors.New("provider extensions exceed the response byte limit")
		}
		key := string(extension.Source()) + "\x00" +
			string(extension.Kind()) + "\x00" +
			extension.Path()
		if _, duplicate := extensionKeys[key]; duplicate {
			return errors.New("provider extension identity is duplicated")
		}
		extensionKeys[key] = struct{}{}
	}
	if err := ValidateProtocolEvidence(response.ProtocolEvidence); err != nil {
		return fmt.Errorf("response protocol evidence: %w", err)
	}
	switch response.StopReason {
	case StopReasonEndTurn, StopReasonMaxTokens, StopReasonToolUse, StopReasonStopSequence:
	default:
		return errors.New("response stop reason is unsupported")
	}
	if (response.StopReason == StopReasonToolUse) != hasToolCall {
		return errors.New("response stop reason and tool calls are inconsistent")
	}
	if response.StopReason != StopReasonStopSequence && response.StopSequence != "" {
		return errors.New("response has an unexpected stop sequence")
	}
	return response.Usage.Validate()
}

func (response Response) Clone() Response {
	cloned := response
	cloned.Blocks = cloneBlocks(response.Blocks)
	cloned.ProviderExtensions = cloneProviderExtensions(response.ProviderExtensions)
	cloned.ProtocolEvidence = slices.Clone(response.ProtocolEvidence)
	return cloned
}

type NoticeCode string

const (
	NoticeCacheControlNotForwarded            NoticeCode = "cache_control_not_forwarded"
	NoticeMetadataNotForwarded                NoticeCode = "metadata_not_forwarded"
	NoticeTopKNotForwarded                    NoticeCode = "top_k_not_forwarded"
	NoticeServiceTierNotForwarded             NoticeCode = "service_tier_not_forwarded"
	NoticeThinkingModeNotForwarded            NoticeCode = "thinking_mode_not_forwarded"
	NoticeThinkingBudgetNotForwarded          NoticeCode = "thinking_budget_not_forwarded"
	NoticeThinkingDisplayNotForwarded         NoticeCode = "thinking_display_not_forwarded"
	NoticeReasoningEffortDowngraded           NoticeCode = "reasoning_effort_downgraded"
	NoticeTaskBudgetNotForwarded              NoticeCode = "task_budget_not_forwarded"
	NoticeContextManagementNotForwarded       NoticeCode = "context_management_not_forwarded"
	NoticeDiagnosticsNotForwarded             NoticeCode = "diagnostics_not_forwarded"
	NoticeContentOrderNormalized              NoticeCode = "content_order_normalized"
	NoticeLateUsageAccounting                 NoticeCode = "late_usage_accounting"
	NoticeReasoningContentNotForwarded        NoticeCode = "reasoning_content_not_forwarded"
	NoticeReasoningUsageNotForwarded          NoticeCode = "reasoning_usage_not_forwarded"
	NoticeCacheReadUsageAssumedUncached       NoticeCode = "cache_read_usage_assumed_uncached"
	NoticeReasoningUsageAssumedNonReasoning   NoticeCode = "reasoning_usage_assumed_non_reasoning"
	NoticeEagerToolInputStreamingNotForwarded NoticeCode = "eager_tool_input_streaming_not_forwarded"
	NoticeToolPlacementNormalized             NoticeCode = "tool_placement_normalized"
	NoticePromptCacheKeyNotForwarded          NoticeCode = "prompt_cache_key_not_forwarded"
	NoticeClientMetadataNotForwarded          NoticeCode = "client_metadata_not_forwarded"
	NoticePreviousResponseIDNotForwarded      NoticeCode = "previous_response_id_not_forwarded"
	NoticeInternalMessageMetadataNotForwarded NoticeCode = "internal_message_metadata_not_forwarded"
	NoticeReasoningContextNotForwarded        NoticeCode = "reasoning_context_not_forwarded"
	NoticeReasoningIncludeNotForwarded        NoticeCode = "reasoning_include_not_forwarded"
	NoticeTextVerbosityNotForwarded           NoticeCode = "text_verbosity_not_forwarded"
	NoticeCustomToolGrammarNotForwarded       NoticeCode = "custom_tool_grammar_not_forwarded"
	NoticeToolNamespaceEncoded                NoticeCode = "tool_namespace_encoded"
	NoticeReasoningSummaryNotForwarded        NoticeCode = "reasoning_summary_not_forwarded"
	NoticeReasoningExecutionNotForwarded      NoticeCode = "reasoning_execution_not_forwarded"
	NoticeToolItemIdentityNotForwarded        NoticeCode = "tool_item_identity_not_forwarded"
	NoticeToolCallerNotForwarded              NoticeCode = "tool_caller_not_forwarded"
	NoticeCitationsNotForwarded               NoticeCode = "citations_not_forwarded"
	NoticeMessageItemIdentityNotForwarded     NoticeCode = "message_item_identity_not_forwarded"
	NoticeMessagePhaseNotProjected            NoticeCode = "message_phase_not_projected"
	NoticeAgentItemIdentityNotForwarded       NoticeCode = "agent_item_identity_not_forwarded"
	NoticeHostedToolNotForwarded              NoticeCode = "hosted_tool_not_forwarded"
	NoticeCustomToolKindEncoded               NoticeCode = "custom_tool_kind_encoded"
	NoticeDeveloperRoleNormalized             NoticeCode = "developer_role_normalized"
	NoticeToolOutputContentNormalized         NoticeCode = "tool_output_content_normalized"
	// NoticeUnknownRequestFieldNotForwarded names a field the client sent that
	// this dialect does not model. Clients add fields faster than any
	// translator learns them; refusing the request would make the product
	// unusable, and dropping it silently would be a translation nobody can
	// audit. Design 07 §3.2 allows the loss only because it is declared here.
	NoticeUnknownRequestFieldNotForwarded NoticeCode = "unknown_request_field_not_forwarded"
)

type TranslationNotice struct {
	Code NoticeCode
	Path string
}

type TranslationReport struct {
	notices []TranslationNotice
}

func NewTranslationReport(notices ...TranslationNotice) TranslationReport {
	return TranslationReport{notices: slices.Clone(notices)}
}

func (report TranslationReport) Notices() []TranslationNotice {
	return slices.Clone(report.notices)
}

func (report TranslationReport) Empty() bool {
	return len(report.notices) == 0
}

func (report TranslationReport) Merge(other TranslationReport) TranslationReport {
	merged := make([]TranslationNotice, 0, len(report.notices)+len(other.notices))
	merged = append(merged, report.notices...)
	merged = append(merged, other.notices...)
	return NewTranslationReport(merged...)
}

func cloneBlocks(blocks []ContentBlock) []ContentBlock {
	cloned := make([]ContentBlock, len(blocks))
	for index, block := range blocks {
		cloned[index] = block.Clone()
	}
	return cloned
}

func cloneProviderExtensions(extensions []ProviderExtension) []ProviderExtension {
	cloned := make([]ProviderExtension, len(extensions))
	for index, extension := range extensions {
		cloned[index] = extension.Clone()
	}
	return cloned
}

func cloneByteSlices(values [][]byte) [][]byte {
	cloned := make([][]byte, len(values))
	for index, value := range values {
		cloned[index] = bytes.Clone(value)
	}
	return cloned
}

func validateIdentifier(label, value string, maxBytes int) error {
	if err := validateText(label, value, maxBytes, false); err != nil {
		return err
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s has surrounding whitespace", label)
	}
	for _, character := range value {
		if character == '\uFEFF' || unicode.IsControl(character) {
			return fmt.Errorf("%s contains a control character", label)
		}
	}
	return nil
}

func validateText(label, value string, maxBytes int, allowEmpty bool) error {
	if !allowEmpty && value == "" {
		return fmt.Errorf("%s is empty", label)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", label)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds the byte limit", label)
	}
	return nil
}

func rejectDuplicateJSONNames(value []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	if err := consumeUniqueJSONValue(decoder); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON object has trailing data")
	}
	return nil
}

func consumeUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		names := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, duplicate := names[name]; duplicate {
				return fmt.Errorf("JSON object key %q is duplicated", name)
			}
			names[name] = struct{}{}
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("JSON object is not terminated")
		}
	case '[':
		for decoder.More() {
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("JSON array is not terminated")
		}
	default:
		return errors.New("JSON delimiter is invalid")
	}
	return nil
}
