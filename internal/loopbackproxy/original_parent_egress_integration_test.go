package loopbackproxy_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

var opaqueParentQueries = [2]string{"platform=synthetic%2Bfixture-A", "platform=synthetic%2Bfixture-B"}

type observedOriginalID struct {
	id  string
	err error
}

// The real generator owns every ID and error. This observer only records its
// output; it never assigns IDs or reconstructs authority from an audit.
type observedOriginalIDs struct {
	real loopbackproxy.ExchangeIDSource
	mu   sync.Mutex
	ids  []observedOriginalID
}

func (o *observedOriginalIDs) NewExchangeID(ctx context.Context) (string, error) {
	id, err := o.real.NewExchangeID(ctx)
	o.mu.Lock()
	o.ids = append(o.ids, observedOriginalID{id, err})
	o.mu.Unlock()
	return id, err
}

func (o *observedOriginalIDs) snapshot() []observedOriginalID {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]observedOriginalID(nil), o.ids...)
}

// Observe the Handler's typed Request, then delegate it unchanged into the
// actual originaltransport.Client. There is no successful recorder substitute.
type observedOriginalClient struct {
	real     *originaltransport.Client
	mu       sync.Mutex
	requests []originaltransport.Request
}

func (o *observedOriginalClient) Do(ctx context.Context, request originaltransport.Request) (*http.Response, error) {
	o.mu.Lock()
	o.requests = append(o.requests, request)
	o.mu.Unlock()
	return o.real.Do(ctx, request)
}

func (o *observedOriginalClient) snapshot() []originaltransport.Request {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]originaltransport.Request(nil), o.requests...)
}

type opaqueParentNetwork struct {
	address            string
	dials, dns, denied atomic.Int64
}

func (n *opaqueParentNetwork) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	if network != "ip" || host != "chatgpt.com" {
		n.denied.Add(1)
		return nil, errors.New("closed parent fixture rejected DNS")
	}
	n.dns.Add(1)
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

func (n *opaqueParentNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	// Direct/System forwards this exact canonical authority to BaseDialer.
	// Map it to the sole private listener without ambient DNS or other dials.
	if network != "tcp" || address != "chatgpt.com:443" {
		n.denied.Add(1)
		return nil, errors.New("closed parent fixture rejected dial")
	}
	n.dials.Add(1)
	return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, n.address)
}

type opaqueParentWire struct {
	builder *egressnetwork.Builder
	roots   *x509.CertPool
	audit   egressaudit.Reader
	client  *observedOriginalClient
	mu      sync.Mutex
	starts  map[string]egressaudit.Attempt
}

