package vlesscore

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"
)

// This uses the actual packaged helper and a real authenticated fixture route.
// A failed outer handshake must not retain a socket indefinitely, kill the
// shared helper, or impose the setup deadline on an established connection.
func TestCoreOuterSetupBudgetAndHealthyStream(t *testing.T) {
	helper := os.Getenv("VIBERMATE_TEST_XRAY_BINARY")
	if helper == "" {
		t.Skip("explicit qualified helper required")
	}
	for _, mode := range []string{"tls-closed", "utls-open", "reality-closed"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			owner, stop := context.WithTimeout(t.Context(), 70*time.Second)
			defer stop()
			var config Config
			var target netip.AddrPort
			if mode == "reality-closed" {
				config, target, _, _, _ = coreFixture(t, owner, helper, time.Minute)
			} else {
				config, target, _, _ = tlsCoreFixture(t, owner, helper, time.Minute)
				if mode == "utls-open" {
					config.Fingerprint = "chrome"
				}
			}
			node, stalled := setupNodeSwitch(t, config.Server)
			config.Server = node
			client, err := Start(owner, config)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			healthy, err := client.DialContext(owner, "tcp", target.String())
			if err != nil {
				t.Fatal(err)
			}
			defer healthy.Close()
			healthy.SetDeadline(time.Now().Add(5 * time.Second))
			echo(t, healthy, []byte("healthy before stalled setup"))
			healthy.SetDeadline(time.Time{})
			abandoned, err := client.DialContext(owner, "tcp", target.String())
			if err != nil {
				t.Fatal(err)
			}
			defer abandoned.Close()
			var outer net.Conn
			select {
			case outer = <-stalled:
			case <-owner.Done():
				t.Fatal("outer setup not reached")
			}
			start := time.Now()
			outer.SetReadDeadline(start.Add(3 * time.Second))
			header := make([]byte, 5)
			if _, err := io.ReadFull(outer, header); err != nil || header[0] != 22 {
				t.Fatalf("ClientHello missing: %v", err)
			}
			if _, err := io.CopyN(io.Discard, outer, int64(binary.BigEndian.Uint16(header[3:]))); err != nil {
				t.Fatal(err)
			}
			if mode != "utls-open" {
				abandoned.Close()
			}
			outer.SetReadDeadline(start.Add(33 * time.Second))
			_, err = io.Copy(io.Discard, outer)
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("outer setup exceeded common 30-second budget plus 3-second allowance")
			}
			if elapsed := time.Since(start); elapsed < 28*time.Second {
				t.Fatalf("setup ended before fixture budget: %s (%v)", elapsed, err)
			}
			select {
			case <-client.Done():
				t.Fatal("one failed setup stopped shared helper")
			default:
			}
			// This connection's own setup deadline has also elapsed by now.
			healthy.SetDeadline(time.Now().Add(5 * time.Second))
			echo(t, healthy, []byte("healthy after setup deadline"))
			fresh, err := client.DialContext(owner, "tcp", target.String())
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			fresh.SetDeadline(time.Now().Add(5 * time.Second))
			echo(t, fresh, []byte("fresh stream on same helper"))
			t.Logf("%s: failed setup retired in %s; established and fresh streams intact", mode, time.Since(start))
		})
	}
}

// The second node connection consumes ClientHello but does not reply. All other
// connections transparently reach the owned authentic TLS/REALITY fixture node.
func setupNodeSwitch(t *testing.T, target netip.AddrPort) (netip.AddrPort, <-chan net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stalled := make(chan net.Conn, 1)
	var mu sync.Mutex
	var connections []net.Conn
	closing := false
	var workers sync.WaitGroup
	acceptDone := make(chan struct{})
	track := func(c net.Conn) {
		mu.Lock()
		if closing {
			c.Close()
		} else {
			connections = append(connections, c)
		}
		mu.Unlock()
	}
	go func() {
		defer close(acceptDone)
		for count := 1; ; count++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			track(conn)
			if count == 2 {
				stalled <- conn
				continue
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				peer, err := net.DialTimeout("tcp", target.String(), 3*time.Second)
				if err != nil {
					return
				}
				track(peer)
				defer peer.Close()
				copied := make(chan struct{})
				go func() { io.Copy(peer, conn); peer.(*net.TCPConn).CloseWrite(); close(copied) }()
				io.Copy(conn, peer)
				conn.(*net.TCPConn).CloseWrite()
				<-copied
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-acceptDone
		mu.Lock()
		closing = true
		for _, conn := range connections {
			conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return netip.MustParseAddrPort(listener.Addr().String()), stalled
}
