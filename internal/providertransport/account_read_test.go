package providertransport

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

func TestAccountReadUsesManagedAuthenticationAndBodyFreeAudit(t *testing.T) {
	gate := newStartedGate(t)
	audit := &runtimeAuditRecorder{}
	transport := &runtimeTransportStub{audit: audit, response: &http.Response{
		StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"plan_type":"pro","rate_limit":null}`)),
	}}
	token := fakeChatGPTToken(`{"https://api.openai.com/auth":{"chatgpt_account_id":"managed-B"}}`)
	secret := testSecretReader(t, token)
	client := newRuntimeFetchClientWithSecret(t, gate, transport, audit, secret.value)
	endpoint := testDiscoveryEndpoint(t, "https://chatgpt.com")
	read, err := NewOwnedAccountRead(endpoint.Origin, upstreamservice.CodexRateLimits, testRuntimeDiscoveryCredential(t, endpoint.RealmID))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.ReadAccount(context.Background(), read)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	request := transport.lastRequest()
	if request.Method != "GET" || request.URL.String() != "https://chatgpt.com/backend-api/wham/usage" ||
		request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("Chatgpt-Account-Id") != "managed-B" {
		t.Fatal("account query did not use the exact managed account and read-only service contract")
	}
	started, terminal := audit.attempts()
	if started.Purpose() != egressaudit.PurposeUpstreamAccountRead || started.PayloadClass() != egressaudit.PayloadRuntime ||
		terminal.Outcome() != egressaudit.OutcomeCompleted || !transport.auditWasPresent() {
		t.Fatal("account read bypassed the runtime audit boundary")
	}
}
