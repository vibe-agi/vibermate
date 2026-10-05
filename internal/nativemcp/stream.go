package nativemcp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/vibe-agi/vibermate/internal/nativetool"
)

var errStreamDetached = errors.New("native stream detached")
var errStreamDelivery = errors.New("native complete event delivery interrupted")

func (e *executor) finishRecoveryTransportFailure(c CapturedRequest, s *session, stream *streamRecord, receipt nativetool.StartReceipt, counts *nativetool.ObservedCounts, claim nativeClaimHandle) Terminal {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := e.dependencies.Store.FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "failed", Reason: "transport_failed", Counts: counts, ObservedAt: e.dependencies.Clock().UTC()})
	if err == nil {
		e.notePhysicalTerminal(claim)
	}
	if err != nil {
		e.retireStream(s, stream)
		return Terminal{OperationID: c.id, Outcome: OutcomeUncertain, Reason: ReasonStorageUnavailable}
	}
	return e.recoveryFailureTerminal(c.id, s, stream)
}

func (e *executor) recoveryFailureTerminal(id nativetool.ID, s *session, stream *streamRecord) Terminal {
	s.mu.Lock()
	live := !stream.retired && stream.context.Err() == nil && !stream.protocolCanceled
	s.mu.Unlock()
	if !live {
		e.retireStream(s, stream)
		return Terminal{OperationID: id, Outcome: OutcomeUncertain, Reason: ReasonExecutionUncertain}
	}
	return Terminal{OperationID: id, Outcome: OutcomeFailed, Reason: ReasonStreamDetached}
}

// A server event ID names one private stream and one immutable ordinal. The
// wire ID is never persisted and never supplies repository authority.
type streamEvent struct {
	id   string
	wire []byte
}
type streamCursor struct {
	stream  *streamRecord
	ordinal int
}
type streamRecord struct {
	claim                       nativeClaimHandle
	operation                   nativetool.ID
	binding                     nativetool.Binding
	authority                   nativetool.StreamAuthority
	receipt                     nativetool.StartReceipt
	observed                    bool
	request                     rpc
	context                     context.Context
	cancel                      context.CancelFunc
	expiryTimer                 *time.Timer
	expires                     time.Time
	credentialRevision          uint64
	attached, terminal, retired bool
	terminalCursor              bool
	knownTerminal               *nativetool.LogicalFinishInput
	protocolCanceled            bool
	unidentifiable              bool
	delivered                   bool
	created                     time.Time
	events                      []streamEvent
	charged                     int64
}

func (e *executor) openStream(ctx context.Context, c CapturedRequest, r rpc, s *session, b nativetool.Binding, receipt nativetool.StartReceipt, started time.Time, claim nativeClaimHandle) (*streamRecord, error) {
	e.reclaimDeliveredStreams(s)
	s.mu.Lock()
	defer s.mu.Unlock()
	active := 0
	for _, stream := range s.streams {
		if !stream.terminal && !stream.retired {
			active++
		}
	}
	if s.closed || active >= e.limits.ConcurrentRPCsPerSession || len(s.streams) >= e.limits.Pending {
		return nil, errBudget
	}
	cost := int64(2048) + 2*jsonAllocation(r.raw)
	if !e.budget.acquire(cost) {
		return nil, errBudget
	}
	expires := started.Add(e.limits.CallLifetime)
	var err error
	cloned := r
	if len(r.raw) > 0 {
		cloned, err = parseRequest(bytes.Clone(r.raw), e.limits)
		if err != nil {
			e.budget.release(cost)
			return nil, err
		}
		cloned.failure, cloned.renewalUnavailable, cloned.retention, cloned.collection, cloned.collectionToken = r.failure, r.renewalUnavailable, r.retention, r.collection, r.collectionToken
	}
	logical, cancel := context.WithCancel(s.context)
	stream := &streamRecord{claim: claim, operation: c.id, binding: b, receipt: receipt, request: cloned, context: logical, cancel: cancel, expires: expires, credentialRevision: c.admission.CredentialRevision(), attached: true, charged: cost}
	stream.created = started
	stream.expiryTimer = time.AfterFunc(time.Until(expires), cancel)
	s.streams[c.id] = stream
	s.retained += cost
	context.AfterFunc(logical, func() { e.retireStream(s, stream) })
	return stream, nil
}

