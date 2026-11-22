package openairesponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3/responses"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

// These tests drive the provider stream with the Anthropic client side's
// real stream encoder, as a composed path does, and read the client bytes
// back with anthropic-sdk-go, so each case reads as what Claude Code would
// receive. The Responses events are written as the public API and the
// minimal ChatGPT-style stream send them; each fixture is first checked to
// parse as openai-go's stream event union.

const (
	streamTools = `"tools":[` +
		`{"name":"Bash","description":"Run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}},` +
		`{"name":"my.tool","input_schema":{"type":"object","properties":{}}}]`
	provisionalID = "msg_vm_00000000000000000000000a"
)

var streamContext = protocolpath.ProviderContext{ReasoningBinding: testBinding}

// streamBody is a Claude Code request with two tools, one whose name the API
// refuses.
func streamBody(display string, stream bool) string {
	thinking := `{"type":"adaptive"}`
	if display != "" {
		thinking = `{"type":"adaptive","display":"` + display + `"}`
	}
	return `{"model":"claude-opus-5-5","max_tokens":128000,"stream":` + strconv.FormatBool(stream) +
		`,"thinking":` + thinking + `,` + streamTools + `,"messages":[{"role":"user","content":"list files"}]}`
}

func streamRequest(t testing.TB, display string, stream bool) protocolcore.Request {
	t.Helper()
	return decodeClaude(t, []byte(streamBody(display, stream)))
}

type fakeClock struct {
	now time.Time
}

func (clock *fakeClock) advance(duration time.Duration) { clock.now = clock.now.Add(duration) }

func newStreamSide(t testing.TB) (*openairesponses.ProviderSide, *fakeClock) {
	t.Helper()
	side := newProviderSide(t, nil)
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	openairesponses.SetProviderSideClock(side, func() time.Time { return clock.now })
	openairesponses.SetProviderSideMessageID(side, func() string { return provisionalID })
	return side, clock
}

// sseWire frames event payloads as a Responses service does; "[DONE]" is
// the marker, and an event's name is its payload type.
func sseWire(t testing.TB, payloads ...string) []byte {
	t.Helper()
	var wire bytes.Buffer
	for _, payload := range payloads {
		if payload == "[DONE]" {
			wire.WriteString("data: [DONE]\n\n")
			continue
		}
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(payload), &header); err != nil || header.Type == "" {
			t.Fatalf("fixture event is invalid: %s", payload)
		}
		fmt.Fprintf(&wire, "event: %s\ndata: %s\n\n", header.Type, payload)
	}
	return wire.Bytes()
}

// checkOpenAIEvents holds a fixture to openai-go's event union: every event
// the SDK models must parse under its own type.
func checkOpenAIEvents(t testing.TB, payloads ...string) {
	t.Helper()
	for _, payload := range payloads {
		if payload == "[DONE]" || strings.Contains(payload, `"type":"keepalive"`) {
			continue
		}
		var event responses.ResponseStreamEventUnion
		if err := event.UnmarshalJSON([]byte(payload)); err != nil {
			t.Fatalf("openai-go cannot parse %s: %v", payload, err)
		}
		var header struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(payload), &header)
		if event.Type != header.Type {
			t.Fatalf("openai-go read %q as %q", header.Type, event.Type)
		}
	}
}

// The public-API stream of one turn: a reasoning item with two summary
// parts, a commentary message and a function call to Bash.
func publicStream(model string) []string {
	response := func(status, output, usage string) string {
		return `{"id":"resp_1","object":"response","created_at":1700000000,"status":"` + status + `","model":"` + model +
			`","output":[` + output + `],"usage":` + usage + `}`
	}
	reasoningItem := `{"id":"rs_1","type":"reasoning","encrypted_content":"ENC-1","summary":[{"type":"summary_text","text":"Plan first."},{"type":"summary_text","text":"Then act."}]}`
	messageItem := `{"id":"msg_1","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"Let me check."}],"phase":"commentary","role":"assistant"}`
	callItem := `{"id":"fc_1","type":"function_call","status":"completed","arguments":"{\"command\":\"ls\"}","call_id":"call_1","name":"Bash"}`
	usage := `{"input_tokens":120,"input_tokens_details":{"cached_tokens":100},"output_tokens":40,"output_tokens_details":{"reasoning_tokens":12},"total_tokens":160}`
	summary := func(sequence, index int, kind, members string) string {
		return `{"type":"response.reasoning_summary_` + kind + `","sequence_number":` + strconv.Itoa(sequence) +
			`,"item_id":"rs_1","output_index":0,"summary_index":` + strconv.Itoa(index) + `,` + members + `}`
	}
	text := func(sequence int, kind, members string) string {
		return `{"type":"response.` + kind + `","sequence_number":` + strconv.Itoa(sequence) +
			`,"item_id":"msg_1","output_index":1,"content_index":0,` + members + `}`
	}
	return []string{
		`{"type":"response.created","sequence_number":0,"response":` + response("in_progress", "", "null") + `}`,
		`{"type":"response.in_progress","sequence_number":1,"response":` + response("in_progress", "", "null") + `}`,
		`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
		summary(3, 0, "part.added", `"part":{"type":"summary_text","text":""}`),
		summary(4, 0, "text.delta", `"delta":"Plan ","obfuscation":"ab"`),
		summary(5, 0, "text.delta", `"delta":"first.","obfuscation":"cd"`),
		summary(6, 0, "text.done", `"text":"Plan first."`),
		summary(7, 0, "part.done", `"part":{"type":"summary_text","text":"Plan first."}`),
		summary(8, 1, "part.added", `"part":{"type":"summary_text","text":""}`),
		summary(9, 1, "text.delta", `"delta":"Then act.","obfuscation":"ef"`),
		summary(10, 1, "text.done", `"text":"Then act."`),
		summary(11, 1, "part.done", `"part":{"type":"summary_text","text":"Then act."}`),
		`{"type":"response.output_item.done","sequence_number":12,"output_index":0,"item":` + reasoningItem + `}`,
		`{"type":"response.output_item.added","sequence_number":13,"output_index":1,"item":{"id":"msg_1","type":"message","status":"in_progress","content":[],"role":"assistant"}}`,
		text(14, "content_part.added", `"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}`),
		text(15, "output_text.delta", `"delta":"Let me ","logprobs":[],"obfuscation":"gh"`),
		text(16, "output_text.delta", `"delta":"check.","logprobs":[],"obfuscation":"ij"`),
		text(17, "output_text.done", `"text":"Let me check.","logprobs":[]`),
		text(18, "content_part.done", `"part":{"type":"output_text","annotations":[],"logprobs":[],"text":"Let me check."}`),
		`{"type":"response.output_item.done","sequence_number":19,"output_index":1,"item":` + messageItem + `}`,
		`{"type":"response.output_item.added","sequence_number":20,"output_index":2,"item":{"id":"fc_1","type":"function_call","status":"in_progress","arguments":"","call_id":"call_1","name":"Bash"}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":21,"item_id":"fc_1","output_index":2,"delta":"{\"command\":","obfuscation":"kl"}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":22,"item_id":"fc_1","output_index":2,"delta":"\"ls\"}","obfuscation":"mn"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":23,"item_id":"fc_1","output_index":2,"name":"Bash","arguments":"{\"command\":\"ls\"}"}`,
		`{"type":"response.output_item.done","sequence_number":24,"output_index":2,"item":` + callItem + `}`,
		`{"type":"response.completed","sequence_number":25,"response":` +
			response("completed", reasoningItem+","+messageItem+","+callItem, usage) + `}`,
	}
}

