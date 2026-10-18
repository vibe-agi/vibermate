package productruntime_test

import (
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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
)

type metadataClosedNetwork struct {
	proxy              string
	dials, denied, dns atomic.Int64
	directTarget       string
	direct             atomic.Int64
}

func (n *metadataClosedNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network == "tcp" && n.directTarget != "" && (address == "models.dev:443" || address == "93.184.216.34:443") {
		n.direct.Add(1)
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, n.directTarget)
	}
	if network != "tcp" || address != n.proxy {
		n.denied.Add(1)
		return nil, errors.New("metadata fixture denied direct or unexpected dial")
	}
	n.dials.Add(1)
	return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
}
func (n *metadataClosedNetwork) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	if network != "ip" || host != "models.dev" {
		n.denied.Add(1)
		return nil, errors.New("metadata fixture denied DNS")
	}
	n.dns.Add(1)
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

type metadataClosedWire struct {
	builder *egressnetwork.Builder
	roots   *x509.CertPool
	sends   atomic.Int64
	before  func()
}

func (w *metadataClosedWire) RoundTrip(r *http.Request, d providertransport.TransportDispatch) (*http.Response, transportprofile.Evidence, error) {
	if r.Method != "GET" || (r.URL.String() != "https://models.dev/models.json" && r.URL.String() != "https://models.dev/api.json") || r.Header.Get("Authorization") != "" {
		return nil, transportprofile.Evidence{}, errors.New("metadata fixture denied request")
	}
	w.sends.Add(1)
	if w.before != nil {
		w.before()
	}
	dialer, err := w.builder.Dialer(d.EgressPolicy())
	if err != nil {
		return nil, transportprofile.Evidence{}, err
	}
	transport := &http.Transport{DialContext: dialer.DialContext, DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: w.roots, MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: time.Second}
	response, err := transport.RoundTrip(r)
	return response, transportprofile.Evidence{}, err
}

func TestActualRuntimeMetadataHoldUsesItsConcreteAuxiliaryProbe(t *testing.T) {
	runActualRuntimeMetadataCase(t, metadataMatrixCase{hold: true})
}

type metadataMatrixCase struct{ prices, builtin, hold, cancel, failedProbe, proxyDown, both bool }

func TestActualRuntimeMetadataAndPricesSelectionAndHoldMatrix(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options metadataMatrixCase
	}{
		{"metadata_global", metadataMatrixCase{}}, {"prices_global", metadataMatrixCase{prices: true}},
		{"metadata_builtin", metadataMatrixCase{builtin: true}}, {"prices_builtin", metadataMatrixCase{prices: true, builtin: true}},
		{"metadata_proxy_down", metadataMatrixCase{proxyDown: true}}, {"prices_proxy_down", metadataMatrixCase{prices: true, proxyDown: true}},
		{"prices_held_edit", metadataMatrixCase{prices: true, hold: true}},
		{"same_origin_held_pair", metadataMatrixCase{both: true, hold: true}},
		{"metadata_held_cancel", metadataMatrixCase{hold: true, cancel: true}}, {"prices_held_cancel", metadataMatrixCase{prices: true, hold: true, cancel: true}},
		{"metadata_failed_probe", metadataMatrixCase{hold: true, failedProbe: true}}, {"prices_failed_probe", metadataMatrixCase{prices: true, hold: true, failedProbe: true}},
	} {
		t.Run(tc.name, func(t *testing.T) { runActualRuntimeMetadataCase(t, tc.options) })
	}
}

