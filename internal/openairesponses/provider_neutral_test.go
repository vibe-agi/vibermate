package openairesponses_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// terminalBody is a unary Responses body with the given output, status
// members and usage.
func terminalBody(output, status, usage string) []byte {
	if status == "" {
		status = `"status":"completed"`
	}
	body := `{"id":"resp_1","object":"response","created_at":7,"model":"reported-model",` + status + `,"output":[` + output + `]`
	if usage != "" {
		body += `,"usage":` + usage
	}
	return []byte(body + `}`)
}

func decodeUnary(t *testing.T, output, status, usage string) (protocolcore.Response, protocolcore.TranslationReport, error) {
	t.Helper()
	side := newProviderSide(t, nil)
	return side.DecodeResponseFor(streamContext, streamRequest(t, "summarized", false), terminalBody(output, status, usage))
}

func mustDecodeUnary(t *testing.T, output, status, usage string) (protocolcore.Response, []string) {
	t.Helper()
	response, report, err := decodeUnary(t, output, status, usage)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return response, noticeLines(report)
}

func TestNormaliserTurnsEachOutputItemIntoNeutralBlocks(t *testing.T) {
	t.Parallel()

	response, notices := mustDecodeUnary(t,
		`{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"One."},{"type":"summary_text","text":"Two."}],"encrypted_content":"ENC"},`+
			`{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"q"}},`+
			`{"id":"msg_1","type":"message","status":"completed","role":"assistant","phase":"final_answer","content":[`+
			`{"type":"output_text","text":"Hello","annotations":[{"type":"url_citation","url":"https://example.com","start_index":0,"end_index":1,"title":"t"}]},`+
			`{"type":"refusal","refusal":"No."}]},`+
			`{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_9","name":"`+hashedToolName("my.tool")+`","arguments":"{\"a\":1}"}`,
		"", `{"input_tokens":50,"input_tokens_details":{"cached_tokens":10,"cache_write_tokens":5},"output_tokens":20,"output_tokens_details":{"reasoning_tokens":8}}`)

	if len(response.Blocks) != 4 {
		t.Fatalf("blocks = %+v", response.Blocks)
	}
	reasoning := response.Blocks[0].Reasoning
	if response.Blocks[0].Kind != protocolcore.BlockReasoning || reasoning == nil ||
		*reasoning != (protocolcore.Reasoning{
			Text: "One.\n\nTwo.", TextSource: protocolcore.ReasoningTextSummary,
			Seal: protocolcore.ReasoningSeal{
				Format: protocolcore.SealResponsesEncrypted, ItemID: "rs_1", Model: mappedModel, Binding: testBinding, Opaque: "ENC",
			},
		}) {
		t.Fatalf("reasoning = %+v", reasoning)
	}
	if response.Blocks[1].Kind != protocolcore.BlockText || response.Blocks[1].Text != "Hello" ||
		response.Blocks[2].Kind != protocolcore.BlockRefusal || response.Blocks[2].Refusal != "No." {
		t.Fatalf("message blocks = %+v %+v", response.Blocks[1], response.Blocks[2])
	}
	call := response.Blocks[3].ToolCall
	if response.Blocks[3].Kind != protocolcore.BlockToolCall || call.Name != "my.tool" ||
		call.Key.Source() != "openai-responses-call" || call.Key.WireID() != "call_9" ||
		call.ItemKey.WireID() != "fc_1" || string(call.Arguments.Bytes()) != `{"a":1}` {
		t.Fatalf("call = %+v", call)
	}
	if response.ID != "resp_1" || response.CreatedAtUnix != 7 || response.ReportedModel != "reported-model" ||
		response.RequestedModel != "claude-opus-5-5" || response.EffectiveModel != mappedModel ||
		response.StopReason != protocolcore.StopReasonToolUse {
		t.Fatalf("response = %+v", response)
	}
	// 50 input tokens include 10 cached and 5 written.
	if response.Usage.InputUncached.Tokens != 35 || response.Usage.CacheRead.Tokens != 10 ||
		response.Usage.CacheWrite.Tokens != 5 || response.Usage.Output.Tokens != 20 || response.Usage.Reasoning.Tokens != 8 {
		t.Fatalf("usage = %+v", response.Usage)
	}
	if !slices.Equal(notices, []string{
		"hosted_output_not_forwarded $.output[1]",
		"citations_not_forwarded $.output[2].content[0].annotations",
	}) {
		t.Fatalf("notices = %v", notices)
	}
}

