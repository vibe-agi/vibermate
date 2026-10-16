package productruntime

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/accountoperation"
	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

const ownedQuotaPath = "/backend-api/wham/usage"
const ownedHistoryPath = "/backend-api/wham/profiles/me"
const ownedDetailsPath = "/backend-api/wham/rate-limit-reset-credits"
const ownedConsumePath = "/backend-api/wham/rate-limit-reset-credits/consume"

// Reuse the runtime's private dial/counter fixture, narrowing its multi-family
// allowlist to this task's sole synthetic authority and exact proxy addresses.
type ownedClosedNetwork struct{ *runtimeClosedNetwork }

func (n ownedClosedNetwork) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if network != "ip" || host != "chatgpt.com" {
		n.denied.Add(1)
		return nil, errors.New("owned fixture rejected DNS")
	}
	return n.runtimeClosedNetwork.LookupNetIP(ctx, network, host)
}
func (n ownedClosedNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" || (address != "chatgpt.com:443" && address != "127.0.0.1:443" && address != n.proxy && address != n.otherProxy) {
		n.denied.Add(1)
		return nil, errors.New("owned fixture rejected dial")
	}
	return n.runtimeClosedNetwork.DialContext(ctx, network, address)
}

// The private adapter preserves the real Client, final authenticator, gate,
// dispatch policy and audit. Only its TLS roots and closed sockets differ.
type ownedRuntimeWire struct {
	builder *egressnetwork.Builder
	roots   *x509.CertPool
	before  func(*http.Request)
}

func (w *ownedRuntimeWire) RoundTrip(r *http.Request, d providertransport.TransportDispatch) (*http.Response, transportprofile.Evidence, error) {
	allowed := r.Method == "GET" && (r.URL.Path == ownedQuotaPath || r.URL.Path == ownedHistoryPath || r.URL.Path == ownedDetailsPath) || r.Method == "POST" && r.URL.Path == ownedConsumePath
	if r.URL.Scheme != "https" || r.URL.Host != "chatgpt.com" || r.URL.RawQuery != "" || !allowed {
		return nil, transportprofile.Evidence{}, errors.New("closed owned fixture rejected HTTP target")
	}
	if w.before != nil {
		w.before(r)
	}
	dialer, err := w.builder.Dialer(d.EgressPolicy())
	if err != nil {
		return nil, transportprofile.Evidence{}, err
	}
	transport := &http.Transport{DialContext: dialer.DialContext, DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: w.roots, MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: time.Second}
	response, err := transport.RoundTrip(r)
	return response, transportprofile.Evidence{}, err
}

type ownedRuntimeFixture struct {
	r                                     *Runtime
	gate                                  *offlinehold.Gate
	wire                                  *ownedRuntimeWire
	network                               *runtimeClosedNetwork
	proxy, proxyB                         net.Listener
	dials, dialsB                         *atomic.Int64
	profile, profileB                     egressprofile.ProfileRevision
	account                               provideraccount.View
	quota, history, details, consume, bad atomic.Int64
	started                               map[string]egressaudit.DecisionRef
	mu                                    sync.Mutex
	afterDetails                          func()
	dropConsume                           bool
}

