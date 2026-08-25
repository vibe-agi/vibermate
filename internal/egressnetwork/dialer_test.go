package egressnetwork_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"golang.org/x/net/dns/dnsmessage"
)

func TestDirectSystemDNSConnectsWithoutViberMateResolutionOrSOCKS(t *testing.T) {
	t.Parallel()

	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer target.Close()
	accepted := make(chan error, 1)
	go func() {
		connection, acceptErr := target.Accept()
		if acceptErr == nil {
			_ = connection.Close()
		}
		accepted <- acceptErr
	}()

	resolver := &recordingResolver{addresses: []netip.Addr{
		netip.MustParseAddr("203.0.113.41"),
	}}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{
		SystemResolver: resolver,
	})
	if err != nil {
		t.Fatalf("NewBuilder(): %v", err)
	}
	dialer, err := builder.Dialer(egressnetwork.DefaultPolicy())
	if err != nil {
		t.Fatalf("Dialer(): %v", err)
	}
	port := target.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection, err := dialer.DialContext(
		ctx,
		"tcp4",
		net.JoinHostPort("localhost", strconv.Itoa(port)),
	)
	if err != nil {
		t.Fatalf("DialContext(): %v", err)
	}
	_ = connection.Close()
	if calls := resolver.callCount(); calls != 0 {
		t.Fatalf("ViberMate resolver calls = %d, want 0 for direct system DNS", calls)
	}
	select {
	case acceptErr := <-accepted:
		if acceptErr != nil {
			t.Fatalf("accept direct target: %v", acceptErr)
		}
	case <-ctx.Done():
		t.Fatal("direct target was not reached")
	}
}

func TestSOCKS5ResolvesTargetBeforeConnectingThroughProxy(t *testing.T) {
	t.Parallel()

	proxy := newSOCKSServer(t, false)
	resolver := &recordingResolver{addresses: []netip.Addr{
		netip.MustParseAddr("203.0.113.41"),
	}}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{
		SystemResolver: resolver,
	})
	if err != nil {
		t.Fatalf("NewBuilder(): %v", err)
	}

	socks5, err := builder.Dialer(egressnetwork.Policy{
		Proxy: egressnetwork.ProxyPolicy{
			Kind:     egressnetwork.ProxySOCKS5,
			Endpoint: proxy.address(),
		},
		Resolver: egressnetwork.ResolverPolicy{Kind: egressnetwork.ResolverSystem},
	})
	if err != nil {
		t.Fatalf("SOCKS5 Dialer(): %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection, err := socks5.DialContext(ctx, "tcp", "model.example:443")
	if err != nil {
		t.Fatalf("SOCKS5 DialContext(): %v", err)
	}
	_ = connection.Close()
	first := proxy.nextTarget(t)
	if first != "203.0.113.41:443" {
		t.Fatalf("SOCKS5 proxy target = %q, want locally resolved IP", first)
	}
	if calls := resolver.callCount(); calls != 1 {
		t.Fatalf("system resolver calls = %d, want 1", calls)
	}
}

func TestDoHResolutionDialsTheReturnedAddressWithoutChangingAuthority(t *testing.T) {
	t.Parallel()

	doh := newDoHServer(t, netip.MustParseAddr("127.0.0.1"))
	systemResolver := &recordingResolver{addresses: []netip.Addr{
		netip.MustParseAddr("192.0.2.99"),
	}}
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer target.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := target.Accept()
		if acceptErr == nil {
			accepted <- connection
		}
	}()

	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{
		TLSClientConfig: doh.clientTLSConfig(),
		SystemResolver:  systemResolver,
	})
	if err != nil {
		t.Fatalf("NewBuilder(): %v", err)
	}
	dialer, err := builder.Dialer(egressnetwork.Policy{
		Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxyDirect},
		Resolver: egressnetwork.ResolverPolicy{
			Kind:      egressnetwork.ResolverDoH,
			DoHURL:    doh.url(),
			Transport: egressnetwork.ResolverTransportDirect,
		},
	})
	if err != nil {
		t.Fatalf("Dialer(): %v", err)
	}
	port := target.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection, err := dialer.DialContext(
		ctx,
		"tcp4",
		net.JoinHostPort("ai.internal.example", strconv.Itoa(port)),
	)
	if err != nil {
		t.Fatalf("DialContext(): %v", err)
	}
	_ = connection.Close()
	select {
	case acceptedConnection := <-accepted:
		_ = acceptedConnection.Close()
	case <-ctx.Done():
		t.Fatal("resolved target was not dialed")
	}
	if names := doh.names(); len(names) != 1 || names[0] != "ai.internal.example." {
		t.Fatalf("DoH questions = %#v", names)
	}
	if calls := systemResolver.callCount(); calls != 0 {
		t.Fatalf("system resolver calls = %d, want no DoH bootstrap lookup for an IP URL", calls)
	}
}