func TestNormaliserSealsReasoningForItsTarget(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		item string
		want protocolcore.Reasoning
		fail string
	}{
		{
			name: "raw reasoning text without a summary",
			item: `{"id":"rs_1","type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"a"},{"type":"reasoning_text","text":"b"}],"encrypted_content":"E"}`,
			want: protocolcore.Reasoning{Text: "a\n\nb", TextSource: protocolcore.ReasoningTextContent, Seal: protocolcore.ReasoningSeal{
				Format: protocolcore.SealResponsesEncrypted, ItemID: "rs_1", Model: mappedModel, Binding: testBinding, Opaque: "E",
			}},
		},
		{
			name: "a summary wins over raw text",
			item: `{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"s"}],"content":[{"type":"reasoning_text","text":"r"}]}`,
			want: protocolcore.Reasoning{Text: "s", TextSource: protocolcore.ReasoningTextSummary, Seal: protocolcore.ReasoningSeal{
				Format: protocolcore.SealResponsesEncrypted, ItemID: "rs_1", Model: mappedModel, Binding: testBinding,
			}},
		},
		{
			name: "no encrypted content: a seal with an empty payload",
			item: `{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":null}`,
			want: protocolcore.Reasoning{TextSource: protocolcore.ReasoningTextSummary, Seal: protocolcore.ReasoningSeal{
				Format: protocolcore.SealResponsesEncrypted, ItemID: "rs_1", Model: mappedModel, Binding: testBinding,
			}},
		},
		{
			name: "no id and no state: no seal",
			item: `{"type":"reasoning","summary":[{"type":"summary_text","text":"s"}]}`,
			want: protocolcore.Reasoning{Text: "s", TextSource: protocolcore.ReasoningTextSummary},
		},
		{name: "state without an id", item: `{"type":"reasoning","summary":[],"encrypted_content":"E"}`, fail: "$.output[0].id"},
		{name: "a summary part of another type", item: `{"id":"rs_1","type":"reasoning","summary":[{"type":"reasoning_text","text":"s"}]}`, fail: "$.output[0].summary[0]"},
		{name: "an unknown status", item: `{"id":"rs_1","type":"reasoning","summary":[],"status":"thinking"}`, fail: "$.output[0].status"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			response, _, err := decodeUnary(t, test.item, "", "")
			if test.fail != "" {
				if failurePathOf(err) != test.fail || protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidProviderResponse {
					t.Fatalf("err = %v, want a failure at %s", err, test.fail)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Blocks) != 1 || *response.Blocks[0].Reasoning != test.want {
				t.Fatalf("reasoning = %+v", response.Blocks)
			}
		})
	}
}

func failurePathOf(err error) string {
	var failure *protocolcore.Failure
	if errors.As(err, &failure) {
		return failure.Path
	}
	return ""
}

func TestNormaliserMapsToolNamesBackAndFailsClosed(t *testing.T) {
	t.Parallel()

	call := func(members string) string {
		return `{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1",` + members + `}`
	}
	for _, test := range []struct {
		name   string
		item   string
		reason protocolcore.Reason
		path   string
		client string
	}{
		{name: "a kept name", item: call(`"name":"Bash","arguments":"{}"`), client: "Bash"},
		{name: "a hashed name maps back", item: call(`"name":"` + hashedToolName("my.tool") + `","arguments":"{}"`), client: "my.tool"},
		{name: "the client name of a hashed tool is not a provider name", item: call(`"name":"my.tool","arguments":"{}"`),
			reason: protocolcore.ReasonInvalidProviderResponse, path: "$.output[0].name"},
		{name: "an undeclared tool", item: call(`"name":"Write","arguments":"{}"`),
			reason: protocolcore.ReasonInvalidProviderResponse, path: "$.output[0].name"},
		{name: "a namespaced call", item: call(`"namespace":"fs","name":"Bash","arguments":"{}"`),
			reason: protocolcore.ReasonInvalidProviderResponse, path: "$.output[0].name"},
		{name: "arguments that are not an object", item: call(`"name":"Bash","arguments":"[1]"`),
			reason: protocolcore.ReasonInvalidProviderResponse, path: "$.output[0].arguments"},
		{name: "arguments above 4 MiB", item: call(`"name":"Bash","arguments":"{\"a\":\"` + strings.Repeat("x", protocolcore.MaxToolJSONBytes) + `\"}"`),
			reason: protocolcore.ReasonInvalidProviderResponse, path: "$.output[0].arguments"},
		{name: "an incomplete call", item: `{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"Bash","arguments":"{}"}`,
			reason: protocolcore.ReasonInvalidProviderResponse, path: "$.output[0].status"},
		{name: "a custom tool call", item: `{"id":"ct_1","type":"custom_tool_call","call_id":"call_1","name":"Bash","input":"x"}`,
			reason: protocolcore.ReasonUnsupportedProviderData, path: "$.output[0].type"},
		{name: "a client-run tool search", item: `{"id":"ts_1","type":"tool_search_call","call_id":"call_1","execution":"client","arguments":{}}`,
			reason: protocolcore.ReasonUnsupportedProviderData, path: "$.output[0].type"},
		{name: "an unknown item", item: `{"id":"x_1","type":"local_shell_call","call_id":"call_1","action":{}}`,
			reason: protocolcore.ReasonUnsupportedProviderData, path: "$.output[0].type"},
		{name: "an unknown content part", item: `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_audio","data":"x"}]}`,
			reason: protocolcore.ReasonUnsupportedProviderData, path: "$.output[0].content[0].type"},
		{name: "a message of another role", item: `{"id":"msg_1","type":"message","role":"user","content":[]}`,
			reason: protocolcore.ReasonInvalidProviderResponse, path: "$.output[0]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			response, _, err := decodeUnary(t, test.item, "", "")
			if test.reason != "" {
				if protocolcore.ReasonOf(err) != test.reason || failurePathOf(err) != test.path {
					t.Fatalf("err = %v, want %s at %s", err, test.reason, test.path)
				}
				return
			}
			if err != nil || response.Blocks[0].ToolCall.Name != test.client {
				t.Fatalf("call = %+v, %v", response.Blocks, err)
			}
		})
	}
}