func newOwnedRuntimeFixture(t *testing.T, rejectTLS ...bool) *ownedRuntimeFixture {
	t.Helper()
	f := &ownedRuntimeFixture{started: map[string]egressaudit.DecisionRef{}}
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1999988400,"https://api.openai.com/auth":{"chatgpt_account_id":"synthetic-owned"}}`)) + ".synthetic"
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(71), DNSNames: []string{"chatgpt.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Host != "chatgpt.com" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("ChatGPT-Account-Id") != "synthetic-owned" || r.Header.Get("Proxy-Authorization") != "" {
			f.bad.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET " + ownedQuotaPath:
			f.quota.Add(1)
			io.WriteString(w, `{"plan_type":"pro","rate_limit_reset_credits":{"available_count":1}}`)
		case "GET " + ownedHistoryPath:
			f.history.Add(1)
			io.WriteString(w, `{"stats":{"lifetime_tokens":7,"daily_usage_buckets":[]}}`)
		case "GET " + ownedDetailsPath:
			f.details.Add(1)
			if f.afterDetails != nil {
				f.afterDetails()
			}
			io.WriteString(w, `{"available_count":1,"credits":[{"id":"credit-owned","reset_type":"codex_rate_limits","status":"available","granted_at":"2026-09-01T00:00:00Z"}]}`)
		case "POST " + ownedConsumePath:
			f.consume.Add(1)
			var payload map[string]string
			if json.Unmarshal(body, &payload) != nil || len(payload) != 2 || payload["credit_id"] != "credit-owned" || payload["redeem_request_id"] != "0f61e2d4-21b6-51fe-b648-bc68aa426258" || r.Header.Get("Content-Type") != "application/json" {
				f.bad.Add(1)
			}
			if f.dropConsume {
				c, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					c.Close()
				}
				return
			}
			io.WriteString(w, `{"code":"reset","windows_reset":1}`)
		default:
			f.bad.Add(1)
			w.WriteHeader(400)
		}
		if r.Method == "GET" && len(body) != 0 {
			f.bad.Add(1)
		}
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.Config.ErrorLog = log.New(io.Discard, "", 0) // intentional untrusted-root probe failures
	server.StartTLS()
	t.Cleanup(server.Close)
	f.proxy, f.dials = runtimeSOCKSFixture(t, server.Listener.Addr().String())
	f.proxyB, f.dialsB = runtimeSOCKSFixture(t, server.Listener.Addr().String())
	f.network = &runtimeClosedNetwork{proxy: f.proxy.Addr().String(), otherProxy: f.proxyB.Addr().String(), directTarget: server.Listener.Addr().String()}
	closedNetwork := ownedClosedNetwork{f.network}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: closedNetwork, SystemResolver: closedNetwork})
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	f.wire = &ownedRuntimeWire{builder: builder, roots: roots}
	probeRoots := roots
	if len(rejectTLS) != 0 && rejectTLS[0] {
		probeRoots = x509.NewCertPool()
	}
	f.gate, err = offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions(t, hostcontract.Desktop(), f.gate)
	f.r, err = startWithBuilders(t.Context(), options, runtimeBuildOverrides{
		originalProber: func() (*originaltransport.TLSProber, error) {
			return originaltransport.NewTLSProberWithBuilder(builder, probeRoots)
		},
		providerProber: func() (*providertransport.ProviderProber, error) {
			return providertransport.NewProviderProberWithBuilder(builder, roots)
		},
		provider: func(request providerBuildRequest) (providerRuntime, error) {
			codex, err := providertransport.NewCodexOAuthAuthenticator(request.secrets)
			if err != nil {
				return nil, err
			}
			return providertransport.NewClient(providertransport.ClientOptions{EgressDefaults: request.egressDefaults, Coordinator: request.coordinator, Authenticators: []providertransport.Authenticator{codex}, Transport: f.wire, InstanceIDs: request.instanceIDs, Audit: request.audit, RawEvidence: request.rawEvidence})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRuntime(t, f.r) })
	for _, p := range []struct {
		id       string
		listener net.Listener
		dest     *egressprofile.ProfileRevision
	}{{"profile.owned-a", f.proxy, &f.profile}, {"profile.owned-b", f.proxyB, &f.profileB}} {
		policy := egressnetwork.DefaultPolicy()
		policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: p.listener.Addr().String()}
		*p.dest, err = f.r.EgressProfiles().Publish(t.Context(), egressprofile.PublishCommand{ID: egressprofile.ID(p.id), DisplayName: p.id, Policy: policy})
		if err != nil {
			t.Fatal(err)
		}
	}
	oauth, err := codexoauth.ImportAuthJSON([]byte(fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q,"id_token":%q,"refresh_token":"synthetic-owned-refresh","account_id":"synthetic-owned"},"last_refresh":%q}`, token, token, time.Now().Format(time.RFC3339Nano))))
	if err != nil {
		t.Fatal(err)
	}
	defer oauth.Destroy()
	encoded, err := oauth.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	material, err := providerauth.NewMaterial(string(encoded), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	wrapped, err := material.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(wrapped)
	value, err := secretstore.NewValue(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	f.account, err = f.r.ProviderAccounts().Create(t.Context(), provideraccount.CreateCommand{ID: "account.owned", DisplayName: "Synthetic owned", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.CodexOAuthDriverRef(), Secret: value})
	if err != nil {
		t.Fatal(err)
	}
	f.wire.before = func(_ *http.Request) { f.captureStarted(t) }
	return f
}

func (f *ownedRuntimeFixture) captureStarted(t *testing.T) {
	t.Helper()
	page, err := f.r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 50})
	if err != nil {
		t.Error("read initial persisted audit", err)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, record := range page.Items {
		if !record.Attempt.Terminal() {
			f.started[record.Attempt.ID()] = record.Attempt.Decision()
		}
	}
}

