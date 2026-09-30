// Package proxyprotocol reads the PROXY protocol header (versions 1 and 2)
// that a layer-4 load balancer writes at the start of each TCP connection to
// tell the backend which client it relays.
//
// The header is only meaningful from a load balancer the operator trusts:
// anyone can write these bytes, so callers must decide to read it by the
// TCP peer address, never by what the connection claims.
package proxyprotocol

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"strconv"
	"strings"
)

// ErrNoHeader means the connection does not start with a PROXY header. No
// bytes were consumed, so the connection can be read as if unwrapped.
var ErrNoHeader = errors.New("connection does not start with a PROXY protocol header")

// ErrInvalidHeader means the connection started like a PROXY header but the
// header is malformed, truncated or unsupported.
var ErrInvalidHeader = errors.New("PROXY protocol header is invalid")

// Header describes the relayed connection.
type Header struct {
	// Local marks a connection the load balancer opened on its own behalf,
	// such as a health check. It carries no client address.
	Local bool
	// Source is the relayed client address. An invalid address with Local=false
	// means UNKNOWN/UNSPEC, not a load balancer's own connection.
	Source netip.AddrPort
}

const (
	maxV1Length       = 107
	maxV2AddressBlock = 4096
)

var v2Signature = []byte("\r\n\r\n\x00\r\nQUIT\n")

// Read consumes one PROXY header from reader.
func Read(reader *bufio.Reader) (Header, error) {
	first, err := reader.Peek(1)
	if err != nil {
		return Header{}, err
	}
	switch first[0] {
	case 'P':
		prefix, err := reader.Peek(6)
		if err != nil || string(prefix) != "PROXY " {
			// An HTTP request such as "POST /" also starts with P.
			return Header{}, noHeaderOr(err)
		}
		return readV1(reader)
	case '\r':
		prefix, err := reader.Peek(len(v2Signature))
		if err != nil || !bytes.Equal(prefix, v2Signature) {
			return Header{}, noHeaderOr(err)
		}
		return readV2(reader)
	default:
		return Header{}, ErrNoHeader
	}
}

// noHeaderOr keeps real read errors such as a deadline visible, while a
// connection that simply ended early is reported as having no header.
func noHeaderOr(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, bufio.ErrBufferFull) {
		return ErrNoHeader
	}
	return err
}

func readV1(reader *bufio.Reader) (Header, error) {
	line := make([]byte, 0, maxV1Length)
	for {
		value, err := reader.ReadByte()
		if err != nil {
			return Header{}, invalid(err)
		}
		line = append(line, value)
		if value == '\n' {
			break
		}
		if len(line) >= maxV1Length {
			return Header{}, ErrInvalidHeader
		}
	}
	text, found := strings.CutSuffix(string(line), "\r\n")
	if !found {
		return Header{}, ErrInvalidHeader
	}
	fields := strings.Split(text, " ")
	if len(fields) >= 2 && fields[0] == "PROXY" && fields[1] == "UNKNOWN" {
		// The proxy could not tell who connected; use the real endpoints.
		return Header{}, nil
	}
	if len(fields) != 6 || fields[0] != "PROXY" {
		return Header{}, ErrInvalidHeader
	}
	source, err := netip.ParseAddr(fields[2])
	if err != nil || source.Zone() != "" {
		return Header{}, ErrInvalidHeader
	}
	destination, err := netip.ParseAddr(fields[3])
	if err != nil || destination.Zone() != "" {
		return Header{}, ErrInvalidHeader
	}
	switch fields[1] {
	case "TCP4":
		if !source.Is4() || !destination.Is4() {
			return Header{}, ErrInvalidHeader
		}
	case "TCP6":
		if !source.Is6() || !destination.Is6() {
			return Header{}, ErrInvalidHeader
		}
	default:
		return Header{}, ErrInvalidHeader
	}
	sourcePort, ok := parsePort(fields[4])
	if !ok {
		return Header{}, ErrInvalidHeader
	}
	if _, ok := parsePort(fields[5]); !ok {
		return Header{}, ErrInvalidHeader
	}
	return Header{Source: netip.AddrPortFrom(source, sourcePort)}, nil
}

func parsePort(text string) (uint16, bool) {
	if text == "" || len(text) > 5 || (len(text) > 1 && text[0] == '0') {
		return 0, false
	}
	value, err := strconv.ParseUint(text, 10, 16)
	return uint16(value), err == nil
}

func readV2(reader *bufio.Reader) (Header, error) {
	fixed := make([]byte, 16)
	if _, err := io.ReadFull(reader, fixed); err != nil {
		return Header{}, invalid(err)
	}
	versionCommand, family := fixed[12], fixed[13]
	length := int(binary.BigEndian.Uint16(fixed[14:16]))
	if versionCommand>>4 != 2 || length > maxV2AddressBlock {
		return Header{}, ErrInvalidHeader
	}
	block := make([]byte, length)
	if _, err := io.ReadFull(reader, block); err != nil {
		return Header{}, invalid(err)
	}
	switch versionCommand & 0x0f {
	case 0x0:
		// LOCAL: the load balancer's own connection, such as a health check.
		return Header{Local: true}, nil
	case 0x1:
	default:
		return Header{}, ErrInvalidHeader
	}
	switch family {
	case 0x00:
		// UNSPEC: no address information; use the real endpoints.
		return Header{}, nil
	case 0x11: // TCP over IPv4
		if length < 12 {
			return Header{}, ErrInvalidHeader
		}
		source := netip.AddrFrom4([4]byte(block[0:4]))
		return Header{Source: netip.AddrPortFrom(source, binary.BigEndian.Uint16(block[8:10]))}, nil
	case 0x21: // TCP over IPv6
		if length < 36 {
			return Header{}, ErrInvalidHeader
		}
		source := netip.AddrFrom16([16]byte(block[0:16]))
		return Header{Source: netip.AddrPortFrom(source, binary.BigEndian.Uint16(block[32:34]))}, nil
	default:
		// UDP and UNIX sockets never reach a TCP listener.
		return Header{}, ErrInvalidHeader
	}
}

func invalid(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrInvalidHeader
	}
	return errors.Join(ErrInvalidHeader, err)
}
