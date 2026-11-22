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
				var parts []json.RawMessage
				if err := json.Unmarshal(want.Output, &parts); err != nil ||
					len(output.Output.OfResponseFunctionCallOutputItemArray) != len(parts) {
					t.Fatalf("SDK item %d list output = %+v", index, output.Output)
				}
			}
		case "reasoning":
			reasoning := item.OfReasoning
			if reasoning == nil || reasoning.ID != want.ID || reasoning.EncryptedContent.Value != want.Encrypted {
				t.Fatalf("SDK item %d reasoning = %+v", index, reasoning)
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
