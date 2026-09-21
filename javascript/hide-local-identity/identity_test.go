package hideidentity_test

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

// Test the actual copy/paste files in the production Goja sandbox, not a Node mock.
//
//go:embed request.js
var requestSource string

//go:embed response.js
var responseSource string

var (
	pipelineOnce sync.Once
	pipeline     messagetransform.Pipeline
	pipelineErr  error
)

const (
	workspaceAlias = "/__vmi1_workspace__"
	homeAlias      = "/__vmi1_home__"
	userAlias      = "⟪vmi1_user⟫"
)

func metadata(user string) messagetransform.RuntimeMetadata {
	return messagetransform.RuntimeMetadata{
		LocalUserName: user, HomeDirectory: "/Users/" + user,
		WorkspaceRoot: "/Users/" + user + "/work/project", OperatingSystem: "darwin",
	}
}

func newTurn(t *testing.T, meta messagetransform.RuntimeMetadata) *messagetransform.PipelineTurn {
	t.Helper()
	pipelineOnce.Do(func() {
		pipeline, pipelineErr = messagetransform.CompilePipeline([]messagetransform.Policy{{
			RequestJavaScript: requestSource, ResponseJavaScript: responseSource,
		}}, messagetransform.DefaultLimits())
	})
	if pipelineErr != nil {
		t.Fatal(pipelineErr)
	}
	turn, err := pipeline.NewTurnWithMetadata(meta)
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

func marshal(t *testing.T, value any) []byte {
	t.Helper()
	result, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func decode(t *testing.T, value []byte) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(value, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func input(t *testing.T, turn *messagetransform.PipelineTurn, value any) map[string]any {
	t.Helper()
	result, err := turn.ApplyRequest(context.Background(), messagetransform.RequestMessage{
		Method: http.MethodPost, Path: "/v1/responses", Headers: http.Header{"X-Probe": {"unchanged"}}, Body: marshal(t, value),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Method != http.MethodPost || result.Path != "/v1/responses" || result.Headers.Get("X-Probe") != "unchanged" {
		t.Fatal("request envelope changed")
	}
	return decode(t, result.Body)
}

func applyResponse(t *testing.T, turn *messagetransform.PipelineTurn, value map[string]any, streaming bool) (map[string]any, error) {
	t.Helper()
	name, _ := value["type"].(string)
	result, err := turn.ApplyResponse(context.Background(), messagetransform.ResponseMessage{
		StatusCode: 200, Streaming: streaming, EventName: name, Headers: http.Header{"X-Probe": {"unchanged"}}, Body: marshal(t, value),
	})
	if err != nil {
		return nil, err
	}
	if result.StatusCode != 200 || result.EventName != name || result.Streaming != streaming || result.Headers.Get("X-Probe") != "unchanged" {
		t.Fatal("response envelope changed")
	}
	return decode(t, result.Body), nil
}

func output(t *testing.T, turn *messagetransform.PipelineTurn, value map[string]any, streaming bool) map[string]any {
	t.Helper()
	result, err := applyResponse(t, turn, value, streaming)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func ready(t *testing.T, meta messagetransform.RuntimeMetadata) *messagetransform.PipelineTurn {
	t.Helper()
	turn := newTurn(t, meta)
	input(t, turn, map[string]any{"input": "continue", "stream": true})
	return turn
}

func responseText(text string) map[string]any {
	return map[string]any{"type": "message", "content": []any{map[string]any{"type": "text", "text": text}}}
}

func readText(value map[string]any) string {
	return value["content"].([]any)[0].(map[string]any)["text"].(string)
}

func TestIdentitySourceIsBounded(t *testing.T) {
	for name, source := range map[string]string{"request": requestSource, "response": responseSource} {
		if len(source) > messagetransform.DefaultLimits().MaximumScriptBytes {
			t.Fatalf("%s exceeds Runtime source limit", name)
		}
	}
	newTurn(t, metadata("alice"))
}

func TestIdentityRequestAndResponseRoundTrip(t *testing.T) {
	meta := metadata("alice")
	turn := newTurn(t, meta)
	arguments := map[string]any{
		"command": "cd " + meta.WorkspaceRoot + " && echo alice",
		"cwd":     meta.HomeDirectory, "array": []any{true, 7, nil, "alice"},
		"nested": map[string]any{"file": meta.WorkspaceRoot + "/src/a.go"},
	}
	originalText := "alice works in " + meta.WorkspaceRoot + "; home=" + meta.HomeDirectory + "."
	// A trailing full stop is ambiguous with a filename extension. Use explicit delimiters.
	originalText = strings.TrimSuffix(originalText, ".") + " (local)"
	result := input(t, turn, map[string]any{
		"model": "alice-model", "stream": true, "previous_response_id": "alice-response",
		"metadata":     map[string]any{"routing": "alice"},
		"instructions": originalText,
		"input": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": originalText}}},
			map[string]any{"type": "function_call", "name": "alice", "call_id": "alice-call", "arguments": string(marshal(t, arguments))},
			map[string]any{"type": "function_call_output", "call_id": "alice-call", "output": originalText},
		},
		"tools": []any{map[string]any{"type": "function", "name": "alice", "description": originalText, "parameters": map[string]any{"type": "object"}}},
	})
	if result["model"] != "alice-model" || result["previous_response_id"] != "alice-response" || result["stream"] != true {
		t.Fatal("protocol controls changed")
	}
	if result["metadata"].(map[string]any)["routing"] != "alice" {
		t.Fatal("opaque metadata changed")
	}
	wantText := userAlias + " works in " + workspaceAlias + "; home=" + homeAlias + " (local)"
	if result["instructions"] != wantText {
		t.Fatalf("masked text = %q, want %q", result["instructions"], wantText)
	}
	items := result["input"].([]any)
	call := items[1].(map[string]any)
	if call["name"] != "alice" || call["call_id"] != "alice-call" {
		t.Fatal("tool identity changed")
	}
	maskedArgs := call["arguments"].(string)
	if strings.Contains(maskedArgs, meta.HomeDirectory) || !strings.Contains(maskedArgs, workspaceAlias) {
		t.Fatal("tool values were not masked")
	}
	restored := output(t, turn, map[string]any{"output": []any{
		map[string]any{"type": "message", "id": "keep", "content": []any{map[string]any{"type": "output_text", "text": wantText}}},
		map[string]any{"type": "function_call", "name": "alice", "call_id": "alice-call", "arguments": maskedArgs},
	}}, false)
	returned := restored["output"].([]any)
	if readText(returned[0].(map[string]any)) != originalText {
		t.Fatal("text did not round-trip")
	}
	if got := decode(t, []byte(returned[1].(map[string]any)["arguments"].(string))); !reflect.DeepEqual(got, decode(t, marshal(t, arguments))) {
		t.Fatalf("arguments changed: %#v", got)
	}
}

func TestIdentityAllThreeRequestAndResponseShapes(t *testing.T) {
	for _, protocol := range []string{"responses", "anthropic", "chat"} {
		t.Run(protocol, func(t *testing.T) {
			turn := newTurn(t, metadata("alice"))
			var request map[string]any
			var response map[string]any
			switch protocol {
			case "responses":
				request = map[string]any{"input": []any{map[string]any{"type": "message", "role": "user", "content": "alice"}}}
				response = map[string]any{"output": []any{map[string]any{"type": "message", "content": userAlias}}}
			case "anthropic":
				request = map[string]any{"system": "alice", "messages": []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "tool_use", "id": "tool1", "name": "shell", "input": map[string]any{"cwd": "/Users/alice"}},
					map[string]any{"type": "tool_result", "tool_use_id": "tool1", "content": []any{map[string]any{"type": "text", "text": "alice"}}},
				}}}}
				response = responseText(userAlias)
			case "chat":
				request = map[string]any{"messages": []any{map[string]any{"role": "assistant", "content": "alice", "tool_calls": []any{
					map[string]any{"type": "function", "id": "tool1", "function": map[string]any{"name": "shell", "arguments": `{"cwd":"/Users/alice"}`}},
				}}}}
				response = map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": userAlias}}}}
			}
			masked := string(marshal(t, input(t, turn, request)))
			if strings.Contains(masked, "alice") || !strings.Contains(masked, userAlias) {
				t.Fatalf("unsupported request shape: %s", masked)
			}
			if got := string(marshal(t, output(t, turn, response, false))); !strings.Contains(got, "alice") || strings.Contains(got, userAlias) {
				t.Fatalf("unsupported response shape: %s", got)
			}
		})
	}
}

func TestIdentityBoundariesAndLiteralCollisions(t *testing.T) {
	meta := metadata("alice")
	for _, test := range []struct{ input, want string }{
		{"alice alice2 malice alice-test alice@example.test", userAlias + " alice2 malice alice-test alice@example.test"},
		{"/Users/alice/work/project/src/a.go", workspaceAlias + "/src/a.go"},
		{"/Users/alice/work/project-other", homeAlias + "/work/project-other"},
		{"/Users/alice-other /backup/Users/alice", "/Users/alice-other /backup/Users/" + userAlias},
		{"/workspace/project /Users/guest vibermate-user", "/workspace/project /Users/guest vibermate-user"},
		{"file:///Users/alice/work/project/a.txt", "file://" + workspaceAlias + "/a.txt"},
		{"alice. /Users/alice. /Users/alice/work/project。", userAlias + ". " + homeAlias + ". " + workspaceAlias + "。"},
		{"alice名 名alice alice2 malice", "alice名 名alice alice2 malice"},
	} {
		t.Run(test.input, func(t *testing.T) {
			turn := newTurn(t, meta)
			masked := input(t, turn, map[string]any{"input": test.input})["input"].(string)
			if masked != test.want {
				t.Fatalf("got %q want %q", masked, test.want)
			}
			if restored := readText(output(t, turn, responseText(masked), false)); restored != test.input {
				t.Fatalf("round-trip got %q want %q", restored, test.input)
			}
		})
	}
	for _, alias := range []string{workspaceAlias, userAlias, `\u005f_vmi1_home__`} {
		t.Run("collision_"+alias, func(t *testing.T) {
			turn := newTurn(t, meta)
			body := `{"input":"` + alias + `"}`
			if _, err := turn.ApplyRequest(context.Background(), messagetransform.RequestMessage{Headers: http.Header{}, Body: []byte(body)}); err == nil {
				t.Fatal("literal reserved namespace must not be silently reinterpreted")
			}
		})
	}
}

func TestIdentityStableMappingWithoutRequestHitsAndParallelIsolation(t *testing.T) {
	for index := 0; index < 12; index++ {
		user := fmt.Sprintf("person%d", index)
		t.Run(user, func(t *testing.T) {
			t.Parallel()
			meta := metadata(user)
			for round := 0; round < 2; round++ {
				turn := ready(t, meta)
				got := readText(output(t, turn, responseText(userAlias+" "+homeAlias+" "+workspaceAlias), false))
				if want := user + " " + meta.HomeDirectory + " " + meta.WorkspaceRoot; got != want {
					t.Fatalf("mapping leaked across users/turns: %q", got)
				}
			}
		})
	}
}

func TestIdentityPathRepresentationsAndJSONEscapes(t *testing.T) {
	for _, meta := range []messagetransform.RuntimeMetadata{
		{LocalUserName: "Alice Smith", HomeDirectory: "/Users/Alice Smith", WorkspaceRoot: "/Users/Alice Smith/中文项目"},
		{LocalUserName: "alice", HomeDirectory: `C:\Users\alice`, WorkspaceRoot: `C:\Users\alice\Work\project`, OperatingSystem: "windows"},
		{LocalUserName: "alice", HomeDirectory: "/home/alice", WorkspaceRoot: "/home/alice", OperatingSystem: "linux"},
		{LocalUserName: "alice", HomeDirectory: "/home/alice", WorkspaceRoot: "/srv/project", OperatingSystem: "linux"},
	} {
		t.Run(meta.WorkspaceRoot, func(t *testing.T) {
			for _, original := range []string{meta.WorkspaceRoot + "/file.txt", meta.HomeDirectory, strings.ReplaceAll(meta.WorkspaceRoot, `\`, "/")} {
				turn := newTurn(t, meta)
				masked := input(t, turn, map[string]any{"input": original})["input"].(string)
				if masked == original {
					t.Fatalf("not hidden: %q", original)
				}
				if restored := readText(output(t, turn, responseText(masked), false)); restored != original {
					t.Fatalf("round-trip changed encoding: %q != %q", restored, original)
				}
			}
		})
	}
	for _, original := range []string{"/Users/Alice%20Smith/work/project", "%2FUsers%2FAlice%20Smith%2Fwork%2Fproject"} {
		turn := newTurn(t, metadata("Alice Smith"))
		masked := input(t, turn, map[string]any{"input": original})["input"].(string)
		if masked == original || strings.Contains(masked, "Alice") {
			t.Fatalf("encoded path was not masked: %q", masked)
		}
		if restored := readText(output(t, turn, responseText(masked), false)); restored != original {
			t.Fatal("encoded representation not preserved")
		}
	}
	turn := newTurn(t, metadata("alice"))
	request, err := turn.ApplyRequest(context.Background(), messagetransform.RequestMessage{
		Headers: http.Header{}, Body: []byte(`{"input":"\u0061lice at \/Users\/alice\/work\/project"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if decode(t, request.Body)["input"] != userAlias+" at "+workspaceAlias {
		t.Fatalf("JSON escaping bypassed masking: %s", request.Body)
	}
}

func TestIdentityCommonNamesNeedIdentityContext(t *testing.T) {
	turn := newTurn(t, metadata("root"))
	result := input(t, turn, map[string]any{"input": "root directory; username: root; /Users/root/work/project; root_value"})
	if want := "root directory; username: " + userAlias + "; " + workspaceAlias + "; root_value"; result["input"] != want {
		t.Fatalf("common word was corrupted: %q", result["input"])
	}
}

func TestIdentityOpaqueFieldsAndSignedThinking(t *testing.T) {
	turn := newTurn(t, metadata("alice"))
	thought := map[string]any{"type": "thinking", "thinking": "Use " + workspaceAlias, "signature": "signed-opaque"}
	image := map[string]any{"type": "image", "source": map[string]any{"type": "base64", "data": "opaque-alice"}}
	result := input(t, turn, map[string]any{"messages": []any{map[string]any{"role": "assistant", "content": []any{thought, image, map[string]any{"type": "text", "text": "alice"}}}}})
	blocks := result["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if !reflect.DeepEqual(blocks[0], thought) || !reflect.DeepEqual(blocks[1], image) {
		t.Fatal("signed or binary fields changed")
	}
	result = output(t, turn, map[string]any{"content": []any{thought, map[string]any{"type": "text", "text": userAlias}}, "usage": map[string]any{"output_tokens": 7}}, false)
	if !reflect.DeepEqual(result["content"].([]any)[0], thought) || result["usage"].(map[string]any)["output_tokens"] != float64(7) {
		t.Fatal("opaque provider evidence changed")
	}
}

func TestIdentityFailsClosedWithoutLeakingErrorText(t *testing.T) {
	for name, body := range map[string]string{
		"invalid_json":            `{"input":`,
		"unsupported_request":     `{"prompt":"alice"}`,
		"unsafe_integer":          `{"input":"alice","counter":9007199254740993}`,
		"rounded_decimal":         `{"input":"alice","amount":0.10000000000000001}`,
		"negative_zero":           `{"input":"alice","amount":-0}`,
		"underflow":               `{"input":"alice","amount":1e-400}`,
		"invalid_tool_json":       `{"input":[{"type":"function_call","name":"shell","arguments":"{broken}"}]}`,
		"private_tool_key":        `{"input":[{"type":"function_call","arguments":"{\"/Users/alice\":\"value\"}"}]}`,
		"signed_private_thought":  `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"/Users/alice","signature":"opaque"}]}]}`,
		"unknown_private_content": `{"input":[{"type":"new_extension","plaintext":"/Users/alice"}]}`,
		"private_schema":          `{"input":"hi","tools":[{"name":"shell","parameters":{"const":"/Users/alice"}}]}`,
		"reserved_schema":         `{"input":"hi","tools":[{"name":"shell","parameters":{"const":"/__vmi1_home__"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			turn := newTurn(t, metadata("alice"))
			_, err := turn.ApplyRequest(context.Background(), messagetransform.RequestMessage{Headers: http.Header{}, Body: []byte(body)})
			if err == nil {
				t.Fatal("expected explicit refusal")
			}
			if strings.Contains(err.Error(), "alice") || strings.Contains(err.Error(), "opaque") {
				t.Fatal("error leaked message content")
			}
		})
	}
	for _, meta := range []messagetransform.RuntimeMetadata{{}, {LocalUserName: "alice", HomeDirectory: "/", WorkspaceRoot: "/work"}} {
		turn := newTurn(t, meta)
		if _, err := turn.ApplyRequest(context.Background(), messagetransform.RequestMessage{Headers: http.Header{}, Body: []byte(`{"input":"hello"}`)}); err == nil {
			t.Fatal("missing or root-wide metadata accepted")
		}
	}
	turn := newTurn(t, metadata("alice"))
	if _, err := applyResponse(t, turn, responseText(userAlias), false); err == nil {
		t.Fatal("response-only installation must not guess a mapping")
	}
}

func TestIdentityNoChangePreservesBodyBytes(t *testing.T) {
	turn := newTurn(t, metadata("alice"))
	body := []byte("{ \"input\": \"hello\", \"stream\":true }\n")
	first, err := turn.ApplyRequest(context.Background(), messagetransform.RequestMessage{Headers: http.Header{}, Body: body})
	if err != nil || string(first.Body) != string(body) {
		t.Fatalf("unnecessary request serialization: %s, %v", first.Body, err)
	}
	// The Runtime reuses an identical request on an internal retry.
	second, err := turn.ApplyRequest(context.Background(), messagetransform.RequestMessage{Headers: http.Header{}, Body: body})
	if err != nil || string(second.Body) != string(first.Body) {
		t.Fatal("retry was not idempotent")
	}
	responseBody := []byte("{ \"content\":[{\"type\":\"text\",\"text\":\"hello\"}] }\n")
	response, err := turn.ApplyResponse(context.Background(), messagetransform.ResponseMessage{StatusCode: 200, Headers: http.Header{}, Body: responseBody})
	if err != nil || string(response.Body) != string(responseBody) {
		t.Fatal("unnecessary response serialization")
	}
}

// Three real wire layouts. Each call remains exactly one event with its original type.
func delta(protocol string, text string, tool bool, channel int) map[string]any {
	switch protocol {
	case "responses":
		typeName := "response.output_text.delta"
		if tool {
			typeName = "response.function_call_arguments.delta"
		}
		return map[string]any{"type": typeName, "output_index": channel, "content_index": 0, "item_id": fmt.Sprintf("item%d", channel), "delta": text}
	case "anthropic":
		value := map[string]any{"type": "text_delta", "text": text}
		if tool {
			value = map[string]any{"type": "input_json_delta", "partial_json": text}
		}
		return map[string]any{"type": "content_block_delta", "index": channel, "delta": value}
	default:
		value := map[string]any{"content": text}
		if tool {
			value = map[string]any{"tool_calls": []any{map[string]any{"index": channel, "function": map[string]any{"arguments": text}}}}
		}
		return map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": value, "finish_reason": nil}}}
	}
}

func deltaText(value map[string]any, protocol string, tool bool) string {
	switch protocol {
	case "responses":
		return value["delta"].(string)
	case "anthropic":
		key := "text"
		if tool {
			key = "partial_json"
		}
		return value["delta"].(map[string]any)[key].(string)
	default:
		delta := value["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
		if tool {
			return delta["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)
		}
		return delta["content"].(string)
	}
}

func terminal(protocol string) map[string]any {
	switch protocol {
	case "responses":
		return map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{}}}
	case "anthropic":
		return map[string]any{"type": "message_stop"}
	default:
		return map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}
	}
}

func TestIdentityTextStreamEveryAliasSplit(t *testing.T) {
	meta := metadata("alice")
	for _, protocol := range []string{"responses", "anthropic", "chat"} {
		t.Run(protocol, func(t *testing.T) {
			for _, alias := range []string{workspaceAlias, homeAlias, userAlias} {
				chars := []rune(alias)
				for split := 0; split <= len(chars); split++ {
					turn := ready(t, meta)
					var collected strings.Builder
					for _, fragment := range []string{"before " + string(chars[:split]), string(chars[split:]) + "/file.txt after"} {
						collected.WriteString(deltaText(output(t, turn, delta(protocol, fragment, false, 0), true), protocol, false))
					}
					output(t, turn, terminal(protocol), true)
					want := "before " + map[string]string{workspaceAlias: meta.WorkspaceRoot, homeAlias: meta.HomeDirectory, userAlias: meta.LocalUserName}[alias] + "/file.txt after"
					if collected.String() != want {
						t.Fatalf("%s split %d: got %q want %q", alias, split, collected.String(), want)
					}
				}
			}
		})
	}
}

func TestIdentityStreamToolJSONEverySplitAndInterleaving(t *testing.T) {
	for _, protocol := range []string{"responses", "anthropic", "chat"} {
		t.Run(protocol, func(t *testing.T) {
			original := `{"cwd":"` + workspaceAlias + `/a.go","user":"` + userAlias + `","literal":"quote: \"; newline: \n"}`
			chars := []rune(original)
			for split := 0; split <= len(chars); split++ {
				turn := ready(t, metadata("alice"))
				first := deltaText(output(t, turn, delta(protocol, string(chars[:split]), true, 0), true), protocol, true)
				// A second tool call must not consume the first tool's partial JSON.
				other := deltaText(output(t, turn, delta(protocol, `{"cwd":"`+homeAlias+`"}`, true, 1), true), protocol, true)
				second := deltaText(output(t, turn, delta(protocol, string(chars[split:]), true, 0), true), protocol, true)
				output(t, turn, terminal(protocol), true)
				got := decode(t, []byte(first+second))
				if got["cwd"] != "/Users/alice/work/project/a.go" || got["user"] != "alice" || got["literal"] != "quote: \"; newline: \n" || decode(t, []byte(other))["cwd"] != "/Users/alice" {
					t.Fatalf("split %d mixed or corrupted arguments: %#v", split, got)
				}
			}
		})
	}
}

func TestIdentityWindowsStreamArgumentsAndLiteralSuffix(t *testing.T) {
	meta := messagetransform.RuntimeMetadata{LocalUserName: "alice", HomeDirectory: `C:\Users\alice`, WorkspaceRoot: `C:\Users\alice\project`}
	turn := newTurn(t, meta)
	masked := input(t, turn, map[string]any{"input": meta.WorkspaceRoot})["input"].(string)
	arguments := string(marshal(t, map[string]any{"cwd": masked + `\src\"quoted"`, "user": userAlias}))
	var collected strings.Builder
	for _, char := range arguments {
		collected.WriteString(deltaText(output(t, turn, delta("responses", string(char), true, 0), true), "responses", true))
	}
	output(t, turn, terminal("responses"), true)
	if got := decode(t, []byte(collected.String())); got["cwd"] != meta.WorkspaceRoot+`\src\"quoted"` || got["user"] != "alice" {
		t.Fatalf("Windows JSON escaping corrupted: %#v", got)
	}
	for _, text := range []string{"ordinary /", "variable_", "C:", "x /", "hello ⟪something else⟫"} {
		turn := ready(t, meta)
		var got strings.Builder
		for _, char := range text {
			got.WriteString(deltaText(output(t, turn, delta("responses", string(char), false, 0), true), "responses", false))
		}
		output(t, turn, terminal("responses"), true)
		if got.String() != text {
			t.Fatalf("ordinary suffix lost: %q != %q", got.String(), text)
		}
	}
}

func TestIdentityStreamSnapshotsMatchDeltas(t *testing.T) {
	turn := ready(t, metadata("alice"))
	output(t, turn, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "name": "shell", "arguments": ""}}, true)
	args := `{"cwd":"` + workspaceAlias + `"}`
	streamed := deltaText(output(t, turn, delta("responses", args, true, 0), true), "responses", true)
	done := output(t, turn, map[string]any{"type": "response.function_call_arguments.done", "output_index": 0, "arguments": args}, true)
	item := map[string]any{"type": "function_call", "name": "shell", "arguments": args}
	itemDone := output(t, turn, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}, true)
	complete := output(t, turn, map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{item}}}, true)
	for _, got := range []string{done["arguments"].(string), itemDone["item"].(map[string]any)["arguments"].(string), complete["response"].(map[string]any)["output"].([]any)[0].(map[string]any)["arguments"].(string)} {
		if got != streamed {
			t.Fatalf("terminal snapshot diverged: %q != %q", got, streamed)
		}
	}
}

func TestIdentityStreamExplicitFailureBoundaries(t *testing.T) {
	for _, protocol := range []string{"responses", "anthropic", "chat"} {
		for _, tool := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_tool_%v", protocol, tool), func(t *testing.T) {
				turn := ready(t, metadata("alice"))
				partial := "/__vmi1_wor"
				if tool {
					partial = `{"cwd":"` + workspaceAlias
				}
				output(t, turn, delta(protocol, partial, tool, 0), true)
				if _, err := applyResponse(t, turn, terminal(protocol), true); err == nil {
					t.Fatal("incomplete stream was silently accepted")
				}
			})
		}
	}
	turn := ready(t, metadata("alice"))
	if _, err := applyResponse(t, turn, delta("responses", `{"data":"`+strings.Repeat("x", 16385), true, 0), true); err == nil {
		t.Fatal("unbounded tool argument buffering accepted")
	}
	turn = ready(t, metadata("alice"))
	for index := 0; index < 32; index++ {
		output(t, turn, delta("responses", "a", false, index), true)
	}
	if _, err := applyResponse(t, turn, delta("responses", "a", false, 32), true); err == nil {
		t.Fatal("unbounded channel state accepted")
	}
}

func TestIdentitySSEWireChunksKeepEventNamesAndJSON(t *testing.T) {
	turn := ready(t, metadata("alice"))
	var wire []byte
	for _, fragment := range []string{"go /__vmi1_", "workspace__/file", " and " + userAlias} {
		event := delta("responses", fragment, false, 0)
		encoded, err := ssewire.Encode(ssewire.Event{Name: event["type"].(string), Data: marshal(t, event)})
		if err != nil {
			t.Fatal(err)
		}
		wire = append(wire, encoded...)
	}
	decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	var collected strings.Builder
	for _, octet := range wire {
		events, err := decoder.Feed([]byte{octet})
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			result, err := turn.ApplyResponse(context.Background(), messagetransform.ResponseMessage{StatusCode: 200, Streaming: true, EventName: event.Name, Headers: http.Header{}, Body: event.Data})
			if err != nil {
				t.Fatal(err)
			}
			if result.EventName != event.Name {
				t.Fatal("SSE type changed")
			}
			collected.WriteString(decode(t, result.Body)["delta"].(string))
			if _, err := ssewire.Encode(ssewire.Event{Name: result.EventName, Data: result.Body}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := decoder.Finish(); err != nil {
		t.Fatal(err)
	}
	output(t, turn, terminal("responses"), true)
	if collected.String() != "go /Users/alice/work/project/file and alice" {
		t.Fatalf("wire chunk reconstruction failed: %q", collected.String())
	}
}

func TestIdentityInitialTextAndInterleavedTextChannels(t *testing.T) {
	turn := ready(t, metadata("alice"))
	start := output(t, turn, map[string]any{
		"type": "response.content_part.added", "output_index": 0, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": "/__vmi1_"},
	}, true)
	first := start["part"].(map[string]any)["text"].(string)
	other := deltaText(output(t, turn, delta("responses", homeAlias, false, 1), true), "responses", false)
	last := deltaText(output(t, turn, delta("responses", "workspace__/a.txt", false, 0), true), "responses", false)
	output(t, turn, terminal("responses"), true)
	if first+last != "/Users/alice/work/project/a.txt" || other != "/Users/alice" {
		t.Fatalf("text channels mixed: %q %q %q", first, other, last)
	}

	turn = ready(t, metadata("alice"))
	start = output(t, turn, map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": "/__vmi1_"},
	}, true)
	first = start["content_block"].(map[string]any)["text"].(string)
	last = deltaText(output(t, turn, delta("anthropic", "home__/a.txt", false, 0), true), "anthropic", false)
	output(t, turn, map[string]any{"type": "content_block_stop", "index": 0}, true)
	if first+last != "/Users/alice/a.txt" {
		t.Fatalf("initial Anthropic text lost: %q", first+last)
	}
}

func TestIdentityCustomToolsAndChatChoices(t *testing.T) {
	turn := newTurn(t, metadata("alice"))
	original := "*** Update File: /Users/alice/work/project/main.go\n+hello alice\n"
	masked := input(t, turn, map[string]any{"input": []any{
		map[string]any{"type": "custom_tool_call", "name": "apply_patch", "call_id": "keep", "input": original},
	}})["input"].([]any)[0].(map[string]any)["input"].(string)
	var got strings.Builder
	for _, char := range masked {
		event := map[string]any{"type": "response.custom_tool_call_input.delta", "output_index": 0, "delta": string(char)}
		got.WriteString(output(t, turn, event, true)["delta"].(string))
	}
	done := output(t, turn, map[string]any{"type": "response.custom_tool_call_input.done", "output_index": 0, "input": masked}, true)
	if got.String() != original || done["input"] != original {
		t.Fatal("custom tool input changed semantics")
	}
	turn = ready(t, metadata("alice"))
	var left, right strings.Builder
	for _, pair := range [][2]string{{"/__vmi1_", "⟪vmi1_"}, {"home__", "user⟫"}} {
		event := map[string]any{"choices": []any{
			map[string]any{"index": 0, "delta": map[string]any{"content": pair[0]}, "finish_reason": nil},
			map[string]any{"index": 1, "delta": map[string]any{"content": pair[1]}, "finish_reason": nil},
		}}
		choices := output(t, turn, event, true)["choices"].([]any)
		left.WriteString(choices[0].(map[string]any)["delta"].(map[string]any)["content"].(string))
		right.WriteString(choices[1].(map[string]any)["delta"].(map[string]any)["content"].(string))
	}
	output(t, turn, map[string]any{"choices": []any{
		map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"},
		map[string]any{"index": 1, "delta": map[string]any{}, "finish_reason": "stop"},
	}}, true)
	if left.String() != "/Users/alice" || right.String() != "alice" {
		t.Fatal("Chat choices mixed")
	}
}

func TestIdentityNonSuccessResponseIsUnchanged(t *testing.T) {
	turn := ready(t, metadata("alice"))
	body := []byte("<html>Provider unavailable</html>")
	result, err := turn.ApplyResponse(context.Background(), messagetransform.ResponseMessage{StatusCode: 503, Headers: http.Header{}, Body: body})
	if err != nil || result.StatusCode != 503 || string(result.Body) != string(body) {
		t.Fatalf("provider error obscured: %#v, %v", result, err)
	}
}

func TestIdentitySafeNumbersAndNumericStrings(t *testing.T) {
	turn := newTurn(t, metadata("alice"))
	result, err := turn.ApplyRequest(context.Background(), messagetransform.RequestMessage{Headers: http.Header{}, Body: []byte(
		`{"input":"alice and 9007199254740993","temperature":0.10,"count":1e3,"fraction":1.5e-4,"nested":"{\"number\":0.10000000000000001}"}`,
	)})
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, result.Body)
	if got["input"] != userAlias+" and 9007199254740993" || got["temperature"] != 0.1 || got["count"] != float64(1000) || got["fraction"] != 0.00015 || got["nested"] != `{"number":0.10000000000000001}` {
		t.Fatalf("numeric or quoted literal changed: %#v", got)
	}
}
