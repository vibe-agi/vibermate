package loopbackproxy_test

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
)

// The decision happens before any dial, so a denied connection never reaches a
// transport and never issues a certificate.
func TestDeniedConnectionNeverReachesATransport(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixtureWithPolicy(t, denyEverythingPolicy(t))
	defer fixture.Close(t)
	authority, stop := echoTarget(t)
	defer stop()

	connection, response := fixture.Connect(
		t,
		fixture.grant.ProxyCapability.Value(),
		authority,
	)
	_ = response.Body.Close()
	_ = connection.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("denied CONNECT status = %d", response.StatusCode)
	}

	page, err := fixture.connections.List(
		context.Background(),
		connectionevent.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	denied := false
	for _, record := range page.Items {
		if record.Decision == connectionevent.DecisionDeny {
			denied = true
			// The record explains itself with the rule that decided, not a
			// literal.
			if record.RuleID != "mode.deny_unknown" {
				t.Fatalf("denied record rule = %q", record.RuleID)
			}
		}
		if record.Decryption == connectionevent.DecryptionMITM {
			t.Fatalf("a denied connection was decrypted: %+v", record)
		}
	}
	if !denied {
		t.Fatal("a denied connection left no deny decision")
	}
}

// A ClientEndpoint is not exempt: policy decides every proxied connection.
func TestPolicyDecidesAClientEndpointToo(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixtureWithPolicy(t, denyEverythingPolicy(t))
	defer fixture.Close(t)

	connection, response := fixture.Connect(
		t,
		fixture.grant.ProxyCapability.Value(),
		"api.anthropic.com:443",
	)
	_ = response.Body.Close()
	_ = connection.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("denied ClientEndpoint status = %d", response.StatusCode)
	}
	if len(fixture.exchanges.Requests()) != 0 {
		t.Fatal("a denied ClientEndpoint created an Exchange")
	}
}

// An allowed connection still names the rule that allowed it.
func TestAnAllowedConnectionNamesItsRule(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixtureWithPolicy(t, allowAnthropicPolicy(t))
	defer fixture.Close(t)

	secured := fixture.ConnectTLS(
		t,
		fixture.grant.ProxyCapability.Value(),
		"api.anthropic.com:443",
		"api.anthropic.com",
	)
	defer secured.Close()

	page, err := fixture.connections.List(
		context.Background(),
		connectionevent.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range page.Items {
		if record.Decision != connectionevent.DecisionAllow {
			continue
		}
		if record.RuleID != "allow-anthropic" {
			t.Fatalf("allowed record rule = %q", record.RuleID)
		}
		return
	}
	t.Fatal("an allowed connection left no allow decision")
}

func denyEverythingPolicy(t *testing.T) connectionpolicy.Snapshot {
	t.Helper()

	set := connectionpolicy.Snapshot{
		Revision: 1,
		Mode:     connectionpolicy.ModeDenyUnknown,
	}
	return set
}

func allowAnthropicPolicy(t *testing.T) connectionpolicy.Snapshot {
	t.Helper()

	set := connectionpolicy.Snapshot{
		Revision: 1,
		Mode:     connectionpolicy.ModeDenyUnknown,
		Rules: []connectionpolicy.Rule{{
			ID:       "allow-anthropic",
			Decision: connectionpolicy.DecisionAllow,
			Match:    connectionpolicy.MatchExactHost("api.anthropic.com"),
		}},
	}
	return set
}

// A rule change reaches the next connection without a restart, and leaves a
// connection that was already decided alone. Revisiting a live tunnel would
// mean a person's edit could sever a transfer already in flight, which is not
// what editing a firewall rule means.
func TestARuleChangeReachesTheNextConnectionOnly(t *testing.T) {
	t.Parallel()

	allowAll := connectionpolicy.Snapshot{
		Revision: 1,
		Mode:     connectionpolicy.ModeMonitor,
	}
	fixture := newProxyFixtureWithPolicy(t, allowAll)
	defer fixture.Close(t)
	authority, stop := echoTarget(t)
	defer stop()

	established, response := fixture.Connect(
		t,
		fixture.grant.ProxyCapability.Value(),
		authority,
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("first connect status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	defer func() {
		_ = established.Close()
	}()

	if _, err := fixture.rules.Replace(
		context.Background(),
		1,
		nil,
		connectionpolicy.ModeDenyUnknown,
	); err != nil {
		t.Fatal(err)
	}

	// The connection decided under the old rules still carries bytes.
	if _, err := established.Write([]byte("ping")); err != nil {
		t.Fatalf("write to an established tunnel: %v", err)
	}
	echoed := make([]byte, len("echo:ping"))
	if _, err := io.ReadFull(established, echoed); err != nil {
		t.Fatalf("read from an established tunnel: %v", err)
	}
	if string(echoed) != "echo:ping" {
		t.Fatalf("established tunnel echoed %q", echoed)
	}

	// The next one is decided under the new rules.
	next, refused := fixture.Connect(
		t,
		fixture.grant.ProxyCapability.Value(),
		authority,
	)
	if refused.StatusCode != http.StatusForbidden {
		t.Fatalf("second connect status = %d", refused.StatusCode)
	}
	_ = refused.Body.Close()
	_ = next.Close()
}
