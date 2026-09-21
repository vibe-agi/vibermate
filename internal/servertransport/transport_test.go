package servertransport

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/serveridentity"
)

func TestTransportUsesSystemRootTrustWithoutCreatingLeafPin(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	identity, err := serveridentity.Open(
		context.Background(),
		filepath.Join(t.TempDir(), "identity"),
		rand.Reader,
		now,
		"127.0.0.1",
	)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := identity.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		NextProtos:   []string{"http/1.1"},
	}
	server.StartTLS()
	defer server.Close()
	target, err := serverconnection.ParseTarget(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	transport, err := Open(Options{
		Target: target, TrustDirectory: filepath.Join(t.TempDir(), "trust"),
		Clock: fixedClock{now: now}, Timeout: 5 * time.Second, RootCAs: roots,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		server.URL,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	response.Body.Close()
	firstUse, fingerprint := transport.Trust()
	if firstUse || fingerprint == "" ||
		transport.TrustMode() != serverconnection.TrustModeSystemRoots {
		t.Fatalf(
			"trust = firstUse:%v fingerprint:%q mode:%q",
			firstUse,
			fingerprint,
			transport.TrustMode(),
		)
	}
	probed, err := ProbeSystemTrust(
		context.Background(),
		SystemTrustProbeOptions{
			Target: target, Clock: fixedClock{now: now},
			Timeout: 5 * time.Second, RootCAs: roots,
		},
	)
	if err != nil || probed != fingerprint {
		t.Fatalf("ProbeSystemTrust() = %q, %v; want %q", probed, err, fingerprint)
	}
}

func TestTransportConfinesHTTPAndStreamsToTheSelectedRuntimeServer(t *testing.T) {
	t.Parallel()
	selected := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/status" {
			http.NotFound(writer, request)
			return
		}
		_, _ = io.WriteString(writer, "selected")
	}))
	defer selected.Close()
	escaped := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		t.Error("request escaped to an unselected Runtime Server")
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer escaped.Close()
	target, err := serverconnection.ParseTarget(selected.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := Open(Options{
		Target:         target,
		TrustDirectory: filepath.Join(t.TempDir(), "trust"),
		Clock:          fixedClock{now: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)},
		Timeout:        5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer transport.Close()

	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, selected.URL+"/status", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.Do(request)
	if err != nil {
		t.Fatalf("Do(selected) error = %v", err)
	}
	response.Body.Close()

	escapeRequest, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, escaped.URL+"/status", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Do(escapeRequest); err == nil ||
		!strings.Contains(err.Error(), "selected Runtime Server") {
		t.Fatalf("Do(unselected) error = %v", err)
	}

	connection, err := transport.Dial(context.Background())
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	_ = connection.Close()
}

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }
