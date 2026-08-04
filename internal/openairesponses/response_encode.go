package openairesponses

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

type responseWire struct {
	ID                   string                 `json:"id"`
	Object               string                 `json:"object"`
	CreatedAt            int64                  `json:"created_at"`
	Status               string                 `json:"status"`
	Error                any                    `json:"error"`
	IncompleteDetails    *incompleteDetailsWire `json:"incomplete_details"`
	Instructions         any                    `json:"instructions"`
	Metadata             map[string]string      `json:"metadata"`
	Model                string                 `json:"model"`
	Output               []any                  `json:"output"`
	ParallelToolCalls    bool                   `json:"parallel_tool_calls"`
	Temperature          *float64               `json:"temperature"`
	ToolChoice           any                    `json:"tool_choice"`
	Tools                []any                  `json:"tools"`
	TopP                 *float64               `json:"top_p"`
	Background           bool                   `json:"background"`
	MaxOutputTokens      *int                   `json:"max_output_tokens"`
	MaxToolCalls         any                    `json:"max_tool_calls"`
	PreviousResponseID   any                    `json:"previous_response_id"`
	PromptCacheKey       any                    `json:"prompt_cache_key"`
	Reasoning            responseReasoningWire  `json:"reasoning"`
	SafetyIdentifier     any                    `json:"safety_identifier"`
	ServiceTier          any                    `json:"service_tier"`
	Text                 responseTextConfigWire `json:"text"`
	Truncation           string                 `json:"truncation"`
	Usage                any                    `json:"usage"`
	PromptCacheRetention any                    `json:"prompt_cache_retention"`
}

type incompleteDetailsWire struct {
	Reason string `json:"reason"`
}

type responseReasoningWire struct {
	Effort  any `json:"effort"`
	Summary any `json:"summary"`
}

type responseTextConfigWire struct {
	Format    responseTextFormatWire `json:"format"`
	Verbosity any                    `json:"verbosity"`
}

type responseTextFormatWire struct {
	Type string `json:"type"`
}

type responseUsageWire struct {
	InputTokens         int64                   `json:"input_tokens"`
	InputTokensDetails  responseInputUsageWire  `json:"input_tokens_details"`
	OutputTokens        int64                   `json:"output_tokens"`
	OutputTokensDetails responseOutputUsageWire `json:"output_tokens_details"`
	TotalTokens         int64                   `json:"total_tokens"`
}

type responseInputUsageWire struct {
	// Responses requires cached_tokens whenever usage is present. When a Chat
	// backend reports only aggregate prompt tokens, the edge conservatively
	// treats the unknown cache share as uncached and records that approximation
	// in TranslationReport.
	CachedTokens     int64  `json:"cached_tokens"`
	CacheWriteTokens *int64 `json:"cache_write_tokens,omitempty"`
}

type responseOutputUsageWire struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

type responseMessageItemWire struct {
	ID      string                       `json:"id"`
	Type    string                       `json:"type"`
	Status  string                       `json:"status"`
	Role    string                       `json:"role"`
	Content []responseMessageContentWire `json:"content"`
}

type responseMessageContentWire struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Refusal     string `json:"refusal,omitempty"`
	Annotations []any  `json:"annotations,omitempty"`
	Logprobs    []any  `json:"logprobs,omitempty"`
}

