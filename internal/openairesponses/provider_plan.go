package openairesponses

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

// The item plan is the first of the encoder's two passes (design 6): every
// item, tool and reasoning replay the body will carry, and every drop with its
// notice, decided over the neutral request and never over JSON. The second
// pass writes it once; the token count (S8) counts it, so the estimate
// counts exactly what would be sent. Plan values borrow the request's
// strings; nothing in a plan is modified after planning.

const (
	// maxOutputTokensCap is where a max_tokens stops being a limit worth
	// sending: Claude Code sends 32000 or more on every request, the Haiku
	// title call included (design R7). A smaller one is raised to the
	// minimum the API accepts.
	maxOutputTokensCap  = 32000
	minimumOutputTokens = 16
	planSizeSlackBytes  = 8 << 10
	// Responses tool outputs have no error flag; an error result says so in
	// its text unless Claude Code already marked it.
	toolErrorPrefix    = "Tool error: "
	toolUseErrorMarker = "<tool_use_error>"
	// A tool_reference load becomes one line of its output's text.
	loadedToolLinePrefix = "Loaded tool: "
	// An inline document's file name when it has no title.
	defaultDocumentName     = "document.pdf"
	defaultTextDocumentName = "document.txt"
	documentSuffix          = "</document>"
	promptCacheKeyPrefix    = "vm1-"
)

type planItemKind uint8

const (
	planItemMessage planItemKind = iota + 1
	planItemFunctionCall
	planItemFunctionCallOutput
	planItemReasoning
)

type planPartKind uint8

const (
	planPartInputText planPartKind = iota + 1
	planPartOutputText
	planPartRefusal
	planPartInputImage
	planPartInputFile
)

// planPart is one content part. A text part's value is prefix, text and
// suffix written as one string, so a wrapper never copies the text it wraps.
type planPart struct {
	kind   planPartKind
	prefix string
	text   string
	suffix string
	// media is the image or PDF of an input_image or input_file part.
	media *protocolcore.Media
	// filename names an inline input_file.
	filename string
}

// planItem is one input item.
type planItem struct {
	kind planItemKind
	// message is the neutral message it came from; -1 for the system prompt.
	message int
	// A message's role, phase and parts.
	role  string
	phase string
	parts []planPart
	// function_call and function_call_output
	callID    string
	name      string
	arguments []byte
	// outputList writes a function_call_output's parts as a content list;
	// otherwise its output is the single text part as a string.
	outputList bool
	// reasoning
	reasoningID string
	// summary is the one summary_text, or none when empty.
	summary string
	// content is the one reasoning_text when contentSource is set.
	content       string
	contentSource bool
	encrypted     string
}

type planFormat struct {
	name   string
	schema []byte
	strict bool
}

type requestPlan struct {
	model             string
	includeEncrypted  bool
	promptCacheKey    string
	maxOutputTokens   int
	temperature       *float64
	topP              *float64
	effort            string
	summary           string
	verbosity         string
	format            *planFormat
	toolChoice        string // auto, required or none; empty with namedTool
	namedTool         string
	parallelToolCalls bool
	instructions      []string
	hasInstructions   bool
	tools             []planTool
	items             []planItem
	imageDetail       string
	// current is the neutral message being planned; -1 for the system prompt.
	current int
	// sizeHint pre-sizes the one buffer the body is written into.
	sizeHint int
	report   protocolcore.TranslationReport
}

// planNotices collects the plan's notices within the request budget.
type planNotices struct {
	builder protocolcore.TranslationReportBuilder
	budget  *protocolcore.ResourceBudget
}

func (notices *planNotices) add(code protocolcore.NoticeCode, path string) error {
	if err := protocolcore.AppendReportWithin(&notices.builder, protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
		Code: code,
		Path: path,
	}), notices.budget); err != nil {
		return protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	return nil
}

