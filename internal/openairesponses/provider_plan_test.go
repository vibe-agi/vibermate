package openairesponses

import (
	"slices"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

const (
	planModel   = "gpt-6.2-sol"
	planBinding = "0123456789abcdef0123456789abcdef"
)

func newPlanSide(t *testing.T, profile func(*ProviderRequestProfile)) *ProviderSide {
	t.Helper()
	options := DefaultProviderSideOptions()
	if profile != nil {
		profile(&options.Profile)
	}
	side, err := NewProviderSide(options)
	if err != nil {
		t.Fatal(err)
	}
	return side
}

func mustBlock(t *testing.T, block protocolcore.ContentBlock, err error) protocolcore.ContentBlock {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return block
}

func textBlock(t *testing.T, text string) protocolcore.ContentBlock {
	t.Helper()
	block, err := protocolcore.NewTextBlock(text)
	return mustBlock(t, block, err)
}

func reasoningBlock(t *testing.T, reasoning protocolcore.Reasoning) protocolcore.ContentBlock {
	t.Helper()
	block, err := protocolcore.NewReasoningBlock(reasoning)
	return mustBlock(t, block, err)
}

func callKey(t *testing.T, id string) protocolcore.CallKey {
	t.Helper()
	key, err := protocolcore.NewCallKey("anthropic-messages-openai-chat", id)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func readCallBlock(t *testing.T, id string) protocolcore.ContentBlock {
	t.Helper()
	arguments, err := protocolcore.NewJSONObject([]byte(`{"path":"a.txt"}`), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	block, err := protocolcore.NewToolCallBlock(protocolcore.ToolCall{Key: callKey(t, id), Name: "Read", Arguments: arguments})
	return mustBlock(t, block, err)
}

func readResultBlock(t *testing.T, id string) protocolcore.ContentBlock {
	t.Helper()
	block, err := protocolcore.NewToolResultBlock(protocolcore.ToolResult{Key: callKey(t, id), Content: "one"})
	return mustBlock(t, block, err)
}

func validSeal() protocolcore.ReasoningSeal {
	return protocolcore.ReasoningSeal{
		Format:  protocolcore.SealResponsesEncrypted,
		ItemID:  "rs_1",
		Model:   planModel,
		Binding: planBinding,
		Opaque:  "ENC-1",
	}
}

func planRequestFor(t *testing.T, messages ...protocolcore.Message) protocolcore.Request {
	t.Helper()
	schema, err := protocolcore.NewJSONObject([]byte(`{"type":"object","properties":{"path":{"type":"string"}}}`), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return protocolcore.Request{
		RequestedModel:  "claude-opus-5-5",
		EffectiveModel:  planModel,
		MaxOutputTokens: 1024,
		Stream:          true,
		Messages:        messages,
		Tools:           []protocolcore.ToolDefinition{{Name: "Read", InputSchema: schema}},
	}
}

func userText(t *testing.T, text string) protocolcore.Message {
	t.Helper()
	return protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{textBlock(t, text)}}
}

// replayRequest is one tool round whose call follows a reasoning block, or,
// without following, an assistant turn that ends with its reasoning.
func replayRequest(t *testing.T, reasoning protocolcore.Reasoning, following bool) protocolcore.Request {
	t.Helper()
	assistant := protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{reasoningBlock(t, reasoning)}}
	if !following {
		return planRequestFor(t, userText(t, "read"), assistant, userText(t, "again"))
	}
	assistant.Blocks = append(assistant.Blocks, readCallBlock(t, "toolu_1"))
	return planRequestFor(t,
		userText(t, "read"),
		assistant,
		protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{readResultBlock(t, "toolu_1")}},
	)
}

func itemKinds(plan requestPlan) []planItemKind {
	kinds := []planItemKind{}
	for _, item := range plan.items {
		kinds = append(kinds, item.kind)
	}
	return kinds
}

func planNoticeLines(plan requestPlan) []string {
	lines := []string{}
	for _, notice := range plan.report.Notices() {
		lines = append(lines, string(notice.Code)+" "+notice.Path)
	}
	return lines
}

// TestReasoningReplayChecks pins the six checks of design 6.3 and the drop
// rules around them: a reasoning block becomes a reasoning item only when its
// seal is one this path minted for the effective model under this attempt's
// binding, the provider has not refused it, and an item follows it in its
// assistant message. Every other block is dropped and declared.
func TestReasoningReplayChecks(t *testing.T) {
	t.Parallel()
	replayContext := protocolpath.ProviderContext{ReasoningBinding: planBinding}
	sealed := protocolcore.Reasoning{Text: "I should read it.", Seal: validSeal()}
	with := func(change func(*protocolcore.ReasoningSeal)) protocolcore.Reasoning {
		reasoning := sealed
		change(&reasoning.Seal)
		return reasoning
	}
	rejecting := replayContext
	rejecting.RejectedSeal = func(digest [32]byte) bool { return digest == protocolcore.SealDigest(validSeal()) }
	notRejecting := replayContext
	notRejecting.RejectedSeal = func([32]byte) bool { return false }

	replayed := []planItemKind{planItemMessage, planItemReasoning, planItemFunctionCall, planItemFunctionCallOutput}
	dropped := []planItemKind{planItemMessage, planItemFunctionCall, planItemFunctionCallOutput}
	for _, test := range []struct {
		name      string
		reasoning protocolcore.Reasoning
		context   protocolpath.ProviderContext
		profile   func(*ProviderRequestProfile)
		following bool
		want      []planItemKind
	}{
		{name: "every check holds", reasoning: sealed, context: replayContext, following: true, want: replayed},
		{name: "a memory that does not hold the seal", reasoning: sealed, context: notRejecting, following: true, want: replayed},
		// 1. The seal parses: anything else is an opaque Anthropic signature.
		{name: "an unparsable seal", context: replayContext, following: true, want: dropped, reasoning: protocolcore.Reasoning{
			Text: "I should read it.",
			Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealAnthropicSignature, Opaque: "vmr1.not-a-seal.ENC-1"},
		}},
		// 2. Its format is openai-responses.encrypted.
		{name: "an Anthropic seal with the same model and binding", context: replayContext, following: true, want: dropped, reasoning: protocolcore.Reasoning{
			Text: "I should read it.",
			Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealAnthropicSignature, Model: planModel, Binding: planBinding, Opaque: "ENC-1"},
		}},
		{name: "redacted thinking", context: replayContext, following: true, want: dropped, reasoning: protocolcore.Reasoning{
			Redacted: true,
			Seal:     protocolcore.ReasoningSeal{Format: protocolcore.SealAnthropicRedacted, Opaque: "ENC-1"},
		}},
		// 3. Its model is the effective model.
		{name: "another model's seal", context: replayContext, following: true, want: dropped,
			reasoning: with(func(seal *protocolcore.ReasoningSeal) { seal.Model = "gpt-6-luna" })},
		// 4. Its binding is this attempt's.
		{name: "another binding's seal", context: replayContext, following: true, want: dropped,
			reasoning: with(func(seal *protocolcore.ReasoningSeal) { seal.Binding = "ffffffffffffffffffffffffffffffff" })},
		{name: "a seal under a zero context", context: protocolpath.ProviderContext{}, following: true, want: dropped, reasoning: sealed},
		{name: "an unbound seal under a zero context", context: protocolpath.ProviderContext{}, following: true, want: replayed,
			reasoning: with(func(seal *protocolcore.ReasoningSeal) { seal.Binding = "" })},
		{name: "an unbound seal under a bound context", context: replayContext, following: true, want: dropped,
			reasoning: with(func(seal *protocolcore.ReasoningSeal) { seal.Binding = "" })},
		// 5. The rejected-seal memory does not hold it.
		{name: "a seal the provider refused", reasoning: sealed, context: rejecting, following: true, want: dropped},
		// 6. An item follows it in its assistant message.
		{name: "reasoning that ends its turn", reasoning: sealed, context: replayContext, following: false,
			want: []planItemKind{planItemMessage, planItemMessage}},
		// And the rules around them.
		{name: "a seal without a payload", context: replayContext, following: true, want: dropped,
			reasoning: with(func(seal *protocolcore.ReasoningSeal) { seal.Opaque = "" })},
		{name: "no seal", context: replayContext, following: true, want: dropped, reasoning: protocolcore.Reasoning{Text: "I should read it."}},
		{name: "replay switched off", reasoning: sealed, context: replayContext, following: true, want: dropped,
			profile: func(profile *ProviderRequestProfile) { profile.ReplayReasoning = ReplayReasoningOff }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plan, err := newPlanSide(t, test.profile).planRequest(test.context, replayRequest(t, test.reasoning, test.following))
			if err != nil {
				t.Fatal(err)
			}
			if got := itemKinds(plan); !slices.Equal(got, test.want) {
				t.Fatalf("items = %v, want %v", got, test.want)
			}
			notices := planNoticeLines(plan)
			if slices.Contains(test.want, planItemReasoning) {
				item := plan.items[1]
				if item.reasoningID != "rs_1" || item.encrypted != "ENC-1" || item.summary != "I should read it." {
					t.Fatalf("reasoning item = %+v", item)
				}
				if len(notices) != 0 {
					t.Fatalf("notices = %q", notices)
				}
				return
			}
			if !slices.Equal(notices, []string{"reasoning_not_replayed $.messages[1].blocks[0]"}) {
				t.Fatalf("notices = %q", notices)
			}
		})
	}
}

