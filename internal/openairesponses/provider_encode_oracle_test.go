package openairesponses_test

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

// wireItem is the encoder's own view of an input item, to check the SDK's.
type wireItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Phase     string          `json:"phase"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Arguments string          `json:"arguments"`
	Output    json.RawMessage `json:"output"`
	Content   []wirePart      `json:"content"`
}

// wirePart is one content part of an input item.
type wirePart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL string `json:"image_url"`
	FileData string `json:"file_data"`
	FileURL  string `json:"file_url"`
	Filename string `json:"filename"`
}

// assertSDKInputParts checks that the SDK filed each user-message part under
// the variant its type names, with the same values.
func assertSDKInputParts(t *testing.T, index int, parts []responses.ResponseInputContentUnionParam, want []wirePart) {
	t.Helper()
	if len(parts) != len(want) {
		t.Fatalf("SDK item %d has %d parts, want %d", index, len(parts), len(want))
	}
	for partIndex, part := range parts {
		expected := want[partIndex]
		var ok bool
		switch expected.Type {
		case "input_text":
			ok = part.OfInputText != nil && part.OfInputText.Text == expected.Text
		case "input_image":
			ok = part.OfInputImage != nil && part.OfInputImage.ImageURL.Value == expected.ImageURL
		case "input_file":
			ok = part.OfInputFile != nil && part.OfInputFile.FileData.Value == expected.FileData &&
				part.OfInputFile.FileURL.Value == expected.FileURL && part.OfInputFile.Filename.Value == expected.Filename
		}
		if !ok {
			t.Fatalf("SDK item %d part %d = %+v, want %+v", index, partIndex, part, expected)
		}
	}
}

// assertSDKOutputParts does the same for a function_call_output's list.
func assertSDKOutputParts(t *testing.T, index int, parts []responses.ResponseFunctionCallOutputItemUnionParam, want []wirePart) {
	t.Helper()
	if len(parts) != len(want) {
		t.Fatalf("SDK item %d has %d output parts, want %d", index, len(parts), len(want))
	}
	for partIndex, part := range parts {
		expected := want[partIndex]
		var ok bool
		switch expected.Type {
		case "input_text":
			ok = part.OfInputText != nil && part.OfInputText.Text == expected.Text
		case "input_image":
			ok = part.OfInputImage != nil && part.OfInputImage.ImageURL.Value == expected.ImageURL
		case "input_file":
			ok = part.OfInputFile != nil && part.OfInputFile.FileData.Value == expected.FileData &&
				part.OfInputFile.FileURL.Value == expected.FileURL && part.OfInputFile.Filename.Value == expected.Filename
		}
		if !ok {
			t.Fatalf("SDK item %d output part %d = %+v, want %+v", index, partIndex, part, expected)
		}
	}
}

// oracleCoverage counts what the oracle compared with values taken from the
// neutral request, so a run that never replays reasoning cannot pass.
type oracleCoverage struct {
	reasoningItems atomic.Int64
	reasoningText  atomic.Int64
	assistantParts atomic.Int64
}

// assistantPart is one part of an assistant message: output_text or refusal.
type assistantPart struct {
	kind, text string
}

// expectedAssistantParts are the parts the request's assistant messages
// become, in order: every text block an output_text part and every refusal
// block a refusal part, whatever items separate them.
func expectedAssistantParts(request protocolcore.Request) []assistantPart {
	var parts []assistantPart
	for _, message := range request.Messages {
		if message.Role != protocolcore.RoleAssistant {
			continue
		}
		for _, block := range message.Blocks {
			switch block.Kind {
			case protocolcore.BlockText:
				parts = append(parts, assistantPart{"output_text", block.Text})
			case protocolcore.BlockRefusal:
				parts = append(parts, assistantPart{"refusal", block.Refusal})
			}
		}
	}
	return parts
}

// expectedCalls are the request's assistant tool calls, in order; each
// becomes one function_call item.
func expectedCalls(request protocolcore.Request) []protocolcore.ToolCall {
	var calls []protocolcore.ToolCall
	for _, message := range request.Messages {
		if message.Role != protocolcore.RoleAssistant {
			continue
		}
		for _, block := range message.Blocks {
			if block.Kind == protocolcore.BlockToolCall {
				calls = append(calls, block.ToolCall)
			}
		}
	}
	return calls
}

// replayedReasoning finds the request's reasoning block whose Responses seal
// names item id.
func replayedReasoning(request protocolcore.Request, id string) (protocolcore.Reasoning, bool) {
	for _, message := range request.Messages {
		for _, block := range message.Blocks {
			if block.Kind == protocolcore.BlockReasoning && block.Reasoning != nil &&
				block.Reasoning.Seal.Format == protocolcore.SealResponsesEncrypted && block.Reasoning.Seal.ItemID == id {
				return *block.Reasoning, true
			}
		}
	}
	return protocolcore.Reasoning{}, false
}

// assertSDKReasoning checks a replayed reasoning item as the SDK reads it
// against the reasoning block it came from: the seal's payload as
// encrypted_content, and the block's text under the part type its source
// names.
func assertSDKReasoning(t *testing.T, index int, request protocolcore.Request, item *responses.ResponseReasoningItemParam, coverage *oracleCoverage) {
	t.Helper()
	if item == nil {
		t.Fatalf("SDK item %d is not a reasoning item", index)
	}
	reasoning, found := replayedReasoning(request, item.ID)
	if !found {
		t.Fatalf("SDK item %d replays reasoning %q the request does not hold", index, item.ID)
	}
	if reasoning.Seal.Opaque == "" || item.EncryptedContent.Value != reasoning.Seal.Opaque {
		t.Fatalf("SDK item %d encrypted_content = %q, want the seal's %q", index, item.EncryptedContent.Value, reasoning.Seal.Opaque)
	}
	coverage.reasoningItems.Add(1)
	if reasoning.TextSource == protocolcore.ReasoningTextContent {
		if len(item.Summary) != 0 || (reasoning.Text == "") != (len(item.Content) == 0) {
			t.Fatalf("SDK item %d raw reasoning = %+v / %+v, want %q as content", index, item.Summary, item.Content, reasoning.Text)
		}
		if len(item.Content) != 0 {
			if len(item.Content) != 1 || item.Content[0].Type != "reasoning_text" || item.Content[0].Text != reasoning.Text {
				t.Fatalf("SDK item %d content = %+v, want one reasoning_text %q", index, item.Content, reasoning.Text)
			}
			coverage.reasoningText.Add(1)
		}
		return
	}
	// A summary is replayed only when the profile rebuilds it.
	if len(item.Content) != 0 || len(item.Summary) > 1 ||
		(len(item.Summary) == 1 && (item.Summary[0].Type != "summary_text" || item.Summary[0].Text != reasoning.Text || reasoning.Text == "")) {
		t.Fatalf("SDK item %d summary = %+v / %+v, want at most one summary_text %q", index, item.Summary, item.Content, reasoning.Text)
	}
}

// assertSDKAssistantParts reads an assistant message through the SDK's
// output-message type, since the input union files every message under its
// easy-input variant whatever its parts.
func assertSDKAssistantParts(t *testing.T, index int, raw json.RawMessage) []assistantPart {
	t.Helper()
	var message responses.ResponseOutputMessageParam
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatalf("SDK cannot read item %d as an output message: %v", index, err)
	}
	parts := make([]assistantPart, 0, len(message.Content))
	for partIndex, part := range message.Content {
		switch {
		case part.OfOutputText != nil && part.OfOutputText.Type == "output_text":
			parts = append(parts, assistantPart{"output_text", part.OfOutputText.Text})
		case part.OfRefusal != nil && part.OfRefusal.Type == "refusal":
			parts = append(parts, assistantPart{"refusal", part.OfRefusal.Refusal})
		default:
			t.Fatalf("SDK item %d part %d is not an output_text or refusal part: %s", index, partIndex, raw)
		}
	}
	return parts
}

// assertOracleAccepts unmarshals an encoded body into the official SDK's
// responses.ResponseNewParams and asserts its fields explicitly, because the
// SDK's unmarshalling is loose: it rejects malformed shapes but would let an
// unknown member pass unnoticed. Replayed reasoning and assistant parts are
// compared with the neutral request, not with another parse of the body, so
// a misnamed member cannot read as empty on both sides.
func assertOracleAccepts(t *testing.T, request protocolcore.Request, body []byte, coverage *oracleCoverage) {
	t.Helper()
	var oracle responses.ResponseNewParams
	if err := json.Unmarshal(body, &oracle); err != nil {
		t.Fatalf("official OpenAI SDK rejected the body: %v\n%s", err, body)
	}
	var own struct {
		Model           string            `json:"model"`
		Include         []string          `json:"include"`
		MaxOutputTokens *int64            `json:"max_output_tokens"`
		PromptCacheKey  string            `json:"prompt_cache_key"`
		Reasoning       map[string]string `json:"reasoning"`
		Instructions    *string           `json:"instructions"`
		Tools           []struct {
			Name string `json:"name"`
		} `json:"tools"`
		ToolChoice json.RawMessage `json:"tool_choice"`
		Text       struct {
			Format *struct {
				Name   string `json:"name"`
				Strict bool   `json:"strict"`
			} `json:"format"`
		} `json:"text"`
		Input []wireItem `json:"input"`
	}
	if err := json.Unmarshal(body, &own); err != nil {
		t.Fatal(err)
	}
	var rawInput struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &rawInput); err != nil {
		t.Fatal(err)
	}
	if oracle.Model != own.Model || !oracle.Store.Valid() || oracle.Store.Value {
		t.Fatalf("SDK model/store = %q %+v", oracle.Model, oracle.Store)
	}
	include := []string{}
	for _, value := range oracle.Include {
		include = append(include, string(value))
	}
	if !slices.Equal(include, append([]string{}, own.Include...)) {
		t.Fatalf("SDK include = %v, want %v", include, own.Include)
	}
	if (own.MaxOutputTokens == nil) != !oracle.MaxOutputTokens.Valid() ||
		(own.MaxOutputTokens != nil && *own.MaxOutputTokens != oracle.MaxOutputTokens.Value) {
		t.Fatalf("SDK max_output_tokens = %+v", oracle.MaxOutputTokens)
	}
	if oracle.PromptCacheKey.Value != own.PromptCacheKey ||
		string(oracle.Reasoning.Effort) != own.Reasoning["effort"] ||
		string(oracle.Reasoning.Summary) != own.Reasoning["summary"] {
		t.Fatalf("SDK cache key/reasoning = %+v %+v", oracle.PromptCacheKey, oracle.Reasoning)
	}
	if (own.Instructions != nil) != (oracle.Instructions.Value != "") {
		t.Fatalf("SDK instructions = %+v", oracle.Instructions)
	}
	if len(oracle.Tools) != len(own.Tools) {
		t.Fatalf("SDK tools = %d, want %d", len(oracle.Tools), len(own.Tools))
	}
	for index, tool := range oracle.Tools {
		function := tool.OfFunction
		if function == nil || function.Name != own.Tools[index].Name || !function.Strict.Valid() || function.Strict.Value ||
			function.Parameters == nil || function.Parameters["$schema"] != nil {
			t.Fatalf("SDK tool %d = %+v", index, tool)
		}
	}
	if len(own.Tools) != 0 {
		choice := oracle.ToolChoice
		if choice.GetType() == nil && !choice.OfToolChoiceMode.Valid() {
			t.Fatalf("SDK tool choice is missing: %+v", choice)
		}
		var mode string
		if json.Unmarshal(own.ToolChoice, &mode) == nil {
			if string(choice.OfToolChoiceMode.Value) != mode {
				t.Fatalf("SDK tool choice = %+v, want %s", choice, mode)
			}
		} else {
			// The SDK's union decoding is loose enough to file a named
			// function choice under allowed_tools, so the object is checked
			// against the SDK's own named-function type directly.
			var named responses.ToolChoiceFunctionParam
			var ownNamed struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(own.ToolChoice, &named); err != nil ||
				json.Unmarshal(own.ToolChoice, &ownNamed) != nil ||
				named.Name == "" || named.Name != ownNamed.Name || string(named.Type) != "function" {
				t.Fatalf("SDK named tool choice = %+v (%v)", named, err)
			}
		}
		if !oracle.ParallelToolCalls.Valid() {
			t.Fatal("SDK parallel_tool_calls is missing")
		}
	}
	if own.Text.Format != nil {
		format := oracle.Text.Format.OfJSONSchema
		if format == nil || format.Name != own.Text.Format.Name || format.Strict.Value != own.Text.Format.Strict || format.Schema == nil {
			t.Fatalf("SDK text format = %+v", oracle.Text.Format)
		}
	}
	items := oracle.Input.OfInputItemList
	if len(items) != len(own.Input) {
		t.Fatalf("SDK input = %d items, want %d", len(items), len(own.Input))
	}
	var assistant []assistantPart
	calls := expectedCalls(request)
	for index, item := range items {
		want := own.Input[index]
		if kind := item.GetType(); kind == nil || *kind != want.Type {
			t.Fatalf("SDK item %d type = %v, want %s", index, kind, want.Type)
		}
		switch want.Type {
		case "message":
			if role := item.GetRole(); role == nil || *role != want.Role {
				t.Fatalf("SDK item %d role = %v, want %s", index, role, want.Role)
			}
			if phase := item.GetPhase(); want.Phase != "" && (phase == nil || *phase != want.Phase) {
				t.Fatalf("SDK item %d phase = %v, want %s", index, phase, want.Phase)
			}
			if want.Role == "user" || want.Role == "developer" {
				if item.OfMessage == nil {
					t.Fatalf("SDK item %d is not an input message: %+v", index, item)
				}
				assertSDKInputParts(t, index, item.OfMessage.Content.OfInputItemContentList, want.Content)
			}
			if want.Role == "assistant" {
				assistant = append(assistant, assertSDKAssistantParts(t, index, rawInput.Input[index])...)
			}
		case "function_call":
			call := item.OfFunctionCall
			if call == nil || call.CallID != want.CallID || call.Name != want.Name || call.Arguments != want.Arguments {
				t.Fatalf("SDK item %d call = %+v", index, call)
			}
			// The call as the request holds it: its id (or the rewritten
			// form an id the API refuses gets), name and exact arguments.
			if len(calls) == 0 {
				t.Fatalf("SDK item %d is a call the request does not hold", index)
			}
			source := calls[0]
			calls = calls[1:]
			if (call.CallID != source.Key.WireID() && !strings.HasPrefix(call.CallID, "call_vm_")) ||
				(call.Name != source.Name && call.Name != hashedToolName(source.Name)) ||
				call.Arguments != string(source.Arguments.Bytes()) {
				t.Fatalf("SDK item %d call = %q %q %q, want the request's %q %q %s",
					index, call.CallID, call.Name, call.Arguments, source.Key.WireID(), source.Name, source.Arguments.Bytes())
			}
		case "function_call_output":
			output := item.OfFunctionCallOutput
			if output == nil || output.CallID != want.CallID {
				t.Fatalf("SDK item %d output = %+v", index, output)
			}
			var text string
			if json.Unmarshal(want.Output, &text) == nil {
				if !output.Output.OfString.Valid() || output.Output.OfString.Value != text {
					t.Fatalf("SDK item %d string output = %+v", index, output.Output)
				}
			} else {
				var parts []wirePart
				if err := json.Unmarshal(want.Output, &parts); err != nil {
					t.Fatal(err)
				}
				assertSDKOutputParts(t, index, output.Output.OfResponseFunctionCallOutputItemArray, parts)
			}
		case "reasoning":
			assertSDKReasoning(t, index, request, item.OfReasoning, coverage)
		default:
			t.Fatalf("item %d has an unexpected type %s", index, want.Type)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("SDK input lacks %d of the request's calls", len(calls))
	}
	if wantParts := expectedAssistantParts(request); !slices.Equal(assistant, wantParts) {
		t.Fatalf("SDK assistant parts = %+v, want %+v", assistant, wantParts)
	}
	coverage.assistantParts.Add(int64(len(assistant)))
}

// TestProviderEncodeOracle holds every body the encoder emits for the Claude
// corpus and for the request-mapping rows against openai-go v3.46.0.
func TestProviderEncodeOracle(t *testing.T) {
	t.Parallel()
	side := newProviderSide(t, nil)
	var coverage oracleCoverage
	t.Run("bodies", func(t *testing.T) {
		for _, file := range claudeCorpusFiles(t) {
			t.Run(filepath.Base(file), func(t *testing.T) {
				t.Parallel()
				request := decodeClaude(t, readFile(t, file))
				encoded, _, err := side.EncodeRequestFor(protocolpath.ProviderContext{ReasoningBinding: testBinding}, request)
				if err != nil {
					t.Fatal(err)
				}
				assertOracleAccepts(t, request, encoded.Body(), &coverage)
			})
		}
		for _, row := range encodeRows() {
			t.Run(row.name, func(t *testing.T) {
				t.Parallel()
				body, _ := encodeRowBody(t, row)
				assertOracleAccepts(t, rowNeutralRequest(t, row), body, &coverage)
			})
		}
	})
	// Without these the request-derived checks above could pass vacuously.
	if coverage.reasoningItems.Load() == 0 || coverage.reasoningText.Load() == 0 || coverage.assistantParts.Load() == 0 {
		t.Fatalf("the oracle compared %d reasoning items, %d reasoning_text parts and %d assistant parts",
			coverage.reasoningItems.Load(), coverage.reasoningText.Load(), coverage.assistantParts.Load())
	}
}

// TestProviderToolNamesMatchTheChatCatalog: the Responses name rule is the
// Chat catalog's as a pure function, so a conversation moved between the two
// paths keeps its tool names.
func TestProviderToolNamesMatchTheChatCatalog(t *testing.T) {
	t.Parallel()
	body := []byte(rowRequest(`"tools":[` + readTool + `,{"name":"my.tool","input_schema":{"type":"object","properties":{}}}],` +
		`"messages":[{"role":"user","content":"hi"}]`))
	chat, err := anthropicchat.New(anthropicchat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := chat.DecodeClientRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	chatBody, _, err := chat.EncodeProviderRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	var chatWire struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(chatBody, &chatWire); err != nil {
		t.Fatal(err)
	}
	encoded, _, err := newProviderSide(t, nil).EncodeRequestFor(protocolpath.ProviderContext{}, decodeClaude(t, body))
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(encoded.Body(), &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Tools) != 2 || len(chatWire.Tools) != 2 {
		t.Fatalf("tools = %+v, chat %+v", wire.Tools, chatWire.Tools)
	}
	for index := range wire.Tools {
		if wire.Tools[index].Name != chatWire.Tools[index].Function.Name {
			t.Errorf("tool %d: Responses %q, Chat %q", index, wire.Tools[index].Name, chatWire.Tools[index].Function.Name)
		}
	}
	if wire.Tools[1].Name != hashedToolName("my.tool") {
		t.Errorf("hashed name = %q", wire.Tools[1].Name)
	}
}
