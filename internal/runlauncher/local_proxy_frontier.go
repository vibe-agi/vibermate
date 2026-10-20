package runlauncher

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/loopbackclient"
)

const (
	localProxyHeaderLimit     = 64 << 10
	localProxyHeaderTimeout   = 30 * time.Second
	localProxyDialTimeout     = 30 * time.Second
	localProxyConnectionLimit = 128
)

// localProxyTarget is transport material already authorized by the launcher.
// The frontier cannot create a Capture, discover a Runtime, or dial a target
// destination. Each accepted connection keeps its own immutable target.
type localProxyTarget struct {
	Generation  string
	ProxyOrigin string
	ProxyToken  string
}

type localProxyBackend struct {
	localProxyTarget
}

type localProxyConnection struct {
	client  net.Conn
	backend net.Conn
	target  *localProxyBackend
	cancel  context.CancelFunc
	closed  bool
	done    chan struct{}
}

type localProxyConnectionKey struct{}

// localProxyFrontier owns the stable child-facing listener and credential.
// Replacing its target retires connections, never requests: no request bytes
// are replayed, and provider credentials remain opaque to this transport.
type localProxyFrontier struct {
	listener    net.Listener
	server      *http.Server
	serveDone   chan struct{}
	token       string
	tokenDigest [sha256.Size]byte
	mutation    sync.Mutex
	mu          sync.Mutex
	target      *localProxyBackend
	connections map[net.Conn]*localProxyConnection
	closed      bool
}

func startLocalProxyFrontier(initial localProxyTarget) (*localProxyFrontier, error) {
	target, err := validateLocalProxyTarget(initial)
	if err != nil {
		return nil, err
	}
	entropy := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, entropy); err != nil {
		return nil, errors.New("generate local proxy credential")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("listen for local proxy frontier")
	}
	token := base64.RawURLEncoding.EncodeToString(entropy)
	frontier := &localProxyFrontier{
		listener: listener, serveDone: make(chan struct{}),
		token: token, tokenDigest: sha256.Sum256([]byte(token)),
		target: target, connections: make(map[net.Conn]*localProxyConnection),
	}
	frontier.server = &http.Server{
		Handler:           http.HandlerFunc(frontier.serveHTTP),
		ReadHeaderTimeout: localProxyHeaderTimeout,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    localProxyHeaderLimit,
		ErrorLog:          log.New(io.Discard, "", 0),
		ConnContext:       frontier.acceptConnection,
		ConnState: func(conn net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				frontier.releaseConnection(conn)
			}
		},
	}
	go func() {
		defer close(frontier.serveDone)
		_ = frontier.server.Serve(listener)
	}()
	return frontier, nil
}

func validateLocalProxyTarget(target localProxyTarget) (*localProxyBackend, error) {
	if target.Generation == "" || len(target.Generation) > 128 || strings.ContainsAny(target.Generation, "\x00\r\n") ||
		target.ProxyToken == "" || len(target.ProxyToken) > 1024 || strings.ContainsAny(target.ProxyToken, "\x00\r\n") {
		return nil, errors.New("local proxy target is incomplete")
	}
	origin, err := url.Parse(target.ProxyOrigin)
	if err != nil || origin.Scheme != "http" || origin.Hostname() != "127.0.0.1" || origin.User != nil ||
		origin.Path != "" || origin.RawPath != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
		return nil, errors.New("local proxy target must be a literal loopback origin")
	}
	port, err := strconv.ParseUint(origin.Port(), 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("local proxy target port is invalid")
	}
	return &localProxyBackend{localProxyTarget: target}, nil
}

func (frontier *localProxyFrontier) Origin() string {
	return "http://" + frontier.listener.Addr().String()
}

func (frontier *localProxyFrontier) Token() string { return frontier.token }

// ReplaceTarget first validates without disturbing the current target, then
// joins every retired connection before making the replacement available.
func (frontier *localProxyFrontier) ReplaceTarget(next localProxyTarget) error {
	target, err := validateLocalProxyTarget(next)
	if err != nil {
		return err
	}
	frontier.mutation.Lock()
	defer frontier.mutation.Unlock()
	frontier.mu.Lock()
	closed := frontier.closed
	frontier.mu.Unlock()
	if closed {
		return errors.New("local proxy frontier is closed")
	}
	frontier.withdrawConnections()
	frontier.mu.Lock()
	frontier.target = target
	frontier.mu.Unlock()
	return nil
}

func (frontier *localProxyFrontier) Withdraw() {
	frontier.mutation.Lock()
	defer frontier.mutation.Unlock()
	frontier.withdrawConnections()
}