func TestNormaliserDeclaresEveryHostedResult(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{
		"web_search_call", "file_search_call", "image_generation_call", "code_interpreter_call",
		"mcp_call", "mcp_list_tools", "tool_search_output", "compaction", "compaction_summary", "context_compaction",
	} {
		response, notices := mustDecodeUnary(t, `{"id":"h_1","type":"`+kind+`","status":"completed"}`, "", "")
		// Nothing was left to show, so the client gets one empty text block.
		if len(response.Blocks) != 1 || response.Blocks[0].Kind != protocolcore.BlockText || response.Blocks[0].Text != "" ||
			!slices.Equal(notices, []string{"hosted_output_not_forwarded $.output[0]"}) {
			t.Fatalf("%s: blocks %+v, notices %v", kind, response.Blocks, notices)
		}
	}
}

func TestTranslatedStopReasons(t *testing.T) {
	t.Parallel()

	text := `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"x"}]}`
	call := `{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"Bash","arguments":"{}"}`
	for _, test := range []struct {
		name   string
		output string
		status string
		want   protocolcore.StopReason
	}{
		{"completed", text, "", protocolcore.StopReasonEndTurn},
		{"completed with a call", text + "," + call, "", protocolcore.StopReasonToolUse},
		{"output limit", text, `"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}`, protocolcore.StopReasonMaxTokens},
		{"content filter", text, `"status":"incomplete","incomplete_details":{"reason":"content_filter"}`, protocolcore.StopReasonRefusal},
		{"another reason", text, `"status":"incomplete","incomplete_details":{"reason":"interrupted"}`, protocolcore.StopReasonIncomplete},
	} {
		response, _ := mustDecodeUnary(t, test.output, test.status, "")
		if response.StopReason != test.want {
			t.Errorf("%s: stop reason %q, want %q", test.name, response.StopReason, test.want)
		}
	}
	for _, status := range []string{`"status":"incomplete"`, `"status":"queued"`} {
		if _, _, err := decodeUnary(t, text, status, ""); protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidProviderResponse {
			t.Errorf("%s: err = %v", status, err)
		}
	}
}

