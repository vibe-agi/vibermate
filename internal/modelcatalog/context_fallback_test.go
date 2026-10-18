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

func TestDiscoverCodexContextWindowFallback(t *testing.T) {
	for _, test := range []struct {
		name, fields string
		want         int64
		invalid      bool
	}{
		{"maximum fallback", `"max_context_window":400000`, 400000, false},
		{"explicit context first", `"context_window":200000,"max_context_window":400000`, 200000, false},
		{"generic length first", `"max_model_len":100000,"context_window":200000,"max_context_window":400000`, 100000, false},
		{"negative maximum", `"max_context_window":-1`, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			origin, err := originidentity.ParseProviderOrigin("https://chatgpt.com")
			if err != nil {
				t.Fatal(err)
			}
			endpoint := testEndpoint(origin)
			catalog, err := New(Options{
				Endpoints: endpointReaderStub{endpoint: endpoint}, Credentials: credentialAuthorityStub{settingsRevision: 1},
				Transport: endpointTransportFunc(func(context.Context, upstreamendpoint.Endpoint) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"opaque-model",` + test.fields + `}]}`))}, nil
				}), Clock: fixedClock{now: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)},
			})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := catalog.Discover(context.Background(), endpoint.ID, testCatalogAccountID, true)
			if test.invalid {
				if !errors.Is(err, ErrInvalidCatalog) {
					t.Fatalf("invalid maximum accepted: %v", err)
				}
				return
			}
			if err != nil || len(snapshot.Models) != 1 || snapshot.Models[0].ContextLimit != test.want {
				t.Fatalf("context fallback: snapshot=%+v err=%v", snapshot, err)
			}
		})
	}
}
