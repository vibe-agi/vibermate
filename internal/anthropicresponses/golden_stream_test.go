package anthropicresponses

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

// goldenCall is one call of the scripted turns: the five calls Claude Code
// made in claude-tools.008, under the ids each upstream gave them.
type goldenCall struct {
	id        string
	name      string
	arguments string
	// replayed is the tool_use id claude-tools.008 holds for this call.
	replayed string
}

// goldenStream is one scripted upstream turn of design 15.3 B.
type goldenStream struct {
	name string
	// reasoning is the upstream reasoning item the turn's thinking replays.
	reasoningID, encrypted string
	thinking               string
	text                   string
	calls                  []goldenCall
	// usage is what Claude Code must read: input, cache read, output.
	usage [3]int64
	// provisional is set for the minimal ChatGPT-style stream, which has no
	// response.created, so the client message id is minted.
	provisional bool
}

func goldenCalls(prefix string) []goldenCall {
	arguments := []string{
		`{"max_results":20,"query":"select:CronCreate,CronDelete,CronList,DesignSync,EnterWorktree,ExitWorktree,Monitor,NotebookEdit,SendMessage,TaskStop,WebFetch,WebSearch"}`,
		`{"file_path":"/tmp/wire-skeleton/002/wire-fixture.png"}`,
		`{"file_path":"/tmp/wire-skeleton/002/wire-fixture.txt"}`,
		`{"file_path":"/tmp/wire-skeleton/002/wire-fixture.pdf"}`,
		`{"command":"echo wire-fixture","description":"Print a marker"}`,
	}
	names := []string{"ToolSearch", "Read", "Read", "Read", "Bash"}
	suffixes := []string{"search", "png", "txt", "pdf", "bash"}
	calls := make([]goldenCall, len(names))
	for index := range names {
		calls[index] = goldenCall{
			id: prefix + suffixes[index], name: names[index], arguments: arguments[index],
			replayed: "toolu_wire_" + suffixes[index],
		}
	}
	return calls
}

var goldenStreams = []goldenStream{
	{
		name:        "public",
		reasoningID: "rs_gold_public", encrypted: "gAAAAAB-GOLDEN-PUBLIC-ENCRYPTED.reasoning.0001==",
		thinking: "Plan the tool calls.\n\nRead the fixtures first.",
		text:     "WIRE-ASSISTANT-PREAMBLE", calls: goldenCalls("call_gold_"),
		usage: [3]int64{12000 - 4000, 4000, 300},
	},
	{
		name:        "chatgpt-minimal",
		reasoningID: "rs_wire_1", encrypted: "WIRE-ENCRYPTED-REASONING-0001",
		thinking: "WIRE-REASONING-SUMMARY\n\nWIRE-SECOND-PART",
		text:     "WIRE-ASSISTANT-PREAMBLE", calls: goldenCalls("call_wire_"),
		usage: [3]int64{11 - 7, 7, 40}, provisional: true,
	},
}

// provisionalMessageID is the client message id minted when a stream has no
// response.created; the goldens hold it normalised.
var provisionalMessageID = regexp.MustCompile(`msg_vm_[0-9a-f]{24}`)

func normaliseProvisional(stream []byte) []byte {
	return provisionalMessageID.ReplaceAll(stream, []byte("msg_vm_000000000000000000000000"))
}

// goldenTurnOne is claude-tools.007, the first request of the tool round
// trip, as the Exchange hands it to the provider side.
func goldenTurnOne(t *testing.T, path *protocolpath.Path) protocolcore.Request {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(goldenCorpus, "claude-tools.007.json"))
	if err != nil {
		t.Fatal(err)
	}
	request, _ := goldenDecode(t, path, body)
	return request
}

func splitEvents(wire []byte) [][]byte {
	var events [][]byte
	for len(wire) != 0 {
		end := bytes.Index(wire, []byte("\n\n"))
		if end < 0 {
			return append(events, wire)
		}
		events = append(events, wire[:end+2])
		wire = wire[end+2:]
	}
	return events
}

// streamResult is what one run of a scripted turn gave the client.
type streamResult struct {
	live, release []byte
	terminal      protocolpath.PendingTerminal
}

func runStream(t *testing.T, path *protocolpath.Path, request protocolcore.Request, fragments [][]byte) streamResult {
	t.Helper()
	stream, err := path.NewStreamFor(goldenContext(goldenAccount), request)
	if err != nil {
		t.Fatal(err)
	}
	var result streamResult
	for _, fragment := range fragments {
		output, err := stream.Feed(t.Context(), fragment)
		if err != nil {
			t.Fatalf("Feed: %v", err)
		}
		result.live = append(result.live, output...)
	}
	if !stream.TerminalReceived() {
		t.Fatal("the stream ended without its terminal")
	}
	result.terminal, err = stream.FinishDecoded(t.Context())
	if err != nil {
		t.Fatalf("FinishDecoded: %v", err)
	}
	return result
}