func runActualRuntimeMetadataCase(t *testing.T, tc metadataMatrixCase) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(31), DNSNames: []string{"models.dev"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var gets, priceGets atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/models.json"
		if tc.prices {
			wantPath = "/api.json"
		}
		if r.Method != "GET" || r.Host != "models.dev" || (r.URL.Path != wantPath && !(tc.both && r.URL.Path == "/api.json")) || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" {
			t.Error("metadata actual request identity changed")
			w.WriteHeader(400)
			return
		}
		gets.Add(1)
		if r.URL.Path == "/api.json" {
			priceGets.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	defer server.Close()
	proxy, calls, live := updateSOCKS(t, server.Listener.Addr().String(), 4*time.Second)
	network := &metadataClosedNetwork{proxy: proxy.Addr().String()}
	if tc.builtin {
		network.directTarget = server.Listener.Addr().String()
	}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network})
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	wire := &metadataClosedWire{builder: builder, roots: roots}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	offers := &productruntime.EgressOfferProbe{}
	r, err := productruntime.StartProductUpdateTestRuntime(ctx, productruntime.ProductUpdateTestOptions(t, gate), wire, builder, roots, offers)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		if err := r.Shutdown(stop); err != nil {
			t.Error(err)
		}
	}()
	productruntime.WaitRecordingTestRuntime(t, r)
	profile, err := r.EgressProfiles().Publish(ctx, egressprofile.PublishCommand{ID: "profile.metadata-matrix", DisplayName: "Metadata private exit", Policy: egressnetwork.Policy{Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: proxy.Addr().String()}}})
	if err != nil {
		t.Fatal(err)
	}
	if !tc.builtin {
		if _, err = r.EgressDefaults().SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}}); err != nil {
			t.Fatal(err)
		}
	}
	var startedMu sync.Mutex
	started := map[string]egressaudit.DecisionRef{}
	wire.before = func() {
		page, err := offers.List(t.Context(), egressaudit.PageRequest{Limit: 10})
		if err != nil {
			t.Error(err)
			return
		}
		startedMu.Lock()
		defer startedMu.Unlock()
		for _, record := range page.Items {
			if !record.Attempt.Terminal() {
				started[record.Attempt.ID()] = record.Attempt.Decision()
			}
		}
	}
	if tc.hold {
		if _, err = r.EnterOfflineHold(ctx, gate.Snapshot().Revision); err != nil {
			t.Fatal(err)
		}
	}
	if tc.proxyDown {
		if err = proxy.Close(); err != nil {
			t.Fatal(err)
		}
	}
	prices := []bool{tc.prices}
	if tc.both {
		prices = []bool{false, true}
	}
	result := make(chan error, len(prices))
	remaining := len(prices)
	for _, price := range prices {
		go func() {
			var response *http.Response
			var err error
			if price {
				response, err = r.FetchModelsDevPrices(ctx)
			} else {
				response, err = r.FetchModelsDev(ctx)
			}
			if response != nil {
				_, readErr := io.Copy(io.Discard, response.Body)
				err = errors.Join(err, readErr, response.Body.Close())
			}
			result <- err
		}()
	}
	defer func() {
		cancel()
		for remaining > 0 {
			<-result
			remaining--
		}
	}()
	if tc.hold {
		deadline := time.Now().Add(time.Second)
		for gate.Snapshot().QueuedRequests != len(prices) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		targets := gate.PendingProbeTargets()
		if len(targets) != 1 || gate.Snapshot().QueuedRequests != len(prices) || targets[0].Kind != offlinehold.EgressAuxiliary || targets[0].TargetRef != "https://models.dev" || targets[0].PlanRevision != 0 || targets[0].PlanDigest != "" || targets[0].TargetAccess != egressnetwork.TargetAccessOwnerConfigured || targets[0].EgressPolicy != profile.Policy || network.dials.Load() != 0 || wire.sends.Load() != 0 {
			t.Fatalf("wrong queued targets %+v", targets)
		}
		if _, err = r.EgressDefaults().SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 2}); err != nil {
			t.Fatal(err)
		}
		if tc.cancel {
			cancel()
		} else {
			if tc.failedProbe {
				proxy.Close()
			}
			_, err = r.ResumeOfflineHold(ctx, gate.Snapshot().Revision)
			if tc.failedProbe {
				if err == nil || gate.Snapshot().State != offlinehold.StateHeld || gate.Snapshot().QueuedRequests != len(prices) {
					t.Fatal("failed real probe released queue")
				}
				cancel()
			} else if err != nil {
				t.Fatal(err)
			}
		}
	}
	for remaining > 0 {
		err := <-result
		remaining--
		if (err != nil) != (tc.cancel || tc.failedProbe || tc.proxyDown) {
			t.Fatalf("unexpected operation error=%v", err)
		}
	}
	page, err := productruntime.SettledEgressTestPage(t, r, egressaudit.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if tc.cancel || tc.failedProbe {
		wantDials := int64(0)
		if tc.failedProbe {
			wantDials = 1
		}
		if len(page.Items) != 0 || calls.Load() != 0 || network.dials.Load() != wantDials || network.denied.Load() != 0 || network.direct.Load() != 0 || wire.sends.Load() != 0 || gets.Load() != 0 || live.Load() != 0 || gate.Snapshot().QueuedRequests != 0 || gate.Snapshot().ActiveEgress != 0 {
			t.Fatal("canceled/failed probe caused late send or leaked queue")
		}
		return
	}
	wantCalls, wantDials, wantDirect, wantGets := int64(len(prices)), int64(len(prices)), int64(0), int64(len(prices))
	if tc.hold {
		wantCalls++
		wantDials++
	}
	if tc.builtin {
		wantCalls, wantDials, wantDirect = 0, 0, int64(len(prices))
	}
	if tc.proxyDown {
		wantCalls, wantGets = 0, 0
	}
	if calls.Load() != wantCalls || network.dials.Load() != wantDials || network.direct.Load() != wantDirect || network.denied.Load() != 0 || wire.sends.Load() != int64(len(prices)) || gets.Load() != wantGets || len(page.Items) != len(prices) {
		t.Fatalf("wrong actual path: proxy%d dial%d direct%d denied%d sends%d GET%d audit%d", calls.Load(), network.dials.Load(), network.direct.Load(), network.denied.Load(), wire.sends.Load(), gets.Load(), len(page.Items))
	}
	wantPrices := int64(0)
	if (tc.prices || tc.both) && !tc.proxyDown {
		wantPrices = 1
	}
	if priceGets.Load() != wantPrices {
		t.Fatal("metadata/prices path substituted")
	}
	for _, record := range page.Items {
		attempt := record.Attempt
		d := attempt.Decision()
		source, revision, profileID := egressprofile.SelectionGlobal, uint64(2), profile.ID.String()
		if tc.builtin {
			source, revision, profileID = egressprofile.SelectionBuiltin, 1, egressprofile.DirectID.String()
		}
		startedMu.Lock()
		before, ok := started[attempt.ID()]
		startedMu.Unlock()
		if !ok || before != d || !attempt.Terminal() || attempt.Purpose() != egressaudit.PurposeModelMetadataDirectory || d.SelectionSource != source || d.DefaultRevision != revision || d.ProxyID != profileID || d.AccountID != "" || (tc.proxyDown && attempt.Outcome() != egressaudit.OutcomeFailed) {
			t.Fatal("metadata immutable audit mismatch", d)
		}
	}
}