// minimalStream is the ChatGPT-style stream (W/codex-models/...gpt-5.5-
// scripted-reply-0.sse) adapted to a Claude request's tool: only completed
// items and a terminal whose output is empty.
func minimalStream() []string {
	return []string{
		`{"item":{"encrypted_content":"WIRE-ENCRYPTED-REASONING-0001","id":"rs_wire_1","summary":[{"text":"WIRE-REASONING-SUMMARY","type":"summary_text"}],"type":"reasoning"},"output_index":0,"type":"response.output_item.done"}`,
		`{"item":{"content":[{"text":"WIRE-ASSISTANT-PREAMBLE","type":"output_text"}],"id":"msg_wire_1","phase":"commentary","role":"assistant","status":"completed","type":"message"},"output_index":1,"type":"response.output_item.done"}`,
		`{"item":{"arguments":"{\"command\":\"echo wire-fixture\"}","call_id":"call_wire_1","id":"fc_wire_1","name":"Bash","status":"completed","type":"function_call"},"output_index":2,"type":"response.output_item.done"}`,
		`{"response":{"created_at":1,"id":"resp_wire_1","model":"fixture","output":[],"status":"completed","usage":{"input_tokens":11,"input_tokens_details":{"cached_tokens":7},"output_tokens":40,"output_tokens_details":{"reasoning_tokens":9},"total_tokens":51}},"type":"response.completed"}`,
	}
}

type streamResult struct {
	live     []byte
	terminal protocolpath.PendingTerminal
	progress uint64
}

func clientEncoder(t testing.TB, request protocolcore.Request) protocolpath.ClientStreamEncoder {
	t.Helper()
	encoder, err := anthropicchat.NewClientStreamEncoder(request)
	if err != nil {
		t.Fatal(err)
	}
	return encoder
}

// feed runs one stream over the fragments and finishes it.
func feed(t testing.TB, side *openairesponses.ProviderSide, request protocolcore.Request, fragments ...[]byte) (streamResult, error) {
	t.Helper()
	stream, err := side.NewProviderStream(streamContext, request, clientEncoder(t, request))
	if err != nil {
		t.Fatal(err)
	}
	var result streamResult
	for _, fragment := range fragments {
		out, err := stream.Feed(context.Background(), fragment)
		result.live = append(result.live, out...)
		if err != nil {
			return result, err
		}
	}
	result.progress = stream.SemanticProgress()
	result.terminal, err = stream.FinishDecoded(context.Background())
	return result, err
}

func mustFeed(t testing.TB, side *openairesponses.ProviderSide, request protocolcore.Request, fragments ...[]byte) streamResult {
	t.Helper()
	result, err := feed(t, side, request, fragments...)
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	return result
}

// approved is everything the client receives once the tool decision allows
// the held bytes.
func approved(t testing.TB, result streamResult) []byte {
	t.Helper()
	release, err := result.terminal.Approve()
	if err != nil {
		t.Fatal(err)
	}
	return append(slices.Clone(result.live), release...)
}

// everySplit feeds the wire whole, byte by byte, and split in two at every
// byte, and requires the same client bytes, decoded response, intents and
// report from each.
func everySplit(t *testing.T, side *openairesponses.ProviderSide, request protocolcore.Request, wire []byte) streamResult {
	t.Helper()
	whole := mustFeed(t, side, request, wire)
	wantResponse := whole.terminal.DecodedResponse()
	wantIntents := whole.terminal.ToolIntents()
	wantReport := whole.terminal.TranslationReport()
	wantBytes := approved(t, whole)
	whole = mustFeed(t, side, request, wire)
	check := func(label string, fragments ...[]byte) {
		t.Helper()
		result := mustFeed(t, side, request, fragments...)
		if !reflect.DeepEqual(result.terminal.DecodedResponse(), wantResponse) ||
			!reflect.DeepEqual(result.terminal.ToolIntents(), wantIntents) ||
			!reflect.DeepEqual(result.terminal.TranslationReport(), wantReport) {
			t.Fatalf("%s: the decoded terminal differs", label)
		}
		if got := approved(t, result); !bytes.Equal(got, wantBytes) {
			t.Fatalf("%s: client bytes differ:\n%s\nwant\n%s", label, got, wantBytes)
		}
	}
	bytewise := make([][]byte, len(wire))
	for index := range wire {
		bytewise[index] = wire[index : index+1]
	}
	check("byte by byte", bytewise...)
	for index := 1; index < len(wire); index++ {
		check(fmt.Sprintf("split at %d", index), wire[:index], wire[index:])
	}
	return whole
}

type clientEvent struct {
	name string
	data []byte
}

func clientEvents(t testing.TB, stream []byte) []clientEvent {
	t.Helper()
	decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	events, err := decoder.Feed(stream)
	if err != nil {
		t.Fatal(err)
	}
	if err := decoder.Finish(); err != nil {
		t.Fatal(err)
	}
	result := make([]clientEvent, len(events))
	for index, event := range events {
		result[index] = clientEvent{name: event.Name, data: event.Data}
	}
	return result
}

// accumulate reads client bytes as anthropic-sdk-go hands them to Claude
// Code.
func accumulate(t testing.TB, stream []byte) anthropic.Message {
	t.Helper()
	var message anthropic.Message
	for _, event := range clientEvents(t, stream) {
		var union anthropic.MessageStreamEventUnion
		if err := union.UnmarshalJSON(event.data); err != nil {
			t.Fatalf("anthropic-sdk-go cannot parse %s: %v", event.data, err)
		}
		if union.Type != event.name {
			t.Fatalf("event %q carries type %q", event.name, union.Type)
		}
		if err := message.Accumulate(union); err != nil {
			t.Fatalf("anthropic-sdk-go cannot accumulate %s: %v", event.data, err)
		}
	}
	return message
}

