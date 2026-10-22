package productruntime

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/providerauth"
)

func TestCodexWorkspaceBundleUsesOnlyExactOriginalRequest(t *testing.T) {
	f := newAccountReadFixtureWithDriver(t, providerauth.CodexOAuthDriverRef())
	original := &codexWorkspaceOriginal{t: t, token: "synthetic-A", policy: egressnetwork.DefaultPolicy()}
	proxy := f.serveProxyWithOriginal(t, original)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(f.runtime.LocalRootCertificate().CertificatePEM()) {
		t.Fatal("invalid fixture CA")
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	for _, test := range []struct {
		name, method, path, body string
		allow                    bool
	}{
		{"exact", "GET", "/backend-api/wham/config/bundle", "", true},
		{"query", "GET", "/backend-api/wham/config/bundle?workspace=B", "", false},
		{"suffix", "GET", "/backend-api/wham/config/bundle/other", "", false},
		{"POST", "POST", "/backend-api/wham/config/bundle", "", false},
		{"body", "GET", "/backend-api/wham/config/bundle", `{"prompt":"must stay local"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(test.method, "https://chatgpt.com"+test.path, strings.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer synthetic-A")
			request.Header.Set("Chatgpt-Account-Id", "workspace-A")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
			if err != nil {
				t.Fatal(err)
			}
			if (response.StatusCode == http.StatusOK) != test.allow {
				t.Fatalf("bundle scope status=%d allowed=%v", response.StatusCode, test.allow)
			}
			if test.allow && !strings.Contains(string(body), "workspace-A-policy") {
				t.Fatal("original nonempty workspace policy was replaced")
			}
		})
	}
	original.mu.Lock()
	defer original.mu.Unlock()
	if original.bundles != 1 {
		t.Fatalf("disallowed bundle request reached original transport: calls=%d", original.bundles)
	}
	f.wire.mu.Lock()
	defer f.wire.mu.Unlock()
	if len(f.wire.requests) != 0 {
		t.Fatal("workspace policy was dispatched to a managed account")
	}
}
