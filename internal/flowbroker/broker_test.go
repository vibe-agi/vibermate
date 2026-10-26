//go:build linux || darwin

package flowbroker

import (
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/processlineage"
)

func startBroker(t *testing.T, allow func(Peer) bool) (broker *Broker, registrations, flows string) {
	t.Helper()
	broker, err := New(processlineage.System(), allow)
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

func TestOneLiveRegistrationPerProcess(t *testing.T) {
	_, registrations, _ := startBroker(t, sameUser)
	first, err := Register(registrations, "run-a", func(flow net.Conn) { flow.Close() })
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := Register(registrations, "run-b", func(flow net.Conn) { flow.Close() }); err == nil {
		second.Close()
		t.Fatal("a process registered as two runs")
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
	broker, registrations, _ := startBroker(t, sameUser)
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	secret := "0123456789abcdef0123456789abcdef"
	go broker.ServeFlowsTCP(listener, []byte(secret))
	now := time.Now()
	broker.now = func() time.Time { return now }
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
	now = now.Add(extensionFreshness)
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
