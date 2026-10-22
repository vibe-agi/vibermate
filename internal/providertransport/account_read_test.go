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

func TestOfficialAPIAccountReadUsesOnlyFixedModelCatalog(t *testing.T) {
	gate := newStartedGate(t)
	audit := &runtimeAuditRecorder{}
	const body = `{"models":[{"slug":"m","future":{"preserve":true}}],"future":true}`
	transport := &runtimeTransportStub{audit: audit, response: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}}
	client := newRuntimeFetchClient(t, gate, transport, audit)
	endpoint := testDiscoveryEndpoint(t, "https://api.openai.com/v1")
	credential := testRuntimeDiscoveryCredential(t, endpoint.RealmID)
	read, err := NewOwnedAccountRead(endpoint.Origin, upstreamservice.CodexModels, credential)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.ReadAccount(t.Context(), read)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(got) != body {
		t.Fatalf("catalog bytes changed: %s %v", got, err)
	}
	request := transport.lastRequest()
	if request.URL.String() != "https://chatgpt.com/backend-api/codex/models?client_version=0.163.0" || request.Header.Get("Authorization") != "Bearer catalog-test-token" {
		t.Fatalf("wrong catalog authority: %s", request.URL)
	}
	for _, operation := range []string{upstreamservice.CodexRateLimits, upstreamservice.CodexUsageHistory, upstreamservice.CodexResetCreditDetails} {
		if _, err := NewOwnedAccountRead(endpoint.Origin, operation, credential); err == nil {
			t.Fatalf("API account admitted %s", operation)
		}
	}
}

func TestOfficialAPIEndpointDiscoveryUsesCodexCapabilityCatalog(t *testing.T) {
	for _, capabilities := range []bool{false, true} {
		gate := newStartedGate(t)
		audit := &runtimeAuditRecorder{}
		transport := &runtimeTransportStub{audit: audit, response: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"models":[]}`))}}
		client := newRuntimeFetchClient(t, gate, transport, audit)
		endpoint := testDiscoveryEndpoint(t, "https://api.openai.com")
		response, err := client.FetchEndpointModels(t.Context(), endpoint, testRuntimeDiscoveryCredential(t, endpoint.RealmID), capabilities)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		want := "https://api.openai.com/v1/models"
		if capabilities {
			want = "https://chatgpt.com/backend-api/codex/models?client_version=0.163.0"
		}
		if got := transport.lastRequest().URL.String(); got != want {
			t.Fatalf("official capability catalog=%s", got)
		}
	}

}
