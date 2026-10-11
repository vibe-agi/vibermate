package productruntime_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
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
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type auxNetwork struct {
	proxyA, proxyB, target    string
	a, b, direct, denied, dns atomic.Int64
}

func (n *auxNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var target string
	switch {
	case network != "tcp":
		n.denied.Add(1)
		return nil, errors.New("account auxiliary fixture denied network")
	case address == n.proxyA:
		n.a.Add(1)
		target = n.proxyA
	case address == n.proxyB:
		n.b.Add(1)
		target = n.proxyB
	case address == "auth.openai.com:443" || address == "api.openai.com:443" || address == "93.184.216.34:443":
		n.direct.Add(1)
		target = n.target
	default:
		n.denied.Add(1)
		return nil, errors.New("account auxiliary fixture denied target")
	}
	return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, target)
}

func (n *auxNetwork) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	if network != "ip" || (host != "auth.openai.com" && host != "api.openai.com") {
		n.denied.Add(1)
		return nil, errors.New("account auxiliary fixture denied DNS")
	}
	n.dns.Add(1)
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

type auxSecrets struct {
	secretstore.Store
	creates, rotations atomic.Int64
}

func (s *auxSecrets) Replace(ctx context.Context, c secretstore.ReplaceCommand) (secretstore.Metadata, error) {
	m, err := s.Store.Replace(ctx, c)
	if err == nil {
		if c.ExpectedRevision == 0 {
			s.creates.Add(1)
		} else {
			s.rotations.Add(1)
		}
	}
	return m, err
}

type auxWire struct {
	builder   *egressnetwork.Builder
	roots     *x509.CertPool
	sends     atomic.Int64
	before    func()
	rejectTLS bool
}

func (w *auxWire) RoundTrip(req *http.Request, d providertransport.TransportDispatch) (*http.Response, transportprofile.Evidence, error) {
	allowed := req.Method == http.MethodPost && req.URL.String() == codexoauth.TokenURL ||
		req.Method == http.MethodGet && req.URL.String() == "https://api.openai.com/v1/models"
	if !allowed {
		return nil, transportprofile.Evidence{}, errors.New("account auxiliary fixture denied request")
	}
	w.sends.Add(1)
	if w.before != nil {
		w.before()
	}
	dialer, err := w.builder.Dialer(d.EgressPolicy())
	if err != nil {
		return nil, transportprofile.Evidence{}, err
	}
	roots := w.roots
	if w.rejectTLS {
		roots = x509.NewCertPool()
	}
	tr := &http.Transport{DialContext: dialer.DialContext, DisableKeepAlives: true,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: time.Second}
	response, err := tr.RoundTrip(req)
	tr.CloseIdleConnections()
	return response, transportprofile.Evidence{}, err
}

type auxFixture struct {
	runtime                      *productruntime.Runtime
	gate                         *offlinehold.Gate
	secrets                      *auxSecrets
	network                      *auxNetwork
	wire                         *auxWire
	proxyA, proxyB               net.Listener
	callsA, callsB, liveA, liveB *atomic.Int64
	liveTLS                      atomic.Int64
	profileA, profileB           egressprofile.ProfileRevision
	startsMu                     sync.Mutex
	starts                       map[string]egressaudit.DecisionRef
	parents                      map[string]egressaudit.ParentRef
	serverLog                    auxServerLog
	databasePath                 string
}

// Preserve every server diagnostic and distinguish deliberate strict TLS
// rejection from unexpected server noise.
type auxServerLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *auxServerLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, string(p))
	return len(p), nil
}

func (f *auxFixture) assertServerDiagnostics(t *testing.T, rejections int) {
	t.Helper()
	f.serverLog.mu.Lock()
	defer f.serverLog.mu.Unlock()
	if len(f.serverLog.lines) != rejections {
		t.Fatalf("TLS server diagnostics=%q want deliberate rejections=%d", f.serverLog.lines, rejections)
	}
	for _, line := range f.serverLog.lines {
		if !strings.Contains(line, "TLS handshake error") || !strings.Contains(line, "remote error: tls: bad certificate") {
			t.Fatalf("unexpected TLS server diagnostic: %s", line)
		}
		t.Logf("expected strict TLS rejection diagnostic: %s", strings.TrimSpace(line))
	}
}

