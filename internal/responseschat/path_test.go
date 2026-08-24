package responseschat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

func TestResponsesPassthroughForwardsOnlyPortableConversationHistory(t *testing.T) {
	t.Parallel()

	path, err := NewResponsesPassthroughProtocolPath(openairesponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	source := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]},
			{
				"id":"rs_private",
				"type":"reasoning",
				"summary":[{"type":"summary_text","text":"private summary"}],
				"content":[{"type":"reasoning_text","text":"private reasoning"}],
				"encrypted_content":"opaque-provider-state"
			},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
		],
		"store":false,
		"stream":true
	}`)
	request, _, err := path.Client().DecodeRequest(source)
	if err != nil {
		t.Fatalf("DecodeRequest() error = %v", err)
	}
	request, err = request.WithEffectiveModel("provider-model")
	if err != nil {
		t.Fatal(err)
	}
	provider, report, err := path.EncodeProviderRequest(request, source, make(http.Header))
	if err != nil {
		t.Fatalf("EncodeProviderRequest() error = %v", err)
	}
	var wire struct {
		Model string            `json:"model"`
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(provider.Body(), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Model != "provider-model" || len(wire.Input) != 3 {
		t.Fatalf("provider wire = %s", provider.Body())
	}
	roles := make([]string, len(wire.Input))
	for index, raw := range wire.Input {
		var item struct {
			Type string `json:"type"`
			Role string `json:"role"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
		if item.Type != "message" {
			t.Fatalf("input[%d] type = %q", index, item.Type)
		}
		roles[index] = item.Role
	}
	if got, want := roles, []string{"user", "assistant", "user"}; !slices.Equal(got, want) {
		t.Fatalf("portable roles = %v, want %v", got, want)
	}
	if bytes.Contains(provider.Body(), []byte("private reasoning")) ||
		bytes.Contains(provider.Body(), []byte("opaque-provider-state")) {
		t.Fatalf("provider request leaked private reasoning: %s", provider.Body())
	}
	notices := report.Notices()
	if len(notices) != 1 ||
		notices[0].Code != protocolcore.NoticeReasoningExecutionNotForwarded ||
		notices[0].Path != "$.input[1]" {
		t.Fatalf("translation notices = %#v", notices)
	}
}