// TestReasoningReplayKeepsThinkingTurns emulates clear_thinking's
// thinking_turns by replaying only the last N assistant turns that reason;
// keep all replays every eligible block.
func TestReasoningReplayKeepsThinkingTurns(t *testing.T) {
	t.Parallel()
	turn := func(itemID string) protocolcore.Message {
		seal := validSeal()
		seal.ItemID = itemID
		return protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{
			reasoningBlock(t, protocolcore.Reasoning{Text: "thought " + itemID, Seal: seal}),
			textBlock(t, "answer "+itemID),
		}}
	}
	request := planRequestFor(t, userText(t, "one"), turn("rs_a"), userText(t, "two"), turn("rs_b"), userText(t, "three"))
	replayContext := protocolpath.ProviderContext{ReasoningBinding: planBinding}
	for _, test := range []struct {
		name    string
		edits   []protocolcore.ContextEdit
		want    []string
		notices []string
	}{
		{name: "no edit", want: []string{"rs_a", "rs_b"}},
		{name: "keep all", edits: []protocolcore.ContextEdit{{Kind: protocolcore.ContextEditClearThinking, KeepAll: true}}, want: []string{"rs_a", "rs_b"}},
		{
			name:    "keep one turn",
			edits:   []protocolcore.ContextEdit{{Kind: protocolcore.ContextEditClearThinking, KeepThinkingTurns: 1}},
			want:    []string{"rs_b"},
			notices: []string{"reasoning_not_replayed $.messages[1].blocks[0]"},
		},
		{name: "keep more turns than exist", edits: []protocolcore.ContextEdit{{Kind: protocolcore.ContextEditClearThinking, KeepThinkingTurns: 5}}, want: []string{"rs_a", "rs_b"}},
		// With several edits the fewest kept turns apply, in either order.
		{
			name: "several edits, fewest last",
			edits: []protocolcore.ContextEdit{
				{Kind: protocolcore.ContextEditClearThinking, KeepThinkingTurns: 3},
				{Kind: protocolcore.ContextEditClearThinking, KeepThinkingTurns: 1},
			},
			want:    []string{"rs_b"},
			notices: []string{"reasoning_not_replayed $.messages[1].blocks[0]"},
		},
		{
			name: "several edits, fewest first",
			edits: []protocolcore.ContextEdit{
				{Kind: protocolcore.ContextEditClearThinking, KeepThinkingTurns: 1},
				{Kind: protocolcore.ContextEditClearThinking, KeepThinkingTurns: 3},
			},
			want:    []string{"rs_b"},
			notices: []string{"reasoning_not_replayed $.messages[1].blocks[0]"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			edited := request
			edited.Context = protocolcore.ContextManagementIntent{Edits: test.edits}
			plan, err := newPlanSide(t, nil).planRequest(replayContext, edited)
			if err != nil {
				t.Fatal(err)
			}
			replayedIDs := []string{}
			for _, item := range plan.items {
				if item.kind == planItemReasoning {
					replayedIDs = append(replayedIDs, item.reasoningID)
				}
			}
			if !slices.Equal(replayedIDs, test.want) {
				t.Fatalf("replayed = %v, want %v", replayedIDs, test.want)
			}
			reasoningNotices := []string{}
			for _, line := range planNoticeLines(plan) {
				if line != "message_phase_inferred $.messages[1].blocks[1]" && line != "message_phase_inferred $.messages[3].blocks[1]" {
					reasoningNotices = append(reasoningNotices, line)
				}
			}
			if !slices.Equal(reasoningNotices, append([]string{}, test.notices...)) {
				t.Fatalf("notices = %q, want %q", reasoningNotices, test.notices)
			}
		})
	}
}