type ownedCounts struct{ quota, history, details, consume, proxyA, proxyB, direct, audits int64 }

func (f *ownedRuntimeFixture) assertCounts(t *testing.T, want ownedCounts) {
	t.Helper()
	got := ownedCounts{f.quota.Load(), f.history.Load(), f.details.Load(), f.consume.Load(), f.dials.Load(), f.dialsB.Load(), f.network.direct.Load(), 0}
	page, err := f.r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	got.audits = int64(len(page.Items))
	if got != want || f.bad.Load() != 0 || f.network.denied.Load() != 0 {
		t.Fatalf("owned HTTP/socket/audit counts got=%+v want=%+v bad=%d denied=%d", got, want, f.bad.Load(), f.network.denied.Load())
	}
	t.Logf("literal owned operation counts=%+v", got)
}

func (f *ownedRuntimeFixture) assertAudits(t *testing.T, source egressprofile.SelectionSource, profileID string, defaultRevision, settingsRevision uint64, failed, actions int) {
	t.Helper()
	page, err := f.r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	gotFailed := 0
	gotActions := 0
	for _, record := range page.Items {
		a := record.Attempt
		d := a.Decision()
		if a.Purpose() == egressaudit.PurposeUpstreamAccountAction {
			gotActions++
		} else if a.Purpose() != egressaudit.PurposeUpstreamAccountRead {
			t.Fatal("owned operation changed its purpose", a)
		}
		if !a.Terminal() || d != f.started[a.ID()] || d.SelectionSource != source || d.ProxyID != profileID || d.ProxyRevision != 1 || d.DefaultRevision != defaultRevision || d.AccountID != "account.owned" || d.AccountSettingsRevision != settingsRevision || d.PolicyID != "account.owned" || d.PolicyRevision != settingsRevision || d.RuleID != "account.egress" || a.TargetOrigin() != "https://chatgpt.com" {
			t.Fatal("owned terminal audit changed initial selection or sender authority", a)
		}
		if a.Outcome() == egressaudit.OutcomeFailed {
			gotFailed++
		} else if a.Outcome() != egressaudit.OutcomeCompleted {
			t.Fatal("unexpected owned terminal audit outcome", a)
		}
	}
	if gotFailed != failed || gotActions != actions {
		t.Fatalf("failed/action audit count=%d/%d want=%d/%d", gotFailed, gotActions, failed, actions)
	}
}

func (f *ownedRuntimeFixture) assertEditedAudits(t *testing.T, original int) {
	t.Helper()
	page, err := f.r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	old, next := 0, 0
	for _, record := range page.Items {
		a := record.Attempt
		d := a.Decision()
		if !a.Terminal() || d != f.started[a.ID()] || d.AccountID != "account.owned" || d.AccountSettingsRevision != 1 || d.ProxyRevision != 1 {
			t.Fatal("old or new operation rewrote its initial audit", a)
		}
		if d.SelectionSource == egressprofile.SelectionGlobal && d.DefaultRevision == 2 && d.ProxyID == "profile.owned-a" {
			old++
		} else if d.SelectionSource == egressprofile.SelectionBuiltin && d.DefaultRevision == 3 && d.ProxyID == "profile.direct" && a.Purpose() == egressaudit.PurposeUpstreamAccountRead && a.Outcome() == egressaudit.OutcomeCompleted {
			next++
		} else {
			t.Fatal("old or new operation changed selected exit", a)
		}
	}
	if old != original || next != 1 {
		t.Fatalf("old/new immutable audit counts=%d/%d want=%d/1", old, next, original)
	}
}

