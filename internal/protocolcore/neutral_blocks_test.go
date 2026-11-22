package protocolcore

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// The block size is both the per-JSON-value structure charge and a factor
// of the response structure limit, so it grows only deliberately: 368 bytes
// plus one word each for the Reasoning, Media and ToolResult.Parts arms.
func TestContentBlockSizeIsPinned(t *testing.T) {
	t.Parallel()
	if size := unsafe.Sizeof(ContentBlock{}); size != 392 {
		t.Fatalf("unsafe.Sizeof(ContentBlock{}) = %d, want 392", size)
	}
	if jsonValueCellBytes != 392 {
		t.Fatalf("JSON value cell = %d, want the block size", jsonValueCellBytes)
	}
}

func testSeal() ReasoningSeal {
	return ReasoningSeal{Format: SealResponsesEncrypted, ItemID: "rs_1", Model: "gpt-test", Binding: "0123456789abcdef0123456789abcdef", Opaque: "ENC-1"}
}

func testAssistantRequest(blocks ...ContentBlock) Request {
	return Request{
		RequestedModel: "claude-test", EffectiveModel: "gpt-test",
		Messages: []Message{
			{Role: RoleUser, Blocks: []ContentBlock{{Kind: BlockText, Text: "question"}}},
			{Role: RoleAssistant, Blocks: blocks},
		},
	}
}

