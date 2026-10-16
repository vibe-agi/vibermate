package providertransport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
)

type tokenProbeResolver struct{}

func (tokenProbeResolver) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	if network != "ip" || host != "auth.openai.com" {
		return nil, errors.New("token probe fixture rejected DNS")
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

// Catches a fixed token producer leaking operation/wire-plan identity into a
// reachability probe, or broadened producer admission for any altered shape.
func TestTokenProbeClosedProducerAndConcreteTLSReachability(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(61), DNSNames: []string{"auth.openai.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var httpCalls atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { httpCalls.Add(1); w.WriteHeader(500) }))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	defer server.Close()
	proxy, calls := newAccountSOCKSFixture(t, server.Listener.Addr().String())
	network := &accountLoopbackNetwork{proxy: proxy.Addr().String()}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: tokenProbeResolver{}})
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	prober, err := originaltransport.NewTLSProberWithBuilder(builder, roots)
	if err != nil {
		t.Fatal(err)
	}
	gate := newStartedGate(t)
	client := newRuntimeFetchClient(t, gate, &runtimeTransportStub{}, &runtimeAuditRecorder{})
	policy := egressnetwork.DefaultPolicy()
	policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: proxy.Addr().String()}
	profile := egressprofile.ProfileRevision{ID: "profile.closed-token", Revision: 1, DisplayName: "Closed token", Policy: policy, PublishedAt: time.Unix(1, 0)}
	resolved, err := egressprofile.RestoreResolved(profile, egressprofile.SelectionGlobal, 2)
	if err != nil {
		t.Fatal(err)
	}
	newSpec := func() runtimeFetchSpec {
		origin, _ := originidentity.ParseProviderOrigin("https://auth.openai.com")
		target, _ := NewTarget(origin)
		return runtimeFetchSpec{purpose: egressaudit.PurposeCredentialRefresh, targetRef: "runtime.codex-oauth", target: target, method: http.MethodPost, relativePath: "/oauth/token", body: []byte(`{"grant_type":"refresh_token","refresh_token":"synthetic"}`), contentType: "application/json", resolved: resolved}
	}
	for _, shape := range []string{"managed_refresh", "first_login_form"} {
		t.Run(shape, func(t *testing.T) {
			spec := newSpec()
			if shape == "managed_refresh" {
				spec.account = testAccountRef()
				spec.account.EgressProfile = profile
				spec.account.ResolvedEgress = resolved
				if err := spec.account.Validate(); err != nil {
					t.Fatal(err)
				}
			} else {
				spec.contentType = "application/x-www-form-urlencoded"
				spec.body = []byte("grant_type=authorization_code&code=synthetic")
			}
			target, err := client.runtimeProbeTarget(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			before := calls.Load()
			if err := prober.Probe(t.Context(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}}); err != nil {
				t.Fatalf("closed token producer cannot reach concrete TLS probe: %v target=%+v", err, target)
			}
			if target.Kind != offlinehold.EgressAuxiliary || target.Transport != offlinehold.ProbeTransportStrictTLS || target.TargetRef != "https://auth.openai.com" || target.NetworkOrigin != "https://auth.openai.com" || target.HTTPAuthority != "auth.openai.com" || target.TLSServerName != "auth.openai.com" || target.PlanRevision != 0 || target.PlanDigest != "" || target.TargetAccess != egressnetwork.TargetAccessOwnerConfigured || target.EgressPolicy != policy || calls.Load() != before+1 || httpCalls.Load() != 0 || network.direct.Load() != 0 {
				t.Fatal("token probe changed route/identity or sent application bytes", target)
			}
			restricted, err := client.runtimeProbeTarget(egressnetwork.WithTargetAccess(t.Context(), egressnetwork.TargetAccessPublicOnly), spec)
			if err != nil || restricted.TargetAccess != egressnetwork.TargetAccessPublicOnly {
				t.Fatalf("restricted producer lost access: %+v %v", restricted, err)
			}
			physicalBefore := calls.Load()
			if err := prober.Probe(t.Context(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{restricted}}); err == nil || calls.Load() != physicalBefore || network.direct.Load() != 0 || httpCalls.Load() != 0 {
				t.Fatalf("restricted auxiliary probe escaped: err=%v proxy_dials=%d", err, calls.Load()-physicalBefore)
			}

		})
	}
	for _, edge := range []string{"origin", "port", "path", "query", "method", "reference", "content-type", "empty-body", "oversized-body", "credential", "captured", "invalid-account"} {
		t.Run(edge, func(t *testing.T) {
			spec := newSpec()
			switch edge {
			case "origin", "port":
				raw := "https://unapproved.invalid"
				if edge == "port" {
					raw = "https://auth.openai.com:444"
				}
				origin, _ := originidentity.ParseProviderOrigin(raw)
				spec.target, err = NewTarget(origin)
				if err != nil {
					t.Fatal(err)
				}
			case "path":
				spec.relativePath = "/other"
			case "query":
				spec.relativePath = "/oauth/token?code=synthetic"
			case "method":
				spec.method = http.MethodGet
			case "reference":
				spec.targetRef = "runtime.unapproved"
			case "content-type":
				spec.contentType = "text/plain"
			case "empty-body":
				spec.body = nil
			case "oversized-body":
				spec.body = make([]byte, (64<<10)+1)
			case "credential":
				spec.credential = testRuntimeDiscoveryCredential(t, "fixture-realm")
			case "captured":
				spec.captured = &capturedAccountRead{}
			case "invalid-account":
				spec.account = providerauth.AccountRef{ID: "incomplete"}
			}
			before := calls.Load()
			target, producerErr := client.runtimeProbeTarget(t.Context(), spec)
			if producerErr == nil {
				probeErr := prober.Probe(t.Context(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}})
				t.Errorf("altered token producer accepted edge=%s concreteProbeErr=%v", edge, probeErr)
			}
			if calls.Load() != before || httpCalls.Load() != 0 || network.direct.Load() != 0 {
				t.Fatal("altered token producer reached network/application")
			}
		})
	}
}