func (f *ownedRuntimeFixture) run(ctx context.Context, operation string) error {
	if operation == "reset" {
		result, err := f.r.AccountReads().RedeemOwned(ctx, f.account.Account.ID, f.account.Account.Revision, "credit-owned")
		if err == nil && (result.AccountID != "account.owned" || result.CredentialEpoch != 1 || result.CreditID != "credit-owned" || result.Outcome != "reset" || result.WindowsReset != 1) {
			return errors.New("redemption lost managed account or result")
		}
		return err
	}
	id := upstreamservice.CodexRateLimits
	if operation == "history" {
		id = upstreamservice.CodexUsageHistory
	}
	result, err := f.r.AccountReads().ReadOwned(ctx, f.account.Account.ID, id)
	if err == nil {
		if result.Facts.AccountID != "account.owned" || result.Facts.CredentialEpoch != 1 || result.Facts.State != "known" {
			return errors.New("owned read lost managed account facts")
		}
		if operation == "quota" && (result.Facts.RateLimitResets == nil || len(result.Facts.RateLimitResets.Details) != 1 || result.Facts.RateLimitResets.Details[0].ID != "credit-owned") {
			return errors.New("quota lost owner reset-detail enrichment")
		}
		if operation == "history" && (result.Facts.History == nil || result.Facts.History.LifetimeTokens == nil || *result.Facts.History.LifetimeTokens != 7) {
			return errors.New("owner history facts lost")
		}
	}
	return err
}

func (f *ownedRuntimeFixture) managerDrain(t *testing.T) {
	t.Helper()
	view, err := f.r.ProviderAccounts().Get(t.Context(), f.account.Account.ID)
	if err != nil || view.Health.CredentialEpoch != 1 {
		t.Fatal("owned operation rotated synthetic credential", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := f.r.accounts.Shutdown(ctx); err != nil {
		t.Fatal("owned Manager leases did not drain", err)
	}
	t.Log("actual Manager shutdown joined all owned credential leases; epoch=1")
}

// Literal rows are independent of observed audits/policies. Independent owner
// operations have account, global and builtin layers; no captured flow exists.
func TestActualRuntimeOwnedAccountSelectionMatrix(t *testing.T) {
	for _, layer := range []struct {
		name                              string
		source                            egressprofile.SelectionSource
		profileID                         string
		defaultRevision, settingsRevision uint64
		proxyA, direct                    int64
	}{
		{"AP", egressprofile.SelectionAccount, "profile.owned-a", 0, 2, 1, 0},
		{"AD", egressprofile.SelectionAccount, "profile.direct", 0, 2, 0, 1},
		{"G", egressprofile.SelectionGlobal, "profile.owned-a", 2, 1, 1, 0},
		{"B", egressprofile.SelectionBuiltin, "profile.direct", 1, 1, 0, 1},
	} {
		for _, op := range []struct {
			name                string
			q, h, d, c, sockets int64
		}{{"quota", 1, 0, 1, 0, 2}, {"history", 0, 1, 0, 0, 1}, {"reset", 0, 0, 1, 1, 2}} {
			t.Run(layer.name+"/"+op.name, func(t *testing.T) {
				f := newOwnedRuntimeFixture(t)
				if layer.name == "AP" || layer.name == "AD" {
					f.setDefault(t, 1, &f.profileB)
					selected := f.profile
					if layer.name == "AD" {
						selected = egressprofile.Direct()
					}
					var err error
					f.account, err = f.r.accounts.SetSettings(t.Context(), provideraccount.SettingsCommand{ID: f.account.Account.ID, ExpectedRevision: 1, EgressProfile: selected})
					if err != nil {
						t.Fatal(err)
					}
				} else if layer.name == "G" {
					f.setDefault(t, 1, &f.profile)
				}
				if err := f.run(t.Context(), op.name); err != nil {
					t.Fatal(err)
				}
				f.assertCounts(t, ownedCounts{op.q, op.h, op.d, op.c, layer.proxyA * op.sockets, 0, layer.direct * op.sockets, op.sockets})
				f.assertAudits(t, layer.source, layer.profileID, layer.defaultRevision, layer.settingsRevision, 0, int(op.c))
				f.drain(t)
				f.managerDrain(t)
			})
		}
	}
}

func TestActualRuntimeOwnedAccountHeldHistoryAndResetFreeze(t *testing.T) {
	for _, op := range []struct {
		name                string
		q, h, d, c, sockets int64
	}{{"history", 0, 1, 0, 0, 2}, {"reset", 0, 0, 1, 1, 3}} {
		t.Run(op.name, func(t *testing.T) {
			f := newOwnedRuntimeFixture(t)
			f.setDefault(t, 1, &f.profile)
			if _, err := f.r.EnterOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- f.run(ctx, op.name) }()
			targets := ownedWaitQueue(t, f)
			if len(targets) != 1 || targets[0].TargetRef != "https://chatgpt.com" || targets[0].PlanRevision != 0 || targets[0].PlanDigest != "" || targets[0].EgressPolicy != f.profile.Policy || f.dials.Load() != 0 || f.network.direct.Load() != 0 {
				t.Error("held owned operation did not freeze its canonical reachability")
			}
			f.setDefault(t, 2, nil)
			if _, err := f.r.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
				cancel()
				<-result
				f.drain(t)
				t.Fatal(err)
			}
			if err := <-result; err != nil {
				t.Error(err)
			}
			f.assertCounts(t, ownedCounts{op.q, op.h, op.d, op.c, op.sockets, 0, 0, op.sockets - 1})
			f.assertAudits(t, egressprofile.SelectionGlobal, "profile.owned-a", 2, 1, 0, int(op.c))
			f.drain(t)
			// The next actual Reader operation must see the newly unset default.
			if err := f.run(t.Context(), "history"); err != nil {
				t.Fatal(err)
			}
			f.assertCounts(t, ownedCounts{op.q, op.h + 1, op.d, op.c, op.sockets, 0, 1, op.sockets})
			f.assertEditedAudits(t, int(op.sockets-1))
			f.drain(t)
			f.managerDrain(t)
		})
	}
}

