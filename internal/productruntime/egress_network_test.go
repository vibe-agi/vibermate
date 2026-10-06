package productruntime

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type runtimeClosedNetwork struct {
	proxy  string
	denied atomic.Int64
}

func (n *runtimeClosedNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if address != n.proxy {
		n.denied.Add(1)
		return nil, errors.New("private fixture rejected direct/unexpected dial")
	}
	return (&net.Dialer{}).DialContext(ctx, network, address)
}
func (*runtimeClosedNetwork) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	if network != "ip" || (host != "chatgpt.com" && host != "auth.openai.com") {
		return nil, errors.New("private fixture rejected DNS")
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

type runtimeOriginalWire struct {
	builder *egressnetwork.Builder
	roots   *x509.CertPool
	before  func()
}

func (p *runtimeOriginalWire) RoundTrip(r *http.Request, policy egressnetwork.Policy) (*http.Response, error) {
	allowed := r.URL.Host == "chatgpt.com" && (r.URL.Path == "/backend-api/wham/usage" || r.URL.Path == "/backend-api/codex/responses") || r.URL.Host == "auth.openai.com" && r.URL.Path == "/oauth/token"
	if r.URL.Scheme != "https" || !allowed {
		return nil, errors.New("private original fixture rejected destination")
	}
	if p.before != nil {
		p.before()
	}
	dialer, err := p.builder.Dialer(policy)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{DialContext: dialer.DialContext, DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: p.roots, MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: time.Second}
	return transport.RoundTrip(r)
}

type runtimeProviderWire struct{ original *runtimeOriginalWire }

func (p runtimeProviderWire) RoundTrip(r *http.Request, dispatch providertransport.TransportDispatch) (*http.Response, transportprofile.Evidence, error) {
	response, err := p.original.RoundTrip(r, dispatch.EgressPolicy())
	return response, transportprofile.Evidence{}, err
}
func runtimeContainedProvider(request providerBuildRequest, wire *runtimeOriginalWire) (providerRuntime, error) {
	bearer, err := providertransport.NewStaticBearerAuthenticator(request.secrets)
	if err != nil {
		return nil, err
	}
	anthropic, err := providertransport.NewAnthropicAPIKeyAuthenticator(request.secrets)
	if err != nil {
		return nil, err
	}
	codex, err := providertransport.NewCodexOAuthAuthenticator(request.secrets)
	if err != nil {
		return nil, err
	}
	return providertransport.NewClient(providertransport.ClientOptions{EgressDefaults: request.egressDefaults, Coordinator: request.coordinator, Authenticators: []providertransport.Authenticator{bearer, anthropic, codex}, Transport: runtimeProviderWire{original: wire}, InstanceIDs: request.instanceIDs, Audit: request.audit, RawEvidence: request.rawEvidence})
}

func runtimeSOCKSFixture(t *testing.T, upstream string) (net.Listener, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	count := &atomic.Int64{}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer c.Close()
				c.SetDeadline(time.Now().Add(3 * time.Second))
				greeting := make([]byte, 3)
				if _, err := io.ReadFull(c, greeting); err != nil || string(greeting) != "\x05\x01\x00" {
					return
				}
				if _, err := c.Write([]byte{5, 0}); err != nil {
					return
				}
				request := make([]byte, 10)
				if _, err := io.ReadFull(c, request); err != nil || string(request) != "\x05\x01\x00\x01\x7f\x00\x00\x01\x01\xbb" {
					return
				}
				peer, err := net.DialTimeout("tcp4", upstream, time.Second)
				if err != nil {
					return
				}
				defer peer.Close()
				peer.SetDeadline(time.Now().Add(3 * time.Second))
				count.Add(1)
				if _, err := c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
					return
				}
				done := make(chan struct{})
				go func() { io.Copy(peer, c); peer.Close(); close(done) }()
				io.Copy(c, peer)
				c.Close()
				peer.Close()
				<-done
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); workers.Wait() })
	return listener, count
}

