package providertransport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/testrecording"
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

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

// Use the real SOCKS, TLS, authentication and provider clients, but permit
// sockets only to the two loopback fixtures. No public DNS or credentials.
func TestAccountEgressThroughSOCKSForEverySupportedOperation(t *testing.T) {
	testAccountEgressThroughSOCKS(t, nil, nil, false)
}

func ExerciseOwnedAccountGlobalSOCKS(t *testing.T, acquire func(*testing.T, egressprofile.ProfileRevision, []byte, secretstore.Store) providerauth.Lease) {
	testAccountEgressThroughSOCKS(t, acquire, nil, false)
}

func ExerciseFirstLoginGlobalSOCKS(t *testing.T, defaults func(egressprofile.ProfileRevision) egressprofile.Resolver) {
	testAccountEgressThroughSOCKS(t, nil, defaults, false)
}

func ExerciseRuntimeMetadataGlobalSOCKS(t *testing.T, defaults func(egressprofile.ProfileRevision) egressprofile.Resolver) {
	testAccountEgressThroughSOCKS(t, nil, defaults, true)
}

func ExerciseGlobalMetadataFault(t *testing.T, defaults func(egressprofile.ProfileRevision) egressprofile.Resolver, mode string) {
	testAccountEgressThroughSOCKS(t, nil, defaults, true, &AccountEgressWorkflow{RuntimeMode: mode})
}

// Only a test bridge: real account-operation composition lives in the external
// package to avoid provideraccount -> exchange -> providertransport cycles.
type AccountEgressWorkflow struct {
	RuntimeMode         string
	Run                 func(*testing.T, egressprofile.ProfileRevision, []byte, secretstore.Store, *Client)
	AfterDetails        func()
	Refresh             bool
	AfterToken          func()
	AutomaticModel      bool
	ExpectDirectRefusal bool
	Actions             *offlinehold.Gate
	ClientHello         transportprofile.Observation
	Completed           func() []egressaudit.Attempt
}

func ExerciseOwnedGlobalWorkflow(t *testing.T, workflow *AccountEgressWorkflow) {
	testAccountEgressThroughSOCKS(t, nil, nil, false, workflow)
}

type accountNetworkClock struct{}

func (accountNetworkClock) Now() time.Time { return time.Now().UTC() }

