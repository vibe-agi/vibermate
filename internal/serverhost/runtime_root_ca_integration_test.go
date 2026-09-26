package serverhost_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/serverhost"
	"github.com/vibe-agi/vibermate/internal/serveridentity"
)

func rootCATestSession(t *testing.T, client *http.Client, host *serverhost.Host) servercontrol.AdminSession {
	t.Helper()
	status := host.Status()
	recovery, err := os.ReadFile(status.RecoveryKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(recovery)
	login := postJSON(t, client, status.Scheme+"://"+status.ListenAddress+servercontrol.AdminSessionPath, "",
		servercontrol.AdminLogin{Schema: servercontrol.AdminLoginSchema, AccessKey: strings.TrimSpace(string(recovery))})
	defer login.Body.Close()
	if login.StatusCode != http.StatusCreated {
		t.Fatalf("fixture login: %d", login.StatusCode)
	}
	var session servercontrol.AdminSession
	if err := json.NewDecoder(login.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	return session
}

func readRuntimeRootCA(t *testing.T, client *http.Client, base, token string) servercontrol.RuntimeRootCA {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, base+servercontrol.RuntimeRootCAPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Root CA export: %d", response.StatusCode)
	}
	var exported servercontrol.RuntimeRootCA
	if err := json.NewDecoder(response.Body).Decode(&exported); err != nil {
		t.Fatal(err)
	}
	if exported.Schema != servercontrol.RuntimeRootCASchema || strings.Contains(exported.CertificatePEM, "PRIVATE") {
		t.Fatal("download is not exclusively the public Runtime Root")
	}
	return exported
}

func strictRootCAClient(t *testing.T, certificatePEM, address string) *http.Client {
	t.Helper()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(certificatePEM)) {
		t.Fatal("download is not usable as a trust anchor")
	}
	transport := &http.Transport{
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots},
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

func TestRuntimeRootCAEstablishesStrictHTTPSForConfiguredHosts(t *testing.T) {
	root := t.TempDir()
	options := serverOptions(t, root)
	options.Transport.TLSHosts = []string{"vibermate.example.test", "192.168.1.20"}
	host, err := serverhost.Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownServer(t, host)
	status := host.Status()
	client := tlsHTTPClient(t)
	base := "https://" + status.ListenAddress
	unauthorized, err := client.Get(base + servercontrol.RuntimeRootCAPath)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatal("Root CA API accepted unauthenticated access")
	}
	session := rootCATestSession(t, client, host)
	exported := readRuntimeRootCA(t, client, base, session.ReadToken)
	trafficRoot, err := os.ReadFile(filepath.Join(root, "data", "local-ca", "root-certificate.pem"))
	if err != nil || exported.CertificatePEM != string(trafficRoot) || exported.Fingerprint == status.TLSFingerprint {
		t.Fatal("download is not the exact traffic Root, distinct from the HTTPS leaf")
	}
	verifiedClient := strictRootCAClient(t, exported.CertificatePEM, status.ListenAddress)
	for _, address := range []string{"localhost", "127.0.0.1", "[::1]", "vibermate.example.test", "192.168.1.20"} {
		verified, err := verifiedClient.Get("https://" + address + servercontrol.WebAuthPath)
		if err != nil {
			t.Fatalf("downloaded Root cannot establish strict HTTPS for %s: %v", address, err)
		}
		_, _ = io.Copy(io.Discard, verified.Body)
		verified.Body.Close()
		if verified.StatusCode != http.StatusOK || verified.TLS == nil || len(verified.TLS.VerifiedChains) == 0 {
			t.Fatal("strict HTTPS verification failed")
		}
		leaf := verified.TLS.PeerCertificates[0]
		digest := sha256.Sum256(leaf.Raw)
		if leaf.IsCA || hex.EncodeToString(digest[:]) != status.TLSFingerprint {
			t.Fatal("listener identity changed or served the Root as its leaf")
		}
	}
	if mismatch, err := verifiedClient.Get("https://not-configured.example.test" + servercontrol.WebAuthPath); err == nil {
		mismatch.Body.Close()
		t.Fatal("Root CA trust bypassed hostname verification")
	}
	request, _ := http.NewRequest(http.MethodGet, base+servercontrol.RuntimeRootCAPath, nil)
	request.Header.Set("Authorization", "Bearer "+session.ReadToken)
	request.Header.Set("Origin", "https://another.example.test")
	crossOrigin, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	crossOrigin.Body.Close()
	if crossOrigin.StatusCode != http.StatusUnauthorized {
		t.Fatal("Root CA API accepted a cross-origin request")
	}
	// The co-located Desktop uses the same credential-free inner export, with
	// authentication owned by its outer router.
	local := httptest.NewRecorder()
	host.ManagementHandler().ServeHTTP(local, httptest.NewRequest(http.MethodGet, servercontrol.RuntimeRootCAPath, nil))
	var localRoot servercontrol.RuntimeRootCA
	if local.Code != http.StatusOK || json.Unmarshal(local.Body.Bytes(), &localRoot) != nil || localRoot != exported {
		t.Fatal("Desktop and Server exported different public Roots")
	}
}