// accumulate reads client bytes as anthropic-sdk-go reads them for Claude
// Code.
func accumulate(t *testing.T, stream []byte) anthropic.Message {
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
	var message anthropic.Message
	for _, event := range events {
		var union anthropic.MessageStreamEventUnion
		if err := union.UnmarshalJSON(event.Data); err != nil {
			t.Fatalf("anthropic-sdk-go cannot parse %s: %v", event.Data, err)
		}
		if union.Type != event.Name {
			t.Fatalf("event %q carries type %q", event.Name, union.Type)
		}
		if err := message.Accumulate(union); err != nil {
			t.Fatalf("anthropic-sdk-go cannot accumulate %s: %v", event.Data, err)
		}
	}
	return message
}

// Golden B: each scripted upstream turn gives the checked-in Anthropic stream,
// whatever the upstream's fragmenting, holds every tool byte behind the
// approval barrier, and accumulates in anthropic-sdk-go into the expected
// message.
func TestGoldenStreams(t *testing.T) {
	path := goldenPath(t)
	request := goldenTurnOne(t, path)
	for _, golden := range goldenStreams {
		t.Run(golden.name, func(t *testing.T) {
			wire, err := os.ReadFile(filepath.Join(goldenRoot, golden.name+".responses.sse"))
			if err != nil {
				t.Fatal(err)
			}
			result := runStream(t, path, request, splitEvents(wire))
			release, err := result.terminal.Approve()
			if err != nil {
				t.Fatal(err)
			}
			client := normaliseProvisional(append(bytes.Clone(result.live), release...))
			compareGolden(t, golden.name+".anthropic.sse", client)
			compareGolden(t, golden.name+".stream-notices.txt", noticeText("stream", result.terminal.TranslationReport()))

			// The approval barrier: thinking and the commentary stream live,
			// every tool byte and the terminal wait for the decision.
			textStop := "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n"
			if bytes.Contains(result.live, []byte("tool_use")) || bytes.Contains(result.live, []byte("message_delta")) ||
				!bytes.HasSuffix(result.live, []byte(textStop)) {
				t.Fatalf("live bytes = %s", result.live)
			}
			if !bytes.HasPrefix(release, []byte("event: content_block_start\n")) || !bytes.Contains(release, []byte(`"type":"tool_use"`)) {
				t.Fatalf("release = %s", release)
			}
			// The same upstream bytes in one fragment and one byte at a time.
			for name, fragments := range map[string][][]byte{"whole": {wire}, "bytewise": bytewise(wire)} {
				again := runStream(t, path, request, fragments)
				repeated, err := again.terminal.Approve()
				if err != nil {
					t.Fatal(err)
				}
				if got := normaliseProvisional(append(again.live, repeated...)); !bytes.Equal(got, client) {
					t.Fatalf("%s fragmenting changed the client bytes:\n%s", name, got)
				}
			}

			message := accumulate(t, client)
			assertGoldenMessage(t, golden, message)
			intents := result.terminal.ToolIntents()
			if len(intents) != len(golden.calls) {
				t.Fatalf("intents = %+v", intents)
			}
			for index, intent := range intents {
				call := golden.calls[index]
				if intent.Call.Key.Source() != "openai-responses-call" || intent.Call.Key.WireID() != call.id ||
					intent.Call.Name != call.name || string(intent.Call.Arguments.Bytes()) != call.arguments {
					t.Fatalf("intent %d = %+v, want %+v", index, intent.Call, call)
				}
			}
		})
	}
}

func bytewise(wire []byte) [][]byte {
	fragments := make([][]byte, len(wire))
	for index := range wire {
		fragments[index] = wire[index : index+1]
	}
	return fragments
}