// Successful local delivery permits bounded cache reclamation under pressure;
// it is not a client acknowledgement. Failed/unknown delivery stays pinned.
func (e *executor) reclaimDeliveredStreams(s *session) {
	for {
		s.mu.Lock()
		if len(s.streams) < e.limits.Pending && len(s.eventIndex) < e.limits.Pending {
			s.mu.Unlock()
			return
		}
		var oldest *streamRecord
		for _, candidate := range s.streams {
			if !candidate.terminal || !candidate.delivered || candidate.attached || candidate.retired {
				continue
			}
			pending := false
			for _, parent := range s.parents {
				if parent.operation == candidate.operation {
					pending = true
					break
				}
			}
			for _, operation := range s.elicitations {
				if operation == candidate.operation {
					pending = true
					break
				}
			}
			if pending {
				continue
			}
			if oldest == nil || candidate.created.Before(oldest.created) || candidate.created.Equal(oldest.created) && candidate.operation < oldest.operation {
				oldest = candidate
			}
		}
		if oldest == nil {
			s.mu.Unlock()
			return
		}
		// Pin the choice against a simultaneous resume while retirement runs.
		oldest.attached = true
		s.mu.Unlock()
		e.retireStream(s, oldest)
		e.detachStream(s, oldest)
	}
}

// An attached no-cursor response retains the ordinary verified-send completion
// path. Observation authority is required before allocating a recovery cursor
// or publishing a callback; any issuance failure remains fail-closed.
func (e *executor) observeStream(ctx context.Context, s *session, stream *streamRecord) error {
	// Classification precedes issuance and takes e.mu before any s.mu.
	e.observeClaim(stream.claim)
	s.mu.Lock()
	defer s.mu.Unlock()
	if stream.retired {
		return errSession
	}
	if stream.observed {
		return nil
	}
	a, err := e.dependencies.Store.OpenObservedStream(ctx, stream.receipt, stream.binding, e.limits.CallLifetime, e.dependencies.Clock().UTC())
	if err != nil {
		return err
	}
	effective := a.ExpiresAt()
	if effective.IsZero() {
		return errSession
	}
	stream.authority = a
	stream.observed = true
	if s.timeLimit.Before(effective) {
		effective = s.timeLimit
	}
	if effective.Before(stream.expires) {
		stream.expires = effective
		stream.expiryTimer.Reset(time.Until(effective))
	}
	if active := s.active[string(stream.request.id)]; active != nil {
		active.cancel = stream.cancel
		active.stream = stream
	}
	return nil
}

func (e *executor) attachStream(c CapturedRequest, s *session, cursor string) (*streamRecord, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	indexed, ok := s.eventIndex[cursor]
	if !ok {
		return nil, 0, errSession
	}
	stream := indexed.stream
	if s.closed || s.renewalFailed || stream.retired || stream.attached || stream.context.Err() != nil || !stream.expires.After(e.dependencies.Clock().UTC()) || stream.credentialRevision != c.admission.CredentialRevision() {
		return nil, 0, errSession
	}
	stream.attached = true
	return stream, indexed.ordinal, nil
}

func (e *executor) detachStream(s *session, stream *streamRecord) {
	s.mu.Lock()
	stream.attached = false
	if !stream.observed {
		stream.retired = true
		stream.expiryTimer.Stop()
		stream.cancel()
	}
	if stream.retired {
		e.releaseStreamLocked(s, stream)
	}
	if s.closed && len(s.active) == 0 {
		e.clearSession(s)
	}
	s.mu.Unlock()
	e.retireResolvedURLStream(s, stream.operation)
}

func (e *executor) releaseStreamLocked(s *session, stream *streamRecord) {
	if stream.attached || stream.charged == 0 {
		return
	}
	for _, event := range stream.events {
		delete(s.eventIndex, event.id)
	}
	delete(s.streams, stream.operation)
	s.retained -= stream.charged
	e.budget.release(stream.charged)
	stream.charged = 0
	stream.events = nil
	stream.request = rpc{}
}