func (w *opaqueParentWire) RoundTrip(request *http.Request, policy egressnetwork.Policy) (*http.Response, error) {
	allowedQuery := request.URL.RawQuery == opaqueParentQueries[0] || request.URL.RawQuery == opaqueParentQueries[1]
	if request.Method != http.MethodGet || request.URL.Scheme != "https" || request.URL.Host != "chatgpt.com" || request.Host != "chatgpt.com" || request.URL.Path != "/backend-api/plugins/featured" || !allowedQuery || request.ContentLength != 0 || policy != egressnetwork.DefaultPolicy() {
		return nil, errors.New("closed parent fixture rejected original wire identity/policy")
	}
	observed := w.client.snapshot()
	if len(observed) == 0 {
		return nil, errors.New("original wire has no live typed request")
	}
	actual := observed[len(observed)-1]
	page, err := w.audit.List(request.Context(), egressaudit.PageRequest{Limit: 10})
	if err != nil {
		return nil, err
	}
	var start egressaudit.Attempt
	for _, record := range page.Items {
		if record.Attempt.ID() == actual.RequestID() && !record.Attempt.Terminal() {
			start = record.Attempt
		}
	}
	if start.ID() == "" {
		return nil, errors.New("original initial durable audit missing before wire I/O")
	}
	w.mu.Lock()
	if _, duplicate := w.starts[actual.RequestID()]; duplicate {
		w.mu.Unlock()
		return nil, errors.New("original wire repeated an outbound request")
	}
	w.starts[actual.RequestID()] = start
	w.mu.Unlock()
	dialer, err := w.builder.Dialer(policy)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{DialContext: dialer.DialContext, DisableKeepAlives: true, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: time.Second, TLSClientConfig: &tls.Config{RootCAs: w.roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	return transport.RoundTrip(request)
}

type opaqueParentFlight struct {
	done   chan error
	cancel context.CancelFunc
	joined bool
}

// Catches a wrong-but-distinct audit parent on the correct connection: each
// parent must equal both the genuine Handler-generated ID and the live Request.
// It supplements the frozen Runtime matrix, not a fabricated durable parent row.
func TestOriginalOpaqueForwardParentMatchesActualRequest(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(193), DNSNames: []string{"chatgpt.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var httpCalls, bad atomic.Int64
	producerDone := make(chan struct{}, 4)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ordinal := httpCalls.Add(1)
		defer func() { producerDone <- struct{}{} }()
		body, readErr := io.ReadAll(r.Body)
		if ordinal > 2 || readErr != nil || r.Method != http.MethodGet || r.Host != "chatgpt.com" || r.URL.Path != "/backend-api/plugins/featured" || r.URL.RawQuery != opaqueParentQueries[ordinal-1] || len(body) != 0 || r.Header.Get("Authorization") != "Bearer original-A" || r.Header.Get("ChatGPT-Account-Id") != "workspace-A" || r.Header.Get("Cookie") != "original-A-cookie" || r.Header.Get("User-Agent") != "original-parent-fixture/1" || r.Header.Get("Proxy-Authorization") != "" {
			bad.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Original-Fixture", "parent")
		_, _ = w.Write([]byte{0, 1, 2, 'r', 'a', 'w', 255})
	}))
	upstream.Config.ReadTimeout, upstream.Config.WriteTimeout = time.Second, time.Second
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	upstream.StartTLS()
	t.Cleanup(upstream.Close)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	network := &opaqueParentNetwork{address: upstream.Listener.Addr().String()}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Start(t.Context(), offlinehold.RuntimeBinding{InstanceID: "opaque-parent-fixture"}); err != nil {
		t.Fatal(err)
	}
	observed := &observedOriginalClient{}
	ids := &observedOriginalIDs{}
	wire := &opaqueParentWire{builder: builder, roots: roots, client: observed, starts: map[string]egressaudit.Attempt{}}
	composition := &proxyFixtureComposition{compiler: originalLivenessCompiler(t), configureProxy: func(options *loopbackproxy.Options) {
		var ok bool
		wire.audit, ok = options.EgressAudit.(egressaudit.Reader)
		if !ok {
			t.Fatal("real Store audit is not readable")
		}
		observed.real, err = originaltransport.New(originaltransport.Options{Coordinator: gate, Transport: wire, Audit: options.EgressAudit})
		if err != nil {
			t.Fatal(err)
		}
		ids.real = options.ExchangeIDs
		options.ExchangeIDs, options.Original = ids, observed
	}}
	f := newProxyFixtureConfigured(t, protocolspec.DialectOpenAIResponses, nil, allowEverythingTestPolicy(t), nil, "", composition, "https://chatgpt.com")
	t.Cleanup(func() { f.Close(t) })
	proxyURL, err := url.Parse("http://" + f.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.User = url.UserPassword("capture", f.grant.ProxyCapability.Value())
	clientRoots := x509.NewCertPool()
	if !clientRoots.AppendCertsFromPEM(f.authority.Certificate().CertificatePEM()) {
		t.Fatal("private downstream root invalid")
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSHandshakeTimeout: time.Second, DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: clientRoots, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != f.listener.Addr().String() {
			return nil, errors.New("closed parent fixture rejected downstream dial")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
	}}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	var flights []*opaqueParentFlight
	var producers int64
	t.Cleanup(func() {
		for _, flight := range flights {
			flight.cancel()
		}
		transport.CloseIdleConnections()
		f.handler.BeginShutdown()
		gate.BeginShutdown()
		for _, flight := range flights {
			if !flight.joined {
				select {
				case <-flight.done:
					flight.joined = true
				case <-time.After(4 * time.Second):
					t.Error("opaque parent cleanup caller join deadline")
				}
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
	producerJoin:
		for producers < httpCalls.Load() {
			select {
			case <-producerDone:
				producers++
			case <-ctx.Done():
				t.Error("opaque parent producer join deadline")
				break producerJoin
			}
		}
		for _, drain := range []func(context.Context) error{observed.real.Shutdown, f.handler.Drain, gate.Drain} {
			if err := drain(ctx); err != nil {
				t.Error("opaque parent cleanup drain", err)
			}
		}
		upstream.CloseClientConnections()
		s := gate.Snapshot()
		if s.ActiveActions != 0 || s.EnteringActions != 0 || s.ActiveEgress != 0 || s.QueuedRequests != 0 || s.HeldBytes != 0 {
			t.Error("opaque parent retained admission", s)
		}
		t.Logf("opaque parent cleanup callers=%d producers=%d HTTP=%d DNS=%d Direct=%d bad=%d denied=%d gate=%+v", len(flights), producers, httpCalls.Load(), network.dns.Load(), network.dials.Load(), bad.Load(), network.denied.Load(), s)
	})
	for _, query := range opaqueParentQueries {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		flight := &opaqueParentFlight{done: make(chan error, 1), cancel: cancel}
		flights = append(flights, flight)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/backend-api/plugins/featured?"+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header = http.Header{"Authorization": {"Bearer original-A"}, "Chatgpt-Account-Id": {"workspace-A"}, "Cookie": {"original-A-cookie"}, "User-Agent": {"original-parent-fixture/1"}, "Proxy-Authorization": {"Basic inner-must-not-leave"}}
		go func() {
			response, sendErr := client.Do(request)
			if response != nil {
				body, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if readErr != nil {
					sendErr = readErr
				} else if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/octet-stream" || response.Header.Get("X-Original-Fixture") != "parent" || !bytes.Equal(body, []byte{0, 1, 2, 'r', 'a', 'w', 255}) {
					sendErr = errors.New("original parent downstream byte/header fidelity failed")
				}
			}
			flight.done <- sendErr
		}()
		select {
		case err := <-flight.done:
			flight.joined = true
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(4 * time.Second):
			t.Fatal("opaque parent caller join deadline")
		}
		cancel()
	}
	transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := observed.real.Shutdown(ctx); err != nil {
		t.Fatal("real original client consumer drain", err)
	}
	f.handler.BeginShutdown()
	if err := f.handler.Drain(ctx); err != nil {
		t.Fatal("actual Handler drain", err)
	}
	if err := gate.Drain(ctx); err != nil {
		t.Fatal("actual gate drain", err)
	}
	requests, generated := observed.snapshot(), ids.snapshot()
	if len(requests) != 2 || len(generated) != 4 || httpCalls.Load() != 2 || network.dials.Load() != 2 || network.dns.Load() != 0 || bad.Load() != 0 || network.denied.Load() != 0 || len(f.exchanges.Requests()) != 0 || f.original.Request().RequestID() != "" {
		t.Fatal("opaque parent extra send/ID/semantic/recorder path", len(requests), len(generated), httpCalls.Load(), network.dials.Load(), network.dns.Load(), bad.Load(), network.denied.Load())
	}
	page, err := f.egress.List(ctx, egressaudit.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatal("opaque parent durable audit count", len(page.Items), err)
	}
	wire.mu.Lock()
	defer wire.mu.Unlock()
	if len(wire.starts) != 2 {
		t.Fatal("opaque parent initial audit count", len(wire.starts))
	}
	seenIDs := map[string]bool{}
	for i, request := range requests {
		parent, outbound := generated[2*i], generated[2*i+1]
		if parent.err != nil || outbound.err != nil || parent.id == "" || outbound.id == "" || parent.id == outbound.id || seenIDs[parent.id] || seenIDs[outbound.id] || request.ParentID() != parent.id || request.RequestID() != outbound.id {
			t.Fatal("opaque parent differs from genuine Handler ID sequence", generated, request.ParentID(), request.RequestID())
		}
		seenIDs[parent.id], seenIDs[outbound.id] = true, true
		headers := request.Headers()
		if request.ConnectionID() == "" || request.Kind() != offlinehold.EgressOpaque || request.PayloadClass() != protocolspec.OperationPayloadControl || request.Origin().String() != "https://chatgpt.com" || request.Method() != http.MethodGet || request.Path() != "/backend-api/plugins/featured" || request.RawQuery() != opaqueParentQueries[i] || len(request.Body()) != 0 || request.EgressPolicy() != egressnetwork.DefaultPolicy() || headers.Get("Authorization") != "Bearer original-A" || headers.Get("ChatGPT-Account-Id") != "workspace-A" || headers.Get("Cookie") != "original-A-cookie" || headers.Get("User-Agent") != "original-parent-fixture/1" || headers.Get("Proxy-Authorization") != "" {
			t.Fatal("live typed original Request lost exact target/credential/authority", request, headers)
		}
		start, ok := wire.starts[outbound.id]
		var terminal egressaudit.Attempt
		for _, record := range page.Items {
			if record.Attempt.ID() == outbound.id {
				terminal = record.Attempt
			}
		}
		wantParent := egressaudit.ParentRef{Kind: egressaudit.ParentOriginalRequest, ID: parent.id}
		wantDecision := egressaudit.DecisionRef{SelectionSource: egressprofile.SelectionFlow, PolicyID: "profile.direct", PolicyRevision: 1, Authority: egressaudit.AuthorityNetwork, RuleID: "flow.egress", ProxyID: "profile.direct", ProxyRevision: 1}
		if !ok || start.Terminal() || start.ID() != request.RequestID() || start.Parent() != wantParent || start.ConnectionID() != request.ConnectionID() || start.Decision() != wantDecision || start.Purpose() != egressaudit.PurposeOriginalOrigin || start.PayloadClass() != egressaudit.PayloadControl || start.TargetOrigin() != request.Origin().String() || !terminal.Terminal() || terminal.ID() != request.RequestID() || terminal.Parent() != start.Parent() || terminal.ConnectionID() != start.ConnectionID() || terminal.Decision() != start.Decision() || terminal.Purpose() != start.Purpose() || terminal.PayloadClass() != start.PayloadClass() || terminal.TargetOrigin() != start.TargetOrigin() || !terminal.StartedAt().Equal(start.StartedAt()) || terminal.Outcome() != egressaudit.OutcomeCompleted || terminal.BytesOut() != 0 || terminal.BytesIn() != 7 {
			t.Fatal("original initial/terminal audit differs from actual Handler parent/Request", start, terminal, request)
		}
		timeline, err := f.connections.Timeline(ctx, request.ConnectionID())
		bound := false
		for _, record := range timeline.Events {
			e := record.Event
			if e.Phase == connectionevent.PhaseConnected && e.ConnectionID == request.ConnectionID() && e.EnvironmentID == f.environment.ID && e.EnvironmentRevision == f.environment.Revision && e.ClientEndpointID == f.environment.ClientEndpoints[0].ID && e.ClientEndpointRevision == 1 && e.IngressID == "capture-run/"+f.grant.Run.ID && e.RequestedHost == "chatgpt.com" && e.Decryption == connectionevent.DecryptionMITM {
				bound = true
			}
		}
		if err != nil || !bound || f.environment.ClientEndpoints[0].ProtocolPlans[0].Destination.Kind != environment.DestinationKindOriginal {
			t.Fatal("actual typed Request connection has no captured Original authority", timeline, err)
		}
		t.Logf("opaque actual Handler originalID=%s outboundID=%s typedParent=%s connection=%s query=%s initial/terminal ParentOriginalRequest matched outcome=%s bytes=0/7", parent.id, outbound.id, request.ParentID(), request.ConnectionID(), request.RawQuery(), terminal.Outcome())
	}
}
