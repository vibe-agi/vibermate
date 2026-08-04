package wirecapture

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"golang.org/x/net/http2"
)

const (
	testAuthorization = "Bearer vm-wire-capture-secret-canary"
	testCookie        = "vm-wire-capture-cookie-canary"
	testSession       = "vm-wire-capture-session-canary"
	testBody          = "vm-wire-capture-body-canary"
)

func TestCaptureHTTP1DropsSensitiveValuesAndWritesPrivateReport(t *testing.T) {
	t.Parallel()

	listener, serverTLS, clientTLS := testListenerAndTLS(t)
	clientTLS.NextProtos = []string{"http/1.1"}
	reportResult := make(chan captureTestResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		report, err := Capture(ctx, listener, Options{
			TLSConfig:   serverTLS,
			SampleLimit: 1,
		})
		reportResult <- captureTestResult{report: report, err: err}
	}()

	connection := testUTLSConnection(t, listener.Addr().String(), clientTLS, utls.HelloGolang)
	body := testBody
	request := strings.Join([]string{
		"POST /private/path?thread=" + testSession + " HTTP/1.1",
		"Host: capture.example",
		"User-Agent: claude-cli/test",
		"Authorization: " + testAuthorization,
		"Cookie: session=" + testCookie,
		"X-Session-ID: " + testSession,
		"Content-Type: application/json",
		"Content-Length: " + stringInt(len(body)),
		"",
		body,
	}, "\r\n")
	if _, err := io.WriteString(connection, request); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 256)
	if _, err := connection.Read(response); err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()

	result := <-reportResult
	if result.err != nil {
		t.Fatal(result.err)
	}
	assertSafeReport(t, result.report, "http/1.1", "claude-cli/test")
	if got := result.report.Samples[0].BodyBytes; got != uint64(len(body)) {
		t.Fatalf("BodyBytes = %d", got)
	}
	if result.report.Samples[0].HTTP2 != nil {
		t.Fatal("HTTP/1.1 sample contains HTTP/2 state")
	}

	directory := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "capture.json")
	if err := WriteReport(path, result.report); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("report permissions = %#o", info.Mode().Perm())
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSensitiveValues(t, stored)
}

func TestCaptureHTTP2PreservesSettingsAndHeaderOrderWithoutValues(t *testing.T) {
	t.Parallel()

	listener, serverTLS, clientTLS := testListenerAndTLS(t)
	reportResult := make(chan captureTestResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		report, err := Capture(ctx, listener, Options{
			TLSConfig:   serverTLS,
			SampleLimit: 1,
		})
		reportResult <- captureTestResult{report: report, err: err}
	}()

	connection := testUTLSConnection(t, listener.Addr().String(), clientTLS, utls.HelloChrome_Auto)
	transport := &http2.Transport{}
	client, err := transport.NewClientConn(connection)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://capture.example/private?thread="+testSession,
		strings.NewReader(testBody),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("User-Agent", "codex_exec/test")
	request.Header.Set("Authorization", testAuthorization)
	request.Header.Set("Cookie", "session="+testCookie)
	request.Header.Set("X-Session-ID", testSession)
	response, err := client.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	_ = connection.Close()

	result := <-reportResult
	if result.err != nil {
		t.Fatal(result.err)
	}
	assertSafeReport(t, result.report, "h2", "codex_exec/test")
	sample := result.report.Samples[0]
	if sample.HTTP2 == nil || len(sample.HTTP2.Settings) == 0 ||
		sample.HTTP2.AkamaiFingerprint == "" || sample.HTTP2.AkamaiHash == "" {
		t.Fatalf("HTTP/2 observation = %+v", sample.HTTP2)
	}
	if len(sample.PseudoHeaderOrder) != 4 || len(sample.HeaderOrder) == 0 {
		t.Fatalf("HTTP/2 header order = %v / %v", sample.PseudoHeaderOrder, sample.HeaderOrder)
	}
}

func TestCaptureRejectsNonLoopbackAndPublicOutputDirectory(t *testing.T) {
	t.Parallel()

	_, serverTLS, _ := testListenerAndTLS(t)
	fake := &fakeListener{address: &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 443}}
	if _, err := Capture(context.Background(), fake, Options{
		TLSConfig:   serverTLS,
		SampleLimit: 1,
	}); err == nil {
		t.Fatal("Capture accepted a non-loopback listener")
	}

	directory := filepath.Join(t.TempDir(), "public")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	report := Report{
		SchemaVersion: ReportSchema,
		CreatedAt:     time.Now().UTC(),
		Samples: []Sample{{
			CapturedAt:     time.Now().UTC(),
			NegotiatedALPN: "http/1.1",
			TLS:            testFingerprint(),
		}},
	}
	if err := WriteReport(filepath.Join(directory, "capture.json"), report); err == nil {
		t.Fatal("WriteReport accepted a non-private directory")
	}
}

type captureTestResult struct {
	report Report
	err    error
}

func assertSafeReport(t *testing.T, report Report, protocol, userAgent string) {
	t.Helper()
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(report.Samples) != 1 || report.Samples[0].NegotiatedALPN != protocol ||
		report.Samples[0].UserAgent != userAgent {
		t.Fatalf("report = %+v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSensitiveValues(t, encoded)
}

func assertNoSensitiveValues(t *testing.T, encoded []byte) {
	t.Helper()
	for _, canary := range []string{
		testAuthorization,
		testCookie,
		testSession,
		testBody,
		"/private/path",
	} {
		if bytes.Contains(encoded, []byte(canary)) {
			t.Fatalf("wire capture report retained sensitive canary %q", canary)
		}
	}
}

func testListenerAndTLS(t *testing.T) (net.Listener, *tls.Config, *utls.Config) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	certificate, roots := testCertificate(t)
	return listener, &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		}, &utls.Config{
			RootCAs:    roots,
			ServerName: "capture.example",
			MinVersion: utls.VersionTLS12,
			NextProtos: []string{"h2", "http/1.1"},
		}
}

func testUTLSConnection(
	t *testing.T,
	address string,
	config *utls.Config,
	hello utls.ClientHelloID,
) *utls.UConn {
	t.Helper()
	raw, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	connection := utls.UClient(raw, config.Clone(), hello)
	if err := connection.Handshake(); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	return connection
}

func testCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "capture.example"},
		DNSNames:     []string{"capture.example"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	certificateRecord, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(certificateRecord)
	return certificate, roots
}

func stringInt(value int) string {
	return strconv.Itoa(value)
}

func testFingerprint() transportprofile.Fingerprint {
	return transportprofile.Fingerprint{
		JA3:           "771,4865,0,,",
		JA3Hash:       "00000000000000000000000000000000",
		JA4:           "t13d0101h1h1_000000000000_000000000000",
		JA4R:          "t13d0101h1h1_1301_0000_",
		Peetprint:     "772|1.1|||||4865|0",
		PeetprintHash: "00000000000000000000000000000000",
	}
}

type fakeListener struct {
	address net.Addr
}

func (*fakeListener) Accept() (net.Conn, error) { return nil, io.EOF }
func (*fakeListener) Close() error              { return nil }
func (listener *fakeListener) Addr() net.Addr   { return listener.address }
