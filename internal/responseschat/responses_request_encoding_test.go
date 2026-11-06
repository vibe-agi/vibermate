package responseschat

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/openairesponses"
)

func TestResponsesPassthroughDoesNotExpandAdmittedRequest(t *testing.T) {
	for _, model := range []string{"client-model", "mapped-model"} {
		t.Run(model, func(t *testing.T) {
			options := openairesponses.DefaultOptions()
			options.MaxRequestBytes = 1024
			path, err := NewResponsesPassthroughProtocolPath(options)
			if err != nil {
				t.Fatal(err)
			}
			prefix := `{"model":"client-model","input":[{"role":"user","content":"<html>&</html>` + "\u2028\u2029"
			suffix := `"}],"stream":true}`
			source := []byte(prefix + strings.Repeat("x", options.MaxRequestBytes-len(prefix)-len(suffix)) + suffix)
			request, _, err := path.Client().DecodeRequest(source)
			if err != nil {
				t.Fatalf("source request was rejected: %v", err)
			}
			request, err = request.WithEffectiveModel(model)
			if err != nil {
				t.Fatal(err)
			}
			provider, _, err := path.EncodeProviderRequest(request, source, make(http.Header))
			if err != nil {
				t.Fatal(err)
			}
			if err := path.ValidateTransformedRequest(provider.Body()); err != nil {
				t.Fatalf("admitted request rejected after encoding (%d -> %d bytes): %v", len(source), len(provider.Body()), err)
			}
			if model == "client-model" && !bytes.Equal(provider.Body(), source) {
				t.Fatal("unmapped provider request was rewritten")
			}
			var wire struct{ Model string }
			if err := json.Unmarshal(provider.Body(), &wire); err != nil || wire.Model != model {
				t.Fatalf("provider model = %q, want %q: %v", wire.Model, model, err)
			}
		})
	}
}