func TestPrivateCAAccessAddressBecomesTheServerCertificateIdentity(t *testing.T) {
	for _, access := range []string{
		"vibermate.home.arpa:9666",
		"192.168.1.20:9666",
	} {
		t.Run(access, func(t *testing.T) {
			root := t.TempDir()
			options := serverOptions(t, root)
			options.AccessAddress = access
			options.Transport.TLSHosts = nil
			host, err := serverhost.Start(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			defer shutdownServer(t, host)
			rootPEM, err := os.ReadFile(
				filepath.Join(root, "data", "local-ca", "root-certificate.pem"),
			)
			if err != nil {
				t.Fatal(err)
			}
			client := strictRootCAClient(t, string(rootPEM), host.Status().ListenAddress)
			accessHost, _, _ := net.SplitHostPort(access)
			response, err := client.Get("https://" + accessHost + servercontrol.WebAuthPath)
			if err != nil {
				t.Fatalf("access identity %s: %v", accessHost, err)
			}
			response.Body.Close()
			recovery, err := os.ReadFile(host.Status().RecoveryKeyPath)
			if err != nil {
				t.Fatal(err)
			}
			setup := postJSON(t, client, "https://"+access+servercontrol.WebSetupPath, "",
				servercontrol.WebSetup{
					Schema: servercontrol.WebSetupSchema, RecoveryKey: strings.TrimSpace(string(recovery)),
					Username: "owner", Password: "synthetic-owner-password",
				})
			defer setup.Body.Close()
			if setup.StatusCode != http.StatusCreated {
				t.Fatalf("HTTPS Web setup status=%d", setup.StatusCode)
			}
			var session servercontrol.WebSession
			if err := json.NewDecoder(setup.Body).Decode(&session); err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequest(http.MethodGet,
				"https://"+access+"/api/v1/manual-captures/context?environmentId="+environment.SystemTransparentID.String(), nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer "+session.ReadToken)
			manualResponse, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer manualResponse.Body.Close()
			if manualResponse.StatusCode != http.StatusOK {
				t.Fatalf("HTTPS owner ManualCapture review status=%d", manualResponse.StatusCode)
			}
			var reviewed capturecontrol.ManualCaptureContext
			if err := json.NewDecoder(manualResponse.Body).Decode(&reviewed); err != nil {
				t.Fatal(err)
			}
			if reviewed.ProxyAddress != "https://"+access || reviewed.Root == nil ||
				reviewed.Root.Kind != "server_download" || reviewed.Root.PEMPath != "" {
				t.Fatalf("HTTPS Web ManualCapture address/CA delivery = %q, %+v", reviewed.ProxyAddress, reviewed.Root)
			}
			created := postJSON(t, client, "https://"+access+"/api/v1/manual-captures", session.WriteToken,
				capturecontrol.ManualCaptureCreateRequest{
					EnvironmentID: reviewed.EnvironmentID, DisplayName: "synthetic remote Web proxy",
					ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeUntilRevoked,
					ConfirmationToken: reviewed.ConfirmationToken,
				})
			defer created.Body.Close()
			if created.StatusCode != http.StatusCreated {
				payload, _ := io.ReadAll(created.Body)
				t.Fatalf("HTTPS Web ManualCapture create status=%d body=%s", created.StatusCode, payload)
			}
			var grant capturecontrol.ManualCaptureGrant
			if err := json.NewDecoder(created.Body).Decode(&grant); err != nil {
				t.Fatal(err)
			}
			if grant.ProxyAddress != reviewed.ProxyAddress || grant.ProxyPassword == "" ||
				grant.Root == nil || grant.Root.Kind != "server_download" {
				t.Fatal("HTTPS Web proxy grant lost the reviewed address or Root")
			}
			origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Header.Get("Proxy-Authorization") != "" {
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				_, _ = io.WriteString(writer, "https-web-proxy-ok")
			}))
			defer origin.Close()
			parsedOrigin, _ := url.Parse(origin.URL)
			rules := host.Runtime().ConnectionRules()
			current := rules.Current()
			if _, err := rules.Replace(context.Background(), current.Revision,
				[]connectionpolicy.Rule{{
					ID: "test.https-web-origin", Priority: 100,
					Decision: connectionpolicy.DecisionAllow,
					Match:    connectionpolicy.MatchExactHostPort(parsedOrigin.Hostname(), mustPort(t, parsedOrigin.Port())),
				}}, current.Mode); err != nil {
				t.Fatal(err)
			}
			proxyURL, err := url.Parse(grant.ProxyAddress)
			if err != nil {
				t.Fatal(err)
			}
			proxyURL.User = url.UserPassword(grant.ProxyUsername, grant.ProxyPassword)
			proxyClient := strictRootCAClient(t, string(rootPEM), host.Status().ListenAddress)
			proxyClient.Transport.(*http.Transport).Proxy = http.ProxyURL(proxyURL)
			proxied, err := proxyClient.Get(origin.URL + "/status")
			if err != nil {
				t.Fatal(err)
			}
			defer proxied.Body.Close()
			body, err := io.ReadAll(proxied.Body)
			if err != nil || proxied.StatusCode != http.StatusOK || string(body) != "https-web-proxy-ok" {
				t.Fatalf("HTTPS Web proxy request status=%d body=%q error=%v", proxied.StatusCode, body, err)
			}
		})
	}
}

