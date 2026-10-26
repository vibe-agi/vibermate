//go:build linux || darwin

package flowbroker

import (
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/processlineage"
)

func startBroker(t *testing.T, allow func(Peer) bool) (broker *Broker, registrations, flows string) {
	t.Helper()
	return startBrokerWith(t, allow, time.Now, livenessInterval)
}

// fakeClock is safe to read from the broker's monitor goroutine.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) advance(by time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(by)
}

func startBrokerWith(t *testing.T, allow func(Peer) bool, now func() time.Time, interval time.Duration) (broker *Broker, registrations, flows string) {
	t.Helper()
	broker, err := newBroker(processlineage.System(), allow, now, interval)
	if err != nil {
		t.Fatal(err)
	}
	directory := shortSocketDir(t)
	registrations = filepath.Join(directory, "runs.sock")
	flows = filepath.Join(directory, "flows.sock")
	for path, serve := range map[string]func(*net.UnixListener) error{
		registrations: broker.ServeRegistrations,
		flows:         broker.ServeFlows,
	} {
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { listener.Close() })
		go serve(listener)
	}
	return broker, registrations, flows
}

func startChild(t *testing.T) int {
	t.Helper()
	command := exec.Command("sleep", "30")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	return command.Process.Pid
}

// submit plays the extension: it asks about pid and, when owned, streams the
// given bytes on the same connection.
func submit(t *testing.T, flows string, pid int, payload string) byte {
	t.Helper()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: flows, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := WriteFlowRequest(conn, pid, ""); err != nil {
		// The broker may close a refused peer before the request is written.
		return 0
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	answer := make([]byte, 1)
	if _, err := io.ReadFull(conn, answer); err != nil {
		return 0
	}
	if answer[0] == flowOwned {
		if _, err := io.WriteString(conn, payload); err != nil {
			t.Fatal(err)
		}
		_ = conn.CloseWrite()
	}
	return answer[0]
}

func sameUser(peer Peer) bool { return peer.UID == os.Getuid() }

func TestOwnedFlowIsHandedToItsRun(t *testing.T) {
	_, registrations, flows := startBroker(t, sameUser)
	received := make(chan string, 1)
	registration, err := Register(registrations, "run-a", func(flow net.Conn) {
		defer flow.Close()
		data, _ := io.ReadAll(flow)
		received <- string(data)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()
	child := startChild(t)
	if answer := submit(t, flows, child, "header-and-bytes"); answer != flowOwned {
		t.Fatalf("child flow answer = %q, want owned", answer)
	}
	select {
	case data := <-received:
		if data != "header-and-bytes" {
			t.Fatalf("run received %q", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run received no flow")
	}
	if answer := submit(t, flows, os.Getppid(), ""); answer != flowUnowned {
		t.Fatalf("unrelated flow answer = %q, want unowned", answer)
	}
	if answer := submit(t, flows, os.Getpid(), ""); answer != flowUnowned {
		t.Fatalf("the launcher's own flow answer = %q, want unowned", answer)
	}
}

func TestClosingRegistrationEndsOwnership(t *testing.T) {
	_, registrations, flows := startBroker(t, sameUser)
	registration, err := Register(registrations, "run-a", func(flow net.Conn) { flow.Close() })
	if err != nil {
		t.Fatal(err)
	}
	child := startChild(t)
	if answer := submit(t, flows, child, ""); answer != flowOwned {
		t.Fatalf("answer = %q, want owned", answer)
	}
	if err := registration.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for submit(t, flows, child, "") != flowUnowned {
		if time.Now().After(deadline) {
			t.Fatal("flow still owned after the registration closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	again, err := Register(registrations, "run-b", func(flow net.Conn) { flow.Close() })
	if err != nil {
		t.Fatalf("re-register after close: %v", err)
	}
	again.Close()
}

func TestOnlyAllowedPeersMaySubmitFlows(t *testing.T) {
	_, registrations, flows := startBroker(t, func(Peer) bool { return false })
	registration, err := Register(registrations, "run-a", func(flow net.Conn) { flow.Close() })
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()
	if answer := submit(t, flows, startChild(t), ""); answer != 0 {
		t.Fatalf("disallowed peer got answer %q, want the connection closed", answer)
	}
}

// A process that registers again (it judged its claim lost) replaces its
// previous claim at once; the old claim ends and flows reach the new one.
func TestReRegistrationReplacesTheSameProcessClaim(t *testing.T) {
	_, registrations, flows := startBroker(t, sameUser)
	first, err := Register(registrations, "run-a", func(flow net.Conn) { flow.Close() })
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	received := make(chan string, 1)
	second, err := Register(registrations, "run-a", func(flow net.Conn) {
		defer flow.Close()
		data, _ := io.ReadAll(flow)
		received <- string(data)
	})
	if err != nil {
		t.Fatalf("re-registration: %v", err)
	}
	defer second.Close()
	select {
	case <-first.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the replaced claim did not end")
	}
	if answer := submit(t, flows, startChild(t), "to-new-claim"); answer != flowOwned {
		t.Fatalf("answer = %q, want owned", answer)
	}
	select {
	case data := <-received:
		if data != "to-new-claim" {
			t.Fatalf("new claim received %q", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the new claim received no flow")
	}
}

func TestLoopbackTCPFlowsNeedTheSecret(t *testing.T) {
	broker, registrations, _ := startBroker(t, sameUser)
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	secret := []byte("0123456789abcdef0123456789abcdef")
	if err := broker.ServeFlowsTCP(listener, []byte("short")); err == nil {
		t.Fatal("a short secret was accepted")
	}
	go broker.ServeFlowsTCP(listener, secret)
	received := make(chan string, 1)
	registration, err := Register(registrations, "run-a", func(flow net.Conn) {
		defer flow.Close()
		data, _ := io.ReadAll(flow)
		received <- string(data)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()
	child := startChild(t)
	submitTCP := func(secret string, payload string) byte {
		conn, err := net.DialTCP("tcp4", nil, listener.Addr().(*net.TCPAddr))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := WriteFlowRequest(conn, child, secret); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		answer := make([]byte, 1)
		if _, err := io.ReadFull(conn, answer); err != nil {
			return 0
		}
		if answer[0] == flowOwned {
			_, _ = io.WriteString(conn, payload)
			_ = conn.CloseWrite()
		}
		return answer[0]
	}
	if answer := submitTCP("wrong-secret-wrong-secret-wrong-s", ""); answer != 0 {
		t.Fatalf("wrong secret got answer %q", answer)
	}
	if answer := submitTCP("", ""); answer != 0 {
		t.Fatalf("missing secret got answer %q", answer)
	}
	if answer := submitTCP(string(secret), "tcp-flow"); answer != flowOwned {
		t.Fatalf("answer = %q, want owned", answer)
	}
	select {
	case data := <-received:
		if data != "tcp-flow" {
			t.Fatalf("run received %q", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run received no TCP flow")
	}
}

func TestCloseEndsLiveRegistrations(t *testing.T) {
	broker, registrations, flows := startBroker(t, sameUser)
	registration, err := Register(registrations, "run-a", func(flow net.Conn) { flow.Close() })
	if err != nil {
		t.Fatal(err)
	}
	broker.Close()
	select {
	case <-registration.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("registration did not observe the broker closing")
	}
	deadline := time.Now().Add(5 * time.Second)
	for submit(t, flows, startChild(t), "") != flowUnowned {
		if time.Now().After(deadline) {
			t.Fatal("flows still owned after the broker closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRegistrationReportsWhetherTheExtensionIsActive(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	broker, registrations, _ := startBrokerWith(t, sameUser, clock.Now, livenessInterval)
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	secret := "0123456789abcdef0123456789abcdef"
	go broker.ServeFlowsTCP(listener, []byte(secret))
	hello := func(secret string) byte {
		conn, err := net.DialTCP("tcp4", nil, listener.Addr().(*net.TCPAddr))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := WriteHello(conn, secret); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		answer := make([]byte, 1)
		if _, err := io.ReadFull(conn, answer); err != nil {
			return 0
		}
		return answer[0]
	}
	registerExtension := func() bool {
		registration, err := Register(registrations, "run", func(flow net.Conn) { flow.Close() })
		if err != nil {
			t.Fatal(err)
		}
		defer registration.Close()
		return registration.Extension()
	}
	if registerExtension() {
		t.Fatal("extension reported active before any hello")
	}
	if answer := hello("wrong-secret-wrong-secret-wrong-s"); answer != 0 {
		t.Fatalf("hello with the wrong secret answered %q", answer)
	}
	if registerExtension() {
		t.Fatal("a hello with the wrong secret counted")
	}
	if answer := hello(secret); answer != helloAccepted {
		t.Fatalf("hello answered %q", answer)
	}
	if !registerExtension() {
		t.Fatal("extension not reported active after a hello")
	}
	clock.advance(extensionFreshness)
	if registerExtension() {
		t.Fatal("a stale hello still counted")
	}
}

// shortSocketDir returns a directory short enough for Unix socket paths:
// macOS limits them to 104 bytes, and its t.TempDir paths are long.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "vm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	return directory
}

const testSecret = "0123456789abcdef0123456789abcdef"

// Liveness tests use their own fast interval and keepalive timeout.
const (
	fastInterval = 20 * time.Millisecond
	fastTimeout  = 200 * time.Millisecond
)

func registerFast(socket, run string, handle func(net.Conn)) (*Registration, error) {
	return register(socket, run, handle, fastTimeout)
}

func serveTCPFlows(t *testing.T, broker *Broker) *net.TCPAddr {
	t.Helper()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go broker.ServeFlowsTCP(listener, []byte(testSecret))
	return listener.Addr().(*net.TCPAddr)
}

func openLivenessStream(t *testing.T, address *net.TCPAddr, queryFailures uint64) *net.TCPConn {
	t.Helper()
	conn, err := net.DialTCP("tcp4", nil, address)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteHelloStream(conn, testSecret, queryFailures); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	answer := make([]byte, 1)
	if _, err := io.ReadFull(conn, answer); err != nil || answer[0] != helloAccepted {
		t.Fatalf("stream hello answer = %q, %v", answer, err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	return conn
}

func waitDone(t *testing.T, registration *Registration, within time.Duration, what string) {
	t.Helper()
	select {
	case <-registration.Done():
	case <-time.After(within):
		t.Fatalf("registration survived %s", what)
	}
}

// The extension crashing or stopping closes its liveness stream; every claim
// ends at once even though vibermated keeps running.
func TestClosedLivenessStreamRevokesRegistrations(t *testing.T) {
	broker, registrations, _ := startBrokerWith(t, sameUser, time.Now, fastInterval)
	stream := openLivenessStream(t, serveTCPFlows(t, broker), 7)
	registration, err := registerFast(registrations, "run", func(flow net.Conn) { flow.Close() })
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()
	if !registration.Extension() {
		t.Fatal("an open liveness stream did not count as an active extension")
	}
	if stats := broker.Stats(); !stats.ExtensionActive || stats.QueryFailures != 7 {
		t.Fatalf("stats = %+v", stats)
	}
	// Keepalives hold a healthy claim open well past the keepalive timeout.
	time.Sleep(4 * fastTimeout)
	select {
	case <-registration.Done():
		t.Fatal("a healthy claim ended")
	default:
	}
	stream.Close()
	waitDone(t, registration, 2*time.Second, "the extension's stream closing")
	if broker.Stats().ExtensionActive {
		t.Fatal("extension still active after its stream closed")
	}
}

// A hung extension that stops sending hellos (and holds no stream) loses
// liveness once its last hello is stale; claims are revoked.
func TestStaleHelloRevokesRegistrations(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	broker, registrations, _ := startBrokerWith(t, sameUser, clock.Now, fastInterval)
	address := serveTCPFlows(t, broker)
	conn, err := net.DialTCP("tcp4", nil, address)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteHello(conn, testSecret); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	registration, err := registerFast(registrations, "run", func(flow net.Conn) { flow.Close() })
	if err != nil || !registration.Extension() {
		t.Fatalf("registration = %v, extension %t", err, err == nil && registration.Extension())
	}
	defer registration.Close()
	clock.advance(extensionFreshness)
	waitDone(t, registration, 2*time.Second, "the extension's hello going stale")
}

// A broker that stops answering (hung, not exited) must not leave runs
// believing they are captured.
func TestSilentBrokerEndsTheClaim(t *testing.T) {
	socket := filepath.Join(shortSocketDir(t), "hung.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = readLine(conn)
		_, _ = io.WriteString(conn, `{"ok":true,"extension":true}`+"\n")
		time.Sleep(10 * time.Second)
	}()
	registration, err := registerFast(socket, "run", func(flow net.Conn) { flow.Close() })
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()
	waitDone(t, registration, 5*fastTimeout, "a broker that stopped sending keepalives")
}
