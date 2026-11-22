package openairesponses_test

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/vibe-agi/vibermate/internal/anthropicchat"
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
	Encrypted string          `json:"encrypted_content"`
	Output    json.RawMessage `json:"output"`
	Content   []wirePart      `json:"content"`
	Summary   []wirePart      `json:"summary"`
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

// assertOracleAccepts unmarshals an encoded body into the official SDK's
// responses.ResponseNewParams and asserts its fields explicitly, because the
// SDK's unmarshalling is loose: it rejects malformed shapes but would let an
// unknown member pass unnoticed.
func assertOracleAccepts(t *testing.T, body []byte) {
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
		case "function_call":
			call := item.OfFunctionCall
			if call == nil || call.CallID != want.CallID || call.Name != want.Name || call.Arguments != want.Arguments {
				t.Fatalf("SDK item %d call = %+v", index, call)
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
			reasoning := item.OfReasoning
			if reasoning == nil || reasoning.ID != want.ID || reasoning.EncryptedContent.Value != want.Encrypted ||
				len(reasoning.Summary) != len(want.Summary) {
				t.Fatalf("SDK item %d reasoning = %+v", index, reasoning)
			}
			for summaryIndex, summary := range reasoning.Summary {
				if summary.Text != want.Summary[summaryIndex].Text {
					t.Fatalf("SDK item %d summary %d = %q", index, summaryIndex, summary.Text)
				}
			}
		default:
			t.Fatalf("item %d has an unexpected type %s", index, want.Type)
		}
	}
}

// TestProviderEncodeOracle holds every body the encoder emits for the Claude
// corpus and for the request-mapping rows against openai-go v3.46.0.
func TestProviderEncodeOracle(t *testing.T) {
	t.Parallel()
	side := newProviderSide(t, nil)
	for _, file := range claudeCorpusFiles(t) {
		t.Run(filepath.Base(file), func(t *testing.T) {
			t.Parallel()
			request := decodeClaude(t, readFile(t, file))
			encoded, _, err := side.EncodeRequestFor(protocolpath.ProviderContext{ReasoningBinding: testBinding}, request)
			if err != nil {
				t.Fatal(err)
			}
			assertOracleAccepts(t, encoded.Body())
		})
	}
	for _, row := range encodeRows() {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			body, _ := encodeRowBody(t, row)
			assertOracleAccepts(t, body)
		})
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
