package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

const nativeStreamLimit = 1024
const nativeSessionStreamLimit = 16

// Body-free fixed-size registrations are bounded before allocation. Frozen
// binding data stays in SQL; the transport separately owns its charged wire.
type nativeObservedStream struct {
	receipt     nativetool.StartReceipt
	session     nativetool.ID
	digest      nativetool.Digest
	revision    uint64
	expires     time.Time
	terminal    bool
	capture     captureidentity.Reference
	timer       *time.Timer
	cleanupOnly bool
	retryDelay  time.Duration
}
type nativeRecovery struct {
	stream nativetool.StreamAuthority
	digest nativetool.Digest
	// A post-Start cleanup obligation retains only the actual issued receipt.
	// This is part of the already bounded association, never a send grant.
	receipt     nativetool.StartReceipt
	cleanupOnly bool
}

// nativeAttemptFacts checks immutable provenance even after physical closure.
// It grants no current send authority and deliberately does not replace Verify.
func nativeAttemptFacts(ctx context.Context, tx *sql.Tx, op nativetool.Operation) (nativetool.StartFacts, egressaudit.Attempt, error) {
	var facts nativetool.StartFacts
	b, err := loadNativeBinding(ctx, tx, op.ID)
	if err != nil {
		return facts, egressaudit.Attempt{}, err
	}
	var revision, claimed int64
	var intent, key []byte
	err = tx.QueryRowContext(ctx, `SELECT attempt_id,authority_revision,claimed_at_unix_ms,intent_digest,replay_key FROM native_tool_once WHERE operation_id=? AND capture_kind=? AND capture_id=?`, op.ID, op.Capture.Kind, op.Capture.ID).Scan(&facts.AttemptID, &revision, &claimed, &intent, &key)
	if err != nil {
		return facts, egressaudit.Attempt{}, err
	}
	expectedKey := b.replay
	if expectedKey == nil {
		d := sha256.Sum256([]byte("native-once-operation\x00" + string(op.ID)))
		expectedKey = d[:]
	}
	if op.BindingDigest == nil || *op.BindingDigest != b.digest || revision <= 0 || claimed < op.CreatedAt.UnixMilli() || string(intent) != string(b.group[:]) || string(key) != string(expectedKey) {
		return facts, egressaudit.Attempt{}, nativetool.ErrDenied
	}
	e, exists, err := loadEnvironmentRevision(ctx, tx, op.EnvironmentID, op.EnvironmentRevision)
	if err != nil {
		return facts, egressaudit.Attempt{}, err
	}
	if !exists {
		return facts, egressaudit.Attempt{}, nativetool.ErrDenied
	}
	if nativeDecisionKind(op.Kind) {
		if op.DecisionSource == "user" {
			if err := nativeApprovalForStart(ctx, tx, op, b, true, true, time.UnixMilli(claimed).UTC()); err != nil {
				return facts, egressaudit.Attempt{}, err
			}
			a, err := loadNativeApproval(ctx, tx, *op.ApprovalID)
			if err != nil || a.ConsumedAt == nil || a.ConsumedAt.UnixMilli() != claimed {
				return facts, egressaudit.Attempt{}, nativetool.ErrDenied
			}
		} else if op.DecisionSource != "policy" || e.EffectivePolicySet().ToolMode != "observe" {
			return facts, egressaudit.Attempt{}, nativetool.ErrDenied
		}
	} else if op.DecisionSource != "policy" {
		return facts, egressaudit.Attempt{}, nativetool.ErrDenied
	}
	record, err := scanEgressAttempt(tx.QueryRowContext(ctx, nativeDeliverySelect+` WHERE attempt_id=?`, facts.AttemptID))
	if err != nil {
		return facts, egressaudit.Attempt{}, err
	}
	expected, err := nativeAuditAttempt(ctx, tx, op, b, e, facts.AttemptID, time.UnixMilli(claimed).UTC())
	if err != nil {
		return facts, egressaudit.Attempt{}, err
	}
	a := record.Attempt
	if a.Terminal() {
		expected, err = expected.Finish(egressaudit.TerminalInput{Outcome: a.Outcome(), ErrorClass: a.ErrorClass(), BytesIn: a.BytesIn(), BytesOut: a.BytesOut(), UnobservedByteCounts: !a.ByteCountsKnown(), CompletedAt: a.CompletedAt()})
		if err != nil {
			return facts, a, err
		}
	}
	if a != expected {
		return facts, a, nativetool.ErrDenied
	}
	facts.OperationID, facts.AuthorityRevision, facts.OwnerEpoch, facts.BindingDigest = op.ID, uint64(revision), b.owner, b.digest
	return facts, a, nil
}

