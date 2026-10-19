package productruntime

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

type runtimeNativeEgressConfig struct {
	choice, edge  string
	recorder      observationRecorderBuilder
	fullRecording bool
}

// A contained transport seam: real Runtime/Store/receipt authority and TLS
// remain in use. This does not qualify external VLESS/REALITY interoperability.
type runtimeNativeVLESS struct{ network *runtimeNativeNetwork }

func (v runtimeNativeVLESS) DialContext(ctx context.Context, p egressnetwork.ProxyPolicy, network, address string) (net.Conn, error) {
	if p.Kind != egressnetwork.ProxyVLESS || p.CredentialRef != "secret://egress-vless/native-fixture" || p.CredentialRevision != 1 || address != "127.0.0.1:443" {
		v.network.unexpected.Add(1)
		return nil, errors.New("unexpected frozen VLESS identity/target")
	}
	return v.network.DialContext(ctx, network, address)
}

type runtimeNativeExpected struct {
	ProfileID       string
	ProfileRevision uint64
	Source          egressprofile.SelectionSource
	DefaultRevision uint64
}
type runtimeNativeCall struct {
	ID       nativetool.ID
	Captured nativemcp.CapturedRequest
	Done     <-chan nativemcp.Terminal
	Cancel   context.CancelFunc
	Response *httptest.ResponseRecorder
}
type runtimeNativeFlight struct {
	call   runtimeNativeCall
	joined bool
}
type runtimeNativeCounts struct{ DirectAttempts, DirectConnections, AAttempts, BAttempts, AConnects, BConnects, TLS, HTTP, Unexpected, Credentials int64 }
type runtimeNativeRequest struct{ Method, Cursor, Body string }

// Only these two proxy addresses and the synthetic native destination can dial.
type runtimeNativeNetwork struct {
	upstream, a, b                                                      string
	directAttempts, directConnections, aAttempts, bAttempts, unexpected atomic.Int64
}

func (n *runtimeNativeNetwork) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	if (network != "ip" && network != "ip4" && network != "ip6") || host != "native.example" {
		n.unexpected.Add(1)
		return nil, errors.New("native fixture denied DNS")
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}
func (n *runtimeNativeNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		n.unexpected.Add(1)
		return nil, errors.New("native fixture denied network")
	}
	switch address {
	case n.a:
		n.aAttempts.Add(1)
	case n.b:
		n.bAttempts.Add(1)
	case "127.0.0.1:443", "native.example:443":
		n.directAttempts.Add(1)
		c, e := (&net.Dialer{}).DialContext(ctx, "tcp4", n.upstream)
		if e == nil {
			n.directConnections.Add(1)
		}
		return c, e
	default:
		n.unexpected.Add(1)
		return nil, errors.New("native fixture denied destination")
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
}

type runtimeNativeProxy struct {
	net.Listener
	connects   atomic.Int64
	workers    sync.WaitGroup
	mu         sync.Mutex
	sockets    map[net.Conn]bool
	unexpected *atomic.Int64
}

func newRuntimeNativeProxy(t *testing.T, upstream string, unexpected *atomic.Int64) *runtimeNativeProxy {
	t.Helper()
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	p := &runtimeNativeProxy{Listener: l, sockets: make(map[net.Conn]bool), unexpected: unexpected}
	p.workers.Add(1)
	go func() {
		defer p.workers.Done()
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			p.mu.Lock()
			p.sockets[c] = true
			p.mu.Unlock()
			p.workers.Add(1)
			go p.serve(c, upstream)
		}
	}()
	return p
}
func (p *runtimeNativeProxy) serve(c net.Conn, upstream string) {
	defer p.workers.Done()
	defer c.Close()
	defer func() { p.mu.Lock(); delete(p.sockets, c); p.mu.Unlock() }()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	greeting := make([]byte, 3)
	if _, e := io.ReadFull(c, greeting); e != nil {
		return
	}
	if string(greeting) != "\x05\x01\x00" {
		p.unexpected.Add(1)
		return
	}
	if _, e := c.Write([]byte{5, 0}); e != nil {
		return
	}
	request := make([]byte, 10)
	if _, e := io.ReadFull(c, request); e != nil {
		return
	}
	if string(request) != "\x05\x01\x00\x01\x7f\x00\x00\x01\x01\xbb" {
		p.unexpected.Add(1)
		return
	}
	peer, e := net.DialTimeout("tcp4", upstream, time.Second)
	if e != nil {
		return
	}
	defer peer.Close()
	peer.SetDeadline(time.Now().Add(10 * time.Second))
	p.mu.Lock()
	p.sockets[peer] = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.sockets, peer); p.mu.Unlock() }()
	p.connects.Add(1)
	if _, e = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); e != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		io.Copy(peer, c)
		if tcp, ok := peer.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
		close(done)
	}()
	io.Copy(c, peer)
	c.Close()
	peer.Close()
	<-done
}
func (p *runtimeNativeProxy) closeAndJoin(t *testing.T) {
	t.Helper()
	p.drop()
	done := make(chan struct{})
	go func() { p.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("native proxy copy/accept drain exceeded safeguard")
	}
}
func (p *runtimeNativeProxy) drop() {
	_ = p.Close()
	p.mu.Lock()
	for c := range p.sockets {
		c.Close()
	}
	p.mu.Unlock()
}

type runtimeNativeWire struct {
	f       *runtimeNativeEgressFixture
	builder *egressnetwork.Builder
	roots   *x509.CertPool
}
type runtimeNativeEntry struct {
	id      nativetool.ID
	method  string
	cursor  string
	body    string
	release chan error
	ctx     context.Context
}

