//go:build linux

// Command vibermate-transparent-emulator stands in for the macOS Network
// Extension on Linux, so tool/transparent-linux-e2e.sh can exercise the shared
// Go path end to end: broker liveness, ownership queries, flow hand-off,
// ingress and the Runtime. It is a test harness, never shipped, and is not
// Linux transparent capture (ADR 0024 plans a network namespace for that).
//
// It runs as root. nftables redirects one test user's outbound TCP to its
// listener; for each connection it recovers the original destination and the
// source process, asks the broker exactly as the extension does, then relays
// an owned flow to the broker or dials an unowned one directly.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/vibe-agi/vibermate/internal/flowbroker"
	"github.com/vibe-agi/vibermate/internal/flowingress"
	"github.com/vibe-agi/vibermate/internal/loopbackclient"
	"github.com/vibe-agi/vibermate/internal/processlineage"
)

const queryTimeout = 200 * time.Millisecond

type emulator struct {
	broker        string // http://127.0.0.1:<flowPort>, for loopbackclient
	secret        string
	queryFailures atomic.Uint64

	mu    sync.Mutex
	owned map[processlineage.Identity]bool
}

func main() {
	flowPort := flag.Int("flow-port", 0, "broker flow port from the bootstrap descriptor")
	secretFile := flag.String("flow-secret-file", "", "file holding the broker flow secret")
	listen := flag.String("listen", "", "address nftables redirects to; empty runs liveness only")
	flag.Parse()
	secret, err := os.ReadFile(*secretFile)
	if *flowPort <= 0 || err != nil {
		log.Fatalf("flow port and secret file are required: %v", err)
	}
	e := &emulator{
		broker: "http://127.0.0.1:" + strconv.Itoa(*flowPort),
		secret: strings.TrimSpace(string(secret)),
		owned:  map[processlineage.Identity]bool{},
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go e.liveness(ctx)
	if *listen == "" {
		<-ctx.Done()
		return
	}
	listener, err := net.Listen("tcp4", *listen)
	if err != nil {
		log.Fatal(err)
	}
	context.AfterFunc(ctx, func() { listener.Close() })
	log.Printf("intercepting on %s", listener.Addr())
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go e.handle(conn.(*net.TCPConn))
	}
}

func (e *emulator) dialBroker(ctx context.Context) (net.Conn, error) {
	client, err := loopbackclient.New(e.broker, queryTimeout)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return client.Dial(ctx)
}

// liveness mirrors the provider: a stream opened at start and reopened when
// it closes, plus a hello every 10 seconds carrying the failure count.
func (e *emulator) liveness(ctx context.Context) {
	var stream net.Conn
	defer func() {
		if stream != nil {
			stream.Close()
		}
	}()
	open := func() {
		conn, err := e.announce(ctx, true)
		if err != nil {
			log.Printf("liveness stream: %v", err)
			return
		}
		stream = conn
		log.Printf("liveness stream open")
		go func(conn net.Conn) {
			_, _ = io.Copy(io.Discard, conn)
		}(conn)
	}
	open()
	streamCheck := time.NewTicker(time.Second)
	hello := time.NewTicker(10 * time.Second)
	defer streamCheck.Stop()
	defer hello.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hello.C:
			if conn, err := e.announce(ctx, false); err == nil {
				conn.Close()
			}
		case <-streamCheck.C:
			if stream == nil || peerClosed(stream) {
				if stream != nil {
					stream.Close()
				}
				stream = nil
				open()
			}
		}
	}
}