func TestTranslatedUsageArithmetic(t *testing.T) {
	t.Parallel()

	known := func(tokens int64) protocolcore.UsageValue {
		return protocolcore.UsageValue{Tokens: tokens, Known: true, Source: "openai-responses"}
	}
	text := `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"x"}]}`
	for _, test := range []struct {
		name    string
		usage   string
		want    protocolcore.Usage
		notices []string
	}{
		{
			name:  "OpenAI's worked example: 12000 input, 4000 cached, 7000 written",
			usage: `{"input_tokens":12000,"input_tokens_details":{"cached_tokens":4000,"cache_write_tokens":7000},"output_tokens":300,"output_tokens_details":{"reasoning_tokens":100}}`,
			want:  protocolcore.Usage{InputUncached: known(1000), CacheRead: known(4000), CacheWrite: known(7000), Output: known(300), Reasoning: known(100)},
		},
		{
			name:  "uncached input is floored at zero",
			usage: `{"input_tokens":10,"input_tokens_details":{"cached_tokens":8,"cache_write_tokens":5},"output_tokens":1}`,
			want:  protocolcore.Usage{InputUncached: known(0), CacheRead: known(8), CacheWrite: known(5), Output: known(1)},
		},
		{
			name:    "no cached count: the input is assumed uncached and declared",
			usage:   `{"input_tokens":10,"input_tokens_details":{"cache_write_tokens":3},"output_tokens":1}`,
			want:    protocolcore.Usage{InputUncached: known(7), CacheWrite: known(3), Output: known(1)},
			notices: []string{"cache_read_usage_assumed_uncached $.usage.input_tokens_details.cached_tokens"},
		},
		{name: "no usage gives unknown counts", usage: "", want: protocolcore.Usage{}},
		{name: "null usage gives unknown counts", usage: "null", want: protocolcore.Usage{}},
		{name: "output only", usage: `{"output_tokens":4}`, want: protocolcore.Usage{Output: known(4)}},
	} {
		response, notices := mustDecodeUnary(t, text, "", test.usage)
		if response.Usage != test.want || !slices.Equal(notices, test.notices) {
			t.Errorf("%s: usage %+v notices %v", test.name, response.Usage, notices)
		}
	}
	for _, usage := range []string{
		`{"input_tokens":-1}`,
		`{"input_tokens":1,"input_tokens_details":{"cached_tokens":-1}}`,
		`{"output_tokens":1,"output_tokens_details":{"reasoning_tokens":2}}`,
		`{"input_tokens":"many"}`,
	} {
		if _, _, err := decodeUnary(t, text, "", usage); protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidProviderResponse {
			t.Errorf("%s: err = %v", usage, err)
		}
	}
}

func TestUnaryDecodeGivesAnEmptyAnswerOneTextBlockAndKeepsReasoningOnlyAnswers(t *testing.T) {
	t.Parallel()

	response, _ := mustDecodeUnary(t, "", "", "")
	if len(response.Blocks) != 1 || response.Blocks[0].Kind != protocolcore.BlockText || response.Blocks[0].Text != "" {
		t.Fatalf("empty answer = %+v", response.Blocks)
	}
	response, _ = mustDecodeUnary(t, `{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"E"}`, "", "")
	if len(response.Blocks) != 1 || response.Blocks[0].Kind != protocolcore.BlockReasoning {
		t.Fatalf("reasoning-only answer = %+v", response.Blocks)
	}
	side := newProviderSide(t, nil)
	if _, _, err := side.DecodeResponseFor(streamContext, streamRequest(t, "", false),
		[]byte(`{"id":"resp_1","created_at":1,"model":"m","status":"completed"}`)); protocolcore.ReasonOf(err) != protocolcore.ReasonUnsupportedProviderData {
		t.Fatalf("a terminal without output: %v", err)
	}
}

func TestUnaryFailedResponseIsClassifiedWithoutItsText(t *testing.T) {
	t.Parallel()

	side := newProviderSide(t, nil)
	body := []byte(`{"id":"resp_1","created_at":1,"model":"m","status":"failed","output":[],` +
		`"error":{"code":"invalid_encrypted_content","message":"Item rs_secret could not be verified."}}`)
	_, _, err := side.DecodeResponseFor(streamContext, streamRequest(t, "", false), body)
	var failure *protocolcore.Failure
	if !errors.As(err, &failure) || failure.Reason != protocolcore.ReasonProviderResponseFailed || failure.Path != "$.error" ||
		failure.Provider == nil || *failure.Provider != (protocolcore.ProviderError{
		Class: protocolcore.ProviderErrorReasoningState, Code: "invalid_encrypted_content",
	}) {
		t.Fatalf("err = %v (%+v)", err, failure)
	}
	if failure.NativeError.ForDialect(protocolspec.DialectOpenAIResponses) != nil || strings.Contains(err.Error(), "rs_secret") {
		t.Fatal("the provider's error text was kept")
	}
}

func TestZeroContextDecodeBindsNothing(t *testing.T) {
	t.Parallel()

	side := newProviderSide(t, nil)
	item := `{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"E"}`
	response, _, err := side.DecodeResponse(streamRequest(t, "", false), terminalBody(item, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if seal := response.Blocks[0].Reasoning.Seal; seal.Binding != "" || seal.Model != mappedModel {
		t.Fatalf("seal = %+v", seal)
	}
	bound, _, err := side.DecodeResponseFor(protocolpath.ProviderContext{ReasoningBinding: testBinding}, streamRequest(t, "", false), terminalBody(item, "", ""))
	if err != nil || bound.Blocks[0].Reasoning.Seal.Binding != testBinding {
		t.Fatalf("bound seal = %+v, %v", bound.Blocks, err)
	}
}
