package providertransport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
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
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

type ownedProbeCredential struct {
	*runtimeDiscoveryCredential
	mode       providerauth.CredentialMode
	driver     providerauth.DriverRef
	hasAccount bool
}

func (c *ownedProbeCredential) Mode() providerauth.CredentialMode { return c.mode }
func (c *ownedProbeCredential) Driver() providerauth.DriverRef    { return c.driver }
func (c *ownedProbeCredential) Account() (providerauth.AccountRef, bool) {
	return c.account, c.hasAccount
}

// Catches a closed owned producer leaking sender/account identity into the
// strict origin reachability contract, and any widening to arbitrary requests.
func TestOwnedAccountProbeClosedProducerAndConcreteTLSReachability(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(73), DNSNames: []string{"chatgpt.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
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
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network})
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	prober, err := originaltransport.NewTLSProberWithBuilder(builder, roots)
	if err != nil {
		t.Fatal(err)
	}
	client := newRuntimeFetchClient(t, newStartedGate(t), &runtimeTransportStub{}, &runtimeAuditRecorder{})
	policy := egressnetwork.DefaultPolicy()
	policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: proxy.Addr().String()}
	profile := egressprofile.ProfileRevision{ID: "profile.owned-probe", Revision: 1, DisplayName: "Closed owned", Policy: policy, PublishedAt: time.Unix(1, 0)}
	resolved, err := egressprofile.RestoreResolved(profile, egressprofile.SelectionGlobal, 2)
	if err != nil {
		t.Fatal(err)
	}
	newSpec := func(originString, operation string) runtimeFetchSpec {
		origin, err := originidentity.ParseProviderOrigin(originString)
		if err != nil {
			t.Fatal(err)
		}
		target, err := NewTarget(origin)
		if err != nil {
			t.Fatal(err)
		}
		credential := &ownedProbeCredential{runtimeDiscoveryCredential: testRuntimeDiscoveryCredential(t, "chatgpt").(*runtimeDiscoveryCredential), mode: providerauth.CredentialManaged, driver: providerauth.CodexOAuthDriverRef(), hasAccount: true}
		credential.account.EgressProfile, credential.account.ResolvedEgress = profile, resolved
		spec := runtimeFetchSpec{purpose: egressaudit.PurposeUpstreamAccountRead, targetRef: credential.account.ID, target: target, credential: credential, account: credential.account, resolved: resolved, method: "GET"}
		if operation == "reset" {
			spec.purpose, spec.method, spec.relativePath, spec.contentType = egressaudit.PurposeUpstreamAccountAction, "POST", resetConsumePath, "application/json"
			requestID, _ := ResetRequestID(credential.account.ID, "credit-probe")
			spec.body = []byte(fmt.Sprintf(`{"redeem_request_id":%q,"credit_id":"credit-probe"}`, requestID))
		} else {
			read, err := upstreamservice.ResolveRead(origin, operation)
			if err != nil {
				t.Fatal(err)
			}
			spec.relativePath = read.Path()
		}
		return spec
	}
	for _, origin := range []string{"https://chatgpt.com", "https://chatgpt.com/backend-api", "https://chatgpt.com/backend-api/codex"} {
		for _, operation := range []string{upstreamservice.CodexRateLimits, upstreamservice.CodexUsageHistory, upstreamservice.CodexResetCreditDetails, "reset"} {
			t.Run(origin+"/"+operation, func(t *testing.T) {
				spec := newSpec(origin, operation)
				target, err := client.runtimeProbeTarget(spec)
				if err != nil {
					t.Fatal(err)
				}
				before := calls.Load()
				if err := prober.Probe(t.Context(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}}); err != nil {
					t.Fatalf("closed owned producer cannot reach concrete TLS probe: %v target=%+v", err, target)
				}
				if target.Kind != offlinehold.EgressAuxiliary || target.Transport != offlinehold.ProbeTransportStrictTLS || target.TargetRef != "https://chatgpt.com" || target.NetworkOrigin != "https://chatgpt.com" || target.HTTPAuthority != "chatgpt.com" || target.TLSServerName != "chatgpt.com" || target.PlanRevision != 0 || target.PlanDigest != "" || target.TargetAccess != 0 || target.EgressPolicy != policy || calls.Load() != before+1 || httpCalls.Load() != 0 || network.direct.Load() != 0 {
					t.Fatal("owned probe changed exit/identity or sent application bytes", target)
				}
			})
		}
	}
	for _, operation := range []string{upstreamservice.CodexRateLimits, "reset"} {
		edges := []string{"origin", "port", "base-path", "path", "query", "method", "reference", "content-type", "body", "missing-credential", "nonmanaged", "invalid-account", "missing-account", "different-account", "different-resolved", "captured", "route-purpose"}
		if operation == "reset" {
			edges = append(edges, "wrong-reset-driver", "invalid-reset-id", "invalid-credit-id", "extra-reset-field", "duplicate-reset-field", "empty-body", "trailing-body", "missing-reset-field")
		}
		for _, edge := range edges {
			t.Run(operation+"/"+edge, func(t *testing.T) {
				spec := newSpec("https://chatgpt.com", operation)
				credential := spec.credential.(*ownedProbeCredential)
				switch edge {
				case "origin", "port", "base-path":
					raw := "https://unapproved.invalid"
					if edge == "port" {
						raw = "https://chatgpt.com:444"
					}
					if edge == "base-path" {
						raw = "https://chatgpt.com/unapproved"
					}
					origin, _ := originidentity.ParseProviderOrigin(raw)
					spec.target, _ = NewTarget(origin)
				case "path":
					spec.relativePath = "/unknown"
				case "query":
					spec.relativePath += "?extra=true"
				case "method":
					spec.method = "DELETE"
				case "reference":
					spec.targetRef = "other-account"
				case "content-type":
					spec.contentType = "text/plain"
				case "body":
					spec.body = []byte("unexpected")
				case "missing-credential":
					spec.credential = nil
				case "nonmanaged":
					credential.mode = providerauth.CredentialClientPassthrough
				case "invalid-account":
					credential.account.CredentialEpoch = 0
				case "missing-account":
					credential.hasAccount = false
				case "different-account":
					spec.account.ID = "different"
				case "different-resolved":
					spec.resolved, _ = egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionBuiltin, 1)
				case "captured":
					spec.captured = &capturedAccountRead{}
				case "route-purpose":
					spec.purpose = egressaudit.PurposeRouteOperation
				case "wrong-reset-driver":
					credential.driver = providerauth.StaticHeaderDriverRef()
				case "invalid-reset-id":
					spec.body = []byte(`{"credit_id":"credit-probe","redeem_request_id":"bad"}`)
				case "invalid-credit-id":
					spec.body = []byte(`{"redeem_request_id":"8a4e4309-657f-543e-bd9a-673f797a0fa8","credit_id":"bad\ncredit"}`)
				case "empty-body":
					spec.body = nil
				case "trailing-body":
					spec.body = append(spec.body, []byte(`{}`)...)
				case "missing-reset-field":
					spec.body = []byte(`{"credit_id":"credit-probe"}`)
				case "duplicate-reset-field":
					spec.body = []byte(`{"redeem_request_id":"8a4e4309-657f-543e-bd9a-673f797a0fa8","credit_id":"other","credit_id":"credit-probe"}`)
				case "extra-reset-field":
					spec.body = []byte(`{"credit_id":"credit-probe","redeem_request_id":"8a4e4309-657f-543e-bd9a-673f797a0fa8","extra":true}`)
				}
				before := calls.Load()
				target, producerErr := client.runtimeProbeTarget(spec)
				if edge == "route-purpose" {
					if producerErr == nil && (target.PlanRevision == 0 || target.PlanDigest == "" || prober.Probe(t.Context(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}}) == nil) {
						t.Error("captured route purpose gained owned reachability exception")
					}
				} else if producerErr == nil {
					t.Errorf("altered owned producer accepted %s", edge)
				}
				if calls.Load() != before || httpCalls.Load() != 0 || network.direct.Load() != 0 {
					t.Fatal("altered owned producer reached network")
				}
			})
		}
	}
}
