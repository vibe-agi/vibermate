package loopbackproxy_test

import (
	"context"
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
			if record.RuleID != "deny-all" {
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

// An AgentEndpoint is not exempt: policy decides every proxied connection.
func TestPolicyDecidesAnAgentEndpointToo(t *testing.T) {
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
		t.Fatalf("denied AgentEndpoint status = %d", response.StatusCode)
	}
	if len(fixture.exchanges.Requests()) != 0 {
		t.Fatal("a denied AgentEndpoint created an Exchange")
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

func denyEverythingPolicy(t *testing.T) connectionpolicy.RuleSet {
	t.Helper()

	set, err := connectionpolicy.NewRuleSet(connectionpolicy.RuleSetOptions{
		Revision: 1,
		Rules: []connectionpolicy.Rule{{
			ID:       "deny-all",
			Decision: connectionpolicy.DecisionDeny,
			Match:    connectionpolicy.MatchAny(),
		}},
		Default: connectionpolicy.Rule{
			ID:       "default-deny",
			Decision: connectionpolicy.DecisionDeny,
			Match:    connectionpolicy.MatchAny(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func allowAnthropicPolicy(t *testing.T) connectionpolicy.RuleSet {
	t.Helper()

	set, err := connectionpolicy.NewRuleSet(connectionpolicy.RuleSetOptions{
		Revision: 1,
		Rules: []connectionpolicy.Rule{{
			ID:       "allow-anthropic",
			Decision: connectionpolicy.DecisionAllow,
			Match:    connectionpolicy.MatchExactHost("api.anthropic.com"),
		}},
		Default: connectionpolicy.Rule{
			ID:       "default-deny",
			Decision: connectionpolicy.DecisionDeny,
			Match:    connectionpolicy.MatchAny(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return set
}
