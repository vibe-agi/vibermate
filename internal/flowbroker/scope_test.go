//go:build linux || darwin

package flowbroker

import (
	"io"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"
)

// submitTo plays a capture-scope-aware extension: it names the flow's
// destination in the ownership query and returns the broker's answer.
func submitTo(t *testing.T, flows string, pid int, destination string) byte {
	t.Helper()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: flows, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := WriteFlowRequestTo(conn, pid, "", netip.MustParseAddrPort(destination)); err != nil {
		return 0
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	answer := make([]byte, 1)
	if _, err := io.ReadFull(conn, answer); err != nil {
		return 0
	}
	_ = conn.CloseWrite()
	return answer[0]
}

// ADR 0025: an owned process's TCP flow to an address its run keeps off
// ViberMate is answered "direct"; everything else keeps its old answer.
func TestScopedDestinationsOfOwnedFlowsGoDirect(t *testing.T) {
	_, registrations, flows := startBroker(t, sameUser)
	registration, err := RegisterWithScope(registrations, "run-a", "", []BypassAddress{
		{Prefix: "192.168.0.0/16"},
		{Prefix: "10.20.0.0/16", Port: 5432},
	}, func(flow net.Conn) { flow.Close() })
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()
	child := startChild(t)
	for destination, want := range map[string]byte{
		"192.168.1.5:22":     flowDirect,
		"10.20.3.4:5432":     flowDirect,
		"10.20.3.4:443":      flowOwned,
		"203.0.113.9:443":    flowOwned,
		"100.100.100.100:53": flowDirect,
	} {
		if answer := submitTo(t, flows, child, destination); answer != want {
			t.Errorf("owned flow to %s answered %q, want %q", destination, answer, want)
		}
	}
	if answer := submit(t, flows, child, ""); answer != flowOwned {
		t.Errorf("a query without a destination answered %q, want owned", answer)
	}
	if answer := submitTo(t, flows, os.Getppid(), "192.168.1.5:22"); answer != flowUnowned {
		t.Errorf("an unrelated process's flow answered %q, want unowned", answer)
	}
}

func TestRegistrationRefusesAnOutOfBoundsScope(t *testing.T) {
	_, registrations, _ := startBroker(t, sameUser)
	for _, bypass := range [][]BypassAddress{
		{{Prefix: "8.8.8.0/24"}},
		{{Prefix: "0.0.0.0/0"}},
		{{Prefix: "not-a-prefix"}},
	} {
		if registration, err := RegisterWithScope(registrations, "run-a", "", bypass, func(flow net.Conn) { flow.Close() }); err == nil {
			registration.Close()
			t.Errorf("registration with %+v was accepted", bypass)
		}
	}
}
