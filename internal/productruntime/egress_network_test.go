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
	"runtime"
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
	"github.com/vibe-agi/vibermate/internal/originaltransport"
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
	proxy      string
	otherProxy string
	denied     atomic.Int64
	// Nonempty only for a matrix case explicitly expecting Direct. Both the
	// canonical request and the actual private destination are closed literals.
	directTarget string
	direct       atomic.Int64
}

func (n *runtimeClosedNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network == "tcp" && n.directTarget != "" && (address == "chatgpt.com:443" || address == "auth.openai.com:443" || address == "127.0.0.1:443") {
		n.direct.Add(1)
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, n.directTarget)
	}
	if network != "tcp" || (address != n.proxy && (n.otherProxy == "" || address != n.otherProxy)) {
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

// A token refresh must be resumable through the concrete Runtime prober,
// retaining its selected exit while the default changes before any send.
func TestActualRuntimeManagedRefreshHoldUsesConcreteProbers(t *testing.T) {
	runRuntimeInheritedSOCKSTLS(t, true, true, false, runtimeSemanticCase{managedHold: true})
}

// Each literal row catches a lower-priority selection replacing the winning
// account/flow/default, or a default edit changing an already-running refresh.
func TestActualRuntimeManagedRefreshAndModelPriorityMatrix(t *testing.T) {
	for _, tc := range []runtimeManagedCase{
		{choice: "account_proxy", source: egressprofile.SelectionAccount, profileID: "profile.runtime-original", settingsRevision: 2},
		{choice: "account_direct", source: egressprofile.SelectionAccount, profileID: "profile.direct", settingsRevision: 2},
		{choice: "flow_proxy", source: egressprofile.SelectionFlow, profileID: "profile.runtime-original", settingsRevision: 1},
		{choice: "flow_direct", source: egressprofile.SelectionFlow, profileID: "profile.direct", settingsRevision: 1},
		{choice: "global", source: egressprofile.SelectionGlobal, profileID: "profile.runtime-original", defaultRevision: 2, settingsRevision: 1},
		{choice: "builtin", source: egressprofile.SelectionBuiltin, profileID: "profile.direct", defaultRevision: 1, settingsRevision: 1},
	} {
		t.Run(tc.choice, func(t *testing.T) {
			runRuntimeInheritedSOCKSTLS(t, true, true, false, runtimeSemanticCase{managedMatrix: &tc})
		})
	}
}

func TestActualRuntimeManagedRefreshFailureBoundaries(t *testing.T) {
	for _, failure := range []string{"token_failure", "model_failure", "held_cancel", "held_cancel_resume", "held_failed_probe"} {
		t.Run(failure, func(t *testing.T) {
			runRuntimeInheritedSOCKSTLS(t, true, true, false, runtimeSemanticCase{managedHold: strings.HasPrefix(failure, "held_"), managedFailure: failure})
		})
	}
}

type runtimeManagedCase struct {
	choice                            string
	source                            egressprofile.SelectionSource
	profileID                         string
	defaultRevision, settingsRevision uint64
}

func TestActualRuntimeSemanticClientCredentialsUseGlobalExitWithoutAccount(t *testing.T) {
	runRuntimeInheritedSOCKSTLS(t, true, false, false, runtimeSemanticCase{enabled: true})
}

func TestActualRuntimeSemanticClientCredentialsRespectFlowAndBuiltin(t *testing.T) {
	for _, choice := range []string{"flow_proxy", "flow_direct", "builtin"} {
		t.Run(choice, func(t *testing.T) {
			runRuntimeInheritedSOCKSTLS(t, true, false, false, runtimeSemanticCase{enabled: true, choice: choice})
		})
	}
}

type runtimeSemanticCase struct {
	enabled        bool
	choice         string
	managedHold    bool
	managedMatrix  *runtimeManagedCase
	managedFailure string
}

func runRuntimeInheritedSOCKSTLS(t *testing.T, model, managed, accountOverride bool, semanticCases ...runtimeSemanticCase) {
	semantic := len(semanticCases) > 0 && semanticCases[0].enabled
	managedHold := len(semanticCases) > 0 && semanticCases[0].managedHold
	managedFailure := ""
	if len(semanticCases) > 0 {
		managedFailure = semanticCases[0].managedFailure
	}
	var matrix *runtimeManagedCase
	if len(semanticCases) > 0 {
		matrix = semanticCases[0].managedMatrix
	}
	choice := ""
	if semantic {
		choice = semanticCases[0].choice
	}
	if matrix != nil {
		choice = matrix.choice
	}
	directExpected := choice == "account_direct" || choice == "flow_direct" || choice == "builtin"
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
	oldToken := token(time.Now().Add(-time.Minute))
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
			if managedFailure == "token_failure" {
				w.WriteHeader(http.StatusServiceUnavailable)
				io.WriteString(w, `{"error":"synthetic token failure"}`)
				return
			}
			fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"synthetic-refresh-new","expires_in":3600}`, newToken)
			return
		}
		encoded, _ := io.ReadAll(r.Body)
		upstreamCalls.Add(1)
		bodyMatches := string(encoded) == body
		if managed || semantic {
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
		if managedFailure == "model_failure" {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"error":"synthetic model failure"}`)
			return
		}
		io.WriteString(w, `{"id":"synthetic-response","object":"response","created_at":1,"model":"synthetic","status":"completed","output":[]}`)
	}))
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	upstream.StartTLS()
	defer upstream.Close()
	proxy, calls := runtimeSOCKSFixture(t, upstream.Listener.Addr().String())
	network := &runtimeClosedNetwork{proxy: proxy.Addr().String()}
	var proxyB net.Listener
	var callsB *atomic.Int64
	if matrix != nil {
		proxyB, callsB = runtimeSOCKSFixture(t, upstream.Listener.Addr().String())
		network.otherProxy = proxyB.Addr().String()
	}
	if directExpected || managedHold || managedFailure != "" {
		network.directTarget = upstream.Listener.Addr().String()
	}
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
	r, err := startWithBuilders(t.Context(), options, runtimeBuildOverrides{
		originalProber: func() (*originaltransport.TLSProber, error) {
			return originaltransport.NewTLSProberWithBuilder(builder, roots)
		},
		providerProber: func() (*providertransport.ProviderProber, error) {
			return providertransport.NewProviderProberWithBuilder(builder, roots)
		},
		provider: func(request providerBuildRequest) (providerRuntime, error) {
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
	var profileB egressprofile.ProfileRevision
	if matrix != nil {
		otherPolicy := egressnetwork.DefaultPolicy()
		otherPolicy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: proxyB.Addr().String()}
		profileB, err = r.EgressProfiles().Publish(t.Context(), egressprofile.PublishCommand{ID: "profile.runtime-other", DisplayName: "Private other", Policy: otherPolicy})
		if err != nil {
			t.Fatal(err)
		}
		// Keep the contained Direct mapping available after the edit too, so
		// a wrong new exit is observable in counters rather than setup refusal.
		network.directTarget = upstream.Listener.Addr().String()
	}
	if choice != "builtin" {
		selected := profile
		if choice == "flow_proxy" {
			selected = egressprofile.Direct()
		}
		if matrix != nil && choice != "global" {
			selected = profileB
		}
		if _, err := r.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: selected.ID, Revision: selected.Revision}}); err != nil {
			t.Fatal(err)
		}
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
	var managedAccount provideraccount.View
	if managed {
		material, err := codexoauth.ImportAuthJSON([]byte(fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q,"id_token":%q,"refresh_token":"synthetic-refresh-old","account_id":"synthetic-runtime-account"},"last_refresh":%q}`, oldToken, oldToken, time.Now().Add(-time.Hour).Format(time.RFC3339Nano))))
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
		managedAccount = account
		if accountOverride || choice == "account_proxy" || choice == "account_direct" {
			selected := profile
			if choice == "account_direct" {
				selected = egressprofile.Direct()
			}
			if _, err := r.accounts.SetSettings(t.Context(), provideraccount.SettingsCommand{ID: account.Account.ID, ExpectedRevision: account.Account.SettingsRevision, AutomaticRefresh: true, EgressProfile: selected}); err != nil {
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
		if matrix != nil && choice != "global" && choice != "builtin" {
			selected := profile
			if choice == "account_proxy" {
				selected = profileB
			}
			if choice == "flow_direct" {
				selected = egressprofile.Direct()
			}
			plan.EgressProfile, err = egressprofile.Explicit(selected)
			if err != nil {
				t.Fatal(err)
			}
		}
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
	if semantic {
		endpoint, err := r.UpstreamEndpoints().Get(t.Context(), upstreamendpoint.ChatGPTOfficialID)
		if err != nil {
			t.Fatal(err)
		}
		aggregate, _, _ := compatibilityEnvironment(t, environment.ClientProtocolOpenAIResponses, protocolspec.DialectOpenAIResponses)
		aggregate.ID = "semantic.original-account"
		aggregate.Name = "Semantic original client account"
		origin, err := originidentity.ParseClientOrigin("https://chatgpt.com")
		if err != nil {
			t.Fatal(err)
		}
		aggregate.ClientEndpoints[0].ClientOrigin = origin
		plan := &aggregate.ClientEndpoints[0].ProtocolPlans[0]
		plan.EgressProfile = egressprofile.Inherit()
		if choice == "flow_proxy" || choice == "flow_direct" {
			selected := profile
			if directExpected {
				selected = egressprofile.Direct()
			}
			plan.EgressProfile, err = egressprofile.Explicit(selected)
			if err != nil {
				t.Fatal(err)
			}
		}
		route := &plan.Destination.Upstream.Routes[0]
		route.ProviderTarget = environment.ProviderTarget{ID: endpoint.ID.String(), Revision: environment.Revision(endpoint.Revision), Origin: endpoint.Origin, RealmID: endpoint.RealmID, Capabilities: []protocolspec.ProviderCapability{protocolspec.ProviderCapabilityMessages, protocolspec.ProviderCapabilityStreaming, protocolspec.ProviderCapabilityToolCalls}}
		route.BackendProtocol = endpoint.BackendProtocols[0]
		route.AccountPolicy = environment.RouteAccountPolicy{Revision: 1, Mode: environment.AccountSelectionOriginal}
		draft, err := r.Environments().SaveDraft(t.Context(), environment.DraftCommand{Candidate: aggregate})
		if err != nil {
			t.Fatal(err)
		}
		preview, err := r.Environments().Preview(t.Context(), aggregate.ID, draft.Revision)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = r.Environments().Publish(t.Context(), preview); err != nil {
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
	startedSemantic := make(chan egressaudit.DecisionRef, 1)
	if !accountOverride {
		wire.before = func() {
			wire.before = nil
			if semantic {
				page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
				if err != nil || len(page.Items) != 1 || page.Items[0].Attempt.Terminal() {
					t.Error("semantic started audit unavailable")
				} else {
					startedSemantic <- page.Items[0].Attempt.Decision()
				}
			}
			change := egressprofile.SetDefaultCommand{ExpectedRevision: 2}
			if choice == "builtin" {
				change.ExpectedRevision = 1
				change.Profile = &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}
			}
			if _, err := r.EgressDefaults().SetDefault(t.Context(), change); err != nil {
				t.Error(err)
			}
		}
	}
	requestContext, cancelRequest := context.WithCancel(t.Context())
	defer cancelRequest()
	request, err := http.NewRequestWithContext(requestContext, method, "https://chatgpt.com"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer original-synthetic-login")
	if managedFailure == "token_failure" || managedFailure == "model_failure" {
		started := map[string]egressaudit.DecisionRef{}
		var edit sync.Once
		wire.before = func() {
			page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
			if err != nil {
				t.Error(err)
				return
			}
			for _, record := range page.Items {
				if !record.Attempt.Terminal() {
					started[record.Attempt.ID()] = record.Attempt.Decision()
				}
			}
			edit.Do(func() {
				if _, err := r.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 2}); err != nil {
					t.Error(err)
				}
			})
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		wantModel, wantEpoch, wantWires := int64(0), uint64(1), int64(1)
		wantAccess, wantRefresh := oldToken, "synthetic-refresh-old"
		if managedFailure == "model_failure" {
			wantModel, wantEpoch, wantWires = 1, 2, 2
			wantAccess, wantRefresh = newToken, "synthetic-refresh-new"
		}
		account, err := r.ProviderAccounts().Get(t.Context(), managedAccount.Account.ID)
		if err != nil || response.StatusCode == 200 || refreshCalls.Load() != 1 || upstreamCalls.Load() != wantModel || account.Health.CredentialEpoch != wantEpoch || calls.Load() != wantWires || network.direct.Load() != 0 || network.denied.Load() != 0 || bad.Load() != 0 {
			t.Fatalf("managed failure replay/rotation/exit status%d token%d model%d epoch%d proxy%d direct%d denied%d", response.StatusCode, refreshCalls.Load(), upstreamCalls.Load(), account.Health.CredentialEpoch, calls.Load(), network.direct.Load(), network.denied.Load())
		}
		assertRuntimeManagedSecret(t, options.Secrets, managedAccount, secretstore.Revision(wantEpoch), wantAccess, wantRefresh)
		page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
		if err != nil || len(page.Items) != int(wantWires) {
			t.Fatal("failure operation audit count changed", len(page.Items), err)
		}
		for _, record := range page.Items {
			audit, decision := record.Attempt, record.Attempt.Decision()
			// HTTP503 is a completed physical exchange. The caller status and
			// secret/model effects above establish the operation's failure.
			wantOutcome := egressaudit.OutcomeCompleted
			if !audit.Terminal() || audit.Outcome() != wantOutcome || decision != started[audit.ID()] || decision.SelectionSource != egressprofile.SelectionGlobal || decision.DefaultRevision != 2 || decision.ProxyID != profile.ID.String() || decision.ProxyRevision != 1 || decision.AccountID != "account.private-runtime" || decision.AccountSettingsRevision != 1 {
				t.Fatal("failure operation rewrote its own frozen audit", audit)
			}
		}
		drain, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		if err := gate.Drain(drain); err != nil {
			t.Fatal("failed managed operation did not drain", err)
		}
		return
	}
	if matrix != nil {
		started := map[string]egressaudit.DecisionRef{}
		entered, release := make(chan struct{}), make(chan struct{})
		var first sync.Once
		wire.before = func() {
			page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
			if err != nil {
				t.Error(err)
				return
			}
			for _, record := range page.Items {
				if !record.Attempt.Terminal() {
					started[record.Attempt.ID()] = record.Attempt.Decision()
				}
			}
			first.Do(func() { close(entered); <-release })
		}
		type result struct {
			status int
			body   string
			err    error
		}
		finished := make(chan result, 1)
		var releaseOnce sync.Once
		joined := false
		defer func() {
			releaseOnce.Do(func() { close(release) })
			if !joined {
				<-finished
			}
		}()
		go func() {
			response, err := client.Do(request)
			got := result{err: err}
			if response != nil {
				encoded, readErr := io.ReadAll(response.Body)
				response.Body.Close()
				got.status, got.body = response.StatusCode, string(encoded)
				if readErr != nil {
					got.err = readErr
				}
			}
			finished <- got
		}()
		select {
		case <-entered:
		case got := <-finished:
			joined = true
			t.Fatalf("managed operation ended before actual send boundary: %+v", got)
		case <-time.After(2 * time.Second):
			t.Fatal("managed operation did not reach actual token send boundary")
		}
		expectedRevision := uint64(2)
		if choice == "builtin" {
			expectedRevision = 1
		}
		_, editErr := r.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: expectedRevision, Profile: &egressprofile.ProfileRef{ID: profileB.ID, Revision: profileB.Revision}})
		releaseOnce.Do(func() { close(release) })
		got := <-finished
		joined = true
		if editErr != nil {
			t.Fatal(editErr)
		}
		if got.err != nil || got.status != 200 || !strings.Contains(got.body, "synthetic-response") {
			t.Fatalf("managed response %+v", got)
		}
		wantProxy, wantDirect := int64(2), int64(0)
		if directExpected {
			wantProxy, wantDirect = 0, 2
		}
		if calls.Load() != wantProxy || callsB.Load() != 0 || network.direct.Load() != wantDirect || network.denied.Load() != 0 || refreshCalls.Load() != 1 || upstreamCalls.Load() != 1 || bad.Load() != 0 {
			t.Fatalf("wrong actual initial wires A%d B%d Direct%d denied%d token%d model%d bad%d", calls.Load(), callsB.Load(), network.direct.Load(), network.denied.Load(), refreshCalls.Load(), upstreamCalls.Load(), bad.Load())
		}
		checkAudit := func(want int, next bool) {
			page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
			if err != nil || len(page.Items) != want {
				t.Fatalf("managed audit count=%d want=%d err=%v", len(page.Items), want, err)
			}
			var tokens, models int
			for _, record := range page.Items {
				audit := record.Attempt
				decision := audit.Decision()
				wantSource, wantRevision, wantID := matrix.source, matrix.defaultRevision, matrix.profileID
				if next && (choice == "global" || choice == "builtin") && decision.ProxyID == "profile.runtime-other" {
					wantSource, wantRevision, wantID = egressprofile.SelectionGlobal, expectedRevision+1, "profile.runtime-other"
				}
				if !audit.Terminal() || audit.Outcome() != egressaudit.OutcomeCompleted || decision != started[audit.ID()] || decision.SelectionSource != wantSource || decision.DefaultRevision != wantRevision || decision.ProxyID != wantID || decision.ProxyRevision != 1 || decision.AccountID != "account.private-runtime" || decision.AccountSettingsRevision != matrix.settingsRevision {
					t.Fatalf("managed immutable operation audit id=%s decision=%+v start=%+v", audit.ID(), decision, started[audit.ID()])
				}
				switch audit.Purpose() {
				case egressaudit.PurposeCredentialRefresh:
					tokens++
					if audit.TargetOrigin() != "https://auth.openai.com" {
						t.Fatal("token target changed")
					}
				case egressaudit.PurposeProviderAttempt:
					models++
					if audit.TargetOrigin() != "https://chatgpt.com" || decision.PolicyID != environmentID.String() {
						t.Fatal("managed model target or actual compiled environment changed")
					}
				default:
					t.Fatal("unexpected managed audit purpose", audit.Purpose())
				}
			}
			if tokens != 1 || models != want-1 {
				t.Fatalf("independent operations token%d model%d", tokens, models)
			}
		}
		checkAudit(2, false)
		account, err := r.ProviderAccounts().Get(t.Context(), managedAccount.Account.ID)
		if err != nil || account.Account.ID != managedAccount.Account.ID || account.Account.SecretRef != managedAccount.Account.SecretRef || account.Health.CredentialEpoch != 2 {
			t.Fatal("actual account did not rotate once", account.Health, err)
		}
		stored, err := options.Secrets.ReadAtRevision(t.Context(), managedAccount.Account.SecretRef, 2)
		if err != nil {
			t.Fatal(err)
		}
		defer stored.Destroy()
		materialBytes, err := stored.CopyBytes()
		if err != nil {
			t.Fatal(err)
		}
		defer clear(materialBytes)
		material, err := providerauth.ParseMaterial(materialBytes)
		if err != nil {
			t.Fatal(err)
		}
		defer material.Destroy()
		credentialBytes := material.CredentialBytes()
		defer clear(credentialBytes)
		var credential struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			AccountID    string `json:"accountId"`
		}
		if json.Unmarshal(credentialBytes, &credential) != nil || credential.AccessToken != newToken || credential.RefreshToken != "synthetic-refresh-new" || credential.AccountID != "synthetic-runtime-account" {
			t.Fatal("actual rotated secret bytes changed or old credential retained")
		}
		next, err := http.NewRequestWithContext(t.Context(), method, "https://chatgpt.com"+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		next.Header.Set("Content-Type", "application/json")
		next.Header.Set("Authorization", "Bearer original-synthetic-login")
		response, err := client.Do(next)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		wantB := int64(0)
		if choice == "global" || choice == "builtin" {
			wantB = 1
		} else if directExpected {
			wantDirect++
		} else {
			wantProxy++
		}
		if response.StatusCode != 200 || calls.Load() != wantProxy || callsB.Load() != wantB || network.direct.Load() != wantDirect || network.denied.Load() != 0 || refreshCalls.Load() != 1 || upstreamCalls.Load() != 2 || bad.Load() != 0 {
			t.Fatal("next actual managed operation did not retain explicit or use edited default", response.StatusCode, calls.Load(), callsB.Load(), network.direct.Load(), refreshCalls.Load(), upstreamCalls.Load())
		}
		checkAudit(3, true)
		drain, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		if err := gate.Drain(drain); err != nil {
			t.Fatal("managed operations did not drain", err)
		}
		return
	}
	if managedHold {
		started := map[string]egressaudit.DecisionRef{}
		wire.before = func() {
			page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
			if err != nil {
				t.Error(err)
				return
			}
			for _, record := range page.Items {
				if !record.Attempt.Terminal() {
					started[record.Attempt.ID()] = record.Attempt.Decision()
				}
			}
		}
		if _, err := r.EnterOfflineHold(t.Context(), gate.Snapshot().Revision); err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() {
			response, err := client.Do(request)
			if response != nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode != http.StatusOK {
					err = fmt.Errorf("managed model status %d", response.StatusCode)
				}
			}
			result <- err
		}()
		// Observe the real queue state; no timed sleep decides which operation
		// wins the edit. Shutdown joins both request and detached refresh flight.
		observation, done := context.WithTimeout(t.Context(), 2*time.Second)
		defer done()
		for gate.Snapshot().QueuedRequests != 1 && observation.Err() == nil {
			runtime.Gosched()
		}
		targets := gate.PendingProbeTargets()
		t.Logf("actual expired-token queue snapshot=%+v targets=%+v proxy=%d token=%d model=%d direct=%d", gate.Snapshot(), targets, calls.Load(), refreshCalls.Load(), upstreamCalls.Load(), network.denied.Load())
		if gate.Snapshot().QueuedRequests != 1 || len(targets) != 1 || targets[0].Kind != offlinehold.EgressAuxiliary || targets[0].TargetRef != "https://auth.openai.com" || targets[0].NetworkOrigin != "https://auth.openai.com" || targets[0].HTTPAuthority != "auth.openai.com" || targets[0].TLSServerName != "auth.openai.com" || targets[0].PlanRevision != 0 || targets[0].PlanDigest != "" || targets[0].TargetAccess != 0 || targets[0].EgressPolicy != profile.Policy || calls.Load() != 0 || refreshCalls.Load() != 0 || upstreamCalls.Load() != 0 || network.denied.Load() != 0 || network.direct.Load() != 0 {
			t.Error("actual managed refresh did not queue its frozen token target")
		}
		if _, err := r.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 2}); err != nil {
			t.Error(err)
		}
		if managedFailure == "held_cancel" || managedFailure == "held_cancel_resume" {
			cancelRequest()
			if err := <-result; err == nil {
				t.Error("canceled managed request returned success")
			}
			cancellation, stop := context.WithTimeout(t.Context(), time.Second)
			for gate.Snapshot().ActiveActions != 1 && cancellation.Err() == nil {
				runtime.Gosched()
			}
			stop()
			if gate.Snapshot().ActiveActions != 1 || gate.Snapshot().QueuedRequests != 1 {
				t.Error("canceled model waiter did not leave only its independently held refresh flight")
			}
			if managedFailure == "held_cancel_resume" {
				if _, err := r.ResumeOfflineHold(t.Context(), gate.Snapshot().Revision); err != nil {
					t.Error("independent frozen refresh could not resume", err)
					gate.BeginShutdown()
				}
				completion, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				if err := gate.Drain(completion); err != nil {
					t.Fatal("independent resumed refresh did not drain", err)
				}
				var epoch secretstore.Revision
				for completion.Err() == nil {
					metadata, err := options.Secrets.Inspect(completion, managedAccount.Account.SecretRef)
					if err != nil {
						t.Fatal(err)
					}
					epoch = metadata.Revision
					if epoch == 2 {
						break
					}
					runtime.Gosched()
				}
				if epoch != 2 || refreshCalls.Load() != 1 || upstreamCalls.Load() != 0 || calls.Load() != 2 || network.direct.Load() != 0 || network.denied.Load() != 0 || bad.Load() != 0 {
					t.Fatal("cancelled model resumed/retried or independent token changed exit", epoch, refreshCalls.Load(), upstreamCalls.Load(), calls.Load())
				}
				assertRuntimeManagedSecret(t, options.Secrets, managedAccount, 2, newToken, "synthetic-refresh-new")
				page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
				if err != nil || len(page.Items) != 1 {
					t.Fatal("canceled model produced an audit or independent token audit missing", err)
				}
				audit, decision := page.Items[0].Attempt, page.Items[0].Attempt.Decision()
				if audit.Purpose() != egressaudit.PurposeCredentialRefresh || audit.TargetOrigin() != "https://auth.openai.com" || !audit.Terminal() || audit.Outcome() != egressaudit.OutcomeCompleted || decision != started[audit.ID()] || decision.SelectionSource != egressprofile.SelectionGlobal || decision.DefaultRevision != 2 || decision.ProxyID != profile.ID.String() || decision.ProxyRevision != 1 || decision.AccountID != "account.private-runtime" || decision.AccountSettingsRevision != 1 {
					t.Fatal("independent refresh lost own frozen operation audit", audit)
				}
				t.Logf("ordinary canceled model joined; independently resumed token completed token=%d model=%d proxy=%d epoch=%d snapshot=%+v", refreshCalls.Load(), upstreamCalls.Load(), calls.Load(), epoch, gate.Snapshot())
				return
			}
			// Refresh singleflight intentionally outlives its canceled waiter.
			// Shutdown explicitly joins the retained, still-held flight; no
			// Resume is permitted after cancellation in this test.
			t.Logf("canceled waiter snapshot before explicit refresh teardown=%+v", gate.Snapshot())
			gate.BeginShutdown()
			drain, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if err := gate.Drain(drain); err != nil {
				t.Fatal("canceled refresh flight did not join", err)
			}
			assertRuntimeManagedSecret(t, options.Secrets, managedAccount, 1, oldToken, "synthetic-refresh-old")
			page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
			if err != nil || len(page.Items) != 0 || refreshCalls.Load() != 0 || upstreamCalls.Load() != 0 || calls.Load() != 0 || network.direct.Load() != 0 || network.denied.Load() != 0 {
				t.Fatal("canceled held refresh sent or rotated")
			}
			return
		}
		if managedFailure == "held_failed_probe" {
			proxy.Close()
		}
		_, resumeErr := r.ResumeOfflineHold(t.Context(), gate.Snapshot().Revision)
		if resumeErr != nil {
			if managedFailure != "held_failed_probe" {
				t.Errorf("actual held managed refresh cannot resume through concrete prober: %v", resumeErr)
			} else if gate.Snapshot().State != offlinehold.StateHeld || gate.Snapshot().QueuedRequests != 1 {
				t.Error("failed probe released held token")
			}
			gate.BeginShutdown()
		}
		if managedFailure == "held_failed_probe" && resumeErr == nil {
			t.Error("closed proxy probe unexpectedly succeeded")
			gate.BeginShutdown()
		}
		if resumeErr == nil && managedFailure == "" {
			// Existing Hold semantics require a separate concrete probe for the
			// model target queued after the resumed credential refresh.
			modelObservation, stop := context.WithTimeout(t.Context(), time.Second)
			for (gate.Snapshot().State != offlinehold.StateHeld || gate.Snapshot().QueuedRequests != 1) && modelObservation.Err() == nil {
				runtime.Gosched()
			}
			stop()
			modelTargets := gate.PendingProbeTargets()
			t.Logf("actual second-stage model queue snapshot=%+v targets=%+v token=%d model=%d proxy=%d", gate.Snapshot(), modelTargets, refreshCalls.Load(), upstreamCalls.Load(), calls.Load())
			if gate.Snapshot().State != offlinehold.StateHeld || len(modelTargets) != 1 || modelTargets[0].Kind != offlinehold.EgressProvider || modelTargets[0].NetworkOrigin != "https://chatgpt.com" || modelTargets[0].EgressPolicy != profile.Policy || refreshCalls.Load() != 1 || upstreamCalls.Load() != 0 || calls.Load() != 2 || network.direct.Load() != 0 || network.denied.Load() != 0 {
				t.Error("managed model did not queue its separate frozen target after rotation")
				gate.BeginShutdown()
			} else {
				_, resumeErr = r.ResumeOfflineHold(t.Context(), gate.Snapshot().Revision)
				if resumeErr != nil {
					t.Error("actual model-stage concrete provider probe failed", resumeErr)
					gate.BeginShutdown()
				}
			}
		}
		if err := <-result; err != nil && resumeErr == nil {
			t.Error(err)
		}
		drain, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		if err := gate.Drain(drain); err != nil {
			t.Error("managed consumers did not drain", err)
		}
		account, accountErr := r.ProviderAccounts().Get(t.Context(), "account.private-runtime")
		page, auditErr := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
		t.Logf("after actual resume consumers drained snapshot=%+v token=%d model=%d proxy=%d direct=%d credentialEpoch=%d auditCount=%d", gate.Snapshot(), refreshCalls.Load(), upstreamCalls.Load(), calls.Load(), network.denied.Load(), account.Health.CredentialEpoch, len(page.Items))
		if resumeErr != nil && (accountErr != nil || auditErr != nil || account.Health.CredentialEpoch != 1 || refreshCalls.Load() != 0 || upstreamCalls.Load() != 0 || calls.Load() != 0 || network.denied.Load() != 0 || len(page.Items) != 0) {
			t.Error("rejected managed resume produced physical send, rotation or audit")
		}
		if resumeErr == nil {
			if accountErr != nil || auditErr != nil || account.Health.CredentialEpoch != 2 || refreshCalls.Load() != 1 || upstreamCalls.Load() != 1 || calls.Load() != 4 || network.denied.Load() != 0 || network.direct.Load() != 0 || bad.Load() != 0 || len(page.Items) != 2 {
				t.Fatal("resumed token/model missed frozen wire route or rotation", account.Health, len(page.Items), calls.Load())
			}
			var tokens, models int
			for _, record := range page.Items {
				audit, decision := record.Attempt, record.Attempt.Decision()
				if !audit.Terminal() || audit.Outcome() != egressaudit.OutcomeCompleted || decision != started[audit.ID()] || decision.SelectionSource != egressprofile.SelectionGlobal || decision.DefaultRevision != 2 || decision.ProxyID != profile.ID.String() || decision.ProxyRevision != 1 || decision.AccountID != "account.private-runtime" || decision.AccountSettingsRevision != 1 {
					t.Fatal("resumed operation audit changed frozen account or selection", decision)
				}
				if audit.Purpose() == egressaudit.PurposeCredentialRefresh && audit.TargetOrigin() == "https://auth.openai.com" {
					tokens++
				} else if audit.Purpose() == egressaudit.PurposeProviderAttempt && audit.TargetOrigin() == "https://chatgpt.com" && decision.PolicyID == environmentID.String() {
					models++
				} else {
					t.Fatal("resumed audit operation identity changed", audit)
				}
			}
			if tokens != 1 || models != 1 {
				t.Fatal("resumed token/model operation audit counts", tokens, models)
			}
			assertRuntimeManagedSecret(t, options.Secrets, managedAccount, 2, newToken, "synthetic-refresh-new")
			next, err := http.NewRequestWithContext(t.Context(), method, "https://chatgpt.com"+path, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			next.Header.Set("Content-Type", "application/json")
			next.Header.Set("Authorization", "Bearer original-synthetic-login")
			response, err := client.Do(next)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 200 || network.direct.Load() != 1 || calls.Load() != 4 || refreshCalls.Load() != 1 || upstreamCalls.Load() != 2 || bad.Load() != 0 || network.denied.Load() != 0 {
				t.Fatal("next operation did not use edited builtin exit without token replay")
			}
			page, err = r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
			if err != nil || len(page.Items) != 3 {
				t.Fatal("next held operation audit missing", err)
			}
			var nextAudit bool
			for _, record := range page.Items {
				decision := record.Attempt.Decision()
				if decision.SelectionSource == egressprofile.SelectionBuiltin {
					nextAudit = true
					if decision.DefaultRevision != 3 || decision.ProxyID != "profile.direct" || decision.ProxyRevision != 1 || decision.AccountID != "account.private-runtime" || decision != started[record.Attempt.ID()] || !record.Attempt.Terminal() {
						t.Fatal("next held operation did not record its own frozen audit", decision)
					}
				}
			}
			if !nextAudit {
				t.Fatal("new operation did not observe default edit")
			}
			if err := gate.Drain(drain); err != nil {
				t.Fatal("next managed operation did not drain", err)
			}
		}
		return
	}
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
	wantProxy, wantDirect := wantWires, int64(0)
	if directExpected {
		wantProxy, wantDirect = 0, wantWires
	}
	if response.StatusCode != 200 || !strings.Contains(string(reply), "synthetic-response") || calls.Load() != wantProxy || network.direct.Load() != wantDirect || upstreamCalls.Load() != 1 || bad.Load() != 0 || network.denied.Load() != 0 {
		t.Fatalf("actual original wire status%d reply%s proxy%d upstream%d refresh%d bad%d direct%d", response.StatusCode, reply, calls.Load(), upstreamCalls.Load(), refreshCalls.Load(), bad.Load(), network.denied.Load())
	}
	page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != int(wantWires) {
		t.Fatalf("actual Runtime audit %+v %v", page, err)
	}
	expectedSource, expectedRevision := egressprofile.SelectionGlobal, uint64(2)
	expectedProfile := profile
	if choice == "flow_proxy" || choice == "flow_direct" {
		expectedSource, expectedRevision = egressprofile.SelectionFlow, 0
	}
	if choice == "builtin" {
		expectedSource, expectedRevision = egressprofile.SelectionBuiltin, 1
	}
	if directExpected {
		expectedProfile = egressprofile.Direct()
	}
	if accountOverride {
		expectedSource, expectedRevision = egressprofile.SelectionAccount, 0
	}
	for _, record := range page.Items {
		audit := record.Attempt
		decision := audit.Decision()
		if decision.SelectionSource != expectedSource || decision.DefaultRevision != expectedRevision || decision.ProxyID != expectedProfile.ID.String() || !audit.Terminal() {
			t.Fatal("actual Runtime audit reselected changed default", decision)
		}
		if managed && decision.AccountID != "account.private-runtime" {
			t.Fatal("actual managed audit lost genuine AccountRef", decision)
		}
		if semantic && (audit.Purpose() != egressaudit.PurposeProviderAttempt || decision.AccountID != "" || decision.AccountSettingsRevision != 0 || decision.PolicyID != environmentID.String()) {
			t.Fatal("semantic passthrough became original transport or borrowed managed account", decision)
		}
		if semantic {
			select {
			case before := <-startedSemantic:
				if before != decision {
					t.Fatal("terminal rewrote frozen semantic decision")
				}
			default:
				t.Fatal("missing semantic started decision")
			}
		}
	}
	if managed && refreshCalls.Load() != 1 {
		t.Fatal("actual account did not refresh once", refreshCalls.Load())
	}
	if directExpected {
		return
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
			if accountOverride || choice == "flow_proxy" {
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

func assertRuntimeManagedSecret(t *testing.T, secrets secretstore.Store, account provideraccount.View, epoch secretstore.Revision, access, refresh string) {
	t.Helper()
	stored, err := secrets.ReadAtRevision(t.Context(), account.Account.SecretRef, epoch)
	if err != nil {
		t.Fatal(err)
	}
	defer stored.Destroy()
	encoded, err := stored.CopyBytes()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	material, err := providerauth.ParseMaterial(encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	credentialBytes := material.CredentialBytes()
	defer clear(credentialBytes)
	var credential struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		AccountID    string `json:"accountId"`
	}
	if json.Unmarshal(credentialBytes, &credential) != nil || credential.AccessToken != access || credential.RefreshToken != refresh || credential.AccountID != "synthetic-runtime-account" {
		t.Fatal("managed secret bytes did not match expected credential epoch")
	}
}
