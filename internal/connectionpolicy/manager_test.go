package connectionpolicy_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
)

type memoryRules struct {
	stored  connectionpolicy.Snapshot
	seeded  bool
	writes  int
	failing error
}

func (memory *memoryRules) Load(
	context.Context,
) (connectionpolicy.Snapshot, error) {
	if !memory.seeded {
		return connectionpolicy.Snapshot{}, connectionpolicy.ErrNoRuleSet
	}
	return memory.stored, nil
}

func (memory *memoryRules) Seed(
	_ context.Context,
	snapshot connectionpolicy.Snapshot,
	_ time.Time,
) (connectionpolicy.Snapshot, error) {
	if memory.seeded {
		return memory.stored, nil
	}
	memory.seeded = true
	memory.stored = snapshot
	return snapshot, nil
}

func (memory *memoryRules) Replace(
	_ context.Context,
	expected uint64,
	snapshot connectionpolicy.Snapshot,
	_ time.Time,
) (connectionpolicy.Snapshot, error) {
	if memory.failing != nil {
		return memory.stored, memory.failing
	}
	if memory.stored.Revision != expected {
		return memory.stored, connectionpolicy.ErrRevisionConflict
	}
	memory.writes++
	memory.stored = snapshot
	return snapshot, nil
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Unix(1_780_000_000, 0).UTC() }

func newManager(t *testing.T, store *memoryRules) *connectionpolicy.Manager {
	t.Helper()

	manager, err := connectionpolicy.NewManager(
		context.Background(),
		connectionpolicy.ManagerOptions{
			Repository: store,
			Clock:      fixedClock{},
			Shipped:    connectionpolicy.ShippedSnapshot(1),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

// A change is visible to the next connection without a restart.
func TestAnAcceptedChangeTakesEffectImmediately(t *testing.T) {
	t.Parallel()

	store := &memoryRules{}
	manager := newManager(t, store)
	request := connectionpolicy.Request{Host: "api.example.com", Port: 443}
	if manager.Source().Current().Evaluate(request).Decision !=
		connectionpolicy.DecisionAsk {
		t.Fatal("the shipped set did not ask about an undecided host")
	}
	if _, err := manager.Replace(
		context.Background(),
		1,
		[]connectionpolicy.Rule{{
			ID:       "deny.one-host",
			Priority: 100,
			Decision: connectionpolicy.DecisionDeny,
			Match:    connectionpolicy.MatchExactHost("api.example.com"),
		}},
		connectionpolicy.Rule{
			ID:       connectionpolicy.DefaultDenyRuleID,
			Decision: connectionpolicy.DecisionDeny,
			Match:    connectionpolicy.MatchAny(),
		},
	); err != nil {
		t.Fatal(err)
	}
	outcome := manager.Source().Current().Evaluate(request)
	if outcome.Decision != connectionpolicy.DecisionDeny ||
		outcome.RuleID != "deny.one-host" {
		t.Fatalf("the new rules were not in force: %+v", outcome)
	}
	if outcome.Revision != 2 {
		t.Fatalf("revision = %d", outcome.Revision)
	}
}

// A refused change leaves both the stored rules and the rules in force exactly
// as they were.
func TestARefusedChangeLeavesTheOldRulesInForce(t *testing.T) {
	t.Parallel()

	refusals := []struct {
		name     string
		expected uint64
		rules    []connectionpolicy.Rule
		fallback connectionpolicy.Rule
		wants    error
	}{
		{
			name:     "prepared against a revision that moved",
			expected: 7,
			fallback: connectionpolicy.Rule{
				ID:       connectionpolicy.DefaultDenyRuleID,
				Decision: connectionpolicy.DecisionDeny,
				Match:    connectionpolicy.MatchAny(),
			},
			wants: connectionpolicy.ErrRevisionConflict,
		},
		{
			name:     "a default that allows everything",
			expected: 1,
			fallback: connectionpolicy.Rule{
				ID:       "default.allow-everything",
				Decision: connectionpolicy.DecisionAllow,
				Match:    connectionpolicy.MatchAny(),
			},
			wants: connectionpolicy.ErrInvalidRuleSet,
		},
		{
			name:     "a rule that carries a pattern",
			expected: 1,
			rules: []connectionpolicy.Rule{{
				ID:       "allow.wildcard",
				Decision: connectionpolicy.DecisionAllow,
				Match:    connectionpolicy.MatchExactHost("*.example.com"),
			}},
			fallback: connectionpolicy.Rule{
				ID:       connectionpolicy.DefaultDenyRuleID,
				Decision: connectionpolicy.DecisionDeny,
				Match:    connectionpolicy.MatchAny(),
			},
			wants: connectionpolicy.ErrInvalidRuleSet,
		},
	}
	for _, refusal := range refusals {
		t.Run(refusal.name, func(t *testing.T) {
			t.Parallel()

			store := &memoryRules{}
			manager := newManager(t, store)
			before := manager.Source().Current()
			if _, err := manager.Replace(
				context.Background(),
				refusal.expected,
				refusal.rules,
				refusal.fallback,
			); !errors.Is(err, refusal.wants) {
				t.Fatalf("error = %v", err)
			}
			if store.writes != 0 {
				t.Fatal("a refused change was written")
			}
			request := connectionpolicy.Request{Host: "api.example.com", Port: 443}
			if manager.Source().Current().Evaluate(request) !=
				before.Evaluate(request) {
				t.Fatal("a refused change altered the rules in force")
			}
			if manager.Current().Revision != 1 {
				t.Fatalf("revision moved to %d", manager.Current().Revision)
			}
		})
	}
}

// A store that refuses the write leaves the rules in force alone. Rules that
// a restart would not bring back must never be the rules being evaluated.
func TestRulesThatWereNotStoredNeverTakeEffect(t *testing.T) {
	t.Parallel()

	store := &memoryRules{failing: errors.New("disk is gone")}
	manager := newManager(t, store)
	before := manager.Source().Current()
	if _, err := manager.Replace(
		context.Background(),
		1,
		nil,
		connectionpolicy.Rule{
			ID:       connectionpolicy.DefaultDenyRuleID,
			Decision: connectionpolicy.DecisionDeny,
			Match:    connectionpolicy.MatchAny(),
		},
	); err == nil {
		t.Fatal("a failed write reported success")
	}
	request := connectionpolicy.Request{Host: "api.example.com", Port: 443}
	if manager.Source().Current().Evaluate(request) != before.Evaluate(request) {
		t.Fatal("unstored rules took effect")
	}
}
