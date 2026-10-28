package flowingress

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectudp"
)

func TestDatagramFramesRoundTripAtTheirLimits(t *testing.T) {
	endpoint := netip.MustParseAddrPort("[2001:db8::1]:443")
	payload := bytes.Repeat([]byte{0xa5}, connectudp.MaxDatagram)
	var buffer bytes.Buffer
	if err := WriteDatagram(&buffer, endpoint, payload); err != nil {
		t.Fatal(err)
	}
	got, data, err := ReadDatagram(&buffer)
	if err != nil || got != endpoint || !bytes.Equal(data, payload) {
		t.Fatalf("round trip = %v, %d bytes, %v", got, len(data), err)
	}
	if err := WriteDatagram(io.Discard, endpoint, make([]byte, connectudp.MaxDatagram+1)); err == nil {
		t.Fatal("an oversized datagram was framed")
	}
	frame := func(length uint32, body []byte) *bytes.Reader {
		header := binary.BigEndian.AppendUint32(nil, length)
		return bytes.NewReader(append(header, body...))
	}
	for name, input := range map[string]*bytes.Reader{
		"empty":         frame(0, nil),
		"too long":      frame(maxDatagramFrame+1, nil),
		"no endpoint":   frame(1, []byte{0}),
		"short":         frame(5, []byte{9, '1', '.', '2', '3'}),
		"not an IP":     frame(16, append([]byte{15}, "example.com:443"...)),
		"port zero":     frame(10, append([]byte{9}, "1.2.3.4:0"...)),
		"bad addrport":  frame(8, append([]byte{7}, "1.2.3.4"...)),
		"truncated end": frame(20, append([]byte{9}, "1.2.3.4:5"...)),
	} {
		if _, _, err := ReadDatagram(input); err == nil {
			t.Errorf("%s frame was accepted", name)
		}
	}
}

func TestUDPHeaderNamesNoDestination(t *testing.T) {
	var buffer bytes.Buffer
	if err := WriteHeader(&buffer, Header{Version: 1, Protocol: "udp"}); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String()[2:]; got != `{"v":1,"proto":"udp"}` {
		t.Fatalf("UDP header = %s", got)
	}
	if header, err := ReadHeader(&buffer); err != nil || header.Protocol != "udp" {
		t.Fatalf("read back %+v, %v", header, err)
	}
	for _, header := range []Header{
		{Version: 1, Protocol: "udp", Destination: netip.MustParseAddrPort("1.2.3.4:53")},
		{Version: 1, Protocol: "udp", Host: "example.com"},
		{Version: 1, Protocol: "tcp"},
		{Version: 1, Protocol: "quic"},
	} {
		if err := header.validate(); err == nil {
			t.Errorf("%+v was accepted", header)
		}
	}
}

type udpRequest struct{ target, credential string }

// fakeUDPRuntime answers connect-udp by echoing datagrams, refuses targets
// in refuse with 403, and records every request.
func fakeUDPRuntime(t *testing.T, refuse map[string]bool) (string, func() []udpRequest) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	var mu sync.Mutex
	var requests []udpRequest
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				request, err := http.ReadRequest(reader)
				if err != nil {
					return
				}
				host, port, err := connectudp.ParsePath(request.URL.EscapedPath())
				if err != nil {
					return
				}
				target := net.JoinHostPort(host, fmt.Sprint(port))
				mu.Lock()
				requests = append(requests, udpRequest{target, request.Header.Get("Proxy-Authorization")})
				mu.Unlock()
				if refuse[target] {
					_, _ = io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
					return
				}
				_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: connect-udp\r\nCapsule-Protocol: ?1\r\n\r\n")
				for {
					payload, err := connectudp.ReadDatagram(reader)
					if err != nil {
						return
					}
					if connectudp.WriteDatagram(conn, append([]byte(target+":"), payload...)) != nil {
						return
					}
				}
			}()
		}
	}()
	return "http://" + listener.Addr().String(), func() []udpRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]udpRequest(nil), requests...)
	}
}

