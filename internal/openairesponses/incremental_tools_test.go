package openairesponses_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/responseschat"
)

// Codex appends partial tool updates to conversation history on resume and
// model changes. A later definition replaces only that tool; unchanged members
// remain declared. The provider receives every original item unchanged.
func TestCompatibleIncrementalToolsKeepsHistoryAndLatestDefinitions(t *testing.T) {
	const source = `{"model":"fixture-model","store":false,"stream":true,"input":[
	 {"type":"additional_tools","role":"developer","tools":[
	  {"type":"namespace","name":"functions","description":"First namespace","tools":[
	   {"type":"custom","name":"exec","description":"old","format":{"type":"text"}},
	   {"type":"function","name":"wait","parameters":{"type":"object"}}]},
	  {"type":"function","name":"inspect","description":"old","parameters":{"type":"object"}}]},
	 {"role":"user","content":"first turn"},
	 {"role":"assistant","content":[{"type":"output_text","text":"first answer"}]},
	 {"type":"additional_tools","role":"developer","tools":[
	  {"type":"namespace","name":"functions","description":"Updated namespace","tools":[
	   {"type":"custom","name":"exec","description":"updated","format":{"type":"text"}},
	   {"type":"function","name":"ask","parameters":{"type":"object"}}]},
	  {"type":"function","name":"inspect","description":"updated","parameters":{"type":"object"}}]},
	 {"role":"user","content":"continue after tools changed"}]}`
	path, err := responseschat.NewResponsesPassthroughProtocolPath(openairesponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := path.Client().DecodeRequest([]byte(source))
	if err != nil {
		t.Fatalf("valid incremental history was rejected: %v", err)
	}
	if len(request.Messages) != 3 || request.Messages[2].Blocks[0].Text != "continue after tools changed" {
		t.Fatalf("conversation changed: %#v", request.Messages)
	}
	if len(request.Tools) != 1 || request.Tools[0].Description != "updated" {
		t.Fatalf("latest function was not projected: %#v", request.Tools)
	}
	if len(request.ToolNamespaces) != 1 {
		t.Fatalf("namespaces=%d", len(request.ToolNamespaces))
	}
	namespace := request.ToolNamespaces[0]
	if namespace.Description != "Updated namespace" || len(namespace.Tools) != 3 ||
		namespace.Tools[0].Name != "exec" || namespace.Tools[0].Description != "updated" ||
		namespace.Tools[1].Name != "wait" || namespace.Tools[2].Name != "ask" {
		t.Fatalf("partial namespace update lost or duplicated definitions: %#v", namespace)
	}
	encoded, _, err := path.EncodeProviderRequest(request, []byte(source), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	var original, forwarded any
	if err := json.Unmarshal([]byte(source), &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded.Body(), &forwarded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, forwarded) {
		t.Fatal("provider request changed the source history or declarations")
	}
	codec, _ := openairesponses.New(openairesponses.DefaultOptions())
	if _, _, err := codec.DecodeClientRequest([]byte(source)); err == nil {
		t.Fatal("incremental native history silently acquired cross-dialect translation semantics")
	}
}

func TestCompatibleIncrementalToolsDoesNotHideInvalidUpdate(t *testing.T) {
	const function = `{"type":"function","name":"inspect","parameters":{"type":"object"}}`
	const namespace = `{"type":"namespace","name":"functions","description":"Fixture","tools":[` + function + `]}`
	codec, _ := openairesponses.New(openairesponses.DefaultOptions())
	for name, update := range map[string]string{
		"duplicate functions within one update":  function + "," + function,
		"duplicate namespaces within one update": namespace + "," + namespace,
		"duplicate namespace members":            `{"type":"namespace","name":"functions","description":"Fixture","tools":[` + function + `,` + function + `]}`,
		"invalid redefined schema":               `{"type":"function","name":"inspect","parameters":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			source := `{"model":"fixture","input":[{"type":"additional_tools","role":"developer","tools":[` + function + `]},{"type":"additional_tools","role":"developer","tools":[` + update + `]},{"role":"user","content":"continue"}]}`
			if _, _, err := codec.DecodeCompatibleClientRequest([]byte(source)); err == nil {
				t.Fatal("invalid update was accepted")
			}
		})
	}
}

func TestCompatibleIncrementalToolsDoesNotHitCountLimitForRedefinitions(t *testing.T) {
	input := make([]json.RawMessage, 0, 301)
	const update = `{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"inspect","description":"latest","parameters":{"type":"object"}}]}`
	for range 300 {
		input = append(input, json.RawMessage(update))
	}
	input = append(input, json.RawMessage(`{"role":"user","content":"tail sentinel"}`))
	source, err := json.Marshal(map[string]any{"model": "fixture", "input": input})
	if err != nil {
		t.Fatal(err)
	}
	codec, _ := openairesponses.New(openairesponses.DefaultOptions())
	request, _, err := codec.DecodeCompatibleClientRequest(source)
	if err != nil {
		t.Fatalf("redefinition history rejected: %v", err)
	}
	if len(request.Tools) != 1 || request.Messages[0].Blocks[0].Text != "tail sentinel" {
		t.Fatal("history or projected catalog changed")
	}
	// Independent new names are still subject to the existing semantic limit.
	for i := range 300 {
		input[i] = json.RawMessage(strings.Replace(update, `"inspect"`, fmt.Sprintf(`"inspect_%d"`, i), 1))
	}
	source, _ = json.Marshal(map[string]any{"model": "fixture", "input": input})
	if _, _, err = codec.DecodeCompatibleClientRequest(source); err == nil {
		t.Fatal("unbounded distinct catalog was accepted")
	}
}
