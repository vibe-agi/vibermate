package runtimepersistence

import (
	"context"
	"database/sql"
	"math"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func nativeSessionCloseAuthority(ctx context.Context, tx *sql.Tx, s nativeSession, now time.Time) error {
	scope, err := loadNativeScope(ctx, tx, s.capture)
	if err != nil {
		return err
	}
	if scope.owner != s.owner || scope.state != "active" {
		return nativetool.ErrDenied
	}
	if err = nativeCapture(ctx, tx, s.capture, now); err != nil {
		return err
	}
	assignment, exists, err := loadCaptureAssignment(ctx, tx, s.capture)
	if err != nil {
		return err
	}
	if !exists || uint64(assignment.Revision) != s.assignment || assignment.EnvironmentID != s.environment || assignment.EnvironmentRevision != s.environmentRevision || assignment.EnvironmentDigest != s.digest {
		return nativetool.ErrDenied
	}
	current, _, exists, err := loadEnvironmentAggregate(ctx, tx, s.environment)
	if err != nil {
		return err
	}
	if !exists || current.State != environment.StateActive || current.ClientToolAccess == nil || !current.ClientToolAccess.Enabled {
		return nativetool.ErrDenied
	}
	frozen, exists, err := loadEnvironmentRevision(ctx, tx, s.environment, s.environmentRevision)
	if err != nil {
		return err
	}
	digest, err := environment.Digest(frozen)
	if err != nil {
		return err
	}
	if !exists || frozen.State != environment.StateActive || digest != s.digest || frozen.ClientToolAccess == nil || !frozen.ClientToolAccess.Enabled || frozen.ClientToolAccess.Route == nil {
		return nativetool.ErrDenied
	}
	route := frozen.ClientToolAccess.Route
	if route.Validate() != nil || route.ID.String() != s.route || uint64(route.Revision) != s.routeRevision || route.TargetOrigin.String() != s.origin {
		return nativetool.ErrDenied
	}
	return nil
}
func (r *nativeToolRepository) TouchSession(ctx context.Context, id nativetool.ID, expected uint64, expires, now time.Time) error {
	if !nativeID(id) || !nativeRevision(expected) || !nativeTime(now) || !expires.After(now) || expires.After(now.Add(30*time.Minute)) {
		return nativetool.ErrInvalid
	}
	return r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		s, err := loadNativeSession(ctx, tx, id)
		if err != nil {
			return err
		}
		if s.revision != expected {
			return nativetool.ErrConflict
		}
		if err = validateNativeSession(ctx, tx, s, now); err != nil {
			return err
		}
		if err = nativeSessionCloseAuthority(ctx, tx, s, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE native_tool_sessions SET expires_at_unix_ms=MAX(expires_at_unix_ms,?) WHERE session_id=? AND revision=?`, expires.UnixMilli(), id, expected); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE native_tool_scopes SET expires_at_unix_ms=MAX(expires_at_unix_ms,?) WHERE capture_kind=? AND capture_id=? AND owner_epoch=? AND state='active'`, expires.UnixMilli(), s.capture.Kind, s.capture.ID, s.owner)
		return err
	})
}
func (r *nativeToolRepository) CloseSession(ctx context.Context, id nativetool.ID, expected uint64, now time.Time) (uint64, error) {
	if !nativeID(id) || !nativeRevision(expected) || expected == math.MaxInt64 || !nativeTime(now) {
		return 0, nativetool.ErrInvalid
	}
	r.issuerMu.Lock()
	defer r.issuerMu.Unlock()
	defer func() {
		for a, s := range r.streams {
			if s.session == id && !s.cleanupOnly {
				r.dropNativeStream(a)
			}
		}
	}()
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		s, err := loadNativeSession(ctx, tx, id)
		if err != nil {
			return err
		}
		if s.revision != expected {
			return nativetool.ErrConflict
		}
		if s.state != "ready" && s.state != "initializing" {
			return nativetool.ErrDenied
		}
		if err = nativeSessionCloseAuthority(ctx, tx, s, now); err != nil {
			return err
		}
		if err = cancelNativeSessionApprovals(ctx, tx, s.capture, id, now, "canceled"); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT o.operation_id FROM native_tool_operations o JOIN native_tool_bindings b ON b.operation_id=o.operation_id WHERE b.session_id=? AND o.state IN('received','validated','started')`, id)
		if err != nil {
			return err
		}
		var operations []nativetool.ID
		for rows.Next() {
			var operation string
			if err = rows.Scan(&operation); err != nil {
				rows.Close()
				return err
			}
			operations = append(operations, nativetool.ID(operation))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, operation := range operations {
			op, err := loadNativeOperation(ctx, tx, operation)
			if err != nil {
				return err
			}
			at := now
			if at.Before(op.CreatedAt) {
				at = op.CreatedAt
			}
			if op.State == "started" {
				if err = finishNative(ctx, tx, op, nativetool.TerminalInput{Outcome: "uncertain", Reason: "execution_uncertain", ObservedAt: at}); err != nil {
					return err
				}
			} else {
				if err = nativeTransition(ctx, tx, op, "canceled", op.DecisionSource, "revoked", "revoked", at); err != nil {
					return err
				}
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE native_tool_sessions SET state='revoked',revision=revision+1 WHERE session_id=? AND revision=?`, id, expected)
		return err
	})
	if err != nil {
		return 0, err
	}
	return expected + 1, nil
}
