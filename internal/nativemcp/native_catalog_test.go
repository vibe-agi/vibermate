package nativemcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestNativeCatalogAuthenticatedObservationOnly(t *testing.T) {
	for _, mutation := range []string{"valid", "wrong_id", "wrong_session", "partial", "redirect", "unauthenticated"} {
		t.Run(mutation, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			for _, body := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`} {
				session := "synthetic-session"
				if strings.Contains(body, `"method":"initialize"`) {
					session = ""
				}
				terminal, _ := f.serve(t, body, session)
				if terminal.Outcome != OutcomeCompleted {
					t.Fatalf("handshake %+v", terminal)
				}
			}
			f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
				if r.URL.String() != "https://native.example/backend-api/ps/mcp" || r.Header.Get("Authorization") != "Bearer synthetic-original-login" || r.Header.Get("Mcp-Session-Id") != "synthetic-session" {
					t.Errorf("authenticated exact outgoing source was lost")
				}
				response := `{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`
				status := 200
				headers := http.Header{"Content-Type": []string{"application/json"}}
				switch mutation {
				case "wrong_id":
					response = strings.Replace(response, `"id":2`, `"id":"2"`, 1)
				case "wrong_session":
					headers.Set("Mcp-Session-Id", "other-session")
				case "partial":
					response = `{"jsonrpc":"2.0","id":2,"result":{"tools":[],"nextCursor":"unobserved-page"}}`
				case "redirect":
					status = 307
					headers.Set("Location", "https://other.invalid/")
				}
				return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(response))}, nil
			}
			request := f.captured(t, "POST", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "synthetic-session")
			if mutation == "unauthenticated" {
				request.request.Header.Del("Authorization")
			}
			w := httptest.NewRecorder()
			terminal := f.executor.Serve(context.Background(), request, w)
			e := f.executor.(*executor)
			e.mu.Lock()
			catalog := false
			for _, s := range e.scopes[f.capture.Key()].sessions {
				s.mu.Lock()
				catalog = catalog || s.catalog != nil
				s.mu.Unlock()
			}
			e.mu.Unlock()
			if catalog != (mutation == "valid") {
				t.Fatalf("catalog evidence for %s: %v terminal%+v", mutation, catalog, terminal)
			}
			if mutation == "unauthenticated" && f.transport.sends() != 2 {
				t.Fatal("unauthenticated list reached transport")
			}
		})
	}
}