func (frontier *localProxyFrontier) Close() {
	frontier.mutation.Lock()
	defer frontier.mutation.Unlock()
	frontier.mu.Lock()
	if frontier.closed {
		frontier.mu.Unlock()
		return
	}
	frontier.closed = true
	frontier.target = nil
	frontier.mu.Unlock()
	_ = frontier.listener.Close()
	_ = frontier.server.Close()
	// ConnContext runs on the accept goroutine. Join it before taking the final
	// snapshot so an accepted, not-yet-registered connection cannot escape Close.
	<-frontier.serveDone
	frontier.withdrawConnections()
}

func (frontier *localProxyFrontier) acceptConnection(ctx context.Context, conn net.Conn) context.Context {
	frontier.mu.Lock()
	if frontier.closed || len(frontier.connections) >= localProxyConnectionLimit {
		frontier.mu.Unlock()
		_ = conn.Close()
		return ctx
	}
	ctx, cancel := context.WithCancel(ctx)
	entry := &localProxyConnection{client: conn, target: frontier.target, cancel: cancel, done: make(chan struct{}), closed: frontier.closed}
	frontier.connections[conn] = entry
	frontier.mu.Unlock()
	return context.WithValue(ctx, localProxyConnectionKey{}, entry)
}

func (frontier *localProxyFrontier) releaseConnection(conn net.Conn) {
	frontier.mu.Lock()
	entry, exists := frontier.connections[conn]
	if exists {
		delete(frontier.connections, conn)
		close(entry.done)
	}
	frontier.mu.Unlock()
	if exists {
		entry.cancel()
	}
}

func (frontier *localProxyFrontier) withdrawConnections() {
	frontier.mu.Lock()
	frontier.target = nil
	type retiredConnection struct {
		entry   *localProxyConnection
		backend net.Conn
	}
	retired := make([]retiredConnection, 0, len(frontier.connections))
	for _, entry := range frontier.connections {
		entry.closed = true
		retired = append(retired, retiredConnection{entry, entry.backend})
	}
	frontier.mu.Unlock()
	for _, item := range retired {
		item.entry.cancel()
		_ = item.entry.client.Close()
		if item.backend != nil {
			_ = item.backend.Close()
		}
	}
	for _, item := range retired {
		<-item.entry.done
	}
}

func (frontier *localProxyFrontier) dialBackend(ctx context.Context, entry *localProxyConnection) (net.Conn, error) {
	client, err := loopbackclient.New(entry.target.ProxyOrigin, localProxyDialTimeout)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	connection, err := client.Dial(ctx)
	if err != nil {
		return nil, err
	}
	frontier.mu.Lock()
	if entry.closed {
		frontier.mu.Unlock()
		_ = connection.Close()
		return nil, net.ErrClosed
	}
	entry.backend = connection
	frontier.mu.Unlock()
	return connection, nil
}

func (frontier *localProxyFrontier) releaseBackend(entry *localProxyConnection, backend net.Conn) {
	_ = backend.Close()
	frontier.mu.Lock()
	if entry.backend == backend {
		entry.backend = nil
	}
	frontier.mu.Unlock()
}

func (frontier *localProxyFrontier) authenticated(header http.Header) bool {
	values := header.Values("Proxy-Authorization")
	if len(values) != 1 || len(values[0]) > 1024 {
		return false
	}
	scheme, encoded, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Basic") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	user, token, ok := strings.Cut(string(decoded), ":")
	digest := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(digest[:], frontier.tokenDigest[:]) == 1 && ok && user == "capture"
}

