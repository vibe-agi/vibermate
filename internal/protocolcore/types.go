package protocolcore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/protocolspec"
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
	// MaxMediaBytes bounds the media payload (base64 data and document text)
	// of one request, across messages and tool-result parts.
	MaxMediaBytes             = 16 << 20
	MaxMediaURLBytes          = 8 << 10
	MaxMediaTitleBytes        = 1 << 10
	MaxProviderFileIDBytes    = 512
	MaxToolResultParts        = 256
	MaxReasoningItemIDBytes   = 512
	MaxReasoningBindingBytes  = 128
	MaxUnservedSafeguardKinds = 8
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
	// BlockReasoning is positioned provider reasoning: Responses needs each
	// reasoning item replayed immediately before the item it produced, which
	// a position-free provider extension cannot express. It shares its name
	// with the stored reasoning kind it projects onto.
	BlockReasoning BlockKind = "reasoning"
	// BlockMedia is an image or document a client attached to a user message.
	BlockMedia BlockKind = "media"
)

type StopReason string

const (
	StopReasonEndTurn      StopReason = "end_turn"
	StopReasonMaxTokens    StopReason = "max_tokens"
	StopReasonToolUse      StopReason = "tool_use"
	StopReasonStopSequence StopReason = "stop_sequence"
	StopReasonRefusal      StopReason = "refusal"
	StopReasonPauseTurn    StopReason = "pause_turn"
	StopReasonContextLimit StopReason = "model_context_window_exceeded"
	// StopReasonIncomplete is a provider-declared incomplete terminal whose
	// reason this projection does not model; the reason stays in the native wire.
	StopReasonIncomplete StopReason = "incomplete"
)

func (reason StopReason) Validate() error {
	switch reason {
	case StopReasonEndTurn, StopReasonMaxTokens, StopReasonToolUse, StopReasonStopSequence,
		StopReasonRefusal, StopReasonPauseTurn, StopReasonContextLimit, StopReasonIncomplete:
		return nil
	default:
		return errors.New("response stop reason is unsupported")
	}
}

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

// ByteLen reports the exact owned byte length without copying private JSON.
func (document JSONDocument) ByteLen() int { return len(document.value) }

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
	case ToolKindProviderAction:
		if call.Arguments.IsZero() {
			return errors.New("provider action does not carry its native item")
		}
		if call.Input != "" || call.Namespace != "" {
			return errors.New("provider action contains portable call fields")
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
	// Parts, when set, is the ordered result content the source dialect sent.
	// Content then equals the concatenation of its text parts with no
	// separator, so every reader of Content keeps its meaning.
	Parts *ToolResultParts
}

type ToolResultPartKind string

const (
	ToolResultPartText ToolResultPartKind = "text"
	// ToolResultPartMedia carries an image or document in a tool result.
	ToolResultPartMedia ToolResultPartKind = "media"
	// ToolResultPartToolReference names a client tool definition the result
	// loads (Anthropic's tool_reference, the output of tool search).
	ToolResultPartToolReference ToolResultPartKind = "tool_reference"
)

type ToolResultPart struct {
	Kind     ToolResultPartKind
	Text     string
	Media    *Media
	ToolName string
}

// ToolResultParts is held by pointer, so a ToolResult without parts costs
// one word of every ContentBlock instead of a slice header.
type ToolResultParts struct {
	Items []ToolResultPart
}

func (part ToolResultPart) Validate() error {
	switch part.Kind {
	case ToolResultPartText:
		if part.Media != nil || part.ToolName != "" {
			return errors.New("text tool-result part contains other content")
		}
		return validateText("tool-result text part", part.Text, MaxTextBytes, true)
	case ToolResultPartMedia:
		if part.Media == nil || part.Text != "" || part.ToolName != "" {
			return errors.New("media tool-result part is invalid")
		}
		return part.Media.Validate()
	case ToolResultPartToolReference:
		if part.Media != nil || part.Text != "" {
			return errors.New("tool-reference part contains other content")
		}
		return validateIdentifier("tool-reference name", part.ToolName, MaxToolNameBytes)
	default:
		return errors.New("tool-result part kind is unsupported")
	}
}

