package modelcatalog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

// Dropping the server's per-model programs must not make an entitled account
// indistinguishable from an account whose capabilities are unknown.
func TestDiscoverPreservesAccountCyberPrograms(t *testing.T) {
	testDiscoverCyberPrograms(t, "https://chatgpt.com")
}
func TestOfficialAPIDiscoverPreservesAccountCyberPrograms(t *testing.T) {
	testDiscoverCyberPrograms(t, "https://api.openai.com/v1")
}
func testDiscoverCyberPrograms(t *testing.T, raw string) {
	origin, _ := originidentity.ParseProviderOrigin(raw)
	endpoint := testEndpoint(origin)
	catalog, err := New(Options{
		Endpoints: endpointReaderStub{endpoint: endpoint}, Credentials: credentialAuthorityStub{settingsRevision: 1},
		Clock: fixedClock{now: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)},
		Transport: endpointTransportFunc(func(context.Context, upstreamendpoint.Endpoint) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"models":[
				{"slug":"blue","available_access_programs":{"cyber":["standard","daybreak_blue"]}},
				{"slug":"standard","available_access_programs":{"cyber":["standard"]}},
				{"slug":"unknown"}, {"slug":"empty","available_access_programs":{"cyber":[]}}
			]}`))}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Discover(context.Background(), endpoint.ID, testCatalogAccountID, false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"blue": []any{"standard", "daybreak_blue"}, "standard": []any{"standard"}, "unknown": nil, "empty": []any{}}
	for _, model := range snapshot.Models {
		wire, err := json.Marshal(model)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(wire, &decoded); err != nil {
			t.Fatal(err)
		}
		programs, present := decoded["availableCyberPrograms"]
		if !present || !reflect.DeepEqual(programs, want[model.ID]) {
			t.Fatalf("model %s programs = %#v (present %v), want %#v", model.ID, programs, present, want[model.ID])
		}
	}
}
