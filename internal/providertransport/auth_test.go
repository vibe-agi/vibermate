package providertransport

import (
	"context"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

func TestAnthropicAPIKeyAuthenticatorUsesDedicatedHeader(t *testing.T) {
	t.Parallel()

	secrets := &secretReaderStub{value: []byte("anthropic-secret")}
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
	if evidence.DriverRef != access.AnthropicAPIKeyAuthDriverRef().String() ||
		evidence.HeaderName != "x-api-key" ||
		!evidence.SecretRead {
		t.Fatalf("credential evidence = %+v", evidence)
	}
}