func (part ToolResultPart) Clone() ToolResultPart {
	cloned := part
	if part.Media != nil {
		media := *part.Media
		cloned.Media = &media
	}
	return cloned
}

func (parts *ToolResultParts) clone() *ToolResultParts {
	if parts == nil {
		return nil
	}
	cloned := &ToolResultParts{Items: make([]ToolResultPart, len(parts.Items))}
	for index, part := range parts.Items {
		cloned.Items[index] = part.Clone()
	}
	return cloned
}

// HasNonTextParts reports a result that a text-only encoder cannot carry.
func (result ToolResult) HasNonTextParts() bool {
	if result.Parts == nil {
		return false
	}
	for _, part := range result.Parts.Items {
		if part.Kind != ToolResultPartText {
			return true
		}
	}
	return false
}

func (result ToolResult) mediaBytes() int {
	if result.Parts == nil {
		return 0
	}
	total := 0
	for _, part := range result.Parts.Items {
		if part.Media != nil {
			total += part.Media.payloadBytes()
		}
	}
	return total
}

func (result ToolResult) validateParts() error {
	if result.Parts == nil {
		return nil
	}
	if len(result.Parts.Items) == 0 || len(result.Parts.Items) > MaxToolResultParts {
		return errors.New("tool-result part count is invalid")
	}
	var text strings.Builder
	for index, part := range result.Parts.Items {
		if err := part.Validate(); err != nil {
			return fmt.Errorf("tool-result part %d: %w", index, err)
		}
		if part.Kind == ToolResultPartText {
			text.WriteString(part.Text)
		}
		if text.Len() > len(result.Content) {
			return errors.New("tool-result content differs from its text parts")
		}
	}
	if text.String() != result.Content {
		return errors.New("tool-result content differs from its text parts")
	}
	return nil
}

type ReasoningTextSource string

const (
	// ReasoningTextSummary (the default) is a provider's reasoning summary.
	ReasoningTextSummary ReasoningTextSource = "summary"
	// ReasoningTextContent is raw reasoning text, as some compatible services
	// and Anthropic thinking blocks provide it.
	ReasoningTextContent ReasoningTextSource = "content"
)

// Reasoning is one positioned reasoning block of an assistant turn.
type Reasoning struct {
	// Text is the visible reasoning; empty when display is omitted or the
	// block is redacted.
	Text string
	// TextSource records where Text came from, for faithful replay.
	TextSource ReasoningTextSource
	// Redacted is Anthropic redacted_thinking; Text must then be empty.
	Redacted bool
	// Seal is the provider's replay state; zero when the provider sent none.
	Seal ReasoningSeal
}

type ReasoningSealFormat string

const (
	SealResponsesEncrypted ReasoningSealFormat = "openai-responses.encrypted"
	SealAnthropicSignature ReasoningSealFormat = "anthropic.signature"
	SealAnthropicRedacted  ReasoningSealFormat = "anthropic.redacted"
)

// ReasoningSeal is opaque replay state that only the provider dialect that
// minted it may replay, and only to the same model on the same binding.
type ReasoningSeal struct {
	Format ReasoningSealFormat
	// ItemID is the Responses reasoning item id; required for
	// SealResponsesEncrypted.
	ItemID string
	// Model is the effective model that produced the reasoning.
	Model string
	// Binding is the provider context's reasoning binding when minted.
	Binding string
	// Opaque is encrypted_content, a signature or redacted data, verbatim.
	// It may be empty when the provider sent no payload; such a seal is never
	// replayed.
	Opaque string
}

func (seal ReasoningSeal) IsZero() bool {
	return seal == ReasoningSeal{}
}

