package openairesponses

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

const (
	// ProviderRelativePath is the Responses endpoint a provider side posts
	// to: the codec path the ChatGPT rewrite and the cyber finalizer
	// recognise.
	ProviderRelativePath = "v1/responses"
	// DefaultProviderMaxRequestBytes bounds an encoded provider body. It is
	// larger than a client body may be, because item wrappers and data URL
	// prefixes make a 16 MiB client body grow; above it the encoder fails
	// first, with its own path, rather than re-admission later.
	DefaultProviderMaxRequestBytes = 20 << 20
)

// InstructionPlacement chooses where the client's system prompt goes.
type InstructionPlacement uint8

const (
	instructionPlacementUnknown InstructionPlacement = iota
	// InstructionPlacementDeveloperMessage sends it as the leading developer
	// message, as Codex does.
	InstructionPlacementDeveloperMessage
	// InstructionPlacementInstructions sends it as top-level instructions.
	InstructionPlacementInstructions
)

// AssistantPhaseMode chooses whether replayed assistant text carries a phase
// (probe Q2).
type AssistantPhaseMode uint8

const (
	assistantPhaseUnknown AssistantPhaseMode = iota
	// AssistantPhaseInfer writes commentary before a tool call in the same
	// assistant message and final_answer otherwise.
	AssistantPhaseInfer
	// AssistantPhaseOmit writes no phase.
	AssistantPhaseOmit
)

// SamplingMode chooses whether temperature and top_p are sent.
type SamplingMode uint8

const (
	samplingUnknown SamplingMode = iota
	// SamplingDrop declares them; reasoning models reject sampling.
	SamplingDrop
	// SamplingForward sends them, for custom services that accept them.
	SamplingForward
)

// EncryptedReasoningInclusion chooses when include asks for
// reasoning.encrypted_content (probe Q6). Its fallback, asking only for
// reasoning models, needs a per-model fact this side does not have and lands
// with that probe's result.
type EncryptedReasoningInclusion uint8

const (
	encryptedReasoningUnknown EncryptedReasoningInclusion = iota
	// IncludeEncryptedReasoningUnlessDisabled asks whenever thinking is not
	// disabled, absent thinking included.
	IncludeEncryptedReasoningUnlessDisabled
)

// ReasoningReplayMode chooses whether sealed reasoning is replayed (probe Q4).
type ReasoningReplayMode uint8

const (
	reasoningReplayUnknown ReasoningReplayMode = iota
	// ReplayReasoningSealed replays a reasoning block whose seal passes every
	// replay check.
	ReplayReasoningSealed
	// ReplayReasoningOff replays none and declares each block.
	ReplayReasoningOff
)

// ReplaySummaryMode chooses what a replayed reasoning item's summary holds
// (probe Q4).
type ReplaySummaryMode uint8

const (
	replaySummaryUnknown ReplaySummaryMode = iota
	// ReplaySummaryRebuilt rebuilds it from the thinking text.
	ReplaySummaryRebuilt
	// ReplaySummaryEmpty sends an empty summary.
	ReplaySummaryEmpty
)

// ToolOutputDocumentMode chooses how a PDF a tool returned is sent (probe Q5).
type ToolOutputDocumentMode uint8

const (
	toolOutputDocumentUnknown ToolOutputDocumentMode = iota
	// ToolOutputDocumentsInputFile sends it as an input_file part.
	ToolOutputDocumentsInputFile
	// ToolOutputDocumentsTextNote replaces it with a text note and declares
	// media_not_forwarded.
	ToolOutputDocumentsTextNote
)

// SchemaNormalizer chooses how much of a tool's input schema is rewritten
// (probe Q1). Its fallback, keeping only the keywords Codex sends, is a
// recursive schema rewrite and lands with that probe's result.
type SchemaNormalizer uint8

const (
	schemaNormalizerUnknown SchemaNormalizer = iota
	// SchemaNormalizerTopLevel removes only a top-level $schema and $id;
	// strict:false makes every other keyword advisory.
	SchemaNormalizerTopLevel
)

// ProviderRequestProfile freezes the shape of a translated Responses request.
// Each field is one switch a live probe can flip (design 18.1); the zero
// value is invalid, so a profile is always chosen deliberately.
type ProviderRequestProfile struct {
	InstructionPlacement      InstructionPlacement
	AssistantPhase            AssistantPhaseMode
	Sampling                  SamplingMode
	IncludeEncryptedReasoning EncryptedReasoningInclusion
	ReplayReasoning           ReasoningReplayMode
	ReplaySummary             ReplaySummaryMode
	ToolOutputDocuments       ToolOutputDocumentMode
	SchemaNormalizer          SchemaNormalizer
	// ImageDetail is written on every input_image when set: auto, low, high
	// or original. Empty leaves the API default.
	ImageDetail string
}

// PublicResponsesProfile is the stage-1 profile for api.openai.com and
// Responses-compatible services.
func PublicResponsesProfile() ProviderRequestProfile {
	return ProviderRequestProfile{
		InstructionPlacement:      InstructionPlacementDeveloperMessage,
		AssistantPhase:            AssistantPhaseInfer,
		Sampling:                  SamplingDrop,
		IncludeEncryptedReasoning: IncludeEncryptedReasoningUnlessDisabled,
		ReplayReasoning:           ReplayReasoningSealed,
		ReplaySummary:             ReplaySummaryRebuilt,
		ToolOutputDocuments:       ToolOutputDocumentsInputFile,
		SchemaNormalizer:          SchemaNormalizerTopLevel,
	}
}