// startUDPFlow serves one UDP flow and returns the extension's end of it.
func startUDPFlow(t *testing.T, origin string) net.Conn {
	t.Helper()
	ingress, err := New(func() (Proxy, error) { return Proxy{Origin: origin, Token: "run-capability"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	extension, flow := net.Pipe()
	served := make(chan error, 1)
	go func() { served <- ingress.Serve(context.Background(), flow) }()
	t.Cleanup(func() {
		extension.Close()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("UDP flow did not end after the extension closed")
		}
	})
	if err := WriteHeader(extension, Header{Version: 1, Protocol: "udp"}); err != nil {
		t.Fatal(err)
	}
	return extension
}

// One application socket talks to two destinations; each reply comes back
// framed with the endpoint that sent it, and the Runtime saw each
// destination once, with the run's credential.
func TestUDPFlowRelaysEachDestinationThroughTheRuntime(t *testing.T) {
	origin, requests := fakeUDPRuntime(t, nil)
	extension := startUDPFlow(t, origin)
	first := netip.MustParseAddrPort("192.0.2.10:443")
	second := netip.MustParseAddrPort("[2001:db8::2]:5353")
	go func() {
		for _, datagram := range []struct {
			to      netip.AddrPort
			payload string
		}{{first, "one"}, {second, "two"}, {first, "three"}} {
			if WriteDatagram(extension, datagram.to, []byte(datagram.payload)) != nil {
				return
			}
		}
	}()
	_ = extension.SetReadDeadline(time.Now().Add(10 * time.Second))
	got := map[string]bool{}
	for range 3 {
		from, payload, err := ReadDatagram(extension)
		if err != nil {
			t.Fatal(err)
		}
		got[from.String()+" "+string(payload)] = true
	}
	for _, want := range []string{
		"192.0.2.10:443 192.0.2.10:443:one",
		"[2001:db8::2]:5353 [2001:db8::2]:5353:two",
		"192.0.2.10:443 192.0.2.10:443:three",
	} {
		if !got[want] {
			t.Errorf("missing reply %q in %v", want, got)
		}
	}
	credential := "Basic " + base64.StdEncoding.EncodeToString([]byte("capture:run-capability"))
	seen := map[string]int{}
	for _, request := range requests() {
		if request.credential != credential {
			t.Errorf("request for %s carried %q", request.target, request.credential)
		}
		seen[request.target]++
	}
	if seen["192.0.2.10:443"] != 1 || seen["[2001:db8::2]:5353"] != 1 || len(seen) != 2 {
		t.Fatalf("Runtime requests = %v, want one per destination", seen)
	}
}

// A destination the Runtime refuses is asked about once, not once per
// datagram, and its datagrams are dropped.
func TestUDPFlowAsksOnceForARefusedDestination(t *testing.T) {
	origin, requests := fakeUDPRuntime(t, map[string]bool{"198.51.100.7:9": true})
	extension := startUDPFlow(t, origin)
	refused := netip.MustParseAddrPort("198.51.100.7:9")
	allowed := netip.MustParseAddrPort("198.51.100.8:9")
	refusedCount := func() int {
		count := 0
		for _, request := range requests() {
			if request.target == "198.51.100.7:9" {
				count++
			}
		}
		return count
	}
	go func() {
		_ = WriteDatagram(extension, refused, []byte("x"))
		// Send the rest only after the Runtime has refused the first.
		for deadline := time.Now().Add(5 * time.Second); refusedCount() == 0 && time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		for range 4 {
			_ = WriteDatagram(extension, refused, []byte("x"))
			time.Sleep(10 * time.Millisecond)
		}
		_ = WriteDatagram(extension, allowed, []byte("after"))
	}()
	_ = extension.SetReadDeadline(time.Now().Add(10 * time.Second))
	from, payload, err := ReadDatagram(extension)
	if err != nil || from != allowed || string(payload) != "198.51.100.8:9:after" {
		t.Fatalf("reply = %v %q %v", from, payload, err)
	}
	if count := refusedCount(); count != 1 {
		t.Fatalf("refused destination was requested %d times, want 1", count)
	}
}

// A flow opens at most maxDatagramSessions destinations at once.
func TestUDPFlowBoundsItsDestinations(t *testing.T) {
	origin, requests := fakeUDPRuntime(t, nil)
	extension := startUDPFlow(t, origin)
	go func() {
		for i := range maxDatagramSessions + 8 {
			endpoint := netip.AddrPortFrom(netip.AddrFrom4([4]byte{203, 0, 113, byte(i)}), 7)
			if WriteDatagram(extension, endpoint, []byte("x")) != nil {
				return
			}
		}
	}()
	_ = extension.SetReadDeadline(time.Now().Add(10 * time.Second))
	for range maxDatagramSessions {
		if _, _, err := ReadDatagram(extension); err != nil {
			t.Fatal(err)
		}
	}
	_ = extension.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := ReadDatagram(extension); err == nil {
		t.Fatal("a destination beyond the bound was relayed")
	}
	if got := len(requests()); got != maxDatagramSessions {
		t.Fatalf("Runtime requests = %d, want %d", got, maxDatagramSessions)
	}
}

func TestDirectTargetUsesTheNameOnlyForSyntheticAddresses(t *testing.T) {
	for _, test := range []struct {
		name    string
		address string
		want    string
	}{
		{"svc.corp.example", "198.18.1.7:443", "svc.corp.example:443"},
		{"svc.corp.example", "10.1.2.3:443", "10.1.2.3:443"},
		{"", "192.168.1.5:22", "192.168.1.5:22"},
		{"", "[::ffff:10.0.0.1]:80", "10.0.0.1:80"},
	} {
		got, err := directTarget(test.name, netip.MustParseAddrPort(test.address))
		if err != nil || got != test.want {
			t.Errorf("directTarget(%q, %s) = %q, %v; want %q", test.name, test.address, got, err, test.want)
		}
	}
	if _, err := directTarget("", netip.MustParseAddrPort("198.18.1.7:443")); err == nil {
		t.Error("a synthetic address without a name was dialable")
	}
}

// ADR 0025: datagrams to a destination the scope covers leave directly; the
// Runtime is never asked.
func TestUDPFlowSendsBypassedDestinationsDirectly(t *testing.T) {
	echo, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, peer, err := echo.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = echo.WriteTo(append([]byte("direct:"), buffer[:n]...), peer)
		}
	}()
	origin, requests := fakeUDPRuntime(t, nil)
	extension := startUDPFlow(t, origin)
	local := netip.MustParseAddrPort(echo.LocalAddr().String())
	go func() { _ = WriteDatagram(extension, local, []byte("ping")) }()
	_ = extension.SetReadDeadline(time.Now().Add(10 * time.Second))
	from, payload, err := ReadDatagram(extension)
	if err != nil || from != local || string(payload) != "direct:ping" {
		t.Fatalf("reply = %v %q %v", from, payload, err)
	}
	if seen := requests(); len(seen) != 0 {
		t.Fatalf("the Runtime was asked about a bypassed destination: %+v", seen)
	}
}