// Dialect is the provider dialect that minted the seal, or "" for a zero
// seal.
func (seal ReasoningSeal) Dialect() protocolspec.Dialect {
	switch seal.Format {
	case SealResponsesEncrypted:
		return protocolspec.DialectOpenAIResponses
	case SealAnthropicSignature, SealAnthropicRedacted:
		return protocolspec.DialectAnthropicMessages
	default:
		return ""
	}
}

func (seal ReasoningSeal) Validate() error {
	if seal.IsZero() {
		return nil
	}
	switch seal.Format {
	case SealResponsesEncrypted:
		if err := validateIdentifier("reasoning item ID", seal.ItemID, MaxReasoningItemIDBytes); err != nil {
			return err
		}
	case SealAnthropicSignature, SealAnthropicRedacted:
		if seal.ItemID != "" {
			return errors.New("Anthropic reasoning seal has an item ID")
		}
	default:
		return errors.New("reasoning seal format is unsupported")
	}
	if seal.Model != "" {
		if err := validateIdentifier("reasoning seal model", seal.Model, MaxModelBytes); err != nil {
			return err
		}
	}
	if seal.Binding != "" {
		if err := validateIdentifier("reasoning seal binding", seal.Binding, MaxReasoningBindingBytes); err != nil {
			return err
		}
	}
	return validateText("reasoning seal payload", seal.Opaque, MaxProviderExtensionBytes, true)
}

func (reasoning Reasoning) Validate() error {
	switch reasoning.TextSource {
	case "", ReasoningTextSummary, ReasoningTextContent:
	default:
		return errors.New("reasoning text source is unsupported")
	}
	if err := validateText("reasoning text", reasoning.Text, MaxTextBytes, true); err != nil {
		return err
	}
	if reasoning.Redacted != (reasoning.Seal.Format == SealAnthropicRedacted) {
		return errors.New("redacted reasoning and its seal disagree")
	}
	if reasoning.Redacted && reasoning.Text != "" {
		return errors.New("redacted reasoning contains text")
	}
	return reasoning.Seal.Validate()
}

type MediaKind string

const (
	MediaImage    MediaKind = "image"
	MediaDocument MediaKind = "document"
)

// Media is an attachment in its source form. Data stays the base64 text the
// client sent; codecs never decode it.
type Media struct {
	Kind MediaKind
	// MediaType comes from a closed set per kind; it may be empty only for a
	// URL or file reference.
	MediaType string
	// Exactly one of Data, URL, FileID and Text is set.
	Data   string
	URL    string // https only
	FileID string // a provider file reference ...
	// FileSource is the dialect that minted FileID.
	FileSource protocolspec.Dialect
	// Text is the body of a plain-text document.
	Text  string
	Title string // optional document title
}

func (media Media) Validate() error {
	// A URL or file reference names its content elsewhere, and Anthropic
	// sends such sources without a media type; inline data always has one.
	referenced := media.URL != "" || media.FileID != ""
	switch media.Kind {
	case MediaImage:
		switch media.MediaType {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		case "":
			if !referenced {
				return errors.New("inline image has no media type")
			}
		default:
			return errors.New("image media type is unsupported")
		}
		if media.Text != "" || media.Title != "" {
			return errors.New("image carries document fields")
		}
	case MediaDocument:
		switch media.MediaType {
		case "application/pdf":
			if media.Text != "" {
				return errors.New("PDF document carries text")
			}
		case "text/plain":
		case "":
			if !referenced {
				return errors.New("inline document has no media type")
			}
		default:
			return errors.New("document media type is unsupported")
		}
		if err := validateText("document title", media.Title, MaxMediaTitleBytes, true); err != nil {
			return err
		}
	default:
		return errors.New("media kind is unsupported")
	}
	sources := 0
	for _, value := range []string{media.Data, media.URL, media.FileID, media.Text} {
		if value != "" {
			sources++
		}
	}
	if sources != 1 {
		return errors.New("media must have exactly one source")
	}
	if media.Text != "" && media.MediaType != "text/plain" {
		return errors.New("only a plain-text document carries text")
	}
	if (media.FileID == "") != (media.FileSource == "") {
		return errors.New("media file reference and its source disagree")
	}
	if media.FileSource != "" && !media.FileSource.Valid() {
		return errors.New("media file source is unsupported")
	}
	switch {
	case media.Data != "":
		if err := validateText("media data", media.Data, MaxMediaBytes, false); err != nil {
			return err
		}
	case media.URL != "":
		if len(media.URL) > MaxMediaURLBytes || !validHTTPSURL(media.URL) {
			return errors.New("media URL is invalid")
		}
	case media.FileID != "":
		if err := validateIdentifier("media file ID", media.FileID, MaxProviderFileIDBytes); err != nil {
			return err
		}
	default:
		if err := validateText("document text", media.Text, MaxTextBytes, false); err != nil {
			return err
		}
	}
	return nil
}