func TestResponsesPassthroughReleasesOnlyPortableConversationHistory(t *testing.T) {
	t.Parallel()

	path, err := NewResponsesPassthroughProtocolPath(openairesponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	sourceRequest := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
		],
		"store":false,
		"stream":false
	}`)
	request, _, err := path.Client().DecodeRequest(sourceRequest)
	if err != nil {
		t.Fatalf("DecodeRequest() error = %v", err)
	}
	request, err = request.WithEffectiveModel("provider-model")
	if err != nil {
		t.Fatal(err)
	}
	reasoning := json.RawMessage(`{
		"id":"rs_private",
		"type":"reasoning",
		"summary":[{"type":"summary_text","text":"private summary"}],
		"content":[{"type":"reasoning_text","text":"private reasoning"}],
		"encrypted_content":null,
		"status":"completed"
	}`)
	message := json.RawMessage(`{
		"id":"msg_portable",
		"type":"message",
		"status":"completed",
		"role":"assistant",
		"content":[{"type":"output_text","text":"portable answer"}]
	}`)
	sourceResponse, err := json.Marshal(map[string]any{
		"id":         "resp_portable",
		"created_at": 1,
		"status":     "completed",
		"model":      "provider-model",
		"output":     []json.RawMessage{reasoning, message},
		"usage":      map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, _, err := path.Backend().DecodeResponse(request, sourceResponse)
	if err != nil {
		t.Fatalf("DecodeResponse() error = %v", err)
	}
	if len(response.ProviderExtensions) != 2 {
		t.Fatalf("provider reasoning evidence = %#v", response.ProviderExtensions)
	}
	released, report, err := path.EncodeClientResponse(request, response, sourceResponse)
	if err != nil {
		t.Fatalf("EncodeClientResponse() error = %v", err)
	}
	var wire struct {
		Model  string            `json:"model"`
		Output []json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(released, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Model != request.RequestedModel || len(wire.Output) != 1 {
		t.Fatalf("released response = %s", released)
	}
	var item struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(wire.Output[0], &item); err != nil {
		t.Fatal(err)
	}
	if item.Type != "message" ||
		bytes.Contains(released, []byte("private reasoning")) {
		t.Fatalf("released response is not portable: %s", released)
	}
	notices := report.Notices()
	if len(notices) != 1 ||
		notices[0].Code != protocolcore.NoticeReasoningExecutionNotForwarded ||
		notices[0].Path != "$.output[0]" {
		t.Fatalf("translation notices = %#v", notices)
	}
}

func TestPathComposesFixedResponsesRequestWithExistingChatBackend(
	t *testing.T,
) {
	t.Parallel()

	path, err := NewProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request, clientReport, err := path.Client().DecodeRequest(
		fixedRequestFixture(),
	)
	if err != nil {
		t.Fatalf("DecodeRequest() error = %v", err)
	}
	request, err = request.WithEffectiveModel("provider-model")
	if err != nil {
		t.Fatal(err)
	}
	provider, backendReport, err := path.Backend().EncodeRequest(request)
	if err != nil {
		t.Fatalf("EncodeRequest() error = %v", err)
	}
	if provider.RelativePath() != anthropicchat.ProviderRelativePath ||
		provider.Headers().Get("Accept") != "text/event-stream" {
		t.Fatalf("provider request = %#v", provider)
	}
	var wire struct {
		Model    string `json:"model"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name       string          `json:"name"`
				Parameters json.RawMessage `json:"parameters"`
				Strict     *bool           `json:"strict"`
			} `json:"function"`
		} `json:"tools"`
		MaxTokens           *int  `json:"max_tokens"`
		MaxCompletionTokens *int  `json:"max_completion_tokens"`
		ParallelToolCalls   *bool `json:"parallel_tool_calls"`
		Stream              bool  `json:"stream"`
	}
	if err := json.Unmarshal(provider.Body(), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Model != "provider-model" ||
		len(wire.Messages) != 2 ||
		wire.Messages[0].Role != "system" ||
		wire.Messages[1].Role != "user" ||
		len(wire.Tools) != 3 ||
		wire.MaxTokens != nil ||
		wire.MaxCompletionTokens != nil ||
		wire.ParallelToolCalls == nil ||
		*wire.ParallelToolCalls ||
		!wire.Stream {
		t.Fatalf("provider wire = %#v", wire)
	}
	if wire.Tools[0].Function.Name != "exec" ||
		!bytes.Equal(
			wire.Tools[0].Function.Parameters,
			[]byte(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":false}`),
		) ||
		wire.Tools[1].Function.Name != "wait" ||
		wire.Tools[1].Function.Strict == nil ||
		*wire.Tools[1].Function.Strict ||
		wire.Tools[2].Function.Name != "collaboration__send_message" {
		t.Fatalf("provider tools = %#v", wire.Tools)
	}
	for _, code := range []protocolcore.NoticeCode{
		protocolcore.NoticeToolPlacementNormalized,
		protocolcore.NoticeCustomToolGrammarNotForwarded,
		protocolcore.NoticeToolNamespaceEncoded,
		protocolcore.NoticeReasoningEffortDowngraded,
		protocolcore.NoticeDeveloperRoleNormalized,
	} {
		if !reportsHaveNotice(clientReport, backendReport, code) {
			t.Fatalf(
				"reports lack %q: client=%#v backend=%#v",
				code,
				clientReport.Notices(),
				backendReport.Notices(),
			)
		}
	}
}

func TestResponsesRefusalRejectsAndRequiredToolChoiceForwardsExplicitly(
	t *testing.T,
) {
	t.Parallel()

	path, err := NewProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	refusalBody := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{
				"type":"message",
				"role":"assistant",
				"content":[{"type":"refusal","refusal":"Unavailable."}]
			},
			{"type":"message","role":"user","content":"Continue."}
		],
		"store":false,
		"stream":true
	}`)
	refusal, _, err := path.Client().DecodeRequest(refusalBody)
	if err != nil {
		t.Fatalf("DecodeRequest() refusal error = %v", err)
	}
	refusal, err = refusal.WithEffectiveModel("provider-model")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := path.Backend().EncodeRequest(
		refusal,
	); protocolcore.ReasonOf(err) !=
		protocolcore.ReasonUnsupportedClientInput {
		t.Fatalf("EncodeRequest() refusal error = %v", err)
	}

	requiredBody := bytes.Replace(
		fixedRequestFixture(),
		[]byte(`"tool_choice":"auto"`),
		[]byte(`"tool_choice":"required"`),
		1,
	)
	required, _, err := path.Client().DecodeRequest(requiredBody)
	if err != nil {
		t.Fatalf("DecodeRequest() required error = %v", err)
	}
	required, err = required.WithEffectiveModel("provider-model")
	if err != nil {
		t.Fatal(err)
	}
	provider, _, err := path.Backend().EncodeRequest(required)
	if err != nil {
		t.Fatalf("EncodeRequest() required error = %v", err)
	}
	var wire struct {
		ToolChoice string `json:"tool_choice"`
	}
	if err := json.Unmarshal(provider.Body(), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.ToolChoice != "required" {
		t.Fatalf("provider tool_choice = %q", wire.ToolChoice)
	}
}

func TestCompleteChatToolCallRoundTripsAsResponsesCustomTool(
	t *testing.T,
) {
	t.Parallel()

	path, err := NewProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := path.Client().DecodeRequest(fixedRequestFixture())
	if err != nil {
		t.Fatal(err)
	}
	request, err = request.WithEffectiveModel("provider-model")
	if err != nil {
		t.Fatal(err)
	}
	providerBody := []byte(`{
		"id":"chatcmpl-1",
		"object":"chat.completion",
		"created":1785470000,
		"model":"provider-model-revision",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"content":null,
				"tool_calls":[{
					"id":"provider-call-1",
					"type":"function",
					"function":{"name":"exec","arguments":"{\"input\":\"pwd\"}"}
				}]
			},
			"finish_reason":"tool_calls"
		}],
		"usage":{
			"prompt_tokens":10,
			"completion_tokens":3,
			"total_tokens":13,
			"prompt_tokens_details":{"cached_tokens":2},
			"completion_tokens_details":{"reasoning_tokens":1}
		}
	}`)
	decoded, _, err := path.Backend().DecodeResponse(request, providerBody)
	if err != nil {
		t.Fatalf("DecodeResponse() error = %v", err)
	}
	call := decoded.Blocks[0].ToolCall
	if call.EffectiveKind() != protocolcore.ToolKindCustom ||
		call.Name != "exec" ||
		call.Input != "pwd" ||
		call.Key.WireID() != "provider-call-1" {
		t.Fatalf("decoded call = %#v", call)
	}
	clientBody, _, err := path.Client().EncodeResponse(request, decoded)
	if err != nil {
		t.Fatalf("EncodeResponse() error = %v", err)
	}
	var oracle responses.Response
	if err := json.Unmarshal(clientBody, &oracle); err != nil {
		t.Fatalf("official SDK rejected response: %v", err)
	}
	custom := oracle.Output[0].AsCustomToolCall()
	if custom.Name != "exec" ||
		custom.Input != "pwd" ||
		custom.CallID == "provider-call-1" ||
		custom.ID == custom.CallID {
		t.Fatalf("encoded custom tool = %#v", custom)
	}
}

func TestResponsesToolHistoryMapsCallIdentityWithoutCollapsingItemIdentity(
	t *testing.T,
) {
	t.Parallel()

	path, err := NewProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	requestBody := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{"type":"message","role":"user","content":"Continue."},
			{
				"type":"function_call",
				"id":"item-function-1",
				"call_id":"call-function-1",
				"name":"read_file",
				"arguments":"{\"path\":\"README.md\"}",
				"status":"completed"
			},
			{
				"type":"function_call_output",
				"call_id":"call-function-1",
				"output":"read"
			},
			{
				"type":"custom_tool_call",
				"id":"item-custom-1",
				"call_id":"call-custom-1",
				"name":"exec",
				"input":"pwd"
			},
			{
				"type":"custom_tool_call_output",
				"call_id":"call-custom-1",
				"output":"/tmp"
			},
			{
				"type":"function_call",
				"id":"item-namespace-1",
				"call_id":"call-namespace-1",
				"namespace":"collaboration",
				"name":"send_message",
				"arguments":"{\"target\":\"worker\"}",
				"status":"completed"
			},
			{
				"type":"function_call_output",
				"call_id":"call-namespace-1",
				"output":"sent"
			}
		],
		"tools":[
			{
				"type":"function",
				"name":"read_file",
				"parameters":{
					"type":"object",
					"properties":{"path":{"type":"string"}},
					"required":["path"],
					"additionalProperties":false
				}
			},
			{"type":"custom","name":"exec","format":{"type":"text"}},
			{
				"type":"namespace",
				"name":"collaboration",
				"description":"Agent collaboration.",
				"tools":[{
					"type":"function",
					"name":"send_message",
					"parameters":{
						"type":"object",
						"properties":{"target":{"type":"string"}},
						"required":["target"],
						"additionalProperties":false
					}
				}]
			}
		],
		"tool_choice":"auto",
		"parallel_tool_calls":false,
		"store":false,
		"stream":true
	}`)
	request, _, err := path.Client().DecodeRequest(requestBody)
	if err != nil {
		t.Fatalf("DecodeRequest() error = %v", err)
	}
	request, err = request.WithEffectiveModel("provider-model")
	if err != nil {
		t.Fatal(err)
	}
	provider, report, err := path.Backend().EncodeRequest(request)
	if err != nil {
		t.Fatalf("EncodeRequest() error = %v", err)
	}
	var wire struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(provider.Body(), &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Messages) != 7 {
		t.Fatalf("provider message count = %d, want 7", len(wire.Messages))
	}
	wantCalls := []struct {
		messageIndex int
		callID       string
		itemID       string
		name         string
		arguments    string
		output       string
	}{
		{1, "call-function-1", "item-function-1", "read_file", `{"path":"README.md"}`, "read"},
		{3, "call-custom-1", "item-custom-1", "exec", `{"input":"pwd"}`, "/tmp"},
		{5, "call-namespace-1", "item-namespace-1", "collaboration__send_message", `{"target":"worker"}`, "sent"},
	}
	for _, want := range wantCalls {
		callMessage := wire.Messages[want.messageIndex]
		outputMessage := wire.Messages[want.messageIndex+1]
		if callMessage.Role != "assistant" ||
			len(callMessage.ToolCalls) != 1 ||
			callMessage.ToolCalls[0].ID != want.callID ||
			callMessage.ToolCalls[0].ID == want.itemID ||
			callMessage.ToolCalls[0].Function.Name != want.name ||
			callMessage.ToolCalls[0].Function.Arguments != want.arguments ||
			outputMessage.Role != "tool" ||
			outputMessage.ToolCallID != want.callID ||
			outputMessage.Content != want.output {
			t.Fatalf(
				"history messages %d/%d = %#v / %#v",
				want.messageIndex,
				want.messageIndex+1,
				callMessage,
				outputMessage,
			)
		}
	}
	for _, code := range []protocolcore.NoticeCode{
		protocolcore.NoticeToolItemIdentityNotForwarded,
		protocolcore.NoticeCustomToolKindEncoded,
		protocolcore.NoticeToolNamespaceEncoded,
	} {
		if !reportsHaveNotice(report, protocolcore.TranslationReport{}, code) {
			t.Fatalf("translation report lacks %q: %#v", code, report.Notices())
		}
	}
}

