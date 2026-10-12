package productruntime

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
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
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/accountselector"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clienttarget"
	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
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

type capturedOriginalConfig struct {
	family, choice, edge string
	history, oauth       bool
}
type capturedOriginalReply struct {
	Status int
	Header http.Header
	Body   []byte
	Err    error
}
type capturedOriginalExpected struct {
	Source                            egressprofile.SelectionSource
	ProfileID                         string
	DefaultRevision, SettingsRevision uint64
}

// This wrapper owns the fixed captured read, semantic, and opaque socket seams. It does not
// broaden runtimeOriginalWire's shared whitelist or replace the proxy/Reader.
type capturedOriginalWire struct {
	builder  *egressnetwork.Builder
	roots    *x509.CertPool
	before   func(context.Context) error
	semantic bool
	raw      bool
}

func (w *capturedOriginalWire) RoundTrip(r *http.Request, policy egressnetwork.Policy) (*http.Response, error) {
	allowed := r.Method == http.MethodGet && (r.URL.Path == "/backend-api/wham/usage" || r.URL.Path == "/backend-api/wham/profiles/me") && r.URL.RawQuery == ""
	if w.semantic {
		allowed = r.Method == http.MethodPost && r.URL.Path == "/backend-api/codex/responses" && r.URL.RawQuery == ""
	}
	if w.raw {
		allowed = r.Method == http.MethodGet && r.URL.Path == "/backend-api/plugins/featured" && r.URL.RawQuery == "platform=synthetic%2Bfixture"
	}
	if r.URL.Scheme != "https" || r.URL.Host != "chatgpt.com" || !allowed {
		return nil, errors.New("closed captured fixture rejected request")
	}
	if err := w.before(r.Context()); err != nil {
		return nil, err
	}
	dialer, err := w.builder.Dialer(policy)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{DialContext: dialer.DialContext, DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: w.roots, MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: time.Second}
	defer transport.CloseIdleConnections()
	return transport.RoundTrip(r)
}

type capturedProviderWire struct{ wire *capturedOriginalWire }

func (w capturedProviderWire) RoundTrip(r *http.Request, d providertransport.TransportDispatch) (*http.Response, transportprofile.Evidence, error) {
	response, err := w.wire.RoundTrip(r, d.EgressPolicy())
	return response, transportprofile.Evidence{}, err
}

type capturedOriginalNetwork struct {
	*runtimeClosedNetwork
	failDirect                     atomic.Bool
	proxyAttemptsA, proxyAttemptsB atomic.Int64
	tokenAttempts                  atomic.Int64
}

