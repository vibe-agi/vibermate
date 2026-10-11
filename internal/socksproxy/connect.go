package socksproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

type peerConn struct {
	net.Conn
	remote net.Addr
}

func (c *peerConn) RemoteAddr() net.Addr { return c.remote }

type pipeListener struct {
	conn     net.Conn
	accepted bool
	mu       sync.Mutex
	closed   chan struct{}
	once     sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.accepted {
		l.accepted = true
		l.mu.Unlock()
		return l.conn, nil
	}
	l.mu.Unlock()
	<-l.closed
	return nil, net.ErrClosed
}
func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed); l.conn.Close() })
	return nil
}
func (l *pipeListener) Addr() net.Addr { return l.conn.LocalAddr() }

type handlerLifetime struct {
	handler http.Handler
	mu      sync.Mutex
	closing bool
	workers sync.WaitGroup
}

func newHandlerLifetime(handler http.Handler) *handlerLifetime {
	return &handlerLifetime{handler: handler}
}
func (h *handlerLifetime) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return
	}
	h.workers.Add(1)
	h.mu.Unlock()
	defer h.workers.Done()
	h.handler.ServeHTTP(w, r)
}
func (h *handlerLifetime) closeAdmission() {
	h.mu.Lock()
	h.closing = true
	h.mu.Unlock()
}
func (h *handlerLifetime) wait() {
	h.workers.Wait()
}

func (x *session) connect(reader *bufio.Reader, target Target, basic string) {
	front, back := net.Pipe()
	listener := &pipeListener{conn: &peerConn{Conn: back, remote: x.control.RemoteAddr()}, closed: make(chan struct{})}
	x.mu.Lock()
	x.bridge = front
	x.mu.Unlock()
	if x.ctx.Err() != nil {
		front.Close()
		back.Close()
		return
	}
	handler := newHandlerLifetime(x.server.options.TCPHandler)
	server := &http.Server{Handler: handler, BaseContext: func(net.Listener) context.Context { return x.ctx }, ReadHeaderTimeout: 10 * time.Second}
	server.SetKeepAlivesEnabled(false)
	served := make(chan struct{})
	go func() { server.Serve(listener); close(served) }()
	defer func() {
		handler.closeAdmission()
		x.cancel()
		front.Close()
		back.Close()
		server.Close()
		listener.Close()
		<-served
		handler.wait()
	}()
	front.SetDeadline(time.Now().Add(10 * time.Second))
	authority := target.authority()
	if _, e := fmt.Fprintf(front, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", authority, authority, basic); e != nil {
		writeReply(x.control, 1, zeroBind)
		return
	}
	responseReader := bufio.NewReader(front)
	response, e := http.ReadResponse(responseReader, &http.Request{Method: http.MethodConnect})
	if e != nil {
		writeReply(x.control, 1, zeroBind)
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status := byte(1)
		if response.StatusCode == 403 || response.StatusCode == 407 {
			status = 2
		}
		writeReply(x.control, status, zeroBind)
		return
	}
	front.SetDeadline(time.Time{})
	local, e := netipPeer(x.control.LocalAddr())
	if e != nil {
		writeReply(x.control, 1, zeroBind)
		return
	}
	if e := writeReply(x.control, 0, local); e != nil {
		return
	}
	done := make(chan struct{})
	go func() { io.Copy(front, reader); front.Close(); x.control.Close(); close(done) }()
	io.Copy(x.control, responseReader)
	front.Close()
	x.control.Close()
	<-done
}
