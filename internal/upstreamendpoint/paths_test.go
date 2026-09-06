package upstreamendpoint

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/originidentity"
)

func TestServicePathsDistinguishChatGPTFromResponsesAPIs(t *testing.T) {
	for _, test := range []struct {
		origin, responsePath, modelsPath string
		codex                            bool
	}{
		{"https://chatgpt.com", "backend-api/codex/responses", "/backend-api/codex/models", true},
		{"https://chatgpt.com/backend-api", "codex/responses", "/backend-api/codex/models", true},
		{"https://chatgpt.com/backend-api/codex", "responses", "/backend-api/codex/models", true},
		{"https://api.openai.com", "v1/responses", "/v1/models", false},
		{"https://relay.example/api/v1", "v1/responses", "/api/v1/models", false},
		{"https://relay.example/backend-api/codex", "v1/responses", "/backend-api/codex/v1/models", false},
		{"https://chatgpt.com.example", "v1/responses", "/v1/models", false},
		{"https://chatgpt.com:8443", "v1/responses", "/v1/models", false},
		{"https://chatgpt.com/other", "v1/responses", "/other/v1/models", false},
	} {
		t.Run(test.origin, func(t *testing.T) {
			origin, err := originidentity.ParseProviderOrigin(test.origin)
			if err != nil {
				t.Fatal(err)
			}
			if got := ProviderRelativePath(origin, "v1/responses"); got != test.responsePath {
				t.Fatalf("response path = %s", got)
			}
			if got := ModelsPath(origin); got != test.modelsPath {
				t.Fatalf("models path = %s", got)
			}
			wantQuery := ""
			if test.codex {
				wantQuery = "client_version=0.147.0"
			}
			if got := ModelsQuery(origin); got != wantQuery {
				t.Fatalf("models query = %q, want %q", got, wantQuery)
			}
			if got := IsChatGPTCodexOrigin(origin); got != test.codex {
				t.Fatalf("Codex = %v", got)
			}
			if got := ProviderRelativePath(origin, "v1/chat/completions"); got != "v1/chat/completions" {
				t.Fatalf("unrelated operation changed: %s", got)
			}
		})
	}
}
