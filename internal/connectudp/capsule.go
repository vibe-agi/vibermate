// Package connectudp carries UDP over an HTTP/1.1 proxy connection as
// connect-udp (RFC 9298): an Upgrade request names the target, and datagrams
// then travel as HTTP Datagram capsules (RFC 9297) on the upgraded stream. A
// captured run uses it to send an Agent's UDP through vibermated on the same
// relay and credential as its TCP (ADR 0024).
package connectudp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	// UpgradeToken is the HTTP Upgrade protocol token for connect-udp.
	UpgradeToken = "connect-udp"
	pathPrefix   = "/.well-known/masque/udp/"
	// datagramCapsule is the DATAGRAM capsule type.
	datagramCapsule = 0x00
	// MaxDatagram bounds one UDP payload; capsules are checked against it.
	MaxDatagram = 65535
	maxCapsule  = MaxDatagram + 16
)

var errMalformed = errors.New("connect-udp capsule is malformed")

// Path returns the request path naming host and port, with the host
// percent-encoded (an IPv6 literal's colons included).
func Path(host string, port uint16) string {
	return pathPrefix + url.PathEscape(host) + "/" + strconv.Itoa(int(port)) + "/"
}

// ParsePath extracts the target from a request path built by Path.
func ParsePath(escapedPath string) (string, uint16, error) {
	rest, ok := strings.CutPrefix(escapedPath, pathPrefix)
	if !ok || !strings.HasSuffix(rest, "/") {
		return "", 0, errors.New("connect-udp path is invalid")
	}
	parts := strings.Split(strings.TrimSuffix(rest, "/"), "/")
	if len(parts) != 2 {
		return "", 0, errors.New("connect-udp path is invalid")
	}
	host, err := url.PathUnescape(parts[0])
	if err != nil || host == "" || len(host) > 253 || strings.ContainsAny(host, "/ \t\r\n") {
		return "", 0, errors.New("connect-udp target host is invalid")
	}
	port, err := strconv.ParseUint(parts[1], 10, 16)
	if err != nil || port == 0 {
		return "", 0, errors.New("connect-udp target port is invalid")
	}
	return host, uint16(port), nil
}

// HeaderHasToken reports whether a comma-separated header lists token.
func HeaderHasToken(values []string, token string) bool {
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(item), token) {
				return true
			}
		}
	}
	return false
}

// WriteDatagram writes payload as a DATAGRAM capsule with context ID 0.
func WriteDatagram(w io.Writer, payload []byte) error {
	if len(payload) > MaxDatagram {
		return fmt.Errorf("datagram of %d bytes exceeds the limit", len(payload))
	}
	value := appendVarint(nil, 0) // Context ID 0: a UDP payload.
	frame := appendVarint(nil, datagramCapsule)
	frame = appendVarint(frame, uint64(len(value)+len(payload)))
	frame = append(frame, value...)
	frame = append(frame, payload...)
	_, err := w.Write(frame)
	return err
}

// ReadDatagram returns the next UDP payload, skipping capsules of other
// types and datagrams for other contexts, as RFC 9297 requires.
func ReadDatagram(r *bufio.Reader) ([]byte, error) {
	for {
		kind, err := readVarint(r)
		if err != nil {
			return nil, err
		}
		length, err := readVarint(r)
		if err != nil {
			return nil, unexpectedEOF(err)
		}
		if length > maxCapsule {
			return nil, errMalformed
		}
		value := make([]byte, length)
		if _, err := io.ReadFull(r, value); err != nil {
			return nil, unexpectedEOF(err)
		}
		if kind != datagramCapsule {
			continue
		}
		context, size := decodeVarint(value)
		if size == 0 {
			return nil, errMalformed
		}
		if context != 0 {
			continue
		}
		return value[size:], nil
	}
}

func unexpectedEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// appendVarint encodes v as a QUIC variable-length integer.
func appendVarint(b []byte, v uint64) []byte {
	switch {
	case v < 1<<6:
		return append(b, byte(v))
	case v < 1<<14:
		return append(b, byte(v>>8)|0x40, byte(v))
	case v < 1<<30:
		return append(b, byte(v>>24)|0x80, byte(v>>16), byte(v>>8), byte(v))
	default:
		return append(b, byte(v>>56)|0xc0, byte(v>>48), byte(v>>40), byte(v>>32),
			byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	}
}

func readVarint(r *bufio.Reader) (uint64, error) {
	first, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	size := 1 << (first >> 6)
	v := uint64(first & 0x3f)
	for i := 1; i < size; i++ {
		next, err := r.ReadByte()
		if err != nil {
			return 0, unexpectedEOF(err)
		}
		v = v<<8 | uint64(next)
	}
	return v, nil
}

// decodeVarint reads a varint from the start of b; size 0 means malformed.
func decodeVarint(b []byte) (uint64, int) {
	if len(b) == 0 {
		return 0, 0
	}
	size := 1 << (b[0] >> 6)
	if len(b) < size {
		return 0, 0
	}
	v := uint64(b[0] & 0x3f)
	for i := 1; i < size; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v, size
}

// Conn is one upgraded connect-udp association.
type Conn struct {
	conn   net.Conn
	reader *bufio.Reader
}

// NewConn wraps an upgraded stream whose response header was already read.
func NewConn(conn net.Conn, reader *bufio.Reader) *Conn {
	return &Conn{conn: conn, reader: reader}
}

func (c *Conn) ReadDatagram() ([]byte, error) { return ReadDatagram(c.reader) }
func (c *Conn) WriteDatagram(payload []byte) error {
	return WriteDatagram(c.conn, payload)
}
func (c *Conn) Close() error { return c.conn.Close() }