func (frontier *localProxyFrontier) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if !frontier.authenticated(request.Header) {
		writer.Header().Set("Proxy-Authenticate", `Basic realm="ViberMate"`)
		writer.Header().Set("Connection", "close")
		http.Error(writer, "local proxy credential required", http.StatusProxyAuthRequired)
		return
	}
	entry, _ := request.Context().Value(localProxyConnectionKey{}).(*localProxyConnection)
	if entry == nil || entry.target == nil {
		writer.Header().Set("Connection", "close")
		http.Error(writer, "Runtime proxy unavailable", http.StatusServiceUnavailable)
		return
	}
	connect := request.Method == http.MethodConnect
	if (connect && (request.URL.Host == "" || request.ContentLength > 0 || len(request.TransferEncoding) != 0)) ||
		(!connect && (!request.URL.IsAbs() || request.URL.Host == "" || request.URL.User != nil || (request.URL.Scheme != "http" && request.URL.Scheme != "https"))) {
		writer.Header().Set("Connection", "close")
		http.Error(writer, "absolute proxy target required", http.StatusBadRequest)
		return
	}
	backend, err := frontier.dialBackend(request.Context(), entry)
	if err != nil {
		http.Error(writer, "Runtime proxy unavailable", http.StatusBadGateway)
		return
	}
	hijacked := false
	defer func() {
		frontier.releaseBackend(entry, backend)
		if hijacked {
			frontier.releaseConnection(entry.client)
		}
	}()
	// DialContext stops owning cancellation once it returns a raw connection.
	// Keep the HTTP request's lifetime attached while waiting on Runtime headers
	// or streaming its response; a disconnected client must release both sides.
	backendCanceled := make(chan struct{})
	stopBackendCancellation := context.AfterFunc(request.Context(), func() {
		defer close(backendCanceled)
		_ = backend.Close()
	})
	joinBackendCancellation := sync.OnceFunc(func() {
		if !stopBackendCancellation() {
			<-backendCanceled
		}
	})
	defer joinBackendCancellation()
	forward := request.Clone(request.Context())
	forward.RequestURI = ""
	forward.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("capture:"+entry.target.ProxyToken)))
	if _, hasAgent := forward.Header["User-Agent"]; !hasAgent {
		forward.Header["User-Agent"] = []string{""}
	}
	// WriteProxy streams the original body once. In particular, there is no
	// RoundTripper retry, GetBody replay, redirect, or destination dial here.
	if err := forward.WriteProxy(backend); err != nil {
		http.Error(writer, "Runtime proxy request interrupted", http.StatusBadGateway)
		return
	}
	response, reader, err := readLocalProxyResponse(backend, forward)
	if err != nil {
		http.Error(writer, "Runtime proxy response unavailable", http.StatusBadGateway)
		return
	}
	if connect && response.StatusCode >= 200 && response.StatusCode < 300 {
		// Hijacking transfers the socket lifetime to the tunnel. Stop and join
		// HTTP cancellation before that handoff, so it cannot close a live tunnel.
		joinBackendCancellation()
		if request.Context().Err() != nil {
			return
		}
		hijacked = frontier.tunnel(writer, backend, reader, response)
		return
	}
	defer response.Body.Close()
	for key, values := range response.Header {
		writer.Header()[key] = append([]string(nil), values...)
	}
	writer.WriteHeader(response.StatusCode)
	if err := http.NewResponseController(writer).Flush(); err != nil {
		return
	}
	_, _ = io.Copy(localProxyStreamingWriter{writer}, response.Body)
}

type localProxyHeaderReader struct {
	reader    io.Reader
	remaining int
	limited   bool
}

func (reader *localProxyHeaderReader) Read(destination []byte) (int, error) {
	if reader.limited {
		if reader.remaining == 0 {
			return 0, errors.New("Runtime proxy headers exceed limit")
		}
		destination = destination[:min(len(destination), reader.remaining)]
	}
	n, err := reader.reader.Read(destination)
	if reader.limited {
		reader.remaining -= n
	}
	return n, err
}

func readLocalProxyResponse(backend net.Conn, request *http.Request) (*http.Response, *bufio.Reader, error) {
	// A Runtime can be waiting on a person's approval or a long provider
	// response. Its lifetime is owned by the connection, withdrawal and Close;
	// a second absolute response/SSE deadline would shorten that authorization.
	limited := &localProxyHeaderReader{reader: backend, remaining: localProxyHeaderLimit, limited: true}
	reader := bufio.NewReader(limited)
	for {
		response, err := http.ReadResponse(reader, request)
		if err != nil {
			return nil, nil, err
		}
		if response.StatusCode >= 100 && response.StatusCode < 200 && response.StatusCode != http.StatusSwitchingProtocols {
			response.Body.Close()
			continue
		}
		limited.limited = false
		return response, reader, nil
	}
}

type localProxyStreamingWriter struct{ http.ResponseWriter }

func (writer localProxyStreamingWriter) Write(body []byte) (int, error) {
	n, err := writer.ResponseWriter.Write(body)
	if err == nil {
		err = http.NewResponseController(writer.ResponseWriter).Flush()
	}
	return n, err
}

func (frontier *localProxyFrontier) tunnel(writer http.ResponseWriter, backend net.Conn, backendReader *bufio.Reader, response *http.Response) (hijacked bool) {
	client, buffer, err := http.NewResponseController(writer).Hijack()
	if err != nil {
		return
	}
	hijacked = true
	defer client.Close()
	if _, err := fmt.Fprintf(buffer, "HTTP/1.1 %s\r\n", response.Status); err != nil {
		return
	}
	if err := response.Header.Write(buffer); err != nil {
		return
	}
	if _, err := buffer.WriteString("\r\n"); err != nil {
		return
	}
	if err := buffer.Flush(); err != nil {
		return
	}
	copyDone := make(chan struct{}, 1)
	go func() {
		_, _ = io.Copy(backend, buffer.Reader)
		if closer, ok := backend.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		copyDone <- struct{}{}
	}()
	_, _ = io.Copy(client, backendReader)
	_ = client.Close()
	_ = backend.Close()
	<-copyDone
	return
}
