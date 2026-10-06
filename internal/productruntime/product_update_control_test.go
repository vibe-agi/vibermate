package productruntime_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/runtimecontrol"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type updateNetwork struct {
	proxies                map[string]bool
	unexpected, dials, dns atomic.Int64
}

func (n *updateNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	n.dials.Add(1)
	if !n.proxies[address] {
		n.unexpected.Add(1)
		return nil, errors.New("contained update denied dial")
	}
	return (&net.Dialer{}).DialContext(ctx, network, address)
}
func (n *updateNetwork) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	n.dns.Add(1)
	if network != "ip" || (host != "api.github.com" && host != "api.openai.com") {
		n.unexpected.Add(1)
		return nil, errors.New("contained update denied DNS")
	}
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

type updateWire struct {
	builder *egressnetwork.Builder
	roots   *x509.CertPool
	sends   *atomic.Int64
}

func (w updateWire) RoundTrip(r *http.Request, d providertransport.TransportDispatch) (*http.Response, transportprofile.Evidence, error) {
	if w.sends != nil {
		w.sends.Add(1)
	}
	if r.URL.String() != "https://api.github.com/repos/vibe-agi/vibermate/releases/latest" && r.URL.String() != "https://api.openai.com/v1/models" {
		return nil, transportprofile.Evidence{}, errors.New("contained update denied request")
	}
	dialer, err := w.builder.Dialer(d.EgressPolicy())
	if err != nil {
		return nil, transportprofile.Evidence{}, err
	}
	transport := &http.Transport{DialContext: dialer.DialContext, DisableKeepAlives: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: w.roots}, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: time.Second}
	response, err := transport.RoundTrip(r)
	return response, transportprofile.Evidence{}, err
}
func updateSOCKS(t *testing.T, target string, lifetime time.Duration) (net.Listener, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	count := &atomic.Int64{}
	live := &atomic.Int64{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			live.Add(1)
			go func() {
				defer wg.Done()
				defer live.Add(-1)
				defer c.Close()
				c.SetDeadline(time.Now().Add(lifetime))
				g := make([]byte, 3)
				if _, err := io.ReadFull(c, g); err != nil || string(g) != "\x05\x01\x00" {
					return
				}
				c.Write([]byte{5, 0})
				r := make([]byte, 10)
				if _, err := io.ReadFull(c, r); err != nil || string(r) != "\x05\x01\x00\x01\x5d\xb8\xd8\x22\x01\xbb" {
					return
				}
				peer, err := net.DialTimeout("tcp4", target, time.Second)
				if err != nil {
					return
				}
				defer peer.Close()
				peer.SetDeadline(time.Now().Add(lifetime))
				count.Add(1)
				c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				done := make(chan struct{})
				go func() { io.Copy(peer, c); peer.Close(); close(done) }()
				io.Copy(c, peer)
				c.Close()
				peer.Close()
				<-done
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); wg.Wait() })
	return listener, count, live
}

type updateReady struct{}

func (updateReady) Ready() bool { return true }

type updateManualUnused struct{}

func (updateManualUnused) ServeHTTP(w http.ResponseWriter, _ *http.Request, _ controlprincipal.Principal) {
	w.WriteHeader(503)
}