func testAccountEgressThroughSOCKS(t *testing.T, acquire func(*testing.T, egressprofile.ProfileRevision, []byte, secretstore.Store) providerauth.Lease, loginDefaults func(egressprofile.ProfileRevision) egressprofile.Resolver, metadata bool, workflows ...*AccountEgressWorkflow) {
	var workflow *AccountEgressWorkflow
	if len(workflows) > 0 {
		workflow = workflows[0]
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{"chatgpt.com", "auth.openai.com", "models.dev"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:   time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	observed := make(chan *http.Request, 4)
	accessToken := fakeChatGPTToken(`{"exp":1999988400,"https://api.openai.com/auth":{"chatgpt_account_id":"fixture-account"}}`)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case observed <- r.Clone(context.Background()):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if workflow != nil {
			if workflow.RuntimeMode != "" {
				if workflow.RuntimeMode == "doh_failure" {
					if r.Method != "POST" || r.Host != "127.0.0.1" || r.URL.Path != "/dns-query" || r.Header.Get("Authorization") != "" {
						t.Error("DoH changed exact private target/auth")
					}
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if r.Method != "GET" || r.Host != "models.dev" || r.URL.Path != "/models.json" || r.Header.Get("Authorization") != "" {
					t.Error("metadata changed target/auth")
				}
				_, _ = io.WriteString(w, `{}`)
				return
			}
			if workflow.AutomaticModel && r.Host == "chatgpt.com" && r.Method == http.MethodPost && r.URL.Path == "/backend-api/codex/responses" {
				if r.Header.Get("Authorization") != "Bearer "+accessToken {
					t.Error("model lost rotated owned credential")
					w.WriteHeader(403)
					return
				}
				_, _ = io.WriteString(w, `{"id":"resp_synthetic","object":"response","created_at":1,"status":"completed","model":"synthetic","output":[{"id":"msg_synthetic","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"synthetic complete","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
				return
			}
			if workflow.Refresh {
				if r.Host != "auth.openai.com" || r.Method != http.MethodPost || r.URL.Path != "/oauth/token" || r.Header.Get("Authorization") != "" {
					t.Error("refresh lost fixed target or leaked access-token authorization")
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if workflow.AfterToken != nil {
					workflow.AfterToken()
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"access_token": accessToken, "refresh_token": "synthetic-rotated"})
				return
			}
			if r.Host != "chatgpt.com" || r.Header.Get("Authorization") != "Bearer "+accessToken {
				t.Error("reset workflow lost target/owned credential")
				w.WriteHeader(403)
				return
			}
			switch r.Method + " " + r.URL.Path {
			case "GET /backend-api/wham/rate-limit-reset-credits":
				if workflow.AfterDetails != nil {
					workflow.AfterDetails()
				}
				_, _ = io.WriteString(w, `{"available_count":1,"credits":[{"id":"credit-fixture","reset_type":"codex_rate_limits","status":"available","granted_at":"2026-09-01T00:00:00Z"}]}`)
			case "POST /backend-api/wham/rate-limit-reset-credits/consume":
				_, _ = io.WriteString(w, `{"code":"reset","windows_reset":1}`)
			default:
				t.Error("unexpected reset workflow request")
				w.WriteHeader(403)
			}
			return
		}
		if loginDefaults != nil && !metadata {
			if r.Host != "auth.openai.com" || r.URL.Path != "/oauth/token" || r.Method != "POST" || r.Header.Get("Authorization") != "" {
				t.Error("first login changed fixed target or leaked access token")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id_token": accessToken, "access_token": accessToken, "refresh_token": "synthetic-first-login"})
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	proxy, proxyCalls := newAccountSOCKSFixture(t, server.Listener.Addr().String())
	network := &accountLoopbackNetwork{proxy: proxy.Addr().String(), upstream: server.Listener.Addr().String()}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	dialers, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}})
	if err != nil {
		t.Fatal(err)
	}
	transport, err := newStrictTransportWithDialers(roots, dialers, DefaultTransportTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	initialToken := accessToken
	if workflow != nil && workflow.AutomaticModel {
		expires := time.Now().Add(time.Minute)
		if workflow.ExpectDirectRefusal {
			expires = time.Now().Add(-time.Minute)
		}
		initialToken = fakeChatGPTToken(`{"exp":` + fmt.Sprint(expires.Unix()) + `,"https://api.openai.com/auth":{"chatgpt_account_id":"fixture-account"}}`)
	}
	authJSON, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt", "tokens": map[string]string{
			"id_token": initialToken, "access_token": initialToken,
			"refresh_token": "fixture-only", "account_id": "fixture-account",
		}, "last_refresh": time.Now().UTC().Format(time.RFC3339Nano),
	})
	stored, err := codexoauth.ImportAuthJSON(authJSON)
	if err != nil {
		t.Fatal(err)
	}
	defer stored.Destroy()
	encoded, err := stored.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	var reader secretstore.Reader = testSecretReaderWithPolicy(t, string(encoded), nil, nil)
	var managed providerauth.Lease
	var workflowSecrets *accountGlobalSecrets
	if workflow != nil {
		workflowSecrets = &accountGlobalSecrets{secretReaderStub: &secretReaderStub{}}
		reader = workflowSecrets
	}
	if acquire != nil {
		secrets := &accountGlobalSecrets{secretReaderStub: &secretReaderStub{}}
		profile := testAccountEgress()
		profile.Policy.Proxy.Endpoint = proxy.Addr().String()
		managed = acquire(t, profile, encoded, secrets)
		defer managed.Release()
		reader = secrets
	}
	auth, err := NewCodexOAuthAuthenticator(reader)
	if err != nil {
		t.Fatal(err)
	}
	gate := newStartedGate(t)
	audit := &ownedWorkflowAudit{}
	var defaults egressprofile.Resolver
	if loginDefaults != nil {
		profile := testAccountEgress()
		profile.Policy.Proxy.Endpoint = proxy.Addr().String()
		if workflow != nil && workflow.RuntimeMode == "doh_failure" {
			profile.Policy.Resolver = egressnetwork.ResolverPolicy{Kind: egressnetwork.ResolverDoH, DoHURL: "https://127.0.0.1/dns-query", Transport: egressnetwork.ResolverTransportProxy}
		}
		defaults = loginDefaults(profile)
	}
	client, err := NewClient(ClientOptions{
		EgressDefaults: defaults,
		Coordinator:    gate, Authenticator: auth, Transport: transport,
		Audit: testrecording.Egress(audit), InstanceIDs: &sequentialInstanceIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownClient(t, client) })
	if workflow != nil && workflow.RuntimeMode != "" {
		runGlobalMetadataFault(t, workflow.RuntimeMode, client, gate, defaults, audit, dialers, roots, network, proxyCalls, observed)
		return
	}
	if workflow != nil {
		workflow.Actions = gate
		workflow.ClientHello = captureTransportClientHello(t, nil)
		workflow.Completed = audit.snapshot
		profile := testAccountEgress()
		profile.Policy.Proxy.Endpoint = proxy.Addr().String()
		workflow.Run(t, profile, encoded, workflowSecrets, client)
		if workflow.ExpectDirectRefusal {
			if proxyCalls.Load() != 0 || network.calls.Load() != 1 || network.direct.Load() != 1 {
				t.Fatal("explicit Direct was overridden or retried")
			}
			return
		}
		wantCalls := int64(2)
		if workflow.Refresh && !workflow.AutomaticModel {
			wantCalls = 1
		}
		if proxyCalls.Load() != wantCalls || network.calls.Load() != wantCalls || network.direct.Load() != 0 {
			t.Fatalf("owned workflow changed exact outbound count: proxy=%d calls=%d direct=%d", proxyCalls.Load(), network.calls.Load(), network.direct.Load())
		}
		started, terminal := audit.attempts()
		if workflow.AutomaticModel {
			return
		} // This branch checks both exact audits in the external workflow.
		if started.Decision().SelectionSource != egressprofile.SelectionGlobal || started.Decision().DefaultRevision != 2 || terminal.Decision() != started.Decision() {
			t.Fatal("reset consume changed frozen selection after details")
		}
		starts, terminals := audit.startedSnapshot(), audit.snapshot()
		if len(starts) != int(wantCalls) || len(terminals) != int(wantCalls) {
			t.Fatal("owned workflow lost a physical start or terminal")
		}
		for i, before := range starts {
			after := terminals[i]
			if before.ID() != after.ID() || before.Decision() != after.Decision() || before.Decision().SelectionSource != egressprofile.SelectionGlobal || before.Decision().DefaultRevision != 2 || !after.Terminal() {
				t.Fatalf("owned workflow audit %d changed frozen authority", i)
			}
		}
		return
	}
	if metadata {
		for _, down := range []bool{false, true} {
			if down {
				_ = proxy.Close()
			}
			for _, prices := range []bool{false, true} {
				before := proxyCalls.Load()
				beforeDials := network.calls.Load()
				beforeAudits := len(audit.startedSnapshot())
				var response *http.Response
				var err error
				if prices {
					response, err = client.FetchModelsDevPrices(context.Background())
				} else {
					response, err = client.FetchModelsDev(context.Background())
				}
				if response != nil {
					_, _ = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
				}
				if down {
					if err == nil || proxyCalls.Load() != before || len(observed) != 0 {
						t.Fatal("metadata failed proxy silently fell back")
					}
				} else {
					if err != nil || proxyCalls.Load() != before+1 || network.direct.Load() != 0 {
						t.Fatalf("metadata missed real global SOCKS, forbiddenDirect=%d: %v", network.direct.Load(), err)
					}
					r := <-observed
					want := "/models.json"
					if prices {
						want = "/api.json"
					}
					if r.Host != "models.dev" || r.URL.Path != want || r.Header.Get("Authorization") != "" {
						t.Fatal("metadata target/auth broadened")
					}
				}
				if network.calls.Load() != beforeDials+1 || network.direct.Load() != 0 || len(audit.startedSnapshot()) != beforeAudits+1 {
					t.Fatal("metadata retried or bypassed its selected proxy")
				}
				started, terminal := audit.attempts()
				if started.Purpose() != egressaudit.PurposeModelMetadataDirectory || started.Decision().SelectionSource != egressprofile.SelectionGlobal || started.Decision().DefaultRevision != 2 || started.Decision().AccountID != "" || !terminal.Terminal() || terminal.Decision() != started.Decision() || (down && terminal.Outcome() != egressaudit.OutcomeFailed) {
					t.Fatal("metadata lost immutable physical audit")
				}
			}
		}
		return
	}
	if loginDefaults != nil {
		var saves int
		logins, err := codexoauth.NewLoginManager(codexoauth.LoginOptions{TokenClient: client, Clock: accountNetworkClock{}, Persist: func(context.Context, codexoauth.LoginAccount, *codexoauth.Credential) (string, error) {
			saves++
			return "synthetic-login", nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		defer logins.Shutdown(context.Background())
		view, err := logins.Start(context.Background(), "synthetic-owner", codexoauth.LoginAccount{ID: "synthetic-login", EndpointID: "target.codex.official"}, "manual")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := defaults.(egressprofile.Defaults).SetDefault(context.Background(), egressprofile.SetDefaultCommand{ExpectedRevision: 2}); err != nil {
			t.Fatal(err)
		}
		authorize, err := url.Parse(view.AuthorizationURL)
		if err != nil {
			t.Fatal(err)
		}
		callback := authorize.Query().Get("redirect_uri") + "?" + url.Values{"state": {authorize.Query().Get("state")}, "code": {"synthetic-code"}}.Encode()
		result, err := logins.Complete(context.Background(), "synthetic-owner", view.ID, callback)
		if err != nil || result.State != "completed" || saves != 1 || proxyCalls.Load() != 1 || network.direct.Load() != 0 {
			t.Fatalf("first login missed actual global SOCKS: state=%s saves=%d proxy=%d forbiddenDirect=%d err=%v", result.State, saves, proxyCalls.Load(), network.direct.Load(), err)
		}
		request := <-observed
		if request.Host != "auth.openai.com" || request.Header.Get("Authorization") != "" {
			t.Fatal("first login mis-scoped authentication")
		}
		started, terminal := audit.attempts()
		decision := started.Decision()
		if decision.SelectionSource != egressprofile.SelectionGlobal || decision.DefaultRevision != 2 || decision.PolicyID != "runtime.egress-default" || decision.PolicyRevision != 2 || decision.RuleID != "global.default" || decision.ProxyID != "login.global" || decision.AccountID != "" || decision.AccountSettingsRevision != 0 || terminal.Decision() != decision {
			t.Fatal("first login actual global proxy mislabeled as BuiltInDirect or fake account audit")
		}
		next, err := client.FreezeLoginEgress(context.Background())
		if err != nil || next.Source() != egressprofile.SelectionBuiltin || next.DefaultRevision() != 3 || !next.Profile().Equal(egressprofile.Direct()) {
			t.Fatal("next first-login operation did not see changed default", err)
		}
		return
	}
	endpoint := testDiscoveryEndpoint(t, "https://chatgpt.com")
	credential := accountCodexEgressCredential{testRuntimeDiscoveryCredential(t, endpoint.RealmID).(*runtimeDiscoveryCredential)}
	credential.account.EgressProfile = testAccountEgress()
	credential.account.EgressProfile.Policy.Proxy.Endpoint = proxy.Addr().String()
	credential.account.ResolvedEgress, _ = egressprofile.RestoreResolved(credential.account.EgressProfile, egressprofile.SelectionAccount, 0)
	credential.account.SettingsRevision = 4
	var owned providerauth.Lease = credential
	if acquire != nil {
		credential.account, _ = managed.Account()
		owned = managed
	}
	for _, unavailable := range []bool{false, true} {
		if unavailable {
			_ = proxy.Close()
		}
		for _, operation := range []string{"model", "oauth", "quota", "history", "models", "reset"} {
			if acquire != nil && (operation == "model" || operation == "oauth") {
				continue
			} // Managed model/refresh share a real Pipeline in their separate workflow.
			name := operation
			if unavailable {
				name += "/proxy-unavailable"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				beforeProxy, beforeDials := proxyCalls.Load(), network.calls.Load()
				var response *http.Response
				var requestErr error
				switch operation {
				case "model":
					options := validRequestOptions(t)
					action, err := gate.BeginAction(ctx, offlinehold.ActionRequest{ActionID: "account-model-" + name})
					if err != nil {
						t.Fatal(err)
					}
					defer action.Release()
					options.Action = action
					options.Target, _ = NewTarget(endpoint.Origin)
					options.RelativePath = "backend-api/codex/responses"
					options.AccountRef = credential.account
					options.AuthDriverRef = credential.Driver()
					options.SecretRef = credential.Secret()
					options.ClientHello = captureTransportClientHello(t, nil)
					request, err := NewRequest(options)
					if err != nil {
						t.Fatal(err)
					}
					response, _, requestErr = client.Do(ctx, request)
				case "oauth":
					request, _ := http.NewRequestWithContext(ctx, http.MethodPost, codexoauth.TokenURL, strings.NewReader(`{"grant_type":"refresh_token","refresh_token":"fixture-only"}`))
					request.Header.Set("Content-Type", "application/json")
					response, requestErr = client.DoCodexOAuthTokenRequest(request, credential.account)
				case "models":
					response, requestErr = client.FetchEndpointModels(ctx, endpoint, owned)
				case "reset":
					id, err := ResetRequestID(credential.account.ID, "fixture-credit")
					if err != nil {
						t.Fatal(err)
					}
					command, err := NewOwnedResetRedemption(endpoint.Origin, "fixture-credit", id, owned)
					if err != nil {
						t.Fatal(err)
					}
					response, requestErr = client.ConsumeResetCredit(ctx, command)
				default:
					op := upstreamservice.CodexRateLimits
					if operation == "history" {
						op = upstreamservice.CodexUsageHistory
					}
					read, err := NewOwnedAccountRead(endpoint.Origin, op, owned)
					if err != nil {
						t.Fatal(err)
					}
					response, requestErr = client.ReadAccount(ctx, read)
				}
				if response != nil {
					_, readErr := io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					if readErr != nil {
						t.Fatal(readErr)
					}
				}
				if unavailable {
					if requestErr == nil || proxyCalls.Load() != beforeProxy {
						t.Fatalf("unavailable proxy did not fail closed: %v", requestErr)
					}
				} else {
					if requestErr != nil || proxyCalls.Load() != beforeProxy+1 {
						t.Fatalf("account operation missed SOCKS: %v", requestErr)
					}
					request := <-observed
					if operation == "oauth" {
						if request.Host != "auth.openai.com" || request.Header.Get("Authorization") != "" {
							t.Fatal("token request changed authority or leaked an access token")
						}
					} else if request.Host != "chatgpt.com" || request.Header.Get("Authorization") != "Bearer "+accessToken {
						t.Fatal("account operation lost its target or authentication")
					}
				}
				if network.calls.Load() != beforeDials+1 || network.direct.Load() != 0 {
					t.Fatal("account operation retried or bypassed its explicit proxy")
				}
				started, terminal := audit.attempts()
				if acquire != nil && (started.Decision().SelectionSource != egressprofile.SelectionGlobal || started.Decision().DefaultRevision != 2) {
					t.Fatal("owned global operation lost frozen selection audit")
				}
				if started.Decision().ProxyID != credential.account.EgressProfile.ID.String() || started.Decision().ProxyRevision != uint64(credential.account.EgressProfile.Revision) || started.Decision().AccountSettingsRevision != credential.account.SettingsRevision || started.Decision().AccountID != credential.account.ID || !terminal.Terminal() || terminal.Decision() != started.Decision() {
					t.Fatal("account operation omitted effective egress or terminal evidence")
				}
			})
		}
	}
}

type ownedWorkflowAudit struct {
	runtimeAuditRecorder
	allMu     sync.Mutex
	completed []egressaudit.Attempt
	started   []egressaudit.Attempt
}

func (a *ownedWorkflowAudit) Append(ctx context.Context, attempt egressaudit.Attempt) (egressaudit.Record, error) {
	result, err := a.runtimeAuditRecorder.Append(ctx, attempt)
	if err == nil {
		a.allMu.Lock()
		a.started = append(a.started, attempt)
		a.allMu.Unlock()
	}
	return result, err
}
func (a *ownedWorkflowAudit) startedSnapshot() []egressaudit.Attempt {
	a.allMu.Lock()
	defer a.allMu.Unlock()
	return append([]egressaudit.Attempt(nil), a.started...)
}

func (a *ownedWorkflowAudit) Complete(ctx context.Context, attempt egressaudit.Attempt) (egressaudit.Record, error) {
	a.allMu.Lock()
	a.completed = append(a.completed, attempt)
	a.allMu.Unlock()
	return a.runtimeAuditRecorder.Complete(ctx, attempt)
}
func (a *ownedWorkflowAudit) snapshot() []egressaudit.Attempt {
	a.allMu.Lock()
	defer a.allMu.Unlock()
	return append([]egressaudit.Attempt(nil), a.completed...)
}

type accountCodexEgressCredential struct{ *runtimeDiscoveryCredential }

func (accountCodexEgressCredential) Driver() providerauth.DriverRef {
	return providerauth.CodexOAuthDriverRef()
}

type accountLoopbackNetwork struct {
	proxy, upstream string
	calls, direct   atomic.Int64
	dnsCalls        atomic.Int64
}

func (network *accountLoopbackNetwork) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	network.dnsCalls.Add(1)
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

func (network *accountLoopbackNetwork) DialContext(ctx context.Context, kind, address string) (net.Conn, error) {
	network.calls.Add(1)
	if address != network.proxy {
		network.direct.Add(1)
		return nil, errors.New("test refused direct target or unexpected destination")
	}
	return (&net.Dialer{}).DialContext(ctx, kind, address)
}

// Synthetic in-memory secret seam; account/default metadata use the real Store.
type accountGlobalSecrets struct{ *secretReaderStub }

func (s *accountGlobalSecrets) Inspect(context.Context, secretstore.Reference) (secretstore.Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revision == 0 {
		return secretstore.Metadata{State: secretstore.StateMissing}, nil
	}
	return secretstore.Metadata{State: secretstore.StateConfigured, Revision: s.revision}, nil
}
func (s *accountGlobalSecrets) Replace(_ context.Context, c secretstore.ReplaceCommand) (secretstore.Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ExpectedRevision != s.revision {
		return secretstore.Metadata{}, secretstore.ErrRevisionConflict
	}
	b, err := c.Value.CopyBytes()
	if err != nil {
		return secretstore.Metadata{}, err
	}
	clear(s.value)
	s.value = b
	s.revision++
	return secretstore.Metadata{State: secretstore.StateConfigured, Revision: s.revision}, nil
}
func (s *accountGlobalSecrets) Delete(context.Context, secretstore.Reference) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.value)
	s.value = nil
	s.revision = 0
	return nil
}

func newAccountSOCKSFixture(t *testing.T, upstream string) (net.Listener, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer connection.Close()
				_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
				// The production dialer resolves to the fixture's IPv4 address.
				greeting := make([]byte, 3)
				if _, err := io.ReadFull(connection, greeting); err != nil || string(greeting) != "\x05\x01\x00" {
					return
				}
				if _, err := connection.Write([]byte{5, 0}); err != nil {
					return
				}
				request := make([]byte, 10)
				if _, err := io.ReadFull(connection, request); err != nil || string(request) != "\x05\x01\x00\x01\x7f\x00\x00\x01\x01\xbb" {
					return
				}
				peer, err := net.DialTimeout("tcp4", upstream, time.Second)
				if err != nil {
					return
				}
				defer peer.Close()
				_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
				calls.Add(1)
				if _, err := connection.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
					return
				}
				done := make(chan struct{})
				go func() {
					_, _ = io.Copy(peer, connection)
					_ = peer.Close()
					close(done)
				}()
				_, _ = io.Copy(connection, peer)
				_ = connection.Close()
				<-done
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); workers.Wait() })
	return listener, &calls
}