func TestReasoningBlockValidation(t *testing.T) {
	t.Parallel()
	valid := []Reasoning{
		{Text: "summary text", Seal: testSeal()},
		{Text: "raw", TextSource: ReasoningTextContent, Seal: testSeal()},
		{Text: "", Seal: ReasoningSeal{Format: SealResponsesEncrypted, ItemID: "rs_2"}},
		{Text: "no replay state"},
		{Text: "thinking", TextSource: ReasoningTextContent, Seal: ReasoningSeal{Format: SealAnthropicSignature, Model: "claude-test", Opaque: "sig"}},
		{Redacted: true, Seal: ReasoningSeal{Format: SealAnthropicRedacted, Opaque: "data"}},
	}
	for index, reasoning := range valid {
		block, err := NewReasoningBlock(reasoning)
		if err != nil {
			t.Fatalf("valid reasoning %d refused: %v", index, err)
		}
		if err := testAssistantRequest(block, ContentBlock{Kind: BlockText, Text: "answer"}).Validate(); err != nil {
			t.Fatalf("assistant reasoning %d refused: %v", index, err)
		}
	}
	invalid := map[string]Reasoning{
		"unknown text source":    {Text: "x", TextSource: "other"},
		"redacted with text":     {Text: "x", Redacted: true, Seal: ReasoningSeal{Format: SealAnthropicRedacted, Opaque: "d"}},
		"redacted without seal":  {Redacted: true},
		"redacted seal unmarked": {Seal: ReasoningSeal{Format: SealAnthropicRedacted, Opaque: "d"}},
		"responses without item": {Seal: ReasoningSeal{Format: SealResponsesEncrypted, Opaque: "x"}},
		"anthropic with item":    {Seal: ReasoningSeal{Format: SealAnthropicSignature, ItemID: "rs", Opaque: "x"}},
		"unknown seal format":    {Seal: ReasoningSeal{Format: "other", Opaque: "x"}},
		"fields without format":  {Seal: ReasoningSeal{Opaque: "x"}},
		"item ID too long":       {Seal: ReasoningSeal{Format: SealResponsesEncrypted, ItemID: strings.Repeat("r", MaxReasoningItemIDBytes+1)}},
		"model with whitespace":  {Seal: ReasoningSeal{Format: SealResponsesEncrypted, ItemID: "rs", Model: " gpt"}},
		"binding too long":       {Seal: ReasoningSeal{Format: SealResponsesEncrypted, ItemID: "rs", Binding: strings.Repeat("b", MaxReasoningBindingBytes+1)}},
		"text not UTF-8":         {Text: "\xff"},
	}
	for name, reasoning := range invalid {
		if _, err := NewReasoningBlock(reasoning); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestReasoningBlockIsAllowedOnlyInAssistantTurnsAndResponses(t *testing.T) {
	t.Parallel()
	block, err := NewReasoningBlock(Reasoning{Text: "r", Seal: testSeal()})
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []Role{RoleUser, RoleSystem, RoleDeveloper, RoleTool} {
		message := Message{Role: role, Blocks: []ContentBlock{block}}
		if err := message.Validate(); err == nil {
			t.Fatalf("reasoning accepted in a %s message", role)
		}
	}
	request := testAssistantRequest(ContentBlock{Kind: BlockText, Text: "a"})
	request.System = []ContentBlock{block}
	if err := request.Validate(); err == nil {
		t.Fatal("reasoning accepted as system content")
	}
	response := Response{ID: "resp", RequestedModel: "m", EffectiveModel: "m", ReportedModel: "m", StopReason: StopReasonEndTurn, Blocks: []ContentBlock{block, {Kind: BlockText, Text: "a"}}}
	if err := response.Validate(); err != nil {
		t.Fatalf("response reasoning refused: %v", err)
	}
	// An arm set on another kind, or a kind without its arm, is refused.
	if err := (ContentBlock{Kind: BlockText, Text: "a", Reasoning: block.Reasoning}).Validate(); err == nil {
		t.Fatal("reasoning arm accepted on a text block")
	}
	if err := (ContentBlock{Kind: BlockReasoning}).Validate(); err == nil {
		t.Fatal("reasoning block without its arm accepted")
	}
}

func TestReasoningSealsShareTheExtensionByteBudget(t *testing.T) {
	t.Parallel()
	big := testSeal()
	big.Opaque = strings.Repeat("e", MaxProviderExtensionBytes/2+1)
	block := ContentBlock{Kind: BlockReasoning, Reasoning: &Reasoning{Seal: big}}
	request := testAssistantRequest(block, ContentBlock{Kind: BlockText, Text: "a"})
	request.Messages = append(request.Messages,
		Message{Role: RoleUser, Blocks: []ContentBlock{{Kind: BlockText, Text: "next"}}},
		Message{Role: RoleAssistant, Blocks: []ContentBlock{block, {Kind: BlockText, Text: "b"}}})
	limits := DefaultResourceLimits()
	if err := ValidateRequestWithin(request, limits); err == nil || !strings.Contains(err.Error(), "provider extensions exceed") {
		t.Fatalf("two half-budget seals: %v", err)
	}
	response := Response{ID: "resp", RequestedModel: "m", EffectiveModel: "m", ReportedModel: "m", StopReason: StopReasonEndTurn, Blocks: []ContentBlock{block, block}}
	if err := response.Validate(); err == nil {
		t.Fatal("response seals above the extension budget accepted")
	}
}

func TestMediaValidation(t *testing.T) {
	t.Parallel()
	valid := []Media{
		{Kind: MediaImage, MediaType: "image/png", Data: "iVBORw0KGgo="},
		{Kind: MediaImage, MediaType: "image/webp", URL: "https://example.com/a.webp"},
		{Kind: MediaImage, MediaType: "image/jpeg", FileID: "file_1", FileSource: protocolspec.DialectAnthropicMessages},
		{Kind: MediaDocument, MediaType: "application/pdf", Data: "JVBERi0=", Title: "Report"},
		{Kind: MediaDocument, MediaType: "text/plain", Text: "plain body", Title: "notes.txt"},
		// Anthropic URL and file sources carry no media type.
		{Kind: MediaImage, URL: "https://example.com/a"},
		{Kind: MediaDocument, FileID: "file_2", FileSource: protocolspec.DialectAnthropicMessages},
	}
	for index, media := range valid {
		block, err := NewMediaBlock(media)
		if err != nil {
			t.Fatalf("valid media %d refused: %v", index, err)
		}
		request := Request{RequestedModel: "m", EffectiveModel: "m", Messages: []Message{{Role: RoleUser, Blocks: []ContentBlock{{Kind: BlockText, Text: "see"}, block}}}}
		if err := request.Validate(); err != nil {
			t.Fatalf("user media %d refused: %v", index, err)
		}
	}
	invalid := map[string]Media{
		"two sources":            {Kind: MediaImage, MediaType: "image/png", Data: "a", URL: "https://example.com/a.png"},
		"no source":              {Kind: MediaImage, MediaType: "image/png"},
		"inline without type":    {Kind: MediaImage, Data: "a"},
		"document data no type":  {Kind: MediaDocument, Data: "a"},
		"text without type":      {Kind: MediaDocument, Text: "body"},
		"image of PDF type":      {Kind: MediaImage, MediaType: "application/pdf", Data: "a"},
		"svg image":              {Kind: MediaImage, MediaType: "image/svg+xml", Data: "a"},
		"document of image type": {Kind: MediaDocument, MediaType: "image/png", Data: "a"},
		"text PDF":               {Kind: MediaDocument, MediaType: "application/pdf", Text: "body"},
		"image title":            {Kind: MediaImage, MediaType: "image/png", Data: "a", Title: "t"},
		"plain http":             {Kind: MediaImage, MediaType: "image/png", URL: "http://example.com/a.png"},
		"URL with credentials":   {Kind: MediaImage, MediaType: "image/png", URL: "https://user:pass@example.com/a.png"},
		"file without source":    {Kind: MediaImage, MediaType: "image/png", FileID: "file_1"},
		"source without file":    {Kind: MediaImage, MediaType: "image/png", Data: "a", FileSource: protocolspec.DialectOpenAIResponses},
		"unknown file source":    {Kind: MediaImage, MediaType: "image/png", FileID: "f", FileSource: "other"},
		"unknown kind":           {Kind: "audio", MediaType: "audio/wav", Data: "a"},
		"title too long":         {Kind: MediaDocument, MediaType: "application/pdf", Data: "a", Title: strings.Repeat("t", MaxMediaTitleBytes+1)},
	}
	for name, media := range invalid {
		if _, err := NewMediaBlock(media); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	block, err := NewMediaBlock(valid[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []Role{RoleAssistant, RoleSystem, RoleDeveloper, RoleTool} {
		if err := (Message{Role: role, Blocks: []ContentBlock{block}}).Validate(); err == nil {
			t.Fatalf("media accepted in a %s message", role)
		}
	}
	response := Response{ID: "resp", RequestedModel: "m", EffectiveModel: "m", ReportedModel: "m", StopReason: StopReasonEndTurn, Blocks: []ContentBlock{block}}
	if err := response.Validate(); err == nil {
		t.Fatal("media accepted in a response")
	}
}

func TestMediaShareOneRequestBudget(t *testing.T) {
	t.Parallel()
	half := strings.Repeat("A", MaxMediaBytes/2)
	image := ContentBlock{Kind: BlockMedia, Media: &Media{Kind: MediaImage, MediaType: "image/png", Data: half}}
	result := ContentBlock{Kind: BlockToolResult, ToolResult: ToolResult{
		Key:   CallKey{source: "anthropic-messages", wireID: "toolu_1"},
		Parts: &ToolResultParts{Items: []ToolResultPart{{Kind: ToolResultPartMedia, Media: &Media{Kind: MediaImage, MediaType: "image/png", Data: half + "AAAA"}}}},
	}}
	request := Request{RequestedModel: "m", EffectiveModel: "m", Messages: []Message{{Role: RoleUser, Blocks: []ContentBlock{image, result}}}}
	limits := ResourceLimits{Request: ResourceCost{PayloadBytes: 64 << 20, StructureBytes: 512 << 20}, Response: DefaultResourceLimits().Response}
	if err := ValidateRequestWithin(request, limits); err == nil || !strings.Contains(err.Error(), "media exceed") {
		t.Fatalf("media above the request budget: %v", err)
	}
	result.ToolResult.Parts.Items[0].Media.Data = "AAAA"
	request.Messages[0].Blocks[1] = result
	if err := ValidateRequestWithin(request, limits); err != nil {
		t.Fatalf("media within the budget refused: %v", err)
	}
}

func TestToolResultPartsKeepContentAsTheirText(t *testing.T) {
	t.Parallel()
	key := CallKey{source: "anthropic-messages", wireID: "toolu_1"}
	pdf := &Media{Kind: MediaDocument, MediaType: "application/pdf", Data: "JVBERi0="}
	parts := &ToolResultParts{Items: []ToolResultPart{
		{Kind: ToolResultPartText, Text: "first "},
		{Kind: ToolResultPartMedia, Media: pdf},
		{Kind: ToolResultPartToolReference, ToolName: "CronCreate"},
		{Kind: ToolResultPartText, Text: "second"},
	}}
	result := ToolResult{Key: key, Content: "first second", Parts: parts}
	if err := result.Validate(); err != nil {
		t.Fatalf("valid parts refused: %v", err)
	}
	if !result.HasNonTextParts() {
		t.Fatal("media and tool-reference parts not reported")
	}
	textOnly := ToolResult{Key: key, Content: "ab", Parts: &ToolResultParts{Items: []ToolResultPart{{Kind: ToolResultPartText, Text: "a"}, {Kind: ToolResultPartText, Text: "b"}}}}
	if err := textOnly.Validate(); err != nil || textOnly.HasNonTextParts() {
		t.Fatalf("text-only parts: %v %v", err, textOnly.HasNonTextParts())
	}
	if (ToolResult{Key: key, Content: "plain"}).HasNonTextParts() {
		t.Fatal("a result without parts reported non-text parts")
	}
	// The part count is bounded on its own: every part below is valid.
	textParts := func(count int) ToolResult {
		items := make([]ToolResultPart, count)
		for index := range items {
			items[index] = ToolResultPart{Kind: ToolResultPartText, Text: "x"}
		}
		return ToolResult{Key: key, Content: strings.Repeat("x", count), Parts: &ToolResultParts{Items: items}}
	}
	if err := textParts(MaxToolResultParts).Validate(); err != nil {
		t.Fatalf("%d parts refused: %v", MaxToolResultParts, err)
	}
	if err := textParts(MaxToolResultParts + 1).Validate(); err == nil {
		t.Fatalf("%d valid parts accepted", MaxToolResultParts+1)
	}
	for name, invalid := range map[string]ToolResult{
		"content differs":       {Key: key, Content: "first  second", Parts: parts},
		"content longer":        {Key: key, Content: "first secondX", Parts: parts},
		"empty parts":           {Key: key, Parts: &ToolResultParts{}},
		"too many parts":        {Key: key, Parts: &ToolResultParts{Items: make([]ToolResultPart, MaxToolResultParts+1)}},
		"media part with text":  {Key: key, Parts: &ToolResultParts{Items: []ToolResultPart{{Kind: ToolResultPartMedia, Media: pdf, Text: "x"}}}},
		"media part without":    {Key: key, Parts: &ToolResultParts{Items: []ToolResultPart{{Kind: ToolResultPartMedia}}}},
		"reference without":     {Key: key, Parts: &ToolResultParts{Items: []ToolResultPart{{Kind: ToolResultPartToolReference}}}},
		"reference with media":  {Key: key, Parts: &ToolResultParts{Items: []ToolResultPart{{Kind: ToolResultPartToolReference, ToolName: "T", Media: pdf}}}},
		"text part with name":   {Key: key, Content: "a", Parts: &ToolResultParts{Items: []ToolResultPart{{Kind: ToolResultPartText, Text: "a", ToolName: "T"}}}},
		"unknown part kind":     {Key: key, Parts: &ToolResultParts{Items: []ToolResultPart{{Kind: "audio"}}}},
		"invalid part media":    {Key: key, Parts: &ToolResultParts{Items: []ToolResultPart{{Kind: ToolResultPartMedia, Media: &Media{Kind: MediaImage}}}}},
		"reference name spaces": {Key: key, Parts: &ToolResultParts{Items: []ToolResultPart{{Kind: ToolResultPartToolReference, ToolName: " T"}}}},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if err := (ContentBlock{Kind: BlockText, Text: "a", ToolResult: ToolResult{Parts: parts}}).Validate(); err == nil {
		t.Fatal("tool-result parts accepted on a text block")
	}
}

func TestCloneDeepCopiesTheNewArms(t *testing.T) {
	t.Parallel()
	reasoning := &Reasoning{Text: "r", Seal: testSeal()}
	media := &Media{Kind: MediaImage, MediaType: "image/png", Data: "AAAA"}
	partMedia := &Media{Kind: MediaDocument, MediaType: "application/pdf", Data: "JVBE"}
	request := Request{
		RequestedModel: "m", EffectiveModel: "m",
		UnservedSafeguards: []SafeguardKind{SafeguardDangerousToolUse},
		Messages: []Message{
			{Role: RoleUser, Blocks: []ContentBlock{
				{Kind: BlockMedia, Media: media},
				{Kind: BlockToolResult, ToolResult: ToolResult{Key: CallKey{source: "s", wireID: "w"}, Parts: &ToolResultParts{Items: []ToolResultPart{{Kind: ToolResultPartMedia, Media: partMedia}}}}},
			}},
			{Role: RoleAssistant, Blocks: []ContentBlock{{Kind: BlockReasoning, Reasoning: reasoning}, {Kind: BlockText, Text: "a"}}},
		},
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	cloned := request.Clone()
	user, assistant := cloned.Messages[0], cloned.Messages[1]
	if user.Blocks[0].Media == media || user.Blocks[1].ToolResult.Parts == request.Messages[0].Blocks[1].ToolResult.Parts ||
		user.Blocks[1].ToolResult.Parts.Items[0].Media == partMedia || assistant.Blocks[0].Reasoning == reasoning {
		t.Fatal("Clone aliases a new pointer arm")
	}
	user.Blocks[0].Media.Data = "BBBB"
	user.Blocks[1].ToolResult.Parts.Items[0].Media.Data = "CCCC"
	user.Blocks[1].ToolResult.Parts.Items = append(user.Blocks[1].ToolResult.Parts.Items, ToolResultPart{Kind: ToolResultPartText})
	assistant.Blocks[0].Reasoning.Seal.Opaque = "changed"
	cloned.UnservedSafeguards[0] = "other"
	if media.Data != "AAAA" || partMedia.Data != "JVBE" || reasoning.Seal.Opaque != "ENC-1" ||
		len(request.Messages[0].Blocks[1].ToolResult.Parts.Items) != 1 || request.UnservedSafeguards[0] != SafeguardDangerousToolUse {
		t.Fatal("mutating a clone changed the original")
	}
	response := Response{Blocks: []ContentBlock{{Kind: BlockReasoning, Reasoning: reasoning}}}
	if response.Clone().Blocks[0].Reasoning == reasoning {
		t.Fatal("response Clone aliases reasoning")
	}
	if (Request{}).Clone().UnservedSafeguards != nil {
		t.Fatal("Clone invented unserved safeguards")
	}
}

func TestLoadedToolsFollowsToolReferences(t *testing.T) {
	t.Parallel()
	schema, err := NewJSONObject([]byte(`{"type":"object"}`), MaxToolJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	tool := func(name string, deferred bool) ToolDefinition {
		return ToolDefinition{Name: name, InputSchema: schema, DeferLoading: deferred}
	}
	request := Request{
		RequestedModel: "m", EffectiveModel: "m",
		Tools: []ToolDefinition{tool("Bash", false), tool("CronCreate", true), tool("DeferredToolPlaceholder", true), tool("Read", false), tool("WebFetch", true)},
		Messages: []Message{
			{Role: RoleUser, Blocks: []ContentBlock{{Kind: BlockText, Text: "q"}}},
			{Role: RoleAssistant, Blocks: []ContentBlock{{Kind: BlockToolCall, ToolCall: ToolCall{Key: CallKey{source: "s", wireID: "toolu_1"}, Name: "ToolSearch", Arguments: schema}}}},
			{Role: RoleUser, Blocks: []ContentBlock{{Kind: BlockToolResult, ToolResult: ToolResult{
				Key: CallKey{source: "s", wireID: "toolu_1"},
				Parts: &ToolResultParts{Items: []ToolResultPart{
					{Kind: ToolResultPartToolReference, ToolName: "WebFetch"},
					{Kind: ToolResultPartToolReference, ToolName: "CronCreate"},
					{Kind: ToolResultPartToolReference, ToolName: "NotDeclared"},
				}},
			}}}},
		},
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, loaded := range request.LoadedTools() {
		names = append(names, loaded.Name)
	}
	if strings.Join(names, ",") != "Bash,CronCreate,Read,WebFetch" {
		t.Fatalf("LoadedTools() = %v", names)
	}
	request.LoadedTools()[0].InputSchema.value[0] = 'X'
	if request.Tools[0].InputSchema.value[0] != '{' {
		t.Fatal("LoadedTools aliases the request's schemas")
	}
	plain := Request{Tools: []ToolDefinition{tool("A", false)}}
	if loaded := plain.LoadedTools(); len(loaded) != 1 || loaded[0].Name != "A" {
		t.Fatalf("LoadedTools() without deferred tools = %v", loaded)
	}
}

func TestUnservedSafeguardsAreAClosedVocabulary(t *testing.T) {
	t.Parallel()
	request := testAssistantRequest(ContentBlock{Kind: BlockText, Text: "a"})
	request.UnservedSafeguards = []SafeguardKind{SafeguardDangerousToolUse}
	if err := request.Validate(); err != nil {
		t.Fatalf("dangerous_tool_use refused: %v", err)
	}
	for name, kinds := range map[string][]SafeguardKind{
		"unknown":   {"prompt_injection"},
		"duplicate": {SafeguardDangerousToolUse, SafeguardDangerousToolUse},
		"empty":     {""},
	} {
		request.UnservedSafeguards = kinds
		if err := request.Validate(); err == nil {
			t.Fatalf("%s safeguards accepted", name)
		}
	}
}

func TestNewNoticeCodesAreDistinct(t *testing.T) {
	t.Parallel()
	codes := []NoticeCode{
		NoticeClientAttributionNotForwarded, NoticeSafeguardsNotServed, NoticeServerToolHistoryNotForwarded,
		NoticeStopSequencesNotForwarded, NoticeSamplingNotForwarded, NoticeMaxOutputTokensNotForwarded,
		NoticeToolErrorFlagEncoded, NoticeToolSchemaNormalized, NoticeToolReferenceEncoded,
		NoticeDeferredToolNotForwarded, NoticeReasoningNotReplayed, NoticeMediaNotForwarded,
		NoticeDocumentConverted, NoticeStructuredOutputNameSynthesized, NoticeMessagePhaseInferred,
		NoticePromptCacheKeyDerived, NoticeSystemRoleNormalized, NoticeHostedOutputNotForwarded,
		NoticeIncompleteToolCallNotForwarded, NoticeRefusalRenderedAsText, NoticeStopReasonApproximated,
		// Existing codes the new sides reuse.
		NoticeCacheControlNotForwarded, NoticeReasoningContentNotForwarded, NoticeCitationsNotForwarded,
		NoticeToolItemIdentityNotForwarded, NoticeUnknownRequestFieldNotForwarded,
	}
	seen := map[NoticeCode]bool{}
	for _, code := range codes {
		if code == "" || seen[code] || strings.ToLower(string(code)) != string(code) {
			t.Fatalf("notice code %q is empty, duplicated or not lower case", code)
		}
		seen[code] = true
	}
}
