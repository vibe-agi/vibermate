package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// nativeExecutionAuthority rechecks actual current authority and frozen rows in
// the same writer transaction as Start or Verify. A DTO or digest alone grants nothing.
func nativeExecutionAuthority(ctx context.Context, tx *sql.Tx, op nativetool.Operation, now time.Time) (nativeBindingRow, nativeScope, environment.Environment, error) {
	b, err := loadNativeBinding(ctx, tx, op.ID)
	if err != nil {
		return b, nativeScope{}, environment.Environment{}, err
	}
	scope, err := nativeActiveScope(ctx, tx, op.Capture, b.owner, now)
	if err != nil {
		return b, scope, environment.Environment{}, err
	}
	s, err := loadNativeSession(ctx, tx, b.session)
	if err != nil {
		return b, scope, environment.Environment{}, err
	}
	if err := validateNativeSession(ctx, tx, s, now); err != nil {
		return b, scope, environment.Environment{}, err
	}
	e, exists, err := loadEnvironmentRevision(ctx, tx, op.EnvironmentID, op.EnvironmentRevision)
	if err != nil {
		return b, scope, e, err
	}
	if !exists || e.ClientToolAccess == nil || !e.ClientToolAccess.Enabled || e.ClientToolAccess.Route == nil || e.ClientToolAccess.Route.Validate() != nil {
		return b, scope, e, nativetool.ErrDenied
	}
	digest, err := environment.Digest(e)
	if err != nil {
		return b, scope, e, err
	}
	route := e.ClientToolAccess.Route
	if digest != op.EnvironmentDigest || e.ContentRecording != op.Recording || op.BindingDigest == nil || *op.BindingDigest != b.digest || s.capture != op.Capture || s.environment != op.EnvironmentID || s.environmentRevision != op.EnvironmentRevision || s.digest != op.EnvironmentDigest || b.assignment != s.assignment || b.owner != s.owner || b.route != route.ID.String() || b.routeRevision != uint64(route.Revision) || s.route != b.route || s.routeRevision != b.routeRevision || s.origin != route.TargetOrigin.String() || op.TargetOrigin != s.origin {
		return b, scope, e, nativetool.ErrInvalid
	}
	if op.Kind == nativetool.RequestServerReply {
		if err := validateNativeParent(ctx, tx, op, b, s, now); err != nil {
			return b, scope, e, err
		}
	} else if s.revision != b.sessionRevision || s.generation != b.generation || string(s.catalog) != string(b.catalog) {
		return b, scope, e, nativetool.ErrConflict
	}
	switch op.Kind {
	case nativetool.RequestInitialize:
		if s.state != "initializing" || s.generation != 0 {
			return b, scope, e, nativetool.ErrDenied
		}
	case nativetool.RequestInitialized:
		var count int
		if s.state != "initializing" || s.generation != 0 {
			return b, scope, e, nativetool.ErrDenied
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_operations o JOIN native_tool_bindings b ON o.operation_id=b.operation_id WHERE b.session_id=? AND b.session_revision=? AND b.owner_epoch=? AND o.rpc_kind='initialize' AND o.state='completed'`, s.id, s.revision, s.owner).Scan(&count); err != nil {
			return b, scope, e, err
		}
		if count != 1 {
			return b, scope, e, nativetool.ErrDenied
		}
	case nativetool.RequestToolsCall:
		if s.state != "ready" || s.generation == 0 || len(b.catalog) != 32 {
			return b, scope, e, nativetool.ErrDenied
		}
	case nativetool.RequestCancel:
		if s.state != "ready" || len(b.cancel) != 32 {
			return b, scope, e, nativetool.ErrDenied
		}
		if err := validateNativeCancellation(ctx, tx, op, b, s); err != nil {
			return b, scope, e, err
		}
	case nativetool.RequestToolsList, nativetool.RequestPing, nativetool.RequestGetStream:
		if s.state != "ready" {
			return b, scope, e, nativetool.ErrDenied
		}
	case nativetool.RequestDeleteSession, nativetool.RequestServerReply:
	default:
		return b, scope, e, nativetool.ErrDenied
	}
	if nativeDecisionKind(op.Kind) && e.EffectivePolicySet().ToolMode == environment.ToolPolicyStrict {
		return b, scope, e, nativetool.ErrDenied
	}
	return b, scope, e, nil
}

func validateNativeCancellation(ctx context.Context, tx *sql.Tx, op nativetool.Operation, b nativeBindingRow, s nativeSession) error {
	var target string
	if err := tx.QueryRowContext(ctx, `SELECT i.operation_id FROM native_tool_inbound i JOIN native_tool_operations o ON o.operation_id=i.operation_id WHERE i.session_id=? AND i.rpc_id_digest=? AND o.capture_kind=? AND o.capture_id=? AND o.state IN('validated','awaiting_decision','started') AND o.rpc_kind NOT IN('initialized','cancel','server_reply')`, s.id, b.cancel, s.capture.Kind, s.capture.ID).Scan(&target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nativetool.ErrDenied
		}
		return err
	}
	id, err := nativetool.NewID(target)
	if err != nil {
		return nativetool.ErrInvalid
	}
	if id == op.ID {
		return nativetool.ErrDenied
	}
	targetOp, err := loadNativeOperation(ctx, tx, id)
	if err != nil {
		return err
	}
	targetBinding, err := loadNativeBinding(ctx, tx, id)
	if err != nil {
		return err
	}
	if targetOp.BindingDigest == nil || targetBinding.session != s.id || targetBinding.owner != s.owner || targetBinding.assignment != s.assignment || targetBinding.sessionRevision > s.revision {
		return nativetool.ErrDenied
	}
	return nil
}

func nativeDecisionKind(k nativetool.RequestKind) bool {
	return k == nativetool.RequestToolsCall || k == nativetool.RequestServerReply
}

// Parent catalog/session revision stays frozen, but a reply still needs the
// real known Start, consumed decision and active audit of that exact parent.
func verifyNativeCommittedParent(ctx context.Context, tx *sql.Tx, p nativetool.Operation, b nativeBindingRow, s nativeSession, now time.Time) error {
	var attempt string
	var revision, claimed int64
	var group, key []byte
	if err := tx.QueryRowContext(ctx, `SELECT attempt_id,authority_revision,claimed_at_unix_ms,intent_digest,replay_key FROM native_tool_once WHERE operation_id=? AND capture_kind=? AND capture_id=?`, p.ID, p.Capture.Kind, p.Capture.ID).Scan(&attempt, &revision, &claimed, &group, &key); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nativetool.ErrDenied
		}
		return err
	}
	scope, err := nativeActiveScope(ctx, tx, p.Capture, s.owner, now)
	if err != nil {
		return err
	}
	if revision <= 0 || uint64(revision) != scope.revision || claimed < p.CreatedAt.UnixMilli() || claimed > now.UnixMilli() || string(group) != string(b.group[:]) {
		return nativetool.ErrDenied
	}
	expectedKey := b.replay
	if expectedKey == nil {
		d := sha256.Sum256([]byte("native-once-operation\x00" + string(p.ID)))
		expectedKey = d[:]
	}
	if string(key) != string(expectedKey) {
		return nativetool.ErrDenied
	}
	e, exists, err := loadEnvironmentRevision(ctx, tx, p.EnvironmentID, p.EnvironmentRevision)
	if err != nil {
		return err
	}
	if !exists {
		return nativetool.ErrDenied
	}
	if p.DecisionSource == "user" {
		if err := nativeApprovalForStart(ctx, tx, p, b, true, true, time.UnixMilli(claimed).UTC()); err != nil {
			return err
		}
		approval, err := loadNativeApproval(ctx, tx, *p.ApprovalID)
		if err != nil {
			return err
		}
		if approval.ConsumedAt == nil || approval.ConsumedAt.UnixMilli() != claimed {
			return nativetool.ErrDenied
		}
	} else if p.DecisionSource != "policy" || e.EffectivePolicySet().ToolMode != environment.ToolPolicyObserve {
		return nativetool.ErrDenied
	}
	actual, err := scanEgressAttempt(tx.QueryRowContext(ctx, egressAttemptSelect+` WHERE attempt_id=?`, attempt))
	if err != nil {
		return nativetool.ErrDenied
	}
	expected, err := nativeAuditAttempt(ctx, tx, p, b, e, attempt, time.UnixMilli(claimed).UTC())
	if err != nil {
		return err
	}
	if actual.Attempt.Terminal() || actual.Attempt != expected {
		return nativetool.ErrDenied
	}
	return nil
}

// Reproduces NewClientToolAttempt's exact frozen-route checks from validated SQL
// rows; private plans cannot be reconstructed by external callers or Store DTOs.
func nativeAuditAttempt(ctx context.Context, tx *sql.Tx, op nativetool.Operation, b nativeBindingRow, e environment.Environment, attempt string, now time.Time) (egressaudit.Attempt, error) {
	var connection string
	if err := tx.QueryRowContext(ctx, `SELECT connection_id FROM native_tool_operations WHERE operation_id=?`, op.ID).Scan(&connection); err != nil {
		return egressaudit.Attempt{}, err
	}
	route := e.ClientToolAccess.Route
	if route == nil || route.Validate() != nil || b.route != route.ID.String() || b.routeRevision != uint64(route.Revision) || op.TargetOrigin != route.TargetOrigin.String() {
		return egressaudit.Attempt{}, nativetool.ErrInvalid
	}
	payload := egressaudit.PayloadNone
	if op.Kind != nativetool.RequestGetStream && op.Kind != nativetool.RequestDeleteSession {
		payload = egressaudit.PayloadClientData
	}
	return egressaudit.New(egressaudit.NewInput{ID: attempt, ConnectionID: connection, Purpose: egressaudit.PurposeClientToolService, PayloadClass: payload, Parent: egressaudit.ParentRef{Kind: egressaudit.ParentClientOperation, ID: string(op.ID)}, Caller: egressaudit.CallerCore, TargetOrigin: op.TargetOrigin, Decision: egressaudit.DecisionRef{PolicyID: op.EnvironmentID.String(), PolicyRevision: uint64(op.EnvironmentRevision), Authority: egressaudit.AuthorityEnvironment, RuleID: route.ID.String(), ProxyID: route.EgressProfile.ID.String(), ProxyRevision: uint64(route.EgressProfile.Revision)}, StartedAt: now})
}

func nativeApprovalForStart(ctx context.Context, tx *sql.Tx, op nativetool.Operation, b nativeBindingRow, required, consumed bool, now time.Time) error {
	if !required {
		if op.DecisionSource == "user" {
			return nativetool.ErrDenied
		}
		return nil
	}
	if op.ApprovalID == nil || op.DecisionSource != "user" {
		return nativetool.ErrDenied
	}
	a, err := loadNativeApproval(ctx, tx, *op.ApprovalID)
	if err != nil {
		return err
	}
	if a.OperationID != op.ID || a.BindingDigest != b.digest || a.State != "allowed" || !a.ExpiresAt.After(now) || (a.ConsumedAt != nil) != consumed {
		return nativetool.ErrDenied
	}
	return nil
}

func (r *nativeToolRepository) Start(ctx context.Context, id nativetool.ID, attempt string, expected uint64, now time.Time) (nativetool.StartReceipt, error) {
	if !nativeID(id) || len(attempt) == 0 || len(attempt) > 512 || !nativeRevision(expected) || !nativeTime(now) {
		return nativetool.StartReceipt{}, nativetool.ErrInvalid
	}
	r.issuerMu.Lock()
	defer r.issuerMu.Unlock()
	var facts nativetool.StartFacts
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		op, err := loadNativeOperation(ctx, tx, id)
		if err != nil {
			return err
		}
		if op.State != "validated" {
			if op.State == "started" || op.CompletedAt != nil {
				return nativetool.ErrReplay
			}
			return nativetool.ErrDenied
		}
		b, scope, e, err := nativeExecutionAuthority(ctx, tx, op, now)
		if err != nil {
			return err
		}
		if scope.revision != expected {
			return nativetool.ErrConflict
		}
		required := nativeDecisionKind(op.Kind) && (e.EffectivePolicySet().ToolMode == environment.ToolPolicyReview || scope.barrier)
		if err := nativeApprovalForStart(ctx, tx, op, b, required, false, now); err != nil {
			return err
		}
		key := b.replay
		if key == nil {
			digest := sha256.Sum256([]byte("native-once-operation\x00" + string(id)))
			key = digest[:]
		}
		var duplicate, count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_once WHERE (capture_kind=? AND capture_id=? AND replay_key=?) OR operation_id=? OR attempt_id=?`, op.Capture.Kind, op.Capture.ID, key, id, attempt).Scan(&duplicate); err != nil {
			return err
		}
		if duplicate != 0 {
			return nativetool.ErrReplay
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_once WHERE capture_kind=? AND capture_id=?`, op.Capture.Kind, op.Capture.ID).Scan(&count); err != nil {
			return err
		}
		if count >= 100000 {
			return nativetool.ErrDenied
		}
		if required {
			result, err := tx.ExecContext(ctx, `UPDATE native_tool_approvals SET consumed_at_unix_ms=? WHERE approval_id=? AND binding_digest=? AND state='allowed' AND consumed_at_unix_ms IS NULL AND expires_at_unix_ms>?`, now.UnixMilli(), *op.ApprovalID, b.digest[:], now.UnixMilli())
			if err != nil {
				return err
			}
			n, _ := result.RowsAffected()
			if n != 1 {
				return nativetool.ErrConflict
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO native_tool_once(capture_kind,capture_id,replay_key,operation_id,intent_digest,attempt_id,claimed_at_unix_ms,authority_revision) VALUES(?,?,?,?,?,?,?,?)`, op.Capture.Kind, op.Capture.ID, key, id, b.group[:], attempt, now.UnixMilli(), scope.revision); err != nil {
			return err
		}
		audit, err := nativeAuditAttempt(ctx, tx, op, b, e, attempt, now)
		if err != nil {
			return err
		}
		if _, err := appendEgressAttempt(ctx, tx, audit); err != nil {
			return err
		}
		source := "policy"
		if required {
			source = "user"
		}
		if err := nativeTransition(ctx, tx, op, "started", source, "", "started", now); err != nil {
			return err
		}
		facts = nativetool.StartFacts{OperationID: id, AttemptID: attempt, AuthorityRevision: scope.revision, OwnerEpoch: b.owner, BindingDigest: b.digest}
		return nil
	})
	// Even a commit-then-error returns no receipt. Finding its once row later is
	// evidence of uncertainty, never a delayed license to resend.
	if err != nil {
		return nativetool.StartReceipt{}, err
	}
	return r.issuer.Issue(facts)
}

