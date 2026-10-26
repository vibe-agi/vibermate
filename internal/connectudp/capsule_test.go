package connectudp

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestVarintRoundTripsAtEveryWidth(t *testing.T) {
	for _, v := range []uint64{0, 63, 64, 16383, 16384, 1<<30 - 1, 1 << 30, 1<<62 - 1} {
		encoded := appendVarint(nil, v)
		got, err := readVarint(bufio.NewReader(bytes.NewReader(encoded)))
		if err != nil || got != v {
			t.Fatalf("varint %d round trip = %d, %v", v, got, err)
		}
		if decoded, size := decodeVarint(encoded); decoded != v || size != len(encoded) {
			t.Fatalf("decodeVarint(%d) = %d, %d", v, decoded, size)
		}
	}
}

func TestPathNamesTheTarget(t *testing.T) {
	for _, test := range []struct {
		host string
		port uint16
	}{{"dns.example", 53}, {"192.0.2.6", 443}, {"2001:db8::1", 4433}} {
		host, port, err := ParsePath(Path(test.host, test.port))
		if err != nil || host != test.host || port != test.port {
			t.Fatalf("ParsePath(Path(%s, %d)) = %s, %d, %v", test.host, test.port, host, port, err)
		}
	}
	for _, invalid := range []string{
		"/.well-known/masque/udp/host/0/", "/.well-known/masque/udp/host/53",
		"/.well-known/masque/udp//53/", "/other/host/53/", "/.well-known/masque/udp/a/b/53/",
	} {
		if _, _, err := ParsePath(invalid); err == nil {
			t.Errorf("ParsePath(%q) succeeded", invalid)
		}
	}
}

func TestDatagramsSkipOtherCapsulesAndContexts(t *testing.T) {
	var stream bytes.Buffer
	// An unknown capsule type, then a datagram for context 2, then ours.
	stream.Write(append(appendVarint(appendVarint(nil, 0x29), 3), 'x', 'y', 'z'))
	other := append(appendVarint(nil, 2), "not-ours"...)
	stream.Write(append(appendVarint(appendVarint(nil, datagramCapsule), uint64(len(other))), other...))
	if err := WriteDatagram(&stream, []byte("dns-query")); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(&stream)
	payload, err := ReadDatagram(reader)
	if err != nil || string(payload) != "dns-query" {
		t.Fatalf("ReadDatagram = %q, %v", payload, err)
	}
	if _, err := ReadDatagram(reader); !errors.Is(err, io.EOF) {
		t.Fatalf("end of stream = %v, want EOF", err)
	}
}

func TestDatagramLimitsAndTruncation(t *testing.T) {
	if err := WriteDatagram(io.Discard, make([]byte, MaxDatagram+1)); err == nil {
		t.Fatal("an oversized datagram was written")
	}
	huge := appendVarint(appendVarint(nil, datagramCapsule), maxCapsule+1)
	if _, err := ReadDatagram(bufio.NewReader(bytes.NewReader(huge))); err == nil {
		t.Fatal("an oversized capsule was accepted")
	}
	var stream bytes.Buffer
	_ = WriteDatagram(&stream, []byte("payload"))
	truncated := stream.Bytes()[:stream.Len()-2]
	if _, err := ReadDatagram(bufio.NewReader(bytes.NewReader(truncated))); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated capsule = %v, want unexpected EOF", err)
	}
}