func eventNames(t testing.TB, stream []byte) []string {
	t.Helper()
	var names []string
	for _, event := range clientEvents(t, stream) {
		name := event.name
		var block struct {
			ContentBlock struct {
				Type string `json:"type"`
			} `json:"content_block"`
			Delta struct {
				Type string `json:"type"`
			} `json:"delta"`
		}
		_ = json.Unmarshal(event.data, &block)
		switch {
		case block.ContentBlock.Type != "":
			name += ":" + block.ContentBlock.Type
		case block.Delta.Type != "":
			name += ":" + block.Delta.Type
		}
		names = append(names, name)
	}
	return names
}

func TestPublicStreamReachesClaudeCodeWithItsCallHeld(t *testing.T) {
	t.Parallel()

	payloads := publicStream(mappedModel)
	checkOpenAIEvents(t, payloads...)
	side, _ := newStreamSide(t)
	request := streamRequest(t, "summarized", true)
	result := everySplit(t, side, request, sseWire(t, payloads...))

	// Reasoning and text stream live; the call and the terminal wait for the
	// tool decision.
	if got, want := eventNames(t, result.live), []string{
		"message_start",
		"content_block_start:thinking", "content_block_delta:thinking_delta", "content_block_delta:thinking_delta",
		"content_block_delta:thinking_delta", "content_block_delta:thinking_delta",
		"content_block_delta:signature_delta", "content_block_stop",
		"content_block_start:text", "content_block_delta:text_delta", "content_block_delta:text_delta", "content_block_stop",
	}; !slices.Equal(got, want) {
		t.Fatalf("live events = %v\nwant %v", got, want)
	}
	message := accumulate(t, approved(t, result))
	if len(message.Content) != 3 {
		t.Fatalf("message has %d blocks", len(message.Content))
	}
	thinking, text, call := message.Content[0], message.Content[1], message.Content[2]
	if thinking.Type != "thinking" || thinking.Thinking != "Plan first.\n\nThen act." {
		t.Fatalf("thinking = %q %q", thinking.Type, thinking.Thinking)
	}
	seal, source, ok := protocolcore.ParseReasoningSeal(thinking.Signature)
	if !ok || source != "" || seal != (protocolcore.ReasoningSeal{
		Format: protocolcore.SealResponsesEncrypted, ItemID: "rs_1", Model: mappedModel, Binding: testBinding, Opaque: "ENC-1",
	}) {
		t.Fatalf("signature %q is not the item's seal: %+v %v", thinking.Signature, seal, ok)
	}
	if text.Type != "text" || text.Text != "Let me check." {
		t.Fatalf("text = %+v", text)
	}
	if call.Type != "tool_use" || call.ID != "call_1" || call.Name != "Bash" || string(call.Input) != `{"command":"ls"}` {
		t.Fatalf("tool_use = %q %q %q %s", call.Type, call.ID, call.Name, call.Input)
	}
	if message.Model != "claude-opus-5-5" || message.ID != "resp_1" || message.StopReason != "tool_use" {
		t.Fatalf("message = %q %q %q", message.Model, message.ID, message.StopReason)
	}
	// 120 input tokens of which 100 were cached; Claude Code's input_tokens
	// excludes cached tokens.
	if message.Usage.InputTokens != 20 || message.Usage.CacheReadInputTokens != 100 ||
		message.Usage.CacheCreationInputTokens != 0 || message.Usage.OutputTokens != 40 {
		t.Fatalf("usage = %+v", message.Usage)
	}

	terminal := mustFeed(t, side, request, sseWire(t, payloads...)).terminal
	intents := terminal.ToolIntents()
	if len(intents) != 1 || intents[0].ResponseID != "resp_1" || intents[0].Call.Name != "Bash" ||
		intents[0].Call.Key.WireID() != "call_1" || intents[0].Call.ItemKey.WireID() != "fc_1" {
		t.Fatalf("intents = %+v", intents)
	}
	decoded := terminal.DecodedResponse()
	if decoded.ID != "resp_1" || decoded.ReportedModel != mappedModel || decoded.StopReason != protocolcore.StopReasonToolUse ||
		decoded.Usage.Reasoning.Tokens != 12 || len(decoded.Blocks) != 3 {
		t.Fatalf("decoded = %+v", decoded)
	}
	// The 12 reasoning tokens stay in output_tokens; Anthropic has no field
	// for them, which the client encoder declares (design 7.6).
	if got := noticeLines(terminal.TranslationReport()); !slices.Equal(got, []string{"reasoning_usage_not_forwarded $.usage"}) {
		t.Fatalf("report = %v", got)
	}
	if err := terminal.Reject(); err != nil {
		t.Fatal(err)
	}
	if release, err := terminal.Approve(); err == nil || len(release) != 0 {
		t.Fatal("a rejected call became releasable")
	}
}