func TestActualOwnerProductUpdateUsesRuntimeGlobalSOCKSAndStrictTLS(t *testing.T) {
	runActualOwnerProductUpdate(t, false)
}
func TestActualOwnerProductUpdateHoldResumesThroughRealFrozenTLSProbe(t *testing.T) {
	runActualOwnerProductUpdate(t, true)
}
func TestActualOwnerProductUpdateQueuedCancellationAndWholeBudgetNeverLaterSend(t *testing.T) {
	for _, edge := range []string{"cancel", "expire"} {
		t.Run(edge, func(t *testing.T) { runActualOwnerProductUpdate(t, true, edge) })
	}
}
func TestActualRuntimeMixedProviderAndUpdateQueueUsesBothConcreteProbers(t *testing.T) {
	runActualOwnerProductUpdate(t, true, "mixed")
}
func TestActualOwnerProductUpdateRejectsUnauthorizedOrOpenInputsBeforeAnyEgress(t *testing.T) {
	runActualOwnerProductUpdate(t, false, "owner-negatives")
}
func TestActualOwnerProductUpdateRejectsRealWireFailuresWithoutDirectRetry(t *testing.T) {
	for _, edge := range []string{"wire-proxy-down", "wire-redirect", "wire-oversize", "wire-malformed"} {
		t.Run(edge, func(t *testing.T) { runActualOwnerProductUpdate(t, false, edge) })
	}
}
func TestActualOwnerProductUpdateSlowBodyUsesOneEightSecondBudgetAndDrains(t *testing.T) {
	runActualOwnerProductUpdate(t, false, "wire-slow")
}
func TestActualOwnerProductUpdateUnavailableDefaultNeverBecomesDirect(t *testing.T) {
	runActualOwnerProductUpdate(t, false, "default-unavailable")
}
func runActualOwnerProductUpdate(t *testing.T, held bool, edges ...string) {
	wireEdge := ""
	if len(edges) > 0 && strings.HasPrefix(edges[0], "wire-") {
		wireEdge = edges[0]
	}
	var responseProgress atomic.Int64
	serverDone := make(chan struct{})

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(5), DNSNames: []string{"api.github.com", "api.openai.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var gets, bad atomic.Int64
	var modelGets atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "api.openai.com" && r.URL.RequestURI() == "/v1/models" {
			modelGets.Add(1)
			if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer synthetic-update-mixed" {
				bad.Add(1)
				w.WriteHeader(400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[]}`)
			return
		}
		gets.Add(1)
		if r.Method != "GET" || r.Host != "api.github.com" || r.URL.RequestURI() != "/repos/vibe-agi/vibermate/releases/latest" || r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" || r.Header.Get("Authorization") != "" {
			bad.Add(1)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch wireEdge {
		case "wire-redirect":
			w.Header().Set("Location", "https://unapproved.invalid/redirect")
			w.WriteHeader(302)
			return
		case "wire-oversize":
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			io.WriteString(w, strings.Repeat(" ", 256*1024+1))
			return
		case "wire-malformed":
			io.WriteString(w, "{malformed-private-response")
			return
		case "wire-slow":
			defer close(serverDone)
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			ticker := time.NewTicker(400 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
					if _, err := io.WriteString(w, " "); err != nil {
						return
					}
					w.(http.Flusher).Flush()
					responseProgress.Add(1)
				}
			}
		}
		io.WriteString(w, `{"tag_name":"v0.1.24","name":"Private release","html_url":"https://github.com/vibe-agi/vibermate/releases/tag/v0.1.24","published_at":"2026-10-05T00:00:00Z","draft":false,"prerelease":false}`)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	defer server.Close()
	tunnelLifetime := 3 * time.Second
	if wireEdge == "wire-slow" {
		tunnelLifetime = 12 * time.Second
	} // private fixture bound exceeds real production8s deadline.
	proxy, calls, activeProxy := updateSOCKS(t, server.Listener.Addr().String(), tunnelLifetime)
	nextProxy, nextCalls, activeNext := updateSOCKS(t, server.Listener.Addr().String(), tunnelLifetime)
	network := &updateNetwork{proxies: map[string]bool{proxy.Addr().String(): true, nextProxy.Addr().String(): true}}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	var sends atomic.Int64
	options := productruntime.ProductUpdateTestOptions(t, gate)
	r, err := productruntime.StartProductUpdateTestRuntime(t.Context(), options, updateWire{builder: builder, roots: roots, sends: &sends}, builder, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	policy := egressnetwork.DefaultPolicy()
	policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: proxy.Addr().String()}
	profile, err := r.EgressProfiles().Publish(t.Context(), egressprofile.PublishCommand{ID: "profile.update-private", DisplayName: "Private update", Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: 1}}); err != nil {
		t.Fatal(err)
	}
	application, err := runtimecontrol.New(runtimecontrol.Options{Runtime: r, Readiness: updateReady{}, Clock: desktopcontrol.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	readToken := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	writeToken := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("w", 32)))
	auth, err := desktopcontrol.NewAuthenticator(desktopcontrol.CapabilityGrant{ReadToken: readToken, WriteToken: writeToken, ExpiresAt: time.Now().Add(time.Hour)}, desktopcontrol.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := controlprincipal.New(controlprincipal.Attributes{ID: "desktop:update-test", Kind: controlprincipal.KindDesktopApp, CredentialRevision: 1, AllowedGrantKinds: []controlprincipal.GrantKind{controlprincipal.GrantManualCapture}})
	if err != nil {
		t.Fatal(err)
	}
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{Authority: "127.0.0.1:18662", AllowedOrigins: []string{"vibermate://desktop"}, Authenticator: auth, Application: application, Bootstrap: http.NotFoundHandler(), CLIControl: http.NotFoundHandler(), ManualCaptures: updateManualUnused{}, DesktopPrincipal: principal})
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) > 0 && edges[0] == "owner-negatives" {
		cases := []struct {
			name, method, path, body, token, contentType string
			status                                       int
		}{
			{"unauthenticated", "POST", "/api/v1/product-update/check", `{}`, "", "application/json", 401},
			{"readonly", "POST", "/api/v1/product-update/check", `{}`, readToken, "application/json", 401},
			{"foreign capability", "POST", "/api/v1/product-update/check", `{}`, base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("f", 32))), "application/json", 401},
			{"caller URL field", "POST", "/api/v1/product-update/check", `{"url":"https://unapproved.invalid/release"}`, writeToken, "application/json", 422},
			{"caller URL query", "POST", "/api/v1/product-update/check?url=https%3A%2F%2Funapproved.invalid", `{}`, writeToken, "application/json", 422},
			{"extra field", "POST", "/api/v1/product-update/check", `{"extra":null}`, writeToken, "application/json", 422},
			{"duplicate field", "POST", "/api/v1/product-update/check", `{"url":"one","url":"two"}`, writeToken, "application/json", 422},
			{"null body", "POST", "/api/v1/product-update/check", `null`, writeToken, "application/json", 422},
			{"array body", "POST", "/api/v1/product-update/check", `[]`, writeToken, "application/json", 422},
			{"missing body", "POST", "/api/v1/product-update/check", "", writeToken, "application/json", 422},
			{"malformed body", "POST", "/api/v1/product-update/check", `{`, writeToken, "application/json", 422},
			{"trailing JSON", "POST", "/api/v1/product-update/check", `{} {}`, writeToken, "application/json", 422},
			{"wrong media type", "POST", "/api/v1/product-update/check", `{}`, writeToken, "text/plain", 422},
			// The existing /api/v1/ closed catch-all deliberately projects
			// unsupported method patterns as control_route_not_found, not405.
			{"GET method", "GET", "/api/v1/product-update/check", `{}`, readToken, "application/json", 404},
			{"PUT method", "PUT", "/api/v1/product-update/check", `{}`, writeToken, "application/json", 404},
			{"DELETE method", "DELETE", "/api/v1/product-update/check", `{}`, writeToken, "application/json", 404},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
				request.Host = "127.0.0.1:18662"
				request.RemoteAddr = "127.0.0.1:5530"
				request.Header.Set("Origin", "vibermate://desktop")
				request.Header.Set("Content-Type", test.contentType)
				if test.token != "" {
					request.Header.Set("Authorization", "Bearer "+test.token)
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				if response.Code != test.status {
					t.Errorf("actual owner boundary status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
				}
				page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
				if err != nil || len(page.Items) != 0 {
					t.Fatalf("rejected input created outgoing audit %+v err=%v", page, err)
				}
				if sends.Load() != 0 || gets.Load() != 0 || modelGets.Load() != 0 || calls.Load() != 0 || nextCalls.Load() != 0 || network.dials.Load() != 0 || network.dns.Load() != 0 || network.unexpected.Load() != 0 {
					t.Fatalf("rejected owner input reached egress: send=%d GET=%d/%d proxy=%d/%d dial=%d DNS=%d unexpected=%d", sends.Load(), gets.Load(), modelGets.Load(), calls.Load(), nextCalls.Load(), network.dials.Load(), network.dns.Load(), network.unexpected.Load())
				}
				if snapshot := gate.Snapshot(); snapshot.QueuedRequests != 0 || snapshot.ActiveEgress != 0 || snapshot.ActiveActions != 0 {
					t.Fatalf("rejected input entered Hold/action %+v", snapshot)
				}
			})
		}
		return
	}
	request := httptest.NewRequest("POST", "/api/v1/product-update/check", strings.NewReader(`{}`))
	request.Host = "127.0.0.1:18662"
	request.RemoteAddr = "127.0.0.1:5530"
	request.Header.Set("Origin", "vibermate://desktop")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+writeToken)
	if len(edges) > 0 && edges[0] == "default-unavailable" {
		// Test-only persisted configuration fault; never SQL authority/session/
		// credential mutation. Restore exact known singleton before shutdown.
		db, err := sql.Open("sqlite", options.Paths.DatabasePath())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`DELETE FROM runtime_egress_default WHERE singleton=1`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := db.Exec(`INSERT INTO runtime_egress_default(singleton,revision,egress_id,egress_revision) VALUES(1,2,?,1)`, profile.ID); err != nil {
				t.Error("restore private default fault", err)
			}
		}()
		if _, err := r.EgressDefaults().GetDefault(t.Context()); err == nil {
			t.Fatal("fixture failed to make actual persisted default unavailable")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != 503 || sends.Load() != 0 || network.dials.Load() != 0 || network.dns.Load() != 0 || calls.Load() != 0 || nextCalls.Load() != 0 || gets.Load() != 0 {
			t.Fatalf("unavailable default became direct/unset: status%d sends%d dial%d DNS%d", response.Code, sends.Load(), network.dials.Load(), network.dns.Load())
		}
		page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
		if err != nil || len(page.Items) != 0 {
			t.Fatal("unresolved default created audit", err)
		}
		if snapshot := gate.Snapshot(); snapshot.ActiveActions != 0 || snapshot.QueuedRequests != 0 || snapshot.ActiveEgress != 0 {
			t.Fatal("unresolved selection entered Hold", snapshot)
		}
		return
	}
	if wireEdge != "" {
		if wireEdge == "wire-proxy-down" {
			proxy.Close()
		}
		response := httptest.NewRecorder()
		started := time.Now()
		router.ServeHTTP(response, request)
		elapsed := time.Since(started)
		if response.Code != 503 || !strings.Contains(response.Body.String(), `"code":"product_update_unavailable"`) || strings.Contains(response.Body.String(), "unapproved.invalid") || strings.Contains(response.Body.String(), "malformed-private") {
			t.Fatalf("real-wire failure not sanitized status%d body%s", response.Code, response.Body.String())
		}
		wantGET, wantProxy := int64(1), int64(1)
		if wireEdge == "wire-proxy-down" {
			wantGET, wantProxy = 0, 0
		}
		if sends.Load() != 1 || gets.Load() != wantGET || calls.Load() != wantProxy || nextCalls.Load() != 0 || modelGets.Load() != 0 || bad.Load() != 0 || network.dials.Load() != 1 || network.dns.Load() != 1 || network.unexpected.Load() != 0 {
			t.Fatalf("real-wire error retried/bypassed: sends%d GET%d proxy%d/%d model%d bad%d dial%d DNS%d unexpected%d", sends.Load(), gets.Load(), calls.Load(), nextCalls.Load(), modelGets.Load(), bad.Load(), network.dials.Load(), network.dns.Load(), network.unexpected.Load())
		}
		if wireEdge == "wire-slow" {
			if elapsed < 7*time.Second || elapsed > 9*time.Second || responseProgress.Load() < 10 {
				t.Fatalf("continuous body activity renewed/shortened8s budget elapsed%v fragments%d", elapsed, responseProgress.Load())
			}
			select {
			case <-serverDone:
			case <-time.After(2 * time.Second):
				t.Fatal("real canceled upstream body handler did not drain")
			}
		}
		deadline := time.Now().Add(2 * time.Second)
		for activeProxy.Load() != 0 || activeNext.Load() != 0 || gate.Snapshot().ActiveActions != 0 || gate.Snapshot().ActiveEgress != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("real-wire resources not drained sockets%d/%d Hold%+v", activeProxy.Load(), activeNext.Load(), gate.Snapshot())
			}
			time.Sleep(time.Millisecond)
		}
		snapshot := gate.Snapshot()
		if snapshot.QueuedRequests != 0 || snapshot.HeldBytes != 0 {
			t.Fatal("real-wire failure retained queue budget", snapshot)
		}
		page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("real-wire failure audit lost %+v %v", page, err)
		}
		attempt := page.Items[0].Attempt
		if !attempt.Terminal() || attempt.Purpose() != egressaudit.PurposeUpdate || attempt.Decision().SelectionSource != egressprofile.SelectionGlobal || attempt.Decision().DefaultRevision != 2 || attempt.Decision().ProxyID != profile.ID.String() {
			t.Fatal("wire failure changed frozen/audit identity", attempt)
		}
		wantOutcome := egressaudit.OutcomeCompleted
		if wireEdge == "wire-proxy-down" || wireEdge == "wire-redirect" || wireEdge == "wire-slow" {
			wantOutcome = egressaudit.OutcomeFailed
		}
		if attempt.Outcome() != wantOutcome {
			t.Fatalf("physical audit outcome%v want%v", attempt.Outcome(), wantOutcome)
		}
		t.Logf("edge=%s elapsed=%v bodyFragments=%d send=%d GET=%d proxy=%d next=%d dial=%d DNS=%d unexpected=%d audit=%s resources=0", wireEdge, elapsed, responseProgress.Load(), sends.Load(), gets.Load(), calls.Load(), nextCalls.Load(), network.dials.Load(), network.dns.Load(), network.unexpected.Load(), attempt.Outcome())
		return
	}
	response := httptest.NewRecorder()
	wantSockets := int64(1)
	if held {
		_, err := r.EnterOfflineHold(t.Context(), r.OfflineHoldSnapshot().Revision)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		request = request.WithContext(ctx)
		done := make(chan struct{})
		go func() { defer close(done); router.ServeHTTP(response, request) }()
		defer func() {
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("actual queued owner consumer did not join")
			}
		}()
		deadline := time.Now().Add(2 * time.Second)
		for gate.Snapshot().QueuedByKind[offlinehold.EgressUpdate] != 1 {
			if time.Now().After(deadline) {
				t.Fatal("actual update never queued", gate.Snapshot())
			}
			time.Sleep(time.Millisecond)
		}
		targets := gate.PendingProbeTargets()
		if len(targets) != 1 || targets[0].Kind != offlinehold.EgressUpdate || targets[0].TargetRef != "https://api.github.com" || targets[0].NetworkOrigin != "https://api.github.com" || targets[0].HTTPAuthority != "api.github.com" || targets[0].TLSServerName != "api.github.com" || targets[0].PlanRevision != 0 || targets[0].PlanDigest != "" || targets[0].TargetAccess != 0 || targets[0].EgressPolicy != profile.Policy {
			t.Fatalf("actual update queue producer identity/frozen selection changed %+v", targets)
		}
		if gets.Load() != 0 || calls.Load() != 0 {
			t.Fatal("Hold leaked probe or update sender before Resume")
		}
		if len(edges) > 0 && edges[0] != "mixed" {
			started := time.Now()
			if edges[0] == "cancel" {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(9 * time.Second):
				t.Fatal("real queued update exceeded whole8s budget")
			}
			if edges[0] == "expire" && time.Since(started) < 7*time.Second {
				t.Fatal("expiry test did not reach real8s Reader budget")
			}
			if response.Code != 503 || gate.Snapshot().QueuedRequests != 0 || gate.Snapshot().ActiveEgress != 0 || gate.Snapshot().HeldBytes != 0 {
				t.Fatalf("actual canceled/expired queue not drained status%d snapshot%+v", response.Code, gate.Snapshot())
			}
			if _, err := r.ResumeOfflineHold(t.Context(), r.OfflineHoldSnapshot().Revision); err != nil {
				t.Fatal("empty queue resume failed", err)
			}
			if gets.Load() != 0 || calls.Load() != 0 || nextCalls.Load() != 0 || network.unexpected.Load() != 0 {
				t.Fatal("canceled/expired operation later sent/probed")
			}
			page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
			if err != nil || len(page.Items) != 0 {
				t.Fatal("unsent queued update acquired outgoing audit", err)
			}
			return
		}
		var modelDone chan error
		if len(edges) > 0 && edges[0] == "mixed" {
			material, err := providerauth.NewMaterial("synthetic-update-mixed", nil, nil)
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
			account, err := r.ProviderAccounts().Create(t.Context(), provideraccount.CreateCommand{ID: "account.update-mixed", DisplayName: "Private mixed", UpstreamEndpointID: upstreamendpoint.OpenAIPlatformID, Driver: providerauth.StaticHeaderDriverRef(), Secret: secret})
			if err != nil {
				t.Fatal(err)
			}
			endpoint, err := r.UpstreamEndpoints().Get(t.Context(), upstreamendpoint.OpenAIPlatformID)
			if err != nil {
				t.Fatal(err)
			}
			credential, err := r.ProviderAccounts().AcquireEndpointCredential(ctx, account.Account.ID, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			defer credential.Release()
			modelDone = make(chan error, 1)
			go func() {
				response, err := r.FetchEndpointModels(ctx, endpoint, credential)
				if response != nil && response.Body != nil {
					io.Copy(io.Discard, response.Body)
					response.Body.Close()
				}
				modelDone <- err
			}()
			defer func() {
				cancel()
				select {
				case <-modelDone:
				case <-time.After(2 * time.Second):
					t.Error("actual mixed provider consumer did not join")
				}
			}()
			deadline := time.Now().Add(2 * time.Second)
			for gate.Snapshot().QueuedByKind[offlinehold.EgressProvider] != 1 {
				if time.Now().After(deadline) {
					t.Fatal("real provider did not queue", gate.Snapshot())
				}
				time.Sleep(time.Millisecond)
			}
		}
		nextPolicy := policy
		nextPolicy.Proxy.Endpoint = nextProxy.Addr().String()
		next, err := r.EgressProfiles().Publish(t.Context(), egressprofile.PublishCommand{ID: "profile.update-next", DisplayName: "Private next", Policy: nextPolicy})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 2, Profile: &egressprofile.ProfileRef{ID: next.ID, Revision: 1}}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.ResumeOfflineHold(t.Context(), r.OfflineHoldSnapshot().Revision); err != nil {
			t.Fatal("actual Runtime update resume rejected", err)
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("resumed update did not drain")
		}
		wantSockets = 2 // genuine no-HTTP TLS probe, then the one fixed GET.
		if modelDone != nil {
			select {
			case err := <-modelDone:
				if err != nil {
					t.Fatal("mixed provider failed", err)
				}
				modelDone <- err
			case <-time.After(2 * time.Second):
				t.Fatal("mixed provider did not drain")
			}
			wantSockets = 4
			if modelGets.Load() != 1 {
				t.Fatal("mixed provider probe sent HTTP or model send duplicated", modelGets.Load())
			}
		}
	} else {
		router.ServeHTTP(response, request)
	}
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"tag":"v0.1.24"`) || calls.Load() != wantSockets || nextCalls.Load() != 0 || gets.Load() != 1 || bad.Load() != 0 || network.unexpected.Load() != 0 {
		t.Fatalf("actual owner closed update missing: status%d response%s sockets%d/%d GET%d bad%d unexpected%d", response.Code, response.Body.String(), calls.Load(), nextCalls.Load(), gets.Load(), bad.Load(), network.unexpected.Load())
	}
	page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
	wantAudits := 1
	if len(edges) > 0 && edges[0] == "mixed" {
		wantAudits = 2
	}
	if err != nil || len(page.Items) != wantAudits {
		t.Fatalf("actual update SQL audit %+v %v", page, err)
	}
	var updateAudits int
	for _, record := range page.Items {
		actual := record.Attempt
		if actual.Purpose() == egressaudit.PurposeUpdate {
			updateAudits++
		}
		if actual.Decision().SelectionSource != egressprofile.SelectionGlobal || actual.Decision().DefaultRevision != 2 || actual.Decision().ProxyID != profile.ID.String() || !actual.Terminal() {
			t.Fatal("actual update audit lost original frozen operation", actual)
		}
	}
	if updateAudits != 1 {
		t.Fatal("actual closed update purpose changed", updateAudits)
	}
}
