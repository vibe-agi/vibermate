package socksproxy

import (
	"bytes"
	"testing"
)

// Dropping IPv6/domain forms or accepting incomplete authorities breaks this.
func TestPacketAddressForms(t *testing.T) {
	for _, test := range []struct {
		wire []byte
		host string
		port uint16
	}{
		{[]byte{1, 127, 0, 0, 1, 1, 187}, "127.0.0.1", 443},
		{[]byte{4, 0x20, 1, 0xd, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 53}, "2001:db8::1", 53},
		{append([]byte{3, 12}, append([]byte("echo.invalid"), 0, 53)...), "echo.invalid", 53},
	} {
		got, e := readTarget(bytes.NewReader(test.wire))
		if e != nil || got.Host != test.host || got.Port != test.port {
			t.Fatalf("address form got=%v err=%v", got, e)
		}
		for n := 0; n < len(test.wire); n++ {
			if _, e := readTarget(bytes.NewReader(test.wire[:n])); e == nil {
				t.Fatalf("incomplete packet length %d accepted", n)
			}
		}
	}
	for _, wire := range [][]byte{{3, 0, 0, 1}, {3, 3, 'a', '\r', 'b', 0, 1}, {9, 0, 0}} {
		if _, e := readTarget(bytes.NewReader(wire)); e == nil {
			t.Fatal("invalid address accepted")
		}
	}
}