func (profile ProviderRequestProfile) validate() error {
	switch profile.InstructionPlacement {
	case InstructionPlacementDeveloperMessage, InstructionPlacementInstructions:
	default:
		return errors.New("Responses instruction placement is invalid")
	}
	switch profile.AssistantPhase {
	case AssistantPhaseInfer, AssistantPhaseOmit:
	default:
		return errors.New("Responses assistant phase mode is invalid")
	}
	switch profile.Sampling {
	case SamplingDrop, SamplingForward:
	default:
		return errors.New("Responses sampling mode is invalid")
	}
	if profile.IncludeEncryptedReasoning != IncludeEncryptedReasoningUnlessDisabled {
		return errors.New("Responses encrypted reasoning inclusion is invalid")
	}
	switch profile.ReplayReasoning {
	case ReplayReasoningSealed, ReplayReasoningOff:
	default:
		return errors.New("Responses reasoning replay mode is invalid")
	}
	switch profile.ReplaySummary {
	case ReplaySummaryRebuilt, ReplaySummaryEmpty:
	default:
		return errors.New("Responses replay summary mode is invalid")
	}
	switch profile.ToolOutputDocuments {
	case ToolOutputDocumentsInputFile, ToolOutputDocumentsTextNote:
	default:
		return errors.New("Responses tool output document mode is invalid")
	}
	if profile.SchemaNormalizer != SchemaNormalizerTopLevel {
		return errors.New("Responses schema normalizer is invalid")
	}
	switch profile.ImageDetail {
	case "", "auto", "low", "high", "original":
	default:
		return errors.New("Responses image detail is invalid")
	}
	return nil
}

// ProviderSideOptions configures a Responses provider side.
type ProviderSideOptions struct {
	// MaxRequestBytes bounds an encoded body and the post-script body that
	// re-admission reads.
	MaxRequestBytes int
	Resources       *protocolcore.ResourceLimits
	Profile         ProviderRequestProfile
}

func DefaultProviderSideOptions() ProviderSideOptions {
	limits := protocolcore.DefaultResourceLimits()
	return ProviderSideOptions{
		MaxRequestBytes: DefaultProviderMaxRequestBytes,
		Resources:       &limits,
		Profile:         PublicResponsesProfile(),
	}
}

// ProviderSide is the OpenAI Responses provider wire edge for a client of
// another dialect (design 4.3, 6-8). It encodes a neutral request into a
// Responses body in two passes, an item plan and one write of that plan, and
// re-admits the post-script body with the compatible decoder. It turns
// Responses output into neutral blocks for the client side, drives the
// client's stream encoder through the shared Assembler, classifies upstream
// rejections and estimates input tokens from the item plan. It never
// forwards source bytes, so it does not implement SourceRequestEncoder.
type ProviderSide struct {
	options   ProviderSideOptions
	resources protocolcore.ResourceLimits
	// admission is the compatible decoder bounded at MaxRequestBytes. It
	// also bounds and validates provider responses.
	admission *Codec
	// now and messageID are replaced only by tests.
	now       func() time.Time
	messageID func() string
}

var (
	_ protocolpath.ProviderSide             = (*ProviderSide)(nil)
	_ protocolpath.ProviderRequestAdmission = (*ProviderSide)(nil)
	_ protocolpath.TokenCounter             = (*ProviderSide)(nil)
)

func NewProviderSide(options ProviderSideOptions) (*ProviderSide, error) {
	if options.MaxRequestBytes <= 0 {
		return nil, errors.New("Responses provider request limit must be positive")
	}
	if err := options.Profile.validate(); err != nil {
		return nil, err
	}
	limits := protocolcore.DefaultResourceLimits()
	if options.Resources != nil {
		limits = *options.Resources
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	options.Resources = &limits
	admission, err := New(Options{
		MaxRequestBytes:  options.MaxRequestBytes,
		MaxResponseBytes: DefaultOptions().MaxResponseBytes,
		Resources:        &limits,
	})
	if err != nil {
		return nil, err
	}
	return &ProviderSide{
		options:   options,
		resources: limits,
		admission: admission,
		now:       time.Now,
		messageID: provisionalMessageID,
	}, nil
}

// provisionalMessageID names the client message of a stream that began
// without response.created: msg_vm_ and 24 random hex digits.
func provisionalMessageID() string {
	var random [12]byte
	_, _ = rand.Read(random[:])
	return "msg_vm_" + hex.EncodeToString(random[:])
}

func (*ProviderSide) Dialect() protocolspec.Dialect {
	return protocolspec.DialectOpenAIResponses
}

// EncodeRequest encodes for a zero provider context: a generic target with
// no reasoning binding and no rejected-seal memory.
func (side *ProviderSide) EncodeRequest(
	request protocolcore.Request,
) (protocolpath.ProviderRequest, protocolcore.TranslationReport, error) {
	return side.EncodeRequestFor(protocolpath.ProviderContext{}, request)
}

// EncodeRequestFor encodes request for the frozen target of one attempt. The
// body always streams (design 9): a Stream=false client is answered by
// aggregating the upstream stream.
func (side *ProviderSide) EncodeRequestFor(
	providerContext protocolpath.ProviderContext,
	request protocolcore.Request,
) (protocolpath.ProviderRequest, protocolcore.TranslationReport, error) {
	plan, err := side.planRequest(providerContext, request)
	if err != nil {
		return protocolpath.ProviderRequest{}, plan.report, err
	}
	body, err := plan.encode(side.options.MaxRequestBytes)
	if err != nil {
		return protocolpath.ProviderRequest{}, plan.report, err
	}
	headers := make(http.Header)
	headers.Set("Accept", "text/event-stream")
	encoded, err := protocolpath.NewProviderRequest(http.MethodPost, ProviderRelativePath, headers, body)
	if err != nil {
		return protocolpath.ProviderRequest{}, plan.report, err
	}
	return encoded, plan.report, nil
}