// planRequest builds the item plan. Notice paths name the neutral request's
// positions ($.messages[i].blocks[j], $.tools[i]), as the Chat encoder's do:
// the client decoder may drop a whole message or system block, so wire
// indexes are not known here.
func (side *ProviderSide) planRequest(
	providerContext protocolpath.ProviderContext,
	request protocolcore.Request,
) (requestPlan, error) {
	if err := protocolcore.ValidateRequestWithin(request, side.resources); err != nil {
		return requestPlan{}, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	cost, err := protocolcore.MeasureRequest(request)
	if err != nil {
		return requestPlan{}, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	budget, err := protocolcore.NewResourceBudget(side.resources.Request)
	if err != nil {
		return requestPlan{}, err
	}
	if err := budget.Reserve(cost); err != nil {
		return requestPlan{}, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	profile := side.options.Profile
	notices := &planNotices{budget: budget}
	plan := requestPlan{
		model:       request.EffectiveModel,
		imageDetail: profile.ImageDetail,
		current:     -1,
		sizeHint:    int(min(cost.PayloadBytes, uint64(side.options.MaxRequestBytes))) + planSizeSlackBytes,
	}
	fail := func(err error) (requestPlan, error) {
		plan.report = notices.builder.Build()
		return plan, err
	}

	if err := plan.planEnvelope(request, profile, notices); err != nil {
		return fail(err)
	}
	tools, byClient, err := planTools(request, notices)
	if err != nil {
		return fail(err)
	}
	plan.tools = tools
	if err := plan.planToolChoice(request, byClient); err != nil {
		return fail(err)
	}
	if len(request.System) != 0 {
		switch profile.InstructionPlacement {
		case InstructionPlacementInstructions:
			// Instructions are one string, which re-admission bounds as one
			// text.
			plan.hasInstructions = true
			plan.instructions = make([]string, len(request.System))
			total := 0
			for index, block := range request.System {
				plan.instructions[index] = block.Text
				total += len(block.Text)
			}
			if total > protocolcore.MaxTextBytes {
				return fail(exceedsProviderLimit("$.system", "joined instruction bytes"))
			}
		default:
			parts := make([]planPart, len(request.System))
			for index, block := range request.System {
				parts[index] = planPart{kind: planPartInputText, text: block.Text}
			}
			plan.push(planItem{kind: planItemMessage, role: "developer", parts: parts})
		}
	}
	keep := reasoningRetention(request)
	for index, message := range request.Messages {
		plan.current = index
		if err := plan.planMessage(providerContext, request, profile, keep, index, message, notices); err != nil {
			return fail(err)
		}
	}
	plan.report = notices.builder.Build()
	return plan, nil
}

// push appends an item of the message being planned.
func (plan *requestPlan) push(item planItem) {
	item.message = plan.current
	plan.items = append(plan.items, item)
}

// planEnvelope decides the root members (design 6.1, 6.4, 6.7, 6.8).
func (plan *requestPlan) planEnvelope(
	request protocolcore.Request,
	profile ProviderRequestProfile,
	notices *planNotices,
) error {
	switch {
	case request.MaxOutputTokens >= maxOutputTokensCap:
		if err := notices.add(protocolcore.NoticeMaxOutputTokensNotForwarded, "$.max_tokens"); err != nil {
			return err
		}
	case request.MaxOutputTokens > 0:
		plan.maxOutputTokens = max(minimumOutputTokens, request.MaxOutputTokens)
	}
	if request.Temperature != nil || request.TopP != nil {
		if profile.Sampling == SamplingForward {
			plan.temperature, plan.topP = request.Temperature, request.TopP
		} else {
			if request.Temperature != nil {
				if err := notices.add(protocolcore.NoticeSamplingNotForwarded, "$.temperature"); err != nil {
					return err
				}
			}
			if request.TopP != nil {
				if err := notices.add(protocolcore.NoticeSamplingNotForwarded, "$.top_p"); err != nil {
					return err
				}
			}
		}
	}
	if len(request.StopSequences) != 0 {
		if err := notices.add(protocolcore.NoticeStopSequencesNotForwarded, "$.stop_sequences"); err != nil {
			return err
		}
	}
	if request.Diagnostics.Requested {
		if err := notices.add(protocolcore.NoticeDiagnosticsNotForwarded, "$.diagnostics"); err != nil {
			return err
		}
	}
	intent := request.Reasoning
	if intent.TaskBudget.Present {
		if err := notices.add(protocolcore.NoticeTaskBudgetNotForwarded, "$.output_config.task_budget"); err != nil {
			return err
		}
	}
	if intent.BudgetTokens != 0 {
		if err := notices.add(protocolcore.NoticeThinkingBudgetNotForwarded, "$.thinking.budget_tokens"); err != nil {
			return err
		}
	}
	if intent.Context != "" {
		if err := notices.add(protocolcore.NoticeReasoningContextNotForwarded, "$.reasoning.context"); err != nil {
			return err
		}
	}
	if intent.Execution != "" {
		if err := notices.add(protocolcore.NoticeReasoningExecutionNotForwarded, "$.reasoning.mode"); err != nil {
			return err
		}
	}
	plan.effort = string(intent.Effort)
	switch intent.Thinking {
	case protocolcore.ThinkingModeAdaptive, protocolcore.ThinkingModeEnabled:
		// Summaries are asked for whatever the display, so long reasoning
		// produces events; an omitted display hides their text (design R9).
		plan.summary = string(protocolcore.ReasoningSummaryAuto)
		if plan.effort == "" && intent.Thinking == protocolcore.ThinkingModeEnabled {
			plan.effort = string(effortForBudget(intent.BudgetTokens))
		}
	case protocolcore.ThinkingModeDisabled:
		// A reasoning model cannot switch reasoning off, and some reject
		// none, so the least effort stands in for it.
		if plan.effort == "" {
			plan.effort = string(protocolcore.ReasoningEffortLow)
		}
		if err := notices.add(protocolcore.NoticeThinkingModeNotForwarded, "$.thinking.type"); err != nil {
			return err
		}
	}
	// A summary the client chose itself (a Responses concept) wins.
	if intent.Summary != "" {
		plan.summary = string(intent.Summary)
	}
	plan.includeEncrypted = intent.Thinking != protocolcore.ThinkingModeDisabled
	plan.verbosity = string(request.OutputVerbosity)
	if request.Output.Kind == protocolcore.StructuredOutputJSONSchema {
		schema := request.Output.Schema.Bytes()
		plan.format = &planFormat{
			name:   structuredOutputName(schema),
			schema: schema,
			strict: strictSchemaCompatible(schema),
		}
		if err := notices.add(protocolcore.NoticeStructuredOutputNameSynthesized, "$.output_config.format"); err != nil {
			return err
		}
	}
	if key, ok := promptCacheKey(request.ProtocolEvidence); ok {
		plan.promptCacheKey = key
		if err := notices.add(protocolcore.NoticePromptCacheKeyDerived, "$"); err != nil {
			return err
		}
	}
	return nil
}

// effortForBudget approximates a thinking budget given without an effort.
func effortForBudget(budget int) protocolcore.ReasoningEffort {
	switch {
	case budget < 4096:
		return protocolcore.ReasoningEffortLow
	case budget < 16384:
		return protocolcore.ReasoningEffortMedium
	case budget < 32768:
		return protocolcore.ReasoningEffortHigh
	default:
		return protocolcore.ReasoningEffortXHigh
	}
}

// promptCacheKey derives a stable cache key per Claude Code session and
// subagent from the request's session evidence, so the raw identifiers never
// leave the machine. There is no key without a session.
func promptCacheKey(evidence []protocolcore.ProtocolEvidenceValue) (string, bool) {
	var session, agent string
	for _, value := range evidence {
		switch value.Name {
		case "claude.session_id":
			session = value.Value
		case "claude.agent_id":
			agent = value.Value
		}
	}
	if session == "" {
		return "", false
	}
	if agent == "" {
		agent = "main"
	}
	digest := sha256.Sum256([]byte("vibermate/prompt-cache\x00" + session + "\x00" + agent))
	return promptCacheKeyPrefix + hex.EncodeToString(digest[:16]), true
}

// planToolChoice maps the tool choice against the tools the body declares.
func (plan *requestPlan) planToolChoice(request protocolcore.Request, byClient map[string]string) error {
	choice := request.ToolChoice
	if len(plan.tools) == 0 {
		switch choice.Mode {
		case protocolcore.ToolChoiceRequired, protocolcore.ToolChoiceNamed:
			return protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				"$.tool_choice",
				errors.New("tool choice requires a tool the request has not loaded"),
			)
		}
		return nil
	}
	plan.parallelToolCalls = !choice.DisableParallel
	switch choice.Mode {
	case "", protocolcore.ToolChoiceAuto:
		plan.toolChoice = "auto"
	case protocolcore.ToolChoiceRequired:
		plan.toolChoice = "required"
	case protocolcore.ToolChoiceNone:
		plan.toolChoice = "none"
	case protocolcore.ToolChoiceNamed:
		name, ok := byClient[choice.Name]
		if !ok {
			return protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				"$.tool_choice",
				errors.New("tool choice requires a tool the request has not loaded"),
			)
		}
		plan.namedTool = name
	default:
		return protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedClientInput,
			"$.tool_choice",
			errors.New("tool choice is unsupported"),
		)
	}
	return nil
}

