package loopbackproxy

import (
	"context"

	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

// NetworkApprovals asks a person about one connection and blocks until they
// answer. The proxy holds the connection open across this call and has dialled
// nothing, resolved no name, and issued no certificate.
type NetworkApprovals interface {
	AskNetwork(
		context.Context,
		toolapproval.NetworkAskRequest,
	) (toolapproval.NetworkAskOutcome, error)
}

// ReasonAskUnavailable is what a connection is told when nothing can put the
// question in front of a person. It is a denial, not a fallback.
const ReasonAskUnavailable ReasonCode = "network_ask_unavailable"

// askResolution is the answer to one asked connection. The rule that asked
// stays whole in the outcome; the reason travels beside it rather than being
// packed into an identifier.
type askResolution struct {
	outcome connectionpolicy.Outcome
	reason  ReasonCode
}

// resolveAsk turns an ask into an allow or a deny. Every path that is not an
// explicit human allow denies: a question the product cannot put in front of a
// person is not a reason to connect anyway.
func (handler *Handler) resolveAsk(
	ctx context.Context,
	outcome connectionpolicy.Outcome,
	ingressID string,
	host string,
	port uint16,
) askResolution {
	if handler.approvals == nil {
		outcome.Decision = connectionpolicy.DecisionDeny
		return askResolution{outcome: outcome, reason: ReasonAskUnavailable}
	}
	answer, _ := handler.approvals.AskNetwork(ctx, toolapproval.NetworkAskRequest{
		IngressID: ingressID,
		Host:      host,
		Port:      port,
	})
	if answer.Allowed {
		outcome.Decision = connectionpolicy.DecisionAllow
		return askResolution{outcome: outcome}
	}
	outcome.Decision = connectionpolicy.DecisionDeny
	reason := ReasonCode(answer.ReasonCode)
	if reason == "" {
		reason = ReasonConnectionDenied
	}
	return askResolution{outcome: outcome, reason: reason}
}