func newAuxFixture(t *testing.T, handler http.Handler) *auxFixture {
	t.Helper()
	f := &auxFixture{starts: map[string]egressaudit.DecisionRef{}, parents: map[string]egressaudit.ParentRef{}}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(83), DNSNames: []string{"auth.openai.com", "api.openai.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(&f.serverLog, "", 0)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			f.liveTLS.Add(1)
		case http.StateClosed:
			f.liveTLS.Add(-1)
		}
	}
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	f.proxyA, f.callsA, f.liveA = updateSOCKS(t, server.Listener.Addr().String(), 4*time.Second)
	f.proxyB, f.callsB, f.liveB = updateSOCKS(t, server.Listener.Addr().String(), 4*time.Second)
	f.network = &auxNetwork{proxyA: f.proxyA.Addr().String(), proxyB: f.proxyB.Addr().String(), target: server.Listener.Addr().String()}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: f.network, SystemResolver: f.network})
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	f.wire = &auxWire{builder: builder, roots: roots}
	f.gate, err = offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	options := productruntime.ProductUpdateTestOptions(t, f.gate)
	f.databasePath = options.Paths.DatabasePath()
	f.secrets = &auxSecrets{Store: options.Secrets}
	options.Secrets = f.secrets
	f.runtime, err = productruntime.StartProductUpdateTestRuntime(t.Context(), options, f.wire, builder, roots)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.shutdown(t) })
	for _, p := range []struct {
		id       egressprofile.ID
		listener net.Listener
		profile  *egressprofile.ProfileRevision
	}{
		{"profile.account-aux-a", f.proxyA, &f.profileA}, {"profile.account-aux-b", f.proxyB, &f.profileB},
	} {
		policy := egressnetwork.DefaultPolicy()
		policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: p.listener.Addr().String()}
		*p.profile, err = f.runtime.EgressProfiles().Publish(t.Context(), egressprofile.PublishCommand{ID: p.id, DisplayName: p.id.String(), Policy: policy})
		if err != nil {
			t.Fatal(err)
		}
	}
	f.wire.before = func() {
		page, err := f.runtime.EgressAttempts().List(context.Background(), egressaudit.PageRequest{Limit: 100})
		if err != nil {
			t.Error(err)
			return
		}
		f.startsMu.Lock()
		defer f.startsMu.Unlock()
		for _, record := range page.Items {
			if !record.Attempt.Terminal() {
				f.starts[record.Attempt.ID()] = record.Attempt.Decision()
				f.parents[record.Attempt.ID()] = record.Attempt.Parent()
			}
		}
	}
	return f
}

func (f *auxFixture) shutdown(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.runtime.Shutdown(ctx); err != nil {
		t.Error("aux shutdown", err)
	}
}

func auxWait(t *testing.T, ready func() bool) {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for !ready() {
		if time.Now().After(until) {
			t.Fatal("account auxiliary fixture did not reach observed state")
		}
		time.Sleep(time.Millisecond)
	}
}

func auxCallback(t *testing.T, view codexoauth.LoginView) string {
	t.Helper()
	auth, err := url.Parse(view.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	return auth.Query().Get("redirect_uri") + "?" + url.Values{"state": {auth.Query().Get("state")}, "code": {"synthetic-code"}}.Encode()
}

func auxToken() string {
	claims := fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":"synthetic-aux-account"}}`, time.Now().Add(time.Hour).Unix())
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".synthetic"
}

func (f *auxFixture) setDefault(t *testing.T, expected uint64, profile *egressprofile.ProfileRevision) {
	t.Helper()
	var ref *egressprofile.ProfileRef
	if profile != nil {
		ref = &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}
	}
	if _, err := f.runtime.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: expected, Profile: ref}); err != nil {
		t.Fatal(err)
	}
}

func (f *auxFixture) assertDrained(t *testing.T) {
	t.Helper()
	auxWait(t, func() bool {
		s := f.gate.Snapshot()
		return s.QueuedRequests == 0 && s.ActiveEgress == 0 && s.ActiveActions == 0 && s.EnteringActions == 0 && s.HeldBytes == 0 && f.liveA.Load() == 0 && f.liveB.Load() == 0 && f.liveTLS.Load() == 0
	})
	t.Logf("drained gate=%+v liveA=%d liveB=%d liveTLS=%d", f.gate.Snapshot(), f.liveA.Load(), f.liveB.Load(), f.liveTLS.Load())
}