func (r *nativeToolRepository) OpenObservedStream(ctx context.Context, receipt nativetool.StartReceipt, frozen nativetool.Binding, lifetime time.Duration, now time.Time) (nativetool.StreamAuthority, error) {
	if !nativeTime(now) || lifetime <= 0 || lifetime > 30*time.Minute {
		return nativetool.StreamAuthority{}, nativetool.ErrInvalid
	}
	r.issuerMu.Lock()
	defer r.issuerMu.Unlock()
	// Retired/expired entries never renew. Their original audit is terminal, so
	// removing their bounded tombstone cannot enable another observation grant.
	for a, s := range r.streams {
		if s.terminal && !s.cleanupOnly && !s.expires.After(now) {
			// Logical terminal is not physical-audit completion. Preserve the
			// existing exact receipt cleanup owner even without live callbacks.
			r.invalidateNativeStream(a)
		}
	}
	if r.streamOpened == nil {
		r.streamOpened = make(map[nativetool.ID]time.Time)
	}
	for id, expires := range r.streamOpened {
		if !expires.After(now) {
			delete(r.streamOpened, id)
		}
	}
	if _, exists := r.streamOpened[receipt.OperationID()]; exists {
		return nativetool.StreamAuthority{}, nativetool.ErrReplay
	}
	if len(r.streamOpened) >= nativeStreamLimit {
		return nativetool.StreamAuthority{}, nativetool.ErrDenied
	}
	if len(r.streams) >= nativeStreamLimit {
		return nativetool.StreamAuthority{}, nativetool.ErrDenied
	}
	count := 0
	for _, s := range r.streams {
		if s.receipt.OperationID() == receipt.OperationID() {
			return nativetool.StreamAuthority{}, nativetool.ErrReplay
		}
		if s.session == frozen.SessionID() && !s.terminal {
			count++
		}
	}
	if count >= nativeSessionStreamLimit {
		return nativetool.StreamAuthority{}, nativetool.ErrDenied
	}
	var entry nativeObservedStream
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		op, err := loadNativeOperation(ctx, tx, receipt.OperationID())
		if err != nil {
			return err
		}
		if op.State != "started" || op.CompletedAt != nil {
			return nativetool.ErrDenied
		}
		b, scope, err := nativeObservationAuthority(ctx, tx, op, now)
		if err != nil {
			return err
		}
		if b.digest != frozen.Digest() {
			return nativetool.ErrDenied
		}
		facts, audit, err := nativeAttemptFacts(ctx, tx, op)
		if err != nil {
			return err
		}
		if audit.Terminal() || audit.StartedAt().After(now) || facts.AuthorityRevision != scope.revision {
			return nativetool.ErrDenied
		}
		if err := r.issuer.Check(receipt, facts); err != nil {
			return err
		}
		s, err := loadNativeSession(ctx, tx, b.session)
		if err != nil {
			return err
		}
		if lifetime > r.streamLifetime {
			lifetime = r.streamLifetime
		}
		expires := audit.StartedAt().Add(lifetime)
		if scope.expires.Before(expires) {
			expires = scope.expires
		}
		if s.expires.Before(expires) {
			expires = s.expires
		}
		if !expires.After(now) {
			return nativetool.ErrDenied
		}
		entry = nativeObservedStream{receipt: receipt, session: b.session, digest: b.digest, revision: op.Revision, expires: expires}
		entry.capture = op.Capture
		// Conservative tombstone survives rollback or ambiguous commit: the
		// same issued receipt cannot become a later observation grant.
		r.streamOpened[op.ID] = audit.StartedAt().Add(r.streamLifetime)
		return nil
	})
	if err != nil {
		return nativetool.StreamAuthority{}, err
	}
	a, err := r.issuer.IssueStream(entry.expires)
	if err != nil {
		return nativetool.StreamAuthority{}, err
	}
	r.streams[a] = &entry
	entry.timer = time.AfterFunc(time.Until(entry.expires), func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = r.RetireObservedStream(cleanup, a, time.Now().UTC())
	})
	return a, nil
}