func TestMinimalStreamStartsProvisionallyAndTakesItsIDFromTheTerminal(t *testing.T) {
	t.Parallel()

	payloads := minimalStream()
	checkOpenAIEvents(t, payloads...)
	side, _ := newStreamSide(t)
	request := streamRequest(t, "summarized", true)
	result := everySplit(t, side, request, sseWire(t, payloads...))
	message := accumulate(t, approved(t, result))
	if message.ID != provisionalID {
		t.Fatalf("client message id = %q", message.ID)
	}
	if len(message.Content) != 3 || message.Content[0].Thinking != "WIRE-REASONING-SUMMARY" ||
		message.Content[1].Text != "WIRE-ASSISTANT-PREAMBLE" || message.Content[2].Name != "Bash" {
		t.Fatalf("content = %+v", message.Content)
	}
	// Everything before the call streamed live, the reasoning block opened
	// by its completed item alone.
	if got := eventNames(t, result.live); !slices.Equal(got, []string{
		"message_start",
		"content_block_start:thinking", "content_block_delta:thinking_delta", "content_block_delta:signature_delta", "content_block_stop",
		"content_block_start:text", "content_block_delta:text_delta", "content_block_stop",
	}) {
		t.Fatalf("live events = %v", got)
	}
	terminal := mustFeed(t, side, request, sseWire(t, payloads...)).terminal
	if intents := terminal.ToolIntents(); len(intents) != 1 || intents[0].ResponseID != "resp_wire_1" {
		t.Fatalf("intents = %+v", intents)
	}
	if decoded := terminal.DecodedResponse(); decoded.ID != "resp_wire_1" || decoded.Usage.InputUncached.Tokens != 4 {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestStreamFailsClosedOnInconsistentEvents(t *testing.T) {
	t.Parallel()

	public := publicStream(mappedModel)
	replace := func(index int, payload string) []string {
		payloads := slices.Clone(public)
		payloads[index] = payload
		return payloads
	}
	for _, test := range []struct {
		name     string
		payloads []string
		reason   protocolcore.Reason
	}{
		{
			name:     "a text delta that disagrees with the completed item",
			payloads: replace(16, `{"type":"response.output_text.delta","sequence_number":16,"item_id":"msg_1","output_index":1,"content_index":0,"delta":"chase."}`),
			reason:   protocolcore.ReasonStreamStateViolation,
		},
		{
			name:     "a reasoning delta that disagrees with the completed item",
			payloads: replace(5, `{"type":"response.reasoning_summary_text.delta","sequence_number":5,"item_id":"rs_1","output_index":0,"summary_index":0,"delta":"last."}`),
			reason:   protocolcore.ReasonStreamStateViolation,
		},
		{
			name:     "a terminal that names another response",
			payloads: replace(0, `{"type":"response.created","sequence_number":0,"response":{"id":"resp_other","created_at":1,"status":"in_progress","model":"m","output":[]}}`),
			reason:   protocolcore.ReasonStreamStateViolation,
		},
		{
			name:     "a terminal that drops the streamed call",
			payloads: replace(len(public)-1, strings.Replace(public[len(public)-1], `,{"id":"fc_1"`, `]`+`,"x":[{"id":"fc_1"`, 1)),
			reason:   protocolcore.ReasonToolCallIncomplete,
		},
		{
			name:     "a call to a tool the request did not declare",
			payloads: replace(24, strings.Replace(public[24], `"name":"Bash"`, `"name":"Write"`, 1)),
			reason:   protocolcore.ReasonInvalidProviderResponse,
		},
		{
			name:     "a custom tool call",
			payloads: replace(20, `{"type":"response.output_item.added","sequence_number":20,"output_index":2,"item":{"id":"ct_1","type":"custom_tool_call","call_id":"call_1","name":"Bash","input":""}}`),
			reason:   protocolcore.ReasonUnsupportedProviderData,
		},
		{
			name:     "an item type the client cannot be given",
			payloads: replace(20, `{"type":"response.output_item.added","sequence_number":20,"output_index":2,"item":{"id":"ap_1","type":"mcp_approval_request","server_label":"s","name":"n","arguments":"{}"}}`),
			reason:   protocolcore.ReasonUnsupportedProviderData,
		},
		{
			name:     "an event after the terminal",
			payloads: append(slices.Clone(public), `{"type":"response.in_progress","response":{"id":"resp_1"}}`),
			reason:   protocolcore.ReasonStreamStateViolation,
		},
		{
			name:     "a done marker before the terminal",
			payloads: append(slices.Clone(public[:len(public)-1]), "[DONE]"),
			reason:   protocolcore.ReasonStreamStateViolation,
		},
		{
			name:     "a second response.created",
			payloads: append([]string{public[0]}, public...),
			reason:   protocolcore.ReasonStreamStateViolation,
		},
		{
			name:     "an output item completed twice",
			payloads: replace(13, public[12]),
			reason:   protocolcore.ReasonStreamStateViolation,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			side, _ := newStreamSide(t)
			result, err := feed(t, side, streamRequest(t, "summarized", true), sseWire(t, test.payloads...))
			if protocolcore.ReasonOf(err) != test.reason {
				t.Fatalf("err = %v, want %s", err, test.reason)
			}
			if bytes.Contains(result.live, []byte(`"tool_use"`)) {
				t.Fatal("a held call reached the client")
			}
		})
	}
}

// The framing checks this driver shares with the native relay (design 7.1):
// no duplicated member, an SSE event name that matches its payload, and a
// bound on the wire bytes of the whole stream. Each case would otherwise
// stream to a clean terminal, since the decoded value it carries is valid.
func TestStreamFramingFailsClosed(t *testing.T) {
	t.Parallel()

	public := publicStream(mappedModel)
	replaced := func(index int, old, new string) [][]byte {
		payloads := slices.Clone(public)
		if !strings.Contains(payloads[index], old) {
			t.Fatalf("fixture event %d has no %s", index, old)
		}
		payloads[index] = strings.Replace(payloads[index], old, new, 1)
		return [][]byte{sseWire(t, payloads...)}
	}
	// The SSE comments carry no event and spend no JSON budget; only the
	// stream's wire bound counts them.
	comment := []byte(": " + strings.Repeat("x", 1<<20) + "\n")
	padded := [][]byte{sseWire(t, public[:len(public)-1]...)}
	for range 17 {
		padded = append(padded, comment)
	}
	padded = append(padded, sseWire(t, public[len(public)-1]))
	for _, test := range []struct {
		name      string
		fragments [][]byte
		reason    protocolcore.Reason
	}{
		{
			// The last duplicate is the valid value, so reading it would pass.
			name:      "a duplicated root member",
			fragments: replaced(0, `"response":{`, `"response":{"id":"resp_other"},"response":{`),
			reason:    protocolcore.ReasonMalformedEventStream,
		},
		{
			name:      "a duplicated nested member",
			fragments: replaced(20, `"name":"Bash"`, `"name":"Write","name":"Bash"`),
			reason:    protocolcore.ReasonMalformedEventStream,
		},
		{
			name: "an event name that differs from its payload type",
			fragments: [][]byte{bytes.Replace(sseWire(t, public...),
				[]byte("event: response.in_progress\n"), []byte("event: response.queued\n"), 1)},
			reason: protocolcore.ReasonMalformedEventStream,
		},
		{
			name:      "fragments that together exceed the response bound",
			fragments: padded,
			reason:    protocolcore.ReasonStreamLimitExceeded,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			side, _ := newStreamSide(t)
			result, err := feed(t, side, streamRequest(t, "summarized", true), test.fragments...)
			if protocolcore.ReasonOf(err) != test.reason {
				t.Fatalf("err = %v, want %s", err, test.reason)
			}
			if bytes.Contains(result.live, []byte(`"tool_use"`)) {
				t.Fatal("a held call reached the client")
			}
		})
	}
}

func TestStreamDoneMarkerAfterTheTerminalIsAccepted(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	request := streamRequest(t, "summarized", true)
	withDone := mustFeed(t, side, request, sseWire(t, append(publicStream(mappedModel), "[DONE]")...))
	without := mustFeed(t, side, request, sseWire(t, publicStream(mappedModel)...))
	if !bytes.Equal(approved(t, withDone), approved(t, without)) {
		t.Fatal("the done marker changed the client bytes")
	}
}