func auxCreateAccount(t *testing.T, f *auxFixture, id provideraccount.ID, oauth bool) provideraccount.View {
	t.Helper()
	endpoint, driver := upstreamendpoint.OpenAIPlatformID, providerauth.StaticHeaderDriverRef()
	credential := "synthetic-aux-bearer"
	headers, deleted := map[string]string{"X-Aux-Fixture": "owned"}, []string{"X-Aux-Deleted"}
	if oauth {
		endpoint, driver = upstreamendpoint.ChatGPTOfficialID, providerauth.CodexOAuthDriverRef()
		token := auxToken()
		imported := []byte(fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q,"id_token":%q,"refresh_token":"synthetic-refresh-old","account_id":"synthetic-aux-account"},"last_refresh":%q}`, token, token, time.Now().Format(time.RFC3339Nano)))
		defer clear(imported)
		oauthCredential, err := codexoauth.ImportAuthJSON(imported)
		if err != nil {
			t.Fatal(err)
		}
		defer oauthCredential.Destroy()
		encoded, err := oauthCredential.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		defer clear(encoded)
		credential, headers, deleted = string(encoded), nil, nil
	}
	material, err := providerauth.NewMaterial(credential, headers, deleted)
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
	view, err := f.runtime.ProviderAccounts().Create(t.Context(), provideraccount.CreateCommand{ID: id, DisplayName: "Synthetic auxiliary", UpstreamEndpointID: endpoint, Driver: driver, Secret: value, AutomaticRefresh: false})
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func auxSetChoice(t *testing.T, f *auxFixture, account provideraccount.View, choice string) provideraccount.View {
	t.Helper()
	var selected egressprofile.ProfileRevision
	switch choice {
	case "AP":
		selected = f.profileA
	case "AD":
		selected = egressprofile.Direct()
	case "G", "B":
	default:
		t.Fatal("unknown auxiliary choice", choice)
	}
	if choice != "B" {
		f.setDefault(t, 1, &f.profileB)
	}
	view, err := f.runtime.ProviderAccounts().SetSettings(t.Context(), provideraccount.SettingsCommand{ID: account.Account.ID, ExpectedRevision: account.Account.SettingsRevision, EgressProfile: selected, AutomaticRefresh: false})
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func auxAssertAudit(t *testing.T, f *auxFixture, purpose egressaudit.EgressPurpose, origin string, expectedResolved egressprofile.Resolved, accountID string, settings uint64, outcome egressaudit.Outcome) {
	t.Helper()
	page, err := f.runtime.EgressAttempts().List(context.Background(), egressaudit.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("aux audit count=%d want=1", len(page.Items))
	}
	attempt := page.Items[0].Attempt
	authority, err := egressaudit.AuthorityForPurpose(purpose)
	if err != nil {
		t.Fatal(err)
	}
	want := egressaudit.BuiltInDirectDecision(authority)
	want.SelectionSource, want.DefaultRevision = expectedResolved.Source(), expectedResolved.DefaultRevision()
	want.ProxyID, want.ProxyRevision = expectedResolved.Profile().ID.String(), uint64(expectedResolved.Profile().Revision)
	if accountID != "" {
		want.PolicyID, want.PolicyRevision, want.RuleID = accountID, settings, "account.egress"
		want.AccountID, want.AccountSettingsRevision = accountID, settings
	} else if expectedResolved.Source() == egressprofile.SelectionGlobal {
		want.PolicyID, want.PolicyRevision, want.RuleID = "runtime.egress-default", expectedResolved.DefaultRevision(), "global.default"
	}
	f.startsMu.Lock()
	start, ok := f.starts[attempt.ID()]
	parent, parentOK := f.parents[attempt.ID()]
	starts := len(f.starts)
	f.startsMu.Unlock()
	if !ok || starts != 1 || !parentOK || parent != attempt.Parent() || start != want || attempt.Decision() != want || !attempt.Terminal() || attempt.Outcome() != outcome || attempt.Purpose() != purpose || attempt.TargetOrigin() != origin || attempt.PayloadClass() != egressaudit.PayloadRuntime || attempt.Caller() != egressaudit.CallerCore || attempt.Parent().Kind != egressaudit.ParentRuntimeAction || attempt.Parent().ID == "" || attempt.Parent().ExchangeID != "" {
		t.Fatalf("aux immutable SQL audit mismatch start=%+v terminal=%+v want=%+v parent=%+v outcome=%s", start, attempt.Decision(), want, attempt.Parent(), attempt.Outcome())
	}
	t.Logf("audit id=%s start=%+v terminal=%+v purpose=%s origin=%s parent=%+v outcome=%s", attempt.ID(), start, attempt.Decision(), purpose, origin, attempt.Parent(), attempt.Outcome())
}