// TestReasoningSitsRightBeforeItsItem splits an assistant text run around a
// reasoning block, so the reasoning item precedes exactly the item it
// produced.
func TestReasoningSitsRightBeforeItsItem(t *testing.T) {
	t.Parallel()
	assistant := protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{
		textBlock(t, "first"),
		reasoningBlock(t, protocolcore.Reasoning{Text: "between", Seal: validSeal()}),
		textBlock(t, "second"),
		readCallBlock(t, "toolu_1"),
	}}
	request := planRequestFor(t, userText(t, "go"), assistant,
		protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{readResultBlock(t, "toolu_1")}})
	plan, err := newPlanSide(t, nil).planRequest(protocolpath.ProviderContext{ReasoningBinding: planBinding}, request)
	if err != nil {
		t.Fatal(err)
	}
	want := []planItemKind{planItemMessage, planItemMessage, planItemReasoning, planItemMessage, planItemFunctionCall, planItemFunctionCallOutput}
	if got := itemKinds(plan); !slices.Equal(got, want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	if plan.items[1].phase != "commentary" || plan.items[3].phase != "commentary" ||
		plan.items[1].parts[0].text != "first" || plan.items[3].parts[0].text != "second" {
		t.Fatalf("assistant messages = %+v, %+v", plan.items[1], plan.items[3])
	}
}

