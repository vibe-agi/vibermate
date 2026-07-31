package anthropicchat

import (
	"errors"

	"github.com/vibe-agi/vibermate/internal/ssewire"
)

const (
	SourceAnthropicMessages = "anthropic-messages"
	SourceOpenAIChat        = "openai-chat"
	CallNamespace           = "anthropic-messages-openai-chat"
	CodecPairID             = "anthropic-messages-to-openai-chat"
	CodecRevision           = 5
	ProviderRelativePath    = "chat/completions"
)

type CompletionTokenField uint8

const (
	completionTokenFieldUnknown CompletionTokenField = iota
	CompletionTokenFieldMaxTokens
	CompletionTokenFieldMaxCompletionTokens
)

type ToolReasoningMode uint8

const (
	toolReasoningModeUnknown ToolReasoningMode = iota
	ToolReasoningModeOmit
	ToolReasoningModeNone
)

type DisabledReasoningMode uint8

const (
	disabledReasoningModeUnknown DisabledReasoningMode = iota
	DisabledReasoningModeOmit
	DisabledReasoningModeNone
)

type InstructionRoleMode uint8

const (
	instructionRoleModeUnknown InstructionRoleMode = iota
	InstructionRolePreserveDeveloper
	InstructionRoleNormalizeDeveloperToSystem
)

// ProviderRequestProfile freezes the provider-side Chat request shape for one
// codec revision. It contains no target host or model-specific dispatch.
type ProviderRequestProfile struct {
	completionTokenField CompletionTokenField
	toolReasoningMode    ToolReasoningMode
	disabledReasoning    DisabledReasoningMode
	instructionRoleMode  InstructionRoleMode
}

func OpenAIChatCompatibilityProfile() ProviderRequestProfile {
	return ProviderRequestProfile{
		completionTokenField: CompletionTokenFieldMaxTokens,
		toolReasoningMode:    ToolReasoningModeOmit,
		disabledReasoning:    DisabledReasoningModeOmit,
		instructionRoleMode:  InstructionRolePreserveDeveloper,
	}
}

// SystemInstructionCompatibilityProfile targets Chat implementations whose
// message-role capability predates the developer role. The semantic downgrade
// is explicit in TranslationReport rather than selected by provider identity.
func SystemInstructionCompatibilityProfile() ProviderRequestProfile {
	return ProviderRequestProfile{
		completionTokenField: CompletionTokenFieldMaxTokens,
		toolReasoningMode:    ToolReasoningModeOmit,
		disabledReasoning:    DisabledReasoningModeOmit,
		instructionRoleMode:  InstructionRoleNormalizeDeveloperToSystem,
	}
}

func (profile ProviderRequestProfile) validate() error {
	switch profile.completionTokenField {
	case CompletionTokenFieldMaxTokens,
		CompletionTokenFieldMaxCompletionTokens:
	default:
		return errors.New("Chat completion token field is invalid")
	}
	switch profile.toolReasoningMode {
	case ToolReasoningModeOmit, ToolReasoningModeNone:
	default:
		return errors.New("Chat tool reasoning mode is invalid")
	}
	switch profile.disabledReasoning {
	case DisabledReasoningModeOmit, DisabledReasoningModeNone:
	default:
		return errors.New("Chat disabled reasoning mode is invalid")
	}
	switch profile.instructionRoleMode {
	case InstructionRolePreserveDeveloper,
		InstructionRoleNormalizeDeveloperToSystem:
	default:
		return errors.New("Chat instruction role mode is invalid")
	}
	return nil
}

type Options struct {
	MaxRequestBytes      int
	MaxResponseBytes     int
	MaxToolArgumentBytes int
	MaxHeldSuffixBytes   int
	MaxToolCalls         int
	SSE                  ssewire.Options
	ProviderRequest      ProviderRequestProfile
}

func DefaultOptions() Options {
	return Options{
		MaxRequestBytes:      16 << 20,
		MaxResponseBytes:     16 << 20,
		MaxToolArgumentBytes: 4 << 20,
		MaxHeldSuffixBytes:   8 << 20,
		MaxToolCalls:         256,
		SSE:                  ssewire.DefaultOptions(),
		ProviderRequest:      OpenAIChatCompatibilityProfile(),
	}
}

type Codec struct {
	options         Options
	providerRequest ProviderRequestProfile
}

func New(options Options) (*Codec, error) {
	if options.MaxRequestBytes <= 0 ||
		options.MaxResponseBytes <= 0 ||
		options.MaxToolArgumentBytes <= 0 ||
		options.MaxHeldSuffixBytes <= 0 ||
		options.MaxToolCalls <= 0 {
		return nil, errors.New("Anthropic to Chat codec limits must be positive")
	}
	if options.MaxHeldSuffixBytes < options.MaxToolArgumentBytes {
		return nil, errors.New("held suffix limit is smaller than the tool argument limit")
	}
	if _, err := ssewire.NewDecoder(options.SSE); err != nil {
		return nil, err
	}
	if err := options.ProviderRequest.validate(); err != nil {
		return nil, err
	}
	return &Codec{
		options:         options,
		providerRequest: options.ProviderRequest,
	}, nil
}

func (codec *Codec) Revision() uint64 {
	return CodecRevision
}