func (r *nativeToolRepository) VerifyStartReceipt(ctx context.Context, receipt nativetool.StartReceipt, now time.Time) error {
	if !nativeID(receipt.OperationID()) || !nativeTime(now) {
		return nativetool.ErrDenied
	}
	r.issuerMu.Lock()
	defer r.issuerMu.Unlock()
	return r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		op, err := loadNativeOperation(ctx, tx, receipt.OperationID())
		if err != nil {
			return err
		}
		if op.State != "started" || op.CompletedAt != nil {
			return nativetool.ErrDenied
		}
		b, scope, e, err := nativeExecutionAuthority(ctx, tx, op, now)
		if err != nil {
			return err
		}
		var attempt string
		var rev, claimed int64
		var intent, key []byte
		if err := tx.QueryRowContext(ctx, `SELECT attempt_id,authority_revision,claimed_at_unix_ms,intent_digest,replay_key FROM native_tool_once WHERE capture_kind=? AND capture_id=? AND operation_id=?`, op.Capture.Kind, op.Capture.ID, op.ID).Scan(&attempt, &rev, &claimed, &intent, &key); err != nil {
			return nativetool.ErrDenied
		}
		if rev <= 0 || uint64(rev) != scope.revision || claimed < op.CreatedAt.UnixMilli() || claimed > now.UnixMilli() || string(intent) != string(b.group[:]) || len(key) != 32 {
			return nativetool.ErrDenied
		}
		expectedKey := b.replay
		if expectedKey == nil {
			d := sha256.Sum256([]byte("native-once-operation\x00" + string(op.ID)))
			expectedKey = d[:]
		}
		if string(key) != string(expectedKey) {
			return nativetool.ErrDenied
		}
		required := nativeDecisionKind(op.Kind) && (e.EffectivePolicySet().ToolMode == environment.ToolPolicyReview || scope.barrier)
		if err := nativeApprovalForStart(ctx, tx, op, b, required, true, now); err != nil {
			return err
		}
		if required {
			approval, err := loadNativeApproval(ctx, tx, *op.ApprovalID)
			if err != nil {
				return err
			}
			if approval.ConsumedAt == nil || approval.ConsumedAt.UnixMilli() != claimed {
				return nativetool.ErrDenied
			}
		}
		actual, err := scanEgressAttempt(tx.QueryRowContext(ctx, egressAttemptSelect+` WHERE attempt_id=?`, attempt))
		if err != nil {
			return nativetool.ErrDenied
		}
		expectedAudit, err := nativeAuditAttempt(ctx, tx, op, b, e, attempt, time.UnixMilli(claimed).UTC())
		if err != nil {
			return err
		}
		if actual.Attempt.Terminal() || actual.Attempt != expectedAudit {
			return nativetool.ErrDenied
		}
		return r.issuer.Check(receipt, nativetool.StartFacts{OperationID: op.ID, AttemptID: attempt, AuthorityRevision: scope.revision, OwnerEpoch: b.owner, BindingDigest: b.digest})
	})
}