// TestPlanRefusesWhatHasNoResponsesForm keeps the fail-closed rules: inputs a
// Responses body cannot carry faithfully are refused, never dropped.
func TestPlanRefusesWhatHasNoResponsesForm(t *testing.T) {
	t.Parallel()
	base := planRequestFor(t, userText(t, "go"))
	extension, err := protocolcore.NewProviderExtension(
		protocolcore.ProviderExtensionSourceAnthropicMessages,
		protocolcore.ProviderExtensionThinking,
		"$.messages[1].content[0]",
		[][]byte{[]byte(`{"type":"thinking","thinking":"x","signature":"y"}`)},
	)
	if err != nil {
		t.Fatal(err)
	}
	extensionBlock, err := protocolcore.NewProviderExtensionBlock(extension)
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		change func(*protocolcore.Request)
		path   string
	}{
		"a provider extension in history": {
			change: func(request *protocolcore.Request) {
				request.Messages = append(request.Messages,
					protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{extensionBlock, textBlock(t, "x")}},
					userText(t, "again"))
			},
			path: "$.messages[1].blocks[0]",
		},
		"agent provenance": {
			change: func(request *protocolcore.Request) {
				request.Messages[0].Agent = &protocolcore.AgentMessageContext{AgentName: "worker"}
			},
			path: "$.messages[0].agent",
		},
		"tool namespaces": {
			change: func(request *protocolcore.Request) {
				request.ToolNamespaces = []protocolcore.ToolNamespace{{Name: "ns", Description: "d", Tools: []protocolcore.ToolDefinition{base.Tools[0]}}}
			},
			path: "$.tool_namespaces",
		},
		"a custom tool": {
			change: func(request *protocolcore.Request) {
				request.Tools = []protocolcore.ToolDefinition{{
					Kind: protocolcore.ToolKindCustom, Name: "exec",
					CustomFormat: protocolcore.CustomToolFormat{Kind: protocolcore.CustomToolFormatText},
				}}
			},
			path: "$.tools[0]",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			request := base.Clone()
			test.change(&request)
			_, err := newPlanSide(t, nil).planRequest(protocolpath.ProviderContext{}, request)
			if protocolcore.ReasonOf(err) != protocolcore.ReasonUnsupportedClientInput || failurePath(err) != test.path {
				t.Fatalf("plan error = %v, want unsupported input at %s", err, test.path)
			}
		})
	}
}

