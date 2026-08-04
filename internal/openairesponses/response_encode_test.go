package openairesponses

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestEncodeCompleteResponseUsesDistinctDeterministicClientIdentities(
	t *testing.T,
) {
	t.Parallel()

	response := completeResponseFixture(t)
	request := streamingRequestFixture(t)
	codec := newTestCodec(t)
	encoded, report, err := codec.EncodeClientResponse(request, response)
	if err != nil {
		t.Fatalf("EncodeClientResponse() error = %v", err)
	}
	if !report.Empty() {
		t.Fatalf("translation report = %#v", report.Notices())
	}
	repeated, repeatedReport, err := codec.EncodeClientResponse(
		request.Clone(),
		response.Clone(),
	)
	if err != nil {
		t.Fatalf("second EncodeClientResponse() error = %v", err)
	}
	if !repeatedReport.Empty() {
		t.Fatalf("repeated translation report = %#v", repeatedReport.Notices())
	}
	if !bytes.Equal(encoded, repeated) {
		t.Fatalf("encoded response is not deterministic:\n%s\n%s", encoded, repeated)
	}
	pristine := bytes.Clone(encoded)
	encoded[0] = '!'
	afterMutation, _, err := codec.EncodeClientResponse(
		request.Clone(),
		response.Clone(),
	)
	if err != nil {
		t.Fatalf("post-mutation EncodeClientResponse() error = %v", err)
	}
	if !bytes.Equal(afterMutation, pristine) {
		t.Fatal("returned response bytes alias codec state")
	}
	encoded = pristine

	var oracle responses.Response
	if err := json.Unmarshal(encoded, &oracle); err != nil {
		t.Fatalf("official OpenAI SDK rejected response: %v\n%s", err, encoded)
	}
	if !strings.HasPrefix(oracle.ID, "resp_") ||
		oracle.ID == response.ID ||
		oracle.CreatedAt != float64(response.CreatedAtUnix) ||
		oracle.Model != "gpt-5.6-sol" ||
		oracle.Status != responses.ResponseStatusCompleted ||
		len(oracle.Output) != 3 {
		t.Fatalf("encoded response = %#v", oracle)
	}
	message := oracle.Output[0].AsMessage()
	if message.Type != "message" ||
		message.Status != responses.ResponseOutputMessageStatusCompleted ||
		len(message.Content) != 1 ||
		message.Content[0].Type != "output_text" ||
		message.Content[0].Text != "Inspecting." {
		t.Fatalf("message item = %#v", message)
	}
	function := oracle.Output[1].AsFunctionCall()
	if !strings.HasPrefix(function.ID, "fc_") ||
		!strings.HasPrefix(function.CallID, "call_") ||
		function.ID == function.CallID ||
		function.CallID == "provider-call-1" ||
		function.Name != "read_file" ||
		function.Arguments != `{"path":"README.md"}` ||
		function.Status != responses.ResponseFunctionToolCallStatusCompleted {
		t.Fatalf("function item = %#v", function)
	}
	custom := oracle.Output[2].AsCustomToolCall()
	if !strings.HasPrefix(custom.ID, "ctc_") ||
		!strings.HasPrefix(custom.CallID, "call_") ||
		custom.ID == custom.CallID ||
		custom.CallID == "provider-call-2" ||
		custom.Name != "exec" ||
		custom.Input != "pwd" {
		t.Fatalf("custom item = %#v", custom)
	}
	if oracle.Usage.InputTokens != 13 ||
		oracle.Usage.InputTokensDetails.CachedTokens != 2 ||
		oracle.Usage.InputTokensDetails.CacheWriteTokens != 1 ||
		oracle.Usage.OutputTokens != 5 ||
		oracle.Usage.OutputTokensDetails.ReasoningTokens != 3 ||
		oracle.Usage.TotalTokens != 18 {
		t.Fatalf("encoded usage = %#v", oracle.Usage)
	}
	if oracle.ParallelToolCalls ||
		len(oracle.Tools) != 2 ||
		oracle.ServiceTier != "" {
		t.Fatalf("request projection = %#v", oracle)
	}
}

func TestEncodeMaxTokenResponseIsIncompleteRatherThanCompleted(t *testing.T) {
	t.Parallel()

	response := completeResponseFixture(t)
	response.Blocks = response.Blocks[:1]
	response.StopReason = protocolcore.StopReasonMaxTokens
	encoded, _, err := newTestCodec(t).EncodeClientResponse(
		streamingRequestFixture(t),
		response,
	)
	if err != nil {
		t.Fatalf("EncodeClientResponse() error = %v", err)
	}
	var oracle responses.Response
	if err := json.Unmarshal(encoded, &oracle); err != nil {
		t.Fatalf("official OpenAI SDK rejected response: %v", err)
	}
	if oracle.Status != responses.ResponseStatusIncomplete ||
		oracle.IncompleteDetails.Reason != "max_output_tokens" {
		t.Fatalf("encoded response = %#v", oracle)
	}
}