func assertGoldenMessage(t *testing.T, golden goldenStream, message anthropic.Message) {
	t.Helper()
	if golden.provisional != strings.HasPrefix(message.ID, "msg_vm_") {
		t.Fatalf("message id = %q", message.ID)
	}
	if string(message.Model) != "claude-opus-5-5" || message.StopReason != anthropic.StopReasonToolUse {
		t.Fatalf("message model/stop = %q %q", message.Model, message.StopReason)
	}
	if message.Usage.InputTokens != golden.usage[0] || message.Usage.CacheReadInputTokens != golden.usage[1] ||
		message.Usage.OutputTokens != golden.usage[2] || message.Usage.CacheCreationInputTokens != 0 {
		t.Fatalf("usage = %+v, want %v", message.Usage, golden.usage)
	}
	if len(message.Content) != 2+len(golden.calls) {
		t.Fatalf("content = %+v", message.Content)
	}
	thinking := message.Content[0]
	if thinking.Type != "thinking" || thinking.Thinking != golden.thinking {
		t.Fatalf("thinking = %+v", thinking)
	}
	seal, source, ok := protocolcore.ParseReasoningSeal(thinking.Signature)
	if !ok || source == protocolcore.ReasoningTextContent ||
		seal.Format != protocolcore.SealResponsesEncrypted || seal.ItemID != golden.reasoningID ||
		seal.Model != goldenModels["claude-opus-5-5"] || seal.Binding != goldenContext(goldenAccount).ReasoningBinding ||
		seal.Opaque != golden.encrypted {
		t.Fatalf("signature %q is not the upstream's sealed reasoning: %+v %q %v", thinking.Signature, seal, source, ok)
	}
	if text := message.Content[1]; text.Type != "text" || text.Text != golden.text {
		t.Fatalf("text = %+v", text)
	}
	for index, call := range golden.calls {
		block := message.Content[2+index]
		var input, want any
		if json.Unmarshal(block.Input, &input) != nil || json.Unmarshal([]byte(call.arguments), &want) != nil {
			t.Fatalf("tool_use %d input = %s", index, block.Input)
		}
		if block.Type != "tool_use" || block.ID != call.id || block.Name != call.name || !jsonEqual(input, want) {
			t.Fatalf("tool_use %d = %+v, want %+v", index, block, call)
		}
	}
}

func jsonEqual(left, right any) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}

// replayTurn builds the request Claude Code sends after a turn: B's
// accumulated message as the assistant turn, replayed verbatim (F32), and
// claude-tools.008's tool results answering its calls.
func replayTurn(t *testing.T, golden goldenStream, message anthropic.Message) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(goldenCorpus, "claude-tools.008.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(root["messages"], &messages); err != nil || len(messages) != 4 {
		t.Fatalf("claude-tools.008 messages: %v (%d)", err, len(messages))
	}
	type block map[string]any
	var assistant []block
	for _, content := range message.Content {
		switch content.Type {
		case "thinking":
			assistant = append(assistant, block{"type": "thinking", "thinking": content.Thinking, "signature": content.Signature})
		case "text":
			assistant = append(assistant, block{"type": "text", "text": content.Text})
		case "tool_use":
			assistant = append(assistant, block{"type": "tool_use", "id": content.ID, "name": content.Name, "input": json.RawMessage(content.Input)})
		default:
			t.Fatalf("unexpected block %s", content.Type)
		}
	}
	replayed, err := json.Marshal(map[string]any{"role": "assistant", "content": assistant})
	if err != nil {
		t.Fatal(err)
	}
	var results struct {
		Role    string  `json:"role"`
		Content []block `json:"content"`
	}
	if err := json.Unmarshal(messages[3], &results); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, call := range golden.calls {
		ids[call.replayed] = call.id
	}
	answered := 0
	for _, result := range results.Content {
		if result["type"] != "tool_result" {
			continue
		}
		id, ok := ids[result["tool_use_id"].(string)]
		if !ok {
			t.Fatalf("claude-tools.008 answers an unknown call %v", result["tool_use_id"])
		}
		result["tool_use_id"] = id
		answered++
	}
	if answered != len(golden.calls) {
		t.Fatalf("claude-tools.008 answers %d of %d calls", answered, len(golden.calls))
	}
	answers, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	messages[2], messages[3] = replayed, answers
	if root["messages"], err = json.Marshal(messages); err != nil {
		t.Fatal(err)
	}
	turn, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

