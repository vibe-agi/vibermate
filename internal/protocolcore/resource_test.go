package protocolcore

import (
	"math"
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

// Catches the existing business-count refusal using the original public method.
func TestResourceCompleteHistory(t *testing.T) {
	request := Request{RequestedModel: "m", EffectiveModel: "m"}
	for index := 0; index < 4111; index++ {
		request.Messages = append(request.Messages, Message{Role: RoleUser, Blocks: []ContentBlock{{Kind: BlockText, Text: "history"}}})
	}
	request.Messages[4110].Blocks[0].Text = "distinct tail"
	limits := ResourceLimits{Request: ResourceCost{10 << 20, 32 << 20}, Response: ResourceCost{16 << 20, 32 << 20}}
	if err := ValidateRequestWithin(request, limits); err != nil {
		t.Fatalf("complete history refused: %v", err)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("default complete history refused: %v", err)
	}
}

func TestResourceAllRetainedFieldsAndResponseOccurrences(t *testing.T) {
	request := Request{RequestedModel: "m", EffectiveModel: "m", Reasoning: ReasoningIntent{Thinking: "a", Display: "bb", Context: "ccc", Effort: "dddd", Summary: "eeeee", Execution: "ffffff"}, ToolChoice: ToolChoice{Mode: "g", Name: "hh"}, Diagnostics: DiagnosticsIntent{PreviousMessageID: "iii"}, OutputVerbosity: "jjjj", Context: ContextManagementIntent{Edits: []ContextEdit{{Kind: "kk"}}}, StopSequences: []string{"lll"}, ProtocolEvidence: []ProtocolEvidenceValue{{Name: "nn", Value: "vvvv"}}}
	cost, err := MeasureRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	// 2 models + (1+2+3+4+5+6) reasoning + 3 choice + 3 diagnostics +
	// 4 verbosity + 2 context + 3 stop + 6 evidence = 44 payload bytes.
	if cost.PayloadBytes != 44 {
		t.Fatalf("request fields omitted: %+v", cost)
	}
	request.Messages = []Message{{Role: "r", Agent: &AgentMessageContext{AgentName: "a", Author: "b", Recipient: "c"}, Blocks: []ContentBlock{{Kind: BlockText, Agent: &AgentMessageContext{AgentName: "d", Author: "e", Recipient: "f"}, ToolCall: ToolCall{Kind: "k", Key: CallKey{source: "s", wireID: "w"}, ItemKey: CallKey{source: "i", wireID: "j"}, Namespace: "n", Name: "t", Input: "u"}, ToolResult: ToolResult{Key: CallKey{source: "q", wireID: "v"}, Namespace: "z", Name: "y", Content: "x"}}}}}
	cost, err = MeasureRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	// Adds role1, kind4, two agents6, all call fields8, result fields5.
	if cost.PayloadBytes != 68 {
		t.Fatalf("inactive identity/agent fields omitted: %+v", cost)
	}
	response := Response{ID: "r", RequestedModel: "m", EffectiveModel: "m", ReportedModel: "m", StopReason: "end_turn", StopSequence: "s", Usage: Usage{InputUncached: UsageValue{Source: "a"}, CacheWrite: UsageValue{Source: "b"}, CacheRead: UsageValue{Source: "c"}, Output: UsageValue{Source: "d"}, Reasoning: UsageValue{Source: "e"}}}
	cost, err = MeasureResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if cost.PayloadBytes != 18 {
		t.Fatalf("response fields omitted: %+v", cost)
	}
	t.Logf("cell sizes: Request=%d Response=%d Message=%d ContentBlock=%d Agent=%d ToolDefinition=%d ToolNamespace=%d ContextEdit=%d Evidence=%d Fragment=%d Stop=%d Notice=%d", unsafe.Sizeof(Request{}), unsafe.Sizeof(Response{}), unsafe.Sizeof(Message{}), unsafe.Sizeof(ContentBlock{}), unsafe.Sizeof(AgentMessageContext{}), unsafe.Sizeof(ToolDefinition{}), unsafe.Sizeof(ToolNamespace{}), unsafe.Sizeof(ContextEdit{}), unsafe.Sizeof(ProtocolEvidenceValue{}), unsafe.Sizeof([]byte(nil)), unsafe.Sizeof(""), unsafe.Sizeof(TranslationNotice{}))
}

func TestResourceCheckedAccountingArithmetic(t *testing.T) {
	var meter resourceMeter
	meter.add(math.MaxUint64, 0)
	meter.add(1, 0)
	if meter.err == nil {
		t.Fatal("payload addition overflow accepted")
	}
	meter = resourceMeter{}
	meter.cells(math.MaxInt, 3)
	if meter.err == nil {
		t.Fatal("cell multiplication overflow accepted")
	}
}

func TestResourceRepeatedExtensionFragments(t *testing.T) {
	fragments := make([][]byte, 100000)
	for i := range fragments {
		fragments[i] = []byte(`null`)
	}
	extension, err := NewProviderExtension(ProviderExtensionSourceOpenAIResponses, ProviderExtensionReasoningContent, "$.reasoning", fragments)
	if err != nil {
		t.Fatal(err)
	}
	response := Response{ID: "r", RequestedModel: "m", EffectiveModel: "m", ReportedModel: "m", StopReason: StopReasonEndTurn, ProviderExtensions: []ProviderExtension{extension}}
	limits := ResourceLimits{Request: ResourceCost{16 << 20, 16 << 20}, Response: ResourceCost{16 << 20, 1024}}
	if _, err := CloneResponseWithin(response, limits); err == nil {
		t.Fatal("tiny extension fragment cells not charged")
	}
	cost, err := MeasureResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	// Root models/id4 + stop8 + source16 + kind17 + path11 + 400000 bytes.
	if cost.PayloadBytes != 400056 {
		t.Fatalf("fragment occurrences not charged: %+v", cost)
	}
}

func TestResourceBudgetReservationIsAtomic(t *testing.T) {
	b, err := NewResourceBudget(ResourceCost{10, 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Reserve(ResourceCost{7, 9}); err != nil {
		t.Fatal(err)
	}
	if err := b.Reserve(ResourceCost{4, 1}); err == nil {
		t.Fatal("payload overrun accepted")
	}
	if err := b.Reserve(ResourceCost{3, 11}); err != nil {
		t.Fatalf("failed reservation consumed capacity: %v", err)
	}
	if err := b.Reserve(ResourceCost{0, 1}); err == nil {
		t.Fatal("structure overrun accepted")
	}
}

func TestResourceBudgetZeroAndOverflow(t *testing.T) {
	for _, limit := range []ResourceCost{{}, {1, 0}, {0, 1}} {
		if _, err := NewResourceBudget(limit); err == nil {
			t.Fatalf("invalid limit accepted: %+v", limit)
		}
	}
	b, err := NewResourceBudget(ResourceCost{math.MaxUint64, math.MaxUint64})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Reserve(ResourceCost{math.MaxUint64, math.MaxUint64}); err != nil {
		t.Fatal(err)
	}
	if err := b.Reserve(ResourceCost{1, 0}); err == nil {
		t.Fatal("overflow accepted")
	}
}

func TestResourceReportGrowthRetainsPartialOrder(t *testing.T) {
	budget, _ := NewResourceBudget(ResourceCost{20, 128})
	var builder TranslationReportBuilder
	first := NewTranslationReport(TranslationNotice{Code: "c", Path: "$.first"})
	if err := AppendReportWithin(&builder, first, budget); err != nil {
		t.Fatal(err)
	}
	if err := AppendReportWithin(&builder, NewTranslationReport(TranslationNotice{Code: "code", Path: strings.Repeat("p", 20)}), budget); err == nil {
		t.Fatal("generated notice path overrun accepted")
	}
	notices := builder.Build().Notices()
	if len(notices) != 1 || notices[0].Path != "$.first" {
		t.Fatalf("partial report changed: %+v", notices)
	}
}

func TestResourceRepeatedAndInactivePayload(t *testing.T) {
	document, err := NewJSONObject([]byte(`{"x":"`+strings.Repeat("x", (4<<20)-8)+`"}`), MaxToolJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{RequestedModel: "m", EffectiveModel: "m", Messages: []Message{{Role: RoleUser, Blocks: []ContentBlock{{Kind: BlockText, Text: "", ToolCall: ToolCall{Arguments: document}}, {Kind: BlockText, ToolCall: ToolCall{Arguments: document}}}}}}
	limits := ResourceLimits{Request: ResourceCost{(4 << 20) + 100, 1 << 20}, Response: ResourceCost{1 << 20, 1 << 20}}
	if _, err := CloneRequestWithin(request, limits); err == nil {
		t.Fatal("repeated inactive JSON received a free clone allowance")
	}
	cost, err := MeasureRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	// 2 model bytes + 4 role bytes + 2*4 kind bytes + 2*4MiB document bytes.
	if cost.PayloadBytes != (8<<20)+14 {
		t.Fatalf("all occurrences must be charged: %+v", cost)
	}
}

func TestResourceDenseRejectionPrecedesClone(t *testing.T) {
	request := Request{RequestedModel: "m", EffectiveModel: "m", Messages: make([]Message, 16384)}
	for i := range request.Messages {
		request.Messages[i] = Message{Role: RoleUser, Blocks: []ContentBlock{{Kind: BlockText}}}
	}
	limits := ResourceLimits{Request: ResourceCost{1 << 20, 1024}, Response: ResourceCost{1 << 20, 1024}}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 10; i++ {
		if _, err := CloneRequestWithin(request, limits); err == nil {
			t.Fatal("dense empty cells exceeded structure limit")
		}
	}
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("rejected clone allocated %d bytes", allocated)
	}
}

func TestResourceModelCloneAndTailSemantics(t *testing.T) {
	limits := ResourceLimits{Request: ResourceCost{1 << 20, 32 << 20}, Response: ResourceCost{1 << 20, 32 << 20}}
	request := Request{RequestedModel: "m", EffectiveModel: "m", Messages: []Message{{Role: RoleUser, Blocks: []ContentBlock{{Kind: BlockText, Text: "tail"}}}}}
	cloned, err := WithEffectiveModelWithin(request, "mapped", limits)
	if err != nil {
		t.Fatal(err)
	}
	cloned.Messages[0].Blocks[0].Text = "changed"
	if request.EffectiveModel != "m" || request.Messages[0].Blocks[0].Text != "tail" {
		t.Fatal("original request mutated")
	}
	request.Messages = append(make([]Message, 4110), request.Messages[0])
	for i := 0; i < 4110; i++ {
		request.Messages[i] = Message{Role: RoleUser, Blocks: []ContentBlock{{Kind: BlockText}}}
	}
	request.Messages[4110].Role = "invalid"
	if err := ValidateRequestWithin(request, limits); err == nil || !strings.Contains(err.Error(), "message 4110") {
		t.Fatalf("tail semantics lost: %v", err)
	}
}
