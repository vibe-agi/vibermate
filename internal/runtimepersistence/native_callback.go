package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// Fixed-size entries are charged to the repository's bounded process registry.
// Known completed replies leave a durable inbound denial fence, so their live
// registrations can be reclaimed without allowing callback replay.
type nativeObservedCallback struct {
	stream  nativetool.StreamAuthority
	facts   nativetool.CallbackFacts
	child   nativetool.ID
	digest  nativetool.Digest
	receipt nativetool.StartReceipt
	retired bool
}

func (r *nativeToolRepository) hasNativeCallbacks(stream nativetool.StreamAuthority) bool {
	for _, callback := range r.callbacks {
		if callback.stream == stream {
			return true
		}
	}
	return false
}

func (r *nativeToolRepository) RegisterObservedCallback(ctx context.Context, stream nativetool.StreamAuthority, facts nativetool.CallbackFacts, now time.Time) (nativetool.CallbackAuthority, error) {
	if !nativeTime(now) || facts.RPCIdentityDigest == (nativetool.Digest{}) || facts.RequestDigest == (nativetool.Digest{}) {
		return nativetool.CallbackAuthority{}, nativetool.ErrInvalid
	}
	switch facts.Kind {
	case nativetool.CallbackPing, nativetool.CallbackForm, nativetool.CallbackURL, nativetool.CallbackVerification:
	default:
		return nativetool.CallbackAuthority{}, nativetool.ErrInvalid
	}
	r.issuerMu.Lock()
	defer r.issuerMu.Unlock()
	if len(r.callbacks) >= nativeStreamLimit {
		return nativetool.CallbackAuthority{}, nativetool.ErrDenied
	}
	for _, c := range r.callbacks {
		if c.stream == stream && c.facts.RPCIdentityDigest == facts.RPCIdentityDigest {
			return nativetool.CallbackAuthority{}, nativetool.ErrReplay
		}
	}
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		op, err := r.streamAuthority(ctx, tx, stream, now, false)
		if err != nil {
			return err
		}
		if op.Kind != nativetool.RequestToolsCall && op.Kind != nativetool.RequestGetStream {
			return nativetool.ErrDenied
		}
		// A row can deny reuse but can never issue observation authority.
		var prior int
		s := r.streams[stream]
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_inbound WHERE session_id=? AND rpc_id_digest=? AND request_kind='server_reply'`, s.session, facts.RPCIdentityDigest[:]).Scan(&prior); err != nil {
			return err
		}
		if prior != 0 {
			return nativetool.ErrReplay
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, nativetool.ErrUnavailable) {
			r.invalidateNativeStream(stream)
		}
		return nativetool.CallbackAuthority{}, err
	}
	a, err := r.issuer.IssueCallback()
	if err != nil {
		return a, err
	}
	if r.callbacks == nil {
		r.callbacks = make(map[nativetool.CallbackAuthority]*nativeObservedCallback)
		r.replies = make(map[nativetool.ID]nativetool.CallbackAuthority)
	}
	r.callbacks[a] = &nativeObservedCallback{stream: stream, facts: facts}
	return a, nil
}

func (r *nativeToolRepository) BindObservedReply(ctx context.Context, id nativetool.ID, b nativetool.Binding, a nativetool.CallbackAuthority, witness nativetool.RegisteredParentWitness, now time.Time) error {
	if !nativeTime(now) || witness.Verify(id, b) != nil {
		return nativetool.ErrDenied
	}
	r.issuerMu.Lock()
	defer r.issuerMu.Unlock()
	c := r.callbacks[a]
	if c == nil || c.retired || c.child != "" {
		return nativetool.ErrDenied
	}
	request, ok := b.ServerRequestDigest()
	rpc, hasRPC := b.RPCIDDigest()
	parent, hasParent := b.ParentOperationID()
	s := r.streams[c.stream]
	if !ok || !hasRPC || !hasParent || s == nil || parent != s.receipt.OperationID() || request != c.facts.RequestDigest || rpc != c.facts.RPCIdentityDigest {
		return nativetool.ErrDenied
	}
	c.child, c.digest = id, b.Digest()
	r.replies[id] = a
	err := r.bind(ctx, id, b, now)
	if err != nil {
		c.retired = true
		if errors.Is(err, nativetool.ErrUnavailable) {
			r.invalidateNativeStream(c.stream)
		}
	}
	return err
}

// Caller holds issuerMu before entering the writer transaction.
func (r *nativeToolRepository) verifyNativeCallback(ctx context.Context, tx *sql.Tx, child, parent nativetool.Operation, now time.Time) error {
	a, ok := r.replies[child.ID]
	c := r.callbacks[a]
	if !ok || c == nil || c.retired || c.child != child.ID || child.BindingDigest != nil && *child.BindingDigest != c.digest {
		return nativetool.ErrDenied
	}
	op, err := r.streamAuthority(ctx, tx, c.stream, now, true)
	if err != nil {
		return err
	}
	if op.ID != parent.ID || parent.Kind != nativetool.RequestGetStream && parent.Kind != nativetool.RequestToolsCall || parent.Kind == nativetool.RequestToolsCall && parent.State != "started" {
		return nativetool.ErrDenied
	}
	return nil
}

func (r *nativeToolRepository) RetireObservedCallback(ctx context.Context, a nativetool.CallbackAuthority, now time.Time) ([]nativetool.StartReceipt, error) {
	if !nativeTime(now) {
		return nil, nativetool.ErrInvalid
	}
	r.issuerMu.Lock()
	defer r.issuerMu.Unlock()
	c := r.callbacks[a]
	if c == nil {
		return nil, nil
	}
	c.retired = true
	prunable := false
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := r.cleanupNativeCallback(ctx, tx, c, now); err != nil {
			return err
		}
		if c.child == "" {
			return nil
		}
		if err := r.verifyRetiredCallback(ctx, tx, c); err != nil {
			return err
		}
		prunable = true
		return nil
	})
	if err != nil {
		r.invalidateNativeStream(c.stream)
	} else if prunable {
		delete(r.replies, c.child)
		delete(r.callbacks, a)
		if c.receipt.OperationID() != "" {
			return []nativetool.StartReceipt{c.receipt}, nil
		}
	}
	return nil, err
}

// A terminal flag alone cannot release a receipt-bearing cleanup obligation.
// Pre-Start denials prove zero claims/audits; started children prove their exact
// immutable terminal attempt. The surviving inbound row only denies ID reuse.
func (r *nativeToolRepository) verifyRetiredCallback(ctx context.Context, tx *sql.Tx, c *nativeObservedCallback) error {
	op, err := loadNativeOperation(ctx, tx, c.child)
	if err != nil {
		return err
	}
	if op.CompletedAt == nil || op.Kind != nativetool.RequestServerReply || op.BindingDigest == nil || *op.BindingDigest != c.digest {
		return nativetool.ErrDenied
	}
	b, err := loadNativeBinding(ctx, tx, c.child)
	if err != nil {
		return err
	}
	stream := r.streams[c.stream]
	if stream == nil || b.session != stream.session {
		return nativetool.ErrDenied
	}
	var fence, claims, audits int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_inbound WHERE operation_id=? AND session_id=? AND request_kind='server_reply' AND rpc_id_digest=? AND request_digest=?`, c.child, b.session, c.facts.RPCIdentityDigest[:], b.request[:]).Scan(&fence); err != nil {
		return err
	}
	if fence != 1 {
		return nativetool.ErrDenied
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, c.child).Scan(&claims); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_deliveries WHERE operation_id=?`, c.child).Scan(&audits); err != nil {
		return err
	}
	if claims == 0 {
		if audits != 0 || c.receipt.OperationID() != "" {
			return nativetool.ErrDenied
		}
		return nil
	}
	if claims != 1 || audits != 1 {
		return nativetool.ErrDenied
	}
	facts, audit, err := nativeAttemptFacts(ctx, tx, op)
	if err != nil {
		return err
	}
	if !audit.Terminal() {
		return nativetool.ErrDenied
	}
	if c.receipt.OperationID() != "" {
		return r.issuer.Check(c.receipt, facts)
	}
	if op.State != "uncertain" {
		return nativetool.ErrDenied
	}
	return nil
}

func (r *nativeToolRepository) cleanupNativeCallback(ctx context.Context, tx *sql.Tx, c *nativeObservedCallback, now time.Time) error {
	if c.child == "" {
		return nil
	}
	s := r.streams[c.stream]
	if s == nil {
		return nativetool.ErrDenied
	}
	op, err := loadNativeOperation(ctx, tx, c.child)
	if err != nil {
		return err
	}
	if op.ApprovalID != nil && (op.State == "awaiting_decision" || op.State == "validated") {
		a, err := loadNativeApproval(ctx, tx, *op.ApprovalID)
		if err != nil {
			return err
		}
		if a.ConsumedAt == nil && a.State == "pending" {
			if err := resolveNativeApproval(ctx, tx, a, op, "canceled", "", now); err != nil {
				return err
			}
		}
		if a.ConsumedAt == nil && a.State == "allowed" {
			if op.State != "validated" {
				return nativetool.ErrInvalid
			}
			result, err := tx.ExecContext(ctx, `UPDATE native_tool_approvals SET state='canceled',revision=revision+1,idempotency_key='' WHERE approval_id=? AND revision=? AND state='allowed' AND consumed_at_unix_ms IS NULL`, a.ID, a.Revision)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return nativetool.ErrConflict
			}
			if err := nativeTransition(ctx, tx, op, "canceled", op.DecisionSource, "canceled", "canceled", now); err != nil {
				return err
			}
		}
	}
	return r.cleanupNativeRecoveryAttempt(ctx, tx, s, c.child, nativeRecovery{stream: c.stream, digest: c.digest, receipt: c.receipt, cleanupOnly: true}, now)
}
