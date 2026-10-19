package runlauncher_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

type codexProviderNativeRequest struct {
	authority, path, bearer, proxyAuthorization, body string
}

// This is an authenticated, terminating proxy, never a forwarding proxy. Even
// unexpected startup requests can only receive a local rejection. Its CA is
// passed to the child through the validated launch overlay, never Keychain.
type codexProviderNativeFixture struct {
	t        *testing.T
	control  *controlFixture
	server   *httptest.Server
	leaf     tls.Certificate
	managed  bool
	mu       sync.Mutex
	requests []codexProviderNativeRequest
	admitted int
}

func newCodexProviderNativeFixture(t *testing.T, binary, home, env, authority string, command []string, fill byte, managed bool) *codexProviderNativeFixture {
	t.Helper()
	rootPath, leaf := codexProviderNativeCertificates(t)
	f := &codexProviderNativeFixture{t: t, leaf: leaf, managed: managed}
	f.control = &controlFixture{t: t, executable: binary, workspace: home, rootPath: rootPath,
		credential: capability(fill), proxy: capability(fill + 1), run: capability(fill + 2),
		expectedCommand: command, expectedEnvironment: env, authorities: []string{authority},
		recipe: clientadapter.LaunchCodexResponsesHTTP, recognition: clientadapter.RecognitionVerified,
		adapter: &capturecontrol.ClientLaunchAdapterView{ClientAdapterView: capturecontrol.ClientAdapterView{
			ID: "codex-cli", Revision: 1, Version: "0.163.0", CatalogRevision: 7,
			Source: capturecontrol.ClientAdapterSourcePrelaunchDigestCatalog, InstallShape: clientadapter.InstallNativeSingleBinary,
			LaunchRecipe: clientadapter.LaunchCodexResponsesHTTP,
		}, StreamingFallbackPolicy: clientadapter.StreamingFallbackClientDefault},
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	return f
}

func (f *codexProviderNativeFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect && strings.HasPrefix(r.URL.Path, "/api/v1/") {
		if r.URL.Path == "/api/v1/capture-runs" {
			recorder := httptest.NewRecorder()
			f.control.ServeHTTP(recorder, r)
			var grant capturecontrol.LaunchGrant
			if err := json.Unmarshal(recorder.Body.Bytes(), &grant); err != nil {
				f.t.Error(err)
				return
			}
			grant.LaunchEnvironment = environment.LaunchEnvironmentPolicy{SetEnv: map[string]string{"CODEX_CA_CERTIFICATE": f.control.rootPath}}
			if !f.managed {
				grant.ManagedCredentialAuthorities = []string{}
			}
			writeControlJSON(w, recorder.Code, grant)
			return
		}
		f.control.ServeHTTP(w, r)
		return
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("capture:"+f.control.proxy))
	if r.Header.Get("Proxy-Authorization") != want {
		w.WriteHeader(http.StatusProxyAuthRequired)
		return
	}
	if r.Method != http.MethodConnect || r.Host != f.control.authorities[0] {
		http.Error(w, "fixture has no upstream transport", http.StatusBadGateway)
		return
	}
	f.mu.Lock()
	f.admitted++
	f.mu.Unlock()
	conn, buffered, err := w.(http.Hijacker).Hijack()
	if err != nil {
		f.t.Error(err)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	_ = buffered.Flush()
	secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{f.leaf}, MinVersion: tls.VersionTLS12})
	defer secure.Close()
	if err := secure.Handshake(); err != nil {
		f.t.Errorf("native fixture TLS: %v", err)
		return
	}
	request, err := http.ReadRequest(bufio.NewReader(secure))
	if err != nil {
		f.t.Errorf("native fixture HTTP: %v", err)
		return
	}
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, 4<<20))
	if err != nil {
		f.t.Error(err)
		return
	}
	wantPath := "/v1/responses"
	if r.Host == "chatgpt.com:443" {
		wantPath = "/backend-api/codex/responses"
	}
	if request.Method != http.MethodPost || request.URL.Path != wantPath || request.Header.Get("Upgrade") != "" {
		_, _ = io.WriteString(secure, "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	codec, err := openairesponses.New(openairesponses.DefaultOptions())
	if err != nil {
		f.t.Error(err)
		return
	}
	if _, _, err := codec.DecodeCompatibleClientRequest(body); err != nil {
		f.t.Errorf("production codec rejected native Responses body: %v", err)
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, codexProviderNativeRequest{r.Host, request.URL.Path, request.Header.Get("Authorization"), request.Header.Get("Proxy-Authorization"), string(body)})
	f.mu.Unlock()
	reply := "synthetic reply for " + f.control.expectedEnvironment
	wire := fmt.Sprintf("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"created_at\":1,\"status\":\"completed\",\"model\":\"fixture\",\"output\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":2,\"total_tokens\":7}}}\n\n", reply)
	_, _ = fmt.Fprintf(secure, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(wire), wire)
}

func (f *codexProviderNativeFixture) launcher(binary, home string, stdin io.Reader, stdout, stderr io.Writer) *runlauncher.Launcher {
	f.t.Helper()
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery:       fixedDiscovery{session: localdiscovery.Session{BaseURL: f.server.URL, ControlCredential: f.control.credential}},
		BaseEnvironment: codexProviderNativeEnvironment(home), Stdin: stdin, Stdout: stdout, Stderr: stderr,
		Getwd: func() (string, error) { return home, nil }, LookPath: func(string) (string, error) { return binary, nil },
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return launcher
}

func (f *codexProviderNativeFixture) assertRequest(bearer string, markers ...string) {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 || f.admitted < 1 {
		f.t.Fatalf("native Responses requests=%d authenticated tunnels=%d", len(f.requests), f.admitted)
	}
	request := f.requests[0]
	if request.bearer != "Bearer "+bearer || request.proxyAuthorization != "" {
		f.t.Fatalf("model bearer/proxy header boundary incorrect: %+v", request)
	}
	for _, secret := range []string{f.control.credential, f.control.proxy, f.control.run} {
		if strings.Contains(request.bearer+request.body, secret) {
			f.t.Fatal("Capture secret reached the native Responses boundary")
		}
	}
	for _, marker := range markers {
		if !strings.Contains(request.body, marker) {
			f.t.Fatalf("resumed Responses request lost marker %q", marker)
		}
	}
	f.control.mu.Lock()
	defer f.control.mu.Unlock()
	if f.control.createCalls != 1 || f.control.attachCalls != 1 || f.control.finishCalls != 1 {
		f.t.Fatalf("Capture lifecycle create=%d attach=%d finish=%d", f.control.createCalls, f.control.attachCalls, f.control.finishCalls)
	}
}

func codexProviderNativeCertificates(t *testing.T) (string, tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ViberMate synthetic native fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	root, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"chatgpt.com", "api.openai.com"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture-ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root}), 0600); err != nil {
		t.Fatal(err)
	}
	return path, tls.Certificate{Certificate: [][]byte{der, root}, PrivateKey: key}
}

func codexProviderNativeEnvironment(home string) []string {
	return []string{"PATH=/usr/bin:/bin", "TERM=xterm-256color", "HOME=" + home, "CODEX_HOME=" + home,
		"VIBERMATE_CODEX_PROVIDER_TOKEN=stale-placeholder",
		"NO_PROXY=chatgpt.com,api.openai.com,localhost"}
}

// Bound every native invocation, including failure paths, without depending on
// the user's shell, environment, credentials, or startup configuration.
func codexProviderNativeContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func codexProviderNativeRejectForeignCapability(t *testing.T, fixture *codexProviderNativeFixture, foreign string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fixture.server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, _ = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", fixture.control.authorities[0], fixture.control.authorities[0], base64.StdEncoding.EncodeToString([]byte("capture:"+foreign)))
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("foreign Capture capability admitted: %d", response.StatusCode)
	}
}
