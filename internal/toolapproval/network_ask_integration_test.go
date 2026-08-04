package toolapproval_test

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func newAskAuthority(
	t *testing.T,
	timeout time.Duration,
) *toolapproval.Authority {
	t.Helper()

	store := openStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	t.Cleanup(func() { shutdownStore(t, store) })
	authority, err := toolapproval.New(
		context.Background(),
		toolapproval.Options{
			Repository: store.ToolApprovalRepository(),
			Clock:      toolapproval.SystemClock{},
			Random:     rand.Reader,
			Config:     toolapproval.Config{DecisionTimeout: timeout},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownAuthority(t, authority) })
	return authority
}

func askRequest() toolapproval.NetworkAskRequest {
	return toolapproval.NetworkAskRequest{
		IngressID: "run-1",
		Host:      "api.example.com",
		Port:      443,
	}
}

func TestAClientRootSignedPathIsLiveEvidenceNotDurableApprovalData(t *testing.T) {
	t.Parallel()

	store := openStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	t.Cleanup(func() { shutdownStore(t, store) })
	repository := store.ToolApprovalRepository()
	authority, err := toolapproval.New(
		context.Background(),
		toolapproval.Options{
			Repository: repository,
			Clock:      toolapproval.SystemClock{},
			Random:     rand.Reader,
			Config:     toolapproval.Config{DecisionTimeout: 10 * time.Second},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownAuthority(t, authority) })

	const signedPath = "/Applications/Claude.app/Contents/MacOS/claude"
	type askResult struct {
		outcome toolapproval.ClientRootAskOutcome
		err     error
	}
	result := make(chan askResult, 1)
	go func() {
		outcome, askErr := authority.AskClientRoot(
			context.Background(),
			toolapproval.ClientRootAskRequest{
				SignerID:       "claude-code",
				SignerRevision: 1,
				SignedPath:     signedPath,
			},
		)
		result <- askResult{outcome: outcome, err: askErr}
	}()
	select {
	case early := <-result:
		t.Fatalf(
			"client Root ask returned before a decision: outcome=%+v err=%v",
			early.outcome,
			early.err,
		)
	case <-time.After(50 * time.Millisecond):
	}

	pending := waitForPendingKind(t, authority, toolapproval.KindClientRootAsk)
	if len(pending.SubjectLabels) != 1 || pending.SubjectLabels[0] != signedPath {
		t.Fatalf("live approval labels = %q", pending.SubjectLabels)
	}
	stored, err := repository.Get(context.Background(), pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(stored.SubjectLabels, "\n"), signedPath) {
		t.Fatalf("signed path entered the durable approval: %q", stored.SubjectLabels)
	}
	if len(stored.SubjectLabels) != 1 || stored.SubjectLabels[0] != "claude-code" {
		t.Fatalf("durable safe labels = %q", stored.SubjectLabels)
	}

	resolved, err := authority.DecideApproval(
		context.Background(),
		toolapproval.DecisionCommand{
			ApprovalID:       pending.ID,
			ExpectedRevision: pending.Revision,
			IdempotencyKey:   "client-root-no-store-0001",
			Decision:         toolapproval.DecisionDeny,
			Scope:            toolapproval.ScopeRequest,
			ReasonCode:       "user_denied",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.SubjectLabels) != 1 || resolved.SubjectLabels[0] != signedPath {
		t.Fatalf("decision response lost live evidence: %q", resolved.SubjectLabels)
	}
	answer := <-result
	if answer.err != nil || answer.outcome.Allowed {
		t.Fatalf("ask result = %+v", answer)
	}
	after, err := authority.GetApproval(context.Background(), pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(after.SubjectLabels, "\n"), signedPath) {
		t.Fatalf("terminal read retained signed path: %q", after.SubjectLabels)
	}
}

// Waiting is bounded. A connection must not be held open forever because
// nobody is looking at the queue, and the end of the wait is a denial.
func TestAnUnansweredAskDeniesWhenItsTimeRunsOut(t *testing.T) {
	t.Parallel()

	authority := newAskAuthority(t, 150*time.Millisecond)
	started := time.Now()
	outcome, err := authority.AskNetwork(context.Background(), askRequest())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Allowed {
		t.Fatal("an unanswered ask allowed the connection")
	}
	if outcome.ReasonCode != "approval_expired" {
		t.Fatalf("reason = %q", outcome.ReasonCode)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("waited %s", elapsed)
	}
}

// One caller going away answers only for itself. The others are still waiting
// on a person, and a person has not decided anything yet.
func TestACallerLeavingDoesNotAnswerForTheOthers(t *testing.T) {
	t.Parallel()

	authority := newAskAuthority(t, 10*time.Second)
	leaving, cancelLeaving := context.WithCancel(context.Background())
	left := make(chan toolapproval.NetworkAskOutcome, 1)
	stayed := make(chan toolapproval.NetworkAskOutcome, 1)
	go func() {
		outcome, _ := authority.AskNetwork(leaving, askRequest())
		left <- outcome
	}()
	waitForPendingKind(t, authority, toolapproval.KindNetworkAsk)
	go func() {
		outcome, _ := authority.AskNetwork(context.Background(), askRequest())
		stayed <- outcome
	}()
	waitForWaiters(t, authority, 2)

	cancelLeaving()
	departed := <-left
	if departed.Allowed || departed.ReasonCode != "connection_canceled" {
		t.Fatalf("departed outcome = %+v", departed)
	}
	select {
	case remaining := <-stayed:
		t.Fatalf("the remaining caller was answered too: %+v", remaining)
	case <-time.After(150 * time.Millisecond):
	}

	pending := waitForPendingKind(t, authority, toolapproval.KindNetworkAsk)
	if _, err := authority.DecideApproval(
		context.Background(),
		toolapproval.DecisionCommand{
			ApprovalID:       pending.ID,
			ExpectedRevision: pending.Revision,
			Decision:         toolapproval.DecisionAllowOnce,
			Scope:            toolapproval.ScopeRequest,
			IdempotencyKey:   "ask-allow-idempotency-0003",
		},
	); err != nil {
		t.Fatal(err)
	}
	if remaining := <-stayed; !remaining.Allowed {
		t.Fatalf("remaining outcome = %+v", remaining)
	}
}

func waitForPendingKind(
	t *testing.T,
	authority *toolapproval.Authority,
	kind toolapproval.Kind,
) toolapproval.View {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		page, err := authority.ListApprovals(
			context.Background(),
			toolapproval.PageRequest{Limit: 20},
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, view := range page.Items {
			if view.State == toolapproval.StatePending &&
				view.Kind == string(kind) {
				return view
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no pending %s appeared", kind)
	return toolapproval.View{}
}

func waitForWaiters(
	t *testing.T,
	authority *toolapproval.Authority,
	want uint32,
) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view := waitForPendingKind(t, authority, toolapproval.KindNetworkAsk)
		if view.WaiterCount >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waiter count never reached %d", want)
}