func (r *nativeToolRepository) streamAuthority(ctx context.Context, tx *sql.Tx, a nativetool.StreamAuthority, now time.Time, terminal bool) (nativetool.Operation, error) {
	s := r.streams[a]
	if s == nil || s.cleanupOnly || !s.expires.After(now) || !terminal && s.terminal {
		return nativetool.Operation{}, nativetool.ErrDenied
	}
	op, err := loadNativeOperation(ctx, tx, s.receipt.OperationID())
	if err != nil {
		return op, err
	}
	getClosed := op.Kind == nativetool.RequestGetStream && (op.State == "completed" || op.State == "failed" && op.Reason == "transport_failed")
	if op.Revision != s.revision || op.BindingDigest == nil || *op.BindingDigest != s.digest || (!s.terminal && op.State != "started") || s.terminal && !getClosed && op.State != "completed" && !(op.State == "failed" && op.Reason == "rpc_error") {
		return op, nativetool.ErrDenied
	}
	b, scope, err := nativeObservationAuthority(ctx, tx, op, now)
	if err != nil {
		return op, err
	}
	facts, audit, err := nativeAttemptFacts(ctx, tx, op)
	if err != nil {
		return op, err
	}
	if b.session != s.session || scope.revision != facts.AuthorityRevision || audit.StartedAt().After(now) {
		return op, nativetool.ErrDenied
	}
	if audit.Terminal() && !(audit.Outcome() == egressaudit.OutcomeFailed && audit.ErrorClass() == "transport_failed") && !(audit.Outcome() == egressaudit.OutcomeCompleted && audit.ErrorClass() == "") {
		return op, nativetool.ErrDenied
	}
	return op, r.issuer.Check(s.receipt, facts)
}

func validNativeTerminal(outcome, reason string) bool {
	return outcome == "completed" && reason == "completed" || outcome == "failed" && (reason == "transport_failed" || reason == "rpc_error") || outcome == "canceled" && (reason == "canceled" || reason == "revoked") || outcome == "uncertain" && reason == "execution_uncertain"
}

// Observation preserves the original catalog snapshot across a live catalog
// notification. It rechecks current scope/assignment/permission, but cannot
// grant another send or relax the child's exact Start/Verify predicates.
func nativeObservationAuthority(ctx context.Context, tx *sql.Tx, op nativetool.Operation, now time.Time) (nativeBindingRow, nativeScope, error) {
	b, err := loadNativeBinding(ctx, tx, op.ID)
	if err != nil {
		return b, nativeScope{}, err
	}
	scope, err := nativeActiveScope(ctx, tx, op.Capture, b.owner, now)
	if err != nil {
		return b, scope, err
	}
	s, err := loadNativeSession(ctx, tx, b.session)
	if err != nil {
		return b, scope, err
	}
	if err := validateNativeSession(ctx, tx, s, now); err != nil {
		return b, scope, err
	}
	if err := nativeSessionCloseAuthority(ctx, tx, s, now); err != nil {
		return b, scope, err
	}
	if op.BindingDigest == nil || *op.BindingDigest != b.digest || s.capture != op.Capture || s.environment != op.EnvironmentID || s.environmentRevision != op.EnvironmentRevision || s.digest != op.EnvironmentDigest || s.assignment != b.assignment || s.owner != b.owner || s.route != b.route || s.routeRevision != b.routeRevision || s.origin != op.TargetOrigin || s.revision < b.sessionRevision {
		return b, scope, nativetool.ErrDenied
	}
	return b, scope, nil
}