type responseFunctionCallItemWire struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	CallID    string `json:"call_id"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responseCustomToolCallItemWire struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Input     string `json:"input"`
}

type responseNamespaceToolWire struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Tools       []any  `json:"tools"`
}

func (codec *Codec) EncodeClientResponse(
	request protocolcore.Request,
	response protocolcore.Response,
) ([]byte, protocolcore.TranslationReport, error) {
	if codec == nil {
		return nil, protocolcore.TranslationReport{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$",
			errors.New("Responses codec is nil"),
		)
	}
	if err := request.Validate(); err != nil {
		return nil, protocolcore.TranslationReport{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$",
			err,
		)
	}
	if err := response.Validate(); err != nil {
		return nil, protocolcore.TranslationReport{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$",
			err,
		)
	}
	if response.RequestedModel != request.RequestedModel ||
		response.EffectiveModel != request.EffectiveModel {
		return nil, protocolcore.TranslationReport{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$",
			errors.New("response model mapping does not match the request"),
		)
	}
	if err := validateResponseCalls(request, response); err != nil {
		return nil, protocolcore.TranslationReport{}, protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedProviderData,
			"$.output",
			err,
		)
	}
	wire, report, err := buildResponseWire(request, response)
	if err != nil {
		return nil, report, err
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, report, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$",
			err,
		)
	}
	if len(encoded) > codec.options.MaxResponseBytes {
		return nil, report, protocolcore.NewFailure(
			protocolcore.ReasonStreamLimitExceeded,
			"$",
			errors.New("encoded Responses body exceeds the configured byte limit"),
		)
	}
	return encoded, report, nil
}

func buildResponseWire(
	request protocolcore.Request,
	response protocolcore.Response,
) (responseWire, protocolcore.TranslationReport, error) {
	output, err := encodeOutputItems(response)
	if err != nil {
		return responseWire{}, protocolcore.TranslationReport{}, err
	}
	usage, err := encodeResponseUsage(response.Usage)
	if err != nil {
		return responseWire{}, protocolcore.TranslationReport{}, err
	}
	status := "completed"
	var incomplete *incompleteDetailsWire
	if response.StopReason == protocolcore.StopReasonMaxTokens {
		status = "incomplete"
		incomplete = &incompleteDetailsWire{Reason: "max_output_tokens"}
	}
	wire := baseResponseWire(
		request,
		responseClientID(response),
		response.CreatedAtUnix,
		response.RequestedModel,
		status,
		output,
	)
	wire.IncompleteDetails = incomplete
	wire.Usage = usage
	report := responseExtensionReport(response)
	if !response.Usage.CacheRead.Known {
		report = report.Merge(protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeCacheReadUsageAssumedUncached,
				Path: "$.usage.input_tokens_details.cached_tokens",
			},
		))
	}
	if !response.Usage.Reasoning.Known {
		report = report.Merge(protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeReasoningUsageAssumedNonReasoning,
				Path: "$.usage.output_tokens_details.reasoning_tokens",
			},
		))
	}
	return wire, report, nil
}

func baseResponseWire(
	request protocolcore.Request,
	responseID string,
	createdAtUnix int64,
	model string,
	status string,
	output []any,
) responseWire {
	tools := encodeResponseTools(request)
	toolChoice := any("auto")
	switch request.ToolChoice.Mode {
	case protocolcore.ToolChoiceRequired:
		toolChoice = "required"
	case protocolcore.ToolChoiceNone:
		toolChoice = "none"
	}
	var maximumOutputTokens *int
	if request.MaxOutputTokens > 0 {
		value := request.MaxOutputTokens
		maximumOutputTokens = &value
	}
	var reasoningEffort any
	if request.Reasoning.Effort != "" {
		reasoningEffort = string(request.Reasoning.Effort)
	}
	var reasoningSummary any
	if request.Reasoning.Summary != "" {
		reasoningSummary = string(request.Reasoning.Summary)
	}
	var verbosity any
	if request.OutputVerbosity != "" {
		verbosity = string(request.OutputVerbosity)
	}
	return responseWire{
		ID:                 responseID,
		Object:             "response",
		CreatedAt:          createdAtUnix,
		Status:             status,
		Error:              nil,
		IncompleteDetails:  nil,
		Instructions:       nil,
		Metadata:           map[string]string{},
		Model:              model,
		Output:             output,
		ParallelToolCalls:  !request.ToolChoice.DisableParallel,
		Temperature:        cloneFloat64Pointer(request.Temperature),
		ToolChoice:         toolChoice,
		Tools:              tools,
		TopP:               cloneFloat64Pointer(request.TopP),
		Background:         false,
		MaxOutputTokens:    maximumOutputTokens,
		MaxToolCalls:       nil,
		PreviousResponseID: nil,
		PromptCacheKey:     nil,
		Reasoning: responseReasoningWire{
			Effort:  reasoningEffort,
			Summary: reasoningSummary,
		},
		SafetyIdentifier: nil,
		ServiceTier:      nil,
		Text: responseTextConfigWire{
			Format:    responseTextFormatWire{Type: "text"},
			Verbosity: verbosity,
		},
		Truncation:           "disabled",
		Usage:                nil,
		PromptCacheRetention: nil,
	}
}

func encodeResponseTools(request protocolcore.Request) []any {
	tools := make([]any, 0, len(request.Tools)+len(request.ToolNamespaces))
	for _, definition := range request.Tools {
		tools = append(tools, encodeResponseTool(definition))
	}
	for _, namespace := range request.ToolNamespaces {
		members := make([]any, len(namespace.Tools))
		for index, definition := range namespace.Tools {
			members[index] = encodeResponseTool(definition)
		}
		tools = append(tools, responseNamespaceToolWire{
			Type:        "namespace",
			Name:        namespace.Name,
			Description: namespace.Description,
			Tools:       members,
		})
	}
	return tools
}

func encodeResponseTool(definition protocolcore.ToolDefinition) any {
	switch definition.EffectiveKind() {
	case protocolcore.ToolKindFunction:
		var strict *bool
		if definition.StrictKnown {
			value := definition.Strict
			strict = &value
		}
		return functionToolWire{
			Type:        "function",
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  definition.InputSchema.Bytes(),
			Strict:      strict,
		}
	case protocolcore.ToolKindCustom:
		var format any
		switch definition.CustomFormat.Kind {
		case protocolcore.CustomToolFormatText:
			format = customFormatWire{Type: "text"}
		case protocolcore.CustomToolFormatGrammar:
			format = customFormatWire{
				Type:       "grammar",
				Syntax:     definition.CustomFormat.Syntax,
				Definition: definition.CustomFormat.Definition,
			}
		}
		return struct {
			Type        string `json:"type"`
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
			Format      any    `json:"format"`
		}{
			Type:        "custom",
			Name:        definition.Name,
			Description: definition.Description,
			Format:      format,
		}
	default:
		return nil
	}
}

func validateResponseCalls(
	request protocolcore.Request,
	response protocolcore.Response,
) error {
	defined := make(map[string]struct{})
	for _, definition := range request.Tools {
		key := responseToolKey(
			definition.EffectiveKind(),
			"",
			definition.Name,
		)
		defined[key] = struct{}{}
	}
	for _, namespace := range request.ToolNamespaces {
		for _, definition := range namespace.Tools {
			key := responseToolKey(
				definition.EffectiveKind(),
				namespace.Name,
				definition.Name,
			)
			defined[key] = struct{}{}
		}
	}
	for _, block := range response.Blocks {
		if block.Kind != protocolcore.BlockToolCall {
			continue
		}
		call := block.ToolCall
		key := responseToolKey(
			call.EffectiveKind(),
			call.Namespace,
			call.Name,
		)
		if _, exists := defined[key]; !exists {
			return errors.New(
				"response tool call is not present in the request catalog",
			)
		}
	}
	return nil
}

func responseToolKey(
	kind protocolcore.ToolKind,
	namespace string,
	name string,
) string {
	return string(kind) + "\x00" + namespace + "\x00" + name
}

func cloneFloat64Pointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func responseExtensionReport(
	response protocolcore.Response,
) protocolcore.TranslationReport {
	report := protocolcore.TranslationReport{}
	for _, extension := range response.ProviderExtensions {
		var code protocolcore.NoticeCode
		switch extension.Kind() {
		case protocolcore.ProviderExtensionReasoningContent:
			code = protocolcore.NoticeReasoningContentNotForwarded
		case protocolcore.ProviderExtensionReasoningUsage:
			code = protocolcore.NoticeReasoningUsageNotForwarded
		default:
			continue
		}
		report = report.Merge(protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: code,
				Path: extension.Path(),
			},
		))
	}
	return report
}

func encodeOutputItems(response protocolcore.Response) ([]any, error) {
	responseID := responseClientID(response)
	output := make([]any, 0, len(response.Blocks))
	for index := 0; index < len(response.Blocks); {
		block := response.Blocks[index]
		switch block.Kind {
		case protocolcore.BlockText, protocolcore.BlockRefusal:
			first := index
			content := make([]responseMessageContentWire, 0, 1)
			for index < len(response.Blocks) {
				current := response.Blocks[index]
				switch current.Kind {
				case protocolcore.BlockText:
					content = append(content, responseMessageContentWire{
						Type:        "output_text",
						Text:        current.Text,
						Annotations: []any{},
						Logprobs:    []any{},
					})
				case protocolcore.BlockRefusal:
					content = append(content, responseMessageContentWire{
						Type:    "refusal",
						Refusal: current.Refusal,
					})
				default:
					goto messageComplete
				}
				index++
			}
		messageComplete:
			output = append(output, responseMessageItemWire{
				ID:      stableClientID("msg", responseID, strconv.Itoa(first)),
				Type:    "message",
				Status:  "completed",
				Role:    "assistant",
				Content: content,
			})
		case protocolcore.BlockToolCall:
			call := block.ToolCall
			itemID, callID := toolClientIDs(responseID, index, call)
			switch call.EffectiveKind() {
			case protocolcore.ToolKindFunction:
				output = append(output, responseFunctionCallItemWire{
					ID:        itemID,
					Type:      "function_call",
					Status:    "completed",
					CallID:    callID,
					Namespace: call.Namespace,
					Name:      call.Name,
					Arguments: string(call.Arguments.Bytes()),
				})
			case protocolcore.ToolKindCustom:
				output = append(output, responseCustomToolCallItemWire{
					ID:        itemID,
					Type:      "custom_tool_call",
					CallID:    callID,
					Namespace: call.Namespace,
					Name:      call.Name,
					Input:     call.Input,
				})
			default:
				return nil, protocolcore.NewFailure(
					protocolcore.ReasonUnsupportedProviderData,
					fmt.Sprintf("$.output[%d]", index),
					errors.New("tool call kind is unsupported"),
				)
			}
			index++
		default:
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedProviderData,
				fmt.Sprintf("$.output[%d]", index),
				errors.New("response block kind is unsupported"),
			)
		}
	}
	return output, nil
}

func encodeResponseUsage(
	usage protocolcore.Usage,
) (responseUsageWire, error) {
	if err := usage.Validate(); err != nil {
		return responseUsageWire{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$.usage",
			err,
		)
	}
	// input_tokens and output_tokens are the only usage fields the Responses
	// wire always carries, so only those are required. A backend that cannot
	// report a cache split must not force this edge to invent one.
	for _, value := range []struct {
		path  string
		usage protocolcore.UsageValue
	}{
		{"$.usage.input_uncached", usage.InputUncached},
		{"$.usage.output", usage.Output},
	} {
		if !value.usage.Known {
			return responseUsageWire{}, protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedProviderData,
				value.path,
				errors.New("required Responses usage is unknown"),
			)
		}
	}
	// Only reported quadrants contribute to the total; an unknown quadrant is
	// absent rather than zero.
	inputTokens, overflow := addTokenCounts(
		usage.InputUncached.Tokens,
		knownTokens(usage.CacheWrite),
		knownTokens(usage.CacheRead),
	)
	if overflow {
		return responseUsageWire{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$.usage.input_tokens",
			errors.New("input usage overflows"),
		)
	}
	totalTokens, overflow := addTokenCounts(inputTokens, usage.Output.Tokens)
	if overflow {
		return responseUsageWire{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$.usage.total_tokens",
			errors.New("total usage overflows"),
		)
	}
	return responseUsageWire{
		InputTokens: inputTokens,
		InputTokensDetails: responseInputUsageWire{
			CachedTokens:     knownTokens(usage.CacheRead),
			CacheWriteTokens: reportedTokens(usage.CacheWrite),
		},
		OutputTokens: usage.Output.Tokens,
		OutputTokensDetails: responseOutputUsageWire{
			ReasoningTokens: knownTokens(usage.Reasoning),
		},
		TotalTokens: totalTokens,
	}, nil
}

func addTokenCounts(values ...int64) (int64, bool) {
	total := int64(0)
	for _, value := range values {
		if value > math.MaxInt64-total {
			return 0, true
		}
		total += value
	}
	return total, false
}

func responseClientID(response protocolcore.Response) string {
	return stableClientID(
		"resp",
		response.ID,
		strconv.FormatInt(response.CreatedAtUnix, 10),
		response.RequestedModel,
		response.EffectiveModel,
	)
}

func stableClientID(prefix string, values ...string) string {
	digest := sha256.New()
	for _, value := range values {
		_, _ = digest.Write([]byte(strconv.Itoa(len(value))))
		_, _ = digest.Write([]byte{':'})
		_, _ = digest.Write([]byte(value))
	}
	sum := digest.Sum(nil)
	return prefix + "_" + hex.EncodeToString(sum[:16])
}

// knownTokens contributes a quadrant to a total only when the backend reported
// it.
func knownTokens(value protocolcore.UsageValue) int64 {
	if !value.Known {
		return 0
	}
	return value.Tokens
}

// reportedTokens renders a detail field only when the backend reported it, so
// an omitted field means unknown rather than zero.
func reportedTokens(value protocolcore.UsageValue) *int64 {
	if !value.Known {
		return nil
	}
	tokens := value.Tokens
	return &tokens
}