func TestActualRuntimeOwnedAccountFailureBoundaries(t *testing.T) {
	for _, operation := range []string{"quota", "history", "reset"} {
		for _, failure := range []string{"held_cancel", "held_proxy_probe", "held_tls_probe", "proxy_lost_before_send"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				f := newOwnedRuntimeFixture(t, failure == "held_tls_probe")
				f.setDefault(t, 1, &f.profile)
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				var requestErr error
				wantDials, wantAudits := int64(0), int64(0)
				if failure == "proxy_lost_before_send" {
					f.wire.before = func(_ *http.Request) { f.captureStarted(t); f.proxy.Close(); f.setDefault(t, 2, nil) }
					requestErr = f.run(ctx, operation)
					wantAudits = 1
				} else {
					if _, err := f.r.EnterOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
						t.Fatal(err)
					}
					result := make(chan error, 1)
					go func() { result <- f.run(ctx, operation) }()
					ownedWaitQueue(t, f)
					f.setDefault(t, 2, nil)
					if failure == "held_cancel" {
						cancel()
					} else {
						if failure == "held_proxy_probe" {
							f.proxy.Close()
						} else {
							wantDials = 1
						}
						_, err := f.r.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision)
						if err == nil || f.gate.Snapshot().State != offlinehold.StateHeld || f.gate.Snapshot().QueuedRequests != 1 {
							t.Error("concrete failed probe released owned operation", err)
						}
						cancel()
					}
					requestErr = <-result
				}
				if requestErr == nil {
					t.Error("failed owned operation succeeded")
				}
				if failure == "held_cancel" && !errors.Is(requestErr, context.Canceled) {
					t.Error("held cancellation lost caller result", requestErr)
				}
				if failure == "proxy_lost_before_send" && operation == "reset" && errors.Is(requestErr, accountoperation.ErrResetUnconfirmed) {
					t.Error("reset details failure falsely reached consume")
				}
				f.assertCounts(t, ownedCounts{proxyA: wantDials, audits: wantAudits})
				f.assertAudits(t, egressprofile.SelectionGlobal, "profile.owned-a", 2, 1, int(wantAudits), 0)
				f.drain(t)
				f.managerDrain(t)
			})
		}
	}
}

