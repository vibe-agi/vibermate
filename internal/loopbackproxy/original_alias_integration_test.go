package loopbackproxy_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clienttarget"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

type aliasWire struct {
	transport *http.Transport
	denied    atomic.Int64
}

func (w *aliasWire) RoundTrip(r *http.Request, _ egressnetwork.Policy) (*http.Response, error) {
	return w.transport.RoundTrip(r)
}

// The real Handler and originaltransport.Client must send the client's synthetic
// credentials only to its frozen alias, even beside managed model Routes.
func TestOriginalAliasPreservesCredentialDestination(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, cleartext := range []bool{false, true} {
			name := "https"
			if cleartext {
				name = "http"
			}
			if managed {
				name += "_managed"
			}
			t.Run(name, func(t *testing.T) {
				const path = "/backend-api/codex/models"
				const query = "client_version=synthetic%2Balias"
				var calls atomic.Int64
				var expectedHost string
				peer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Host != expectedHost || r.URL.Path != path || r.URL.RawQuery != query || r.Header.Get("Authorization") != "Bearer synthetic-alias-secret" || r.Header.Get("Proxy-Authorization") != "" {
						t.Errorf("original request changed: %s auth=%v", r.URL, r.Header.Get("Authorization") == "Bearer synthetic-alias-secret")
						w.WriteHeader(400)
						return
					}
					w.WriteHeader(http.StatusAccepted)
				}))
				actual := "https://example.com:8443"
				if cleartext {
					actual = "http://" + peer.Listener.Addr().String()
				}
				targetURL, _ := url.Parse(actual)
				expectedHost = targetURL.Host
				if cleartext {
					peer.Start()
				} else {
					peer.StartTLS()
				}
				defer peer.Close()
				roots := x509.NewCertPool()
				if !cleartext {
					roots.AddCert(peer.Certificate())
				}
				wire := &aliasWire{}
				wire.transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
					if network != "tcp" || address != targetURL.Host {
						wire.denied.Add(1)
						return nil, errors.New("closed alias fixture rejected destination")
					}
					return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, peer.Listener.Addr().String())
				}}
				defer wire.transport.CloseIdleConnections()
				gate, err := offlinehold.New(offlinehold.DefaultConfig())
				if err != nil {
					t.Fatal(err)
				}
				if err = gate.Start(t.Context(), offlinehold.RuntimeBinding{InstanceID: "alias-fixture"}); err != nil {
					t.Fatal(err)
				}
				defer func() {
					gate.BeginShutdown()
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					if err := gate.Drain(ctx); err != nil {
						t.Error(err)
					}
				}()
				observed := &observedOriginalClient{}
				adapter := fixedCodexAdapterEvidence()
				compiler := testEnvironmentCompiler(t, protocolspec.DialectOpenAIResponses, livenessAccountCatalog{})
				composition := &proxyFixtureComposition{compiler: compiler, configure: func(e *environment.Environment) {
					if managed {
						configureLivenessUpstream(t, e, "https://managed.example")
					}
				}, configureProxy: func(options *loopbackproxy.Options) {
					observed.real, err = originaltransport.New(originaltransport.Options{Coordinator: gate, Transport: wire, Audit: options.EgressAudit})
					if err != nil {
						t.Fatal(err)
					}
					options.Original = observed
				}}
				f := newProxyFixtureConfigured(t, protocolspec.DialectOpenAIResponses, &adapter, allowEverythingTestPolicy(t), nil, "", composition, "https://api.openai.com")
				defer f.Close(t)
				defer func() {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					if err := observed.real.Shutdown(ctx); err != nil {
						t.Error(err)
					}
				}()
				grant, err := f.runs.Create(t.Context(), capturerun.CreateCommand{CWD: t.TempDir(), CanonicalExecutablePath: "/tmp/synthetic-codex", ExecutableLabel: "codex", Lifetime: time.Minute, CatalogRevision: adapter.CatalogRevision, Adapter: &adapter, Recognition: fixtureRecognition(&adapter), Workspace: proxyWorkspaceScope(t)})
				if err != nil {
					t.Fatal(err)
				}
				capture, err := captureidentity.New(captureidentity.KindManagedRun, grant.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				profile, err := clienttarget.NewProfile("codex-cli", clienttarget.EnvironmentFacts{CodexBaseURL: actual + "/backend-api/codex"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.assignments.Create(t.Context(), captureassignment.CreateCommand{Capture: capture, EnvironmentID: f.environment.ID, Source: captureassignment.SourceLaunch, ClientProfile: profile}); err != nil {
					t.Fatal(err)
				}
				proxyURL, _ := url.Parse("http://" + f.listener.Addr().String())
				proxyURL.User = url.UserPassword("capture", grant.ProxyCapability.Value())
				downstreamRoots := x509.NewCertPool()
				downstreamRoots.AppendCertsFromPEM(f.authority.Certificate().CertificatePEM())
				downstream := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: downstreamRoots, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
				defer downstream.CloseIdleConnections()
				client := &http.Client{Transport: downstream, Timeout: 3 * time.Second}
				request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, actual+path+"?"+query, nil)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer synthetic-alias-secret")
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				requests := observed.snapshot()
				if len(requests) != 1 {
					t.Fatalf("original requests=%d status=%d", len(requests), response.StatusCode)
				}
				if requests[0].Origin().String() != actual || response.StatusCode != 202 || calls.Load() != 1 || wire.denied.Load() != 0 {
					t.Fatalf("actual=%s outbound=%s status=%d peer_calls=%d denied=%d", actual, requests[0].Origin(), response.StatusCode, calls.Load(), wire.denied.Load())
				}
				for _, rejected := range []string{"/backend-api/codex/unknown", "/backend-api/plugins/featured", path + "?not_catalogued=secret"} {
					request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, actual+rejected, nil)
					request.Header.Set("Authorization", "Bearer synthetic-alias-secret")
					response, err := client.Do(request)
					if err != nil {
						t.Fatal(err)
					}
					io.Copy(io.Discard, response.Body)
					response.Body.Close()
					if response.StatusCode < 400 || len(observed.snapshot()) != 1 || calls.Load() != 1 || wire.denied.Load() != 0 {
						t.Fatalf("out-of-scope operation %s escaped: status=%d calls=%d", rejected, response.StatusCode, calls.Load())
					}
				}

			})
		}
	}
}
