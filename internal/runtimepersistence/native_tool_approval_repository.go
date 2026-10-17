package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func (r *nativeToolRepository) nativeApprovalAuthority(ctx context.Context, tx *sql.Tx, op nativetool.Operation, now time.Time) error {
	if op.BindingDigest == nil || (op.Kind != nativetool.RequestToolsCall && op.Kind != nativetool.RequestServerReply) || (op.State != "validated" && op.State != "awaiting_decision") {
		return nativetool.ErrDenied
	}
	b, err := loadNativeBinding(ctx, tx, op.ID)
	if err != nil {
		return err
	}
	s, err := loadNativeSession(ctx, tx, b.session)
	if err != nil {
		return err
	}
	if err := validateNativeSession(ctx, tx, s, now); err != nil {
		return err
	}
	if s.state != "ready" {
		return nativetool.ErrDenied
	}
	if op.Kind == nativetool.RequestServerReply {
		if err := r.validateNativeParent(ctx, tx, op, b, s, now); err != nil {
			return err
		}
	} else if s.revision != b.sessionRevision || s.generation != b.generation || string(s.catalog) != string(b.catalog) {
		return nativetool.ErrConflict
	}
	e, exists, err := loadEnvironmentRevision(ctx, tx, op.EnvironmentID, op.EnvironmentRevision)
	if err != nil {
		return err
	}
	if !exists || e.EffectivePolicySet().ToolMode == environment.ToolPolicyStrict {
		return nativetool.ErrDenied
	}
	scope, err := loadNativeScope(ctx, tx, op.Capture)
	if err != nil {
		return err
	}
	if e.EffectivePolicySet().ToolMode != environment.ToolPolicyReview && !scope.barrier {
		return nativetool.ErrDenied
	}
	return nil
}
func (r *nativeToolRepository) RequestApproval(ctx context.Context, operation, id nativetool.ID, now, expires time.Time) (nativetool.Approval, error) {
	r.issuerMu.Lock()
	defer r.issuerMu.Unlock()
	if !nativeID(operation) || !nativeID(id) || !nativeTime(now) || !expires.After(now) {
		return nativetool.Approval{}, nativetool.ErrInvalid
	}
	var a nativetool.Approval
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		op, err := loadNativeOperation(ctx, tx, operation)
		if err != nil {
			return err
		}
		if err := r.nativeApprovalAuthority(ctx, tx, op, now); err != nil {
			return err
		}
		if op.ApprovalID != nil {
			a, err = loadNativeApproval(ctx, tx, *op.ApprovalID)
			if err != nil {
				return err
			}
			if a.State != "pending" || !a.ExpiresAt.After(now) {
				return nativetool.ErrDenied
			}
			return nil
		}
		if op.State != "validated" {
			return nativetool.ErrConflict
		}
		b, err := loadNativeBinding(ctx, tx, operation)
		if err != nil {
			return err
		}
		s, err := loadNativeSession(ctx, tx, b.session)
		if err != nil {
			return err
		}
		scope, err := loadNativeScope(ctx, tx, op.Capture)
		if err != nil {
			return err
		}
		if expires.After(s.expires) {
			expires = s.expires
		}
		if expires.After(scope.expires) {
			expires = scope.expires
		}
		if !expires.After(now) {
			return nativetool.ErrDenied
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO native_tool_approvals(approval_id,operation_id,binding_digest,revision,state,created_at_unix_ms,expires_at_unix_ms,resolved_at_unix_ms,idempotency_key,consumed_at_unix_ms) VALUES(?,?,?,1,'pending',?,?,NULL,'',NULL)`, id, operation, b.digest[:], now.UnixMilli(), expires.UnixMilli()); err != nil {
			return err
		}
		op.ApprovalID = &id
		if err := nativeTransition(ctx, tx, op, "awaiting_decision", "none", "", "awaiting_decision", now); err != nil {
			return err
		}
		a, err = loadNativeApproval(ctx, tx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, nativetool.ErrUnavailable) {
			if c := r.callbacks[r.replies[operation]]; c != nil {
				c.retired = true
				r.invalidateNativeStream(c.stream)
			}
		}
		return nativetool.Approval{}, err
	}
	return a, nil
}
func resolveNativeApproval(ctx context.Context, tx *sql.Tx, a nativetool.Approval, op nativetool.Operation, state, key string, now time.Time) error {
	if a.Revision == math.MaxInt64 {
		return nativetool.ErrInvalid
	}
	result, err := tx.ExecContext(ctx, `UPDATE native_tool_approvals SET state=?,revision=revision+1,resolved_at_unix_ms=?,idempotency_key=? WHERE approval_id=? AND revision=? AND state='pending'`, state, now.UnixMilli(), key, a.ID, a.Revision)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return nativetool.ErrConflict
	}
	switch state {
	case "allowed":
		return nativeTransition(ctx, tx, op, "validated", "user", "", "allowed", now)
	case "denied":
		return nativeTransition(ctx, tx, op, "denied", "user", "approval_denied", "denied", now)
	case "expired":
		return nativeTransition(ctx, tx, op, "denied", "none", "approval_expired", "denied", now)
	case "canceled":
		return nativeTransition(ctx, tx, op, "canceled", "none", "canceled", "canceled", now)
	}
	return nativetool.ErrInvalid
}
func (r *nativeToolRepository) DecideApproval(ctx context.Context, in nativetool.DecisionInput) (nativetool.Approval, error) {
	r.issuerMu.Lock()
	defer r.issuerMu.Unlock()
	if !nativeID(in.ApprovalID) || !nativeRevision(in.ExpectedRevision) || !nativeTime(in.ObservedAt) || (in.Decision != nativetool.DecisionAllowOnce && in.Decision != nativetool.DecisionDeny) || len(in.IdempotencyKey) < 16 || len(in.IdempotencyKey) > 512 {
		return nativetool.Approval{}, nativetool.ErrInvalid
	}
	var answer nativetool.Approval
	var operation nativetool.ID
	denied := false
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		a, err := loadNativeApproval(ctx, tx, in.ApprovalID)
		if err != nil {
			return err
		}
		operation = a.OperationID
		op, err := loadNativeOperation(ctx, tx, a.OperationID)
		if err != nil {
			return err
		}
		if op.BindingDigest == nil || *op.BindingDigest != a.BindingDigest {
			return nativetool.ErrInvalid
		}
		desired := "allowed"
		if in.Decision == nativetool.DecisionDeny {
			desired = "denied"
		}
		if a.State != "pending" {
			var key string
			if err := tx.QueryRowContext(ctx, `SELECT idempotency_key FROM native_tool_approvals WHERE approval_id=?`, a.ID).Scan(&key); err != nil {
				return err
			}
			if a.State == desired && key == in.IdempotencyKey && a.Revision == in.ExpectedRevision+1 {
				answer = a
				return nil
			}
			return nativetool.ErrConflict
		}
		if op.Kind == nativetool.RequestServerReply {
			if _, _, _, err := r.nativeExecutionAuthority(ctx, tx, op, in.ObservedAt); err != nil {
				return err
			}
		}
		if a.Revision != in.ExpectedRevision {
			return nativetool.ErrConflict
		}
		if !a.ExpiresAt.After(in.ObservedAt) {
			if err := resolveNativeApproval(ctx, tx, a, op, "expired", "", in.ObservedAt); err != nil {
				return err
			}
			denied = true
			return nil
		}
		if err := r.nativeApprovalAuthority(ctx, tx, op, in.ObservedAt); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_approvals WHERE idempotency_key=?`, in.IdempotencyKey).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return nativetool.ErrConflict
		}
		if err := resolveNativeApproval(ctx, tx, a, op, desired, in.IdempotencyKey, in.ObservedAt); err != nil {
			return err
		}
		answer, err = loadNativeApproval(ctx, tx, a.ID)
		return err
	})
	if err != nil {
		if errors.Is(err, nativetool.ErrUnavailable) {
			if c := r.callbacks[r.replies[operation]]; c != nil {
				c.retired = true
				r.invalidateNativeStream(c.stream)
			}
		}
		return nativetool.Approval{}, err
	}
	if denied {
		return nativetool.Approval{}, nativetool.ErrDenied
	}
	return answer, nil
}
func cancelNativeApprovals(ctx context.Context, tx *sql.Tx, c captureidentity.Reference, now time.Time, state string) error {
	return cancelNativeSelectedApprovals(ctx, tx, c, nil, now, state)
}
func cancelNativeSessionApprovals(ctx context.Context, tx *sql.Tx, c captureidentity.Reference, session nativetool.ID, now time.Time, state string) error {
	return cancelNativeSelectedApprovals(ctx, tx, c, &session, now, state)
}
func cancelNativeSelectedApprovals(ctx context.Context, tx *sql.Tx, c captureidentity.Reference, session *nativetool.ID, now time.Time, state string) error {
	return cancelNativeScopedApprovals(ctx, tx, c, session, nil, now, state)
}

