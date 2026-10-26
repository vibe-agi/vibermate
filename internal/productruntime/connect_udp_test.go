package productruntime

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
}
