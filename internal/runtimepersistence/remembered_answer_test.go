package runtimepersistence

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func pendingNetworkAsk(t *testing.T, store *Store) toolapproval.Record {
	t.Helper()

	created := time.Unix(1_780_000_000, 0).UTC()
	record := toolapproval.Record{
		ID:            "approval-network-1",
		Revision:      1,
		Kind:          toolapproval.KindNetworkAsk,
		AggregateKey:  "aggregate-network-1",
		SubjectRefs:   []string{"api.example.com:443"},
		SubjectLabels: []string{"api.example.com"},
		Target: toolapproval.Target{
			Host: "api.example.com",
			Port: 443,
		},
		RequestCount: 1,
		WaiterCount:  1,
		State:        toolapproval.StatePending,
		CreatedAt:    created,
		ExpiresAt:    created.Add(time.Minute),
	}
	if err := store.ToolApprovalRepository().Create(
		context.Background(),
		record,
	); err != nil {
		t.Fatal(err)
	}
	return record
}

func seededRules(t *testing.T, store *Store) connectionpolicy.Snapshot {
	t.Helper()

	snapshot, err := store.ConnectionRuleRepository().Seed(
		context.Background(),
		connectionpolicy.ShippedSnapshot(1),
		time.Unix(1_780_000_000, 0).UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// An answer that is not remembered changes no rules at all.
func TestAnUnrememberedAnswerWritesNoRule(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)
	before := seededRules(t, store)
	record := pendingNetworkAsk(t, store)

	if _, err := store.ToolApprovalRepository().Decide(
		context.Background(),
		toolapproval.DecisionCommand{
			ApprovalID:       record.ID,
			ExpectedRevision: 1,
			IdempotencyKey:   "decide-once-idempotency-01",
			Decision:         toolapproval.DecisionAllowOnce,
			Scope:            toolapproval.ScopeRequest,
		},
		time.Unix(1_780_000_030, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	after, err := store.ConnectionRuleRepository().Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || len(after.Rules) != len(before.Rules) {
		t.Fatalf("an unremembered answer changed the rules: %+v", after)
	}
}

// The answer and the rule it creates are one commit. If the rule cannot be
// written, the question stays open rather than being answered into nothing.
func TestARuleThatCannotBeWrittenLeavesTheQuestionOpen(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)
	record := pendingNetworkAsk(t, store)
	// There is no rule set to remember into, which is the one way this write
	// can fail without also failing the decision on its own.
	if _, err := store.ConnectionRuleRepository().Load(
		context.Background(),
	); !errors.Is(err, connectionpolicy.ErrNoRuleSet) {
		t.Fatalf("precondition: %v", err)
	}

	if _, err := store.ToolApprovalRepository().Decide(
		context.Background(),
		toolapproval.DecisionCommand{
			ApprovalID:       record.ID,
			ExpectedRevision: 1,
			IdempotencyKey:   "decide-remember-idempotency-1",
			Decision:         toolapproval.DecisionAllowOnce,
			Scope:            toolapproval.ScopeHostPort,
		},
		time.Unix(1_780_000_030, 0).UTC(),
	); err == nil {
		t.Fatal("a decision whose rule could not be written reported success")
	}
	reread, err := store.ToolApprovalRepository().Get(
		context.Background(),
		record.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if reread.State != toolapproval.StatePending || reread.Revision != 1 {
		t.Fatalf("the question was resolved without its rule: %+v", reread)
	}
}

// A remembered answer writes exactly the connection that was asked about.
func TestARememberedAnswerIsExactlyAsWideAsTheQuestion(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)
	before := seededRules(t, store)
	record := pendingNetworkAsk(t, store)

	if _, err := store.ToolApprovalRepository().Decide(
		context.Background(),
		toolapproval.DecisionCommand{
			ApprovalID:       record.ID,
			ExpectedRevision: 1,
			IdempotencyKey:   "decide-remember-idempotency-2",
			Decision:         toolapproval.DecisionDeny,
			ReasonCode:       "user_denied",
			Scope:            toolapproval.ScopeHostPort,
		},
		time.Unix(1_780_000_030, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	after, err := store.ConnectionRuleRepository().Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision <= before.Revision {
		t.Fatalf("the rule set revision did not advance: %+v", after)
	}
	var remembered []connectionpolicy.Rule
	for _, rule := range after.Rules {
		if rule.Match.Kind == connectionpolicy.MatchKindExactHostPort {
			remembered = append(remembered, rule)
		}
	}
	if len(remembered) != 1 {
		t.Fatalf("remembered rules = %+v", after.Rules)
	}
	if remembered[0].Decision != connectionpolicy.DecisionDeny ||
		remembered[0].Match.Host != "api.example.com" ||
		remembered[0].Match.Port != 443 {
		t.Fatalf("remembered rule = %+v", remembered[0])
	}
	// Nothing wider was written: the set still answers another port by its
	// other rules rather than by this memory.
	compiled, err := after.Compile()
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := compiled.Evaluate(connectionpolicy.Request{
		Host: "api.example.com",
		Port: 8443,
	})
	if elsewhere.RuleID == remembered[0].ID {
		t.Fatal("a remembered answer decided a connection nobody asked about")
	}
}