func TestActualRuntimeOriginalAccountReadUsesFrozenGlobalExitThroughSOCKSTLS(t *testing.T) {
	runRuntimeInheritedSOCKSTLS(t, false, false, false)
}
func TestActualRuntimeOriginalCredentialModelUsesFrozenGlobalExitThroughSOCKSTLS(t *testing.T) {
	runRuntimeInheritedSOCKSTLS(t, true, false, false)
}
func TestActualRuntimeManagedRefreshAndModelShareFrozenGlobalExitThroughSOCKSTLS(t *testing.T) {
	runRuntimeInheritedSOCKSTLS(t, true, true, false)
}
func TestActualRuntimeExplicitAccountRefreshAndModelIgnoreUnavailableGlobal(t *testing.T) {
	runRuntimeInheritedSOCKSTLS(t, true, true, true)
}
func runRuntimeInheritedSOCKSTLS(t *testing.T, model, managed, accountOverride bool) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"chatgpt.com", "auth.openai.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	method, path, body := "GET", "/backend-api/wham/usage", ""
	if model {
		method, path, body = "POST", "/backend-api/codex/responses", `{"model":"synthetic","input":"hello","stream":false}`
	}
	var upstreamCalls, refreshCalls, bad atomic.Int64
	token := func(expiry time.Time) string {
		return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":"synthetic-runtime-account"}}`, expiry.Unix()))) + ".synthetic"
	}
	newToken := token(time.Now().Add(time.Hour))
	wantAuthorization := "Bearer original-synthetic-login"
	if managed {
		wantAuthorization = "Bearer " + newToken
	}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if managed && r.Host == "auth.openai.com" && r.URL.Path == "/oauth/token" {
			refreshCalls.Add(1)
			var payload map[string]string
			if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&payload) != nil || len(payload) != 3 || payload["grant_type"] != "refresh_token" || payload["refresh_token"] != "synthetic-refresh-old" || payload["client_id"] != codexoauth.ClientID {
				bad.Add(1)
				w.WriteHeader(400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"synthetic-refresh-new","expires_in":3600}`, newToken)
			return
		}
		encoded, _ := io.ReadAll(r.Body)
		upstreamCalls.Add(1)
		bodyMatches := string(encoded) == body
		if managed {
			var fields map[string]json.RawMessage
			bodyMatches = json.Unmarshal(encoded, &fields) == nil && len(fields) == 3 && string(fields["model"]) == `"synthetic"` && string(fields["input"]) == `"hello"` && string(fields["stream"]) == "false"
		}
		if r.Method != method || r.Host != "chatgpt.com" || r.URL.RequestURI() != path || r.Header.Get("Authorization") != wantAuthorization || !bodyMatches || r.Header.Get("Proxy-Authorization") != "" {
			bad.Add(1)
			w.WriteHeader(400)
			return
		}
		if managed && r.Header.Get("ChatGPT-Account-Id") != "synthetic-runtime-account" {
			bad.Add(1)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"synthetic-response","object":"response","created_at":1,"model":"synthetic","status":"completed","output":[]}`)
	}))
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	upstream.StartTLS()
	defer upstream.Close()
	proxy, calls := runtimeSOCKSFixture(t, upstream.Listener.Addr().String())
	network := &runtimeClosedNetwork{proxy: proxy.Addr().String()}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network})
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	wire := &runtimeOriginalWire{builder: builder, roots: roots}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions(t, hostcontract.Desktop(), gate)
	options.OriginalTransport = wire
	r, err := startWithBuilders(t.Context(), options, runtimeBuildOverrides{provider: func(request providerBuildRequest) (providerRuntime, error) {
		return runtimeContainedProvider(request, wire)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRuntime(t, r)
	policy := egressnetwork.DefaultPolicy()
	policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: proxy.Addr().String()}
	profile, err := r.EgressProfiles().Publish(t.Context(), egressprofile.PublishCommand{ID: "profile.runtime-original", DisplayName: "Private original", Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}}); err != nil {
		t.Fatal(err)
	}
	grant, err := r.ManualCaptures().Create(t.Context(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Private original capture", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if err != nil {
		t.Fatal(err)
	}
	capture, err := captureidentity.New(captureidentity.KindManualCapture, grant.Capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	environmentID := environment.SystemTransparentID
	if managed {
		material, err := codexoauth.ImportAuthJSON([]byte(fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q,"id_token":%q,"refresh_token":"synthetic-refresh-old","account_id":"synthetic-runtime-account"},"last_refresh":%q}`, token(time.Now().Add(-time.Minute)), token(time.Now().Add(-time.Minute)), time.Now().Add(-time.Hour).Format(time.RFC3339Nano))))
		if err != nil {
			t.Fatal(err)
		}
		defer material.Destroy()
		encoded, err := material.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		defer clear(encoded)
		authMaterial, err := providerauth.NewMaterial(string(encoded), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer authMaterial.Destroy()
		secretBytes, err := authMaterial.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		defer clear(secretBytes)
		value, err := secretstore.NewValue(secretBytes)
		if err != nil {
			t.Fatal(err)
		}
		defer value.Destroy()
		account, err := r.ProviderAccounts().Create(t.Context(), provideraccount.CreateCommand{ID: "account.private-runtime", DisplayName: "Private managed", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.CodexOAuthDriverRef(), AutomaticRefresh: true, Secret: value})
		if err != nil {
			t.Fatal(err)
		}
		if accountOverride {
			if _, err := r.accounts.SetSettings(t.Context(), provideraccount.SettingsCommand{ID: account.Account.ID, ExpectedRevision: account.Account.SettingsRevision, AutomaticRefresh: true, EgressProfile: profile}); err != nil {
				t.Fatal(err)
			}
		}
		endpoint, err := r.UpstreamEndpoints().Get(t.Context(), upstreamendpoint.ChatGPTOfficialID)
		if err != nil {
			t.Fatal(err)
		}
		aggregate, _, _ := compatibilityEnvironment(t, environment.ClientProtocolOpenAIResponses, protocolspec.DialectOpenAIResponses)
		aggregate.ID = "managed.network"
		aggregate.Name = "Private managed network"
		origin, err := originidentity.ParseClientOrigin("https://chatgpt.com")
		if err != nil {
			t.Fatal(err)
		}
		aggregate.ClientEndpoints[0].ClientOrigin = origin
		plan := &aggregate.ClientEndpoints[0].ProtocolPlans[0]
		plan.EgressProfile = egressprofile.Inherit()
		route := &plan.Destination.Upstream.Routes[0]
		route.ProviderTarget = environment.ProviderTarget{ID: endpoint.ID.String(), Revision: environment.Revision(endpoint.Revision), Origin: endpoint.Origin, RealmID: endpoint.RealmID, Capabilities: []protocolspec.ProviderCapability{protocolspec.ProviderCapabilityMessages, protocolspec.ProviderCapabilityStreaming, protocolspec.ProviderCapabilityToolCalls}}
		route.BackendProtocol = endpoint.BackendProtocols[0]
		route.AccountPolicy.FixedAccountID = account.Account.ID.String()
		route.AccountPolicy.Accounts = []environment.RouteAccountReference{{ID: account.Account.ID.String(), Revision: environment.Revision(account.Account.Revision), DisplayName: account.Account.DisplayName}}
		draft, err := r.Environments().SaveDraft(t.Context(), environment.DraftCommand{Candidate: aggregate})
		if err != nil {
			t.Fatal(err)
		}
		preview, err := r.Environments().Preview(t.Context(), aggregate.ID, draft.Revision)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Environments().Publish(t.Context(), preview); err != nil {
			t.Fatal(err)
		}
		environmentID = aggregate.ID
	}
	if _, err := r.CaptureAssignments().Create(t.Context(), captureassignment.CreateCommand{Capture: capture, EnvironmentID: environmentID, Source: captureassignment.SourceSystemTransparent}); err != nil {
		t.Fatal(err)
	}
	if accountOverride {
		// Isolated persisted configuration corruption only, never session,
		// credential, binding, once or approval authority. Preserve/restore row.
		db, err := sql.Open("sqlite", options.Paths.DatabasePath())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`DELETE FROM runtime_egress_default WHERE singleton=1`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := db.Exec(`INSERT INTO runtime_egress_default(singleton,revision,egress_id,egress_revision) VALUES(1,2,?,?)`, profile.ID, profile.Revision); err != nil {
				t.Error("restore synthetic default fault", err)
			}
		}()
		if _, err := r.EgressDefaults().GetDefault(t.Context()); err == nil {
			t.Fatal("private persisted default fault was not unavailable")
		}
	}
	listener := httptest.NewServer(r.ProxyHandler())
	defer listener.Close()
	proxyURL, err := url.Parse(listener.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.User = url.UserPassword("capture", grant.Credential.Value())
	clientRoots := x509.NewCertPool()
	if !clientRoots.AppendCertsFromPEM(r.LocalRootCertificate().CertificatePEM()) {
		t.Fatal("private Runtime Root invalid")
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: clientRoots, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != listener.Listener.Addr().String() {
			return nil, errors.New("private client denied other destination")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	transport.OnProxyConnectResponse = func(_ context.Context, _ *url.URL, _ *http.Request, response *http.Response) error {
		if response.StatusCode != 200 {
			encoded, _ := io.ReadAll(response.Body)
			t.Logf("actual private CONNECT status=%d body=%s", response.StatusCode, encoded)
		}
		return nil
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	if !accountOverride {
		wire.before = func() {
			wire.before = nil
			if _, err := r.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 2}); err != nil {
				t.Error(err)
			}
		}
	}
	request, err := http.NewRequestWithContext(t.Context(), method, "https://chatgpt.com"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer original-synthetic-login")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	reply, _ := io.ReadAll(response.Body)
	response.Body.Close()
	wantWires := int64(1)
	if managed {
		wantWires = 2
	}
	if response.StatusCode != 200 || !strings.Contains(string(reply), "synthetic-response") || calls.Load() != wantWires || upstreamCalls.Load() != 1 || bad.Load() != 0 || network.denied.Load() != 0 {
		t.Fatalf("actual original wire status%d reply%s proxy%d upstream%d refresh%d bad%d direct%d", response.StatusCode, reply, calls.Load(), upstreamCalls.Load(), refreshCalls.Load(), bad.Load(), network.denied.Load())
	}
	page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != int(wantWires) {
		t.Fatalf("actual Runtime audit %+v %v", page, err)
	}
	expectedSource, expectedRevision := egressprofile.SelectionGlobal, uint64(2)
	if accountOverride {
		expectedSource, expectedRevision = egressprofile.SelectionAccount, 0
	}
	for _, record := range page.Items {
		audit := record.Attempt
		decision := audit.Decision()
		if decision.SelectionSource != expectedSource || decision.DefaultRevision != expectedRevision || decision.ProxyID != profile.ID.String() || !audit.Terminal() {
			t.Fatal("actual Runtime audit reselected changed default", decision)
		}
		if managed && decision.AccountID != "account.private-runtime" {
			t.Fatal("actual managed audit lost genuine AccountRef", decision)
		}
	}
	if managed && refreshCalls.Load() != 1 {
		t.Fatal("actual account did not refresh once", refreshCalls.Load())
	}
	// A subsequent operation selects the updated profile then finds its proxy
	// unavailable. No original credential retry through Direct is permitted.
	if !accountOverride {
		if _, err := r.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 3, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}}); err != nil {
			t.Fatal(err)
		}
	}
	proxy.Close()
	failedRequest, err := http.NewRequestWithContext(t.Context(), method, "https://chatgpt.com"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	failedRequest.Header.Set("Content-Type", "application/json")
	failedRequest.Header.Set("Authorization", "Bearer original-synthetic-login")
	failed, err := client.Do(failedRequest)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, failed.Body)
	failed.Body.Close()
	if failed.StatusCode == 200 || calls.Load() != wantWires || upstreamCalls.Load() != 1 || network.denied.Load() != 0 {
		t.Fatalf("unavailable frozen proxy bypass/retry: status%d proxy%d upstream%d unexpected/direct%d", failed.StatusCode, calls.Load(), upstreamCalls.Load(), network.denied.Load())
	}
	page, err = r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != int(wantWires)+1 {
		t.Fatalf("failure lost independent audit %+v %v", page, err)
	}
	var matched bool
	for _, record := range page.Items {
		if record.Attempt.Outcome() == egressaudit.OutcomeFailed {
			matched = true
			decision := record.Attempt.Decision()
			wantRevision := uint64(4)
			if accountOverride {
				wantRevision = 0
			}
			if !record.Attempt.Terminal() || decision.DefaultRevision != wantRevision || decision.SelectionSource != expectedSource || decision.ProxyID != profile.ID.String() {
				t.Fatal("failed physical audit changed selection")
			}
		}
	}
	if !matched {
		t.Fatal("subsequent operation did not use current default revision4")
	}
}