func TestActualRuntimeOwnedAccountResetBetweenDetailsAndConsume(t *testing.T) {
	for _, mode := range []string{"default_edit", "proxy_lost", "consume_response_lost"} {
		t.Run(mode, func(t *testing.T) {
			f := newOwnedRuntimeFixture(t)
			f.setDefault(t, 1, &f.profile)
			f.afterDetails = func() {
				f.setDefault(t, 2, nil)
				if mode == "proxy_lost" {
					f.proxy.Close()
				}
			}
			f.dropConsume = mode == "consume_response_lost"
			err := f.run(t.Context(), "reset")
			if mode == "default_edit" && err != nil {
				t.Fatal(err)
			}
			if mode != "default_edit" && !errors.Is(err, accountoperation.ErrResetUnconfirmed) {
				t.Fatal("ambiguous consume lost existing unconfirmed result", err)
			}
			wantConsume, wantDials, failed := int64(1), int64(2), 0
			if mode == "proxy_lost" {
				wantConsume, wantDials, failed = 0, 1, 1
			}
			if mode == "consume_response_lost" {
				failed = 1
			}
			f.assertCounts(t, ownedCounts{details: 1, consume: wantConsume, proxyA: wantDials, audits: 2})
			f.assertAudits(t, egressprofile.SelectionGlobal, "profile.owned-a", 2, 1, failed, 1)
			f.drain(t)
			// Independent history sees the edit even when the prior command failed.
			if err := f.run(t.Context(), "history"); err != nil {
				t.Fatal(err)
			}
			f.assertCounts(t, ownedCounts{history: 1, details: 1, consume: wantConsume, proxyA: wantDials, direct: 1, audits: 3})
			f.assertEditedAudits(t, 2)
			f.drain(t)
			f.managerDrain(t)
		})
	}
}

func (f *ownedRuntimeFixture) setDefault(t *testing.T, revision uint64, profile *egressprofile.ProfileRevision) {
	t.Helper()
	command := egressprofile.SetDefaultCommand{ExpectedRevision: revision}
	if profile != nil {
		command.Profile = &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}
	}
	if _, err := f.r.EgressDefaults().SetDefault(t.Context(), command); err != nil {
		t.Fatal(err)
	}
}

func ownedWaitQueue(t *testing.T, f *ownedRuntimeFixture) []offlinehold.ProbeTarget {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for f.gate.Snapshot().QueuedRequests != 1 && ctx.Err() == nil {
		runtime.Gosched()
	}
	if f.gate.Snapshot().QueuedRequests != 1 {
		t.Fatal("real owned operation did not queue", f.gate.Snapshot())
	}
	return f.gate.PendingProbeTargets()
}

func (f *ownedRuntimeFixture) drain(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := f.gate.Drain(ctx); err != nil {
		t.Fatal("owned operation did not drain", err)
	}
	s := f.gate.Snapshot()
	if s.ActiveActions != 0 || s.ActiveEgress != 0 || s.QueuedRequests != 0 || s.HeldBytes != 0 {
		t.Fatal("owned admission retained", s)
	}
	t.Logf("joined owned operation drain=%+v quota=%d history=%d details=%d consume=%d proxyA=%d proxyB=%d direct=%d denied=%d", s, f.quota.Load(), f.history.Load(), f.details.Load(), f.consume.Load(), f.dials.Load(), f.dialsB.Load(), f.network.direct.Load(), f.network.denied.Load())
}

func TestActualRuntimeOwnedAccountHeldQuotaConcreteRecovery(t *testing.T) {
	f := newOwnedRuntimeFixture(t)
	f.setDefault(t, 1, &f.profile)
	if _, err := f.r.EnterOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := f.r.AccountReads().ReadOwned(ctx, f.account.Account.ID, upstreamservice.CodexRateLimits)
		result <- err
	}()
	targets := ownedWaitQueue(t, f)
	t.Logf("actual owned quota queued targets=%+v snapshot=%+v", targets, f.gate.Snapshot())
	if len(targets) != 1 || targets[0].Kind != offlinehold.EgressAuxiliary || targets[0].Transport != offlinehold.ProbeTransportStrictTLS || targets[0].TargetRef != "https://chatgpt.com" || targets[0].NetworkOrigin != "https://chatgpt.com" || targets[0].HTTPAuthority != "chatgpt.com" || targets[0].TLSServerName != "chatgpt.com" || targets[0].PlanRevision != 0 || targets[0].PlanDigest != "" || targets[0].TargetAccess != egressnetwork.TargetAccessOwnerConfigured || targets[0].EgressPolicy != f.profile.Policy {
		t.Error("actual owned quota did not queue canonical frozen probe identity")
	}
	f.setDefault(t, 2, nil)
	_, resumeErr := f.r.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision)
	if resumeErr != nil {
		t.Errorf("actual owned quota concrete Resume failed: %v; HTTP quota=%d details=%d proxy=%d direct=%d", resumeErr, f.quota.Load(), f.details.Load(), f.dials.Load(), f.network.direct.Load())
		cancel()
		<-result
		f.drain(t)
		return
	}
	if err := <-result; err != nil {
		t.Error(err)
	}
	f.drain(t)
	f.assertCounts(t, ownedCounts{quota: 1, details: 1, proxyA: 3, audits: 2})
	f.assertAudits(t, egressprofile.SelectionGlobal, "profile.owned-a", 2, 1, 0, 0)
	if err := f.run(t.Context(), "history"); err != nil {
		t.Fatal(err)
	}
	f.assertCounts(t, ownedCounts{quota: 1, history: 1, details: 1, proxyA: 3, direct: 1, audits: 3})
	f.assertEditedAudits(t, 2)
	f.drain(t)
	f.managerDrain(t)
}