func finishNative(ctx context.Context, tx *sql.Tx, op nativetool.Operation, in nativetool.TerminalInput) error {
	if op.State != "started" {
		return nativetool.ErrConflict
	}
	var attempt string
	if err := tx.QueryRowContext(ctx, `SELECT attempt_id FROM native_tool_once WHERE operation_id=? AND capture_kind=? AND capture_id=?`, op.ID, op.Capture.Kind, op.Capture.ID).Scan(&attempt); err != nil {
		return err
	}
	record, err := scanEgressAttempt(tx.QueryRowContext(ctx, egressAttemptSelect+` WHERE attempt_id=?`, attempt))
	if err != nil {
		return err
	}
	unknown := in.Outcome == "uncertain"
	outcome := egressaudit.Outcome(in.Outcome)
	if unknown {
		outcome = egressaudit.OutcomeFailed
	}
	errorClass := in.Reason
	if in.Outcome == "completed" {
		errorClass = ""
	}
	terminal, err := record.Attempt.Finish(egressaudit.TerminalInput{Outcome: outcome, ErrorClass: errorClass, BytesIn: in.BytesIn, BytesOut: in.BytesOut, UnobservedByteCounts: unknown, CompletedAt: in.ObservedAt})
	if err != nil {
		return err
	}
	if _, err := completeEgressAttempt(ctx, tx, terminal); err != nil {
		return err
	}
	if err := nativeTransition(ctx, tx, op, in.Outcome, op.DecisionSource, in.Reason, in.Outcome, in.ObservedAt); err != nil {
		return err
	}
	var bytesIn, bytesOut any
	if !unknown {
		bytesIn = in.BytesIn
		bytesOut = in.BytesOut
	}
	if _, err := tx.ExecContext(ctx, `UPDATE native_tool_operations SET bytes_in=?,bytes_out=? WHERE operation_id=?`, bytesIn, bytesOut, op.ID); err != nil {
		return err
	}
	if unknown {
		_, err = tx.ExecContext(ctx, `UPDATE native_tool_scopes SET replay_barrier=1 WHERE capture_kind=? AND capture_id=?`, op.Capture.Kind, op.Capture.ID)
	}
	return err
}