func (w *runtimeNativeWire) RoundTrip(r *http.Request, policy egressnetwork.Policy) (*http.Response, error) {
	if (r.Method != "POST" && r.Method != "GET") || r.URL.Scheme != "https" || r.URL.Host != "native.example" || r.URL.Path != environment.ClientToolMCPPath || r.URL.RawQuery != "" {
		w.f.network.unexpected.Add(1)
		return nil, errors.New("native fixture denied request target")
	}
	body, e := io.ReadAll(r.Body)
	if e != nil {
		return nil, e
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	w.f.mu.Lock()
	id, ok := w.f.requests[runtimeNativeRequestKey(r.Method, r.Header.Get("Last-Event-Id"), string(body))]
	w.f.mu.Unlock()
	if !ok {
		return nil, errors.New("native fixture denied unregistered RPC bytes")
	}
	// A separate observer reads real Start/receipt evidence while I/O is blocked.
	barrierContext, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	entry := runtimeNativeEntry{id: id, method: r.Method, cursor: r.Header.Get("Last-Event-Id"), body: string(body), release: make(chan error, 1), ctx: barrierContext}
	select {
	case w.f.entries <- entry:
	case <-barrierContext.Done():
		return nil, fmt.Errorf("native transport-entry observer safeguard: %w", barrierContext.Err())
	}
	select {
	case e = <-entry.release:
		if e != nil {
			return nil, e
		}
	case <-barrierContext.Done():
		return nil, fmt.Errorf("native transport-entry release safeguard: %w", barrierContext.Err())
	}
	dialer, e := w.builder.Dialer(policy)
	if e != nil {
		return nil, e
	}
	transport := &http.Transport{Proxy: nil, DialContext: dialer.DialContext, DisableKeepAlives: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: w.roots}}
	return transport.RoundTrip(r)
}

func (f *runtimeNativeEgressFixture) inspectEntry(entry runtimeNativeEntry) error {
	id := entry.id
	f.mu.Lock()
	expected := f.expected[id]
	wantCounts, checkCounts := f.entryCounts[id]
	f.observedEntries[id] = append(f.observedEntries[id], runtimeNativeRequest{entry.method, entry.cursor, entry.body})
	f.mu.Unlock()
	if got := f.counts(); checkCounts && got != wantCounts {
		return fmt.Errorf("native phase entry %s counts=%+v want=%+v", id, got, wantCounts)
	}
	record, e := f.audit(id)
	if e != nil {
		return e
	}
	if e = f.checkAudit(record, id, expected, "", ""); e != nil {
		return e
	}
	var once, consumed int
	var attemptID string
	if e = f.db.QueryRowContext(entry.ctx, `SELECT COUNT(*),COALESCE(MAX(attempt_id),'') FROM native_tool_once WHERE operation_id=?`, id.String()).Scan(&once, &attemptID); e != nil {
		return e
	}
	if e = f.db.QueryRowContext(entry.ctx, `SELECT COUNT(*) FROM native_tool_approvals WHERE operation_id=? AND consumed_at_unix_ms IS NOT NULL`, id.String()).Scan(&consumed); e != nil {
		return e
	}
	if once != 1 || attemptID != record.Attempt.ID() {
		return fmt.Errorf("native entry once=%d attempt=%s audit=%s", once, attemptID, record.Attempt.ID())
	}
	needsApproval := strings.Contains(entry.body, `"method":"tools/call"`) || strings.Contains(entry.body, `"id":"native-parent-ping"`)
	if needsApproval && consumed != 1 {
		return fmt.Errorf("native approved POST entry consumed Allow=%d", consumed)
	}
	op, e := f.r.NativeToolRepository().Get(entry.ctx, id)
	if e != nil {
		return e
	}
	if needsApproval {
		if op.ApprovalID == nil {
			return errors.New("native approved entry lacks actual Approval")
		}
		approval, err := f.r.NativeToolRepository().GetApproval(entry.ctx, *op.ApprovalID)
		if err != nil {
			return err
		}
		if approval.OperationID != id || approval.State != "allowed" || approval.ConsumedAt == nil || op.BindingDigest == nil || approval.BindingDigest != *op.BindingDigest {
			return fmt.Errorf("native actual consumed Approval=%+v", approval)
		}
	} else if op.ApprovalID != nil {
		return errors.New("native GET unexpectedly has Approval")
	}
	if !needsApproval && consumed != 0 {
		return fmt.Errorf("native GET entry consumed Allow=%d", consumed)
	}
	f.mu.Lock()
	f.started[id] = record
	f.mu.Unlock()
	return nil
}

type runtimeNativeEgressFixture struct {
	r                                   *Runtime
	gate                                *offlinehold.Gate
	admission                           captureadmission.Admission
	capture                             captureidentity.Reference
	connection                          interface{ Close() }
	db                                  *sql.DB
	databasePath                        string
	network                             *runtimeNativeNetwork
	server                              *httptest.Server
	proxyA, proxyB                      *runtimeNativeProxy
	roots                               *x509.CertPool
	wrongName                           atomic.Bool
	tlsCount, httpCount, badCredentials atomic.Int64
	profileA, profileB                  egressprofile.ProfileRef
	current                             runtimeNativeExpected
	mu                                  sync.Mutex
	next                                uint64
	bodies                              map[string]nativetool.ID
	requests                            map[string]nativetool.ID
	entryCounts                         map[nativetool.ID]runtimeNativeCounts
	observedEntries                     map[nativetool.ID][]runtimeNativeRequest
	endpointRequests                    []runtimeNativeRequest
	mode                                string
	edge                                string
	choice                              string
	expected                            map[nativetool.ID]runtimeNativeExpected
	started                             map[nativetool.ID]egressaudit.Record
	flights                             map[nativetool.ID]*runtimeNativeFlight
	cleaned                             bool
	entries                             chan runtimeNativeEntry
	observerCancel                      context.CancelFunc
	observerDone                        chan struct{}
	entryGates                          map[nativetool.ID]chan error
	entryErrors                         map[nativetool.ID]error
	acceptedRead                        chan struct{}
	acceptedRelease                     chan struct{}
	acceptedBody                        string
	acceptedReads                       atomic.Int64
	acceptedReleaseOnce                 sync.Once
}

func runtimeNativeRequestKey(method, cursor, body string) string {
	return method + "\x00" + cursor + "\x00" + body
}