func (n *capturedOriginalNetwork) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	if network != "ip" || host != "chatgpt.com" {
		n.denied.Add(1)
		return nil, errors.New("closed captured fixture rejected DNS")
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

func (n *capturedOriginalNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if address == "auth.openai.com:443" {
		n.tokenAttempts.Add(1)
		n.denied.Add(1)
		return nil, errors.New("captured read fixture forbids token traffic")
	}
	if address == n.proxy {
		n.proxyAttemptsA.Add(1)
	} else if address == n.otherProxy {
		n.proxyAttemptsB.Add(1)
	}
	if network == "tcp" && (address == "chatgpt.com:443" || address == "127.0.0.1:443") && n.failDirect.Load() {
		n.direct.Add(1)
		return nil, errors.New("closed captured fixture target dial failure")
	}
	return n.runtimeClosedNetwork.DialContext(ctx, network, address)
}

type capturedOriginalSecrets struct {
	secretstore.Store
	reads, inspects, writes atomic.Int64
}

func (s *capturedOriginalSecrets) ReadAtRevision(ctx context.Context, ref secretstore.Reference, revision secretstore.Revision) (*secretstore.Value, error) {
	s.reads.Add(1)
	return s.Store.ReadAtRevision(ctx, ref, revision)
}
func (s *capturedOriginalSecrets) Inspect(ctx context.Context, ref secretstore.Reference) (secretstore.Metadata, error) {
	s.inspects.Add(1)
	return s.Store.Inspect(ctx, ref)
}
func (s *capturedOriginalSecrets) Replace(ctx context.Context, c secretstore.ReplaceCommand) (secretstore.Metadata, error) {
	s.writes.Add(1)
	return s.Store.Replace(ctx, c)
}

type capturedOriginalFixture struct {
	cfg                capturedOriginalConfig
	r                  *Runtime
	gate               *offlinehold.Gate
	options            Options
	account            provideraccount.View
	accountSnapshot    provideraccount.View
	secretHash         [32]byte
	secrets            *capturedOriginalSecrets
	aggregate          environment.Environment
	capture            captureidentity.Reference
	assignment         captureassignment.Assignment
	client             *http.Client
	transport          *http.Transport
	upstream, listener *httptest.Server
	proxyA, proxyB     net.Listener
	callsA, callsB     *atomic.Int64
	network            *capturedOriginalNetwork
	httpCalls, bad     atomic.Int64
	mu                 sync.Mutex
	headers            []http.Header
	bodies             [][]byte
	starts             map[string]egressaudit.DecisionRef
	wants              map[string]capturedOriginalExpected
	asserted           map[string]bool
	expected           capturedOriginalExpected
	entered, release   chan struct{}
	first, releaseOnce sync.Once
	token              string
	semanticRequests   []providertransport.Request
	streamEntered      chan struct{}
	producerDone       chan struct{}
	streamFirst        sync.Once
}

func newCapturedOriginalFixture(t *testing.T, cfg capturedOriginalConfig) *capturedOriginalFixture {
	t.Helper()
	if cfg.family != "managed_read" && cfg.family != "original_read" && cfg.family != "semantic" && cfg.family != "raw" && cfg.family != "original_model" && cfg.family != "transformed" {
		t.Fatal("captured fixture family has no approved consumer", cfg.family)
	}
	f := &capturedOriginalFixture{cfg: cfg, starts: map[string]egressaudit.DecisionRef{}, wants: map[string]capturedOriginalExpected{}, asserted: map[string]bool{}}
	if cfg.edge == "send_edit" || cfg.edge == "proxy_loss" || cfg.edge == "dial_failure" || cfg.edge == "tls_failure" {
		f.entered, f.release = make(chan struct{}), make(chan struct{})
	}
	if cfg.family == "original_model" || cfg.family == "transformed" {
		f.producerDone = make(chan struct{}, 4)
		if cfg.edge == "stream_edit" || cfg.edge == "stream_disconnect" {
			f.streamEntered, f.release = make(chan struct{}), make(chan struct{})
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	name := "chatgpt.com"
	if cfg.edge == "wrong_name" || cfg.edge == "tls_failure" || cfg.edge == "held_tls_failure" || cfg.edge == "strict_tls_failure" {
		name = "wrong.invalid"
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(91), DNSNames: []string{name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	f.upstream = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.httpCalls.Add(1)
		f.mu.Lock()
		f.headers = append(f.headers, r.Header.Clone())
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		managed := cfg.family == "managed_read"
		valid := r.Method == http.MethodGet && r.Host == "chatgpt.com" && len(body) == 0 && r.Header.Get("Proxy-Authorization") == ""
		if cfg.family == "semantic" {
			var fields map[string]json.RawMessage
			valid = r.Method == http.MethodPost && r.Host == "chatgpt.com" && r.URL.RequestURI() == "/backend-api/codex/responses" && r.Header.Get("Proxy-Authorization") == "" && json.Unmarshal(body, &fields) == nil && len(fields) == 3 && string(fields["model"]) == `"synthetic"` && string(fields["input"]) == `"hello"` && string(fields["stream"]) == "false"
		}
		if cfg.family == "raw" {
			valid = valid && r.URL.RequestURI() == "/backend-api/plugins/featured?platform=synthetic%2Bfixture"
		}
		if cfg.family == "original_model" || cfg.family == "transformed" {
			var fields map[string]json.RawMessage
			input, marker := `"hello"`, ""
			if cfg.family == "transformed" {
				input, marker = `"transformed-input"`, "request"
			}
			valid = r.Method == http.MethodPost && r.Host == "chatgpt.com" && r.URL.RequestURI() == "/backend-api/codex/responses" && r.Header.Get("Proxy-Authorization") == "" && r.Header.Get("Content-Type") == "application/json" && r.Header.Get("X-Original-Transform") == marker && json.Unmarshal(body, &fields) == nil && len(fields) == 3 && string(fields["model"]) == `"synthetic"` && string(fields["input"]) == input && string(fields["stream"]) == "true"
		}
		if managed {
			valid = valid && r.Header.Get("Authorization") == "Bearer "+f.token && r.Header.Get("ChatGPT-Account-Id") == "workspace-B" && r.Header.Get("Cookie") == ""
		} else {
			valid = valid && r.Header.Get("Authorization") == "Bearer original-A" && r.Header.Get("ChatGPT-Account-Id") == "workspace-A" && r.Header.Get("Cookie") == "original-A-cookie" && r.Header.Get("User-Agent") == "captured-original-fixture/1"
		}
		if !valid {
			f.bad.Add(1)
			w.WriteHeader(400)
			return
		}
		if cfg.family == "raw" {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("X-Original-Fixture", "raw")
			_, _ = w.Write([]byte{0, 1, 2, 'r', 'a', 'w', 255})
			return
		}
		if cfg.family == "original_model" || cfg.family == "transformed" {
			defer func() { f.producerDone <- struct{}{} }()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, originalForwardPrefix)
			w.(http.Flusher).Flush()
			if f.streamEntered != nil {
				f.streamFirst.Do(func() {
					close(f.streamEntered)
					select {
					case <-f.release:
					case <-r.Context().Done():
					}
				})
			}
			if r.Context().Err() == nil {
				_, _ = io.WriteString(w, originalForwardTerminal)
				w.(http.Flusher).Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if cfg.family == "semantic" {
			io.WriteString(w, `{"id":"synthetic-response","object":"response","created_at":1,"model":"synthetic","status":"completed","output":[]}`)
			return
		}
		if r.URL.Path == "/backend-api/wham/profiles/me" {
			io.WriteString(w, `{"stats":{"lifetime_tokens":1200}}`)
			return
		}
		fmt.Fprintf(w, `{"plan_type":"pro","account_id":%q,"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":25,"limit_window_seconds":18000,"reset_after_seconds":3600,"reset_at":1800000000}}}`, r.Header.Get("ChatGPT-Account-Id"))
	}))
	f.upstream.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	f.upstream.StartTLS()
	t.Cleanup(f.upstream.Close)
	f.proxyA, f.callsA = runtimeSOCKSFixture(t, f.upstream.Listener.Addr().String())
	f.proxyB, f.callsB = runtimeSOCKSFixture(t, f.upstream.Listener.Addr().String())
	f.network = &capturedOriginalNetwork{runtimeClosedNetwork: &runtimeClosedNetwork{proxy: f.proxyA.Addr().String(), otherProxy: f.proxyB.Addr().String(), directTarget: f.upstream.Listener.Addr().String()}}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: f.network, SystemResolver: f.network})
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if cfg.edge != "untrusted" {
		roots.AddCert(f.upstream.Certificate())
	}
	wire := &capturedOriginalWire{builder: builder, roots: roots, semantic: cfg.family == "semantic" || cfg.family == "original_model" || cfg.family == "transformed", raw: cfg.family == "raw", before: func(ctx context.Context) error {
		page, err := f.r.EgressAttempts().List(ctx, egressaudit.PageRequest{Limit: 100})
		if err != nil {
			return err
		}
		f.mu.Lock()
		for _, record := range page.Items {
			if !record.Attempt.Terminal() {
				f.starts[record.Attempt.ID()] = record.Attempt.Decision()
				f.wants[record.Attempt.ID()] = f.expected
			}
		}
		f.mu.Unlock()
		if f.entered != nil {
			f.first.Do(func() {
				close(f.entered)
				select {
				case <-f.release:
				case <-ctx.Done():
				}
			})
		}
		return ctx.Err()
	}}
	f.gate, err = offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	f.options = testOptions(t, hostcontract.Desktop(), f.gate)
	f.secrets = &capturedOriginalSecrets{Store: f.options.Secrets}
	f.options.Secrets = f.secrets
	f.options.OriginalTransport = wire
	f.r, err = startWithBuilders(t.Context(), f.options, runtimeBuildOverrides{
		originalProber: func() (*originaltransport.TLSProber, error) {
			return originaltransport.NewTLSProberWithBuilder(builder, roots)
		},
		providerProber: func() (*providertransport.ProviderProber, error) {
			return providertransport.NewProviderProberWithBuilder(builder, roots)
		},
		provider: func(request providerBuildRequest) (providerRuntime, error) {
			bearer, err := providertransport.NewStaticBearerAuthenticator(request.secrets)
			if err != nil {
				return nil, err
			}
			oauth, err := providertransport.NewCodexOAuthAuthenticator(request.secrets)
			if err != nil {
				return nil, err
			}
			anthropic, err := providertransport.NewAnthropicAPIKeyAuthenticator(request.secrets)
			if err != nil {
				return nil, err
			}
			client, err := providertransport.NewClient(providertransport.ClientOptions{EgressDefaults: request.egressDefaults, Coordinator: request.coordinator, Authenticators: []providertransport.Authenticator{bearer, oauth, anthropic}, Transport: capturedProviderWire{wire}, InstanceIDs: request.instanceIDs, Audit: request.audit, RawEvidence: request.rawEvidence})
			if err != nil || (cfg.family != "semantic" && cfg.family != "original_model" && cfg.family != "transformed") {
				return client, err
			}
			return capturedSemanticProvider{providerRuntime: client, fixture: f}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.unblock(); shutdownRuntime(t, f.r) })
	publish := func(id string, listener net.Listener) egressprofile.ProfileRevision {
		policy := egressnetwork.DefaultPolicy()
		policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: listener.Addr().String()}
		p, err := f.r.EgressProfiles().Publish(t.Context(), egressprofile.PublishCommand{ID: egressprofile.ID(id), DisplayName: id, Policy: policy})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, b := publish("profile.captured-original-a", f.proxyA), publish("profile.captured-original-b", f.proxyB)
	f.expected = capturedOriginalExpectation(t, cfg.choice, cfg.family == "managed_read")
	if cfg.choice != "builtin" {
		p := b
		if cfg.choice == "global" {
			p = a
		}
		if _, err := f.r.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: p.ID, Revision: p.Revision}}); err != nil {
			t.Fatal(err)
		}
	}
	endpoint, err := f.r.UpstreamEndpoints().Get(t.Context(), upstreamendpoint.ChatGPTOfficialID)
	if err != nil {
		t.Fatal(err)
	}
	f.aggregate, _, _ = compatibilityEnvironment(t, environment.ClientProtocolOpenAIResponses, protocolspec.DialectOpenAIResponses)
	f.aggregate.ID, f.aggregate.Name = "captured.original.egress", "Private captured/original egress"
	origin, err := originidentity.ParseClientOrigin("https://chatgpt.com")
	if err != nil {
		t.Fatal(err)
	}
	f.aggregate.ClientEndpoints[0].ClientOrigin = origin
	plan := &f.aggregate.ClientEndpoints[0].ProtocolPlans[0]
	plan.EgressProfile = egressprofile.Inherit()
	if cfg.choice == "flow_proxy" || cfg.choice == "flow_direct" || cfg.choice == "account_proxy" || cfg.choice == "account_direct" {
		p := a
		if cfg.choice == "flow_direct" {
			p = egressprofile.Direct()
		}
		if cfg.choice == "account_proxy" {
			p = b
		}
		plan.EgressProfile, err = egressprofile.Explicit(p)
		if err != nil {
			t.Fatal(err)
		}
	}
	route := &plan.Destination.Upstream.Routes[0]
	route.ProviderTarget = environment.ProviderTarget{ID: endpoint.ID.String(), Revision: environment.Revision(endpoint.Revision), Origin: endpoint.Origin, RealmID: endpoint.RealmID, Capabilities: endpoint.Capabilities}
	route.BackendProtocol = endpoint.BackendProtocols[0]
	route.AllowAccountHistory = cfg.history
	if cfg.family == "managed_read" {
		f.token = "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":"workspace-B"}}`, time.Now().Add(time.Hour).Unix()))) + ".synthetic"
		credential := f.token
		driver := providerauth.StaticHeaderDriverRef()
		if cfg.oauth {
			driver = providerauth.CodexOAuthDriverRef()
			material, err := codexoauth.ImportAuthJSON([]byte(fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q,"id_token":%q,"refresh_token":"synthetic-refresh-B","account_id":"workspace-B"},"last_refresh":%q}`, f.token, f.token, time.Now().UTC().Format(time.RFC3339Nano))))
			if err != nil {
				t.Fatal(err)
			}
			defer material.Destroy()
			encoded, err := material.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			defer clear(encoded)
			credential = string(encoded)
		}
		material, err := providerauth.NewMaterial(credential, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer material.Destroy()
		encoded, err := material.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		defer clear(encoded)
		secret, err := secretstore.NewValue(encoded)
		if err != nil {
			t.Fatal(err)
		}
		defer secret.Destroy()
		f.account, err = f.r.ProviderAccounts().Create(t.Context(), provideraccount.CreateCommand{ID: "captured-managed-b", DisplayName: "Managed B", UpstreamEndpointID: endpoint.ID, Driver: driver, AutomaticRefresh: false, Secret: secret})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.choice == "account_proxy" || cfg.choice == "account_direct" {
			p := a
			if cfg.choice == "account_direct" {
				p = egressprofile.Direct()
			}
			if _, err := f.r.ProviderAccounts().SetSettings(t.Context(), provideraccount.SettingsCommand{ID: f.account.Account.ID, ExpectedRevision: 1, EgressProfile: p}); err != nil {
				t.Fatal(err)
			}
		}
		route.AccountPolicy.FixedAccountID = f.account.Account.ID.String()
		route.AccountPolicy.Accounts = []environment.RouteAccountReference{{ID: f.account.Account.ID.String(), Revision: environment.Revision(f.account.Account.Revision), DisplayName: f.account.Account.DisplayName}}
		if cfg.edge == "selector" {
			route.AccountPolicy.Mode, route.AccountPolicy.FixedAccountID = environment.AccountSelectionJavaScript, ""
			route.AccountPolicy.Selector = &codelibrary.AccountSelectorRevision{ID: "selector.captured", Revision: 1, CollectionID: "routing", DisplayName: "Unbound selector", Policy: accountselector.Policy{JavaScript: `selection.accountId = accounts[0].id;`}, PublishedAt: time.Now().UTC()}
		}
	} else {
		route.AccountPolicy = environment.RouteAccountPolicy{Revision: 1, Mode: environment.AccountSelectionOriginal}
		// Original caller history uses original authority. The compiler rejects
		// attaching the managed-history grant to an original-account route.
		route.AllowAccountHistory = false
	}
	if cfg.family == "raw" || cfg.family == "original_model" || cfg.family == "transformed" {
		plan.Destination = environment.DestinationPlan{Kind: environment.DestinationKindOriginal}
	}
	if cfg.family == "transformed" {
		plan.Transforms = []codelibrary.TransformRevision{{ID: "captured-original.transform", Revision: 1, CollectionID: "synthetic", DisplayName: "Synthetic original transform", PublishedAt: time.Now().UTC(), Policy: messagetransform.Policy{
			RequestJavaScript:  `const p=JSON.parse(request.body); p.input="transformed-input"; request.body=JSON.stringify(p); request.headers["x-original-transform"]="request";`,
			ResponseJavaScript: `response.headers["x-original-transform"]="response"; const p=JSON.parse(response.body); if(p.type==="response.output_text.delta") p.delta="transformed-delta"; response.body=JSON.stringify(p);`,
		}}}
	}
	f.publish(t)
	var capability string
	if cfg.edge == "managed_launch" {
		dir := t.TempDir()
		grant, err := f.r.captureRuns.Create(t.Context(), capturerun.CreateCommand{CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "codex"), ExecutableLabel: "codex", Lifetime: 5 * time.Minute, CatalogRevision: 1})
		if err != nil {
			t.Fatal(err)
		}
		f.capture, err = captureidentity.New(captureidentity.KindManagedRun, grant.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := clienttarget.NewProfile("codex-cli", clienttarget.EnvironmentFacts{})
		if err != nil {
			t.Fatal(err)
		}
		assignment, _, err := f.r.CaptureAssignments().CreateForLaunch(t.Context(), captureassignment.CreateCommand{Capture: f.capture, EnvironmentID: f.aggregate.ID, Source: captureassignment.SourceLaunch, ClientProfile: profile})
		if err != nil {
			t.Fatal(err)
		}
		if assignment.ClientTarget.ActualOrigin().String() != "https://chatgpt.com/backend-api/codex" {
			t.Fatal("managed launch lost native base path")
		}
		f.assignment = assignment
		capability = grant.ProxyCapability.Value()
	} else {
		grant, err := f.r.ManualCaptures().Create(t.Context(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Private captured reads", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
		if err != nil {
			t.Fatal(err)
		}
		f.capture, err = captureidentity.New(captureidentity.KindManualCapture, grant.Capture.ID)
		if err != nil {
			t.Fatal(err)
		}
		f.assignment, err = f.r.CaptureAssignments().Create(t.Context(), captureassignment.CreateCommand{Capture: f.capture, EnvironmentID: f.aggregate.ID, Source: captureassignment.SourceSystemTransparent})
		if err != nil {
			t.Fatal(err)
		}
		capability = grant.Credential.Value()
	}
	f.listener = httptest.NewServer(f.r.ProxyHandler())
	t.Cleanup(f.listener.Close)
	proxyURL, err := url.Parse(f.listener.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.User = url.UserPassword("capture", capability)
	clientRoots := x509.NewCertPool()
	if !clientRoots.AppendCertsFromPEM(f.r.LocalRootCertificate().CertificatePEM()) {
		t.Fatal("Runtime CA invalid")
	}
	f.transport = &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: clientRoots, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != f.listener.Listener.Addr().String() {
			return nil, errors.New("closed downstream fixture rejected dial")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	t.Cleanup(f.transport.CloseIdleConnections)
	f.client = &http.Client{Transport: f.transport, Timeout: 3 * time.Second}
	if cfg.family == "managed_read" {
		f.accountSnapshot, err = f.r.ProviderAccounts().Get(t.Context(), f.account.Account.ID)
		if err != nil {
			t.Fatal(err)
		}
		value, err := f.secrets.Store.ReadAtRevision(t.Context(), f.account.Account.SecretRef, 1)
		if err != nil {
			t.Fatal(err)
		}
		bytes, err := value.CopyBytes()
		value.Destroy()
		if err != nil {
			t.Fatal(err)
		}
		f.secretHash = sha256.Sum256(bytes)
		clear(bytes)
		t.Logf("before account id=%s revision=%d settings=%d association=%d epoch=%d synthetic-secret-SHA256=%x", f.accountSnapshot.Account.ID, f.accountSnapshot.Account.Revision, f.accountSnapshot.Account.SettingsRevision, f.accountSnapshot.Account.AssociationRevision, f.accountSnapshot.Health.CredentialEpoch, f.secretHash)
	}
	f.secrets.reads.Store(0)
	f.secrets.inspects.Store(0)
	f.secrets.writes.Store(0)
	return f
}
func capturedOriginalExpectation(t *testing.T, choice string, managed bool) capturedOriginalExpected {
	t.Helper()
	rows := map[string]capturedOriginalExpected{
		"account_proxy": {egressprofile.SelectionAccount, "profile.captured-original-a", 0, 2}, "account_direct": {egressprofile.SelectionAccount, "profile.direct", 0, 2},
		"flow_proxy": {egressprofile.SelectionFlow, "profile.captured-original-a", 0, 1}, "flow_direct": {egressprofile.SelectionFlow, "profile.direct", 0, 1},
		"global": {egressprofile.SelectionGlobal, "profile.captured-original-a", 2, 1}, "builtin": {egressprofile.SelectionBuiltin, "profile.direct", 1, 1},
	}
	row, ok := rows[choice]
	if !ok {
		t.Fatal("unknown closed selection", choice)
	}
	if !managed {
		row.SettingsRevision = 0
	}
	return row
}
func (f *capturedOriginalFixture) publish(t *testing.T) {
	t.Helper()
	draft, err := f.r.Environments().SaveDraft(t.Context(), environment.DraftCommand{ExpectedBaseRevision: f.aggregate.Revision - 1, Candidate: f.aggregate})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.r.Environments().Preview(t.Context(), f.aggregate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.Environments().Publish(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
}
func (f *capturedOriginalFixture) open(ctx context.Context, method, path, body string) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, method, "https://chatgpt.com"+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer original-A")
	r.Header.Set("ChatGPT-Account-Id", "workspace-A")
	r.Header.Set("Cookie", "original-A-cookie")
	r.Header.Set("User-Agent", "captured-original-fixture/1")
	if f.cfg.family == "semantic" || f.cfg.family == "original_model" || f.cfg.family == "transformed" {
		r.Header.Set("Content-Type", "application/json")
	}
	return f.client.Do(r)
}
func (f *capturedOriginalFixture) send(ctx context.Context, method, path, body string) capturedOriginalReply {
	r, err := f.open(ctx, method, path, body)
	reply := capturedOriginalReply{Err: err}
	if r != nil {
		defer r.Body.Close()
		reply.Status, reply.Header = r.StatusCode, r.Header.Clone()
		reply.Body, reply.Err = io.ReadAll(r.Body)
	}
	return reply
}
func (f *capturedOriginalFixture) unblock() {
	if f.release != nil {
		f.releaseOnce.Do(func() { close(f.release) })
	}
}
func (f *capturedOriginalFixture) waitQueue(t *testing.T) []offlinehold.ProbeTarget {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for f.gate.Snapshot().QueuedRequests != 1 && ctx.Err() == nil {
		runtime.Gosched()
	}
	if f.gate.Snapshot().QueuedRequests != 1 {
		t.Fatal("captured operation did not queue", f.gate.Snapshot())
	}
	t.Logf("capture=%s assignment-revision=%d environment=%s revision=%d digest=%s", f.capture.Key(), f.assignment.Revision, f.assignment.EnvironmentID, f.assignment.EnvironmentRevision, f.assignment.EnvironmentDigest)
	return f.gate.PendingProbeTargets()
}
func (f *capturedOriginalFixture) editDefault(t *testing.T) {
	t.Helper()
	command := egressprofile.SetDefaultCommand{ExpectedRevision: 2}
	if f.cfg.choice == "builtin" {
		command.ExpectedRevision = 1
		command.Profile = &egressprofile.ProfileRef{ID: "profile.captured-original-b", Revision: 1}
	}
	if _, err := f.r.EgressDefaults().SetDefault(t.Context(), command); err != nil {
		t.Fatal(err)
	}
}
func (f *capturedOriginalFixture) assertAudits(t *testing.T, want int) {
	t.Helper()
	if f.cfg.family == "raw" || f.cfg.family == "original_model" || f.cfg.family == "transformed" {
		f.assertForwardAudits(t, want)
		return
	}
	if f.cfg.family == "semantic" {
		f.assertSemanticAudits(t, want)
		return
	}
	page, err := f.r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 100})
	if err != nil || len(page.Items) != want {
		t.Fatalf("captured audits=%d want=%d err=%v", len(page.Items), want, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.starts) != want || len(f.wants) != want {
		t.Fatal("orphan captured audit start", len(f.starts), len(f.wants), want)
	}
	for _, record := range page.Items {
		a, d := record.Attempt, record.Attempt.Decision()
		expected, ok := f.wants[a.ID()]
		if !ok || !a.Terminal() || d != f.starts[a.ID()] || d.SelectionSource != expected.Source || d.ProxyID != expected.ProfileID || d.ProxyRevision != 1 || d.DefaultRevision != expected.DefaultRevision || d.AccountSettingsRevision != expected.SettingsRevision || a.ConnectionID() == "" {
			t.Fatalf("captured immutable attempt id=%s decision=%+v start=%+v expected=%+v", a.ID(), d, f.starts[a.ID()], expected)
		}
		timeline, err := f.r.ConnectionEvents().Timeline(t.Context(), a.ConnectionID())
		if err != nil {
			t.Fatal("captured attempt has no actual connection", err)
		}
		bound := false
		for _, record := range timeline.Events {
			event := record.Event
			if event.ConnectionID == a.ConnectionID() && event.EnvironmentID == f.assignment.EnvironmentID && event.EnvironmentRevision == f.assignment.EnvironmentRevision {
				bound = true
			}
		}
		if !bound || a.Parent().ID == a.ID() || a.Parent().ExchangeID != "" {
			t.Fatal("captured parent/connection authority mismatch", a.Parent(), timeline)
		}
		if f.cfg.family == "managed_read" {
			if a.Purpose() != egressaudit.PurposeRouteOperation || a.PayloadClass() != egressaudit.PayloadControl || d.Authority != egressaudit.AuthorityEnvironment || d.PolicyID != f.assignment.EnvironmentID.String() || d.PolicyRevision != uint64(f.assignment.EnvironmentRevision) || d.RuleID != "route.test" || d.AccountID != f.account.Account.ID.String() || a.Parent().Kind != egressaudit.ParentClientOperation || a.Parent().ID == "" {
				t.Fatal("captured managed audit lost authority", a)
			}
		} else {
			policyID, policyRevision, ruleID := expected.ProfileID, uint64(1), "flow.egress"
			if expected.Source == egressprofile.SelectionGlobal || expected.Source == egressprofile.SelectionBuiltin {
				policyID, policyRevision, ruleID = "runtime.egress-default", expected.DefaultRevision, "global.default"
			}
			if a.Purpose() != egressaudit.PurposeAgentProbe || a.PayloadClass() != egressaudit.PayloadControl || d.Authority != egressaudit.AuthorityNetwork || d.AccountID != "" || a.Parent().Kind != egressaudit.ParentOriginalRequest || a.Parent().ID == "" || d.PolicyID != policyID || d.PolicyRevision != policyRevision || d.RuleID != ruleID {
				t.Fatal("original read audit lost authority", a)
			}
		}
		wantOutcome := egressaudit.OutcomeCompleted
		if f.cfg.edge == "proxy_loss" || f.cfg.edge == "dial_failure" || f.cfg.edge == "tls_failure" {
			wantOutcome = egressaudit.OutcomeFailed
		}
		if a.Outcome() != wantOutcome {
			t.Fatal("captured audit outcome", a)
		}
		if !f.asserted[a.ID()] {
			t.Logf("attempt=%s parent=%+v connection=%s purpose=%s outcome=%s decision=%+v", a.ID(), a.Parent(), a.ConnectionID(), a.Purpose(), a.Outcome(), d)
			f.asserted[a.ID()] = true
		}
	}
}
func (f *capturedOriginalFixture) drain(t *testing.T) {
	t.Helper()
	f.unblock()
	f.transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := f.gate.Drain(ctx); err != nil {
		t.Fatal("captured gate drain", err)
	}
	if err := f.r.bodyAdmission.Drain(ctx); err != nil {
		t.Fatal("captured body admission drain", err)
	}
	s := f.gate.Snapshot()
	if s.ActiveActions != 0 || s.ActiveEgress != 0 || s.QueuedRequests != 0 || s.HeldBytes != 0 {
		t.Fatal("captured retained admission", s)
	}
	if f.cfg.family == "managed_read" {
		view, err := f.r.ProviderAccounts().Get(t.Context(), f.account.Account.ID)
		if err != nil || view.Health.CredentialEpoch != 1 || view.Account.SecretRef != f.account.Account.SecretRef || view.Account.Revision != f.accountSnapshot.Account.Revision || view.Account.SettingsRevision != f.accountSnapshot.Account.SettingsRevision || view.Account.AutomaticRefresh {
			t.Fatal("captured read rotated credentials", view.Health, err)
		}
		value, err := f.secrets.Store.ReadAtRevision(t.Context(), f.account.Account.SecretRef, 1)
		if err != nil {
			t.Fatal(err)
		}
		bytes, err := value.CopyBytes()
		value.Destroy()
		if err != nil {
			t.Fatal(err)
		}
		after := sha256.Sum256(bytes)
		clear(bytes)
		if after != f.secretHash {
			t.Fatal("captured credential bytes changed")
		}
		t.Logf("after account id=%s revision=%d settings=%d association=%d epoch=%d synthetic-secret-SHA256=%x reads=%d inspects=%d writes=%d", view.Account.ID, view.Account.Revision, view.Account.SettingsRevision, view.Account.AssociationRevision, view.Health.CredentialEpoch, after, f.secrets.reads.Load(), f.secrets.inspects.Load(), f.secrets.writes.Load())
		if value, err := f.secrets.Store.ReadAtRevision(t.Context(), f.account.Account.SecretRef, 2); err == nil {
			value.Destroy()
			t.Fatal("captured read saved a second secret revision")
		}
	}
	if f.secrets.writes.Load() != 0 {
		t.Fatal("captured read saved secret material")
	}
	if f.cfg.family == "semantic" || f.cfg.family == "raw" || f.cfg.family == "original_model" || f.cfg.family == "transformed" {
		f.drainSemantic(t, ctx)
	}
	if f.bad.Load() != 0 || f.network.denied.Load() != 0 {
		t.Fatal("captured fixture unexpected traffic/credential", f.bad.Load(), f.network.denied.Load())
	}
	t.Logf("captured family=%s choice=%s edge=%s HTTP=%d A=%d B=%d Direct=%d proxyAttemptsA=%d proxyAttemptsB=%d bad=%d denied=%d rotation=0 drain=%+v", f.cfg.family, f.cfg.choice, f.cfg.edge, f.httpCalls.Load(), f.callsA.Load(), f.callsB.Load(), f.network.direct.Load(), f.network.proxyAttemptsA.Load(), f.network.proxyAttemptsB.Load(), f.bad.Load(), f.network.denied.Load(), s)
}
