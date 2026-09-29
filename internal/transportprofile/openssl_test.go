package transportprofile

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestFollowClientOpenSSLHelloReachesStrictTLSUpstream(t *testing.T) {
	// Captured from Node v25.8.1 / OpenSSL 3.6.4 against a loopback listener,
	// default TLS settings, SNI agent.example and ALPN http/1.1. Public random
	// and key-share bytes are fixture data, never reused for upstream state.
	encoded, err := os.ReadFile("testdata/node-openssl-clienthello.hex")
	if err != nil {
		t.Fatal(err)
	}
	hello, err := hex.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	client, captured := net.Pipe()
	defer client.Close()
	defer captured.Close()
	go func() { _, _ = client.Write(hello) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observed, _, err := CaptureClientHello(ctx, captured, DefaultMaxClientHelloBytes)
	if err != nil || !slices.Contains(observed.ExtensionOrder(), uint16(22)) {
		t.Fatalf("OpenSSL fixture must include encrypt_then_mac: %v", err)
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			t.Errorf("upstream request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	for _, test := range []struct {
		name       string
		serverName string
		roots      *x509.CertPool
	}{
		{name: "untrusted_ca", serverName: "example.com", roots: x509.NewCertPool()},
		{name: "wrong_hostname", serverName: "wrong.invalid", roots: roots},
	} {
		t.Run(test.name, func(t *testing.T) {
			connector, err := NewConnector(ConnectorOptions{
				Dialer: &net.Dialer{}, RootCAs: test.roots, HandshakeTimeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			connection, _, err := connector.Connect(ctx, ConnectRequest{
				Network: "tcp", Address: upstream.Listener.Addr().String(), TLSServerName: test.serverName,
				Plan: testTransportPlan(t), Observation: observed,
			})
			if connection != nil {
				connection.Close()
			}
			var unknownCA x509.UnknownAuthorityError
			var wrongHost x509.HostnameError
			if connection != nil || (!errors.As(err, &unknownCA) && !errors.As(err, &wrongHost)) {
				t.Fatalf("fingerprint fallback bypassed TLS verification: %v", err)
			}
		})
	}
	connector, err := NewConnector(ConnectorOptions{
		Dialer: &net.Dialer{}, RootCAs: roots, HandshakeTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, evidence, err := connector.Connect(ctx, ConnectRequest{
		Network: "tcp", Address: upstream.Listener.Addr().String(), TLSServerName: "example.com",
		Plan: testTransportPlan(t), Observation: observed,
	})
	if err != nil {
		t.Fatalf("default Original Destination TLS rejected OpenSSL: %v", err)
	}
	defer connection.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/v1/responses", strings.NewReader(`{"input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := request.Write(connection); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(body) != `{"ok":true}` {
		t.Fatalf("upstream response = %d %s, %v", response.StatusCode, body, err)
	}
	if !evidence.UsedFallback() || evidence.Effective().Source != wireprofile.TransportFingerprintStandard ||
		evidence.FallbackReason() != FallbackClientHelloUnsupported {
		t.Fatalf("unsupported fingerprint must report strict standard TLS fallback: %+v", evidence)
	}
}