// reasoningRetention returns which assistant messages may replay reasoning
// under a clear_thinking edit that keeps the last N thinking turns: the last
// N assistant messages holding reasoning. nil keeps every message, as
// keep:"all" and no edit do.
func reasoningRetention(request protocolcore.Request) map[int]struct{} {
	turns := 0
	for _, edit := range request.Context.Edits {
		if edit.Kind == protocolcore.ContextEditClearThinking && edit.KeepThinkingTurns > 0 &&
			(turns == 0 || edit.KeepThinkingTurns < turns) {
			turns = edit.KeepThinkingTurns
		}
	}
	if turns == 0 {
		return nil
	}
	kept := make(map[int]struct{}, turns)
	for index := len(request.Messages) - 1; index >= 0 && len(kept) < turns; index-- {
		message := request.Messages[index]
		if message.Role != protocolcore.RoleAssistant {
			continue
		}
		for _, block := range message.Blocks {
			if block.Kind == protocolcore.BlockReasoning {
				kept[index] = struct{}{}
				break
			}
		}
	}
	return kept
}

func (plan *requestPlan) planMessage(
	providerContext protocolpath.ProviderContext,
	request protocolcore.Request,
	profile ProviderRequestProfile,
	keep map[int]struct{},
	index int,
	message protocolcore.Message,
	notices *planNotices,
) error {
	path := fmt.Sprintf("$.messages[%d]", index)
	if message.Agent != nil {
		return unsupportedPlanInput(path+".agent", "agent message provenance")
	}
	for blockIndex, block := range message.Blocks {
		if block.Agent != nil {
			return unsupportedPlanInput(fmt.Sprintf("%s.blocks[%d].agent", path, blockIndex), "agent block provenance")
		}
	}
	switch message.Role {
	case protocolcore.RoleSystem, protocolcore.RoleDeveloper:
		// Mid-conversation instructions stay where they were. Responses
		// services and the ChatGPT backend refuse a system role there.
		if message.Role == protocolcore.RoleSystem {
			if err := notices.add(protocolcore.NoticeSystemRoleNormalized, path+".role"); err != nil {
				return err
			}
		}
		parts := make([]planPart, len(message.Blocks))
		for blockIndex, block := range message.Blocks {
			parts[blockIndex] = planPart{kind: planPartInputText, text: block.Text}
		}
		plan.push(planItem{kind: planItemMessage, role: "developer", parts: parts})
		return nil
	case protocolcore.RoleUser, protocolcore.RoleTool:
		return plan.planUserMessage(profile, path, message, notices)
	case protocolcore.RoleAssistant:
		_, kept := keep[index]
		return plan.planAssistantMessage(providerContext, request, profile, keep == nil || kept, path, message, notices)
	default:
		return unsupportedPlanInput(path+".role", "message role")
	}
}

