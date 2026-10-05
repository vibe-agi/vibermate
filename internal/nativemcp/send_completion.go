package nativemcp

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// Only Serve's own successful Start plus first exclusive unsent claim selects
// this closure. Historical metadata grants neither another send nor audit drain.
func (e *executor) finishKnownUnsent(id nativetool.ID, s *session, w http.ResponseWriter, cause error, r rpc, b nativetool.Binding, receipt nativetool.StartReceipt) Terminal {
	op, err := e.unsentOperation(id, s, b)
	if err != nil {
		return e.unsentStorageFailure(id, s, r, w)
	}
	if op.CompletedAt != nil {
		return e.preserveUnsentTerminal(op, s, r, w)
	}
	if op.State != "started" || op.BytesIn != nil || op.BytesOut != nil {
		return e.unsentStorageFailure(id, s, r, w)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	if r.stream != nil {
		err = e.dependencies.Store.FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "failed", Reason: "transport_failed", Counts: &nativetool.ObservedCounts{}, ObservedAt: e.dependencies.Clock().UTC()})
	} else {
		err = e.dependencies.Store.Finish(ctx, id, nativetool.TerminalInput{Outcome: "failed", Reason: "transport_failed", BytesIn: 0, BytesOut: 0, ObservedAt: e.dependencies.Clock().UTC()})
	}
	cancel()
	if errors.Is(err, nativetool.ErrConflict) {
		// An independent lifecycle actor may have won after our open-row read.
		// Re-read exact history; never retry Finish or overwrite its outcome.
		if final, readErr := e.unsentOperation(id, s, b); readErr == nil && final.CompletedAt != nil {
			return e.preserveUnsentTerminal(final, s, r, w)
		}
	}
	if err != nil || e.cleanupUnsentRequest(id, s, r) != nil {
		return e.unsentStorageFailure(id, s, r, w)
	}
	if r.stream != nil {
		return e.recoveryFailureTerminal(id, s, r.stream)
	}
	return unsentTerminal(w, Terminal{OperationID: id, Outcome: OutcomeFailed, Reason: reasonFor(cause)})
}

func (e *executor) unsentOperation(id nativetool.ID, s *session, b nativetool.Binding) (nativetool.Operation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	op, err := e.dependencies.Store.Get(ctx, id)
	p := b.Plan()
	if err == nil && (op.ID != id || op.Capture != s.owner.capture || op.Capture != b.Capture() || op.Kind != b.Kind() || op.BindingDigest == nil || *op.BindingDigest != b.Digest() || op.EnvironmentID != p.EnvironmentID() || op.EnvironmentRevision != p.EnvironmentRevision() || op.EnvironmentDigest != p.EnvironmentDigest() || op.Recording != p.ContentRecording() || op.TargetOrigin != p.Route().TargetOrigin.String()) {
		err = nativetool.ErrDenied
	}
	return op, err
}

func (e *executor) cleanupUnsentRequest(id nativetool.ID, s *session, r rpc) error {
	if r.kind == nativetool.RequestInitialize || r.kind == nativetool.RequestInitialized {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return e.closeSession(ctx, s)
	}
	if r.kind == nativetool.RequestServerReply {
		return e.retireFailedReplyParent(s, string(r.id), id)
	}
	return nil
}

func (e *executor) preserveUnsentTerminal(op nativetool.Operation, s *session, r rpc, w http.ResponseWriter) Terminal {
	terminal := Terminal{OperationID: op.ID, Outcome: Outcome(op.State), Reason: ReasonExecutionUncertain}
	valid := op.CompletedAt != nil && !op.CompletedAt.Before(op.CreatedAt) && (op.BytesIn == nil) == (op.BytesOut == nil)
	switch op.State {
	case "uncertain":
		valid = valid && op.Reason == "execution_uncertain" && op.BytesIn == nil
	case "canceled":
		valid = valid && (op.Reason == "canceled" || op.Reason == "revoked") && op.BytesIn != nil
		terminal.Reason = ReasonRevoked
	case "failed":
		valid = valid && (op.Reason == "transport_failed" || op.Reason == "rpc_error")
	default:
		// A stored logical success cannot supply a missing upstream response.
		valid = false
	}
	if op.BytesIn != nil && op.BytesOut != nil {
		valid = valid && *op.BytesIn >= 0 && *op.BytesOut >= 0
		terminal.BytesIn, terminal.BytesOut = *op.BytesIn, *op.BytesOut
	}
	if !valid || e.cleanupUnsentRequest(op.ID, s, r) != nil {
		return e.unsentStorageFailure(op.ID, s, r, w)
	}
	// No physical association is removed from historical metadata here.
	// The Store's receipt-checked retirement/Close/Recover obligations remain.
	return unsentTerminal(w, terminal)
}

func (e *executor) unsentStorageFailure(id nativetool.ID, s *session, r rpc, w http.ResponseWriter) Terminal {
	if r.stream != nil {
		e.retireStream(s, r.stream)
	} else {
		// A commit error is not proof of rollback. Fence before further work,
		// using a new budget even if Finish already exhausted its context.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = e.revokeOwner(ctx, s.owner.capture, s.owner, RevocationOwnerChanged)
		cancel()
	}
	return unsentTerminal(w, Terminal{OperationID: id, Outcome: OutcomeUncertain, Reason: ReasonStorageUnavailable})
}

func unsentTerminal(w http.ResponseWriter, terminal Terminal) Terminal {
	if w != nil {
		reject(w, terminal.OperationID, terminal.Reason)
	}
	return terminal
}

func knownUnsentKind(kind nativetool.RequestKind) bool {
	switch kind {
	case nativetool.RequestGetStream, nativetool.RequestToolsCall,
		nativetool.RequestPing, nativetool.RequestCancel, nativetool.RequestDeleteSession,
		nativetool.RequestToolsList, nativetool.RequestInitialize, nativetool.RequestInitialized, nativetool.RequestServerReply:
		return true
	default:
		return false
	}
}