func TestCallCompletingWhileTextIsOpenWaitsForTheTextAndIsHeld(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	request := streamRequest(t, "summarized", true)
	call := `{"id":"fc_1","type":"function_call","status":"completed","arguments":"{}","call_id":"call_1","name":"` + hashedToolName("my.tool") + `"}`
	message := `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello there"}]}`
	payloads := []string{
		`{"type":"response.created","response":{"id":"resp_1","created_at":1,"model":"m","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hello"}`,
		`{"type":"response.output_item.done","output_index":1,"item":` + call + `}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":" there"}`,
		`{"type":"response.output_item.done","output_index":0,"item":` + message + `}`,
		`{"type":"response.completed","response":{"id":"resp_1","created_at":1,"model":"m","status":"completed","output":[` + message + `,` + call + `]}}`,
	}
	result := everySplit(t, side, request, sseWire(t, payloads...))
	if got := eventNames(t, result.live); !slices.Equal(got, []string{
		"message_start", "content_block_start:text", "content_block_delta:text_delta",
	}) {
		t.Fatalf("live events = %v", got)
	}
	message2 := accumulate(t, approved(t, result))
	if len(message2.Content) != 2 || message2.Content[0].Text != "Hello there" || message2.Content[1].Name != "my.tool" {
		t.Fatalf("content = %+v", message2.Content)
	}
}

// The barrier arms at a call's output_item.added (design 7.2), before any of
// its arguments: text that arrives between the call's announcement and its
// first argument event already waits for the tool decision.
func TestCallAddedWhileTextIsOpenArmsTheBarrier(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	request := streamRequest(t, "summarized", true)
	call := `{"id":"fc_1","type":"function_call","status":"completed","arguments":"{}","call_id":"call_1","name":"Bash"}`
	message := `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello there"}]}`
	payloads := []string{
		`{"type":"response.created","response":{"id":"resp_1","created_at":1,"model":"m","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hello"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","status":"in_progress","arguments":"","call_id":"call_1","name":"Bash"}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":" there"}`,
		`{"type":"response.output_item.done","output_index":0,"item":` + message + `}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":` + call + `}`,
		`{"type":"response.completed","response":{"id":"resp_1","created_at":1,"model":"m","status":"completed","output":[` + message + `,` + call + `]}}`,
	}
	checkOpenAIEvents(t, payloads...)
	result := everySplit(t, side, request, sseWire(t, payloads...))
	if got := eventNames(t, result.live); !slices.Equal(got, []string{
		"message_start", "content_block_start:text", "content_block_delta:text_delta",
	}) {
		t.Fatalf("live events = %v", got)
	}
	message2 := accumulate(t, approved(t, result))
	if len(message2.Content) != 2 || message2.Content[0].Text != "Hello there" || message2.Content[1].Name != "Bash" ||
		message2.StopReason != "tool_use" {
		t.Fatalf("content = %+v", message2.Content)
	}
}

func TestStreamProgressCountsEveryEventAndIgnoresUnknownOnes(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	request := streamRequest(t, "summarized", true)
	stream, err := side.NewProviderStream(streamContext, request, clientEncoder(t, request))
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{
		`{"type":"keepalive"}`,
		`{"type":"response.metadata","metadata":{"a":"b"}}`,
		`{"type":"codex.rate_limits","rate_limits":{}}`,
		`{"type":"response.web_search_call.searching","output_index":0,"item_id":"ws_1"}`,
		`{"type":"response.something_new","output_index":0}`,
	} {
		before := stream.SemanticProgress()
		out, err := stream.Feed(context.Background(), sseWire(t, payload))
		if err != nil || len(out) != 0 || stream.SemanticProgress() != before+1 {
			t.Fatalf("%s: out=%q err=%v progress %d -> %d", payload, out, err, before, stream.SemanticProgress())
		}
	}
}

func TestStreamHostedResultsAreDroppedAndDeclared(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	request := streamRequest(t, "summarized", true)
	search := `{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"q"}}`
	message := `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Found it.","annotations":[{"type":"url_citation","start_index":0,"end_index":5,"url":"https://example.com","title":"t"}]}]}`
	payloads := []string{
		`{"type":"response.created","response":{"id":"resp_1","created_at":1,"model":"m","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"in_progress"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":` + search + `}`,
		`{"type":"response.output_text.annotation.added","item_id":"msg_1","output_index":1,"content_index":0,"annotation_index":0,"annotation":{"type":"url_citation","start_index":0,"end_index":5,"url":"https://example.com","title":"t"}}`,
		`{"type":"response.output_item.done","output_index":1,"item":` + message + `}`,
		`{"type":"response.completed","response":{"id":"resp_1","created_at":1,"model":"m","status":"completed","output":[` + search + `,` + message + `]}}`,
	}
	result := mustFeed(t, side, request, sseWire(t, payloads...))
	// The stream and the terminal snapshot declare each loss once.
	if got := noticeLines(result.terminal.TranslationReport()); !slices.Equal(got, []string{
		"hosted_output_not_forwarded $.output[0]",
		"citations_not_forwarded $.output[1].content[0].annotations",
	}) {
		t.Fatalf("notices = %v", got)
	}
	message2 := accumulate(t, approved(t, result))
	if len(message2.Content) != 1 || message2.Content[0].Text != "Found it." || message2.StopReason != "end_turn" {
		t.Fatalf("message = %+v", message2)
	}
	if bytes.Contains(result.live, []byte("ws_1")) || bytes.Contains(result.live, []byte("example.com")) {
		t.Fatal("hosted output reached the client")
	}
}

func TestStreamErrorNotificationIsTheFailureOnlyWithoutATerminal(t *testing.T) {
	t.Parallel()

	notification := `{"type":"error","code":"rate_limit_exceeded","message":"Slow down, the limit is secret-org-123","param":null,"sequence_number":3}`
	public := publicStream(mappedModel)
	side, _ := newStreamSide(t)
	request := streamRequest(t, "summarized", true)

	// error then a terminal: the terminal wins.
	withTerminal := append(slices.Clone(public[:4]), append([]string{notification}, public[4:]...)...)
	mustFeed(t, side, request, sseWire(t, withTerminal...))

	// error then EOF: the notification is the classified failure.
	result, err := feed(t, side, request, sseWire(t, append(slices.Clone(public[:4]), notification)...))
	var failure *protocolcore.Failure
	if !errors.As(err, &failure) || failure.Reason != protocolcore.ReasonProviderResponseFailed ||
		failure.Provider == nil || failure.Provider.Class != protocolcore.ProviderErrorRateLimited ||
		failure.Provider.Status != 0 || failure.Provider.Code != "rate_limit_exceeded" {
		t.Fatalf("err = %v (%+v)", err, failure)
	}
	if failure.NativeError.ForDialect(protocolspec.DialectOpenAIResponses) != nil {
		t.Fatal("the provider's own error would reach a foreign client")
	}
	if strings.Contains(fmt.Sprint(failure.Provider), "secret") || bytes.Contains(result.live, []byte("secret")) {
		t.Fatal("provider text was kept")
	}
}

