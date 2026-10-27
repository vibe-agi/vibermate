package productruntime

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/connectudp"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

// A captured run's UDP reaches the Runtime as connect-udp on its proxy
// connection and leaves through the same UDP path as SOCKS5 UDP: connection
// policy decides it, and datagrams keep their boundaries both ways.
func TestConnectUDPUsesTheRuntimeUDPPath(t *testing.T) {
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), gate))
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	peer := runtimeUDPEcho(t)
	if _, err := r.ConnectionRules().Replace(t.Context(), r.ConnectionRules().Current().Revision,
		[]connectionpolicy.Rule{{ID: "owned.connect-udp", Priority: 1, Decision: connectionpolicy.DecisionAllow,
			Match: connectionpolicy.MatchExactHostPort("127.0.0.1", peer.Port())}},
		connectionpolicy.ModeDenyUnknown); err != nil {
		t.Fatal(err)
	}
	grant, err := r.ManualCaptures().Create(t.Context(), manualcapture.CreateCommand{
		Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "connect-udp capture",
		ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRecordingMirror(t, r)
	server := httptest.NewServer(r.ProxyHandler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	conn, err := connectudp.Dial(ctx, server.URL, grant.Credential.Value(), "127.0.0.1", peer.Port())
	if err != nil {
		t.Fatalf("connect-udp to the allowed echo: %v", err)
	}
	defer conn.Close()
	for _, payload := range [][]byte{[]byte("first datagram"), {0, 255, 128, 9}, make([]byte, 1400)} {
		if err := conn.WriteDatagram(payload); err != nil {
			t.Fatal(err)
		}
		got, err := conn.ReadDatagram()
		if err != nil || string(got) != string(payload) {
			t.Fatalf("echo of %d bytes = %d bytes, %v", len(payload), len(got), err)
		}
	}

	if _, err := connectudp.Dial(ctx, server.URL, grant.Credential.Value(), "127.0.0.1", peer.Port()+1); err == nil ||
		!strings.Contains(err.Error(), "403") {
		t.Fatalf("a target the policy does not allow = %v, want a 403 refusal", err)
	}
	if _, err := connectudp.Dial(ctx, server.URL, "not-a-credential", "127.0.0.1", peer.Port()); err == nil ||
		!strings.Contains(err.Error(), "407") {
		t.Fatalf("a wrong credential = %v, want a 407 refusal", err)
	}

	// Closing the session finishes the connection, which stays listable.
	conn.Close()
	runtimeUDPWait(t, func() bool { return gate.Snapshot().ActiveEgress == 0 && gate.Snapshot().ActiveActions == 0 })
	if err := r.recorder.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := r.connections.List(ctx, connectionevent.PageRequest{Limit: 50})
	if err != nil {
		t.Fatalf("list connections after the session closed: %v", err)
	}
	finished := false
	for _, item := range page.Items {
		finished = finished || (item.Port == peer.Port() && (item.Phase == connectionevent.PhaseClosed || item.Phase == connectionevent.PhaseFailed))
	}
	if !finished {
		t.Fatal("the closed connect-udp session has no finished connection record")
	}
}

// A capture's first connection may start before the recording mirror has
// published the capture's fence. Its events must still be recorded once the
// mirror catches up, not discarded as if the capture had been deleted.
func TestConnectionAtCaptureStartIsRecorded(t *testing.T) {
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), gate))
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	waitRecordingMirror(t, r)
	peer := runtimeUDPEcho(t)
	if _, err := r.ConnectionRules().Replace(t.Context(), r.ConnectionRules().Current().Revision,
		[]connectionpolicy.Rule{{ID: "owned.connect-udp", Priority: 1, Decision: connectionpolicy.DecisionAllow,
			Match: connectionpolicy.MatchExactHostPort("127.0.0.1", peer.Port())}},
		connectionpolicy.ModeDenyUnknown); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(r.ProxyHandler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	// No wait for the mirror between creating the capture and using it.
	grant, err := r.ManualCaptures().Create(t.Context(), manualcapture.CreateCommand{
		Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "fast capture",
		ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked,
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := connectudp.Dial(ctx, server.URL, grant.Credential.Value(), "127.0.0.1", peer.Port())
	if err != nil {
		t.Fatalf("connect-udp right after capture creation: %v", err)
	}
	if err := conn.WriteDatagram([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if got, err := conn.ReadDatagram(); err != nil || string(got) != "first" {
		t.Fatalf("echo = %q, %v", got, err)
	}
	conn.Close()
	runtimeUDPWait(t, func() bool { return gate.Snapshot().ActiveEgress == 0 && gate.Snapshot().ActiveActions == 0 })
	if err := r.recorder.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	waitRecordingMirror(t, r)
	page, err := r.connectionInspection.List(ctx, connectionevent.PageRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	phases := map[connectionevent.Phase]bool{}
	for _, item := range page.Items {
		if item.Port == peer.Port() {
			phases[item.Phase] = true
		}
	}
	for _, phase := range []connectionevent.Phase{connectionevent.PhaseAttempted, connectionevent.PhaseDecided, connectionevent.PhaseConnected, connectionevent.PhaseClosed} {
		if !phases[phase] {
			t.Errorf("no %s event for the capture's first connection; recorded %v", phase, phases)
		}
	}
}