func finishNativeLogical(ctx context.Context, tx *sql.Tx, op nativetool.Operation, in nativetool.LogicalFinishInput) error {
	if op.State != "started" {
		return nativetool.ErrConflict
	}
	if op.Kind != nativetool.RequestGetStream {
		if err := cancelNativeParentApprovals(ctx, tx, op.Capture, op.ID, in.ObservedAt); err != nil {
			return err
		}
	}
	if err := nativeTransition(ctx, tx, op, in.Outcome, op.DecisionSource, in.Reason, in.Outcome, in.ObservedAt); err != nil {
		return err
	}
	if in.Outcome == "uncertain" {
		_, err := tx.ExecContext(ctx, `UPDATE native_tool_scopes SET replay_barrier=1 WHERE capture_kind=? AND capture_id=?`, op.Capture.Kind, op.Capture.ID)
		return err
	}
	return nil
}

func (r *nativeToolRepository) FinishAttempt(ctx context.Context, receipt nativetool.StartReceipt, in nativetool.PhysicalFinishInput) error {
	if !nativeTime(in.ObservedAt) || !validNativeTerminal(in.Outcome, in.Reason) || in.Outcome == "uncertain" || in.Counts != nil && (in.Counts.BytesIn < 0 || in.Counts.BytesOut < 0) {
		return nativetool.ErrInvalid
	}
	r.issuerMu.Lock()
	defer r.issuerMu.Unlock()
	var closedGETRevision uint64
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		op, err := loadNativeOperation(ctx, tx, receipt.OperationID())
		if err != nil {
			return err
		}
		facts, audit, err := nativeAttemptFacts(ctx, tx, op)
		if err != nil {
			return err
		}
		if err := r.issuer.Check(receipt, facts); err != nil {
			return err
		}
		if audit.Terminal() {
			return nativetool.ErrConflict
		}
		// Logical completion may legitimately precede socket termination.
		if op.State != "started" && op.State != "completed" && !(op.State == "failed" && op.Reason == "rpc_error") {
			return nativetool.ErrDenied
		}
		counts := nativetool.ObservedCounts{}
		if in.Counts != nil {
			counts = *in.Counts
		}
		reason := in.Reason
		if in.Outcome == "completed" {
			reason = ""
		}
		terminal, err := audit.Finish(egressaudit.TerminalInput{Outcome: egressaudit.Outcome(in.Outcome), ErrorClass: reason, BytesIn: counts.BytesIn, BytesOut: counts.BytesOut, UnobservedByteCounts: in.Counts == nil, CompletedAt: in.ObservedAt})
		if err != nil {
			return err
		}
		if _, err := completeNativeDelivery(ctx, tx, terminal); err != nil {
			return err
		}
		var bytesIn, bytesOut any
		if in.Counts != nil {
			bytesIn, bytesOut = counts.BytesIn, counts.BytesOut
		}
		result, err := tx.ExecContext(ctx, `UPDATE native_tool_operations SET bytes_in=?,bytes_out=? WHERE operation_id=? AND revision=?`, bytesIn, bytesOut, op.ID, op.Revision)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return nativetool.ErrConflict
		}
		if op.Kind == nativetool.RequestGetStream {
			closedGETRevision = op.Revision + 1
			return finishNativeLogical(ctx, tx, op, nativetool.LogicalFinishInput{Outcome: in.Outcome, Reason: in.Reason, ObservedAt: in.ObservedAt})
		}
		return nil
	})
	if errors.Is(err, nativetool.ErrUnavailable) {
		r.dropNativeOperation(receipt.OperationID())
	}
	if err == nil {
		delete(r.recovery, receipt.OperationID())
		for _, s := range r.streams {
			if closedGETRevision != 0 && s.receipt.OperationID() == receipt.OperationID() {
				s.revision = closedGETRevision
				s.terminal = true
				delete(r.streamOpened, receipt.OperationID())
			}
		}
	}
	return err
}