func TestStreamFailedResponseIsClassifiedAndRenderedForTheClient(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	client, err := anthropicchat.NewClientSide(anthropicchat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	identifier, err := protocolspec.NewCodecPairID("anthropic-messages-to-test-responses")
	if err != nil {
		t.Fatal(err)
	}
	operation, err := protocolspec.NewClientOperationID(operationcatalog.AnthropicMessagesCreateID)
	if err != nil {
		t.Fatal(err)
	}
	path, err := protocolpath.Compose(protocolpath.ComposeOptions{
		ID: identifier, Revision: 1, ClientOperationIDs: []protocolspec.ClientOperationID{operation},
		Client: client, Provider: side,
	})
	if err != nil {
		t.Fatalf("the provider side does not compose: %v", err)
	}
	request := streamRequest(t, "summarized", true)
	stream, err := path.NewStreamFor(streamContext, request)
	if err != nil {
		t.Fatal(err)
	}
	public := publicStream(mappedModel)
	failed := `{"type":"response.failed","sequence_number":5,"response":{"id":"resp_1","created_at":1,"model":"m","status":"failed","output":[],` +
		`"error":{"code":"invalid_encrypted_content","message":"The encrypted content for item rs_9 could not be verified."}}}`
	out, err := stream.Feed(context.Background(), sseWire(t, append(slices.Clone(public[:4]), failed)...))
	var failure *protocolcore.Failure
	if !errors.As(err, &failure) || failure.Provider == nil || failure.Provider.Class != protocolcore.ProviderErrorReasoningState {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Contains(out, []byte("message_start")) {
		t.Fatalf("bytes before the failure were lost: %q", out)
	}
	name, data := failure.NativeError.StreamEvent(protocolspec.DialectAnthropicMessages)
	payload := failure.NativeError.ForDialect(protocolspec.DialectAnthropicMessages)
	if payload == nil || !bytes.Contains(payload, []byte(`"capability_rejected: thinking_signature"`)) {
		t.Fatalf("rendered error = %s (%s %s)", payload, name, data)
	}
	if bytes.Contains(payload, []byte("rs_9")) || bytes.Contains(data, []byte("rs_9")) {
		t.Fatal("provider text reached the client")
	}
}

func TestHiddenReasoningSendsHeartbeatsAtMostEveryTenSeconds(t *testing.T) {
	t.Parallel()

	side, clock := newStreamSide(t)
	request := streamRequest(t, "omitted", true)
	stream, err := side.NewProviderStream(streamContext, request, clientEncoder(t, request))
	if err != nil {
		t.Fatal(err)
	}
	feedOne := func(payload string) []byte {
		t.Helper()
		out, err := stream.Feed(context.Background(), sseWire(t, payload))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	delta := func(text string) string {
		return `{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"` + text + `"}`
	}
	feedOne(`{"type":"response.created","response":{"id":"resp_1","created_at":1,"model":"m","status":"in_progress","output":[]}}`)
	feedOne(`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`)
	if got := eventNames(t, feedOne(`{"type":"response.reasoning_summary_part.added","item_id":"rs_1","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)); !slices.Equal(got, []string{"content_block_start:thinking"}) {
		t.Fatalf("part added = %v", got)
	}
	heartbeats := 0
	var streamed []byte
	for _, step := range []time.Duration{0, 3 * time.Second, 6900 * time.Millisecond, 100 * time.Millisecond, 5 * time.Second, 6 * time.Second, 9 * time.Second} {
		clock.advance(step)
		out := feedOne(delta("secret thought "))
		streamed = append(streamed, out...)
		heartbeats += strings.Count(string(out), `"thinking_delta"`)
	}
	// Elapsed 0, 3, 9.9, 10, 15, 21 and 30 seconds: heartbeats at 10, 21
	// (11 after the last) but not 30 (9 after it).
	if heartbeats != 2 {
		t.Fatalf("heartbeats = %d", heartbeats)
	}
	if bytes.Contains(streamed, []byte("secret")) {
		t.Fatal("hidden reasoning text reached the client")
	}
	reasoning := `{"id":"rs_1","type":"reasoning","encrypted_content":"ENC","summary":[{"type":"summary_text","text":"secret thought"}]}`
	feedOne(`{"type":"response.output_item.done","output_index":0,"item":` + reasoning + `}`)
	feedOne(`{"type":"response.completed","response":{"id":"resp_1","created_at":1,"model":"m","status":"completed","output":[` + reasoning + `]}}`)
	terminal, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The neutral response keeps the text the client was not shown.
	if blocks := terminal.DecodedResponse().Blocks; len(blocks) != 1 || blocks[0].Reasoning == nil || blocks[0].Reasoning.Text != "secret thought" {
		t.Fatalf("decoded blocks = %+v", blocks)
	}
	release, err := terminal.Approve()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(release, []byte("secret")) {
		t.Fatal("hidden reasoning text reached the client")
	}
}

func TestReasoningWithoutEncryptedContentIsSealedButNeverReplayed(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	request := streamRequest(t, "summarized", true)
	reasoning := `{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"Think."}]}`
	message := `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Done."}]}`
	payloads := []string{
		`{"type":"response.output_item.done","output_index":0,"item":` + reasoning + `}`,
		`{"type":"response.output_item.done","output_index":1,"item":` + message + `}`,
		`{"type":"response.completed","response":{"id":"resp_1","created_at":1,"model":"m","status":"completed","output":[]}}`,
	}
	message2 := accumulate(t, approved(t, mustFeed(t, side, request, sseWire(t, payloads...))))
	seal, _, ok := protocolcore.ParseReasoningSeal(message2.Content[0].Signature)
	if !ok || seal.ItemID != "rs_1" || seal.Opaque != "" {
		t.Fatalf("seal = %+v %v", seal, ok)
	}
	next := replayRequest(t, "summarized", message2, `{"role":"user","content":"again"}`)
	encoded, report, err := side.EncodeRequestFor(streamContext, next)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded.Body(), []byte(`"type":"reasoning"`)) ||
		!slices.Contains(noticeLines(report), "reasoning_not_replayed $.messages[1].blocks[0]") {
		t.Fatalf("an empty seal was replayed: %s %v", encoded.Body(), noticeLines(report))
	}
}

// replayRequest is the next Claude Code request after message, which it
// replays verbatim as the assistant turn (F32), followed by the given user
// turn, with the same thinking display.
func replayRequest(t testing.TB, display string, message anthropic.Message, user string) protocolcore.Request {
	t.Helper()
	blocks := make([]string, 0, len(message.Content))
	for _, block := range message.Content {
		var encoded []byte
		var err error
		switch block.Type {
		case "thinking":
			encoded, err = json.Marshal(map[string]string{"type": "thinking", "thinking": block.Thinking, "signature": block.Signature})
		case "text":
			encoded, err = json.Marshal(map[string]string{"type": "text", "text": block.Text})
		case "tool_use":
			encoded, err = json.Marshal(map[string]any{"type": "tool_use", "id": block.ID, "name": block.Name, "input": block.Input})
		default:
			t.Fatalf("unexpected block %q", block.Type)
		}
		if err != nil {
			t.Fatal(err)
		}
		blocks = append(blocks, string(encoded))
	}
	body := `{"model":"claude-opus-5-5","max_tokens":128000,"stream":true,"thinking":{"type":"adaptive","display":"` + display + `"},` + streamTools +
		`,"messages":[{"role":"user","content":"list files"},{"role":"assistant","content":[` + strings.Join(blocks, ",") + `]},` + user + `]}`
	return decodeClaude(t, []byte(body))
}

// Claude Code 2.1.329 sends display omitted (-p) or updates (stream-json and
// interactive), so the round trip runs with each; summarized is the
// documented default. With display omitted Claude Code never sees the
// summary, replays empty thinking, and the item is replayed with an empty
// summary but the same id and encrypted state.
func TestStreamedReasoningRoundTripsThroughClaudeCode(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		display  string
		thinking string
		summary  string
	}{
		{"omitted", "", `[]`},
		{"updates", "Plan first.\n\nThen act.", `[{"type":"summary_text","text":"Plan first.\n\nThen act."}]`},
		{"summarized", "Plan first.\n\nThen act.", `[{"type":"summary_text","text":"Plan first.\n\nThen act."}]`},
	} {
		t.Run(test.display, func(t *testing.T) {
			t.Parallel()
			side, _ := newStreamSide(t)
			request := streamRequest(t, test.display, true)
			message := accumulate(t, approved(t, mustFeed(t, side, request, sseWire(t, publicStream(mappedModel)...))))
			if message.Content[0].Thinking != test.thinking {
				t.Fatalf("Claude Code saw thinking %q", message.Content[0].Thinking)
			}
			if seal, _, ok := protocolcore.ParseReasoningSeal(message.Content[0].Signature); !ok || seal.ItemID != "rs_1" {
				t.Fatalf("signature %q is not the item's seal", message.Content[0].Signature)
			}
			next := replayRequest(t, test.display, message, `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"a.txt"}]}`)

			encoded, _, err := side.EncodeRequestFor(streamContext, next)
			if err != nil {
				t.Fatal(err)
			}
			if err := side.ValidateTransformedRequest(encoded.Body()); err != nil {
				t.Fatalf("the replayed body is not re-admitted: %v", err)
			}
			var body struct {
				Input []map[string]json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(encoded.Body(), &body); err != nil {
				t.Fatal(err)
			}
			var types []string
			for _, item := range body.Input {
				var kind string
				_ = json.Unmarshal(item["type"], &kind)
				types = append(types, kind)
			}
			// The reasoning item sits right before the message it produced.
			if !slices.Equal(types, []string{"message", "reasoning", "message", "function_call", "function_call_output"}) {
				t.Fatalf("items = %v", types)
			}
			reasoning := body.Input[1]
			if string(reasoning["id"]) != `"rs_1"` || string(reasoning["encrypted_content"]) != `"ENC-1"` ||
				string(reasoning["summary"]) != test.summary {
				t.Fatalf("replayed reasoning = %s %s %s", reasoning["id"], reasoning["encrypted_content"], reasoning["summary"])
			}
			if string(body.Input[3]["call_id"]) != `"call_1"` || string(body.Input[4]["call_id"]) != `"call_1"` ||
				string(body.Input[2]["phase"]) != `"commentary"` {
				t.Fatalf("call ids or phase changed: %s", encoded.Body())
			}

			// Another binding (a rotated account) drops the reasoning instead.
			other := protocolpath.ProviderContext{ReasoningBinding: protocolpath.NewReasoningBinding("endpoint", "other-account")}
			encoded, report, err := side.EncodeRequestFor(other, next)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(encoded.Body(), []byte(`"type":"reasoning"`)) ||
				!slices.Contains(noticeLines(report), "reasoning_not_replayed $.messages[1].blocks[0]") {
				t.Fatalf("a seal of another binding was replayed: %v", noticeLines(report))
			}
		})
	}
}

func TestIncompleteTerminalClosesTheOpenTextFromItsSnapshot(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	request := streamRequest(t, "summarized", true)
	message := `{"id":"msg_1","type":"message","status":"incomplete","role":"assistant","content":[{"type":"output_text","text":"Partial ans"}]}`
	payloads := []string{
		`{"type":"response.created","response":{"id":"resp_1","created_at":1,"model":"m","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Partial"}`,
		`{"type":"response.incomplete","response":{"id":"resp_1","created_at":1,"model":"m","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[` + message + `]}}`,
	}
	result := everySplit(t, side, request, sseWire(t, payloads...))
	message2 := accumulate(t, approved(t, result))
	if len(message2.Content) != 1 || message2.Content[0].Text != "Partial ans" || message2.StopReason != "max_tokens" {
		t.Fatalf("message = %+v", message2)
	}
}

func TestRefusalPartBecomesTextWithItsStopReason(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	request := streamRequest(t, "summarized", true)
	message := `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"refusal","refusal":"I can't help with that."}]}`
	payloads := []string{
		`{"type":"response.created","response":{"id":"resp_1","created_at":1,"model":"m","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"refusal","refusal":""}}`,
		`{"type":"response.refusal.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"I can't "}`,
		`{"type":"response.refusal.done","item_id":"msg_1","output_index":0,"content_index":0,"refusal":"I can't help with that."}`,
		`{"type":"response.content_part.done","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"refusal","refusal":"I can't help with that."}}`,
		`{"type":"response.output_item.done","output_index":0,"item":` + message + `}`,
		`{"type":"response.completed","response":{"id":"resp_1","created_at":1,"model":"m","status":"completed","output":[` + message + `]}}`,
	}
	checkOpenAIEvents(t, payloads...)
	result := everySplit(t, side, request, sseWire(t, payloads...))
	message2 := accumulate(t, approved(t, result))
	if len(message2.Content) != 1 || message2.Content[0].Text != "I can't help with that." || message2.StopReason != "end_turn" {
		t.Fatalf("message = %+v", message2)
	}
	if got := noticeLines(result.terminal.TranslationReport()); !slices.Equal(got, []string{"refusal_rendered_as_text $"}) {
		t.Fatalf("notices = %v", got)
	}
	if blocks := result.terminal.DecodedResponse().Blocks; len(blocks) != 1 || blocks[0].Kind != protocolcore.BlockRefusal {
		t.Fatalf("decoded blocks = %+v", blocks)
	}
}

func TestRawReasoningTextStreamsWithItsSource(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	request := streamRequest(t, "summarized", true)
	reasoning := `{"id":"rs_1","type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"raw one"},{"type":"reasoning_text","text":"raw two"}],"encrypted_content":"E"}`
	message := `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}`
	payloads := []string{
		`{"type":"response.created","response":{"id":"resp_1","created_at":1,"model":"m","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"raw "}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"one"}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":1,"delta":"raw two"}`,
		`{"type":"response.reasoning_text.done","item_id":"rs_1","output_index":0,"content_index":1,"text":"raw two"}`,
		`{"type":"response.output_item.done","output_index":0,"item":` + reasoning + `}`,
		`{"type":"response.output_item.done","output_index":1,"item":` + message + `}`,
		`{"type":"response.completed","response":{"id":"resp_1","created_at":1,"model":"m","status":"completed","output":[]}}`,
	}
	result := everySplit(t, side, request, sseWire(t, payloads...))
	message2 := accumulate(t, approved(t, result))
	if message2.Content[0].Thinking != "raw one\n\nraw two" {
		t.Fatalf("thinking = %q", message2.Content[0].Thinking)
	}
	if _, source, ok := protocolcore.ParseReasoningSeal(message2.Content[0].Signature); !ok || source != protocolcore.ReasoningTextContent {
		t.Fatalf("seal source = %q %v", source, ok)
	}
}

func TestEmptyResponseGivesOneEmptyTextBlock(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	request := streamRequest(t, "summarized", true)
	result := everySplit(t, side, request, sseWire(t,
		`{"type":"response.completed","response":{"id":"resp_1","created_at":1,"model":"m","status":"completed","output":[]}}`))
	message := accumulate(t, approved(t, result))
	if message.ID != provisionalID || len(message.Content) != 1 || message.Content[0].Type != "text" || message.Content[0].Text != "" {
		t.Fatalf("message = %+v", message)
	}
}

// The three modes decode the same items into equal responses: the stream's
// pending terminal, the aggregating stream for a Stream=false request, and
// the unary decode of the terminal body.
func TestStreamAggregationAndUnaryDecodeAgree(t *testing.T) {
	t.Parallel()

	for name, payloads := range map[string][]string{
		"public":  publicStream(mappedModel),
		"minimal": minimalStream(),
		"empty":   {`{"type":"response.completed","response":{"id":"resp_1","created_at":1,"model":"m","status":"completed","output":[]}}`},
		"reasoning only": {
			`{"type":"response.completed","response":{"id":"resp_1","created_at":1,"model":"m","status":"completed","output":[{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"E"}]}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			side, _ := newStreamSide(t)
			wire := sseWire(t, payloads...)
			streamed := mustFeed(t, side, streamRequest(t, "summarized", true), wire).terminal

			unaryRequest := streamRequest(t, "summarized", false)
			aggregating, err := side.NewAggregatingStream(streamContext, unaryRequest)
			if err != nil {
				t.Fatal(err)
			}
			for index := range wire {
				out, err := aggregating.Feed(context.Background(), wire[index:index+1])
				if err != nil || len(out) != 0 {
					t.Fatalf("aggregating Feed = %q, %v", out, err)
				}
			}
			aggregated, err := aggregating.FinishDecoded(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if release, err := aggregated.Approve(); err != nil || len(release) != 0 {
				t.Fatalf("aggregation released %q, %v", release, err)
			}

			var terminal struct {
				Response json.RawMessage `json:"response"`
			}
			if err := json.Unmarshal([]byte(payloads[len(payloads)-1]), &terminal); err != nil {
				t.Fatal(err)
			}
			body := terminal.Response
			if name == "minimal" {
				// A unary body carries its items in output.
				var items []string
				for _, payload := range payloads[:len(payloads)-1] {
					var done struct {
						Item json.RawMessage `json:"item"`
					}
					_ = json.Unmarshal([]byte(payload), &done)
					items = append(items, string(done.Item))
				}
				body = []byte(strings.Replace(string(body), `"output":[]`, `"output":[`+strings.Join(items, ",")+`]`, 1))
			}
			unary, _, err := side.DecodeResponseFor(streamContext, unaryRequest, body)
			if err != nil {
				t.Fatal(err)
			}
			want := streamed.DecodedResponse()
			// Clone gives every response the same slice shapes.
			unary = unary.Clone()
			if got := aggregated.DecodedResponse(); !reflect.DeepEqual(got, want) {
				t.Fatalf("aggregated response differs:\n%+v\nwant\n%+v", got, want)
			}
			if !reflect.DeepEqual(unary.Blocks, want.Blocks) || unary.StopReason != want.StopReason ||
				unary.Usage != want.Usage || unary.ID != want.ID {
				t.Fatalf("unary response differs:\n%+v\nwant\n%+v", unary, want)
			}
			if !reflect.DeepEqual(aggregated.ToolIntents(), streamed.ToolIntents()) {
				t.Fatal("aggregated intents differ")
			}
		})
	}
}

func TestStreamModeIsChecked(t *testing.T) {
	t.Parallel()

	side, _ := newStreamSide(t)
	streaming, unary := streamRequest(t, "", true), streamRequest(t, "", false)
	if _, err := side.NewProviderStream(streamContext, unary, clientEncoder(t, unary)); protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidClientRequest {
		t.Fatalf("a unary request streamed: %v", err)
	}
	if _, err := side.NewAggregatingStream(streamContext, streaming); protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidClientRequest {
		t.Fatalf("a streaming request was aggregated: %v", err)
	}
}
