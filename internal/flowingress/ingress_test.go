package flowingress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type connectRecord struct {
	target        string
	authorization string
	payload       []byte
}

// fakeProxy accepts one CONNECT, records it, answers 200 plus an optional
// banner, then records every tunneled byte until the client half-closes.
func fakeProxy(t *testing.T, banner string) (Proxy, <-chan connectRecord) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	records := make(chan connectRecord, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		request, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n"+banner)
		payload, _ := io.ReadAll(reader)
		records <- connectRecord{request.Host, request.Header.Get("Proxy-Authorization"), payload}
	}()
	return Proxy{Origin: "http://" + listener.Addr().String(), Token: "run-token"}, records
}

func runFlow(t *testing.T, proxy Proxy, header Header, payload []byte) (connectRecord, []byte) {
	t.Helper()
	ingress, err := New(func() (Proxy, error) { return proxy, nil })
	if err != nil {
		t.Fatal(err)
	}
	client, server := tcpPair(t)
	done := make(chan error, 1)
	go func() { done <- ingress.Serve(context.Background(), server) }()
	if err := WriteHeader(client, header); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = client.(*net.TCPConn).CloseWrite()
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	received, _ := io.ReadAll(client)
	client.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ingress did not finish")
	}
	return connectRecord{}, received
}

// tcpPair returns a connected loopback pair; unlike net.Pipe, writes are
// buffered and either side can half-close.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

func wantRecord(t *testing.T, records <-chan connectRecord, target string, payload []byte) {
	t.Helper()
	select {
	case got := <-records:
		if got.target != target {
			t.Fatalf("CONNECT target = %q, want %q", got.target, target)
		}
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("capture:run-token"))
		if got.authorization != want {
			t.Fatalf("Proxy-Authorization = %q, want %q", got.authorization, want)
		}
		if !bytes.Equal(got.payload, payload) {
			t.Fatalf("tunneled bytes = %q, want %q", got.payload, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxy saw no CONNECT")
	}
}

func clientHello(t *testing.T, serverName string) []byte {
	t.Helper()
	recorder := &recordingConn{}
	_ = tls.Client(recorder, &tls.Config{ServerName: serverName, InsecureSkipVerify: serverName == ""}).Handshake()
	if len(recorder.written) == 0 {
		t.Fatal("no ClientHello recorded")
	}
	return recorder.written
}

type recordingConn struct {
	replayConn
	written []byte
}

func (c *recordingConn) Write(p []byte) (int, error) {
	c.written = append(c.written, p...)
	return len(p), nil
}
func (c *recordingConn) Read([]byte) (int, error) { return 0, io.EOF }

func TestIngressNamesDestination(t *testing.T) {
	hello := clientHello(t, "API.Anthropic.com")
	for _, test := range []struct {
		name    string
		header  Header
		payload []byte
		target  string
	}{
		{
			name:    "platform host name wins",
			header:  Header{Version: 1, Protocol: "tcp", Destination: netip.MustParseAddrPort("203.0.113.7:443"), Host: "chatgpt.com"},
			payload: hello,
			target:  "chatgpt.com:443",
		},
		{
			name:    "TLS SNI",
			header:  Header{Version: 1, Protocol: "tcp", Destination: netip.MustParseAddrPort("203.0.113.7:443")},
			payload: hello,
			target:  "api.anthropic.com:443",
		},
		{
			name:    "HTTP Host keeps the original port",
			header:  Header{Version: 1, Protocol: "tcp", Destination: netip.MustParseAddrPort("203.0.113.7:8080")},
			payload: []byte("GET / HTTP/1.1\r\nHost: Example.COM:8080\r\n\r\n"),
			target:  "example.com:8080",
		},
		{
			name:    "opaque bytes fall back to the IP",
			header:  Header{Version: 1, Protocol: "tcp", Destination: netip.MustParseAddrPort("203.0.113.7:22")},
			payload: []byte("SSH-2.0-OpenSSH_9.8\r\n"),
			target:  "203.0.113.7:22",
		},
		{
			name:    "IPv6 fallback is bracketed",
			header:  Header{Version: 1, Protocol: "tcp", Destination: netip.MustParseAddrPort("[2001:db8::1]:443")},
			payload: []byte{0x00, 0x01, 0x02},
			target:  "[2001:db8::1]:443",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy, records := fakeProxy(t, "banner")
			_, received := runFlow(t, proxy, test.header, test.payload)
			if string(received) != "banner" {
				t.Fatalf("client received %q, want the proxy banner sent right after 200", received)
			}
			wantRecord(t, records, test.target, test.payload)
		})
	}
}

func TestSNIWithoutServerNameFallsBackToIP(t *testing.T) {
	proxy, records := fakeProxy(t, "x")
	hello := clientHello(t, "")
	header := Header{Version: 1, Protocol: "tcp", Destination: netip.MustParseAddrPort("198.51.100.4:443")}
	runFlow(t, proxy, header, hello)
	wantRecord(t, records, "198.51.100.4:443", hello)
}

func TestReadHeaderRejectsWhatItDoesNotModel(t *testing.T) {
	frame := func(body string) []byte {
		return append([]byte{byte(len(body) >> 8), byte(len(body))}, body...)
	}
	for _, body := range []string{
		`{"v":2,"proto":"tcp","dst":"203.0.113.7:443"}`,
		`{"v":1,"proto":"udp","dst":"203.0.113.7:443"}`,
		`{"v":1,"proto":"tcp","dst":"203.0.113.7:0"}`,
		`{"v":1,"proto":"tcp","dst":"203.0.113.7:443","host":"203.0.113.8"}`,
		`{"v":1,"proto":"tcp","dst":"203.0.113.7:443","host":"Upper.example"}`,
		`{"v":1,"proto":"tcp","dst":"203.0.113.7:443","run":"other"}`,
		`{"v":1,"proto":"tcp","dst":"203.0.113.7:443"}{}`,
	} {
		if _, err := ReadHeader(bytes.NewReader(frame(body))); err == nil {
			t.Errorf("ReadHeader(%s) succeeded", body)
		}
	}
	if _, err := ReadHeader(bytes.NewReader([]byte{0x04, 0x01})); err == nil {
		t.Error("oversized frame accepted")
	}
	header := Header{Version: 1, Protocol: "tcp", Destination: netip.MustParseAddrPort("203.0.113.7:443"), Host: "api.openai.com"}
	var buffer bytes.Buffer
	if err := WriteHeader(&buffer, header); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadHeader(&buffer); err != nil || got != header {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
}

func TestIngressReportsProxyRefusal(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = http.ReadRequest(bufio.NewReader(conn))
		_, _ = io.WriteString(conn, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
	}()
	ingress, _ := New(func() (Proxy, error) { return Proxy{Origin: "http://" + listener.Addr().String(), Token: "bad"}, nil })
	client, server := tcpPair(t)
	if err := WriteHeader(client, Header{Version: 1, Protocol: "tcp", Destination: netip.MustParseAddrPort("203.0.113.7:443"), Host: "api.openai.com"}); err != nil {
		t.Fatal(err)
	}
	err = ingress.Serve(context.Background(), server)
	if err == nil || !strings.Contains(err.Error(), "407") {
		t.Fatalf("Serve() error = %v, want the 407 refusal", err)
	}
}
