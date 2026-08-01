package loopbackproxy

import (
	"context"
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

type stubApprovals struct {
	outcome toolapproval.NetworkAskOutcome
	err     error
	asked   toolapproval.NetworkAskRequest
}

func (stub *stubApprovals) AskNetwork(
	_ context.Context,
	request toolapproval.NetworkAskRequest,
) (toolapproval.NetworkAskOutcome, error) {
	stub.asked = request
	return stub.outcome, stub.err
}

func askingOutcome() connectionpolicy.Outcome {
	return connectionpolicy.Outcome{
		Decision: connectionpolicy.DecisionAsk,
		RuleID:   "ask.unknown-host",
		Revision: 3,
	}
}

// Nothing that fails on the way to a person may become a reason to connect.
// This is the one direction the ask path must never fail in.
func TestNoAskFailureProducesAnAllow(t *testing.T) {
	t.Parallel()

	failures := []struct {
		name    string
		handler *Handler
	}{
		{
			name:    "no approval surface at all",
			handler: &Handler{},
		},
		{
			name: "the approval surface errors",
			handler: &Handler{approvals: &stubApprovals{
				outcome: toolapproval.NetworkAskOutcome{},
				err:     errors.New("storage is gone"),
			}},
		},
		{
			name: "the question expires unanswered",
			handler: &Handler{approvals: &stubApprovals{
				outcome: toolapproval.NetworkAskOutcome{
					ReasonCode: "approval_expired",
				},
			}},
		},
		{
			name: "the runtime is stopping",
			handler: &Handler{approvals: &stubApprovals{
				outcome: toolapproval.NetworkAskOutcome{
					ReasonCode: "runtime_stopping",
				},
			}},
		},
		{
			name: "a person denies",
			handler: &Handler{approvals: &stubApprovals{
				outcome: toolapproval.NetworkAskOutcome{ReasonCode: "denied"},
			}},
		},
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			resolution := failure.handler.resolveAsk(
				context.Background(),
				askingOutcome(),
				"run-1",
				"api.example.com",
				443,
			)
			if resolution.outcome.Decision != connectionpolicy.DecisionDeny {
				t.Fatalf("decision = %q", resolution.outcome.Decision)
			}
			if resolution.reason == "" {
				t.Fatal("a denial carried no reason")
			}
		})
	}
}

// An answered ask keeps naming the rule that asked. The rule is what a person
// edits; packing the answer into its identifier would make the record describe
// a rule that does not exist.
func TestAnAnsweredAskStillNamesTheRuleThatAsked(t *testing.T) {
	t.Parallel()

	handler := &Handler{approvals: &stubApprovals{
		outcome: toolapproval.NetworkAskOutcome{Allowed: true},
	}}
	resolution := handler.resolveAsk(
		context.Background(),
		askingOutcome(),
		"run-1",
		"api.example.com",
		443,
	)
	if resolution.outcome.Decision != connectionpolicy.DecisionAllow {
		t.Fatalf("decision = %q", resolution.outcome.Decision)
	}
	if resolution.outcome.RuleID != "ask.unknown-host" ||
		resolution.outcome.Revision != 3 {
		t.Fatalf("outcome = %+v", resolution.outcome)
	}
}

// The question carries the target and who asked, and nothing else. A
// connection that has not been dialled has no path, header, or credential to
// leak into a prompt.
func TestAnAskCarriesOnlyTheTargetAndTheIngress(t *testing.T) {
	t.Parallel()

	stub := &stubApprovals{
		outcome: toolapproval.NetworkAskOutcome{Allowed: true},
	}
	handler := &Handler{approvals: stub}
	handler.resolveAsk(
		context.Background(),
		askingOutcome(),
		"run-7",
		"api.example.com",
		8443,
	)
	if stub.asked != (toolapproval.NetworkAskRequest{
		IngressID: "run-7",
		Host:      "api.example.com",
		Port:      8443,
	}) {
		t.Fatalf("asked = %+v", stub.asked)
	}
}
