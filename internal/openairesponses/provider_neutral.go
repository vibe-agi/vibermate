package openairesponses

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

// The output normaliser turns Responses output items into neutral blocks for
// a client of another dialect (design 7): reasoning items become positioned
// reasoning blocks sealed for replay, message parts become text or refusal
// blocks, function calls become tool calls named as the client declared
// them, hosted results are dropped and declared, and every other item fails
// closed, since it cannot be shown to the client and might be an action. The
// stream driver, the aggregating stream and DecodeResponseFor all use it, so
// the three modes cannot drift. The native DecodeProviderResponse stays as
// it is for R->R.

// reasoningPartSeparator joins the summary parts (or raw text parts) of one
// reasoning item into one thinking text, as the stream driver sends them.
const reasoningPartSeparator = "\n\n"

// outputNormalizer holds what normalising needs from one attempt: the
// target facts a seal is bound to, the reverse tool-name map, and the side
// that classifies a failed response.
type outputNormalizer struct {
	side            *ProviderSide
	providerContext protocolpath.ProviderContext
	request         protocolcore.Request
	// toolNames maps each provider tool name the body declared back to the
	// client's name for it.
	toolNames map[string]string
}

func (side *ProviderSide) newOutputNormalizer(providerContext protocolpath.ProviderContext, request protocolcore.Request) outputNormalizer {
	loaded := request.LoadedTools()
	names := make(map[string]string, len(loaded))
	for _, tool := range loaded {
		if tool.EffectiveKind() == protocolcore.ToolKindFunction {
			names[providerToolName(protocolcore.ToolKindFunction, "", tool.Name)] = tool.Name
		}
	}
	return outputNormalizer{side: side, providerContext: providerContext, request: request, toolNames: names}
}

// normalizedItem is one output item as neutral blocks, with what it lost.
type normalizedItem struct {
	blocks  []protocolcore.ContentBlock
	notices []protocolcore.TranslationNotice
}

type translatedReasoningWire struct {
	ID               string                    `json:"id"`
	Type             string                    `json:"type"`
	Status           string                    `json:"status"`
	Summary          []translatedReasoningPart `json:"summary"`
	Content          []translatedReasoningPart `json:"content"`
	EncryptedContent *string                   `json:"encrypted_content"`
}

type translatedReasoningPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type translatedMessageWire struct {
	Type    string                  `json:"type"`
	Role    string                  `json:"role"`
	Status  string                  `json:"status"`
	Content []translatedMessagePart `json:"content"`
}

type translatedMessagePart struct {
	Type        string          `json:"type"`
	Text        *string         `json:"text"`
	Refusal     *string         `json:"refusal"`
	Annotations json.RawMessage `json:"annotations"`
}

// item normalises one output item at path.
func (normalizer outputNormalizer) item(raw json.RawMessage, path string) (normalizedItem, error) {
	kind, err := peekType(raw)
	if err != nil {
		return normalizedItem{}, invalidProvider(path, err)
	}
	switch kind {
	case "reasoning":
		reasoning, err := normalizer.reasoning(raw, path)
		if err != nil {
			return normalizedItem{}, err
		}
		block, err := protocolcore.NewReasoningBlock(reasoning)
		if err != nil {
			return normalizedItem{}, invalidProvider(path, err)
		}
		return normalizedItem{blocks: []protocolcore.ContentBlock{block}}, nil
	case "message":
		return normalizer.message(raw, path)
	case "function_call":
		call, err := normalizer.functionCall(raw, path)
		if err != nil {
			return normalizedItem{}, err
		}
		block, err := protocolcore.NewToolCallBlock(call)
		if err != nil {
			return normalizedItem{}, invalidProvider(path, err)
		}
		return normalizedItem{blocks: []protocolcore.ContentBlock{block}}, nil
	case "custom_tool_call":
		// The client declared only function tools.
		return normalizedItem{}, unsupportedOutputItem(path+".type", "custom tool calls cannot be delivered to this client")
	default:
		if isOpaqueResponsesProviderOutputItem(kind) {
			// A hosted result already ran on the provider: it is not an
			// action, and the client has nowhere to show it.
			return normalizedItem{notices: []protocolcore.TranslationNotice{{
				Code: protocolcore.NoticeHostedOutputNotForwarded,
				Path: path,
			}}}, nil
		}
		return normalizedItem{}, unsupportedOutputItem(path+".type", "Responses output item type cannot be delivered to this client")
	}
}