func failurePath(err error) string {
	failure, ok := err.(*protocolcore.Failure)
	if !ok {
		return ""
	}
	return failure.Path
}

func TestProviderProfileSwitches(t *testing.T) {
	t.Parallel()
	if err := PublicResponsesProfile().validate(); err != nil {
		t.Fatalf("public profile: %v", err)
	}
	for name, change := range map[string]func(*ProviderRequestProfile){
		"zero instruction placement":  func(profile *ProviderRequestProfile) { profile.InstructionPlacement = 0 },
		"zero assistant phase":        func(profile *ProviderRequestProfile) { profile.AssistantPhase = 0 },
		"zero sampling":               func(profile *ProviderRequestProfile) { profile.Sampling = 0 },
		"unknown encrypted inclusion": func(profile *ProviderRequestProfile) { profile.IncludeEncryptedReasoning = 2 },
		"zero replay":                 func(profile *ProviderRequestProfile) { profile.ReplayReasoning = 0 },
		"zero replay summary":         func(profile *ProviderRequestProfile) { profile.ReplaySummary = 0 },
		"zero tool output documents":  func(profile *ProviderRequestProfile) { profile.ToolOutputDocuments = 0 },
		"unknown schema normalizer":   func(profile *ProviderRequestProfile) { profile.SchemaNormalizer = 2 },
		"unknown image detail":        func(profile *ProviderRequestProfile) { profile.ImageDetail = "ultra" },
	} {
		options := DefaultProviderSideOptions()
		change(&options.Profile)
		if _, err := NewProviderSide(options); err == nil {
			t.Errorf("%s: profile accepted", name)
		}
	}
	if _, err := NewProviderSide(ProviderSideOptions{Profile: PublicResponsesProfile()}); err == nil {
		t.Error("a zero request limit was accepted")
	}
	if _, err := NewProviderSide(ProviderSideOptions{MaxRequestBytes: 1, Profile: ProviderRequestProfile{}}); err == nil {
		t.Error("a zero profile was accepted")
	}
}

// TestEffortForBudget pins each arm of design 6.4's budget approximation.
func TestEffortForBudget(t *testing.T) {
	t.Parallel()
	for budget, want := range map[int]protocolcore.ReasoningEffort{
		1024:  protocolcore.ReasoningEffortLow,
		4095:  protocolcore.ReasoningEffortLow,
		4096:  protocolcore.ReasoningEffortMedium,
		16383: protocolcore.ReasoningEffortMedium,
		16384: protocolcore.ReasoningEffortHigh,
		32767: protocolcore.ReasoningEffortHigh,
		32768: protocolcore.ReasoningEffortXHigh,
		64000: protocolcore.ReasoningEffortXHigh,
	} {
		if got := effortForBudget(budget); got != want {
			t.Errorf("effortForBudget(%d) = %s, want %s", budget, got, want)
		}
	}
}
