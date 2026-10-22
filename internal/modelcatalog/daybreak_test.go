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
	"github.com/vibe-agi/vibermate/internal/providerauth"
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
	snapshot, err := catalog.Discover(context.Background(), endpoint.ID, testCatalogAccountID, false, true)
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

// The generic picker retains the whole API directory; a capability request
// explicitly selects the Codex directory and must not share its cache entry.
func TestOfficialAPICatalogOptInKeepsGenericModelsAndCacheSeparate(t *testing.T) {
	origin, _ := originidentity.ParseProviderOrigin("https://api.openai.com")
	endpoint := testEndpoint(origin)
	var modes []bool
	service, err := New(Options{Endpoints: endpointReaderStub{endpoint: endpoint}, Credentials: credentialAuthorityStub{settingsRevision: 1}, Clock: fixedClock{now: time.Now()}, Transport: catalogModeTransport(func(capabilities bool) (*http.Response, error) {
		modes = append(modes, capabilities)
		body := `{"data":[{"id":"non-codex-platform-model"}]}`
		if capabilities {
			body = `{"models":[{"slug":"codex-model","available_access_programs":{"cyber":["daybreak_blue"]}}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []bool{false, true, false, true} {
		snapshot, err := service.Discover(t.Context(), endpoint.ID, testCatalogAccountID, false, mode)
		if err != nil || len(snapshot.Models) != 1 {
			t.Fatalf("mode=%v snapshot=%+v err=%v", mode, snapshot, err)
		}
		want := "non-codex-platform-model"
		if mode {
			want = "codex-model"
		}
		if snapshot.Models[0].ID != want {
			t.Fatalf("mode=%v returned %s", mode, snapshot.Models[0].ID)
		}
	}
	if !reflect.DeepEqual(modes, []bool{false, true}) {
		t.Fatalf("catalog cache mixed modes: %v", modes)
	}
	custom, _ := originidentity.ParseProviderOrigin("https://custom.example/v1")
	service.endpoints = endpointReaderStub{endpoint: testEndpoint(custom)}
	if _, err := service.Discover(t.Context(), endpoint.ID, testCatalogAccountID, true, true); err == nil || len(modes) != 2 {
		t.Fatal("custom capability opt-in reached transport")
	}
}

type catalogModeTransport func(bool) (*http.Response, error)

func (f catalogModeTransport) FetchEndpointModels(_ context.Context, _ upstreamendpoint.Endpoint, _ providerauth.Lease, capabilities ...bool) (*http.Response, error) {
	return f(len(capabilities) == 1 && capabilities[0])
}