func TestDoHTransportCanUseTheConfiguredSOCKS5Proxy(t *testing.T) {
	t.Parallel()

	doh := newDoHServer(t, netip.MustParseAddr("127.0.0.1"))
	proxy := newSOCKSServer(t, true)
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer target.Close()
	go func() {
		connection, acceptErr := target.Accept()
		if acceptErr == nil {
			_ = connection.Close()
		}
	}()

	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{
		TLSClientConfig: doh.clientTLSConfig(),
	})
	if err != nil {
		t.Fatalf("NewBuilder(): %v", err)
	}
	dialer, err := builder.Dialer(egressnetwork.Policy{
		Proxy: egressnetwork.ProxyPolicy{
			Kind:     egressnetwork.ProxySOCKS5,
			Endpoint: proxy.address(),
		},
		Resolver: egressnetwork.ResolverPolicy{
			Kind:      egressnetwork.ResolverDoH,
			DoHURL:    doh.url(),
			Transport: egressnetwork.ResolverTransportProxy,
		},
	})
	if err != nil {
		t.Fatalf("Dialer(): %v", err)
	}
	port := target.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection, err := dialer.DialContext(
		ctx,
		"tcp4",
		net.JoinHostPort("ai.internal.example", strconv.Itoa(port)),
	)
	if err != nil {
		t.Fatalf("DialContext(): %v", err)
	}
	_ = connection.Close()
	first := proxy.nextTarget(t)
	second := proxy.nextTarget(t)
	dohPort := doh.server.Listener.Addr().(*net.TCPAddr).Port
	if first != net.JoinHostPort("127.0.0.1", strconv.Itoa(dohPort)) {
		t.Fatalf("first proxy target = %q, want DoH server", first)
	}
	if second != net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) {
		t.Fatalf("second proxy target = %q, want resolved AI endpoint", second)
	}
}

func TestDoHCanResolveDirectlyBeforeTheAITargetUsesSOCKS5(t *testing.T) {
	t.Parallel()

	doh := newDoHServer(t, netip.MustParseAddr("127.0.0.1"))
	proxy := newSOCKSServer(t, true)
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer target.Close()
	go func() {
		connection, acceptErr := target.Accept()
		if acceptErr == nil {
			_ = connection.Close()
		}
	}()

	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{
		TLSClientConfig: doh.clientTLSConfig(),
	})
	if err != nil {
		t.Fatalf("NewBuilder(): %v", err)
	}
	dialer, err := builder.Dialer(egressnetwork.Policy{
		Proxy: egressnetwork.ProxyPolicy{
			Kind:     egressnetwork.ProxySOCKS5,
			Endpoint: proxy.address(),
		},
		Resolver: egressnetwork.ResolverPolicy{
			Kind:      egressnetwork.ResolverDoH,
			DoHURL:    doh.url(),
			Transport: egressnetwork.ResolverTransportDirect,
		},
	})
	if err != nil {
		t.Fatalf("Dialer(): %v", err)
	}
	port := target.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection, err := dialer.DialContext(
		ctx,
		"tcp4",
		net.JoinHostPort("ai.internal.example", strconv.Itoa(port)),
	)
	if err != nil {
		t.Fatalf("DialContext(): %v", err)
	}
	_ = connection.Close()
	if got := proxy.nextTarget(t); got != net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) {
		t.Fatalf("proxy target = %q, want only the resolved AI endpoint", got)
	}
	if names := doh.names(); len(names) != 1 || names[0] != "ai.internal.example." {
		t.Fatalf("DoH questions = %#v", names)
	}
}

type recordingResolver struct {
	mu        sync.Mutex
	addresses []netip.Addr
	calls     int
}

func (resolver *recordingResolver) LookupNetIP(
	_ context.Context,
	_ string,
	_ string,
) ([]netip.Addr, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.calls++
	return append([]netip.Addr(nil), resolver.addresses...), nil
}

func (resolver *recordingResolver) callCount() int {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return resolver.calls
}

type socksServer struct {
	t           *testing.T
	listener    net.Listener
	forward     bool
	targets     chan string
	closeOnce   sync.Once
	connections sync.WaitGroup
}

func newSOCKSServer(t *testing.T, forward bool) *socksServer {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen SOCKS server: %v", err)
	}
	server := &socksServer{
		t: t, listener: listener, forward: forward, targets: make(chan string, 8),
	}
	server.connections.Add(1)
	go func() {
		defer server.connections.Done()
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			server.connections.Add(1)
			go func() {
				defer server.connections.Done()
				server.handle(connection)
			}()
		}
	}()
	t.Cleanup(server.close)
	return server
}

func (server *socksServer) address() string { return server.listener.Addr().String() }