func unsupportedOutputItem(path, message string) error {
	return protocolcore.NewFailure(protocolcore.ReasonUnsupportedProviderData, path, errors.New(message))
}

// reasoning normalises a reasoning item. Its text is the summary, or the raw
// reasoning text when there is no summary (compatible services), parts
// joined by a blank line. The seal binds the item's replay state to the
// effective model and this attempt's binding; an item without
// encrypted_content gets a seal with an empty payload, which is never
// replayed, and an item without an id gets none.
func (normalizer outputNormalizer) reasoning(raw json.RawMessage, path string) (protocolcore.Reasoning, error) {
	var wire translatedReasoningWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return protocolcore.Reasoning{}, invalidProvider(path, err)
	}
	switch wire.Status {
	case "", "in_progress", "completed", "incomplete":
	default:
		return protocolcore.Reasoning{}, invalidProvider(path+".status", errors.New("Responses reasoning item status is invalid"))
	}
	summary, err := joinReasoningParts(wire.Summary, "summary_text", path+".summary")
	if err != nil {
		return protocolcore.Reasoning{}, err
	}
	content, err := joinReasoningParts(wire.Content, "reasoning_text", path+".content")
	if err != nil {
		return protocolcore.Reasoning{}, err
	}
	reasoning := protocolcore.Reasoning{Text: summary, TextSource: protocolcore.ReasoningTextSummary}
	if len(wire.Summary) == 0 && len(wire.Content) != 0 {
		reasoning = protocolcore.Reasoning{Text: content, TextSource: protocolcore.ReasoningTextContent}
	}
	encrypted := ""
	if wire.EncryptedContent != nil {
		encrypted = *wire.EncryptedContent
	}
	switch {
	case wire.ID != "":
		reasoning.Seal = protocolcore.ReasoningSeal{
			Format:  protocolcore.SealResponsesEncrypted,
			ItemID:  wire.ID,
			Model:   normalizer.request.EffectiveModel,
			Binding: normalizer.providerContext.ReasoningBinding,
			Opaque:  encrypted,
		}
	case encrypted != "":
		return protocolcore.Reasoning{}, invalidProvider(path+".id", errors.New("Responses reasoning state has no item id"))
	}
	if err := reasoning.Validate(); err != nil {
		return protocolcore.Reasoning{}, invalidProvider(path, err)
	}
	return reasoning, nil
}

func joinReasoningParts(parts []translatedReasoningPart, wantType, path string) (string, error) {
	if len(parts) == 0 {
		return "", nil
	}
	texts := make([]string, len(parts))
	for index, part := range parts {
		if part.Type != wantType || !utf8.ValidString(part.Text) {
			return "", invalidProvider(fmt.Sprintf("%s[%d]", path, index), errors.New("Responses reasoning text is invalid"))
		}
		texts[index] = part.Text
	}
	return strings.Join(texts, reasoningPartSeparator), nil
}