func cancelNativeParentApprovals(ctx context.Context, tx *sql.Tx, c captureidentity.Reference, parent nativetool.ID, now time.Time) error {
	return cancelNativeScopedApprovals(ctx, tx, c, nil, &parent, now, "canceled")
}

func cancelNativeScopedApprovals(ctx context.Context, tx *sql.Tx, c captureidentity.Reference, session, parent *nativetool.ID, now time.Time, state string) error {
	query := `SELECT a.approval_id FROM native_tool_approvals a JOIN native_tool_operations o ON o.operation_id=a.operation_id WHERE o.capture_kind=? AND o.capture_id=? AND (a.state='pending' OR (a.state='allowed' AND a.consumed_at_unix_ms IS NULL))`
	arguments := []any{c.Kind, c.ID}
	if session != nil {
		query += ` AND EXISTS(SELECT 1 FROM native_tool_bindings b WHERE b.operation_id=o.operation_id AND b.session_id=?)`
		arguments = append(arguments, *session)
	}
	if parent != nil {
		query += ` AND EXISTS(SELECT 1 FROM native_tool_bindings b WHERE b.operation_id=o.operation_id AND b.parent_operation_id=?)`
		arguments = append(arguments, *parent)
	}
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return err
	}
	ids := []nativetool.ID{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		parsed, err := nativetool.NewID(id)
		if err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, parsed)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		a, err := loadNativeApproval(ctx, tx, id)
		if err != nil {
			return err
		}
		op, err := loadNativeOperation(ctx, tx, a.OperationID)
		if err != nil {
			return err
		}
		if now.Before(a.CreatedAt) {
			now = a.CreatedAt
		}
		if now.Before(op.MaximumEventAt) {
			now = op.MaximumEventAt
		}
		if a.State == "allowed" {
			// A known Start consumes approval atomically. An unconsumed allow on
			// any other state is corrupt, and cancellation must not conceal it.
			if op.State != "validated" {
				return nativetool.ErrInvalid
			}
			result, err := tx.ExecContext(ctx, `UPDATE native_tool_approvals SET state='canceled',revision=revision+1,idempotency_key='' WHERE approval_id=? AND revision=? AND state='allowed' AND consumed_at_unix_ms IS NULL`, a.ID, a.Revision)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if count != 1 {
				return nativetool.ErrConflict
			}
			if err := nativeTransition(ctx, tx, op, "canceled", op.DecisionSource, "canceled", "canceled", now); err != nil {
				return err
			}
			continue
		}
		if err := resolveNativeApproval(ctx, tx, a, op, state, "", now); err != nil {
			return err
		}
	}
	return nil
}
func (r *nativeToolRepository) CloseApproval(ctx context.Context, id nativetool.ID, expected uint64, closure nativetool.ApprovalClosure, now time.Time) (nativetool.Approval, error) {
	if !nativeID(id) || !nativeRevision(expected) || !nativeTime(now) || (closure != nativetool.ApprovalClosureCancelled && closure != nativetool.ApprovalClosureExpired) {
		return nativetool.Approval{}, nativetool.ErrInvalid
	}
	var answer nativetool.Approval
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		a, err := loadNativeApproval(ctx, tx, id)
		if err != nil {
			return err
		}
		if a.State != "pending" || a.Revision != expected {
			return nativetool.ErrConflict
		}
		if closure == nativetool.ApprovalClosureExpired && now.Before(a.ExpiresAt) {
			return nativetool.ErrDenied
		}
		if now.Before(a.CreatedAt) {
			return nativetool.ErrInvalid
		}
		op, err := loadNativeOperation(ctx, tx, a.OperationID)
		if err != nil {
			return err
		}
		if op.BindingDigest == nil || *op.BindingDigest != a.BindingDigest || op.State != "awaiting_decision" {
			return nativetool.ErrInvalid
		}
		b, err := loadNativeBinding(ctx, tx, op.ID)
		if err != nil {
			return err
		}
		s, err := loadNativeScope(ctx, tx, op.Capture)
		if err != nil {
			return err
		}
		if s.owner != b.owner || s.state == "closed" {
			return nativetool.ErrDenied
		}
		if err := resolveNativeApproval(ctx, tx, a, op, string(closure), "", now); err != nil {
			return err
		}
		answer, err = loadNativeApproval(ctx, tx, id)
		return err
	})
	if err != nil {
		return nativetool.Approval{}, err
	}
	return answer, nil
}
