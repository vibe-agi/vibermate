//go:build linux || darwin

package desktophost_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
	"github.com/vibe-agi/vibermate/internal/flowbroker"
)

// The Host publishes transparent-capture rendezvous only to the native shell:
// a private registration socket and a loopback flow port guarded by a secret.
// Shutdown ends live registrations so runs stop their Agents.
func TestHostServesTransparentCaptureRendezvous(t *testing.T) {
	root := shortSocketDir(t)
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	options := hostOptions(t, paths, filepath.Join(root, "data"))
	options.TransparentCapture = true
	host := startHost(t, options)

	bootstrap := host.Bootstrap()
	if bootstrap.FlowPort == 0 || len(bootstrap.FlowSecret) != 43 {
		t.Fatalf("bootstrap flow rendezvous = port %d, secret length %d", bootstrap.FlowPort, len(bootstrap.FlowSecret))
	}
	if _, present := bootstrapJSONFields(t, bootstrap)["transparentCaptureError"]; present {
		t.Fatal("successful capture bootstrap included a failure reason")
	}
	info, err := os.Stat(paths.FlowRegistrationSocket())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Type() != os.ModeSocket || info.Mode().Perm() != 0o600 {
		t.Fatalf("registration socket mode = %v", info.Mode())
	}

	// Play the extension: hold a liveness stream open for the whole test.
	stream, err := net.Dial("tcp4", "127.0.0.1:"+strconv.Itoa(bootstrap.FlowPort))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if err := flowbroker.WriteHelloStream(stream, bootstrap.FlowSecret, 3); err != nil {
		t.Fatal(err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(stream, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if status := host.Status(); !status.TransparentCaptureExtensionActive || status.TransparentCaptureQueryFailures != 3 {
		t.Fatalf("status = %+v, want the extension active with its reported failures", status)
	}

	received := make(chan string, 1)
	registration, err := flowbroker.Register(paths.FlowRegistrationSocket(), "run-1", "", func(flow net.Conn) {
		defer flow.Close()
		data, _ := io.ReadAll(flow)
		received <- string(data)
	})

	if err != nil {
		t.Fatal(err)
	}
	if !registration.Extension() {
		t.Fatal("registration did not see the active extension")
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
	if _, present := bootstrapJSONFields(t, host.Bootstrap())["transparentCaptureError"]; present {
		t.Fatal("disabled capture bootstrap included a failure reason")
	}
	if _, err := os.Lstat(paths.FlowRegistrationSocket()); !os.IsNotExist(err) {
		t.Fatalf("disabled Host created a registration socket: %v", err)
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

// An unusable rendezvous disables transparent capture visibly; it must not
// keep the Desktop Host, and every ordinary capture, from starting.
func TestHostStartsWithoutAnUnusableTransparentCaptureRendezvous(t *testing.T) {
	root := filepath.Join(shortSocketDir(t), strings.Repeat("long-cache-directory-", 5))
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	options := hostOptions(t, paths, filepath.Join(root, "data"))
	options.TransparentCapture = true
	host := startHost(t, options)
	defer shutdownHost(t, host)
	status := host.Status()
	if !status.Ready || status.TransparentCaptureError == "" {
		t.Fatalf("status = %+v, want a ready Host reporting why capture is unavailable", status)
	}
	if bootstrap := host.Bootstrap(); bootstrap.FlowPort != 0 || bootstrap.FlowSecret != "" {
		t.Fatalf("unusable rendezvous was published: port %d", bootstrap.FlowPort)
	}
	var reason string
	if err := json.Unmarshal(bootstrapJSONFields(t, host.Bootstrap())["transparentCaptureError"], &reason); err != nil {
		t.Fatalf("bootstrap did not carry the capture startup failure: %v", err)
	}
	if reason != status.TransparentCaptureError {
		t.Fatalf("bootstrap capture error = %q, want %q", reason, status.TransparentCaptureError)
	}
	app := host.AppSession()
	response := controlRequest(t, app.BaseURL, http.MethodGet, "/api/v1/status", app.ReadToken, "vibermate://desktop")
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("ordinary runtime status = %d", response.StatusCode)
	}
	var runtimeStatus map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&runtimeStatus); err != nil {
		t.Fatal(err)
	}
	if string(runtimeStatus["ready"]) != "true" {
		t.Fatal("ordinary runtime is not ready after capture startup failed")
	}
	if _, present := runtimeStatus["transparentCaptureError"]; present {
		t.Fatal("capture bootstrap reason leaked into the generic runtime status contract")
	}
}

func bootstrapJSONFields(t *testing.T, descriptor desktopbootstrap.Descriptor) map[string]json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}