func TestUnknownProviderToolNameFailsClosedForCompleteAndStream(
	t *testing.T,
) {
	t.Parallel()

	path, err := NewProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := path.Client().DecodeRequest(fixedRequestFixture())
	if err != nil {
		t.Fatal(err)
	}
	request, err = request.WithEffectiveModel("provider-model")
	if err != nil {
		t.Fatal(err)
	}
	complete := []byte(`{
		"id":"chatcmpl-unknown",
		"object":"chat.completion",
		"created":1785470000,
		"model":"provider-model-revision",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"content":null,
				"tool_calls":[{
					"id":"provider-call-unknown",
					"type":"function",
					"function":{"name":"unknown_tool","arguments":"{}"}
				}]
			},
			"finish_reason":"tool_calls"
		}],
		"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
	}`)
	if _, _, err := path.Backend().DecodeResponse(
		request,
		complete,
	); protocolcore.ReasonOf(err) !=
		protocolcore.ReasonUnsupportedProviderData {
		t.Fatalf("DecodeResponse() error = %v", err)
	}

	stream, err := path.Streaming().NewStream(request)
	if err != nil {
		t.Fatal(err)
	}
	wire := providerEvents(t,
		`{"id":"chatcmpl-unknown","object":"chat.completion.chunk","created":1785470000,"model":"provider-model-revision","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"provider-call-unknown","type":"function","function":{"name":"unknown_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"id":"chatcmpl-unknown","object":"chat.completion.chunk","created":1785470000,"model":"provider-model-revision","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
	)
	safe, err := stream.Feed(context.Background(), wire)
	if err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	if bytes.Contains(safe, []byte("unknown_tool")) ||
		bytes.Contains(safe, []byte("provider-call-unknown")) {
		t.Fatalf("unknown tool escaped before validation: %s", safe)
	}
	if _, err := stream.FinishDecoded(
		context.Background(),
	); protocolcore.ReasonOf(err) !=
		protocolcore.ReasonToolCallIncomplete {
		t.Fatalf("FinishDecoded() error = %v", err)
	}
}

func TestStreamingChatDecoderFeedsResponsesEncoderIncrementally(
	t *testing.T,
) {
	t.Parallel()

	path, err := NewProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := path.Client().DecodeRequest(fixedRequestFixture())
	if err != nil {
		t.Fatal(err)
	}
	request, err = request.WithEffectiveModel("provider-model")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := path.Streaming().NewStream(request)
	if err != nil {
		t.Fatal(err)
	}
	wire := providerEvents(t,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1785470000,"model":"provider-model-revision","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello "},"finish_reason":null}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1785470000,"model":"provider-model-revision","choices":[{"index":0,"delta":{"content":"world"},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1785470000,"model":"provider-model-revision","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
	)
	safe, err := stream.Feed(context.Background(), wire)
	if err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	if !bytes.Contains(safe, []byte(`"delta":"Hello "`)) ||
		!bytes.Contains(safe, []byte(`"delta":"world"`)) ||
		bytes.Contains(safe, []byte(`response.completed`)) {
		t.Fatalf("safe stream = %s", safe)
	}
	terminal, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatalf("FinishDecoded() error = %v", err)
	}
	if len(terminal.ToolIntents()) != 0 {
		t.Fatalf("tool intents = %#v", terminal.ToolIntents())
	}
	release, err := terminal.Approve()
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	combined := append(bytes.Clone(safe), release...)
	events := decodeEventTypes(t, combined)
	if events[len(events)-1] != "response.completed" {
		t.Fatalf("event types = %v", events)
	}
}

func TestStreamingCustomToolRemainsHiddenUntilApproval(t *testing.T) {
	t.Parallel()

	path, err := NewProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := path.Client().DecodeRequest(fixedRequestFixture())
	if err != nil {
		t.Fatal(err)
	}
	request, err = request.WithEffectiveModel("provider-model")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := path.Streaming().NewStream(request)
	if err != nil {
		t.Fatal(err)
	}
	wire := providerEvents(t,
		`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1785470001,"model":"provider-model-revision","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"provider-call-1","type":"function","function":{"name":"exec","arguments":"{\"input\":\""}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1785470001,"model":"provider-model-revision","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1785470001,"model":"provider-model-revision","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`,
	)
	safe, err := stream.Feed(context.Background(), wire)
	if err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	if bytes.Contains(safe, []byte("provider-call-1")) ||
		bytes.Contains(safe, []byte("pwd")) ||
		bytes.Contains(safe, []byte("custom_tool_call")) {
		t.Fatalf("tool semantics escaped before approval: %s", safe)
	}
	terminal, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatalf("FinishDecoded() error = %v", err)
	}
	intents := terminal.ToolIntents()
	if len(intents) != 1 ||
		intents[0].Call.EffectiveKind() != protocolcore.ToolKindCustom ||
		intents[0].Call.Name != "exec" ||
		intents[0].Call.Input != "pwd" {
		t.Fatalf("tool intents = %#v", intents)
	}
	release, err := terminal.Approve()
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if !bytes.Contains(release, []byte("custom_tool_call")) ||
		!bytes.Contains(release, []byte(`"delta":"pwd"`)) ||
		bytes.Contains(release, []byte("provider-call-1")) {
		t.Fatalf("approved release = %s", release)
	}
	events := decodeEventTypes(t, append(bytes.Clone(safe), release...))
	if events[len(events)-1] != "response.completed" {
		t.Fatalf("event types = %v", events)
	}
}

func fixedRequestFixture() []byte {
	return []byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{
				"type":"additional_tools",
				"role":"developer",
				"tools":[
					{
						"type":"custom",
						"name":"exec",
						"description":"Execute one command.",
						"format":{
							"type":"grammar",
							"syntax":"lark",
							"definition":"start: /.+/"
						}
					},
					{
						"type":"function",
						"name":"wait",
						"description":"Wait for completion.",
						"parameters":{
							"type":"object",
							"properties":{"cell_id":{"type":"string"}},
							"required":["cell_id"],
							"additionalProperties":false
						},
						"strict":false
					},
					{
						"type":"namespace",
						"name":"collaboration",
						"description":"Agent collaboration.",
						"tools":[{
							"type":"function",
							"name":"send_message",
							"description":"Send one message.",
							"parameters":{
								"type":"object",
								"properties":{"target":{"type":"string"}},
								"required":["target"],
								"additionalProperties":false
							}
						}]
					}
				]
			},
			{
				"type":"message",
				"role":"developer",
				"content":[{"type":"input_text","text":"Be exact."}]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"Inspect one file."}]
			}
		],
		"tool_choice":"auto",
		"parallel_tool_calls":false,
		"reasoning":{"context":"all_turns","effort":"low"},
		"store":false,
		"stream":true,
		"include":["reasoning.encrypted_content"],
		"text":{"verbosity":"low"}
	}`)
}