// message normalises an assistant message: each output_text part a text
// block, each refusal part a refusal block, in order. Annotations have no
// client form and are declared once per part.
func (normalizer outputNormalizer) message(raw json.RawMessage, path string) (normalizedItem, error) {
	var wire translatedMessageWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return normalizedItem{}, invalidProvider(path, err)
	}
	// A message cut off by an incomplete terminal is itself incomplete.
	if wire.Role != "assistant" ||
		(wire.Status != "" && wire.Status != "in_progress" && wire.Status != "completed" && wire.Status != "incomplete") {
		return normalizedItem{}, invalidProvider(path, errors.New("Responses message output is invalid"))
	}
	item := normalizedItem{blocks: make([]protocolcore.ContentBlock, 0, len(wire.Content))}
	for index, part := range wire.Content {
		partPath := fmt.Sprintf("%s.content[%d]", path, index)
		var block protocolcore.ContentBlock
		var err error
		switch part.Type {
		case "output_text":
			if part.Text == nil {
				return normalizedItem{}, invalidProvider(partPath+".text", errors.New("Responses output text is missing"))
			}
			block, err = protocolcore.NewTextBlock(*part.Text)
			if annotated(part.Annotations) {
				item.notices = append(item.notices, protocolcore.TranslationNotice{
					Code: protocolcore.NoticeCitationsNotForwarded,
					Path: partPath + ".annotations",
				})
			}
		case "refusal":
			if part.Refusal == nil {
				return normalizedItem{}, invalidProvider(partPath+".refusal", errors.New("Responses refusal is missing"))
			}
			block, err = protocolcore.NewRefusalBlock(*part.Refusal)
		default:
			return normalizedItem{}, unsupportedOutputItem(partPath+".type", "Responses output content type is unsupported")
		}
		if err != nil {
			return normalizedItem{}, invalidProvider(partPath, err)
		}
		item.blocks = append(item.blocks, block)
	}
	return item, nil
}

// annotated reports a non-empty annotations array.
func annotated(raw json.RawMessage) bool {
	var annotations []json.RawMessage
	return rawPresent(raw) && json.Unmarshal(raw, &annotations) == nil && len(annotations) != 0
}

// functionCall normalises a completed function call. Its arguments must be
// one JSON object within the tool JSON bound, and its name must be one the
// body declared: it is mapped back to the client's name, and anything else
// fails closed, since the client could not have offered that tool.
func (normalizer outputNormalizer) functionCall(raw json.RawMessage, path string) (protocolcore.ToolCall, error) {
	var wire providerFunctionCallWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return protocolcore.ToolCall{}, invalidProvider(path, err)
	}
	if wire.Status != "" && wire.Status != "completed" {
		return protocolcore.ToolCall{}, invalidProvider(path+".status", errors.New("function call is incomplete"))
	}
	name, declared := normalizer.toolNames[wire.Name]
	if wire.Namespace != "" || !declared {
		return protocolcore.ToolCall{}, invalidProvider(path+".name", errors.New("function call names a tool the request did not declare"))
	}
	arguments, err := protocolcore.NewJSONObject([]byte(wire.Arguments), protocolcore.MaxToolJSONBytes)
	if err != nil {
		return protocolcore.ToolCall{}, invalidProvider(path+".arguments", err)
	}
	call, err := newToolCall(protocolcore.ToolKindFunction, wire.ID, wire.CallID, "", name, arguments, "")
	if err != nil {
		return protocolcore.ToolCall{}, invalidProvider(path, err)
	}
	return call, nil
}

