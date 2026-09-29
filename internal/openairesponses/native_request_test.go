package openairesponses

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// The same-dialect wire is authoritative: the projection observes what it
// models and never rejects valid Responses requests it merely does not model.
// A translated (cross-dialect) request cannot carry these and still fails.
func TestSameDialectRequestAcceptsCurrentResponsesShapes(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"string input":        `{"model":"m","input":"hello"}`,
		"typeless message":    `{"model":"m","input":[{"role":"user","content":"hello"}]}`,
		"item reference":      `{"model":"m","input":[{"role":"user","content":"hi"},{"type":"item_reference","id":"msg_1"}]}`,
		"mcp approval":        `{"model":"m","input":[{"role":"user","content":"hi"},{"type":"mcp_approval_response","approval_request_id":"r1","approve":true}]}`,
		"future history item": `{"model":"m","input":[{"role":"user","content":"hi"},{"type":"future_item","id":"f1","data":{"x":1}}]}`,
		"local shell tool":    `{"model":"m","input":"hi","tools":[{"type":"local_shell"}]}`,
		"web search filters":  `{"model":"m","input":"hi","tools":[{"type":"web_search","filters":{"allowed_domains":["example.com"]},"search_context_size":"low"}]}`,
		"hosted mcp tool":     `{"model":"m","input":"hi","tools":[{"type":"mcp","server_label":"docs","server_url":"https://mcp.example.com","require_approval":"never"}]}`,
		"image generation":    `{"model":"m","input":"hi","tools":[{"type":"image_generation"},{"type":"code_interpreter","container":{"type":"auto"}}]}`,
		"allowed tools choice": `{"model":"m","input":"hi","tools":[{"type":"function","name":"f","parameters":{"type":"object","properties":{}}}],
			"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"f"}]}}`,
		"include values": `{"model":"m","input":"hi","include":["file_search_call.results","message.output_text.logprobs"]}`,
		"input file":     `{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"read"},{"type":"input_file","file_id":"file_1"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			request, _, err := newTestCodec(t).DecodeCompatibleClientRequest([]byte(body))
			if err != nil {
				t.Fatalf("same-dialect decode: %v", err)
			}
			if request.RequestedModel != "m" || len(request.Messages) == 0 {
				t.Fatalf("projection = %#v", request)
			}
			if _, _, err := newTestCodec(t).DecodeClientRequest([]byte(body)); err == nil && name != "string input" && name != "typeless message" {
				t.Fatal("translated decode accepted a shape no other dialect can carry")
			}
		})
	}
}

func TestStringInputIsOneUserMessage(t *testing.T) {
	t.Parallel()
	for _, decode := range []func(*Codec, []byte) (protocolcore.Request, protocolcore.TranslationReport, error){
		(*Codec).DecodeCompatibleClientRequest, (*Codec).DecodeClientRequest,
	} {
		request, _, err := decode(newTestCodec(t), []byte(`{"model":"m","input":"hello"}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(request.Messages) != 1 || request.Messages[0].Role != protocolcore.RoleUser ||
			len(request.Messages[0].Blocks) != 1 || request.Messages[0].Blocks[0].Text != "hello" {
			t.Fatalf("string input projection = %#v", request.Messages)
		}
	}
}