func (r *nativeToolRepository) CompleteStreamRPC(ctx context.Context, authority nativetool.StreamAuthority, in nativetool.LogicalFinishInput) error {
	if !nativeTime(in.ObservedAt) || !validNativeTerminal(in.Outcome, in.Reason) {
		return nativetool.ErrInvalid
	}
	r.issuerMu.Lock()
	defer r.issuerMu.Unlock()
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		op, err := r.streamAuthority(ctx, tx, authority, in.ObservedAt, false)
		if err != nil {
			return err
		}
		if op.Kind == nativetool.RequestGetStream {
			return nativetool.ErrDenied
		}
		return finishNativeLogical(ctx, tx, op, in)
	})
	if err == nil {
		s := r.streams[authority]
		delete(r.streamOpened, s.receipt.OperationID())
		s.revision++
		s.terminal = true
		if in.Outcome != "completed" && !(in.Outcome == "failed" && in.Reason == "rpc_error") {
			r.invalidateNativeStream(authority)
		}
	} else if errors.Is(err, nativetool.ErrUnavailable) {
		r.invalidateNativeStream(authority)
	}
	return err
}

func (r *nativeToolRepository) FinishObservedRPC(ctx context.Context, receipt nativetool.StartReceipt, physical nativetool.PhysicalFinishInput, logical nativetool.LogicalFinishInput) error {
	known := logical.Outcome == "completed" && logical.Reason == "completed" || logical.Outcome == "failed" && logical.Reason == "rpc_error"
	if !known || !nativeTime(logical.ObservedAt) || !nativeTime(physical.ObservedAt) || logical.ObservedAt.After(physical.ObservedAt) || !validNativeTerminal(physical.Outcome, physical.Reason) || physical.Outcome == "uncertain" || physical.Counts != nil && (physical.Counts.BytesIn < 0 || physical.Counts.BytesOut < 0) {
		return nativetool.ErrInvalid
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
		switch op.Kind {
		case nativetool.RequestInitialize, nativetool.RequestPing, nativetool.RequestToolsList, nativetool.RequestToolsCall:
		default:
			return nativetool.ErrDenied
		}
		var method string
		if err := tx.QueryRowContext(ctx, `SELECT method FROM native_tool_operations WHERE operation_id=?`, op.ID).Scan(&method); err != nil {
			return err
		}
		if method != "POST" {
			return nativetool.ErrDenied
		}
		facts, audit, err := nativeAttemptFacts(ctx, tx, op)
		if err != nil {
			return err
		}
		if err := r.issuer.Check(receipt, facts); err != nil {
			return err
		}
		if audit.Terminal() || logical.ObservedAt.Before(audit.StartedAt()) {
			return nativetool.ErrDenied
		}
		counts := nativetool.ObservedCounts{}
		if physical.Counts != nil {
			counts = *physical.Counts
		}
		reason := physical.Reason
		if physical.Outcome == "completed" {
			reason = ""
		}
		terminal, err := audit.Finish(egressaudit.TerminalInput{Outcome: egressaudit.Outcome(physical.Outcome), ErrorClass: reason, BytesIn: counts.BytesIn, BytesOut: counts.BytesOut, UnobservedByteCounts: physical.Counts == nil, CompletedAt: physical.ObservedAt})
		if err != nil {
			return err
		}
		if _, err := completeNativeDelivery(ctx, tx, terminal); err != nil {
			return err
		}
		if err := finishNativeLogical(ctx, tx, op, logical); err != nil {
			return err
		}
		var in, out any
		if physical.Counts != nil {
			in, out = counts.BytesIn, counts.BytesOut
		}
		_, err = tx.ExecContext(ctx, `UPDATE native_tool_operations SET bytes_in=?,bytes_out=? WHERE operation_id=?`, in, out, op.ID)
		return err
	})
}

func (r *nativeToolRepository) ValidateLocalReplay(ctx context.Context, a nativetool.StreamAuthority, now time.Time) error {
	if !nativeTime(now) {
		return nativetool.ErrInvalid
	}
	r.issuerMu.Lock()
	defer r.issuerMu.Unlock()
	return r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		s := r.streams[a]
		if s == nil || !s.terminal {
			return nativetool.ErrDenied
		}
		_, err := r.streamAuthority(ctx, tx, a, now, true)
		return err
	})
}