// payloadBytes is what the request-wide media budget charges.
func (media Media) payloadBytes() int {
	return len(media.Data) + len(media.Text)
}

func validHTTPSURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" &&
		parsed.User == nil && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n\t ")
}

type SafeguardKind string

const (
	// SafeguardDangerousToolUse is Claude Code auto mode's request for a
	// server-side classifier of dangerous tool use.
	SafeguardDangerousToolUse SafeguardKind = "dangerous_tool_use"
)

func validateUnservedSafeguards(kinds []SafeguardKind) error {
	if len(kinds) > MaxUnservedSafeguardKinds {
		return errors.New("unserved safeguard count is invalid")
	}
	for index, kind := range kinds {
		if kind != SafeguardDangerousToolUse {
			return errors.New("unserved safeguard kind is unsupported")
		}
		if slices.Contains(kinds[:index], kind) {
			return errors.New("unserved safeguard kind is duplicated")
		}
	}
	return nil
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
	if err := validateText("tool result content", result.Content, MaxTextBytes, true); err != nil {
		return err
	}
	return result.validateParts()
}

type ContentBlock struct {
	Kind              BlockKind
	Text              string
	Refusal           string
	ToolCall          ToolCall
	ToolResult        ToolResult
	ProviderExtension ProviderExtension
	// Reasoning and Media are pointers so each new arm costs one word of every
	// block; they are set only for their own kind.
	Reasoning *Reasoning
	Media     *Media
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

func NewReasoningBlock(reasoning Reasoning) (ContentBlock, error) {
	if err := reasoning.Validate(); err != nil {
		return ContentBlock{}, err
	}
	return ContentBlock{Kind: BlockReasoning, Reasoning: &reasoning}, nil
}

func NewMediaBlock(media Media) (ContentBlock, error) {
	if err := media.Validate(); err != nil {
		return ContentBlock{}, err
	}
	return ContentBlock{Kind: BlockMedia, Media: &media}, nil
}

func (block ContentBlock) Validate() error {
	if block.Agent != nil {
		if err := block.Agent.Validate(); err != nil {
			return err
		}
	}
	if (block.Reasoning != nil) != (block.Kind == BlockReasoning) {
		return errors.New("reasoning arm does not match the block kind")
	}
	if (block.Media != nil) != (block.Kind == BlockMedia) {
		return errors.New("media arm does not match the block kind")
	}
	if block.Kind != BlockToolResult && block.ToolResult.Parts != nil {
		return errors.New("tool-result parts outside a tool result")
	}
	switch block.Kind {
	case BlockReasoning:
		return block.Reasoning.Validate()
	case BlockMedia:
		return block.Media.Validate()
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
	cloned.ToolResult.Parts = block.ToolResult.Parts.clone()
	cloned.ProviderExtension = block.ProviderExtension.Clone()
	if block.Reasoning != nil {
		reasoning := *block.Reasoning
		cloned.Reasoning = &reasoning
	}
	if block.Media != nil {
		media := *block.Media
		cloned.Media = &media
	}
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
			if block.Kind != BlockText && block.Kind != BlockToolResult &&
				block.Kind != BlockMedia &&
				!(block.Kind == BlockProviderExtension &&
					block.ProviderExtension.source == ProviderExtensionSourceAnthropicMessages &&
					block.ProviderExtension.kind == ProviderExtensionOpaqueItem) &&
				!(block.Kind == BlockProviderExtension &&
					block.ProviderExtension.source ==
						ProviderExtensionSourceOpenAIResponses &&
					(block.ProviderExtension.kind ==
						ProviderExtensionInputImage ||
						block.ProviderExtension.kind ==
							ProviderExtensionInputAudio ||
						block.ProviderExtension.kind ==
							ProviderExtensionOpaqueItem)) {
				return errors.New("user message contains an unsupported block")
			}
		case RoleAssistant:
			if block.Kind != BlockText &&
				block.Kind != BlockRefusal &&
				block.Kind != BlockToolCall &&
				block.Kind != BlockProviderExtension &&
				block.Kind != BlockReasoning {
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
	// Native definitions are meaningful only in their original wire dialect.
	// They do not assert where a tool executes or grant any execution authority.
	ToolKindNative ToolKind = "native"
	// A provider action is an upstream output item that its dialect does not
	// model. The client may execute it, so it is always an unproven action:
	// the Environment tool policy decides whether it may pass. Its arguments
	// are the complete native item; it exists only as a call, never as a
	// definition, and no other dialect can encode it.
	ToolKindProviderAction ToolKind = "provider_action"
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
	NativeType          string
	InputSchema         JSONDocument
	CustomFormat        CustomToolFormat
	StrictKnown         bool
	Strict              bool
	EagerInputStreaming bool
	// DeferLoading is Anthropic's defer_loading: the tool is offered to the
	// model only once a tool_reference result in the history loads it.
	DeferLoading bool
}

func (definition ToolDefinition) Validate() error {
	if err := validateIdentifier("tool name", definition.Name, MaxToolNameBytes); err != nil {
		return err
	}
	if err := validateText("tool description", definition.Description, MaxTextBytes, true); err != nil {
		return err
	}
	if definition.EffectiveKind() != ToolKindNative && definition.NativeType != "" {
		return errors.New("portable tool contains a native type")
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
	case ToolKindNative:
		if err := validateIdentifier("native tool type", definition.NativeType, 256); err != nil {
			return err
		}
		if !definition.InputSchema.IsZero() || !definition.CustomFormat.IsZero() ||
			definition.StrictKnown || definition.Strict || definition.EagerInputStreaming {
			return errors.New("native tool contains portable tool configuration")
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
	// ThinkingDisplayUpdates is what Claude Code 2.1.3xx asks for in its
	// interactive and stream-json modes.
	ThinkingDisplayUpdates ThinkingDisplay = "updates"
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
	case "", ThinkingDisplaySummarized, ThinkingDisplayOmitted, ThinkingDisplayUpdates:
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
	// UnservedSafeguards lists server-side safeguards the client asked for
	// that this route does not run, so client encoders can report them
	// honestly as unsupported.
	UnservedSafeguards []SafeguardKind
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
	previous := ""
	for index, value := range values {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("protocol evidence %d: %w", index, err)
		}
		if index > 0 && value.Name <= previous {
			return errors.New("protocol evidence is not canonically ordered")
		}
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
	return ValidateRequestWithin(request, DefaultResourceLimits())
}

// Structural validation is independent of the selected finite policy.
func (request Request) validate() error {
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
	if len(request.Messages) == 0 {
		return errors.New("message count is invalid")
	}
	providerExtensionBytes := 0
	mediaBytes := 0
	for index, message := range request.Messages {
		if err := message.Validate(); err != nil {
			return fmt.Errorf("message %d: %w", index, err)
		}
		// History contains many responses. Bound extension count per message,
		// not over the whole history needed for the next turn or compaction.
		// The byte budget remains aggregate, and message/block counts are bounded.
		providerExtensionCount := 0
		for _, block := range message.Blocks {
			switch block.Kind {
			case BlockMedia:
				mediaBytes += block.Media.payloadBytes()
			case BlockToolResult:
				mediaBytes += block.ToolResult.mediaBytes()
			case BlockReasoning:
				// A reasoning seal is provider-locked opaque state, charged to
				// the same aggregate budget as provider extensions.
				providerExtensionBytes += len(block.Reasoning.Seal.Opaque)
			}
			if mediaBytes > MaxMediaBytes {
				return errors.New("media exceed the request limit")
			}
			if providerExtensionBytes > MaxProviderExtensionBytes {
				return errors.New("provider extensions exceed the request limit")
			}
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
	return validateUnservedSafeguards(request.UnservedSafeguards)
}

// LoadedTools is Anthropic's defer_loading behaviour as a pure function:
// every tool that is not deferred, plus each deferred tool that a
// tool_reference result anywhere in the history names, in request order.
// Tool namespaces are unaffected.
func (request Request) LoadedTools() []ToolDefinition {
	referenced := make(map[string]struct{})
	for _, message := range request.Messages {
		for _, block := range message.Blocks {
			if block.Kind != BlockToolResult || block.ToolResult.Parts == nil {
				continue
			}
			for _, part := range block.ToolResult.Parts.Items {
				if part.Kind == ToolResultPartToolReference {
					referenced[part.ToolName] = struct{}{}
				}
			}
		}
	}
	loaded := make([]ToolDefinition, 0, len(request.Tools))
	for _, tool := range request.Tools {
		if _, named := referenced[tool.Name]; !tool.DeferLoading || named {
			loaded = append(loaded, tool.Clone())
		}
	}
	return loaded
}

func (request Request) Clone() Request {
	cloned := request
	cloned.UnservedSafeguards = slices.Clone(request.UnservedSafeguards)
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
	return WithEffectiveModelWithin(request, model, DefaultResourceLimits())
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
	ProviderExtensionInputImage                   ProviderExtensionKind = "input_image"
	ProviderExtensionInputAudio                   ProviderExtensionKind = "input_audio"
	ProviderExtensionOpaqueItem                   ProviderExtensionKind = "opaque_item"
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

// WalkFragments lends each immutable fragment through Read only. A reader is
// valid only during its synchronous callback, which must not retain/use it
// concurrently. Errors stop traversal before the next callback.
func (extension ProviderExtension) WalkFragments(visit func(int, io.Reader) error) error {
	if visit == nil {
		return errors.New("fragment visitor is nil")
	}
	for _, fragment := range extension.fragments {
		reader := &providerFragmentReader{data: fragment}
		err := visit(len(fragment), reader)
		reader.data = nil
		reader.expired = true
		if err != nil {
			return err
		}
	}
	return nil
}

type providerFragmentReader struct {
	data    []byte
	offset  int
	expired bool
}

func (r *providerFragmentReader) Read(p []byte) (int, error) {
	if r.expired {
		return 0, errors.New("fragment reader expired")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.offset == len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
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
			extension.kind != ProviderExtensionInputImage &&
			extension.kind != ProviderExtensionInputAudio &&
			extension.kind != ProviderExtensionOpaqueItem &&
			extension.kind != ProviderExtensionAgentMessageEncryptedContent &&
			extension.kind != ProviderExtensionAgentMessageImage &&
			extension.kind != ProviderExtensionAgentMessageFile &&
			extension.kind != ProviderExtensionAgentMessageScreenshot {
			return errors.New("provider extension kind is unsupported for OpenAI Responses")
		}
	case ProviderExtensionSourceAnthropicMessages:
		if extension.kind != ProviderExtensionThinking &&
			extension.kind != ProviderExtensionRedactedThinking &&
			extension.kind != ProviderExtensionOpaqueItem {
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
	// A terminal can arrive before any output, e.g. an interrupted response.
	// Its status and usage remain evidence; do not synthesize a text block.
	if len(response.Blocks) > MaxContentBlocks {
		return errors.New("response content block count is invalid")
	}
	toolKeys := make(map[CallKey]struct{})
	resultKeys := make(map[CallKey]struct{})
	hasToolCall := false
	providerExtensionCount := len(response.ProviderExtensions)
	providerExtensionBytes := 0
	reasoningSealBytes := 0
	for index, block := range response.Blocks {
		if block.Kind != BlockText &&
			block.Kind != BlockRefusal &&
			block.Kind != BlockToolCall &&
			block.Kind != BlockToolResult &&
			block.Kind != BlockProviderExtension &&
			block.Kind != BlockReasoning {
			return fmt.Errorf("response content block %d has an unsupported kind", index)
		}
		if block.Kind == BlockReasoning && block.Reasoning != nil {
			reasoningSealBytes += len(block.Reasoning.Seal.Opaque)
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
	// Reasoning seals share the provider extensions' aggregate byte budget.
	if reasoningSealBytes > 0 && providerExtensionBytes+reasoningSealBytes > MaxProviderExtensionBytes {
		return errors.New("provider extensions exceed the response byte limit")
	}
	extensionKeys := make(map[string]struct{}, len(response.ProviderExtensions))
	extensionBytes := providerExtensionBytes + reasoningSealBytes
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
	if err := response.StopReason.Validate(); err != nil {
		return err
	}
	// A limit, refusal or pause can follow tool calls in the same response.
	// Their execution still requires the separate tool-decision boundary.
	if response.StopReason == StopReasonToolUse && !hasToolCall {
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
	// NoticeNativeContentNotProjected marks same-dialect content that the
	// original wire carries unchanged but the neutral projection does not model.
	NoticeNativeContentNotProjected NoticeCode = "native_content_not_projected"
	// NoticeUnknownRequestFieldNotForwarded names a field the client sent that
	// this dialect does not model. Clients add fields faster than any
	// translator learns them; refusing the request would make the product
	// unusable, and dropping it silently would be a translation nobody can
	// audit. Design 07 §3.2 allows the loss only because it is declared here.
	NoticeUnknownRequestFieldNotForwarded NoticeCode = "unknown_request_field_not_forwarded"

	// Codes of the translated Anthropic client side.
	NoticeClientAttributionNotForwarded NoticeCode = "client_attribution_not_forwarded"
	NoticeSafeguardsNotServed           NoticeCode = "safeguards_not_served"
	NoticeServerToolHistoryNotForwarded NoticeCode = "server_tool_history_not_forwarded"
	// Codes of the Responses provider side.
	NoticeStopSequencesNotForwarded       NoticeCode = "stop_sequences_not_forwarded"
	NoticeSamplingNotForwarded            NoticeCode = "sampling_not_forwarded"
	NoticeMaxOutputTokensNotForwarded     NoticeCode = "max_output_tokens_not_forwarded"
	NoticeToolErrorFlagEncoded            NoticeCode = "tool_error_flag_encoded"
	NoticeToolSchemaNormalized            NoticeCode = "tool_schema_normalized"
	NoticeToolReferenceEncoded            NoticeCode = "tool_reference_encoded"
	NoticeDeferredToolNotForwarded        NoticeCode = "deferred_tool_not_forwarded"
	NoticeReasoningNotReplayed            NoticeCode = "reasoning_not_replayed"
	NoticeMediaNotForwarded               NoticeCode = "media_not_forwarded"
	NoticeDocumentConverted               NoticeCode = "document_converted"
	NoticeStructuredOutputNameSynthesized NoticeCode = "structured_output_name_synthesized"
	NoticeMessagePhaseInferred            NoticeCode = "message_phase_inferred"
	NoticePromptCacheKeyDerived           NoticeCode = "prompt_cache_key_derived"
	NoticeSystemRoleNormalized            NoticeCode = "system_role_normalized"
	NoticeHostedOutputNotForwarded        NoticeCode = "hosted_output_not_forwarded"
	// Codes of the stream Assembler and the Anthropic client encoders.
	NoticeRefusalRenderedAsText  NoticeCode = "refusal_rendered_as_text"
	NoticeStopReasonApproximated NoticeCode = "stop_reason_approximated"
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
	return ValidateJSONNames(value)
}
