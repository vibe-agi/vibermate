package upstreamservice_test

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

func TestClientAccountReadScopeUsesExactAdapterContracts(t *testing.T) {
	t.Parallel()
	base, err := originidentity.ParseProviderOrigin("https://chatgpt.com/backend-api/codex")
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := originidentity.ParseClientOrigin("https://chatgpt.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		request protocolspec.RequestTarget
		allow   bool
	}{
		{"quota", protocolspec.RequestTarget{Method: "GET", Path: "/backend-api/wham/usage", Transport: "http"}, true},
		{"history", protocolspec.RequestTarget{Method: "GET", Path: "/backend-api/wham/profiles/me", Transport: "http"}, true},
		{"unknown sibling", protocolspec.RequestTarget{Method: "GET", Path: "/backend-api/wham/other", Transport: "http"}, false},
		{"reset list", protocolspec.RequestTarget{Method: "GET", Path: "/backend-api/wham/rate-limit-reset-credits", Transport: "http"}, false},
		{"reset consumption", protocolspec.RequestTarget{Method: "POST", Path: "/backend-api/wham/rate-limit-reset-credits/consume", Transport: "http"}, false},
		{"wrong method", protocolspec.RequestTarget{Method: "POST", Path: "/backend-api/wham/usage", Transport: "http"}, false},
		{"query override", protocolspec.RequestTarget{Method: "GET", Path: "/backend-api/wham/usage", RawQuery: "account_id=other", Transport: "http"}, false},
		{"subpath", protocolspec.RequestTarget{Method: "GET", Path: "/backend-api/wham/usage/other", Transport: "http"}, false},
		{"encoded path", protocolspec.RequestTarget{Method: "GET", Path: "/backend-api/wham/usage", RawPath: "/backend-api/wham/%75sage", Transport: "http"}, false},
		{"websocket", protocolspec.RequestTarget{Method: "GET", Path: "/backend-api/wham/usage", Transport: "websocket"}, false},
		{"other API family", protocolspec.RequestTarget{Method: "POST", Path: "/v1/responses", Transport: "http"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := upstreamservice.AllowsClientRead(base, canonical, test.request); got != test.allow {
				t.Fatalf("AllowsClientRead() = %t, want %t", got, test.allow)
			}
		})
	}
}

func TestClientAccountReadScopeDoesNotExpandCustomTargets(t *testing.T) {
	t.Parallel()
	request := protocolspec.RequestTarget{Method: "GET", Path: "/backend-api/wham/usage", Transport: "http"}
	for _, test := range []struct{ base, canonical string }{
		{"https://chatgpt.com/custom", "https://chatgpt.com"},
		{"https://chatgpt.com/backend-api/codex/subpath", "https://chatgpt.com"},
		{"https://chatgpt.com:444/backend-api/codex", "https://chatgpt.com"},
		{"https://chatgpt.com.example/backend-api/codex", "https://chatgpt.com"},
		{"https://gateway.example/backend-api/codex", "https://chatgpt.com"},
		{"http://127.0.0.1:443/backend-api/codex", "https://chatgpt.com"},
		{"https://chatgpt.com/backend-api/codex", "https://api.openai.com"},
		{"https://api.openai.com/v1", "https://api.openai.com"},
	} {
		t.Run(test.base+"->"+test.canonical, func(t *testing.T) {
			base, err := originidentity.ParseProviderOrigin(test.base)
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := originidentity.ParseClientOrigin(test.canonical)
			if err != nil {
				t.Fatal(err)
			}
			if upstreamservice.AllowsClientRead(base, canonical, request) {
				t.Fatal("account read escaped a custom client target scope")
			}
		})
	}
	if upstreamservice.AllowsClientRead(originidentity.ProviderOrigin{}, originidentity.ClientOrigin{}, request) {
		t.Fatal("invalid target admitted an account read")
	}
}