func (r *nativeToolRepository) Finish(ctx context.Context, id nativetool.ID, in nativetool.TerminalInput) error {
	valid := in.Outcome == "completed" && in.Reason == "completed" || in.Outcome == "failed" && (in.Reason == "transport_failed" || in.Reason == "rpc_error") || in.Outcome == "canceled" && (in.Reason == "canceled" || in.Reason == "revoked") || in.Outcome == "uncertain" && in.Reason == "execution_uncertain"
	if !nativeID(id) || !nativeTime(in.ObservedAt) || !valid || in.BytesIn < 0 || in.BytesOut < 0 || in.Outcome == "uncertain" && (in.BytesIn != 0 || in.BytesOut != 0) {
		return nativetool.ErrInvalid
	}
	return r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		op, err := loadNativeOperation(ctx, tx, id)
		if err != nil {
			return err
		}
		return finishNative(ctx, tx, op, in)
	})
}

// All authoritative owner hooks call this private helper in their existing Tx.
func revokeNativeScope(ctx context.Context, tx *sql.Tx, c captureidentity.Reference, now time.Time) error {
	s, err := loadNativeScope(ctx, tx, c)
	if errors.Is(err, nativetool.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.revision == math.MaxInt64 {
		return nativetool.ErrInvalid
	}
	if err := retireNativeWork(ctx, tx, c, now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE native_tool_scopes SET state='revoked',revision=revision+1,replay_barrier=1 WHERE capture_kind=? AND capture_id=?`, c.Kind, c.ID)
	return err
}

func retireNativeWork(ctx context.Context, tx *sql.Tx, c captureidentity.Reference, now time.Time) error {
	if err := cancelNativeApprovals(ctx, tx, c, now, "canceled"); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT operation_id FROM native_tool_operations WHERE capture_kind=? AND capture_id=? AND state IN('received','validated','started')`, c.Kind, c.ID)
	if err != nil {
		return err
	}
	var ids []nativetool.ID
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		id, err := nativetool.NewID(raw)
		if err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		op, err := loadNativeOperation(ctx, tx, id)
		if err != nil {
			return err
		}
		at := now
		var last int64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(occurred_at_unix_ms) FROM native_tool_events WHERE operation_id=?`, id).Scan(&last); err != nil {
			return err
		}
		if at.UnixMilli() < last {
			at = time.UnixMilli(last).UTC()
		}
		if op.State == "started" {
			if err := finishNative(ctx, tx, op, nativetool.TerminalInput{Outcome: "uncertain", Reason: "execution_uncertain", ObservedAt: at}); err != nil {
				return err
			}
		} else {
			if err := nativeTransition(ctx, tx, op, "canceled", op.DecisionSource, "revoked", "revoked", at); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE native_tool_sessions SET state='revoked',revision=revision+1 WHERE capture_kind=? AND capture_id=? AND state IN('initializing','ready')`, c.Kind, c.ID); err != nil {
		return err
	}
	return nil
}

