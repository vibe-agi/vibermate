package responseschat

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/vibe-agi/vibermate/internal/openairesponses"
)

func TestNativeResponseAliasPreservesActualServerModel(t *testing.T) {
	for _, test := range []struct{ model, want string }{{"provider-model", "client-alias"}, {"restricted-model", "restricted-model"}} {
		t.Run(test.model, func(t *testing.T) {
			path, err := NewResponsesPassthroughProtocolPath(openairesponses.DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			request, _, err := path.Client().DecodeRequest([]byte(`{"model":"client-alias","input":"hello"}`))
			if err != nil {
				t.Fatal(err)
			}
			request, err = request.WithEffectiveModel("provider-model")
			if err != nil {
				t.Fatal(err)
			}
			body := []byte(`{ "id":"r", "created_at":1, "status":"completed", "model":"` + test.model + `", "output":[], "usage":{} }`)
			response, _, err := path.Backend().DecodeResponse(request, body)
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := path.EncodeClientResponse(request, response, body)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(got, &wire) != nil || wire.Model != test.want {
				t.Fatalf("downstream response = %s", got)
			}
			if response.ReportedModel != test.model || response.EffectiveModel != "provider-model" {
				t.Fatalf("upstream audit identity changed: %+v", response)
			}
			if test.model == test.want && !bytes.Equal(got, body) {
				t.Fatal("genuine reroute was rewritten")
			}
		})
	}
}