func runtimeNativeCertificate(t *testing.T, name string, serial int64) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	cert, e := x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, cert
}
func newRuntimeNativeEgressFixture(t *testing.T, cfg runtimeNativeEgressConfig) *runtimeNativeEgressFixture {
	t.Helper()
	// Register temporary-data cleanup first so Runtime and SQL close before it.
	paths, e := NewRuntimePaths(filepath.Join(t.TempDir(), "runtime-data"))
	if e != nil {
		t.Fatal(e)
	}
	f := &runtimeNativeEgressFixture{databasePath: paths.DatabasePath(), network: &runtimeNativeNetwork{}, bodies: make(map[string]nativetool.ID), requests: make(map[string]nativetool.ID), entryCounts: make(map[nativetool.ID]runtimeNativeCounts), observedEntries: make(map[nativetool.ID][]runtimeNativeRequest), edge: cfg.edge, choice: cfg.choice, expected: make(map[nativetool.ID]runtimeNativeExpected), started: make(map[nativetool.ID]egressaudit.Record), flights: make(map[nativetool.ID]*runtimeNativeFlight), entryGates: make(map[nativetool.ID]chan error), entryErrors: make(map[nativetool.ID]error), acceptedRead: make(chan struct{}, 1), acceptedRelease: make(chan struct{})}
	t.Cleanup(func() { f.cleanup(t) })
	good, goodCert := runtimeNativeCertificate(t, "native.example", 1)
	bad, badCert := runtimeNativeCertificate(t, "wrong.native.example", 2)
	f.roots = x509.NewCertPool()
	f.roots.AddCert(goodCert)
	f.roots.AddCert(badCert)
	f.server = httptest.NewUnstartedServer(http.HandlerFunc(f.handle))
	f.server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if hello.ServerName != "native.example" {
			f.network.unexpected.Add(1)
			return nil, errors.New("native fixture denied SNI")
		}
		f.tlsCount.Add(1)
		if f.wrongName.Load() {
			return &bad, nil
		}
		return &good, nil
	}}
	f.server.StartTLS()
	f.network.upstream = f.server.Listener.Addr().String()
	f.proxyA = newRuntimeNativeProxy(t, f.network.upstream, &f.network.unexpected)
	f.proxyB = newRuntimeNativeProxy(t, f.network.upstream, &f.network.unexpected)
	f.network.a = f.proxyA.Addr().String()
	f.network.b = f.proxyB.Addr().String()
	builder, e := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: f.network, SystemResolver: f.network, VLESS: runtimeNativeVLESS{f.network}})
	if e != nil {
		t.Fatal(e)
	}
	f.gate, e = offlinehold.New(offlinehold.DefaultConfig())
	if e != nil {
		t.Fatal(e)
	}
	options := testOptionsWithPaths(t, paths, hostcontract.Desktop(), f.gate)
	options.NetworkOptions = egressnetwork.BuilderOptions{BaseDialer: f.network, SystemResolver: f.network}
	options.OriginalTransport = &runtimeNativeWire{f: f, builder: builder, roots: f.roots}
	f.r, e = startWithBuilders(context.Background(), options, runtimeBuildOverrides{recorder: cfg.recorder, originalProber: func() (*originaltransport.TLSProber, error) {
		return originaltransport.NewTLSProberWithBuilder(builder, f.roots)
	}})
	if e != nil {
		t.Fatal(e)
	}
	f.db, e = sql.Open("sqlite", options.Paths.DatabasePath())
	if e != nil {
		t.Fatal(e)
	}
	f.entries = make(chan runtimeNativeEntry)
	f.observerDone = make(chan struct{})
	observerContext, observerCancel := context.WithCancel(context.Background())
	f.observerCancel = observerCancel
	go func() {
		defer close(f.observerDone)
		for {
			select {
			case entry := <-f.entries:
				err := f.inspectEntry(entry)
				if f.holdEntry(entry) {
					gate := make(chan error, 1)
					f.mu.Lock()
					f.entryGates[entry.id] = gate
					f.entryErrors[entry.id] = err
					f.mu.Unlock()
					select {
					case err = <-gate:
					case <-entry.ctx.Done():
						err = entry.ctx.Err()
					case <-observerContext.Done():
						err = observerContext.Err()
					}
				}
				entry.release <- err
			case <-observerContext.Done():
				return
			}
		}
	}()
	publish := func(id, address string) egressprofile.ProfileRef {
		policy := egressnetwork.DefaultPolicy()
		policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: address}
		if strings.HasPrefix(cfg.choice, "global_vless") && id == "profile.native-a" {
			policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxyVLESS, CredentialRef: "secret://egress-vless/native-fixture", CredentialRevision: 1}
			policy.DirectDomains = "excluded.example"
			if cfg.choice == "global_vless_large" {
				var lines []string
				for i := 0; i < 960; i++ {
					lines = append(lines, fmt.Sprintf("d%03d.example.com", i))
				}
				policy.DirectDomains = egressnetwork.DirectDomains(strings.Join(lines, "\n"))
			}
		}
		p, e := f.r.EgressProfiles().Publish(t.Context(), egressprofile.PublishCommand{ID: egressprofile.ID(id), DisplayName: id, Policy: policy})
		if e != nil {
			t.Fatal(e)
		}
		return egressprofile.ProfileRef{ID: p.ID, Revision: p.Revision}
	}
	f.profileA = publish("profile.native-a", f.network.a)
	f.profileB = publish("profile.native-b", f.network.b)
	var authored egressprofile.Choice
	switch cfg.choice {
	case "flow_proxy":
		profile, err := f.r.EgressProfiles().GetRevision(t.Context(), f.profileA.ID, f.profileA.Revision)
		if err != nil {
			t.Fatal(err)
		}
		if c, err := egressprofile.Explicit(profile); err == nil {
			authored = c
		} else {
			t.Fatal(err)
		}
		f.setDefault(t, &f.profileB)
		f.mu.Lock()
		f.current = runtimeNativeExpected{ProfileID: f.profileA.ID.String(), ProfileRevision: uint64(f.profileA.Revision), Source: egressprofile.SelectionFlow, DefaultRevision: 0}
		f.mu.Unlock()
	case "flow_direct":
		authored, _ = egressprofile.Explicit(egressprofile.Direct())
		f.setDefault(t, &f.profileB)
		f.mu.Lock()
		f.current = runtimeNativeExpected{ProfileID: egressprofile.DirectID.String(), ProfileRevision: 1, Source: egressprofile.SelectionFlow, DefaultRevision: 0}
		f.mu.Unlock()
	case "builtin":
		authored = egressprofile.Inherit()
		f.mu.Lock()
		f.current = runtimeNativeExpected{ProfileID: egressprofile.DirectID.String(), ProfileRevision: 1, Source: egressprofile.SelectionBuiltin, DefaultRevision: 1}
		f.mu.Unlock()
	default:
		f.setDefault(t, &f.profileA)
		authored = egressprofile.Inherit()
	}
	origin, _ := originidentity.ParseProviderOrigin("https://native.example")
	env := environment.Environment{ID: "native.egress", Name: "Native egress", State: environment.StateActive, Revision: 1, ContentRecording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, PolicySet: &environment.PolicySet{ToolMode: environment.ToolPolicyReview}, ClientToolAccess: &environment.ClientToolAccessPolicy{Enabled: true, Route: &environment.ClientToolRoute{ID: "native.egress.route", Revision: 1, TargetOrigin: origin, Path: environment.ClientToolMCPPath, CredentialOwner: environment.ClientToolCredentialOriginal, Contract: environment.ClientToolContractCodexLegacy, EgressProfile: authored}}}
	if cfg.fullRecording {
		env.ContentRecording = environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1}
	}
	draft, e := f.r.Environments().SaveDraft(t.Context(), environment.DraftCommand{Candidate: env})
	if e != nil {
		t.Fatal(e)
	}
	preview, e := f.r.Environments().Preview(t.Context(), env.ID, draft.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.Environments().Publish(t.Context(), preview); e != nil {
		t.Fatal(e)
	}
	grant, e := f.r.manualCaptures.Create(t.Context(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Synthetic native", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeTemporary, ExpiresIn: time.Hour})
	if e != nil {
		t.Fatal(e)
	}
	authorizer, e := captureadmission.NewAuthorizer(f.r.captureRuns, f.r.manualCaptures)
	if e != nil {
		t.Fatal(e)
	}
	credential, e := captureadmission.NewProxyCredential(grant.Credential.Value())
	if e != nil {
		t.Fatal(e)
	}
	f.admission, e = authorizer.Authorize(t.Context(), credential)
	if e != nil {
		t.Fatal(e)
	}
	f.capture, _ = f.admission.CaptureReference()
	if _, e = f.r.assignments.Create(t.Context(), captureassignment.CreateCommand{Capture: f.capture, EnvironmentID: env.ID, Source: captureassignment.SourceManualCreate}); e != nil {
		t.Fatal(e)
	}
	f.connection, e = f.r.assignments.RegisterProviderConnection(t.Context(), f.capture, "native.egress.connection", origin)
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *runtimeNativeEgressFixture) holdEntry(entry runtimeNativeEntry) bool {
	if f.edge == "tool_proxy_down" && strings.Contains(entry.body, `"method":"tools/call"`) {
		return true
	}
	if f.edge == "reply_proxy_down" && strings.Contains(entry.body, `"id":"native-parent-ping"`) {
		return true
	}
	return f.edge == "recovery_proxy_down" && entry.method == "GET" && entry.cursor == "native-progress"
}
func (f *runtimeNativeEgressFixture) releaseEntry(t *testing.T, id nativetool.ID) {
	t.Helper()
	f.mu.Lock()
	gate := f.entryGates[id]
	err := f.entryErrors[id]
	f.mu.Unlock()
	if gate == nil {
		t.Fatalf("transport entry gate missing for %s", id)
	}
	gate <- err
}
func (f *runtimeNativeEgressFixture) waitAcceptedBody(t *testing.T, want string) {
	t.Helper()
	runtimeNativeObserve(t, "accepted body read", func(context.Context) bool {
		select {
		case <-f.acceptedRead:
			return true
		default:
			return false
		}
	}, func() any {
		f.mu.Lock()
		defer f.mu.Unlock()
		return fmt.Sprintf("reads=%d body=%q", f.acceptedReads.Load(), f.acceptedBody)
	})
	f.mu.Lock()
	body := f.acceptedBody
	f.mu.Unlock()
	if body != want || f.acceptedReads.Load() != 1 {
		t.Fatalf("accepted body read=%d body=%q want=%q", f.acceptedReads.Load(), body, want)
	}
}
func (f *runtimeNativeEgressFixture) releaseAccepted() {
	f.acceptedReleaseOnce.Do(func() { close(f.acceptedRelease) })
}
func (f *runtimeNativeEgressFixture) handle(w http.ResponseWriter, r *http.Request) {
	if (r.Method != "POST" && r.Method != "GET") || r.Host != "native.example" || r.URL.Path != environment.ClientToolMCPPath || r.URL.RawQuery != "" {
		f.network.unexpected.Add(1)
		w.WriteHeader(403)
		return
	}
	if r.Header.Get("Authorization") != "Bearer synthetic-original-login" || r.Header.Get("Cookie") != "synthetic-cookie=original" || r.Header.Get("Proxy-Authorization") != "" {
		f.badCredentials.Add(1)
		w.WriteHeader(403)
		return
	}
	body, e := io.ReadAll(r.Body)
	if e != nil {
		return
	}
	f.mu.Lock()
	_, ok := f.requests[runtimeNativeRequestKey(r.Method, r.Header.Get("Last-Event-Id"), string(body))]
	if ok {
		f.endpointRequests = append(f.endpointRequests, runtimeNativeRequest{r.Method, r.Header.Get("Last-Event-Id"), string(body)})
	}
	f.mu.Unlock()
	if !ok {
		f.network.unexpected.Add(1)
		w.WriteHeader(400)
		return
	}
	f.httpCount.Add(1)
	if f.edge == "tool_accepted_then_dropped" && r.Method == "POST" && strings.Contains(string(body), `"id":44`) {
		f.mu.Lock()
		f.acceptedBody = string(body)
		f.mu.Unlock()
		f.acceptedReads.Add(1)
		select {
		case f.acceptedRead <- struct{}{}:
		default:
		}
		select {
		case <-f.acceptedRelease:
		case <-r.Context().Done():
			return
		}
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
	}
	if r.Method == "POST" && strings.Contains(string(body), `"id":"native-parent-ping"`) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if r.Method == "GET" {
		w.Header().Set("Content-Type", "text/event-stream")
		if r.Header.Get("Last-Event-Id") == "native-progress" {
			io.WriteString(w, "id: native-terminal\ndata: {\"jsonrpc\":\"2.0\",\"id\":44,\"result\":{\"content\":[]}}\n\n")
		} else {
			io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"native-parent-ping\",\"method\":\"ping\"}\n\n")
		}
		return
	}
	var rpc struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(body, &rpc) != nil {
		w.WriteHeader(400)
		return
	}
	if rpc.Method != "initialize" && (r.Header.Get("Mcp-Session-Id") != "synthetic-session" || r.Header.Get("Mcp-Protocol-Version") != "2025-06-18") {
		f.network.unexpected.Add(1)
		w.WriteHeader(400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	var result string
	switch rpc.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "synthetic-session")
		result = `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"synthetic","version":"1"}}`
	case "notifications/initialized":
		w.WriteHeader(202)
		return
	case "tools/list":
		result = `{"tools":[{"name":"read_file","inputSchema":{"type":"object"}}]}`
	case "tools/call":
		if f.mode == "matrix" && strings.Contains(string(body), `"id":44`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "id: native-progress\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"native-token\",\"progress\":1}}\n\n")
			return
		}
		result = `{"content":[{"type":"text","text":"synthetic result"}]}`
	default:
		f.network.unexpected.Add(1)
		w.WriteHeader(400)
		return
	}
	io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(rpc.ID)+`,"result":`+result+`}`)
}
func (f *runtimeNativeEgressFixture) begin(t *testing.T, method, body, cursor string) runtimeNativeCall {
	t.Helper()
	f.mu.Lock()
	expected := f.current
	f.mu.Unlock()
	return f.beginExpected(t, method, body, cursor, expected)
}
func (f *runtimeNativeEgressFixture) beginExpected(t *testing.T, method, body, cursor string, expected runtimeNativeExpected) runtimeNativeCall {
	t.Helper()
	return f.beginPhase(t, method, body, cursor, expected, nil)
}
func (f *runtimeNativeEgressFixture) beginPhase(t *testing.T, method, body, cursor string, expected runtimeNativeExpected, entryCounts *runtimeNativeCounts) runtimeNativeCall {
	t.Helper()
	return f.beginPhaseContext(t, context.Background(), method, body, cursor, expected, entryCounts)
}
func (f *runtimeNativeEgressFixture) beginPhaseContext(t *testing.T, parent context.Context, method, body, cursor string, expected runtimeNativeExpected, entryCounts *runtimeNativeCounts) runtimeNativeCall {
	t.Helper()
	facts := environment.RequestFacts{Target: protocolspec.RequestTarget{Method: method, Path: environment.ClientToolMCPPath, Transport: protocolspec.ClientOperationTransportHTTP}, DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1}
	lease, e := f.r.assignments.BeginRequest(t.Context(), f.capture, "native.egress.connection", facts)
	if e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	f.next++
	id, _ := nativetool.NewID(fmt.Sprintf("%032x", f.next))
	f.expected[id] = expected
	if entryCounts != nil {
		f.entryCounts[id] = *entryCounts
	}
	f.bodies[body] = id
	f.requests[runtimeNativeRequestKey(method, cursor, body)] = id
	f.mu.Unlock()
	request := httptest.NewRequest(method, "https://native.example"+environment.ClientToolMCPPath, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer synthetic-original-login")
	request.Header.Set("Cookie", "synthetic-cookie=original")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if !strings.Contains(body, `"method":"initialize"`) {
		request.Header.Set("Mcp-Session-Id", "synthetic-session")
		request.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	}
	if cursor != "" {
		request.Header.Set("Last-Event-Id", cursor)
	}
	captured, e := nativemcp.NewCapturedRequest(lease, f.admission, request, id)
	if e != nil {
		lease.Release()
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan nativemcp.Terminal, 1)
	response := httptest.NewRecorder()
	call := runtimeNativeCall{ID: id, Captured: captured, Done: done, Cancel: cancel, Response: response}
	f.mu.Lock()
	f.flights[id] = &runtimeNativeFlight{call: call}
	f.mu.Unlock()
	go func() { done <- f.r.nativeTools.Serve(ctx, captured, response) }()
	return call
}
func (f *runtimeNativeEgressFixture) handshake(t *testing.T) {
	t.Helper()
	for _, body := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`} {
		call := f.begin(t, "POST", body, "")
		terminal := f.join(t, call)
		if terminal.Outcome != nativemcp.OutcomeCompleted {
			t.Fatalf("native handshake %+v counters=%+v", terminal, f.counts())
		}
	}
	if c := f.counts(); c.HTTP != 3 || (c.AConnects != 3 && c.DirectConnections != 3) || c.TLS != 3 {
		t.Fatalf("native baseline %+v", c)
	}
}
func runtimeNativeObserve(t *testing.T, stage string, check func(context.Context) bool, diagnostic func() any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if check(ctx) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("native stage %s safeguard: %+v", stage, diagnostic())
		case <-ticker.C:
		}
	}
}
func (f *runtimeNativeEgressFixture) allow(t *testing.T, id nativetool.ID) {
	t.Helper()
	var approval nativetool.Approval
	runtimeNativeObserve(t, "Review pending", func(ctx context.Context) bool {
		op, e := f.r.NativeToolRepository().Get(ctx, id)
		if e != nil || op.ApprovalID == nil {
			return false
		}
		approval, e = f.r.NativeToolRepository().GetApproval(ctx, *op.ApprovalID)
		return e == nil && approval.State == "pending"
	}, func() any {
		return fmt.Sprintf("approval=%+v gate=%+v counters=%+v", approval, f.gate.Snapshot(), f.counts())
	})
	if _, e := f.r.NativeToolRepository().DecideApproval(t.Context(), nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "native-fixture-allow-" + id.String(), ObservedAt: time.Now().UTC()}); e != nil {
		t.Fatal(e)
	}
}
func (f *runtimeNativeEgressFixture) waitQueued(t *testing.T) offlinehold.ProbeTarget {
	t.Helper()
	runtimeNativeObserve(t, "Hold queued", func(context.Context) bool {
		return f.gate.Snapshot().QueuedRequests == 1 && len(f.gate.PendingProbeTargets()) == 1
	}, func() any { return fmt.Sprintf("gate=%+v counts=%+v", f.gate.Snapshot(), f.counts()) })
	return f.gate.PendingProbeTargets()[0]
}
func (f *runtimeNativeEgressFixture) waitStarted(t *testing.T, id nativetool.ID) {
	t.Helper()
	runtimeNativeObserve(t, "native transport entry", func(context.Context) bool {
		f.mu.Lock()
		_, ok := f.started[id]
		if ok && (f.edge == "tool_proxy_down" || f.edge == "reply_proxy_down" || f.edge == "recovery_proxy_down") {
			_, ok = f.entryGates[id]
		}
		f.mu.Unlock()
		return ok
	}, func() any { return fmt.Sprintf("started=%s counts=%+v", id, f.counts()) })
}
func (f *runtimeNativeEgressFixture) setDefault(t *testing.T, profile *egressprofile.ProfileRef) uint64 {
	t.Helper()
	old, e := f.r.EgressDefaults().GetDefault(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	setting, e := f.r.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: old.Revision, Profile: profile})
	if e != nil {
		t.Fatal(e)
	}
	if f.choice == "flow_proxy" || f.choice == "flow_direct" {
		return setting.Revision
	}
	if profile == nil {
		f.mu.Lock()
		f.current = runtimeNativeExpected{ProfileID: "profile.direct", ProfileRevision: 1, Source: egressprofile.SelectionBuiltin, DefaultRevision: setting.Revision}
		f.mu.Unlock()
	} else {
		f.mu.Lock()
		f.current = runtimeNativeExpected{ProfileID: profile.ID.String(), ProfileRevision: uint64(profile.Revision), Source: egressprofile.SelectionGlobal, DefaultRevision: setting.Revision}
		f.mu.Unlock()
	}
	return setting.Revision
}
func (f *runtimeNativeEgressFixture) join(t *testing.T, call runtimeNativeCall) nativemcp.Terminal {
	t.Helper()
	select {
	case terminal := <-call.Done:
		call.Cancel()
		f.mu.Lock()
		f.flights[call.ID].joined = true
		f.mu.Unlock()
		return terminal
	case <-time.After(10 * time.Second):
		t.Fatalf("native Serve join %s safeguard gate=%+v counts=%+v", call.ID, f.gate.Snapshot(), f.counts())
		return nativemcp.Terminal{}
	}
}
func (f *runtimeNativeEgressFixture) audit(id nativetool.ID) (egressaudit.Record, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// This send-entry proof reads the required physical delivery row. Optional
	// audit pagination/settlement is never authority for transport or once use.
	var attemptID, connectionID, target, policyID, ruleID, proxyID, payload, selection, outcome, errorClass string
	var policyRevision, proxyRevision, defaultRevision uint64
	var started, bytesOut, bytesIn int64
	var completed sql.NullInt64
	var known bool
	err := f.db.QueryRowContext(ctx, `SELECT attempt_id,connection_id,payload_class,target_origin,policy_id,policy_revision,rule_id,proxy_id,proxy_revision,selection_source,default_revision,started_at_unix_ms,completed_at_unix_ms,outcome,error_class,bytes_out,bytes_in,byte_counts_known FROM native_tool_deliveries WHERE operation_id=?`, id.String()).Scan(&attemptID, &connectionID, &payload, &target, &policyID, &policyRevision, &ruleID, &proxyID, &proxyRevision, &selection, &defaultRevision, &started, &completed, &outcome, &errorClass, &bytesOut, &bytesIn, &known)
	if err != nil {
		return egressaudit.Record{}, err
	}
	attempt, err := egressaudit.New(egressaudit.NewInput{ID: attemptID, ConnectionID: connectionID, Purpose: egressaudit.PurposeClientToolService, PayloadClass: egressaudit.PayloadClass(payload), Parent: egressaudit.ParentRef{Kind: egressaudit.ParentClientOperation, ID: id.String()}, Caller: egressaudit.CallerCore, TargetOrigin: target, Decision: egressaudit.DecisionRef{PolicyID: policyID, PolicyRevision: policyRevision, Authority: egressaudit.AuthorityEnvironment, RuleID: ruleID, ProxyID: proxyID, ProxyRevision: proxyRevision, SelectionSource: egressprofile.SelectionSource(selection), DefaultRevision: defaultRevision}, StartedAt: time.UnixMilli(started).UTC()})
	if err != nil {
		return egressaudit.Record{}, err
	}
	if completed.Valid {
		attempt, err = attempt.Finish(egressaudit.TerminalInput{Outcome: egressaudit.Outcome(outcome), ErrorClass: errorClass, BytesOut: bytesOut, BytesIn: bytesIn, UnobservedByteCounts: !known, CompletedAt: time.UnixMilli(completed.Int64).UTC()})
	}
	return egressaudit.Record{Attempt: attempt}, err
}
func (f *runtimeNativeEgressFixture) checkAudit(record egressaudit.Record, id nativetool.ID, want runtimeNativeExpected, outcome, errorClass string) error {
	a := record.Attempt
	d := a.Decision()
	if a.Purpose() != egressaudit.PurposeClientToolService || a.Parent().Kind != egressaudit.ParentClientOperation || a.Parent().ID != id.String() || a.Caller() != egressaudit.CallerCore || a.TargetOrigin() != "https://native.example" || d.PolicyID != "native.egress" || d.PolicyRevision != 1 || d.Authority != egressaudit.AuthorityEnvironment || d.RuleID != "native.egress.route" || d.ProxyID != want.ProfileID || d.ProxyRevision != want.ProfileRevision || d.SelectionSource != want.Source || d.DefaultRevision != want.DefaultRevision || d.AccountID != "" || d.AccountSettingsRevision != 0 {
		return fmt.Errorf("native audit identity mismatch id=%s want=%+v audit=%+v", id, want, d)
	}
	body := f.bodyFor(id)
	wantPayload := egressaudit.PayloadClass("")
	if f.methodFor(id) == "GET" {
		wantPayload = egressaudit.PayloadNone
	} else if strings.Contains(body, `"method":"tools/call"`) || strings.Contains(body, `"id":"native-parent-ping"`) {
		wantPayload = egressaudit.PayloadClientData
	}
	if wantPayload != "" && a.PayloadClass() != wantPayload {
		return errors.New("native tool audit payload mismatch")
	}
	if outcome == "" {
		if a.Terminal() {
			return errors.New("native entry audit already terminal")
		}
	} else if !a.Terminal() || string(a.Outcome()) != outcome || a.ErrorClass() != errorClass {
		return fmt.Errorf("native audit terminal mismatch: %s/%s", a.Outcome(), a.ErrorClass())
	}
	if a.Terminal() {
		if outcome == "completed" && (!a.ByteCountsKnown() || a.BytesOut() != int64(len(body)) || (a.BytesIn() <= 0 && a.BytesOut() <= 0)) {
			return fmt.Errorf("native completed byte counts unknown/empty in=%d out=%d known=%t", a.BytesIn(), a.BytesOut(), a.ByteCountsKnown())
		}
		if errorClass == "execution_uncertain" && (a.ByteCountsKnown() || a.BytesIn() != 0 || a.BytesOut() != 0) {
			return fmt.Errorf("native uncertain byte counts unexpectedly known in=%d out=%d", a.BytesIn(), a.BytesOut())
		}
		if errorClass == "transport_failed" {
			// Broken exchanges cannot observe request writes through this
			// transport API, so both directions stay unknown even after SSE.
			if a.ByteCountsKnown() || a.BytesIn() != 0 || a.BytesOut() != 0 {
				return errors.New("native broken exchange invented observed bytes")
			}
		}
	}
	return nil
}
func (f *runtimeNativeEgressFixture) methodFor(id nativetool.ID) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, owner := range f.requests {
		if owner == id {
			return strings.SplitN(key, "\x00", 2)[0]
		}
	}
	return ""
}
func (f *runtimeNativeEgressFixture) bodyFor(id nativetool.ID) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for body, owner := range f.bodies {
		if owner == id {
			return body
		}
	}
	return ""
}
func (f *runtimeNativeEgressFixture) assertAudit(t *testing.T, id nativetool.ID, want runtimeNativeExpected, outcome, errorClass string) {
	t.Helper()
	record, e := f.audit(id)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.checkAudit(record, id, want, outcome, errorClass); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	initial, ok := f.started[id]
	f.mu.Unlock()
	if !ok {
		t.Fatal("native audit lacks actual transport-entry snapshot")
	}
	a, b := initial.Attempt, record.Attempt
	if initial.Sequence != record.Sequence || a.ID() != b.ID() || !reflect.DeepEqual(a.Parent(), b.Parent()) || !reflect.DeepEqual(a.Decision(), b.Decision()) || a.TargetOrigin() != b.TargetOrigin() || !a.StartedAt().Equal(b.StartedAt()) {
		t.Fatal("native initial audit identity mutated at terminal")
	}
	t.Logf("native initial/terminal immutable id=%s attempt=%s sequence=%d purpose=%s parent=%+v decision=%+v outcome=%s errorClass=%s", id, b.ID(), record.Sequence, b.Purpose(), b.Parent(), b.Decision(), b.Outcome(), b.ErrorClass())
}
func (f *runtimeNativeEgressFixture) assertNoStart(t *testing.T, id nativetool.ID) {
	t.Helper()
	var once, audits, consumed int
	for _, q := range []struct {
		query string
		value *int
	}{{`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, &once}, {`SELECT COUNT(*) FROM native_tool_deliveries WHERE operation_id=?`, &audits}, {`SELECT COUNT(*) FROM native_tool_approvals WHERE operation_id=? AND consumed_at_unix_ms IS NOT NULL`, &consumed}} {
		if e := f.db.QueryRow(q.query, id.String()).Scan(q.value); e != nil {
			t.Fatal(e)
		}
	}
	if once != 0 || audits != 0 || consumed != 0 {
		t.Fatalf("native pre-Start once=%d audit=%d consumed=%d", once, audits, consumed)
	}
	t.Logf("native no-Start id=%s once=%d audit=%d consumedAllow=%d counts=%+v gate=%+v", id, once, audits, consumed, f.counts(), f.gate.Snapshot())
}
func (f *runtimeNativeEgressFixture) assertReplayAndFreshStart(t *testing.T, call runtimeNativeCall) {
	t.Helper()
	beforeCounts := f.counts()
	before := f.failureState(t, call.ID)
	f.mu.Lock()
	beforeEntries := len(f.observedEntries[call.ID])
	beforeRequests := append([]runtimeNativeRequest(nil), f.endpointRequests...)
	f.mu.Unlock()
	assertUnchanged := func(stage string) {
		t.Helper()
		after := f.failureState(t, call.ID)
		f.mu.Lock()
		entries := len(f.observedEntries[call.ID])
		requests := append([]runtimeNativeRequest(nil), f.endpointRequests...)
		f.mu.Unlock()
		if !reflect.DeepEqual(before, after) || f.counts() != beforeCounts || entries != beforeEntries || !reflect.DeepEqual(beforeRequests, requests) {
			t.Fatalf("%s mutated operation/Allow/once/audit or transport before=%+v after=%+v entries=%d/%d counts=%+v/%+v", stage, before, after, beforeEntries, entries, beforeCounts, f.counts())
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	replay := f.r.nativeTools.Serve(ctx, call.Captured, httptest.NewRecorder())
	if replay.OperationID != call.ID || replay.Outcome != nativemcp.OutcomeRejected || replay.Reason != nativemcp.ReasonProtocolInvalid {
		t.Fatalf("native captured-request replay rejection=%+v", replay)
	}
	assertUnchanged("captured-request replay")
	scope, err := f.r.NativeToolRepository().GetScope(t.Context(), f.capture)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := f.r.NativeToolRepository().Start(t.Context(), call.ID, "fresh-attempt-"+call.ID.String(), scope.Revision, time.Now().UTC())
	if !errors.Is(err, nativetool.ErrReplay) || fresh != (nativetool.StartReceipt{}) {
		t.Fatalf("fresh Store.Start unexpectedly accepted receipt=%+v err=%v", fresh, err)
	}
	assertUnchanged("fresh Store.Start")
}

type runtimeNativeFailureState struct {
	Operation              nativetool.Operation
	Approval               nativetool.Approval
	Audit                  egressaudit.Record
	Once, Consumed, Audits int
	AttemptID              string
}

func (f *runtimeNativeEgressFixture) failureState(t *testing.T, id nativetool.ID) runtimeNativeFailureState {
	t.Helper()
	var state runtimeNativeFailureState
	var err error
	state.Operation, err = f.r.NativeToolRepository().Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	wantState, wantReason := "uncertain", "execution_uncertain"
	if f.methodFor(id) == "GET" {
		wantState, wantReason = "failed", "transport_failed"
	}
	if state.Operation.State != wantState || state.Operation.Reason != wantReason || state.Operation.CompletedAt == nil || state.Operation.BytesIn != nil || state.Operation.BytesOut != nil {
		t.Fatalf("native failure logical/byte state=%+v", state.Operation)
	}
	if state.Operation.ApprovalID != nil {
		state.Approval, err = f.r.NativeToolRepository().GetApproval(t.Context(), *state.Operation.ApprovalID)
		if err != nil {
			t.Fatal(err)
		}
		if state.Approval.State != "allowed" || state.Approval.ConsumedAt == nil {
			t.Fatalf("failure Allow=%+v", state.Approval)
		}
	}
	state.Audit, err = f.audit(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(attempt_id),'') FROM native_tool_once WHERE operation_id=?`, id.String()).Scan(&state.Once, &state.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_approvals WHERE operation_id=? AND consumed_at_unix_ms IS NOT NULL`, id.String()).Scan(&state.Consumed); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_deliveries WHERE operation_id=?`, id.String()).Scan(&state.Audits); err != nil {
		t.Fatal(err)
	}
	wantConsumed := 1
	if f.methodFor(id) == "GET" {
		wantConsumed = 0
	}
	if state.Once != 1 || state.Audits != 1 || state.Consumed != wantConsumed || state.AttemptID != state.Audit.Attempt.ID() {
		t.Fatalf("failure once/Allow/audit=%+v", state)
	}
	return state
}
func (f *runtimeNativeEgressFixture) counts() runtimeNativeCounts {
	return runtimeNativeCounts{f.network.directAttempts.Load(), f.network.directConnections.Load(), f.network.aAttempts.Load(), f.network.bAttempts.Load(), f.proxyA.connects.Load(), f.proxyB.connects.Load(), f.tlsCount.Load(), f.httpCount.Load(), f.network.unexpected.Load(), f.badCredentials.Load()}
}
func (f *runtimeNativeEgressFixture) assertDrained(t *testing.T) {
	t.Helper()
	s := f.gate.Snapshot()
	if s.ActiveActions != 0 || s.EnteringActions != 0 || s.ActiveEgress != 0 || s.QueuedRequests != 0 || s.HeldBytes != 0 || len(f.gate.PendingProbeTargets()) != 0 {
		t.Fatalf("native Gate not drained %+v", s)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, flight := range f.flights {
		if !flight.joined {
			t.Fatalf("native Serve not joined %s", id)
		}
	}
	t.Logf("native joined/drained gate=%+v counts=%+v", s, f.counts())
}
func (f *runtimeNativeEgressFixture) cleanup(t *testing.T) {
	t.Helper()
	if f.cleaned {
		return
	}
	f.cleaned = true
	f.releaseAccepted()
	if f.r != nil {
		for _, path := range []string{filepath.Dir(f.databasePath), f.databasePath} {
			if _, e := os.Stat(path); e != nil {
				t.Errorf("native Runtime data must outlive cleanup path=%s: %v", path, e)
			}
		}
	}
	f.mu.Lock()
	var calls []runtimeNativeCall
	for _, flight := range f.flights {
		flight.call.Cancel()
		if !flight.joined {
			calls = append(calls, flight.call)
		}
	}
	f.mu.Unlock()
	for _, call := range calls {
		f.join(t, call)
	}
	if f.gate != nil {
		f.assertDrained(t)
	}
	if f.observerCancel != nil {
		f.observerCancel()
		select {
		case <-f.observerDone:
		case <-time.After(10 * time.Second):
			t.Error("native entry observer drain exceeded safeguard")
		}
	}
	if f.proxyA != nil {
		f.proxyA.closeAndJoin(t)
	}
	if f.proxyB != nil {
		f.proxyB.closeAndJoin(t)
	}
	if f.server != nil {
		f.server.CloseClientConnections()
		f.server.Close()
	}
	if f.connection != nil {
		f.connection.Close()
	}
	if f.r != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := f.r.Shutdown(ctx); e != nil {
			t.Errorf("shutdown ProductRuntime: %v", e)
		}
	}
	if f.db != nil {
		f.db.Close()
	}
}