// terminal normalises a Responses terminal response object. When its output
// is empty, the items the stream completed stand in for it (ChatGPT can end
// with an empty output, F16). output is the item list that was used. A
// failed response becomes its classified failure.
func (normalizer outputNormalizer) terminal(
	body []byte,
	completedOutput []json.RawMessage,
) (response protocolcore.Response, output []json.RawMessage, notices []protocolcore.TranslationNotice, err error) {
	codec := normalizer.side.admission
	if len(body) == 0 || len(body) > codec.options.MaxResponseBytes {
		return protocolcore.Response{}, nil, nil, invalidProvider("$", errors.New("response body has an invalid size"))
	}
	if err := codec.ValidateResponseJSON(body); err != nil {
		return protocolcore.Response{}, nil, nil, invalidProvider("$", err)
	}
	if err := rejectDuplicateNames(body); err != nil {
		return protocolcore.Response{}, nil, nil, invalidProvider("$", err)
	}
	var wire providerResponseWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return protocolcore.Response{}, nil, nil, invalidProvider("$", err)
	}
	if len(wire.Output) == 0 && len(completedOutput) > 0 {
		wire.Output = completedOutput
	}
	if wire.Status == "failed" || rawPresent(wire.Error) {
		return protocolcore.Response{}, nil, nil, normalizer.side.streamFailure(normalizer.request, "$.error", wire.Error)
	}
	if wire.Output == nil {
		return protocolcore.Response{}, nil, nil, protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedProviderData,
			"$.output",
			errors.New("Responses terminal has no output array"),
		)
	}
	blocks := make([]protocolcore.ContentBlock, 0, len(wire.Output))
	for index, raw := range wire.Output {
		item, err := normalizer.item(raw, fmt.Sprintf("$.output[%d]", index))
		if err != nil {
			return protocolcore.Response{}, nil, nil, err
		}
		blocks = append(blocks, item.blocks...)
		notices = append(notices, item.notices...)
	}
	stopReason, err := translatedStopReason(wire, blocks)
	if err != nil {
		return protocolcore.Response{}, nil, nil, err
	}
	usage, usageNotices, err := translatedUsage(wire.Usage)
	if err != nil {
		return protocolcore.Response{}, nil, nil, err
	}
	notices = append(notices, usageNotices...)
	evidence, err := providerOutputProtocolEvidence(wire.Output)
	if err != nil {
		return protocolcore.Response{}, nil, nil, err
	}
	response = protocolcore.Response{
		ID:               wire.ID,
		CreatedAtUnix:    wire.CreatedAt,
		RequestedModel:   normalizer.request.RequestedModel,
		EffectiveModel:   normalizer.request.EffectiveModel,
		ReportedModel:    wire.Model,
		Blocks:           blocks,
		ProtocolEvidence: evidence,
		StopReason:       stopReason,
		Usage:            usage,
	}
	if err := codec.ValidateResponse(response); err != nil {
		return protocolcore.Response{}, nil, nil, invalidProvider("$", err)
	}
	return response, wire.Output, notices, nil
}

// translatedStopReason maps the terminal status (design 7.5): a completed
// response ends the turn or uses tools, an incomplete one stops at the
// output limit, is a refusal when the content filter ended it, and is
// otherwise incomplete, which the client encoder approximates.
func translatedStopReason(wire providerResponseWire, blocks []protocolcore.ContentBlock) (protocolcore.StopReason, error) {
	stopReason := protocolcore.StopReasonEndTurn
	for _, block := range blocks {
		if block.Kind == protocolcore.BlockToolCall {
			stopReason = protocolcore.StopReasonToolUse
			break
		}
	}
	switch wire.Status {
	case "completed":
		return stopReason, nil
	case "incomplete":
		if wire.IncompleteDetails == nil || wire.IncompleteDetails.Reason == "" {
			return "", invalidProvider("$.incomplete_details", errors.New("Responses incomplete terminal has no reason"))
		}
		switch wire.IncompleteDetails.Reason {
		case "max_output_tokens":
			return protocolcore.StopReasonMaxTokens, nil
		case "content_filter":
			return protocolcore.StopReasonRefusal, nil
		default:
			return protocolcore.StopReasonIncomplete, nil
		}
	default:
		return "", invalidProvider("$.status", errors.New("Responses terminal status is invalid"))
	}
}