func TestRemovedCertificateRoutesCannotChangePersistedHTTPS(t *testing.T) {
	root := t.TempDir()
	options := serverOptions(t, root)
	options.Transport.TLSHosts = []string{"192.168.1.20"}
	host, err := serverhost.Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownServer(t, host)
	initial := host.Status()
	base := "https://" + initial.ListenAddress
	client := tlsHTTPClient(t)
	session := rootCATestSession(t, client, host)
	exported := readRuntimeRootCA(t, client, base, session.ReadToken)
	identityPath := filepath.Join(root, "data", "server-transport", "server-tls-identity.json")
	before, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(before)
	for _, path := range []string{"/api/v1/server/certificate", "/api/v1/server/certificate/stage", "/api/v1/server/certificate/apply"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			for _, token := range []string{"", session.ReadToken, session.WriteToken} {
				response := sendJSON(t, client, method, base+path, token, map[string]any{
					"schema": "vibermate-server-certificate-stage-v1", "hosts": []string{"192.168.1.30"},
					"expectedFingerprint": initial.TLSFingerprint, "expectedPendingFingerprint": "",
				})
				response.Body.Close()
				if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusNotFound {
					t.Fatalf("removed route still dispatched: %s %s (%d)", method, path, response.StatusCode)
				}
			}
			local := httptest.NewRecorder()
			host.ManagementHandler().ServeHTTP(local, httptest.NewRequest(method, path, nil))
			if local.Code != http.StatusNotFound {
				t.Fatal("Desktop still dispatches a removed certificate route")
			}
		}
	}
	mutation := postJSON(t, client, base+servercontrol.RuntimeRootCAPath, session.ReadToken, map[string]any{"hosts": []string{"192.168.1.30"}})
	mutation.Body.Close()
	if mutation.StatusCode != http.StatusNotFound {
		t.Fatal("Root CA read endpoint accepted a mutation")
	}
	after, err := os.ReadFile(identityPath)
	defer clear(after)
	if err != nil || !bytes.Equal(before, after) || host.Status().TLSFingerprint != initial.TLSFingerprint || host.Status().InstanceID != initial.InstanceID {
		t.Fatal("removed API changed the active identity or restarted the Runtime")
	}
	for _, name := range []string{"server-tls-pending.json", "server-tls-ca.json", "server-tls-identity.json.previous"} {
		if _, err := os.Stat(filepath.Join(root, "data", "server-transport", name)); !os.IsNotExist(err) {
			t.Fatalf("removed API created obsolete file %s", name)
		}
	}
	shutdownServer(t, host)
	restartOptions := serverOptions(t, root)
	restartOptions.Transport.TLSHosts = []string{"192.168.1.30"}
	restarted, err := serverhost.Start(context.Background(), restartOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownServer(t, restarted)
	if restarted.Status().TLSFingerprint == initial.TLSFingerprint {
		t.Fatal("explicit startup address change did not reissue the leaf certificate")
	}
	strict := strictRootCAClient(t, exported.CertificatePEM, restarted.Status().ListenAddress)
	verified, err := strict.Get("https://192.168.1.30" + servercontrol.WebAuthPath)
	if err != nil {
		t.Fatalf("existing CA trust did not accept the deliberately reissued leaf: %v", err)
	}
	verified.Body.Close()
	if mismatch, err := strict.Get("https://192.168.1.20" + servercontrol.WebAuthPath); err == nil {
		mismatch.Body.Close()
		t.Fatal("retired access address remained in the replacement leaf")
	}
	restartSession := rootCATestSession(t, client, restarted)
	if current := readRuntimeRootCA(t, client, "https://"+restarted.Status().ListenAddress, restartSession.ReadToken); current != exported {
		t.Fatal("restart changed the downloadable Root")
	}
}

