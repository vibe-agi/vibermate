package anthropicresponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

// responsesWire frames event payloads as the public Responses API does.
func responsesWire(t testing.TB, payloads ...string) []byte {
	t.Helper()
	var wire bytes.Buffer
	for _, payload := range payloads {
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

const ownershipRequest = `{"model":"claude-opus-5-5","max_tokens":1024,"stream":true,` +
	`"tools":[{"name":"Bash","description":"Run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],` +
	`"messages":[{"role":"user","content":"list files"}]}`

// The composed stream owns its request, the bytes it was fed and the bytes it
// returned: a caller that reuses its buffers or edits its request after the
// stream started changes nothing the terminal reports or releases.
func TestComposedStreamOwnsItsRequestInputAndOutput(t *testing.T) {
	t.Parallel()
	path, err := NewProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := path.Client().DecodeRequest([]byte(ownershipRequest))
	if err != nil {
		t.Fatal(err)
	}
	request, err = request.WithEffectiveModel("gpt-upstream")
	if err != nil {
		t.Fatal(err)
	}
	attempt := protocolpath.ProviderContext{ReasoningBinding: protocolpath.NewReasoningBinding("target", "account")}
	stream, err := path.NewStreamFor(attempt, request)
	if err != nil {
		t.Fatal(err)
	}
	// The stream maps the provider's tool name back through its own copy of
	// the request, so these edits must not reach it.
	request.Tools[0].Name = "Mutated"
	request.Messages = nil

	call := `{"id":"fc_1","type":"function_call","status":"completed","arguments":"{\"command\":\"ls\"}","call_id":"call_1","name":"Bash"}`
	message := `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"text":"owned text"}]}`
	progress := strings.Repeat(string(responsesWire(t, `{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"gpt-upstream","output":[]}}`)), 64)
	fragments := [][]byte{
		responsesWire(t, `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"gpt-upstream","output":[]}}`),
		[]byte(progress),
		responsesWire(t,
			`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
			`{"type":"response.content_part.added","sequence_number":3,"item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"text":""}}`,
			`{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"owned text"}`,
			`{"type":"response.output_item.done","sequence_number":5,"output_index":0,"item":`+message+`}`,
			`{"type":"response.output_item.added","sequence_number":6,"output_index":1,"item":{"id":"fc_1","type":"function_call","status":"in_progress","arguments":"","call_id":"call_1","name":"Bash"}}`,
			`{"type":"response.output_item.done","sequence_number":7,"output_index":1,"item":`+call+`}`,
			`{"type":"response.completed","sequence_number":8,"response":{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"gpt-upstream","output":[`+message+`,`+call+`],`+
				`"usage":{"input_tokens":5,"input_tokens_details":{"cached_tokens":0},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":8}}}`,
		),
	}
	var live []byte
	for _, fragment := range fragments {
		output, err := stream.Feed(t.Context(), fragment)
		if err != nil {
			t.Fatal(err)
		}
		live = append(live, output...)
		clear(fragment)
		clear(output)
	}
	if !bytes.Contains(live, []byte("owned text")) || bytes.Contains(live, []byte("tool_use")) {
		t.Fatalf("live bytes lost the text or leaked the held call: %s", live)
	}
	terminal, err := stream.FinishDecoded(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	copied := terminal.DecodedResponse()
	copied.Blocks[0].Text = "caller mutation"
	copied.Blocks[1].ToolCall.Name = "caller mutation"
	decoded := terminal.DecodedResponse()
	if decoded.Blocks[0].Text != "owned text" || decoded.Blocks[1].ToolCall.Name != "Bash" {
		t.Fatalf("terminal borrowed a caller mutation: %+v", decoded.Blocks)
	}
	intents := terminal.ToolIntents()
	if len(intents) != 1 || intents[0].Call.Name != "Bash" || intents[0].Call.Key.Source() != "openai-responses-call" {
		t.Fatalf("intents = %+v", intents)
	}
	intents[0].Call.Name = "caller mutation"
	if terminal.ToolIntents()[0].Call.Name != "Bash" {
		t.Fatal("intents borrowed a caller mutation")
	}
	approved, err := terminal.Approve()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(approved, []byte(`"name":"Bash"`)) || !bytes.Contains(approved, []byte("message_stop")) {
		t.Fatalf("approval lost the held call or the terminal: %s", approved)
	}
	if err := terminal.Reject(); err == nil {
		t.Fatal("a decided terminal was decided again")
	}
}

// Every unary answer is a fresh value: encoding the same response twice
// gives equal bytes, and the encoder keeps no reference to the response.
func TestClientResponseEncodingOwnsItsInput(t *testing.T) {
	t.Parallel()
	path, err := NewProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := path.Client().DecodeRequest([]byte(strings.Replace(ownershipRequest, `"stream":true`, `"stream":false`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	request, err = request.WithEffectiveModel("gpt-upstream")
	if err != nil {
		t.Fatal(err)
	}
	response := protocolcore.Response{
		ID: "resp_1", RequestedModel: request.RequestedModel, EffectiveModel: "gpt-upstream", ReportedModel: "gpt-upstream",
		StopReason: protocolcore.StopReasonEndTurn,
		Blocks:     []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "first"}},
	}
	first, _, err := path.EncodeClientResponse(request, response, nil)
	if err != nil {
		t.Fatal(err)
	}
	kept := bytes.Clone(first)
	response.Blocks[0].Text = "second"
	if !bytes.Equal(first, kept) {
		t.Fatal("the encoded body aliases the response")
	}
	again, _, err := path.EncodeClientResponse(request, response, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(again, []byte(`"text":"second"`)) || bytes.Equal(again, first) {
		t.Fatalf("second encode = %s", again)
	}
}
