package upstreamendpoint

import (
	"net/http"
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
				wantQuery = "client_version=0.153.4"
			}
			if got := ModelsQuery(origin, nil); got != wantQuery {
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

func TestModelsQueryUsesOnlyAnExplicitBoundedCodexVersion(t *testing.T) {
	chatGPT, _ := originidentity.ParseProviderOrigin("https://chatgpt.com")
	api, _ := originidentity.ParseProviderOrigin("https://api.openai.com")
	for _, test := range []struct{ name, version, ua, want string }{
		{"fallback", "", "", "0.153.4"},
		{"account Version", "0.160.1", "codex-tui/0.159.0 (Mac OS)", "0.160.1"},
		{"account UA", "", "codex-tui/0.159.2 (Mac OS; arm64)", "0.159.2"},
		{"CLI UA", "", "codex_cli_rs/0.159.3 (Linux)", "0.159.3"},
		{"codex UA", "", "codex/0.159.4", "0.159.4"},
		{"prerelease", "0.159.0-alpha.2+test", "", "0.159.0"},
		{"older explicit version", "0.140.0", "", "0.140.0"},
		{"arbitrary UA is not a Codex version", "", "private-app/99.1.0", "0.153.4"},
		{"query injection", "0.159.0&token=private", "", "0.153.4"},
		{"unbounded version", "999999999999999999999.1.0", "", "0.153.4"},
		{"invalid Version can use valid UA", "private-value", "codex-tui/0.159.2", "0.159.2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := http.Header{"Version": {test.version}, "User-Agent": {test.ua}}
			if got := ModelsQuery(chatGPT, headers); got != "client_version="+test.want {
				t.Fatalf("query = %q", got)
			}
			if got := ModelsQuery(api, headers); got != "" {
				t.Fatalf("Codex negotiation leaked to API service: %q", got)
			}
		})
	}
}
