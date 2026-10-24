package providertransport

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/testrecording"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

func TestTransportSelectionCannotBeBorrowedForManagedTLSRequest(t *testing.T) {
	for _, origin := range []string{"https://provider.example", "https://127.0.0.1:9443"} {
		t.Run(origin, func(t *testing.T) {
			options := validRequestOptions(t)
			parsed, err := originidentity.ParseProviderOrigin(origin)
			if err != nil {
				t.Fatal(err)
			}
			options.Target, err = NewTarget(parsed)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionTransport, 0)
			if err != nil {
				t.Fatal(err)
			}
			options.AccountRef.ResolvedEgress = resolved
			if _, err = NewRequest(options); err == nil {
				t.Fatal("local-only lease was accepted for a strict-TLS model target")
			}
		})
	}
}

func TestTransportSelectionRuntimeConsumersRefuseBeforeAdmission(t *testing.T) {
	for _, operation := range []string{"models", "https_loopback", "refresh", "login"} {
		t.Run(operation, func(t *testing.T) {
			gate := newStartedGate(t)
			secrets := testSecretReader(t, "synthetic-borrow-refused")
			auth, err := NewStaticBearerAuthenticator(secrets)
			if err != nil {
				t.Fatal(err)
			}
			audit := &runtimeAuditRecorder{}
			ids := &sequentialInstanceIDs{}
			transport := &runtimeTransportStub{audit: audit, err: errors.New("unexpected outbound")}
			client, err := NewClient(ClientOptions{Coordinator: gate, Authenticator: auth, Transport: transport, Audit: testrecording.Egress(audit), InstanceIDs: ids})
			if err != nil {
				t.Fatal(err)
			}
			defer shutdownClient(t, client)
			origin := "https://provider.example"
			if operation == "https_loopback" {
				origin = "https://127.0.0.1:9443"
			}
			endpoint := testDiscoveryEndpoint(t, origin)
			credential := testRuntimeDiscoveryCredential(t, endpoint.RealmID).(*runtimeDiscoveryCredential)
			credential.account.ResolvedEgress, err = egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionTransport, 0)
			if err != nil {
				t.Fatal(err)
			}
			var response *http.Response
			if operation == "models" || operation == "https_loopback" {
				response, err = client.FetchEndpointModels(t.Context(), endpoint, credential, false)
			} else {
				request, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, codexoauth.TokenURL, strings.NewReader(`{"grant_type":"synthetic"}`))
				request.Header.Set("Content-Type", "application/json")
				if operation == "refresh" {
					response, err = client.DoCodexOAuthTokenRequest(request, credential.account)
				} else {
					response, err = client.ExchangeLoginCode(request, credential.account.ResolvedEgress)
				}
			}
			if response != nil {
				response.Body.Close()
			}
			ids.mu.Lock()
			allocated := ids.next
			ids.mu.Unlock()
			if err == nil || response != nil || allocated != 0 || secrets.readCount() != 0 || transport.callCount() != 0 || audit.appendCount() != 0 {
				t.Fatalf("borrowed transport selection reached work: error=%t ids=%d secretReads=%d transport=%d audit=%d", err != nil, allocated, secrets.readCount(), transport.callCount(), audit.appendCount())
			}
		})
	}
}