func (r *nativeToolRepository) dropNativeStream(a nativetool.StreamAuthority) {
	if s := r.streams[a]; s != nil && s.timer != nil {
		s.timer.Stop()
	}
	delete(r.streams, a)
	for authority, c := range r.callbacks {
		if c.stream == a {
			delete(r.replies, c.child)
			delete(r.callbacks, authority)
		}
	}
}

func (r *nativeToolRepository) BindRecoveryGET(ctx context.Context, id nativetool.ID, b nativetool.Binding, a nativetool.StreamAuthority, now time.Time) error {
	resolved, resolvedErr := b.Plan().ResolvedEgress()
	if resolvedErr != nil {
		return nativetool.ErrInvalid
	}
	if !nativeTime(now) || !nativeID(id) || b.Kind() != nativetool.RequestGetStream {
		return nativetool.ErrInvalid
	}
	r.issuerMu.Lock()
	defer r.issuerMu.Unlock()
	if err := r.pruneTerminalRecovery(ctx); err != nil {
		return err
	}
	if len(r.recovery) >= nativeStreamLimit {
		return nativetool.ErrDenied
	}
	if _, exists := r.recovery[id]; exists {
		return nativetool.ErrReplay
	}
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		op, err := r.streamAuthority(ctx, tx, a, now, false)
		if err != nil {
			return err
		}
		s := r.streams[a]
		origin, err := loadNativeBinding(ctx, tx, op.ID)
		if err != nil {
			return err
		}
		if id == op.ID || b.SessionID() != s.session || b.Capture() != op.Capture || b.OwnerEpoch() != origin.owner || b.AssignmentRevision() != origin.assignment || b.Plan().EnvironmentDigest() != op.EnvironmentDigest || b.Plan().Route().ID.String() != origin.route || uint64(b.Plan().Route().Revision) != origin.routeRevision || origin.bindingVersion != 2 || origin.resolved != resolved {
			return nativetool.ErrDenied
		}
		var method string
		if err := tx.QueryRowContext(ctx, `SELECT method FROM native_tool_operations WHERE operation_id=?`, id).Scan(&method); err != nil {
			return err
		}
		if method != "GET" {
			return nativetool.ErrDenied
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, nativetool.ErrUnavailable) {
			r.invalidateNativeStream(a)
		}
		return err
	}
	// Publish the association before durable binding: an ambiguous Bind can
	// never leave a recovery child free to use the ordinary GET Start path.
	r.recovery[id] = nativeRecovery{stream: a, digest: b.Digest()}
	err = r.Bind(ctx, id, b, now)
	if errors.Is(err, nativetool.ErrUnavailable) {
		r.invalidateNativeStream(a)
	}
	return err
}

func (r *nativeToolRepository) verifyNativeRecovery(ctx context.Context, tx *sql.Tx, op nativetool.Operation, now time.Time) error {
	for _, stream := range r.streams {
		if stream.cleanupOnly && stream.receipt.OperationID() == op.ID {
			return nativetool.ErrDenied
		}
	}
	child, exists := r.recovery[op.ID]
	if !exists {
		return nil
	}
	if child.cleanupOnly {
		return nativetool.ErrDenied
	}
	if op.Kind != nativetool.RequestGetStream || op.BindingDigest == nil || *op.BindingDigest != child.digest {
		return nativetool.ErrDenied
	}
	_, err := r.streamAuthority(ctx, tx, child.stream, now, false)
	return err
}
func (r *nativeToolRepository) dropNativeOperation(id nativetool.ID) {
	for a, s := range r.streams {
		if s.receipt.OperationID() == id {
			r.invalidateNativeStream(a)
		}
	}
	if child, ok := r.recovery[id]; ok {
		child.cleanupOnly = true
		r.recovery[id] = child
		r.invalidateNativeStream(child.stream)
	}
}

