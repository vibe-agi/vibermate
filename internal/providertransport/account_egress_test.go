package providertransport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

func testAccountEgress() egressprofile.ProfileRevision {
	profile := egressprofile.Direct()
	profile.ID = "profile.us"
	profile.DisplayName = "US"
	profile.Revision = 3
	profile.Policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:1080"}
	return profile
}

func TestProviderAccountEgressOverridesRouteAndIsFrozen(t *testing.T) {
	for _, profile := range []egressprofile.ProfileRevision{testAccountEgress(), egressprofile.Direct()} {
		t.Run(profile.ID.String(), func(t *testing.T) {
			options := validRequestOptions(t)
			options.EgressProfile = testAccountEgress()
			options.EgressProfile.Policy.Proxy.Endpoint = "127.0.0.1:9090"
			options.AccountRef.EgressProfile = profile
			options.AccountRef.SettingsRevision = 2
			frozen, err := NewRequest(options)
			if err != nil {
				t.Fatal(err)
			}
			options.AccountRef.EgressProfile = egressprofile.Direct()
			if frozen.EgressPolicy() != profile.Policy || frozen.probeTarget.EgressPolicy != profile.Policy {
				t.Fatal("attempt or admission ignored account egress")
			}
			decision := providerEgressDecision(frozen)
			if decision.ProxyID != profile.ID.String() || decision.ProxyRevision != uint64(profile.Revision) || decision.AccountID != options.AccountRef.ID || decision.AccountSettingsRevision != 2 {
				t.Fatal("audit misreported the effective account egress")
			}
		})
	}
}

func TestRuntimeAccountOperationsUseAccountEgressAndNeverFallback(t *testing.T) {
	for _, operation := range []string{"oauth", "quota", "history", "models"} {
		for _, fail := range []bool{false, true} {
			name := operation
			if fail {
				name += "/unavailable"
			}
			t.Run(name, func(t *testing.T) {
				audit := &runtimeAuditRecorder{}
				transport := &runtimeTransportStub{audit: audit, response: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader("{}"))}}
				if fail {
					transport.err = errors.New("synthetic proxy unavailable")
				}
				client := newRuntimeFetchClient(t, newStartedGate(t), transport, audit)
				endpoint := testDiscoveryEndpoint(t, "https://chatgpt.com")
				credential := testRuntimeDiscoveryCredential(t, endpoint.RealmID).(*runtimeDiscoveryCredential)
				credential.account.EgressProfile = testAccountEgress()
				credential.account.SettingsRevision = 4
				var response *http.Response
				var err error
				switch operation {
				case "oauth":
					request, _ := http.NewRequest(http.MethodPost, codexoauth.TokenURL, strings.NewReader(`{"grant_type":"refresh_token","refresh_token":"synthetic"}`))
					request.Header.Set("Content-Type", "application/json")
					response, err = client.DoCodexOAuthTokenRequest(request, credential.account)
				case "models":
					response, err = client.FetchEndpointModels(context.Background(), endpoint, credential)
				default:
					op := upstreamservice.CodexRateLimits
					if operation == "history" {
						op = upstreamservice.CodexUsageHistory
					}
					read, createErr := NewOwnedAccountRead(endpoint.Origin, op, credential)
					if createErr != nil {
						t.Fatal(createErr)
					}
					response, err = client.ReadAccount(context.Background(), read)
				}
				if fail && err == nil || !fail && err != nil {
					t.Fatalf("operation error: %v", err)
				}
				if response != nil {
					_, _ = io.Copy(io.Discard, response.Body)
					response.Body.Close()
				}
				if transport.callCount() != 1 || transport.dispatch.egressPolicy != credential.account.EgressProfile.Policy {
					t.Fatal("account operation ignored proxy or silently retried direct")
				}
				started, _ := audit.attempts()
				if started.Decision().ProxyID != credential.account.EgressProfile.ID.String() || started.Decision().PolicyID != credential.account.ID || started.Decision().PolicyRevision != 4 || started.Decision().AccountSettingsRevision != 4 || started.Decision().ProxyRevision != 3 || started.Decision().AccountID != credential.account.ID {
					t.Fatal("account operation lost settings provenance")
				}
				if operation == "oauth" && transport.lastRequest().Header.Get("Authorization") != "" {
					t.Fatal("account access token leaked to refresh endpoint")
				}
			})
		}
	}
}

func TestMalformedAccountEgressIsRejectedBeforeTransport(t *testing.T) {
	audit := &runtimeAuditRecorder{}
	transport := &runtimeTransportStub{audit: audit}
	client := newRuntimeFetchClient(t, newStartedGate(t), transport, audit)
	request, _ := http.NewRequest(http.MethodPost, codexoauth.TokenURL, strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	scope := providerauth.AccountRef{ID: "account", Revision: 1, CredentialEpoch: 1, SettingsRevision: 1, RealmID: "openai.chatgpt", EgressProfile: testAccountEgress()}
	scope.EgressProfile.Revision = 0
	if _, err := client.DoCodexOAuthTokenRequest(request, scope); err == nil || transport.callCount() != 0 {
		t.Fatal("malformed egress escaped or fell back to direct")
	}
}