func (e *executor) retireStream(s *session, stream *streamRecord) {
	s.mu.Lock()
	if stream.retired {
		s.mu.Unlock()
		return
	}
	stream.retired = true
	stream.expiryTimer.Stop()
	stream.cancel()
	key := string(stream.request.id)
	if active := s.active[key]; active != nil && active.stream == stream {
		delete(s.active, key)
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if stream.observed {
		claims := e.snapshotRetirementClaims(s.owner.claimKey, s.claimKey, "")
		proof, err := e.dependencies.Store.RetireObservedStream(ctx, stream.authority, e.dependencies.Clock().UTC())
		if err != nil {
			// Durable cleanup keeps its exact bounded retry obligation. Fence
			// the process owner immediately even while SQL is unavailable.
			e.mu.Lock()
			s.owner.revoked = true
			s.owner.cancel()
			e.mu.Unlock()
		} else {
			e.noteRetirementProof(claims, proof, s.owner.claimKey, s.claimKey)
		}
	}
	e.retireCompletedParents(ctx, s, stream.operation)
	s.mu.Lock()
	e.retireStreamToolLocked(s, stream.operation)
	e.releaseStreamLocked(s, stream)
	s.mu.Unlock()
}

func (e *executor) retireStreamToolLocked(s *session, id nativetool.ID) {
	if witness := s.tools[id]; witness != nil && s.hasher != nil {
		_ = s.hasher.RetireToolCall(*witness)
		delete(s.tools, id)
		s.retained -= 2048
		e.budget.release(2048)
	}
}

// Completion settles a detached no-cursor GET's remaining external workflow.
// It cannot retire an attached stream, recoverable bytes or another callback.
func (e *executor) retireResolvedURLStream(s *session, operation nativetool.ID) {
	s.mu.Lock()
	stream := s.streams[operation]
	if stream == nil || stream.binding.Kind() != nativetool.RequestGetStream || !stream.terminal || stream.attached || stream.retired || len(stream.events) != 0 {
		s.mu.Unlock()
		return
	}
	for _, parent := range s.parents {
		if parent.operation == operation {
			s.mu.Unlock()
			return
		}
	}
	for _, owner := range s.elicitations {
		if owner == operation {
			s.mu.Unlock()
			return
		}
	}
	s.mu.Unlock()
	e.retireStream(s, stream)
}

func (e *executor) replayTerminal(ctx context.Context, c CapturedRequest, s *session, stream *streamRecord, ordinal int, w http.ResponseWriter) Terminal {
	if c.admission.Validate() != nil || ctx.Err() != nil {
		return reject(w, c.id, ReasonRevoked)
	}
	if err := e.dependencies.Store.ValidateLocalReplay(ctx, stream.authority, e.dependencies.Clock().UTC()); err != nil {
		return reject(w, c.id, reasonFor(err))
	}
	s.mu.Lock()
	if !stream.terminal || !stream.terminalCursor || stream.retired || !stream.attached || ordinal >= len(stream.events)-1 {
		s.mu.Unlock()
		return reject(w, c.id, ReasonSessionInvalid)
	}
	// Exclusive attachment pins the charged ledger until detach; cancellation
	// can fence it but cannot release its wire while this handler reads it.
	events := stream.events[ordinal+1:]
	stream.delivered = false
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, event := range events {
		if ctx.Err() != nil || stream.context.Err() != nil {
			return Terminal{OperationID: stream.operation, Outcome: OutcomeFailed, Reason: ReasonRevoked}
		}
		if n, err := w.Write(event.wire); err != nil || n != len(event.wire) {
			return Terminal{OperationID: stream.operation, Outcome: OutcomeFailed, Reason: ReasonStreamDetached}
		}
		if err := flushStream(w); err != nil {
			return Terminal{OperationID: stream.operation, Outcome: OutcomeFailed, Reason: ReasonStreamDetached}
		}
	}
	s.mu.Lock()
	stream.delivered = true
	s.mu.Unlock()
	return Terminal{OperationID: stream.operation, Outcome: OutcomeCompleted}
}

func flushStream(w http.ResponseWriter) error {
	if tracked, ok := w.(*nativeWriter); ok {
		w = tracked.ResponseWriter
	}
	return http.NewResponseController(w).Flush()
}

func (e *executor) finishStreamAttempt(c CapturedRequest, r rpc, s *session, receipt nativetool.StartReceipt, counted *countedResponseBody, w http.ResponseWriter, cause error, claim nativeClaimHandle) Terminal {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream := r.stream
	s.mu.Lock()
	observed := stream.observed
	known := stream.knownTerminal
	s.mu.Unlock()
	if !observed {
		if known != nil && errors.Is(cause, errStreamDelivery) {
			err := e.dependencies.Store.FinishObservedRPC(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "failed", Reason: "transport_failed", ObservedAt: e.dependencies.Clock().UTC()}, *known)
			if err != nil {
				return e.finishFailure(c.id, s, w, err, true, claim)
			}
			e.notePhysicalTerminal(claim)
			e.retireCompletedParents(ctx, s, c.id)
			return Terminal{OperationID: c.id, Outcome: Outcome(known.Outcome)}
		}
		if cause != nil {
			return e.finishFailure(c.id, s, w, cause, true, claim)
		}
		outcome, reason := "completed", "completed"
		result := OutcomeCompleted
		if r.failure != nil && *r.failure {
			outcome, reason = "failed", "rpc_error"
			result = OutcomeFailed
		}
		if err := e.dependencies.Store.Finish(ctx, c.id, nativetool.TerminalInput{Outcome: outcome, Reason: reason, BytesIn: counted.bytes.Load(), BytesOut: int64(len(r.raw)), ObservedAt: e.dependencies.Clock().UTC()}); err != nil {
			return e.finishFailure(c.id, s, w, err, true, claim)
		}
		e.notePhysicalTerminal(claim)
		return Terminal{OperationID: c.id, Outcome: result, BytesIn: counted.bytes.Load(), BytesOut: int64(len(r.raw))}
	}
	state, reason := "completed", "completed"
	if cause != nil {
		state, reason = "failed", "transport_failed"
	}
	// The transport interface does not report written request bytes. On a
	// broken exchange both directions remain unknown as an atomic pair.
	var counts *nativetool.ObservedCounts
	if cause == nil {
		counts = &nativetool.ObservedCounts{BytesIn: counted.bytes.Load(), BytesOut: int64(len(r.raw))}
	}
	err := e.dependencies.Store.FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: state, Reason: reason, Counts: counts, ObservedAt: e.dependencies.Clock().UTC()})
	if err == nil {
		// This physical response may belong to a recovery child. Its exact
		// handle is not interchangeable with the original stream's handle.
		e.notePhysicalTerminal(claim)
	}
	s.mu.Lock()
	terminal := stream.terminal
	getCallbacks := false
	for _, parent := range s.parents {
		if parent.operation == stream.operation && !parent.consumed {
			getCallbacks = true
			break
		}
	}
	for _, operation := range s.elicitations {
		if operation == stream.operation {
			getCallbacks = true
			break
		}
	}
	resumable := len(stream.events) > 0 && !stream.unidentifiable && !stream.retired && !stream.protocolCanceled && stream.context.Err() == nil
	getCallbacks = getCallbacks && stream.binding.Kind() == nativetool.RequestGetStream && !stream.retired && !stream.protocolCanceled && stream.context.Err() == nil
	if stream.binding.Kind() == nativetool.RequestGetStream && (cause == nil || getCallbacks && (errors.Is(cause, errStreamDetached) || errors.Is(cause, errStreamDelivery))) {
		stream.terminal = true
		stream.delivered = cause == nil
	}
	s.mu.Unlock()
	if err != nil {
		e.retireStream(s, stream)
		return Terminal{OperationID: c.id, Outcome: OutcomeUncertain, Reason: ReasonStorageUnavailable}
	}
	if getCallbacks && (errors.Is(cause, errStreamDetached) || errors.Is(cause, errStreamDelivery)) {
		return Terminal{OperationID: c.id, Outcome: OutcomeFailed, Reason: ReasonStreamDetached}
	}
	if terminal {
		s.mu.Lock()
		retain := stream.terminalCursor
		s.mu.Unlock()
		if !retain || errors.Is(cause, errProtocol) || errors.Is(cause, errSession) || errors.Is(cause, errBudget) {
			e.retireStream(s, stream)
		}
		return Terminal{OperationID: c.id, Outcome: OutcomeCompleted, BytesIn: counted.bytes.Load(), BytesOut: int64(len(r.raw))}
	}
	if (errors.Is(cause, errStreamDetached) || errors.Is(cause, errStreamDelivery)) && resumable {
		return Terminal{OperationID: c.id, Outcome: OutcomeFailed, Reason: ReasonStreamDetached}
	}
	if r.kind == nativetool.RequestGetStream && cause == nil {
		return Terminal{OperationID: c.id, Outcome: OutcomeCompleted}
	}
	e.retireStream(s, stream)
	s.mu.Lock()
	exact := s.closed || stream.protocolCanceled
	for _, active := range s.active {
		if active.operation == stream.operation && active.protocolCanceled {
			exact = true
		}
	}
	s.mu.Unlock()
	if !exact {
		_ = e.Revoke(ctx, s.owner.capture, RevocationOwnerChanged)
	}
	if w != nil {
		reject(w, c.id, ReasonExecutionUncertain)
	}
	return Terminal{OperationID: c.id, Outcome: OutcomeUncertain, Reason: ReasonExecutionUncertain}
}