// Cleanup-only entries retain no send, replay or observation authority. One
// timer per bounded registration backs off until exact durable cleanup wins.
func (r *nativeToolRepository) invalidateNativeStream(a nativetool.StreamAuthority) {
	s := r.streams[a]
	if s == nil {
		return
	}
	s.cleanupOnly = true
	if s.timer != nil {
		s.timer.Stop()
	}
	if s.retryDelay == 0 {
		s.retryDelay = 100 * time.Millisecond
	} else if s.retryDelay < 5*time.Second {
		s.retryDelay *= 2
		if s.retryDelay > 5*time.Second {
			s.retryDelay = 5 * time.Second
		}
	}
	s.timer = time.AfterFunc(s.retryDelay, func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = r.RetireObservedStream(ctx, a, time.Now().UTC())
	})
}

func (r *nativeToolRepository) RetireObservedStream(ctx context.Context, a nativetool.StreamAuthority, now time.Time) ([]nativetool.StartReceipt, error) {
	if !nativeTime(now) {
		return nil, nativetool.ErrInvalid
	}
	r.issuerMu.Lock()
	defer r.issuerMu.Unlock()
	s := r.streams[a]
	if s == nil {
		return nil, nil
	}
	s.cleanupOnly = true
	for _, c := range r.callbacks {
		if c.stream == a {
			c.retired = true
		}
	}
	// Every associated child must become durably terminal before its denying
	// association can disappear. Receipt-bearing attempts retain the reviewed
	// physical cleanup; pre-Start children close without creating an audit.
	for id, child := range r.recovery {
		if child.stream == a {
			child.cleanupOnly = true
			r.recovery[id] = child
		}
	}
	// Candidates remain private until the entire retirement transaction has
	// committed. An ambiguous commit must return no physical verification.
	var verified []nativetool.StartReceipt
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, c := range r.callbacks {
			if c.stream == a {
				if err := r.cleanupNativeCallback(ctx, tx, c, now); err != nil {
					return err
				}
				if c.receipt.OperationID() != "" {
					verified = append(verified, c.receipt)
				}
			}
		}
		for id, child := range r.recovery {
			if child.stream == a && child.cleanupOnly {
				if err := r.cleanupNativeRecoveryAttempt(ctx, tx, s, id, child, now); err != nil {
					return err
				}
				if child.receipt.OperationID() != "" {
					verified = append(verified, child.receipt)
				}
			}
		}
		op, err := loadNativeOperation(ctx, tx, s.receipt.OperationID())
		if err != nil {
			return err
		}
		facts, audit, err := nativeAttemptFacts(ctx, tx, op)
		if err != nil {
			return err
		}
		if err := r.issuer.Check(s.receipt, facts); err != nil {
			return err
		}
		verified = append(verified, s.receipt)
		if op.CompletedAt != nil {
			if !audit.Terminal() {
				// A known RPC terminal can precede an ambiguous physical close.
				// Finish only that exact audit; never relabel the logical result.
				terminal, err := audit.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeFailed, ErrorClass: "transport_failed", UnobservedByteCounts: true, CompletedAt: now})
				if err != nil {
					return err
				}
				if _, err := completeNativeDelivery(ctx, tx, terminal); err != nil {
					return err
				}
				_, err = tx.ExecContext(ctx, `UPDATE native_tool_operations SET bytes_in=NULL,bytes_out=NULL WHERE operation_id=? AND revision=?`, op.ID, op.Revision)
				return err
			}
			return nil
		}
		return finishNative(ctx, tx, op, nativetool.TerminalInput{Outcome: "uncertain", Reason: "execution_uncertain", ObservedAt: now})
	})
	if err == nil {
		delete(r.streamOpened, s.receipt.OperationID())
		for id, child := range r.recovery {
			if child.stream == a && child.cleanupOnly {
				delete(r.recovery, id)
			}
		}
		r.dropNativeStream(a)
		return verified, nil
	} else {
		r.invalidateNativeStream(a)
	}
	return nil, err
}