// translatedUsage reads usage for a client of another dialect (design 7.6).
// OpenAI's input_tokens includes cached and cache-written tokens, while the
// neutral uncached input excludes both, so both are subtracted, floored at
// zero. Without cached_tokens the input is still known and is assumed
// uncached, which is declared. A terminal without usage gives unknown
// counts, which clients write as zero, rather than a failure, because
// compatible services may omit it. The shared decodeProviderUsage keeps its
// meaning for R->R.
func translatedUsage(raw json.RawMessage) (protocolcore.Usage, []protocolcore.TranslationNotice, error) {
	if !rawPresent(raw) {
		return protocolcore.Usage{}, nil, nil
	}
	var wire providerUsageWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return protocolcore.Usage{}, nil, invalidProvider("$.usage", err)
	}
	for _, value := range []struct {
		path  string
		count *int64
	}{
		{"$.usage.input_tokens", wire.InputTokens},
		{"$.usage.input_tokens_details.cached_tokens", wire.InputTokenDetails.CachedTokens},
		{"$.usage.input_tokens_details.cache_write_tokens", wire.InputTokenDetails.CacheWriteTokens},
		{"$.usage.output_tokens", wire.OutputTokens},
		{"$.usage.output_tokens_details.reasoning_tokens", wire.OutputTokenDetails.ReasoningTokens},
	} {
		if value.count != nil && *value.count < 0 {
			return protocolcore.Usage{}, nil, invalidProvider(value.path, errors.New("usage count is negative"))
		}
	}
	known := func(value int64) protocolcore.UsageValue {
		return protocolcore.UsageValue{Tokens: value, Known: true, Source: providerUsageSource}
	}
	var usage protocolcore.Usage
	var notices []protocolcore.TranslationNotice
	cached, written := int64(0), int64(0)
	if wire.InputTokenDetails.CachedTokens != nil {
		cached = *wire.InputTokenDetails.CachedTokens
		usage.CacheRead = known(cached)
	}
	if wire.InputTokenDetails.CacheWriteTokens != nil {
		written = *wire.InputTokenDetails.CacheWriteTokens
		usage.CacheWrite = known(written)
	}
	if wire.InputTokens != nil {
		usage.InputUncached = known(max(0, *wire.InputTokens-cached-written))
		if wire.InputTokenDetails.CachedTokens == nil {
			notices = append(notices, protocolcore.TranslationNotice{
				Code: protocolcore.NoticeCacheReadUsageAssumedUncached,
				Path: "$.usage.input_tokens_details.cached_tokens",
			})
		}
	}
	if wire.OutputTokens != nil {
		usage.Output = known(*wire.OutputTokens)
	}
	if wire.OutputTokenDetails.ReasoningTokens != nil {
		usage.Reasoning = known(*wire.OutputTokenDetails.ReasoningTokens)
	}
	if err := usage.Validate(); err != nil {
		return protocolcore.Usage{}, nil, invalidProvider("$.usage", err)
	}
	return usage, notices, nil
}

// withEmptyText gives a response with no block one empty text block, as the
// stream gives the client (design 4.4 rule 5), so a unary answer and a
// streamed one read alike.
func withEmptyText(response protocolcore.Response) protocolcore.Response {
	if len(response.Blocks) == 0 {
		response.Blocks = []protocolcore.ContentBlock{{Kind: protocolcore.BlockText}}
	}
	return response
}

// DecodeResponse decodes a unary Responses body for a zero provider context.
func (side *ProviderSide) DecodeResponse(
	request protocolcore.Request,
	body []byte,
) (protocolcore.Response, protocolcore.TranslationReport, error) {
	return side.DecodeResponseFor(protocolpath.ProviderContext{}, request, body)
}

// DecodeResponseFor decodes a unary Responses body (an application/json
// answer) through the output normaliser.
func (side *ProviderSide) DecodeResponseFor(
	providerContext protocolpath.ProviderContext,
	request protocolcore.Request,
	body []byte,
) (protocolcore.Response, protocolcore.TranslationReport, error) {
	if err := side.admission.ValidateRequest(request); err != nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	response, _, notices, err := side.newOutputNormalizer(providerContext, request).terminal(body, nil)
	if err != nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{}, err
	}
	return withEmptyText(response), protocolcore.NewTranslationReport(notices...), nil
}