func (server *socksServer) nextTarget(t *testing.T) string {
	t.Helper()
	select {
	case target := <-server.targets:
		return target
	case <-time.After(3 * time.Second):
		t.Fatal("SOCKS target was not observed")
		return ""
	}
}

func (server *socksServer) close() {
	server.closeOnce.Do(func() {
		_ = server.listener.Close()
		server.connections.Wait()
	})
}

func (server *socksServer) handle(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	header := make([]byte, 2)
	if _, err := io.ReadFull(client, header); err != nil || header[0] != 5 {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(client, methods); err != nil {
		return
	}
	if _, err := client.Write([]byte{5, 0}); err != nil {
		return
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(client, request); err != nil || request[0] != 5 || request[1] != 1 {
		return
	}
	host, err := readSOCKSHost(client, request[3])
	if err != nil {
		return
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(client, portBytes); err != nil {
		return
	}
	port := int(binary.BigEndian.Uint16(portBytes))
	target := net.JoinHostPort(host, strconv.Itoa(port))
	server.targets <- target
	if !server.forward {
		_, _ = client.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
		return
	}
	upstream, err := net.DialTimeout("tcp", target, 3*time.Second)
	if err != nil {
		_, _ = client.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	if _, err := client.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	_ = upstream.SetDeadline(time.Time{})
	done := make(chan struct{}, 1)
	go func() {
		_, _ = io.Copy(upstream, client)
		_ = upstream.(*net.TCPConn).CloseWrite()
		done <- struct{}{}
	}()
	_, _ = io.Copy(client, upstream)
	<-done
}

func readSOCKSHost(reader io.Reader, kind byte) (string, error) {
	switch kind {
	case 1:
		value := make([]byte, net.IPv4len)
		_, err := io.ReadFull(reader, value)
		return net.IP(value).String(), err
	case 3:
		length := make([]byte, 1)
		if _, err := io.ReadFull(reader, length); err != nil {
			return "", err
		}
		value := make([]byte, int(length[0]))
		_, err := io.ReadFull(reader, value)
		return string(value), err
	case 4:
		value := make([]byte, net.IPv6len)
		_, err := io.ReadFull(reader, value)
		return net.IP(value).String(), err
	default:
		return "", errors.New("unsupported SOCKS address kind")
	}
}

type dohServer struct {
	t      *testing.T
	server *httptest.Server
	mu     sync.Mutex
	asked  []string
	answer netip.Addr
}

func newDoHServer(t *testing.T, answer netip.Addr) *dohServer {
	t.Helper()
	doh := &dohServer{t: t, answer: answer}
	doh.server = httptest.NewTLSServer(http.HandlerFunc(doh.handle))
	t.Cleanup(doh.server.Close)
	return doh
}

func (server *dohServer) url() string { return server.server.URL + "/dns-query" }

func (server *dohServer) clientTLSConfig() *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(server.server.Certificate())
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
}

func (server *dohServer) names() []string {
	server.mu.Lock()
	defer server.mu.Unlock()
	return append([]string(nil), server.asked...)
}

func (server *dohServer) handle(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/dns-message" {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, 65536))
	if err != nil {
		http.Error(writer, "invalid body", http.StatusBadRequest)
		return
	}
	var parser dnsmessage.Parser
	header, err := parser.Start(payload)
	if err != nil {
		http.Error(writer, "invalid DNS message", http.StatusBadRequest)
		return
	}
	question, err := parser.Question()
	if err != nil {
		http.Error(writer, "missing DNS question", http.StatusBadRequest)
		return
	}
	server.mu.Lock()
	server.asked = append(server.asked, question.Name.String())
	server.mu.Unlock()
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID: header.ID, Response: true, RecursionAvailable: true,
	})
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil || builder.Question(question) != nil ||
		builder.StartAnswers() != nil {
		http.Error(writer, "build DNS response", http.StatusInternalServerError)
		return
	}
	resourceHeader := dnsmessage.ResourceHeader{
		Name: question.Name, Class: dnsmessage.ClassINET, TTL: 60,
	}
	if question.Type == dnsmessage.TypeA && server.answer.Is4() {
		resourceHeader.Type = dnsmessage.TypeA
		if err := builder.AResource(resourceHeader, dnsmessage.AResource{
			A: server.answer.As4(),
		}); err != nil {
			http.Error(writer, "build A response", http.StatusInternalServerError)
			return
		}
	} else if question.Type == dnsmessage.TypeAAAA && server.answer.Is6() {
		resourceHeader.Type = dnsmessage.TypeAAAA
		if err := builder.AAAAResource(resourceHeader, dnsmessage.AAAAResource{
			AAAA: server.answer.As16(),
		}); err != nil {
			http.Error(writer, "build AAAA response", http.StatusInternalServerError)
			return
		}
	}
	encoded, err := builder.Finish()
	if err != nil {
		http.Error(writer, "finish DNS response", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/dns-message")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(encoded)
}