func TestRuntimeRootCAExportDoesNotDependOnHTTPSTransport(t *testing.T) {
	for _, mode := range []serverhost.TransportMode{serverhost.TransportHTTP, serverhost.TransportTLSFiles} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			options := serverOptions(t, root)
			options.Transport.Mode = mode
			var external serveridentity.Identity
			if mode == serverhost.TransportTLSFiles {
				options.AccessAddress = "localhost:9666"
				var err error
				external, err = serveridentity.Open(context.Background(), t.TempDir(), rand.Reader, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				pair, err := external.Certificate()
				if err != nil {
					t.Fatal(err)
				}
				key, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
				if err != nil {
					t.Fatal(err)
				}
				defer clear(key)
				options.Transport.CertificateFile = filepath.Join(root, "external.crt")
				options.Transport.PrivateKeyFile = filepath.Join(root, "external.key")
				if err := os.WriteFile(options.Transport.CertificateFile, external.CertificatePEM(), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(options.Transport.PrivateKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
					t.Fatal(err)
				}
			}
			host, err := serverhost.Start(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			defer shutdownServer(t, host)
			client := tlsHTTPClient(t)
			session := rootCATestSession(t, client, host)
			exported := readRuntimeRootCA(t, client, host.Status().Scheme+"://"+host.Status().ListenAddress, session.ReadToken)
			trafficRoot, err := os.ReadFile(filepath.Join(root, "data", "local-ca", "root-certificate.pem"))
			if err != nil || exported.CertificatePEM != string(trafficRoot) {
				t.Fatal("Root export depends on the transport mode")
			}
			if external.Valid() {
				if exported.Fingerprint == external.Fingerprint() {
					t.Fatal("external HTTPS leaf exported as Root")
				}
				strict := strictRootCAClient(t, exported.CertificatePEM, host.Status().ListenAddress)
				if response, err := strict.Get("https://localhost" + servercontrol.WebAuthPath); err == nil {
					response.Body.Close()
					t.Fatal("unrelated Runtime Root incorrectly trusted an external identity")
				}
			}
		})
	}
}