// planUserMessage keeps the block order exactly: consecutive text and media
// share one user message, and each tool result is an output item at its own
// position.
func (plan *requestPlan) planUserMessage(
	profile ProviderRequestProfile,
	path string,
	message protocolcore.Message,
	notices *planNotices,
) error {
	var parts []planPart
	attachments := 0
	flush := func() {
		if len(parts) != 0 {
			plan.push(planItem{kind: planItemMessage, role: "user", parts: parts})
			parts, attachments = nil, 0
		}
	}
	for blockIndex, block := range message.Blocks {
		blockPath := fmt.Sprintf("%s.blocks[%d]", path, blockIndex)
		switch block.Kind {
		case protocolcore.BlockText:
			parts = append(parts, planPart{kind: planPartInputText, text: block.Text})
		case protocolcore.BlockMedia:
			part, err := plan.mediaPart(block.Media, blockPath, false, profile, notices)
			if err != nil {
				return err
			}
			// Re-admission holds each user-message attachment as one
			// provider extension, of which a message may carry a bounded
			// number.
			if part.media != nil {
				attachments++
				if attachments > protocolcore.MaxProviderExtensions {
					return exceedsProviderLimit(blockPath, "user message attachments")
				}
			}
			parts = append(parts, part)
		case protocolcore.BlockToolResult:
			flush()
			item, err := plan.toolOutput(profile, blockPath, block.ToolResult, notices)
			if err != nil {
				return err
			}
			plan.push(item)
		default:
			return unsupportedPlanInput(blockPath, "user content block")
		}
	}
	flush()
	return nil
}

