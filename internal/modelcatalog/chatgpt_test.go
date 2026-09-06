package modelcatalog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestDiscoverChatGPTModelSlugsAreEndpointOwned(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, origin, body string
		want               []string
	}{
		{"native slugs", "https://chatgpt.com", `{"models":[{"slug":"model-b","display_name":"Model B"},{"slug":"model-a"}]}`, []string{"model-a", "model-b"}},
		{"native base path", "https://chatgpt.com/backend-api/codex", `{"models":[{"slug":"model-a"}]}`, []string{"model-a"}},
		{"standard API ids", "https://api.openai.com", `{"data":[{"id":"model-a"}]}`, []string{"model-a"}},
		{"standard API ignores unrelated slug metadata", "https://api.openai.com", `{"data":[{"id":"model-a","slug":{"unrelated":42}}]}`, []string{"model-a"}},
		{"no invented relay ids", "https://relay.example", `{"models":[{"slug":"model-a"}]}`, nil},
		{"non-string slug", "https://chatgpt.com", `{"models":[{"slug":42}]}`, nil},
		{"empty slug", "https://chatgpt.com", `{"models":[{"slug":""}]}`, nil},
		{"invalid slug", "https://chatgpt.com", `{"models":[{"slug":"model\ninvalid"}]}`, nil},
		{"oversized slug", "https://chatgpt.com", `{"models":[{"slug":"` + strings.Repeat("a", 257) + `"}]}`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			origin, err := originidentity.ParseProviderOrigin(test.origin)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := testEndpoint(origin)
			catalog, err := New(Options{
				Endpoints: endpointReaderStub{endpoint: endpoint}, Credentials: credentialAuthorityStub{},
				Clock: fixedClock{now: time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)},
				Transport: endpointTransportFunc(func(context.Context, upstreamendpoint.Endpoint) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(test.body))}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := catalog.Discover(context.Background(), endpoint.ID, testCatalogAccountID, false)
			if test.want == nil {
				if !errors.Is(err, ErrInvalidCatalog) {
					t.Fatalf("invalid model catalog error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.AvailabilitySource != AvailabilitySourceEndpoint || len(snapshot.Models) != len(test.want) {
				t.Fatalf("catalog = %+v", snapshot)
			}
			for i, model := range snapshot.Models {
				if model.ID != test.want[i] || !model.VerifiedAvailable {
					t.Fatalf("model = %+v, want exact endpoint slug %q", model, test.want[i])
				}
			}
		})
	}
}
