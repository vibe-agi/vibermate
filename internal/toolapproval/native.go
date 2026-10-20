package toolapproval

import (
	"context"
	"encoding/hex"
	"io"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func (authority *Authority) decideNative(ctx context.Context, request exchange.ToolDecisionRequest) (exchange.ToolDecision, error) {
	if err := request.VerifyNativeEvidence(); err != nil {
		return exchange.ToolDecision{}, err
	}
	id, binding, native := request.NativeExecution()
	policy, available := request.NativeContext()
	if !native || !available || authority.nativeRepository == nil || binding.Digest() == (nativetool.Digest{}) || policy.PolicySet() != binding.Plan().PolicySet() {
		return exchange.ToolDecision{}, nativetool.ErrDenied
	}
	op, err := authority.nativeRepository.Get(ctx, id)
	if err != nil {
		return exchange.ToolDecision{}, err
	}
	if op.ID != id || op.BindingDigest == nil || *op.BindingDigest != binding.Digest() || op.Kind != binding.Kind() || op.Capture != binding.Capture() || op.EnvironmentID != binding.Plan().EnvironmentID() || op.EnvironmentRevision != binding.Plan().EnvironmentRevision() || op.EnvironmentDigest != binding.Plan().EnvironmentDigest() || op.TargetOrigin != binding.Plan().Route().TargetOrigin.String() {
		return exchange.ToolDecision{}, nativetool.ErrDenied
	}
	switch policy.PolicySet().ToolMode {
	case environment.ToolPolicyObserve:
		if op.State != "validated" {
			return exchange.ToolDecision{}, nativetool.ErrDenied
		}
		scope, err := authority.nativeRepository.GetScope(ctx, binding.Capture())
		if err != nil {
			return exchange.ToolDecision{}, err
		}
		if scope.State != "active" || scope.OwnerEpoch != binding.OwnerEpoch() || !scope.ExpiresAt.After(authority.clock.Now().UTC()) {
			return exchange.ToolDecision{}, nativetool.ErrDenied
		}
		if !scope.ReplayBarrier {
			return exchange.ToolDecision{Outcome: exchange.ToolDecisionApproved}, nil
		}
	case environment.ToolPolicyStrict:
		return exchange.ToolDecision{Outcome: exchange.ToolDecisionRejected, ReasonCode: "tool_policy_strict"}, nil
	case environment.ToolPolicyReview:
	default:
		return exchange.ToolDecision{}, nativetool.ErrDenied
	}
	var random [16]byte
	if _, err := io.ReadFull(authority.random, random[:]); err != nil {
		return exchange.ToolDecision{}, err
	}
	approvalID, err := nativetool.NewID(hex.EncodeToString(random[:]))
	if err != nil {
		return exchange.ToolDecision{}, err
	}
	now := authority.clock.Now().UTC()
	approval, err := authority.nativeRepository.RequestApproval(ctx, id, approvalID, now, now.Add(authority.config.DecisionTimeout))
	if err != nil {
		return exchange.ToolDecision{}, err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(authority.config.DecisionTimeout)
	defer timeout.Stop()
	closeQuestion := func(closure nativetool.ApprovalClosure) {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = authority.nativeRepository.CloseApproval(cleanup, approval.ID, approval.Revision, closure, authority.clock.Now().UTC())
	}
	for {
		authority.mu.Lock()
		closing := authority.closing
		authority.mu.Unlock()
		if closing {
			closeQuestion(nativetool.ApprovalClosureCancelled)
			return exchange.ToolDecision{}, ErrRuntimeStopping
		}
		record, err := authority.nativeRepository.GetApproval(ctx, approval.ID)
		if err != nil {
			closure := nativetool.ApprovalClosureCancelled
			if !approval.ExpiresAt.After(authority.clock.Now().UTC()) {
				closure = nativetool.ApprovalClosureExpired
			}
			closeQuestion(closure)
			return exchange.ToolDecision{}, err
		}
		if record.ID != approval.ID || record.OperationID != id || record.BindingDigest != binding.Digest() {
			closeQuestion(nativetool.ApprovalClosureCancelled)
			return exchange.ToolDecision{}, nativetool.ErrDenied
		}
		switch record.State {
		case "allowed":
			if record.ConsumedAt != nil || !record.ExpiresAt.After(authority.clock.Now().UTC()) {
				return exchange.ToolDecision{}, nativetool.ErrDenied
			}
			if err := request.VerifyNativeEvidence(); err != nil {
				return exchange.ToolDecision{}, err
			}
			return exchange.ToolDecision{Outcome: exchange.ToolDecisionApproved}, nil
		case "denied", "canceled", "expired":
			return exchange.ToolDecision{Outcome: exchange.ToolDecisionRejected, ReasonCode: "native_approval_denied"}, nil
		case "pending":
		default:
			return exchange.ToolDecision{}, nativetool.ErrDenied
		}
		if !record.ExpiresAt.After(authority.clock.Now().UTC()) {
			closeQuestion(nativetool.ApprovalClosureExpired)
			return exchange.ToolDecision{Outcome: exchange.ToolDecisionRejected, ReasonCode: "native_approval_expired"}, nil
		}
		select {
		case <-ctx.Done():
			closeQuestion(nativetool.ApprovalClosureCancelled)
			return exchange.ToolDecision{}, ctx.Err()
		case <-timeout.C:
			closeQuestion(nativetool.ApprovalClosureExpired)
			return exchange.ToolDecision{Outcome: exchange.ToolDecisionRejected, ReasonCode: "native_approval_expired"}, nil
		case <-ticker.C:
		}
	}
}