// exceedsProviderLimit is the encoder's own failure for a value that
// re-admission would refuse after it grew in translation.
func exceedsProviderLimit(path, what string) error {
	return protocolcore.NewFailure(
		protocolcore.ReasonInvalidClientRequest,
		path,
		errors.New(what+" exceed the provider's request limit"),
	)
}

// mediaPart renders an attachment. A provider file reference cannot travel:
// admission refuses it first with a capability class; the encoder fails
// closed on it too.
func (plan *requestPlan) mediaPart(
	media *protocolcore.Media,
	path string,
	toolOutput bool,
	profile ProviderRequestProfile,
	notices *planNotices,
) (planPart, error) {
	if media.FileID != "" {
		if err := notices.add(protocolcore.NoticeMediaNotForwarded, path); err != nil {
			return planPart{}, err
		}
		return planPart{}, protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedClientInput,
			path+".source",
			errors.New("provider file references cannot be represented by the configured provider dialect"),
		)
	}
	switch media.Kind {
	case protocolcore.MediaImage:
		return planPart{kind: planPartInputImage, media: media}, nil
	case protocolcore.MediaDocument:
		if media.Text != "" {
			if err := notices.add(protocolcore.NoticeDocumentConverted, path); err != nil {
				return planPart{}, err
			}
			prefix := "<document>"
			if media.Title != "" {
				prefix = `<document title="` + escapeDocumentTitle(media.Title) + `">`
			}
			part := planPart{kind: planPartInputText, prefix: prefix, text: media.Text, suffix: documentSuffix}
			if part.textBytes() > protocolcore.MaxTextBytes {
				return planPart{}, exceedsProviderLimit(path, "converted document bytes")
			}
			return part, nil
		}
		if toolOutput && profile.ToolOutputDocuments == ToolOutputDocumentsTextNote {
			if err := notices.add(protocolcore.NoticeMediaNotForwarded, path); err != nil {
				return planPart{}, err
			}
			return planPart{kind: planPartInputText, text: documentNote(media)}, nil
		}
		part := planPart{kind: planPartInputFile, media: media}
		if media.Data != "" {
			part.filename = documentName(media)
		}
		return part, nil
	default:
		return planPart{}, unsupportedPlanInput(path, "media kind")
	}
}

// documentName is an inline document's file name: its title, else a name
// for its media type.
func documentName(media *protocolcore.Media) string {
	switch {
	case media.Title != "":
		return media.Title
	case media.MediaType == "text/plain":
		return defaultTextDocumentName
	default:
		return defaultDocumentName
	}
}

// documentNote stands in for a document a tool returned when the profile
// does not send tool-output documents.
func documentNote(media *protocolcore.Media) string {
	return "[The document " + documentName(media) + " was returned here but could not be forwarded.]"
}

// textBytes is the length of the string a text part writes.
func (part planPart) textBytes() int {
	return len(part.prefix) + len(part.text) + len(part.suffix)
}

// escapeDocumentTitle keeps a title inside its attribute.
func escapeDocumentTitle(title string) string {
	return strings.NewReplacer(`&`, "&amp;", `"`, "&quot;", `<`, "&lt;", `>`, "&gt;").Replace(title)
}

