package connectionpolicy_test

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
)

func ruleSet(t *testing.T, rules ...connectionpolicy.Rule) connectionpolicy.RuleSet {
	t.Helper()

	set, err := connectionpolicy.NewRuleSet(connectionpolicy.RuleSetOptions{
		Revision: 1,
		Rules:    rules,
		Default: connectionpolicy.Rule{
			ID:       "default",
			Decision: connectionpolicy.DecisionDeny,
			Match:    connectionpolicy.MatchAny(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func request(host string, port uint16) connectionpolicy.Request {
	return connectionpolicy.Request{Host: host, Port: port}
}

// The first matching rule wins, and the decision names the rule that produced
// it so a record carries an explanation rather than a literal.
func TestFirstMatchingRuleWinsAndNamesItself(t *testing.T) {
	t.Parallel()

	set := ruleSet(t,
		connectionpolicy.Rule{
			ID:       "allow-anthropic",
			Decision: connectionpolicy.DecisionAllow,
			Match:    connectionpolicy.MatchExactHost("api.anthropic.com"),
		},
		connectionpolicy.Rule{
			ID:       "deny-anthropic-again",
			Decision: connectionpolicy.DecisionDeny,
			Match:    connectionpolicy.MatchExactHost("api.anthropic.com"),
		},
	)
	decision := set.Evaluate(request("api.anthropic.com", 443))
	if decision.Decision != connectionpolicy.DecisionAllow ||
		decision.RuleID != "allow-anthropic" {
		t.Fatalf("first match = %+v", decision)
	}
}

// Evaluation is deterministic: the same set and the same connection always
// produce the same answer.
func TestEvaluationIsDeterministic(t *testing.T) {
	t.Parallel()

	set := ruleSet(t,
		connectionpolicy.Rule{
			ID:       "allow-one",
			Decision: connectionpolicy.DecisionAllow,
			Match:    connectionpolicy.MatchExactHost("one.example"),
		},
		connectionpolicy.Rule{
			ID:       "allow-two",
			Decision: connectionpolicy.DecisionAllow,
			Match:    connectionpolicy.MatchExactHost("two.example"),
		},
	)
	first := set.Evaluate(request("two.example", 443))
	for range 32 {
		if set.Evaluate(request("two.example", 443)) != first {
			t.Fatal("evaluation is not deterministic")
		}
	}
}

// An unmatched connection falls to the declared default rather than to an
// implicit answer.
func TestUnmatchedConnectionFallsToTheDeclaredDefault(t *testing.T) {
	t.Parallel()

	set := ruleSet(t)
	decision := set.Evaluate(request("unknown.example", 443))
	if decision.Decision != connectionpolicy.DecisionDeny ||
		decision.RuleID != "default" {
		t.Fatalf("default decision = %+v", decision)
	}
}

// Design 06 makes a wildcard allow default an invariant violation, because a
// default that allows everything makes the firewall the one control that never
// fires.
func TestAWildcardAllowDefaultIsRefused(t *testing.T) {
	t.Parallel()

	_, err := connectionpolicy.NewRuleSet(connectionpolicy.RuleSetOptions{
		Revision: 1,
		Default: connectionpolicy.Rule{
			ID:       "default",
			Decision: connectionpolicy.DecisionAllow,
			Match:    connectionpolicy.MatchAny(),
		},
	})
	if err == nil {
		t.Fatal("a wildcard allow default was accepted")
	}
}

// A port narrows a rule; it never widens one.
func TestAPortNarrowsARule(t *testing.T) {
	t.Parallel()

	set := ruleSet(t, connectionpolicy.Rule{
		ID:       "allow-https-only",
		Decision: connectionpolicy.DecisionAllow,
		Match:    connectionpolicy.MatchExactHostPort("api.example", 443),
	})
	if got := set.Evaluate(request("api.example", 443)); got.Decision !=
		connectionpolicy.DecisionAllow {
		t.Fatalf("matching port = %+v", got)
	}
	if got := set.Evaluate(request("api.example", 8443)); got.Decision !=
		connectionpolicy.DecisionDeny {
		t.Fatalf("different port = %+v", got)
	}
}

// A rule set must declare a default; leaving it out would make the answer for
// an unmatched connection implicit.
func TestARuleSetMustDeclareADefault(t *testing.T) {
	t.Parallel()

	if _, err := connectionpolicy.NewRuleSet(
		connectionpolicy.RuleSetOptions{Revision: 1},
	); err == nil {
		t.Fatal("a rule set without a default was accepted")
	}
}

// The ask path exists now, so a rule set may carry one. A decision that blocks
// on a person is only safe to express once something can actually block.
func TestAnAskRuleIsAccepted(t *testing.T) {
	t.Parallel()

	set, err := connectionpolicy.NewRuleSet(connectionpolicy.RuleSetOptions{
		Revision: 4,
		Rules: []connectionpolicy.Rule{{
			ID:       "ask.unknown-host",
			Decision: connectionpolicy.DecisionAsk,
			Match:    connectionpolicy.MatchAny(),
		}},
		Default: connectionpolicy.Rule{
			ID:       "default.deny",
			Decision: connectionpolicy.DecisionDeny,
			Match:    connectionpolicy.MatchAny(),
		},
	})
	if err != nil {
		t.Fatalf("ask rule rejected: %v", err)
	}
	outcome := set.Evaluate(connectionpolicy.Request{
		Host: "api.example.com",
		Port: 443,
	})
	if outcome.Decision != connectionpolicy.DecisionAsk {
		t.Fatalf("decision = %q", outcome.Decision)
	}
	if outcome.RuleID != "ask.unknown-host" {
		t.Fatalf("rule ID = %q", outcome.RuleID)
	}
}

// Design 06 makes `ask` the shipped answer for a host nobody has decided on,
// so it must be expressible as the default. Unlike allow, it does not admit
// anything on its own.
func TestAskIsAllowedAsTheDefault(t *testing.T) {
	t.Parallel()

	set, err := connectionpolicy.NewRuleSet(connectionpolicy.RuleSetOptions{
		Revision: 5,
		Default: connectionpolicy.Rule{
			ID:       "default.ask",
			Decision: connectionpolicy.DecisionAsk,
			Match:    connectionpolicy.MatchAny(),
		},
	})
	if err != nil {
		t.Fatalf("ask default rejected: %v", err)
	}
	if set.Evaluate(connectionpolicy.Request{
		Host: "unknown.example.com",
		Port: 443,
	}).Decision != connectionpolicy.DecisionAsk {
		t.Fatal("default did not ask")
	}
}

// A stored set has no order a person can see, so precedence is a declared
// property of the rule rather than a position in a slice.
func TestPrecedenceIsDeclaredNotPositional(t *testing.T) {
	t.Parallel()

	set, err := connectionpolicy.NewRuleSet(connectionpolicy.RuleSetOptions{
		Revision: 6,
		Rules: []connectionpolicy.Rule{
			{
				ID:       "broad.allow-any",
				Priority: 10,
				Decision: connectionpolicy.DecisionAllow,
				Match:    connectionpolicy.MatchAny(),
			},
			{
				ID:       "narrow.deny-one-host",
				Priority: 100,
				Decision: connectionpolicy.DecisionDeny,
				Match:    connectionpolicy.MatchExactHost("blocked.example.com"),
			},
		},
		Default: connectionpolicy.Rule{
			ID:       "default.deny",
			Decision: connectionpolicy.DecisionDeny,
			Match:    connectionpolicy.MatchAny(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	blocked := set.Evaluate(connectionpolicy.Request{
		Host: "blocked.example.com",
		Port: 443,
	})
	if blocked.Decision != connectionpolicy.DecisionDeny ||
		blocked.RuleID != "narrow.deny-one-host" {
		t.Fatalf("higher precedence lost: %+v", blocked)
	}
	other := set.Evaluate(connectionpolicy.Request{
		Host: "allowed.example.com",
		Port: 443,
	})
	if other.Decision != connectionpolicy.DecisionAllow {
		t.Fatalf("lower precedence did not apply: %+v", other)
	}
}

// Equal precedence must not depend on the order storage happened to return.
// The identifier is the tie-break, because it is the one thing a rule always
// has and a person can see.
func TestEqualPrecedenceResolvesByIdentifier(t *testing.T) {
	t.Parallel()

	rules := []connectionpolicy.Rule{
		{
			ID:       "b.allow",
			Priority: 50,
			Decision: connectionpolicy.DecisionAllow,
			Match:    connectionpolicy.MatchExactHost("tied.example.com"),
		},
		{
			ID:       "a.deny",
			Priority: 50,
			Decision: connectionpolicy.DecisionDeny,
			Match:    connectionpolicy.MatchExactHost("tied.example.com"),
		},
	}
	forward, err := connectionpolicy.NewRuleSet(connectionpolicy.RuleSetOptions{
		Revision: 7,
		Rules:    rules,
		Default: connectionpolicy.Rule{
			ID:       "default.deny",
			Decision: connectionpolicy.DecisionDeny,
			Match:    connectionpolicy.MatchAny(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reversed, err := connectionpolicy.NewRuleSet(connectionpolicy.RuleSetOptions{
		Revision: 7,
		Rules:    []connectionpolicy.Rule{rules[1], rules[0]},
		Default: connectionpolicy.Rule{
			ID:       "default.deny",
			Decision: connectionpolicy.DecisionDeny,
			Match:    connectionpolicy.MatchAny(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := connectionpolicy.Request{Host: "tied.example.com", Port: 443}
	if forward.Evaluate(request) != reversed.Evaluate(request) {
		t.Fatalf(
			"order of input decided the answer: %+v vs %+v",
			forward.Evaluate(request),
			reversed.Evaluate(request),
		)
	}
	if forward.Evaluate(request).RuleID != "a.deny" {
		t.Fatalf("tie-break = %q", forward.Evaluate(request).RuleID)
	}
}