func (e *emulator) announce(ctx context.Context, stream bool) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	conn, err := e.dialBroker(ctx)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(queryTimeout))
	if stream {
		err = flowbroker.WriteHelloStream(conn, e.secret, e.queryFailures.Load())
	} else {
		err = flowbroker.WriteHello(conn, e.secret)
	}
	answer := make([]byte, 1)
	if err == nil {
		_, err = io.ReadFull(conn, answer)
	}
	if err == nil && answer[0] != 'H' {
		err = fmt.Errorf("hello answer %q", answer)
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func peerClosed(conn net.Conn) bool {
	_ = conn.SetReadDeadline(time.Now().Add(time.Millisecond))
	defer conn.SetReadDeadline(time.Time{})
	_, err := conn.Read(make([]byte, 1))
	var timeout net.Error
	return !(errors.As(err, &timeout) && timeout.Timeout())
}

func (e *emulator) handle(client *net.TCPConn) {
	defer client.Close()
	destination, err := originalDestination(client)
	if err != nil {
		log.Printf("flow: no original destination: %v", err)
		return
	}
	peer := netip.MustParseAddrPort(client.RemoteAddr().String())
	pid := pidOfSocket(peer)
	process, _ := processlineage.System().Read(pid)
	decision := "direct"
	defer func() { log.Printf("flow pid=%d dst=%s decision=%s", pid, destination, decision) }()
	if pid > 1 {
		broker, answer, err := e.query(pid)
		switch {
		case err != nil:
			e.queryFailures.Add(1)
			if e.wasOwned(process.Identity) {
				decision = "refused"
				return
			}
			decision = "direct-after-failure"
		case answer == 'Y':
			e.remember(process.Identity)
			decision = "owned"
			defer broker.Close()
			if err := flowingress.WriteHeader(broker, flowingress.Header{Version: 1, Protocol: "tcp", Destination: destination}); err != nil {
				decision = "owned-header-failed"
				return
			}
			splice(client, broker)
			return
		}
	}
	upstream, err := dialDirect(destination)
	if err != nil {
		decision += "-dial-failed"
		return
	}
	defer upstream.Close()
	splice(client, upstream)
}

func (e *emulator) query(pid int) (net.Conn, byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	conn, err := e.dialBroker(ctx)
	if err != nil {
		return nil, 0, err
	}
	_ = conn.SetDeadline(time.Now().Add(queryTimeout))
	answer := make([]byte, 1)
	if err = flowbroker.WriteFlowRequest(conn, pid, e.secret); err == nil {
		_, err = io.ReadFull(conn, answer)
	}
	if err != nil || (answer[0] != 'Y' && answer[0] != 'N') {
		conn.Close()
		if err == nil {
			err = fmt.Errorf("query answer %q", answer)
		}
		return nil, 0, err
	}
	_ = conn.SetDeadline(time.Time{})
	if answer[0] == 'N' {
		conn.Close()
		return nil, 'N', nil
	}
	return conn, 'Y', nil
}

func (e *emulator) wasOwned(identity processlineage.Identity) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.owned[identity]
}

func (e *emulator) remember(identity processlineage.Identity) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.owned[identity] = true
}

// originalDestination reads SO_ORIGINAL_DST, which nftables redirect records
// through conntrack. IPv4 only, like the redirect rule.
func originalDestination(conn *net.TCPConn) (netip.AddrPort, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}
	var address netip.AddrPort
	var lookupErr error
	if err := raw.Control(func(fd uintptr) {
		const soOriginalDst = 80
		mreq, err := syscall.GetsockoptIPv6Mreq(int(fd), syscall.SOL_IP, soOriginalDst)
		if err != nil {
			lookupErr = err
			return
		}
		// struct sockaddr_in: family(2) port(2, big endian) addr(4).
		bytes := mreq.Multiaddr
		port := uint16(bytes[2])<<8 | uint16(bytes[3])
		address = netip.AddrPortFrom(netip.AddrFrom4([4]byte{bytes[4], bytes[5], bytes[6], bytes[7]}), port)
	}); err != nil {
		return netip.AddrPort{}, err
	}
	return address, lookupErr
}

// pidOfSocket finds the process owning the client socket whose local address
// is peer: /proc/net/tcp gives its inode, /proc/<pid>/fd names it. The client
// socket keeps its un-NATed tuple, so its local address is the peer we see.
func pidOfSocket(peer netip.AddrPort) int {
	data, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		return 0
	}
	inode := ""
	for _, line := range strings.Split(string(data), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		if local, ok := parseProcAddress(fields[1]); ok && local == peer {
			inode = fields[9]
			break
		}
	}
	if inode == "" || inode == "0" {
		return 0
	}
	target := "socket:[" + inode + "]"
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		fds, _ := os.ReadDir("/proc/" + entry.Name() + "/fd")
		for _, fd := range fds {
			if link, _ := os.Readlink("/proc/" + entry.Name() + "/fd/" + fd.Name()); link == target {
				return pid
			}
		}
	}
	return 0
}

// parseProcAddress decodes "0100007F:1F90": a little-endian IPv4 address in
// hex and a big-endian port in hex.
func parseProcAddress(field string) (netip.AddrPort, bool) {
	host, port, ok := strings.Cut(field, ":")
	if !ok || len(host) != 8 {
		return netip.AddrPort{}, false
	}
	value, err := strconv.ParseUint(host, 16, 32)
	number, portErr := strconv.ParseUint(port, 16, 16)
	if err != nil || portErr != nil {
		return netip.AddrPort{}, false
	}
	address := netip.AddrFrom4([4]byte{byte(value), byte(value >> 8), byte(value >> 16), byte(value >> 24)})
	return netip.AddrPortFrom(address, uint16(number)), true
}

func splice(a, b net.Conn) {
	var wait sync.WaitGroup
	wait.Add(2)
	copyHalf := func(to, from net.Conn) {
		defer wait.Done()
		_, _ = io.Copy(to, from)
		if half, ok := to.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		}
	}
	go copyHalf(a, b)
	go copyHalf(b, a)
	wait.Wait()
}