func TestActualRuntimeOwnedAccountHeldConsumeConcreteRecovery(t *testing.T) {
	for _, mode := range []string{"resume", "cancel", "proxy_probe_failure", "tls_probe_failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newOwnedRuntimeFixture(t, mode == "tls_probe_failure")
			f.setDefault(t, 1, &f.profile)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			held := make(chan error, 1)
			f.afterDetails = func() {
				go func() { _, err := f.r.EnterOfflineHold(ctx, f.gate.Snapshot().Revision); held <- err }()
				for f.gate.Snapshot().State != offlinehold.StateEntering && ctx.Err() == nil {
					runtime.Gosched()
				}
			}
			result := make(chan error, 1)
			go func() { result <- f.run(ctx, "reset") }()
			targets := ownedWaitQueue(t, f)
			if err := <-held; err != nil {
				cancel()
				<-result
				f.drain(t)
				t.Fatal(err)
			}
			if len(targets) != 1 || targets[0].Kind != offlinehold.EgressAuxiliary || targets[0].Transport != offlinehold.ProbeTransportStrictTLS || targets[0].TargetRef != "https://chatgpt.com" || targets[0].PlanRevision != 0 || targets[0].PlanDigest != "" || targets[0].EgressPolicy != f.profile.Policy || targets[0].TargetAccess != egressnetwork.TargetAccessOwnerConfigured || f.gate.Snapshot().HeldBytes != 87 || f.details.Load() != 1 || f.consume.Load() != 0 {
				t.Error("explicit consume did not hold its own closed target/body")
			}
			f.setDefault(t, 2, nil)
			if mode != "resume" {
				wantDials := int64(1)
				if mode != "cancel" {
					if mode == "proxy_probe_failure" {
						f.proxy.Close()
					} else {
						wantDials = 2
					}
					_, err := f.r.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision)
					if err == nil || f.gate.Snapshot().State != offlinehold.StateHeld || f.gate.Snapshot().QueuedRequests != 1 {
						t.Error("failed concrete consume probe released POST", err)
					}
				}
				cancel()
				if err := <-result; !errors.Is(err, accountoperation.ErrResetUnconfirmed) || !errors.Is(err, context.Canceled) {
					t.Error("held consume cancellation changed unconfirmed result", err)
				}
				f.assertCounts(t, ownedCounts{details: 1, proxyA: wantDials, audits: 1})
				f.assertAudits(t, egressprofile.SelectionGlobal, "profile.owned-a", 2, 1, 0, 0)
				f.drain(t)
				f.managerDrain(t)
				return
			}
			if _, err := f.r.ResumeOfflineHold(t.Context(), f.gate.Snapshot().Revision); err != nil {
				cancel()
				<-result
				f.drain(t)
				t.Fatal(err)
			}
			if err := <-result; err != nil {
				t.Error(err)
			}
			f.assertCounts(t, ownedCounts{details: 1, consume: 1, proxyA: 3, audits: 2})
			f.assertAudits(t, egressprofile.SelectionGlobal, "profile.owned-a", 2, 1, 0, 1)
			f.drain(t)
			if err := f.run(t.Context(), "history"); err != nil {
				t.Fatal(err)
			}
			f.assertCounts(t, ownedCounts{history: 1, details: 1, consume: 1, proxyA: 3, direct: 1, audits: 3})
			f.assertEditedAudits(t, 2)
			f.drain(t)
			f.managerDrain(t)
		})
	}
}
