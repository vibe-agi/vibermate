package socksproxy

import (
	"bytes"
	"errors"
	"net"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// A wrong platform mask, or a predicate that always returns true or false,
// must misclassify one of these actual received datagrams.
func TestUDPMessageTruncatedReceivedFlags(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bufferLen int
		truncated bool
	}{{"complete", 64, false}, {"truncated", 3, true}} {
		t.Run(tc.name, func(t *testing.T) {
			receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { receiver.Close() })
			sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sender.Close() })
			deadline := time.Now().Add(5 * time.Second)
			if err := receiver.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			if err := sender.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			payload := []byte{0, 255, 1, 128, 0, 42, 17, 254}
			if n, err := sender.WriteToUDPAddrPort(payload, receiver.LocalAddr().(*net.UDPAddr).AddrPort()); err != nil || n != len(payload) {
				t.Fatalf("send binary datagram: n=%d err=%v", n, err)
			}
			buffer := make([]byte, tc.bufferLen)
			n, _, flags, source, err := receiver.ReadMsgUDPAddrPort(buffer, nil)
			t.Logf("received n=%d flags=%#x truncated=%t err=%v", n, flags, udpMessageTruncated(flags), err)
			if udpMessageTruncated(flags) != tc.truncated {
				t.Fatalf("received datagram truncation classification mismatch: flags=%#x want=%t err=%v", flags, tc.truncated, err)
			}
			// Winsock may additionally report the documented WSAEMSGSIZE (10040).
			if err != nil && !(tc.truncated && runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(10040))) {
				t.Fatalf("receive datagram: %v", err)
			}
			if !tc.truncated {
				if source != sender.LocalAddr().(*net.UDPAddr).AddrPort() || n != len(payload) || !bytes.Equal(buffer[:n], payload) {
					t.Fatalf("complete datagram changed: n=%d source=%s bytes=%x", n, source, buffer[:n])
				}
			}
		})
	}
}
