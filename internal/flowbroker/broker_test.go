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

func startBroker(t *testing.T, allow func(Peer) bool) (registrations, flows string) {
	t.Helper()
	broker, err := New(processlineage.System(), allow)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
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
	return registrations, flows
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
	if err := WriteFlowRequest(conn, pid); err != nil {
		t.Fatal(err)
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
	registrations, flows := startBroker(t, sameUser)
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
	registrations, flows := startBroker(t, sameUser)
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
	registrations, flows := startBroker(t, func(Peer) bool { return false })
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
	registrations, _ := startBroker(t, sameUser)
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