// toolOutput renders one tool result as a function_call_output item. A result
// sent as a block list keeps its parts in order; tool_reference loads become
// one text line each, joined when adjacent, and the tools they load are
// declared in tools (design M6).
func (plan *requestPlan) toolOutput(
	profile ProviderRequestProfile,
	path string,
	result protocolcore.ToolResult,
	notices *planNotices,
) (planItem, error) {
	callID, rewritten := providerCallID(result.Key.WireID())
	if rewritten {
		if err := notices.add(protocolcore.NoticeToolItemIdentityNotForwarded, path+".id"); err != nil {
			return planItem{}, err
		}
	}
	item := planItem{kind: planItemFunctionCallOutput, callID: callID}
	flagError := result.IsError && !strings.Contains(result.Content, toolUseErrorMarker)
	if flagError {
		if err := notices.add(protocolcore.NoticeToolErrorFlagEncoded, path+".is_error"); err != nil {
			return planItem{}, err
		}
	}
	if result.Parts == nil {
		part := planPart{kind: planPartInputText, text: result.Content}
		if flagError {
			part.prefix = toolErrorPrefix
		}
		item.parts = []planPart{part}
		if part.textBytes() > protocolcore.MaxTextBytes {
			return planItem{}, exceedsProviderLimit(path, "tool output text bytes")
		}
		return item, nil
	}
	item.outputList = true
	item.parts = make([]planPart, 0, len(result.Parts.Items)+1)
	referenced := false
	for partIndex, part := range result.Parts.Items {
		partPath := fmt.Sprintf("%s.content[%d]", path, partIndex)
		switch part.Kind {
		case protocolcore.ToolResultPartText:
			item.parts = append(item.parts, planPart{kind: planPartInputText, text: part.Text})
		case protocolcore.ToolResultPartMedia:
			rendered, err := plan.mediaPart(part.Media, partPath, true, profile, notices)
			if err != nil {
				return planItem{}, err
			}
			item.parts = append(item.parts, rendered)
		case protocolcore.ToolResultPartToolReference:
			referenced = true
			line := loadedToolLinePrefix + part.ToolName
			last := len(item.parts) - 1
			if partIndex > 0 && result.Parts.Items[partIndex-1].Kind == protocolcore.ToolResultPartToolReference {
				item.parts[last].text += "\n" + line
				continue
			}
			item.parts = append(item.parts, planPart{kind: planPartInputText, text: line})
		default:
			return planItem{}, unsupportedPlanInput(partPath, "tool result part")
		}
	}
	if referenced {
		if err := notices.add(protocolcore.NoticeToolReferenceEncoded, path+".content"); err != nil {
			return planItem{}, err
		}
	}
	if flagError {
		if item.parts[0].kind == planPartInputText {
			item.parts[0].prefix = toolErrorPrefix + item.parts[0].prefix
		} else {
			item.parts = append([]planPart{{kind: planPartInputText, text: strings.TrimSpace(toolErrorPrefix)}}, item.parts...)
		}
	}
	// Re-admission joins an output's text parts with line breaks into one
	// text, which the neutral model bounds.
	textBytes, textParts := 0, 0
	for _, part := range item.parts {
		if part.kind == planPartInputText {
			textBytes += part.textBytes()
			textParts++
		}
	}
	if textParts > 1 {
		textBytes += textParts - 1
	}
	if textBytes > protocolcore.MaxTextBytes {
		return planItem{}, exceedsProviderLimit(path, "tool output text bytes")
	}
	return item, nil
}

