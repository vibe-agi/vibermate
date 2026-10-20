package productruntime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/cyberaccess"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
)

// Keep the Capture, CONNECT client, account settings and credentials unchanged.
// Only the external directory transport recovers between the two requests.
func TestCyberDiscoveryNetworkRecoveryThroughExistingCapture(t *testing.T) {
	wire := &cyberRecoveryWire{directoryError: errors.New("synthetic-private-network-detail")}
	f := newAccountReadFixtureWithBuilders(t, providerauth.StaticHeaderDriverRef(), runtimeBuildOverrides{
		provider: func(request providerBuildRequest) (providerRuntime, error) {
			auth, err := providertransport.NewStaticBearerAuthenticator(request.secrets)
			if err != nil {
				return nil, err
			}
			return providertransport.NewClient(providertransport.ClientOptions{
				Coordinator: request.coordinator, Authenticator: auth, Transport: wire,
				InstanceIDs: request.instanceIDs, Audit: request.audit,
				RawEvidence: request.rawEvidence, EgressDefaults: request.egressDefaults,
			})
		},
	})
	account := f.account.Account
	if _, err := f.runtime.accounts.SetSettings(context.Background(), provideraccount.SettingsCommand{
		ID: account.ID, ExpectedRevision: account.SettingsRevision,
		AutomaticRefresh: account.AutomaticRefresh, CyberAccessProgram: cyberaccess.Blue,
	}); err != nil {
		t.Fatal(err)
	}
	proxy := f.serveProxy(t)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(f.runtime.LocalRootCertificate().CertificatePEM()) {
		t.Fatal("fixture root invalid")
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	send := func() (int, http.Header, []byte) {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-native","input":"synthetic-prompt-do-not-echo","stream":false}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer synthetic-original-token")
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, response.Header, body
	}
	status, headers, body := send()
	if status != http.StatusServiceUnavailable || headers.Get("X-Should-Retry") == "false" || headers.Get("X-Vibermate-Reason") != "cyber_access_discovery_unavailable" {
		t.Fatalf("temporary discovery failure became terminal: status=%d headers=%v body=%s", status, headers, body)
	}
	for _, secret := range []string{"synthetic-private-network-detail", "synthetic-original-token", "synthetic-prompt-do-not-echo", f.token} {
		if strings.Contains(string(body), secret) {
			t.Fatal("discovery response exposed private request/transport details")
		}
	}
	wire.mu.Lock()
	reads, writes := wire.directoryRequests, wire.modelRequests
	wire.directoryError = nil
	wire.mu.Unlock()
	if writes != 0 || reads != 1 {
		t.Fatalf("failure sent model request or retried internally: reads=%d writes=%d", reads, writes)
	}
	status, _, body = send()
	if status != http.StatusOK || !strings.Contains(string(body), "recovered-reply") {
		t.Fatalf("existing Capture did not recover: status=%d body=%s", status, body)
	}
	wire.mu.Lock()
	defer wire.mu.Unlock()
	if wire.directoryRequests != 2 || wire.modelRequests != 1 || wire.modelProgram != "daybreak_blue" || wire.authorization != "Bearer "+f.token || wire.accountID != "workspace-B" {
		t.Fatal("recovery changed mode/account authority, reused failed discovery, or duplicated model execution")
	}
}

type cyberRecoveryWire struct {
	mu                                     sync.Mutex
	directoryError                         error
	directoryRequests, modelRequests       int
	modelProgram, authorization, accountID string
}

func (wire *cyberRecoveryWire) RoundTrip(request *http.Request, _ providertransport.TransportDispatch) (*http.Response, transportprofile.Evidence, error) {
	wire.mu.Lock()
	defer wire.mu.Unlock()
	if request.URL.Path == "/backend-api/codex/models" {
		wire.directoryRequests++
		if wire.directoryError != nil {
			return nil, transportprofile.Evidence{}, wire.directoryError
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(managedCodexCatalogFixture))}, transportprofile.Evidence{}, nil
	}
	if request.URL.Path != "/backend-api/codex/responses" {
		return nil, transportprofile.Evidence{}, errors.New("unexpected synthetic operation")
	}
	wire.modelRequests++
	wire.authorization, wire.accountID = request.Header.Get("Authorization"), request.Header.Get("Chatgpt-Account-Id")
	body, err := io.ReadAll(request.Body)
	_ = request.Body.Close()
	if err != nil {
		return nil, transportprofile.Evidence{}, err
	}
	var value struct {
		Access struct {
			Cyber string `json:"cyber"`
		} `json:"access_programs"`
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, transportprofile.Evidence{}, err
	}
	wire.modelProgram = value.Access.Cyber
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"resp_recovered","object":"response","model":"gpt-native","status":"completed","output":[{"id":"msg_reply","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"recovered-reply","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))}, transportprofile.Evidence{}, nil
}
