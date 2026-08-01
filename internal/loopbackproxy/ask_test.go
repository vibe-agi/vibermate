package loopbackproxy_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func askPolicy(t *testing.T) connectionpolicy.Snapshot {
	t.Helper()

	set := connectionpolicy.Snapshot{
		Revision: 1,
		Rules: []connectionpolicy.Rule{{
			ID:       "ask-unknown",
			Decision: connectionpolicy.DecisionAsk,
			Match:    connectionpolicy.MatchAny(),
		}},
		Default: connectionpolicy.Rule{
			ID:       "default.deny",
			Decision: connectionpolicy.DecisionDeny,
			Match:    connectionpolicy.MatchAny(),
		},
	}
	return set
}

// The ask blocks before the dial: nothing is connected while the question is
// open, and the answer decides whether anything ever is.
func TestAnAskBlocksUntilAPersonDecides(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixtureWithPolicy(t, askPolicy(t))
	defer fixture.Close(t)
	authority, stop := echoTarget(t)
	defer stop()

	var waiting sync.WaitGroup
	var status int
	waiting.Add(1)
	go func() {
		defer waiting.Done()
		connection, response := fixture.Connect(
			t,
			fixture.grant.ProxyCapability.Value(),
			authority,
		)
		status = response.StatusCode
		_ = response.Body.Close()
		_ = connection.Close()
	}()

	pending := waitForPendingAsk(t, fixture)
	if pending.Kind != string(toolapproval.KindNetworkAsk) {
		t.Fatalf("pending approval kind = %q", pending.Kind)
	}
	if len(pending.SubjectLabels) == 0 {
		t.Fatalf("pending approval has no subject: %+v", pending)
	}
	if _, err := fixture.approvals.DecideApproval(
		context.Background(),
		toolapproval.DecisionCommand{
			ApprovalID:       pending.ID,
			ExpectedRevision: pending.Revision,
			Decision:         toolapproval.DecisionAllowOnce,
			Scope:            toolapproval.ScopeRequest,
			IdempotencyKey:   "ask-allow-idempotency-0001",
		},
	); err != nil {
		t.Fatal(err)
	}
	waiting.Wait()
	if status != http.StatusOK {
		t.Fatalf("allowed ask status = %d", status)
	}
}

// Every way the wait can fail denies. A denial answers the connection rather
// than letting it through.
func TestADeniedAskRefusesTheConnection(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixtureWithPolicy(t, askPolicy(t))
	defer fixture.Close(t)
	authority, stop := echoTarget(t)
	defer stop()

	var waiting sync.WaitGroup
	var status int
	waiting.Add(1)
	go func() {
		defer waiting.Done()
		connection, response := fixture.Connect(
			t,
			fixture.grant.ProxyCapability.Value(),
			authority,
		)
		status = response.StatusCode
		_ = response.Body.Close()
		_ = connection.Close()
	}()

	pending := waitForPendingAsk(t, fixture)
	if _, err := fixture.approvals.DecideApproval(
		context.Background(),
		toolapproval.DecisionCommand{
			ApprovalID:       pending.ID,
			ExpectedRevision: pending.Revision,
			Decision:         toolapproval.DecisionDeny,
			Scope:            toolapproval.ScopeRequest,
			ReasonCode:       "user_denied",
			IdempotencyKey:   "ask-deny-idempotency-0001",
		},
	); err != nil {
		t.Fatal(err)
	}
	waiting.Wait()
	if status != http.StatusForbidden {
		t.Fatalf("denied ask status = %d", status)
	}
}

