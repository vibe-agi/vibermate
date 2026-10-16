package loopbackproxy_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clienttarget"
)

func TestCapturedHTTPDestinationIndependentOfProxyTLS(t *testing.T) {
	observer := &rawObserver{}
	f := newProxyFixtureWithRawEvidence(t, observer)
	defer f.Close(t)
	grant, err := f.runs.Create(context.Background(), capturerun.CreateCommand{
		CWD: t.TempDir(), CanonicalExecutablePath: "/tmp/synthetic-claude", ExecutableLabel: "claude",
		Lifetime: 5 * time.Minute, CatalogRevision: 1, Recognition: fixtureRecognition(nil), Workspace: proxyWorkspaceScope(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	capture, err := captureidentity.New(captureidentity.KindManagedRun, grant.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := clienttarget.NewProfile("claude-code", clienttarget.EnvironmentFacts{AnthropicBaseURL: "http://127.0.0.1:43431"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.assignments.Create(context.Background(), captureassignment.CreateCommand{
		Capture: capture, EnvironmentID: f.environment.ID, Source: captureassignment.SourceLaunch, ClientProfile: profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	secured := httptest.NewTLSServer(f.handler)
	defer secured.Close()
	roots := x509.NewCertPool()
	roots.AddCert(secured.Certificate())
	for _, outerTLS := range []bool{false, true} {
		var conn net.Conn
		if outerTLS {
			conn, err = tls.Dial("tcp", strings.TrimPrefix(secured.URL, "https://"), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
		} else {
			conn, err = net.Dial("tcp", f.listener.Addr().String())
		}
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		body := `{"model":"client"}`
		auth := base64.StdEncoding.EncodeToString([]byte("capture:" + grant.ProxyCapability.Value()))
		_, err = fmt.Fprintf(conn, "POST http://127.0.0.1:43431/v1/messages HTTP/1.1\r\nHost: 127.0.0.1:43431\r\nContent-Type: application/json\r\nProxy-Authorization: Basic %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", auth, len(body), body)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Errorf("captured cleartext target through outerTLS=%v rejected: status=%d body=%s", outerTLS, response.StatusCode, data)
		}
	}
	observations := observer.snapshot()
	if len(observations) != 4 {
		t.Fatalf("raw observation count = %d, want 4", len(observations))
	}
	for _, observation := range observations {
		if observation.Scheme != "http" {
			t.Errorf("HTTP target recorded with scheme %q", observation.Scheme)
		}
	}
}

func TestCapturedHTTPDefaultPortAuthority(t *testing.T) {
	f := newProxyFixture(t)
	defer f.Close(t)
	grant, err := f.runs.Create(context.Background(), capturerun.CreateCommand{
		CWD: t.TempDir(), CanonicalExecutablePath: "/tmp/synthetic-claude", ExecutableLabel: "claude",
		Lifetime: 5 * time.Minute, CatalogRevision: 1, Recognition: fixtureRecognition(nil), Workspace: proxyWorkspaceScope(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	capture, err := captureidentity.New(captureidentity.KindManagedRun, grant.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := clienttarget.NewProfile("claude-code", clienttarget.EnvironmentFacts{AnthropicBaseURL: "http://127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.assignments.Create(context.Background(), captureassignment.CreateCommand{
		Capture: capture, EnvironmentID: f.environment.ID, Source: captureassignment.SourceLaunch, ClientProfile: profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"127.0.0.1:80", "127.0.0.1"} {
		conn, err := net.Dial("tcp", f.listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		body := `{"model":"client"}`
		auth := base64.StdEncoding.EncodeToString([]byte("capture:" + grant.ProxyCapability.Value()))
		_, err = fmt.Fprintf(conn, "POST http://%s/v1/messages HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nProxy-Authorization: Basic %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", host, host, auth, len(body), body)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Errorf("captured cleartext target rejected ordinary default-port authority: status=%d body=%s", response.StatusCode, data)
		}
	}
}

type optimisticConnectConn struct {
	net.Conn
	prefix      string
	reader      *bufio.Reader
	established bool
}

func (c *optimisticConnectConn) Write(p []byte) (int, error) {
	if c.prefix == "" {
		return c.Conn.Write(p)
	}
	prefix := c.prefix
	c.prefix = ""
	n, err := c.Conn.Write(append([]byte(prefix), p...))
	n -= len(prefix)
	if n < 0 {
		n = 0
	}
	return n, err
}
func (c *optimisticConnectConn) Read(p []byte) (int, error) {
	if !c.established {
		c.established = true
		response, err := http.ReadResponse(c.reader, &http.Request{Method: http.MethodConnect})
		if err != nil {
			return 0, err
		}
		if response.StatusCode != 200 {
			return 0, fmt.Errorf("CONNECT status %d", response.StatusCode)
		}
	}
	return c.reader.Read(p)
}

func TestConnectPreservesBufferedClientHello(t *testing.T) {
	f := newProxyFixture(t)
	defer f.Close(t)
	proxy := httptest.NewTLSServer(f.handler)
	defer proxy.Close()
	proxyRoots := x509.NewCertPool()
	proxyRoots.AddCert(proxy.Certificate())
	destinationRoots := x509.NewCertPool()
	if !destinationRoots.AppendCertsFromPEM(f.authority.Certificate().CertificatePEM()) {
		t.Fatal("append Proxy CA")
	}
	for _, outerTLS := range []bool{false, true} {
		for _, coalesced := range []bool{false, true} {
			t.Run(fmt.Sprintf("outerTLS=%v/coalesced=%v", outerTLS, coalesced), func(t *testing.T) {
				var connection net.Conn
				var err error
				if outerTLS {
					connection, err = tls.Dial("tcp", strings.TrimPrefix(proxy.URL, "https://"), &tls.Config{RootCAs: proxyRoots, MinVersion: tls.VersionTLS12})
				} else {
					connection, err = net.Dial("tcp", f.listener.Addr().String())
				}
				if err != nil {
					t.Fatal(err)
				}
				defer connection.Close()
				if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
					t.Fatal(err)
				}
				credentials := base64.StdEncoding.EncodeToString([]byte("capture:" + f.grant.ProxyCapability.Value()))
				prefix := "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\nProxy-Authorization: Basic " + credentials + "\r\n\r\n"
				wrapped := &optimisticConnectConn{Conn: connection, reader: bufio.NewReader(connection)}
				if coalesced {
					wrapped.prefix = prefix
				} else {
					if _, err := io.WriteString(connection, prefix); err != nil {
						t.Fatal(err)
					}
					response, err := http.ReadResponse(wrapped.reader, &http.Request{Method: http.MethodConnect})
					if err != nil {
						t.Fatal(err)
					}
					if response.StatusCode != http.StatusOK {
						t.Fatalf("CONNECT status = %d", response.StatusCode)
					}
					wrapped.established = true
				}
				secured := tls.Client(wrapped, &tls.Config{RootCAs: destinationRoots, ServerName: "api.anthropic.com", MinVersion: tls.VersionTLS12})
				defer secured.Close()
				if err := secured.Handshake(); err != nil {
					t.Fatalf("TLS handshake after CONNECT: %v", err)
				}
				for requestNumber := 1; requestNumber <= 2; requestNumber++ {
					const body = `{"model":"client"}`
					response := writeInnerRequest(t, secured, &http.Request{
						Method: http.MethodPost, URL: mustURL(t, "/v1/messages"), Host: "api.anthropic.com",
						Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Close: requestNumber == 2,
					})
					payload, err := io.ReadAll(response.Body)
					response.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
					if response.StatusCode != http.StatusOK || string(payload) != `{"result":"proxied"}` {
						t.Errorf("request %d captured response status=%d body=%s", requestNumber, response.StatusCode, payload)
					}
				}
			})
		}
	}
}

func TestBlindConnectPreservesBufferedPayload(t *testing.T) {
	f := newProxyFixture(t)
	defer f.Close(t)
	authority, stop := echoTarget(t)
	defer stop()
	connection, err := net.Dial("tcp", f.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	credentials := base64.StdEncoding.EncodeToString([]byte("capture:" + f.grant.ProxyCapability.Value()))
	request := "CONNECT " + authority + " HTTP/1.1\r\nHost: " + authority + "\r\nProxy-Authorization: Basic " + credentials + "\r\n\r\nopaque-bytes"
	if _, err := io.WriteString(connection, request); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d", response.StatusCode)
	}
	echoed := make([]byte, len("echo:opaque-bytes"))
	if _, err := io.ReadFull(reader, echoed); err != nil {
		t.Fatalf("coalesced tunnel payload lost: %v", err)
	}
	if string(echoed) != "echo:opaque-bytes" {
		t.Fatalf("tunnel payload = %q", echoed)
	}
}

func TestDefaultPortMatchingRejectsMalformedAuthorities(t *testing.T) {
	f := newProxyFixture(t)
	defer f.Close(t)
	for _, authority := range []string{"[api.anthropic.com]", "api.anthropic.com:", "api.anthropic.com:0", "api.anthropic.com:65536", "api.anthropic.com:bad", "api.anthropic.com.evil.test"} {
		t.Run(authority, func(t *testing.T) {
			connection := f.ConnectTLS(t, f.grant.ProxyCapability.Value(), "api.anthropic.com:443", "api.anthropic.com")
			defer connection.Close()
			const body = `{"model":"client"}`
			if _, err := fmt.Fprintf(connection, "POST /v1/messages HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", authority, len(body), body); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodPost})
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusMisdirectedRequest {
				t.Errorf("invalid authority %q status = %d, want 421", authority, response.StatusCode)
			}
		})
	}
	if requests := f.exchanges.Requests(); len(requests) != 0 {
		t.Errorf("invalid authorities reached exchange executor: %d requests", len(requests))
	}
}

func TestRejectsHTTPTargetInHTTPSBinding(t *testing.T) {
	f := newProxyFixture(t)
	defer f.Close(t)
	connection := f.ConnectTLS(t, f.grant.ProxyCapability.Value(), "api.anthropic.com:443", "api.anthropic.com")
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	const body = `{"model":"client"}`
	if _, err := fmt.Fprintf(connection, "POST http://api.anthropic.com:443/v1/messages HTTP/1.1\r\nHost: api.anthropic.com:443\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("absolute HTTP target inside frozen HTTPS tunnel status=%d body=%s, want 421", response.StatusCode, payload)
	}
	if requests := f.exchanges.Requests(); len(requests) != 0 {
		t.Errorf("HTTP target reached HTTPS exchange executor: %d requests", len(requests))
	}
}
