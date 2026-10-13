package nativemcp

import (
	"context"
	"time"

	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func (e *executor) cancelTarget(s *session, r rpc) error {
	identity, err := identity(r.params["requestId"], e.limits.OpaqueIDBytes)
	if err != nil {
		return err
	}
	s.mu.Lock()
	target := s.active[string(identity)]
	if target != nil {
		target.protocolCanceled = true
		if target.stream != nil {
			target.stream.protocolCanceled = true
		}
		if target.cancel != nil {
			target.cancel()
		}
	}
	s.mu.Unlock()
	if target == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	op, err := e.dependencies.Store.Get(ctx, target.operation)
	if err != nil {
		return err
	}
	if op.State == "awaiting_decision" && op.ApprovalID != nil {
		approval, err := e.dependencies.Store.GetApproval(ctx, *op.ApprovalID)
		if err != nil {
			return err
		}
		_, err = e.dependencies.Store.CloseApproval(ctx, approval.ID, approval.Revision, nativetool.ApprovalClosureCancelled, e.dependencies.Clock().UTC())
		return err
	}
	if op.State == "received" || op.State == "validated" {
		return e.dependencies.Store.Finish(ctx, op.ID, nativetool.TerminalInput{Outcome: "canceled", Reason: "canceled", ObservedAt: e.dependencies.Clock().UTC()})
	}
	return nil
}