func revokeNativeScopesWhere(ctx context.Context, tx *sql.Tx, where string, now time.Time, args ...any) error {
	rows, err := tx.QueryContext(ctx, `SELECT capture_kind,capture_id FROM native_tool_scopes WHERE `+where, args...)
	if err != nil {
		return err
	}
	var captures []captureidentity.Reference
	for rows.Next() {
		var k, id string
		if err := rows.Scan(&k, &id); err != nil {
			rows.Close()
			return err
		}
		c, err := captureidentity.New(captureidentity.Kind(k), id)
		if err != nil {
			rows.Close()
			return err
		}
		captures = append(captures, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range captures {
		if err := revokeNativeScope(ctx, tx, c, now); err != nil {
			return err
		}
	}
	return nil
}

func (r *nativeToolRepository) Revoke(ctx context.Context, c captureidentity.Reference, expected uint64, reason string, now time.Time) error {
	if c.Validate() != nil || !nativeRevision(expected) || reason != "revoked" || !nativeTime(now) {
		return nativetool.ErrInvalid
	}
	return r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		s, err := loadNativeScope(ctx, tx, c)
		if err != nil {
			return err
		}
		if s.revision != expected {
			return nativetool.ErrConflict
		}
		return revokeNativeScope(ctx, tx, c, now)
	})
}