// Called in the same transaction as origin cleanup. Success of only one side
// cannot discard the timer while the other still has an ambiguous audit.
func (r *nativeToolRepository) cleanupNativeRecoveryAttempt(ctx context.Context, tx *sql.Tx, stream *nativeObservedStream, id nativetool.ID, child nativeRecovery, now time.Time) error {
	op, err := loadNativeOperation(ctx, tx, id)
	if err != nil {
		return err
	}
	if op.Kind != nativetool.RequestGetStream && op.Kind != nativetool.RequestServerReply || op.BindingDigest != nil && *op.BindingDigest != child.digest || op.Capture != stream.capture {
		return nativetool.ErrDenied
	}
	if child.receipt.OperationID() == "" {
		if op.CompletedAt != nil {
			return nil
		}
		if op.State == "started" {
			return finishNative(ctx, tx, op, nativetool.TerminalInput{Outcome: "uncertain", Reason: "execution_uncertain", ObservedAt: now})
		}
		if op.State != "received" && op.State != "validated" {
			return nativetool.ErrDenied
		}
		var claims, audits int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, id).Scan(&claims); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_deliveries WHERE operation_id=?`, id).Scan(&audits); err != nil {
			return err
		}
		if claims != 0 || audits != 0 {
			return nativetool.ErrInvalid
		}
		if err := nativeTransition(ctx, tx, op, "canceled", op.DecisionSource, "revoked", "revoked", now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE native_tool_operations SET bytes_in=0,bytes_out=0 WHERE operation_id=?`, id)
		return err
	}
	if child.receipt.OperationID() != id || op.BindingDigest == nil {
		return nativetool.ErrDenied
	}
	b, err := loadNativeBinding(ctx, tx, op.ID)
	if err != nil {
		return err
	}
	if b.session != stream.session {
		return nativetool.ErrDenied
	}
	facts, audit, err := nativeAttemptFacts(ctx, tx, op)
	if err != nil {
		return err
	}
	if err := r.issuer.Check(child.receipt, facts); err != nil {
		return err
	}
	if audit.Terminal() {
		if op.CompletedAt == nil {
			return nativetool.ErrInvalid
		}
		return nil
	}
	if op.State != "started" || op.CompletedAt != nil {
		return nativetool.ErrInvalid
	}
	outcome, reason := "failed", "transport_failed"
	if op.Kind == nativetool.RequestServerReply {
		outcome, reason = "uncertain", "execution_uncertain"
	}
	terminal, err := audit.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeFailed, ErrorClass: reason, UnobservedByteCounts: true, CompletedAt: now})
	if err != nil {
		return err
	}
	if _, err := completeNativeDelivery(ctx, tx, terminal); err != nil {
		return err
	}
	if err := finishNativeLogical(ctx, tx, op, nativetool.LogicalFinishInput{Outcome: outcome, Reason: reason, ObservedAt: now}); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE native_tool_operations SET bytes_in=NULL,bytes_out=NULL WHERE operation_id=?`, op.ID)
	return err
}

// issuerMu must be held. Known terminal rows cannot pass ordinary Start, so
// their process-local association can be released after a successful read Tx.
// An ambiguous read/commit or still-startable row preserves its denying entry.
func (r *nativeToolRepository) pruneTerminalRecovery(ctx context.Context) error {
	if len(r.recovery) == 0 {
		return nil
	}
	var terminal []nativetool.ID
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for id, child := range r.recovery {
			op, err := loadNativeOperation(ctx, tx, id)
			if err != nil {
				return err
			}
			if op.CompletedAt == nil {
				continue
			}
			var once int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, id).Scan(&once); err != nil {
				return err
			}
			if once != 0 {
				facts, audit, err := nativeAttemptFacts(ctx, tx, op)
				if err != nil {
					return err
				}
				if !audit.Terminal() {
					continue
				}
				if child.receipt.OperationID() != "" {
					if err := r.issuer.Check(child.receipt, facts); err != nil {
						return err
					}
				}
			}
			terminal = append(terminal, id)
		}
		return nil
	})
	if err == nil {
		for _, id := range terminal {
			delete(r.recovery, id)
		}
	}
	return err
}
