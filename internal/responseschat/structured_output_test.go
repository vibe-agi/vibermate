package responseschat

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/vibe-agi/vibermate/internal/openairesponses"
)

func TestResponsesStructuredTitleSchemaIsPreservedAcrossModelMapping(t *testing.T) {
	path, err := NewResponsesPassthroughProtocolPath(openairesponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	source := []byte(`{"model":"gpt-client","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Generate a title"}]}],"stream":true,"store":false,"text":{"verbosity":"low","format":{"type":"json_schema","strict":true,"schema":{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":36}},"required":["title"],"additionalProperties":false},"name":"codex_output_schema"}}}`)
	request, _, err := path.Client().DecodeRequest(source)
	if err != nil {
		t.Fatal(err)
	}
	request, err = request.WithEffectiveModel("gpt-provider")
	if err != nil {
		t.Fatal(err)
	}
	provider, _, err := path.EncodeProviderRequest(request, source, make(http.Header))
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	if err := json.Unmarshal(source, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(provider.Body(), &after); err != nil {
		t.Fatal(err)
	}
	if after["model"] != "gpt-provider" || !reflect.DeepEqual(before["text"], after["text"]) {
		t.Fatalf("structured output contract changed: %s", provider.Body())
	}
	codec, err := openairesponses.New(openairesponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := codec.DecodeClientRequest(source); err == nil {
		t.Fatal("translation accepted a schema it cannot preserve")
	}
}
