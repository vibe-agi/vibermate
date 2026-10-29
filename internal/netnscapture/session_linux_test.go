//go:build linux

package netnscapture

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/capturescope"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectudp"
	"github.com/vibe-agi/vibermate/internal/flowingress"
)

const (
	agentModeVariable   = "VIBERMATE_NETNS_TEST_AGENT"
	agentTargetVariable = "VIBERMATE_NETNS_TEST_TARGET"
)

// TestMain lets this test binary act as the namespace helper and as the
// Agent, so the real re-exec path runs without a separate executable.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == ChildCommand {
		if err := RunChild(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "helper:", err)
		}
		os.Exit(1)
	}
	if mode := os.Getenv(agentModeVariable); mode != "" {
		if err := runTestAgent(mode); err != nil {
			fmt.Fprintln(os.Stderr, "agent:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runTestAgent is the Agent: it resolves names through the namespace's DNS
// and talks over TCP and UDP, with no proxy configuration at all.
func runTestAgent(mode string) error {
	if os.Getenv("HTTP_PROXY") != "" || os.Getenv("HTTPS_PROXY") != "" {
		return fmt.Errorf("proxy variables leaked into the Agent")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil || !bytes.Contains(status, []byte("CapEff:\t0000000000000000")) {
		return fmt.Errorf("the Agent kept capabilities")
	}
	switch mode {
	case "tcp":
		conn, err := net.DialTimeout("tcp", "service.example:8080", 10*time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.WriteString(conn, "hello over tcp"); err != nil {
			return err
		}
		reply := make([]byte, len("hello over tcp"))
		if _, err := io.ReadFull(conn, reply); err != nil {
			return err
		}
		fmt.Printf("tcp:%s\n", reply)
	case "udp":
		conn, err := net.DialTimeout("udp", "echo.example:9999", 10*time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write([]byte("hello over udp")); err != nil {
			return err
		}
		reply := make([]byte, 64)
		n, err := conn.Read(reply)
		if err != nil {
			return err
		}
		fmt.Printf("udp:%s\n", reply[:n])
	case "tcp-target", "udp-target":
		network := strings.TrimSuffix(mode, "-target")
		conn, err := net.DialTimeout(network, os.Getenv(agentTargetVariable), 10*time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.WriteString(conn, "hello direct"); err != nil {
			return err
		}
		reply := make([]byte, 64)
		n, err := conn.Read(reply)
		if err != nil {
			return err
		}
		fmt.Printf("%s:%s\n", network, reply[:n])
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
	return nil
}

type runtimeRecord struct {
	kind, target, credential string
}

// fakeRuntime answers CONNECT by echoing bytes and connect-udp by echoing
// datagrams, recording what each request named.
func fakeRuntime(t *testing.T) (string, func() []runtimeRecord) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	var mu sync.Mutex
	var records []runtimeRecord
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
				record := runtimeRecord{credential: request.Header.Get("Proxy-Authorization")}
				if request.Method == http.MethodConnect {
					record.kind, record.target = "tcp", request.Host
					mu.Lock()
					records = append(records, record)
					mu.Unlock()
					_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
					_, _ = io.Copy(conn, reader)
					return
				}
				host, port, err := connectudp.ParsePath(request.URL.EscapedPath())
				if err != nil {
					return
				}
				record.kind, record.target = "udp", net.JoinHostPort(host, fmt.Sprint(port))
				mu.Lock()
				records = append(records, record)
				mu.Unlock()
				_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: connect-udp\r\nCapsule-Protocol: ?1\r\n\r\n")
				for {
					payload, err := connectudp.ReadDatagram(reader)
					if err != nil {
						return
					}
					if connectudp.WriteDatagram(conn, payload) != nil {
						return
					}
				}
			}()
		}
	}()
	return "http://" + listener.Addr().String(), func() []runtimeRecord {
		mu.Lock()
		defer mu.Unlock()
		return append([]runtimeRecord(nil), records...)
	}
}

func runCapturedAgent(t *testing.T, origin, mode string) string {
	t.Helper()
	return runCapturedAgentWith(t, origin, mode, agentOptions{})
}

// agentOptions shape one captured run: its capture scope, the host resolver
// its namespace uses for bypassed names, and the Agent's target.
type agentOptions struct {
	scope   capturescope.Scope
	resolve func(context.Context, string) ([]netip.Addr, error)
	target  string
}

func runCapturedAgentWith(t *testing.T, origin, mode string, options agentOptions) string {
	t.Helper()
	ingress, err := flowingress.New(func() (flowingress.Proxy, error) {
		return flowingress.Proxy{Origin: origin, Token: "run-capability"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ingress.UseScope(options.scope)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The Agent runs a shell that starts the real client as its child, so the
	// child's traffic is captured because it inherited the namespace.
	command := exec.Command("/bin/sh", "-c", `"$0" -test.run=^$`, self)
	command.Env = append(os.Environ(), agentModeVariable+"="+mode, agentTargetVariable+"="+options.target)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	session, err := Prepare(command, ingress)
	if err != nil {
		t.Fatal(err)
	}
	if options.resolve != nil {
		session.resolve = options.resolve
	}
	defer session.Close()
	if err := command.Start(); err != nil {
		t.Skipf("unprivileged user namespaces are unavailable: %v", err)
	}
	if err := session.Start(); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		if errors.Is(err, ErrRestricted) {
			t.Skipf("this host restricts unprivileged user namespaces: %v", err)
		}
		t.Fatalf("namespace hand-off: %v (stderr %q)", err, stderr.String())
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("captured Agent failed: %v (stderr %q)", err, stderr.String())
		}
	case <-time.After(30 * time.Second):
		_ = command.Process.Kill()
		t.Fatalf("captured Agent did not finish (stderr %q)", stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

func wantRecord(t *testing.T, records []runtimeRecord, kind, target string) {
	t.Helper()
	credential := "Basic " + base64.StdEncoding.EncodeToString([]byte("capture:run-capability"))
	for _, record := range records {
		if record.kind == kind && record.target == target && record.credential == credential {
			return
		}
	}
	t.Fatalf("no %s record for %s with the run credential in %+v", kind, target, records)
}

// A descendant's TCP connection, made by name with no proxy settings, reaches
// the Runtime as CONNECT for that name with the run's credential.
func TestNamespaceCapturesTCPByName(t *testing.T) {
	origin, records := fakeRuntime(t)
	if got := runCapturedAgent(t, origin, "tcp"); got != "tcp:hello over tcp" {
		t.Fatalf("Agent output = %q", got)
	}
	wantRecord(t, records(), "tcp", "service.example:8080")
}

// A descendant's UDP goes through vibermated as connect-udp, never around it.
func TestNamespaceCarriesUDPThroughTheRuntime(t *testing.T) {
	origin, records := fakeRuntime(t)
	if got := runCapturedAgent(t, origin, "udp"); got != "udp:hello over udp" {
		t.Fatalf("Agent output = %q", got)
	}
	wantRecord(t, records(), "udp", "echo.example:9999")
}

// hostAddress is an IPv4 address of this machine in reserved space, reachable
// from the launcher but routed through the namespace's TUN device.
func hostAddress(t *testing.T) netip.Addr {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err != nil {
			continue
		}
		addr := prefix.Addr()
		if addr.Is4() && addr.IsPrivate() {
			return addr
		}
	}
	t.Skip("this machine has no private IPv4 address to test bypass with")
	return netip.Addr{}
}

func tcpEcho(t *testing.T, address netip.Addr) netip.AddrPort {
	t.Helper()
	listener, err := net.Listen("tcp4", netip.AddrPortFrom(address, 0).String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buffer := make([]byte, 64)
				n, _ := conn.Read(buffer)
				_, _ = conn.Write(buffer[:n])
			}()
		}
	}()
	return netip.MustParseAddrPort(listener.Addr().String())
}

func udpEcho(t *testing.T, address netip.Addr) netip.AddrPort {
	t.Helper()
	conn, err := net.ListenPacket("udp4", netip.AddrPortFrom(address, 0).String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, peer, err := conn.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = conn.WriteTo(buffer[:n], peer)
		}
	}()
	return netip.MustParseAddrPort(conn.LocalAddr().String())
}

func scopeOf(rules ...capturescope.Rule) capturescope.Scope {
	return capturescope.Compile(&capturescope.Policy{Rules: rules}, nil, capturescope.Options{})
}

// ADR 0025: a destination the capture scope covers leaves directly from the
// launcher; the Runtime never sees it. Without the rule it goes through.
func TestNamespaceBypassesScopedAddressesDirectly(t *testing.T) {
	host := hostAddress(t)
	tcpTarget, udpTarget := tcpEcho(t, host), udpEcho(t, host)
	lan := scopeOf(capturescope.Rule{ID: "lan", Kind: capturescope.KindPrefix, Value: netip.PrefixFrom(host, 32).String()})

	origin, records := fakeRuntime(t)
	if got := runCapturedAgentWith(t, origin, "tcp-target", agentOptions{scope: lan, target: tcpTarget.String()}); got != "tcp:hello direct" {
		t.Fatalf("bypassed TCP: %q", got)
	}
	if got := runCapturedAgentWith(t, origin, "udp-target", agentOptions{scope: lan, target: udpTarget.String()}); got != "udp:hello direct" {
		t.Fatalf("bypassed UDP: %q", got)
	}
	if seen := records(); len(seen) != 0 {
		t.Fatalf("the Runtime saw bypassed flows: %+v", seen)
	}

	if got := runCapturedAgentWith(t, origin, "tcp-target", agentOptions{target: tcpTarget.String()}); got != "tcp:hello direct" {
		t.Fatalf("captured TCP: %q", got)
	}
	wantRecord(t, records(), "tcp", tcpTarget.String())
}

// A name the scope covers resolves on the host to its real address, and the
// flow to that address leaves directly.
func TestNamespaceResolvesBypassedNamesForReal(t *testing.T) {
	host := hostAddress(t)
	target := tcpEcho(t, host)
	resolved := make(chan string, 4)
	resolve := func(_ context.Context, name string) ([]netip.Addr, error) {
		resolved <- name
		return []netip.Addr{host}, nil
	}
	origin, records := fakeRuntime(t)
	scope := scopeOf(capturescope.Rule{ID: "intranet", Kind: capturescope.KindSuffix, Value: "bypass.example"})
	got := runCapturedAgentWith(t, origin, "tcp-target", agentOptions{
		scope: scope, resolve: resolve, target: fmt.Sprintf("svc.bypass.example:%d", target.Port()),
	})
	if got != "tcp:hello direct" {
		t.Fatalf("bypassed name: %q", got)
	}
	select {
	case name := <-resolved:
		if name != "svc.bypass.example" {
			t.Fatalf("host resolver asked for %q", name)
		}
	default:
		t.Fatal("the bypassed name was not resolved on the host")
	}
	if seen := records(); len(seen) != 0 {
		t.Fatalf("the Runtime saw a bypassed flow: %+v", seen)
	}
}