func providerEvents(t *testing.T, payloads ...string) []byte {
	t.Helper()
	var encoded bytes.Buffer
	for _, payload := range payloads {
		event, err := ssewire.Encode(ssewire.Event{
			Name: "message",
			Data: []byte(payload),
		})
		if err != nil {
			t.Fatal(err)
		}
		encoded.Write(event)
	}
	done, err := ssewire.Encode(ssewire.Event{
		Name: "message",
		Data: []byte("[DONE]"),
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded.Write(done)
	return encoded.Bytes()
}

func decodeEventTypes(t *testing.T, encoded []byte) []string {
	t.Helper()
	decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	events, err := decoder.Feed(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := decoder.Finish(); err != nil {
		t.Fatal(err)
	}
	types := make([]string, len(events))
	for index, event := range events {
		var oracle responses.ResponseStreamEventUnion
		if err := json.Unmarshal(event.Data, &oracle); err != nil {
			t.Fatalf("official SDK rejected event %d: %v", index, err)
		}
		if event.Name != oracle.Type {
			t.Fatalf(
				"event %d name/type mismatch: %q != %q",
				index,
				event.Name,
				oracle.Type,
			)
		}
		types[index] = oracle.Type
	}
	return types
}

func reportsHaveNotice(
	first protocolcore.TranslationReport,
	second protocolcore.TranslationReport,
	code protocolcore.NoticeCode,
) bool {
	for _, report := range []protocolcore.TranslationReport{first, second} {
		for _, notice := range report.Notices() {
			if notice.Code == code {
				return true
			}
		}
	}
	return false
}