func TestEncodeResponseTreatsUnknownReasoningAsOrdinaryOutput(t *testing.T) {
	t.Parallel()

	request := streamingRequestFixture(t)
	response := completeResponseFixture(t)
	response.Blocks = response.Blocks[:1]
	response.StopReason = protocolcore.StopReasonEndTurn
	response.Usage.Reasoning = protocolcore.UsageValue{}
	encoded, report, err := newTestCodec(t).EncodeClientResponse(request, response)
	if err != nil {
		t.Fatalf("EncodeClientResponse() error = %v", err)
	}
	var wire struct {
		Usage struct {
			OutputTokensDetails map[string]json.RawMessage `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	reasoning, exists := wire.Usage.OutputTokensDetails["reasoning_tokens"]
	if !exists || string(reasoning) != "0" {
		t.Fatalf("reasoning_tokens = %s, want 0", reasoning)
	}
	found := false
	for _, notice := range report.Notices() {
		if notice.Code == protocolcore.NoticeReasoningUsageAssumedNonReasoning {
			found = true
		}
	}
	if !found {
		t.Fatalf("translation report = %#v", report.Notices())
	}
}

func TestEncodeResponseReportsObservationOnlyProviderExtensions(t *testing.T) {
	t.Parallel()

	request := streamingRequestFixture(t)
	response := completeResponseFixture(t)
	response.Blocks = response.Blocks[:1]
	response.StopReason = protocolcore.StopReasonEndTurn
	extension, err := protocolcore.NewProviderExtension(
		protocolcore.ProviderExtensionSourceOpenAIChat,
		protocolcore.ProviderExtensionReasoningContent,
		"$.choices[0].message.reasoning_content",
		[][]byte{[]byte(`"opaque-reasoning"`)},
	)
	if err != nil {
		t.Fatal(err)
	}
	response.ProviderExtensions = []protocolcore.ProviderExtension{extension}
	encoded, report, err := newTestCodec(t).EncodeClientResponse(
		request,
		response,
	)
	if err != nil {
		t.Fatalf("EncodeClientResponse() error = %v", err)
	}
	if bytes.Contains(encoded, []byte("opaque-reasoning")) ||
		!reportHasNotice(
			report,
			protocolcore.NoticeReasoningContentNotForwarded,
		) {
		t.Fatalf("encoded=%s report=%#v", encoded, report.Notices())
	}
}

func TestEncodeResponseRejectsToolOutsideRequestCatalog(t *testing.T) {
	t.Parallel()

	request := streamingRequestFixture(t)
	response := completeResponseFixture(t)
	response.Blocks = response.Blocks[1:2]
	response.Blocks[0].ToolCall.Name = "unknown_tool"
	if _, _, err := newTestCodec(t).EncodeClientResponse(
		request,
		response,
	); protocolcore.ReasonOf(err) !=
		protocolcore.ReasonUnsupportedProviderData {
		t.Fatalf("EncodeClientResponse() error = %v", err)
	}
}

func completeResponseFixture(t *testing.T) protocolcore.Response {
	t.Helper()
	text, err := protocolcore.NewTextBlock("Inspecting.")
	if err != nil {
		t.Fatal(err)
	}
	functionKey, err := protocolcore.NewCallKey(
		"openai-chat",
		"provider-call-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	functionArguments, err := protocolcore.NewJSONObject(
		[]byte(`{"path":"README.md"}`),
		1024,
	)
	if err != nil {
		t.Fatal(err)
	}
	function, err := protocolcore.NewToolCallBlock(protocolcore.ToolCall{
		Kind:      protocolcore.ToolKindFunction,
		Key:       functionKey,
		Name:      "read_file",
		Arguments: functionArguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	customKey, err := protocolcore.NewCallKey(
		"openai-chat",
		"provider-call-2",
	)
	if err != nil {
		t.Fatal(err)
	}
	custom, err := protocolcore.NewToolCallBlock(protocolcore.ToolCall{
		Kind:  protocolcore.ToolKindCustom,
		Key:   customKey,
		Name:  "exec",
		Input: "pwd",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := protocolcore.Response{
		ID:             "chatcmpl-provider-1",
		CreatedAtUnix:  1_785_470_000,
		RequestedModel: "gpt-5.6-sol",
		EffectiveModel: "provider-model",
		ReportedModel:  "provider-model-revision",
		Blocks:         []protocolcore.ContentBlock{text, function, custom},
		StopReason:     protocolcore.StopReasonToolUse,
		Usage: protocolcore.Usage{
			InputUncached: knownUsage(10),
			CacheRead:     knownUsage(2),
			CacheWrite:    knownUsage(1),
			Output:        knownUsage(5),
			Reasoning:     knownUsage(3),
		},
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response fixture Validate() error = %v", err)
	}
	return response
}

func knownUsage(tokens int64) protocolcore.UsageValue {
	return protocolcore.UsageValue{
		Tokens: tokens,
		Known:  true,
		Source: "openai-chat",
	}
}
