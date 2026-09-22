package providertransport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

func TestCodexOAuthAuthenticatorAtomicallyBindsAccessTokenAndAccount(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	accessToken := fakeChatGPTToken(`{"exp":1789988400,"https://api.openai.com/auth":{"chatgpt_account_id":"selected-account","chatgpt_account_is_fedramp":true}}`)
	authJSON, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt", "OPENAI_API_KEY": nil,
		"tokens": map[string]string{
			"id_token": accessToken, "access_token": accessToken,
			"refresh_token": "refresh-secret", "account_id": "selected-account",
		},
		"last_refresh": now.Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := codexoauth.ImportAuthJSON(authJSON)
	clear(authJSON)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	encoded, err := credential.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	secrets := testSecretReaderWithPolicy(
		t, string(encoded), map[string]string{"User-Agent": "managed-codex"}, nil,
	)
	authenticator, err := NewCodexOAuthAuthenticator(secrets)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(
		http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "chatgpt.com"
	request.Header.Set("Authorization", "Bearer client-token")
	request.Header.Set(chatGPTAccountHeader, "client-account")
	request.Header.Set("X-OpenAI-FedRAMP", "false")
	request.Header.Set("Cookie", "client-session")
	reference, err := secretstore.ParseReference("secret://provider/codex-oauth")
	if err != nil {
		t.Fatal(err)
	}
	origin, err := originidentity.ParseProviderOrigin("https://chatgpt.com")
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := authenticator.Apply(
		context.Background(), request, reference, 9, targetFromProviderOrigin(origin),
	)
	if err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("Authorization") != "Bearer "+accessToken ||
		request.Header.Get(chatGPTAccountHeader) != "selected-account" ||
		request.Header.Get("X-OpenAI-FedRAMP") != "true" ||
		request.Header.Get("Cookie") != "" || request.Header.Get("User-Agent") != "managed-codex" {
		t.Fatalf("Codex OAuth headers = %#v", request.Header)
	}
	for _, name := range []string{"Authorization", chatGPTAccountHeader, "X-Openai-Fedramp"} {
		if !slices.Contains(evidence.ProtectedHeaderNames, name) {
			t.Fatalf("protected headers = %#v, missing %s", evidence.ProtectedHeaderNames, name)
		}
	}
	if evidence.DriverRef != providerauth.CodexOAuthDriverRef().String() ||
		evidence.HeaderName != "authorization" || !evidence.SecretRead ||
		secrets.lastExpectedRevision() != 9 {
		t.Fatalf("credential evidence = %+v revision=%d", evidence, secrets.lastExpectedRevision())
	}
}

func TestAnthropicAPIKeyAuthenticatorUsesDedicatedHeader(t *testing.T) {
	t.Parallel()

	secrets := testSecretReader(t, "anthropic-secret")
	authenticator, err := NewAnthropicAPIKeyAuthenticator(secrets)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(
		http.MethodPost,
		"https://api.anthropic.com/v1/messages",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "api.anthropic.com"
	request.Header.Set("Authorization", "Bearer client-secret")
	request.Header.Set("Cookie", "client-session")
	reference, err := secretstore.ParseReference("secret://provider/account")
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := authenticator.Apply(
		context.Background(),
		request,
		reference,
		secretstore.Revision(7),
		testTarget("api.anthropic.com", 443),
	)
	if err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("X-Api-Key") != "anthropic-secret" ||
		request.Header.Get("Authorization") != "" ||
		request.Header.Get("Cookie") != "" {
		t.Fatalf("provider credential headers = %#v", request.Header)
	}
	if evidence.DriverRef != providerauth.AnthropicAPIKeyDriverRef().String() ||
		evidence.HeaderName != "x-api-key" ||
		!evidence.SecretRead {
		t.Fatalf("credential evidence = %+v", evidence)
	}
	if secrets.lastExpectedRevision() != 7 {
		t.Fatalf("credential revision = %d, want 7", secrets.lastExpectedRevision())
	}
}

func TestStaticBearerAuthenticatorUsesAuthorizationHeader(t *testing.T) {
	t.Parallel()

	secrets := testSecretReader(t, "oauth-access-token")
	authenticator, err := NewStaticBearerAuthenticator(secrets)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(
		http.MethodPost,
		"https://api.anthropic.com/v1/messages",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "api.anthropic.com"
	request.Header.Set("X-Api-Key", "client-api-key")
	request.Header.Set("Cookie", "client-session")
	reference, err := secretstore.ParseReference("secret://provider/claude-oauth")
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := authenticator.Apply(
		context.Background(),
		request,
		reference,
		secretstore.Revision(11),
		testTarget("api.anthropic.com", 443),
	)
	if err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("Authorization") != "Bearer oauth-access-token" ||
		request.Header.Get("X-Api-Key") != "" ||
		request.Header.Get("Cookie") != "" {
		t.Fatalf("provider credential headers = %#v", request.Header)
	}
	if evidence.DriverRef != providerauth.StaticHeaderDriverRef().String() ||
		evidence.HeaderName != "authorization" ||
		!evidence.SecretRead {
		t.Fatalf("credential evidence = %+v", evidence)
	}
	if secrets.lastExpectedRevision() != 11 {
		t.Fatalf("credential revision = %d, want 11", secrets.lastExpectedRevision())
	}
}

func TestAuthenticatorRefusesASecretFromAnotherCredentialRevision(t *testing.T) {
	t.Parallel()

	secrets := &secretReaderStub{
		value:    []byte("rotated-secret"),
		revision: 2,
	}
	authenticator, err := NewStaticBearerAuthenticator(secrets)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(
		http.MethodPost,
		"https://api.anthropic.com/v1/messages",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "api.anthropic.com"
	reference, err := secretstore.ParseReference("secret://provider/rotated")
	if err != nil {
		t.Fatal(err)
	}
	_, err = authenticator.Apply(
		context.Background(),
		request,
		reference,
		secretstore.Revision(1),
		testTarget("api.anthropic.com", 443),
	)
	if !errors.Is(err, secretstore.ErrRevisionConflict) {
		t.Fatalf("Apply() error = %v, want revision conflict", err)
	}
	if request.Header.Get("Authorization") != "" {
		t.Fatal("revision-conflicted credential reached the provider request")
	}
}
