package providertransport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
)

// Test-only bridge: the external package assembles real Store/Manager authority
// without introducing provideraccount -> exchange -> providertransport cycles.
func ExerciseCapturedAccountProbe(t *testing.T, acquire func(t *testing.T, origin, path, accountID string, history bool, profile egressprofile.ProfileRevision) (environment.RequestPlan, providerauth.Lease)) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(92), DNSNames: []string{"chatgpt.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var httpCalls atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { httpCalls.Add(1); w.WriteHeader(500) }))
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
	profile := egressprofile.ProfileRevision{ID: "profile.captured-probe", Revision: 1, DisplayName: "Captured probe", Policy: policy, PublishedAt: time.Unix(1, 0)}
	newSpec := func(t *testing.T, origin, path, accountID string, history bool) runtimeFetchSpec {
		plan, lease := acquire(t, origin, path, accountID, history, profile)
		t.Cleanup(lease.Release)
		request, err := NewCapturedAccountRead(plan, lease, "connection.captured-probe", "operation.captured-probe")
		if err != nil {
			t.Fatal(err)
		}
		target, err := NewTarget(request.read.Origin())
		if err != nil {
			t.Fatal(err)
		}
		account, ok := lease.Account()
		if !ok {
			t.Fatal("real Manager lease has no account")
		}
		return runtimeFetchSpec{purpose: egressaudit.PurposeRouteOperation, targetRef: request.targetRef, target: target, relativePath: request.read.Path(), credential: lease, method: http.MethodGet, captured: request.captured, account: account, resolved: account.ResolvedEgress}
	}
	for _, origin := range []string{"https://chatgpt.com", "https://chatgpt.com/backend-api", "https://chatgpt.com/backend-api/codex"} {
		for _, path := range []string{"/backend-api/wham/usage", "/backend-api/wham/profiles/me"} {
			t.Run(origin+path, func(t *testing.T) {
				spec := newSpec(t, origin, path, "captured-b", true)
				plan := spec.captured.plan
				target, err := client.runtimeProbeTarget(t.Context(), spec)
				if err != nil {
					t.Fatal(err)
				}
				before := calls.Load()
				if err := prober.Probe(t.Context(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}}); err != nil {
					t.Fatal("concrete captured TLS probe", target, err)
				}
				if target.Kind != offlinehold.EgressAuxiliary || target.Transport != offlinehold.ProbeTransportStrictTLS || target.TargetRef != "https://chatgpt.com" || target.NetworkOrigin != "https://chatgpt.com" || target.HTTPAuthority != "chatgpt.com" || target.TLSServerName != "chatgpt.com" || target.PlanRevision != 0 || target.PlanDigest != "" || target.TargetAccess != egressnetwork.TargetAccessOwnerConfigured || target.EgressPolicy != policy || calls.Load() != before+1 || httpCalls.Load() != 0 || network.direct.Load() != 0 {
					t.Fatal("captured probe lost closed reachability", target)
				}
				restricted, err := client.runtimeProbeTarget(egressnetwork.WithTargetAccess(t.Context(), egressnetwork.TargetAccessPublicOnly), spec)
				if err != nil || restricted.TargetAccess != egressnetwork.TargetAccessPublicOnly {
					t.Fatalf("restricted producer lost access: %+v %v", restricted, err)
				}
				physicalBefore := calls.Load()
				if err := prober.Probe(t.Context(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{restricted}}); err == nil || calls.Load() != physicalBefore || network.direct.Load() != 0 || httpCalls.Load() != 0 {
					t.Fatalf("restricted auxiliary probe escaped: err=%v proxy_dials=%d", err, calls.Load()-physicalBefore)
				}

				if spec.target.Origin().String() != origin || spec.captured.plan.EnvironmentRevision() == 0 || spec.captured.plan.EnvironmentDigest().String() == "" || spec.captured.plan.EnvironmentDigest() != plan.EnvironmentDigest() || spec.captured.plan.EnvironmentRevision() != plan.EnvironmentRevision() {
					t.Fatal("captured producer erased original send authority")
				}
				t.Logf("providerOrigin=%s environment=%s revision=%d digest=%s probe=%+v HTTP=0 proxyDelta=1 direct=0", spec.target.Origin(), plan.EnvironmentID(), plan.EnvironmentRevision(), plan.EnvironmentDigest(), target)
			})
		}
	}
	for _, edge := range []string{"nil_capture", "missing_credential", "nonmanaged", "zero_account", "account_id", "account_revision", "realm", "connection", "operation", "target_id", "origin", "path", "method", "body", "content_type", "purpose", "denied_history", "reset_details", "consume", "unknown_read", "resolved", "account_exit", "different_real_lease", "original_account"} {
		t.Run(edge, func(t *testing.T) {
			spec := newSpec(t, "https://chatgpt.com", "/backend-api/wham/usage", "captured-b", true)
			switch edge {
			case "nil_capture":
				spec.captured = nil
			case "missing_credential":
				spec.credential = nil
			case "nonmanaged":
				spec.credential = &ownedProbeCredential{runtimeDiscoveryCredential: testRuntimeDiscoveryCredential(t, "openai.chatgpt").(*runtimeDiscoveryCredential), mode: providerauth.CredentialClientPassthrough}
			case "zero_account":
				spec.account = providerauth.AccountRef{}
			case "account_id":
				spec.account.ID = "different"
			case "account_revision":
				spec.account.Revision++
			case "realm":
				spec.account.RealmID = "different"
			case "connection":
				spec.captured.connectionID = ""
			case "operation":
				spec.captured.operationID = ""
			case "target_id":
				spec.targetRef = "different"
			case "origin":
				origin, _ := originidentity.ParseProviderOrigin("https://unapproved.invalid")
				spec.target, _ = NewTarget(origin)
			case "path":
				spec.relativePath = "/backend-api/wham/profiles/me"
			case "method":
				spec.method = http.MethodPost
			case "body":
				spec.body = []byte("unexpected")
			case "content_type":
				spec.contentType = "text/plain"
			case "purpose":
				spec.purpose = egressaudit.PurposeUpstreamAccountRead
			case "denied_history":
				plan, lease := acquire(t, "https://chatgpt.com", "/backend-api/wham/profiles/me", "captured-b", false, profile)
				t.Cleanup(lease.Release)
				spec.captured.plan = plan
				spec.credential = lease
				spec.relativePath = "/backend-api/wham/profiles/me"
				spec.account, _ = lease.Account()
				spec.resolved = spec.account.ResolvedEgress
			case "reset_details":
				spec.relativePath = "/backend-api/wham/rate-limit-reset-credits"
			case "consume":
				spec.method = http.MethodPost
				spec.relativePath = "/backend-api/wham/rate-limit-reset-credits/consume"
			case "unknown_read":
				spec.captured.plan = environment.RequestPlan{}
				spec.relativePath = "/closed-unknown-read"
			case "resolved":
				spec.resolved, _ = egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionBuiltin, 1)
			case "account_exit":
				spec.account.ResolvedEgress, _ = egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionBuiltin, 1)
				spec.account.EgressProfile = egressprofile.Direct()
			case "different_real_lease":
				_, lease := acquire(t, "https://chatgpt.com", "/backend-api/wham/usage", "captured-other", true, profile)
				t.Cleanup(lease.Release)
				spec.credential = lease
				spec.account, _ = lease.Account()
				spec.resolved = spec.account.ResolvedEgress
			case "original_account":
				plan, lease := acquire(t, "https://chatgpt.com", "/backend-api/wham/usage", "original", false, profile)
				t.Cleanup(lease.Release)
				spec.captured.plan = plan
			}
			before := calls.Load()
			if _, err := capturedAccountProbeTarget(spec); err == nil {
				t.Fatal("altered captured producer accepted", edge)
			}
			if edge != "nil_capture" {
				if _, err := client.runtimeProbeTarget(t.Context(), spec); err == nil {
					t.Fatal("captured dispatch accepted altered closed operation", edge)
				}
			}
			if calls.Load() != before || httpCalls.Load() != 0 || network.direct.Load() != 0 {
				t.Fatal("rejected captured producer reached network")
			}
		})
	}
	t.Run("generic_routed_auxiliary_stays_rejected", func(t *testing.T) {
		spec := newSpec(t, "https://chatgpt.com", "/backend-api/wham/usage", "captured-b", true)
		spec.captured = nil
		target, err := client.runtimeProbeTarget(t.Context(), spec)
		if err != nil {
			t.Fatal(err)
		}
		before := calls.Load()
		if target.PlanRevision == 0 || target.PlanDigest == "" || prober.Probe(t.Context(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}}) == nil || calls.Load() != before || httpCalls.Load() != 0 {
			t.Fatal("generic routed Auxiliary gained closed captured exception")
		}
	})
}
