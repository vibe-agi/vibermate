package anthropicchat

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
)

func TestProtocolPathComposesAnthropicClientAndChatBackendEdges(t *testing.T) {
	t.Parallel()

	path, err := NewProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if path.Client().Dialect() != access.DialectAnthropicMessages ||
		path.Backend().Dialect() != access.DialectOpenAIChat {
		t.Fatal("protocol path dialect edges are incomplete")
	}
	request, _, err := path.Client().DecodeRequest([]byte(`{
		"model":"claude-client",
		"max_tokens":16,
		"messages":[{"role":"user","content":"hello"}],
		"stream":true
	}`))
	if err != nil {
		t.Fatal(err)
	}
	request, err = request.WithEffectiveModel("provider-model")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _, err := path.Backend().EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Method() != http.MethodPost ||
		encoded.RelativePath() != ProviderRelativePath ||
		encoded.Headers().Get("Accept") != "text/event-stream" {
		t.Fatalf("encoded provider request = %+v", encoded)
	}
	var wire struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(encoded.Body(), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Model != "provider-model" || !wire.Stream {
		t.Fatalf("provider wire = %+v", wire)
	}
	if _, err := path.Streaming().NewStream(request); err != nil {
		t.Fatalf("stream bridge: %v", err)
	}
}
