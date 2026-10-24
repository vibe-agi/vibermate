//go:build linux || darwin

package desktophost_test

import (
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/flowbroker"
)

// The Host publishes transparent-capture rendezvous only to the native shell:
// a private registration socket and a loopback flow port guarded by a secret.
// Shutdown ends live registrations so runs stop their Agents.
func TestHostServesTransparentCaptureRendezvous(t *testing.T) {
	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	options := hostOptions(t, paths, filepath.Join(root, "data"))
	options.TransparentCapture = true
	host := startHost(t, options)

	bootstrap := host.Bootstrap()
	if bootstrap.FlowPort == 0 || len(bootstrap.FlowSecret) != 43 {
		t.Fatalf("bootstrap flow rendezvous = port %d, secret length %d", bootstrap.FlowPort, len(bootstrap.FlowSecret))
	}
	info, err := os.Stat(paths.FlowRegistrationSocket())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Type() != os.ModeSocket || info.Mode().Perm() != 0o600 {
		t.Fatalf("registration socket mode = %v", info.Mode())
	}

	received := make(chan string, 1)
	registration, err := flowbroker.Register(paths.FlowRegistrationSocket(), "run-1", func(flow net.Conn) {
		defer flow.Close()
		data, _ := io.ReadAll(flow)
		received <- string(data)
	})
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })

	conn, err := net.Dial("tcp4", "127.0.0.1:"+strconv.Itoa(bootstrap.FlowPort))
	if err != nil {
		t.Fatal(err)
	}
	if err := flowbroker.WriteFlowRequest(conn, child.Process.Pid, bootstrap.FlowSecret); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	answer := make([]byte, 1)
	if _, err := io.ReadFull(conn, answer); err != nil || answer[0] != 'Y' {
		t.Fatalf("flow answer = %q, %v; want owned", answer, err)
	}
	_, _ = io.WriteString(conn, "flow-bytes")
	_ = conn.(*net.TCPConn).CloseWrite()
	select {
	case data := <-received:
		if data != "flow-bytes" {
			t.Fatalf("run received %q", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run received no flow")
	}
	conn.Close()

	shutdownHost(t, host)
	select {
	case <-registration.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("registration survived Host shutdown")
	}
	registration.Close()
	if _, err := os.Lstat(paths.FlowRegistrationSocket()); !os.IsNotExist(err) {
		t.Fatalf("registration socket remains after shutdown: %v", err)
	}
}

func TestHostWithoutTransparentCapturePublishesNoRendezvous(t *testing.T) {
	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	host := startHost(t, hostOptions(t, paths, filepath.Join(root, "data")))
	defer shutdownHost(t, host)
	if bootstrap := host.Bootstrap(); bootstrap.FlowPort != 0 || bootstrap.FlowSecret != "" {
		t.Fatalf("disabled Host published flow rendezvous: %+v", bootstrap)
	}
	if _, err := os.Lstat(paths.FlowRegistrationSocket()); !os.IsNotExist(err) {
		t.Fatalf("disabled Host created a registration socket: %v", err)
	}
}
