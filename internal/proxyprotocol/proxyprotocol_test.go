package proxyprotocol

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"strings"
	"testing"
)

func v2Header(command, family byte, addresses []byte) []byte {
	header := append([]byte{}, v2Signature...)
	header = append(header, 0x20|command, family, 0, 0)
	binary.BigEndian.PutUint16(header[14:16], uint16(len(addresses)))
	return append(header, addresses...)
}

func ipv4Block(source string, sourcePort uint16) []byte {
	block := make([]byte, 12)
	copy(block[0:4], netip.MustParseAddr(source).AsSlice())
	copy(block[4:8], netip.MustParseAddr("192.0.2.1").AsSlice())
	binary.BigEndian.PutUint16(block[8:10], sourcePort)
	binary.BigEndian.PutUint16(block[10:12], 443)
	return block
}

func ipv6Block(source string, sourcePort uint16) []byte {
	block := make([]byte, 36)
	copy(block[0:16], netip.MustParseAddr(source).AsSlice())
	copy(block[16:32], netip.MustParseAddr("2001:db8::1").AsSlice())
	binary.BigEndian.PutUint16(block[32:34], sourcePort)
	binary.BigEndian.PutUint16(block[34:36], 443)
	return block
}

// readThenRest reads a header and returns what the connection sends after it.
func readThenRest(t *testing.T, stream []byte) (Header, error, string) {
	t.Helper()
	reader := bufio.NewReader(bytes.NewReader(stream))
	header, err := Read(reader)
	rest, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return header, err, string(rest)
}

func TestReadsClientAddresses(t *testing.T) {
	t.Parallel()

	withTLV := append(ipv4Block("203.0.113.9", 51000), 0x04, 0x00, 0x02, 'o', 'k')
	for name, test := range map[string]struct {
		stream []byte
		want   netip.AddrPort
	}{
		"v1 IPv4": {
			stream: []byte("PROXY TCP4 203.0.113.9 192.0.2.1 51000 443\r\nGET / HTTP/1.1\r\n"),
			want:   netip.MustParseAddrPort("203.0.113.9:51000"),
		},
		"v1 IPv6": {
			stream: []byte("PROXY TCP6 2001:db8::9 2001:db8::1 51000 443\r\nGET / HTTP/1.1\r\n"),
			want:   netip.MustParseAddrPort("[2001:db8::9]:51000"),
		},
		"v2 IPv4 with TLVs": {
			stream: append(v2Header(0x1, 0x11, withTLV), "GET / HTTP/1.1\r\n"...),
			want:   netip.MustParseAddrPort("203.0.113.9:51000"),
		},
		"v2 IPv6": {
			stream: append(v2Header(0x1, 0x21, ipv6Block("2001:db8::9", 51000)), "GET / HTTP/1.1\r\n"...),
			want:   netip.MustParseAddrPort("[2001:db8::9]:51000"),
		},
	} {
		header, err, rest := readThenRest(t, test.stream)
		if err != nil || header.Local || header.Source != test.want || rest != "GET / HTTP/1.1\r\n" {
			t.Errorf("%s: Read() = %+v, %v, rest %q", name, header, err, rest)
		}
	}
}

func TestReadsLoadBalancerOwnConnections(t *testing.T) {
	t.Parallel()

	for name, stream := range map[string][]byte{
		"v1 UNKNOWN":              []byte("PROXY UNKNOWN\r\n"),
		"v1 UNKNOWN detail":       []byte("PROXY UNKNOWN ffff::1 ffff::2 1 2\r\n"),
		"v2 LOCAL":                v2Header(0x0, 0x00, nil),
		"v2 LOCAL with addresses": v2Header(0x0, 0x11, ipv4Block("203.0.113.9", 1)),
		"v2 UNSPEC":               v2Header(0x1, 0x00, nil),
	} {
		header, err, rest := readThenRest(t, append(stream, "GET /health HTTP/1.1\r\n"...))
		if err != nil || !header.Local || header.Source.IsValid() || rest != "GET /health HTTP/1.1\r\n" {
			t.Errorf("%s: Read() = %+v, %v, rest %q", name, header, err, rest)
		}
	}
}

func TestConnectionsWithoutAHeaderKeepTheirBytes(t *testing.T) {
	t.Parallel()

	for name, stream := range map[string]string{
		"TLS ClientHello": "\x16\x03\x01\x02\x00\x01",
		"HTTP GET":        "GET / HTTP/1.1\r\n",
		"HTTP POST":       "POST /api HTTP/1.1\r\n",
		"short P":         "PRI",
		"CR not v2":       "\r\n\r\nhello",
	} {
		header, err, rest := readThenRest(t, []byte(stream))
		if !errors.Is(err, ErrNoHeader) || header != (Header{}) || rest != stream {
			t.Errorf("%s: Read() = %+v, %v, rest %q", name, header, err, rest)
		}
	}
	reader := bufio.NewReader(strings.NewReader(""))
	if _, err := Read(reader); !errors.Is(err, io.EOF) {
		t.Fatalf("Read(empty) = %v, want EOF", err)
	}
}

func TestRejectsMalformedHeaders(t *testing.T) {
	t.Parallel()

	oversized := v2Header(0x1, 0x11, make([]byte, maxV2AddressBlock+1))
	for name, stream := range map[string][]byte{
		"v1 without CRLF":      []byte("PROXY TCP4 203.0.113.9 192.0.2.1 51000 443\n"),
		"v1 too long":          []byte("PROXY TCP4 " + strings.Repeat("1", 120) + "\r\n"),
		"v1 truncated":         []byte("PROXY TCP4 203.0.113.9"),
		"v1 family mismatch":   []byte("PROXY TCP4 2001:db8::9 192.0.2.1 51000 443\r\n"),
		"v1 unknown family":    []byte("PROXY UDP4 203.0.113.9 192.0.2.1 51000 443\r\n"),
		"v1 leading zero port": []byte("PROXY TCP4 203.0.113.9 192.0.2.1 051000 443\r\n"),
		"v1 port out of range": []byte("PROXY TCP4 203.0.113.9 192.0.2.1 70000 443\r\n"),
		"v1 zone":              []byte("PROXY TCP6 fe80::1%eth0 2001:db8::1 51000 443\r\n"),
		"v2 wrong version":     append(append([]byte{}, v2Signature...), 0x11, 0x11, 0, 0),
		"v2 unknown command":   v2Header(0x2, 0x11, ipv4Block("203.0.113.9", 1)),
		"v2 UDP":               v2Header(0x1, 0x12, ipv4Block("203.0.113.9", 1)),
		"v2 UNIX":              v2Header(0x1, 0x31, make([]byte, 216)),
		"v2 short IPv4 block":  v2Header(0x1, 0x11, make([]byte, 8)),
		"v2 short IPv6 block":  v2Header(0x1, 0x21, make([]byte, 20)),
		"v2 truncated block":   v2Header(0x1, 0x11, ipv4Block("203.0.113.9", 1))[:20],
		"v2 oversized block":   oversized,
		"v2 truncated fixed":   append([]byte{}, v2Signature[:12]...),
	} {
		reader := bufio.NewReader(bytes.NewReader(stream))
		if _, err := Read(reader); !errors.Is(err, ErrInvalidHeader) {
			t.Errorf("%s: Read() = %v, want ErrInvalidHeader", name, err)
		}
	}
}