func (r *nativeToolRepository) MarkReplayBarrier(ctx context.Context, c captureidentity.Reference, expected uint64, now time.Time) error {
	if c.Validate() != nil || !nativeRevision(expected) || !nativeTime(now) {
		return nativetool.ErrInvalid
	}
	return r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		s, err := loadNativeScope(ctx, tx, c)
		if err != nil {
			return err
		}
		if s.revision != expected {
			return nativetool.ErrConflict
		}
		if s.state != "active" {
			return nativetool.ErrDenied
		}
		if err := nativeCapture(ctx, tx, c, now); err != nil {
			return err
		}
		if err := cancelNativeApprovals(ctx, tx, c, now, "canceled"); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE native_tool_scopes SET replay_barrier=1 WHERE capture_kind=? AND capture_id=?`, c.Kind, c.ID)
		return err
	})
}

func (r *nativeToolRepository) Recover(ctx context.Context, now time.Time) error {
	if !nativeTime(now) {
		return nativetool.ErrInvalid
	}
	r.issuerMu.Lock()
	defer r.issuerMu.Unlock()
	issuer, err := nativetool.NewStartReceiptIssuer()
	if err != nil {
		return errors.Join(nativetool.ErrUnavailable, err)
	}
	err = r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return revokeNativeScopesWhere(ctx, tx, "state<>'closed'", now)
	})
	// Rotate even on unknown commit: old receipts cannot outlive this boundary.
	r.issuer = issuer
	return err
}
