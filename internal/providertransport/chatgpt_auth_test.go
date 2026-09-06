package providertransport

import (
	"context"
	"encoding/base64"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

// Synthetic, unsigned fixture: no real account or usable access token.
func fakeChatGPTToken(claims string) string {
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".test-signature"
}

func TestChatGPTAccountIDOnlyReadsBoundedTokenMetadata(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, token, want string
	}{
		{"selected account", fakeChatGPTToken(`{"https://api.openai.com/auth":{"chatgpt_account_id":"selected-account"}}`), "selected-account"},
		{"opaque", "opaque-access-token", ""},
		{"malformed base64", "header.!.signature", ""},
		{"malformed json", fakeChatGPTToken(`{`), ""},
		{"missing claim", fakeChatGPTToken(`{"sub":"not-the-account"}`), ""},
		{"wrong claim type", fakeChatGPTToken(`{"https://api.openai.com/auth":{"chatgpt_account_id":42}}`), ""},
		{"header injection", fakeChatGPTToken(`{"https://api.openai.com/auth":{"chatgpt_account_id":"a\r\nb"}}`), ""},
		{"whitespace", fakeChatGPTToken(`{"https://api.openai.com/auth":{"chatgpt_account_id":" a"}}`), ""},
		{"oversized", fakeChatGPTToken(`{"https://api.openai.com/auth":{"chatgpt_account_id":"` + strings.Repeat("a", 513) + `"}}`), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := chatGPTAccountID([]byte(test.token)); got != test.want {
				t.Fatalf("account metadata = %q, want %q", got, test.want)
			}
		})
	}
}

func TestStaticBearerBindsChatGPTIdentityToSelectedCredential(t *testing.T) {
	t.Parallel()
	selectedToken := fakeChatGPTToken(`{"https://api.openai.com/auth":{"chatgpt_account_id":"selected-account"}}`)
	for _, test := range []struct {
		name, host, token, wantID string
		set                       map[string]string
		delete                    []string
	}{
		{name: "derive selected account", host: "chatgpt.com", token: selectedToken, wantID: "selected-account"},
		{name: "explicit account wins", host: "chatgpt.com", token: selectedToken, set: map[string]string{"chatgpt-account-id": "explicit-account"}, wantID: "explicit-account"},
		{name: "explicit delete wins", host: "chatgpt.com", token: selectedToken, delete: []string{"chatgpt-account-id"}},
		{name: "opaque never reuses client account", host: "chatgpt.com", token: "opaque-token"},
		{name: "opaque with explicit account", host: "chatgpt.com", token: "opaque-token", set: map[string]string{"ChatGPT-Account-Id": "explicit-account"}, wantID: "explicit-account"},
		{name: "API is not ChatGPT", host: "api.openai.com", token: selectedToken},
		{name: "relay is not ChatGPT", host: "relay.example", token: selectedToken},
		{name: "lookalike is not ChatGPT", host: "chatgpt.com.example", token: selectedToken},
	} {
		t.Run(test.name, func(t *testing.T) {
			secrets := testSecretReaderWithPolicy(t, test.token, test.set, test.delete)
			authenticator, err := NewStaticBearerAuthenticator(secrets)
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequest(http.MethodPost, "https://"+test.host+"/backend-api/codex/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer old-client-token")
			request.Header.Set(chatGPTAccountHeader, "old-client-account")
			request.Header.Set("Cookie", "old-client-session")
			reference, err := secretstore.ParseReference("secret://provider/selected-account")
			if err != nil {
				t.Fatal(err)
			}
			origin, err := originidentity.ParseProviderOrigin("https://" + test.host)
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := authenticator.Apply(context.Background(), request, reference, 7, targetFromProviderOrigin(origin))
			if err != nil {
				t.Fatal(err)
			}
			if request.Header.Get("Authorization") != "Bearer "+test.token || request.Header.Get("Cookie") != "" {
				t.Fatal("selected credential did not replace the old client credential")
			}
			if got := request.Header.Get(chatGPTAccountHeader); got != test.wantID {
				t.Fatalf("account header = %q, want %q", got, test.wantID)
			}
			if secrets.lastExpectedRevision() != 7 {
				t.Fatal("authentication did not read the frozen selected credential epoch")
			}
			if test.wantID != "" && !slices.Contains(evidence.ProtectedHeaderNames, chatGPTAccountHeader) {
				t.Fatal("selected account header is not protected from raw evidence")
			}
			stripProtectedCredentialHeaders(request.Header, evidence.ProtectedHeaderNames)
			if request.Header.Get(chatGPTAccountHeader) != "" || request.Header.Get("Authorization") != "" {
				t.Fatal("diagnostic headers retained selected credential metadata")
			}
		})
	}
}