// A burst of connections to one host is one question answered once for all of
// them.
func TestIdenticalAsksAreOneQuestion(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixtureWithPolicy(t, askPolicy(t))
	defer fixture.Close(t)
	authority, stop := echoTarget(t)
	defer stop()

	const callers uint32 = 4
	var waiting sync.WaitGroup
	statuses := make([]int, callers)
	for index := range int(callers) {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			connection, response := fixture.Connect(
				t,
				fixture.grant.ProxyCapability.Value(),
				authority,
			)
			statuses[index] = response.StatusCode
			_ = response.Body.Close()
			_ = connection.Close()
		}()
	}

	// Every caller must have arrived before the question is answered.
	// Answering early would leave a straggler to open a second question, which
	// says nothing about whether identical questions merge.
	pending := waitForAskWaiters(t, fixture, callers)
	page, err := fixture.approvals.ListApprovals(
		context.Background(),
		toolapproval.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	open := 0
	for _, view := range page.Items {
		if view.State == toolapproval.StatePending {
			open++
		}
	}
	if open != 1 {
		t.Fatalf("%d callers produced %d questions", callers, open)
	}
	if pending.RequestCount != callers {
		t.Fatalf("one question counted %d of %d callers", pending.RequestCount, callers)
	}
	if _, err := fixture.approvals.DecideApproval(
		context.Background(),
		toolapproval.DecisionCommand{
			ApprovalID:       pending.ID,
			ExpectedRevision: pending.Revision,
			Decision:         toolapproval.DecisionAllowOnce,
			Scope:            toolapproval.ScopeRequest,
			IdempotencyKey:   "ask-allow-idempotency-0002",
		},
	); err != nil {
		t.Fatal(err)
	}
	waiting.Wait()
	for index, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("caller %d status = %d", index, status)
		}
	}
}

// waitForAskWaiters waits until one question has gathered every caller.
func waitForAskWaiters(
	t *testing.T,
	fixture *proxyFixture,
	want uint32,
) toolapproval.View {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		view := waitForPendingAsk(t, fixture)
		if view.WaiterCount >= want {
			return view
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("one question never gathered %d callers", want)
	return toolapproval.View{}
}

func waitForPendingAsk(
	t *testing.T,
	fixture *proxyFixture,
) toolapproval.View {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		page, err := fixture.approvals.ListApprovals(
			context.Background(),
			toolapproval.PageRequest{Limit: 20},
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, view := range page.Items {
			if view.State == toolapproval.StatePending &&
				view.Kind == string(toolapproval.KindNetworkAsk) {
				return view
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no network ask became pending")
	return toolapproval.View{}
}

// Remembering is what makes `ask` a default rather than an interrogation. The
// answer writes a rule, and the next connection to the same host and port is
// decided without asking anyone.
func TestARememberedAnswerDecidesTheNextConnection(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixtureWithPolicy(t, askPolicy(t))
	defer fixture.Close(t)
	authority, stop := echoTarget(t)
	defer stop()

	var waiting sync.WaitGroup
	var first int
	waiting.Add(1)
	go func() {
		defer waiting.Done()
		connection, response := fixture.Connect(
			t,
			fixture.grant.ProxyCapability.Value(),
			authority,
		)
		first = response.StatusCode
		_ = response.Body.Close()
		_ = connection.Close()
	}()

	pending := waitForPendingAsk(t, fixture)
	if _, err := fixture.approvals.DecideApproval(
		context.Background(),
		toolapproval.DecisionCommand{
			ApprovalID:       pending.ID,
			ExpectedRevision: pending.Revision,
			Decision:         toolapproval.DecisionAllowOnce,
			Scope:            toolapproval.ScopeHostPort,
			IdempotencyKey:   "ask-remember-idempotency-0001",
		},
	); err != nil {
		t.Fatal(err)
	}
	waiting.Wait()
	if first != http.StatusOK {
		t.Fatalf("remembered allow status = %d", first)
	}

	// The second connection is decided by the rule, so nothing new is pending.
	second, response := fixture.Connect(
		t,
		fixture.grant.ProxyCapability.Value(),
		authority,
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("second connect status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	_ = second.Close()

	page, err := fixture.approvals.ListApprovals(
		context.Background(),
		toolapproval.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range page.Items {
		if view.State == toolapproval.StatePending {
			t.Fatalf("a remembered answer asked again: %+v", view)
		}
	}

	// The rule is exactly as wide as the question. Another port on the same
	// host is a different connection and is still asked about.
	remembered := fixture.rules.Current()
	if len(remembered.Rules) != 2 {
		t.Fatalf("remembered rule set = %+v", remembered)
	}
	found := false
	for _, rule := range remembered.Rules {
		if rule.Match.Kind == connectionpolicy.MatchKindExactHostPort {
			found = true
			if rule.Decision != connectionpolicy.DecisionAllow {
				t.Fatalf("remembered decision = %q", rule.Decision)
			}
		}
	}
	if !found {
		t.Fatal("no rule was remembered")
	}
}
