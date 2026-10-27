package flowingress

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	sniffBytes   = 16 << 10
	sniffTimeout = 2 * time.Second
)

// sniffHost names the destination from the client's first bytes: TLS SNI or
// an HTTP/1 Host header. It returns "" when neither is present; every byte it
// reads stays buffered in reader for replay.
func sniffHost(conn net.Conn, reader *bufio.Reader) string {
	_ = conn.SetReadDeadline(time.Now().Add(sniffTimeout))
	defer conn.SetReadDeadline(time.Time{})
	first, err := reader.Peek(1)
	if err != nil {
		return ""
	}
	if first[0] == 0x16 {
		return sniffSNI(reader)
	}
	return sniffHTTPHost(reader)
}

// sniffSNI lets crypto/tls parse the ClientHello from a copy of the buffered
// bytes and stops the handshake as soon as the server name is known.
func sniffSNI(reader *bufio.Reader) string {
	name := ""
	peekGrowing(reader, func(peeked []byte) bool {
		var complete bool
		name, complete = clientHelloServerName(peeked)
		return complete
	})
	return name
}

// peekGrowing hands parse every buffered byte, waiting for one more byte at a
// time, until parse reports completion, the peek limit is reached or the read
// deadline fires. Nothing is consumed.
func peekGrowing(reader *bufio.Reader, parse func([]byte) bool) {
	want := max(reader.Buffered(), 1)
	for want <= sniffBytes {
		peeked, err := reader.Peek(want)
		if parse(peeked) || err != nil {
			return
		}
		want = max(reader.Buffered(), len(peeked)+1)
	}
}

var errHelloRead = errors.New("client hello read")

func clientHelloServerName(data []byte) (string, bool) {
	var name string
	seen := false
	server := tls.Server(&replayConn{reader: bytes.NewReader(data)}, &tls.Config{
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			name, seen = hello.ServerName, true
			return nil, errHelloRead
		},
	})
	_ = server.Handshake()
	if !seen || !validHostName(strings.ToLower(name)) {
		return "", seen
	}
	return strings.ToLower(name), true
}

var httpMethods = [][]byte{
	[]byte("GET "), []byte("POST "), []byte("PUT "), []byte("HEAD "), []byte("DELETE "),
	[]byte("OPTIONS "), []byte("PATCH "), []byte("TRACE "),
}

// startsLikeHTTP reports whether the buffered bytes can still begin an HTTP/1
// request line, so opaque protocols stop sniffing at their first bytes.
func startsLikeHTTP(reader *bufio.Reader) bool {
	peeked, _ := reader.Peek(min(reader.Buffered(), 8))
	for _, method := range httpMethods {
		n := min(len(peeked), len(method))
		if bytes.Equal(peeked[:n], method[:n]) {
			if n < len(method) {
				return morePeek(reader, len(method), method)
			}
			return true
		}
	}
	return false
}

func morePeek(reader *bufio.Reader, size int, method []byte) bool {
	peeked, err := reader.Peek(size)
	return err == nil && bytes.Equal(peeked, method)
}

func sniffHTTPHost(reader *bufio.Reader) string {
	if !startsLikeHTTP(reader) {
		return ""
	}
	host := ""
	peekGrowing(reader, func(peeked []byte) bool {
		end := bytes.Index(peeked, []byte("\r\n\r\n"))
		if end < 0 {
			return false
		}
		request, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(peeked[:end+4])))
		if err != nil {
			return true
		}
		candidate := request.Host
		if h, _, splitErr := net.SplitHostPort(candidate); splitErr == nil {
			candidate = h
		}
		if candidate = strings.ToLower(candidate); validHostName(candidate) {
			host = candidate
		}
		return true
	})
	return host
}

// replayConn feeds recorded bytes to crypto/tls and swallows its writes.
type replayConn struct {
	reader io.Reader
	net.Conn
}

func (c *replayConn) Read(p []byte) (int, error)       { return c.reader.Read(p) }
func (c *replayConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *replayConn) Close() error                     { return nil }
func (c *replayConn) SetDeadline(time.Time) error      { return nil }
func (c *replayConn) SetReadDeadline(time.Time) error  { return nil }
func (c *replayConn) SetWriteDeadline(time.Time) error { return nil }
func (c *replayConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *replayConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