// Golden C: B's message, replayed by Claude Code with the tool results,
// replays the upstream's own reasoning item right before the item it
// produced, keeps every call id and infers the phases; on another binding the
// reasoning is dropped and declared.
func TestGoldenRoundTrips(t *testing.T) {
	path := goldenPath(t)
	request := goldenTurnOne(t, path)
	for _, golden := range goldenStreams {
		t.Run(golden.name, func(t *testing.T) {
			wire, err := os.ReadFile(filepath.Join(goldenRoot, golden.name+".responses.sse"))
			if err != nil {
				t.Fatal(err)
			}
			result := runStream(t, path, request, splitEvents(wire))
			release, err := result.terminal.Approve()
			if err != nil {
				t.Fatal(err)
			}
			turn := replayTurn(t, golden, accumulate(t, append(result.live, release...)))
			replay, decodeReport := goldenDecode(t, path, turn)
			if replay.EffectiveModel != request.EffectiveModel {
				t.Fatalf("turn two maps to %s, turn one to %s", replay.EffectiveModel, request.EffectiveModel)
			}

			provider, encodeReport := goldenEncode(t, path, goldenContext(goldenAccount), replay, turn)
			compareGolden(t, golden.name+".round-trip.responses.json", provider.Body())
			compareGolden(t, golden.name+".round-trip.notices.txt", noticeText("decode", decodeReport, "encode", encodeReport))
			assertResponsesOracle(t, replay, provider.Body())
			if reportHas(encodeReport, protocolcore.NoticeReasoningNotReplayed) {
				t.Fatalf("the path's own seal was not replayed: %+v", encodeReport.Notices())
			}
			items := inputItems(t, provider.Body())
			at := slices.IndexFunc(items, func(item roundTripItem) bool { return item.Type == "reasoning" })
			if at < 0 || slices.ContainsFunc(items[at+1:], func(item roundTripItem) bool { return item.Type == "reasoning" }) {
				t.Fatalf("want exactly one reasoning item: %s", provider.Body())
			}
			reasoning := items[at]
			if reasoning.ID != golden.reasoningID || reasoning.EncryptedContent != golden.encrypted ||
				len(reasoning.Summary) != 1 || reasoning.Summary[0].Type != "summary_text" || reasoning.Summary[0].Text != golden.thinking {
				t.Fatalf("replayed reasoning = %+v", reasoning)
			}
			// It sits immediately before the commentary it produced, which
			// precedes the calls, so its phase is commentary.
			next := items[at+1]
			if next.Type != "message" || next.Role != "assistant" || next.Phase != "commentary" ||
				len(next.Content) != 1 || next.Content[0].Text != golden.text {
				t.Fatalf("item after the reasoning = %+v", next)
			}
			var calls, outputs []string
			for _, item := range items[at+2:] {
				switch item.Type {
				case "function_call":
					calls = append(calls, item.CallID+" "+item.Name+" "+item.Arguments)
				case "function_call_output":
					outputs = append(outputs, item.CallID)
				}
			}
			var wantCalls, wantOutputs []string
			for _, call := range golden.calls {
				wantCalls = append(wantCalls, call.id+" "+call.name+" "+call.arguments)
			}
			// claude-tools.008 answers in its own order: search, txt, png, pdf, bash.
			for _, index := range []int{0, 2, 1, 3, 4} {
				wantOutputs = append(wantOutputs, golden.calls[index].id)
			}
			if !slices.Equal(calls, wantCalls) || !slices.Equal(outputs, wantOutputs) {
				t.Fatalf("calls = %v outputs = %v; want %v and %v", calls, outputs, wantCalls, wantOutputs)
			}

			// The same history on a route with another account.
			other, otherReport := goldenEncode(t, path, goldenContext("another-account"), replay, turn)
			if bytes.Contains(other.Body(), []byte(`"type":"reasoning"`)) || bytes.Contains(other.Body(), []byte(golden.encrypted)) {
				t.Fatalf("another binding replayed the reasoning: %s", other.Body())
			}
			if !slices.ContainsFunc(otherReport.Notices(), func(notice protocolcore.TranslationNotice) bool {
				return notice.Code == protocolcore.NoticeReasoningNotReplayed && notice.Path == "$.messages[2].blocks[0]"
			}) {
				t.Fatalf("the dropped reasoning was not declared: %+v", otherReport.Notices())
			}
			if !bytes.Contains(other.Body(), []byte(`"phase":"commentary"`)) || !bytes.Contains(other.Body(), []byte(golden.calls[0].id)) {
				t.Fatalf("dropping the reasoning changed the rest of the turn: %s", other.Body())
			}
		})
	}
}

type roundTripItem struct {
	Type             string `json:"type"`
	ID               string `json:"id"`
	Role             string `json:"role"`
	Phase            string `json:"phase"`
	CallID           string `json:"call_id"`
	Name             string `json:"name"`
	Arguments        string `json:"arguments"`
	EncryptedContent string `json:"encrypted_content"`
	Summary          []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func inputItems(t *testing.T, body []byte) []roundTripItem {
	t.Helper()
	var wire struct {
		Input []roundTripItem `json:"input"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.Input
}
