package egressnetwork

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDoHLookupHasATotalResponseBodyDeadline(t *testing.T) {
	releaseBody := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/dns-message")
		writer.WriteHeader(http.StatusOK)
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		<-releaseBody
	}))
	defer server.Close()
	defer close(releaseBody)

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	resolver, err := newDoHResolver(
		server.URL+"/dns-query",
		&net.Dialer{},
		&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
	)
	if err != nil {
		t.Fatalf("newDoHResolver() error = %v", err)
	}
	if resolver.client.Timeout <= 0 {
		t.Fatal("DoH HTTP client has no total timeout")
	}

	resolver.client.Timeout = 75 * time.Millisecond
	result := make(chan error, 1)
	go func() {
		_, lookupErr := resolver.LookupNetIP(context.Background(), "ip4", "example.test")
		result <- lookupErr
	}()

	select {
	case lookupErr := <-result:
		if lookupErr == nil {
			t.Fatal("LookupNetIP() error = nil, want total-timeout failure")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("LookupNetIP() did not stop while the DoH response body stalled")
	}
}