// planAssistantMessage keeps the block order exactly. Text runs become one
// assistant message, each call a function_call, and each replayable reasoning
// block a reasoning item right before the item it produced.
func (plan *requestPlan) planAssistantMessage(
	providerContext protocolpath.ProviderContext,
	request protocolcore.Request,
	profile ProviderRequestProfile,
	retained bool,
	path string,
	message protocolcore.Message,
	notices *planNotices,
) error {
	// following[j] reports a block after j that becomes an item; a reasoning
	// item without one is refused upstream with no recovery.
	following := make([]bool, len(message.Blocks))
	toolAfter := make([]bool, len(message.Blocks))
	seenItem, seenTool := false, false
	for blockIndex := len(message.Blocks) - 1; blockIndex >= 0; blockIndex-- {
		following[blockIndex], toolAfter[blockIndex] = seenItem, seenTool
		switch message.Blocks[blockIndex].Kind {
		case protocolcore.BlockText, protocolcore.BlockRefusal:
			seenItem = true
		case protocolcore.BlockToolCall:
			seenItem, seenTool = true, true
		}
	}
	var parts []planPart
	partsStart, partsEnd := 0, 0
	flush := func() error {
		if len(parts) == 0 {
			return nil
		}
		item := planItem{kind: planItemMessage, role: "assistant", parts: parts}
		if profile.AssistantPhase == AssistantPhaseInfer {
			item.phase = "final_answer"
			if toolAfter[partsEnd] {
				item.phase = "commentary"
			}
			if err := notices.add(protocolcore.NoticeMessagePhaseInferred, fmt.Sprintf("%s.blocks[%d]", path, partsStart)); err != nil {
				return err
			}
		}
		plan.push(item)
		parts = nil
		return nil
	}
	for blockIndex, block := range message.Blocks {
		blockPath := fmt.Sprintf("%s.blocks[%d]", path, blockIndex)
		switch block.Kind {
		case protocolcore.BlockText, protocolcore.BlockRefusal:
			if len(parts) == 0 {
				partsStart = blockIndex
			}
			partsEnd = blockIndex
			if block.Kind == protocolcore.BlockText {
				parts = append(parts, planPart{kind: planPartOutputText, text: block.Text})
			} else {
				parts = append(parts, planPart{kind: planPartRefusal, text: block.Refusal})
			}
		case protocolcore.BlockToolCall:
			if err := flush(); err != nil {
				return err
			}
			item, err := toolCallItem(blockPath, block.ToolCall, notices)
			if err != nil {
				return err
			}
			plan.push(item)
		case protocolcore.BlockReasoning:
			if err := flush(); err != nil {
				return err
			}
			reasoning := block.Reasoning
			if !retained || !replayable(providerContext, request, profile, *reasoning) || !following[blockIndex] {
				if err := notices.add(protocolcore.NoticeReasoningNotReplayed, blockPath); err != nil {
					return err
				}
				continue
			}
			plan.push(reasoningItem(profile, *reasoning))
		default:
			return unsupportedPlanInput(blockPath, "assistant content block")
		}
	}
	return flush()
}

// replayable applies the seal checks of design 6.3: a Responses seal this
// path minted (the client decoder parses only those into this format), with a
// payload, for the effective model, under this attempt's binding, and not one
// the provider already refused. The caller also requires an item after it.
func replayable(
	providerContext protocolpath.ProviderContext,
	request protocolcore.Request,
	profile ProviderRequestProfile,
	reasoning protocolcore.Reasoning,
) bool {
	seal := reasoning.Seal
	return profile.ReplayReasoning == ReplayReasoningSealed &&
		seal.Format == protocolcore.SealResponsesEncrypted &&
		seal.ItemID != "" && seal.Opaque != "" &&
		seal.Model == request.EffectiveModel &&
		seal.Binding == providerContext.ReasoningBinding &&
		!providerContext.SealRejected(seal)
}

func reasoningItem(profile ProviderRequestProfile, reasoning protocolcore.Reasoning) planItem {
	item := planItem{
		kind:        planItemReasoning,
		reasoningID: reasoning.Seal.ItemID,
		encrypted:   reasoning.Seal.Opaque,
	}
	switch {
	case reasoning.TextSource == protocolcore.ReasoningTextContent:
		item.content, item.contentSource = reasoning.Text, true
	case profile.ReplaySummary == ReplaySummaryRebuilt:
		item.summary = reasoning.Text
	}
	return item
}

func toolCallItem(path string, call protocolcore.ToolCall, notices *planNotices) (planItem, error) {
	if call.EffectiveKind() != protocolcore.ToolKindFunction {
		return planItem{}, unsupportedPlanInput(path+".kind", "tool call kind")
	}
	callID, rewritten := providerCallID(call.Key.WireID())
	if rewritten {
		if err := notices.add(protocolcore.NoticeToolItemIdentityNotForwarded, path+".id"); err != nil {
			return planItem{}, err
		}
	}
	if !call.ItemKey.IsZero() {
		if err := notices.add(protocolcore.NoticeToolItemIdentityNotForwarded, path+".item_id"); err != nil {
			return planItem{}, err
		}
	}
	if call.Arguments.ByteLen() > protocolcore.MaxToolJSONBytes {
		return planItem{}, exceedsProviderLimit(path+".arguments", "tool call argument bytes")
	}
	return planItem{
		kind:      planItemFunctionCall,
		callID:    callID,
		name:      providerToolName(protocolcore.ToolKindFunction, call.Namespace, call.Name),
		arguments: call.Arguments.Bytes(),
	}, nil
}

func unsupportedPlanInput(path, what string) error {
	return protocolcore.NewFailure(
		protocolcore.ReasonUnsupportedClientInput,
		path,
		errors.New(what+" cannot be represented by the configured provider dialect"),
	)
}
