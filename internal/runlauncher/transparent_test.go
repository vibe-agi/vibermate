//go:build linux || darwin

package runlauncher

import (
	"bytes"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/flowbroker"
	"github.com/vibe-agi/vibermate/internal/flowingress"
	"github.com/vibe-agi/vibermate/internal/processlineage"
)

const testFlowSecret = "0123456789abcdef0123456789abcdef"

type testBroker struct {
	broker       *flowbroker.Broker
	registration *net.UnixListener
	flows        *net.TCPListener
}

func startTestBroker(t *testing.T, socket string, announce bool) *testBroker {
	t.Helper()
	broker, err := flowbroker.New(processlineage.System(), func(flowbroker.Peer) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	registration, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	flows, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	go broker.ServeRegistrations(registration)
	go broker.ServeFlowsTCP(flows, []byte(testFlowSecret))
	started := &testBroker{broker: broker, registration: registration, flows: flows}
	t.Cleanup(started.stop)
	if announce {
		conn := started.dial(t)
		defer conn.Close()
		if err := flowbroker.WriteHello(conn, testFlowSecret); err != nil {
			t.Fatal(err)
		}
		if answer := readAnswer(t, conn); answer != 'H' {
			t.Fatalf("hello answer = %q", answer)
		}
	}
	return started
}

func (b *testBroker) stop() {
	b.registration.Close()
	b.flows.Close()
	b.broker.Close()
}

func (b *testBroker) dial(t *testing.T) *net.TCPConn {
	t.Helper()
	conn, err := net.DialTCP("tcp4", nil, b.flows.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func readAnswer(t *testing.T, conn net.Conn) byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	answer := make([]byte, 1)
	if _, err := io.ReadFull(conn, answer); err != nil {
		return 0
	}
	_ = conn.SetReadDeadline(time.Time{})
	return answer[0]
}

func startProcessGroup(t *testing.T) *exec.Cmd {
	t.Helper()
	child := exec.Command("sleep", "60")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-child.Process.Pid, syscall.SIGCONT)
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	return child
}

func TestTransparentEnvironmentKeepsTrustAndDropsProxies(t *testing.T) {
	got := withoutProxyEnvironment([]string{
		"PATH=/bin", "HTTPS_PROXY=http://capture:x@127.0.0.1:1", "https_proxy=x", "HTTP_PROXY=x",
		"http_proxy=x", "NO_PROXY=x", "no_proxy=x", "NODE_USE_ENV_PROXY=1",
		"NODE_EXTRA_CA_CERTS=/root.pem", "SSL_CERT_FILE=/root.pem", "VIBERMATE_CAPTURE_RUN_ID=run",
	})
	want := []string{"PATH=/bin", "NODE_EXTRA_CA_CERTS=/root.pem", "SSL_CERT_FILE=/root.pem", "VIBERMATE_CAPTURE_RUN_ID=run"}
	if !slices.Equal(got, want) {
		t.Fatalf("environment = %q, want %q", got, want)
	}
}

func TestTransparentCaptureRefusesWithoutAnActiveExtension(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "flows.sock")
	if _, err := startTransparentCapture(socket, "run", flowingress.Proxy{}); err == nil {
		t.Fatal("capture started without the app")
	}
	startTestBroker(t, socket, false)
	if _, err := startTransparentCapture(socket, "run", flowingress.Proxy{}); err != ErrTransparentExtensionInactive {
		t.Fatalf("start error = %v, want inactive extension", err)
	}
}

// A descendant's flow, submitted the way the extension submits it, reaches
// the Runtime as CONNECT with the run's credential and the sniffed host.
func TestTransparentCaptureForwardsADescendantsFlow(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "flows.sock")
	broker := startTestBroker(t, socket, true)
	backendOrigin, observed := echoConnectBackend(t)
	capture, err := startTransparentCapture(socket, "run", flowingress.Proxy{Origin: backendOrigin, Token: "run-capability"})
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	child := startProcessGroup(t)

	conn := broker.dial(t)
	defer conn.Close()
	if err := flowbroker.WriteFlowRequest(conn, child.Process.Pid, testFlowSecret); err != nil {
		t.Fatal(err)
	}
	if answer := readAnswer(t, conn); answer != 'Y' {
		t.Fatalf("flow answer = %q, want owned", answer)
	}
	request := "GET /v1/models HTTP/1.1\r\nHost: api.openai.com\r\n\r\n"
	if err := flowingress.WriteHeader(conn, flowingress.Header{Version: 1, Protocol: "tcp", Destination: netip.MustParseAddrPort("203.0.113.5:80")}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	echoed := make([]byte, len(request))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, echoed); err != nil || string(echoed) != request {
		t.Fatalf("echo = %q, %v", echoed, err)
	}
	select {
	case got := <-observed:
		if got.target != "api.openai.com:80" || got.proxyAuth != frontierBasic("capture", "run-capability") {
			t.Fatalf("Runtime saw %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Runtime saw no CONNECT")
	}
}

func processState(t *testing.T, pid int) byte {
	t.Helper()
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Skipf("process state unavailable: %v", err)
	}
	fields := strings.Fields(string(data[bytes.LastIndexByte(data, ')')+1:]))
	return fields[0][0]
}

func waitState(t *testing.T, pid int, want byte) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for processState(t, pid) != want {
		if time.Now().After(deadline) {
			t.Fatalf("process %d state = %c, want %c", pid, processState(t, pid), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTransparentCapturePausesTheAgentUntilCaptureReturns(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "flows.sock")
	first := startTestBroker(t, socket, true)
	capture, err := startTransparentCapture(socket, "run", flowingress.Proxy{})
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	child := startProcessGroup(t)
	var notices lockedBuffer
	failed := capture.supervise(t.Context(), child.Process.Pid, &notices)

	first.stop()
	waitState(t, child.Process.Pid, 'T')
	startTestBroker(t, socket, true)
	waitState(t, child.Process.Pid, 'S')
	select {
	case err := <-failed:
		t.Fatalf("supervision failed after recovery: %v", err)
	default:
	}
	if text := notices.String(); !strings.Contains(text, "paused") || !strings.Contains(text, "resumed") {
		t.Fatalf("notices = %q", text)
	}
}

func TestTransparentCaptureFailsWhenCaptureDoesNotReturn(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "flows.sock")
	broker := startTestBroker(t, socket, true)
	capture, err := startTransparentCapture(socket, "run", flowingress.Proxy{})
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	capture.recoveryWindow = 200 * time.Millisecond
	child := startProcessGroup(t)
	failed := capture.supervise(t.Context(), child.Process.Pid, io.Discard)
	broker.stop()
	select {
	case err := <-failed:
		if err != ErrTransparentLost {
			t.Fatalf("supervision error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervision did not give up")
	}
	waitState(t, child.Process.Pid, 'T')
}

// lockedBuffer collects notices written from the supervision goroutine.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
